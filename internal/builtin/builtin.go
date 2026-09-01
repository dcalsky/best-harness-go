// Package builtin contains explicitly registered file and shell tools.
package builtin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dcalsky/best-harness-go/internal/fff"
	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/tool"
)

type FileSystem interface {
	ReadFile(string) ([]byte, error)
	WriteFile(string, []byte, fs.FileMode) error
	MkdirAll(string, fs.FileMode) error
	ReadDir(string) ([]os.DirEntry, error)
	Stat(string) (fs.FileInfo, error)
	WalkDir(string, fs.WalkDirFunc) error
}
type OSFileSystem struct{}

func (OSFileSystem) ReadFile(p string) ([]byte, error)                 { return os.ReadFile(p) }
func (OSFileSystem) WriteFile(p string, b []byte, m fs.FileMode) error { return os.WriteFile(p, b, m) }
func (OSFileSystem) MkdirAll(p string, m fs.FileMode) error            { return os.MkdirAll(p, m) }
func (OSFileSystem) ReadDir(p string) ([]os.DirEntry, error)           { return os.ReadDir(p) }
func (OSFileSystem) Stat(p string) (fs.FileInfo, error)                { return os.Stat(p) }
func (OSFileSystem) WalkDir(p string, fn fs.WalkDirFunc) error         { return filepath.WalkDir(p, fn) }

type OutputStore interface {
	Save(context.Context, string, []byte) (string, error)
}
type Config struct {
	Cwd            string
	FileSystem     FileSystem
	Shell          ShellExecutor
	OutputStore    OutputStore
	MaxOutputBytes int
	MutationQueue  *MutationQueue
	// Search overrides the native FFF backend. It is primarily useful for
	// deterministic tests and embedded callers with their own FFF lifecycle.
	Search fff.Searcher
	// FFFLibraryPath is an optional absolute path to the FFF C library. When
	// empty, the pinned prebuilt release is downloaded and verified.
	FFFLibraryPath string
	// FFFScanTimeout bounds the first-call wait for FFF's initial index scan.
	FFFScanTimeout time.Duration
	// FFFCacheDir overrides the cache used for the pinned prebuilt FFF library.
	FFFCacheDir string
	// FFFMaxRoots bounds the number of live indexed roots and file watchers.
	FFFMaxRoots int
}

func (c Config) defaults() Config {
	if c.FileSystem == nil {
		c.FileSystem = OSFileSystem{}
	}
	if c.Shell == nil {
		c.Shell = OSShellExecutor{}
	}
	if c.MaxOutputBytes <= 0 {
		c.MaxOutputBytes = 64 << 10
	}
	if c.MutationQueue == nil {
		c.MutationQueue = NewMutationQueue()
	}
	if c.FFFLibraryPath == "" {
		c.FFFLibraryPath = os.Getenv("BEST_HARNESS_FFF_LIBRARY")
	}
	if c.FFFCacheDir == "" {
		c.FFFCacheDir = os.Getenv("BEST_HARNESS_FFF_CACHE_DIR")
	}
	return c
}
func resolve(c Config, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(c.Cwd, p)
}

func searcher(c Config) (fff.Searcher, error) {
	if c.Search != nil {
		return c.Search, nil
	}
	switch c.FileSystem.(type) {
	case OSFileSystem, *OSFileSystem:
	default:
		return nil, errors.New("FFF-backed find and grep require OSFileSystem; provide Config.Search for a custom filesystem")
	}
	return fff.NewPool(fff.Options{
		LibraryPath: c.FFFLibraryPath,
		CacheDir:    c.FFFCacheDir,
		ScanTimeout: c.FFFScanTimeout,
		MaxRoots:    c.FFFMaxRoots,
	}), nil
}

func prepareSearch(c Config) (Config, *fff.Pool, error) {
	c = c.defaults()
	if c.Search != nil {
		return c, nil, nil
	}
	engine, err := searcher(c)
	if err != nil {
		return Config{}, nil, err
	}
	pool := engine.(*fff.Pool)
	c.Search = pool
	return c, pool, nil
}

type Truncation struct {
	Truncated     bool   `json:"truncated"`
	OriginalBytes int    `json:"originalBytes"`
	StoredAt      string `json:"storedAt,omitempty"`
}

func truncate(ctx context.Context, c Config, name string, b []byte) (string, Truncation) {
	d := Truncation{OriginalBytes: len(b)}
	if len(b) <= c.MaxOutputBytes {
		return string(b), d
	}
	d.Truncated = true
	if c.OutputStore != nil {
		d.StoredAt, _ = c.OutputStore.Save(ctx, name, b)
	}
	marker := []byte("\n... output truncated ...\n")
	if c.MaxOutputBytes <= len(marker) {
		return string(marker[:c.MaxOutputBytes]), d
	}
	budget := c.MaxOutputBytes - len(marker)
	head := budget / 2
	tailStart := len(b) - (budget - head)
	for head > 0 && head < len(b) && b[head]&0xc0 == 0x80 {
		head--
	}
	for tailStart < len(b) && b[tailStart]&0xc0 == 0x80 {
		tailStart++
	}
	out := append(append([]byte(nil), b[:head]...), marker...)
	out = append(out, b[tailStart:]...)
	return string(out), d
}

type ReadParams struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}
type ReadDetails struct {
	Path  string `json:"path"`
	Lines int    `json:"lines"`
	Truncation
}

func Read(c Config) tool.Tool[ReadParams, ReadDetails] {
	c = c.defaults()
	return tool.Tool[ReadParams, ReadDetails]{Name: "read", Description: "Read a text file or return an image.", ExecutionMode: tool.Parallel, Execute: func(ctx context.Context, _ tool.ToolCall, p ReadParams, _ tool.Update[ReadDetails]) (tool.ToolResult[ReadDetails], error) {
		if err := ctx.Err(); err != nil {
			return tool.ToolResult[ReadDetails]{}, err
		}
		path := resolve(c, p.Path)
		b, err := c.FileSystem.ReadFile(path)
		if err != nil {
			return tool.ToolResult[ReadDetails]{}, err
		}
		mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
		if strings.HasPrefix(mimeType, "image/") {
			return tool.ToolResult[ReadDetails]{Content: []message.Content{message.Text("Read image file [" + mimeType + "]"), message.Image(base64.StdEncoding.EncodeToString(b), mimeType)}, Details: ReadDetails{Path: path}}, nil
		}
		lines := strings.Split(string(b), "\n")
		start := 0
		if p.Offset > 0 {
			start = p.Offset - 1
		}
		if start >= len(lines) {
			return tool.ToolResult[ReadDetails]{}, fmt.Errorf("offset %d is beyond end of file (%d lines total)", p.Offset, len(lines))
		}
		end := len(lines)
		if p.Limit > 0 && start+p.Limit < end {
			end = start + p.Limit
		}
		text, tr := truncate(ctx, c, "read-"+filepath.Base(path), []byte(strings.Join(lines[start:end], "\n")))
		return tool.ToolResult[ReadDetails]{Content: []message.Content{message.Text(text)}, Details: ReadDetails{Path: path, Lines: end - start, Truncation: tr}}, nil
	}}
}

type WriteParams struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
type WriteDetails struct {
	Path  string
	Bytes int
}

func Write(c Config) tool.Tool[WriteParams, WriteDetails] {
	c = c.defaults()
	return tool.Tool[WriteParams, WriteDetails]{Name: "write", Description: "Write the complete contents of a file.", ExecutionMode: tool.Parallel, Execute: func(ctx context.Context, _ tool.ToolCall, p WriteParams, _ tool.Update[WriteDetails]) (tool.ToolResult[WriteDetails], error) {
		path := resolve(c, p.Path)
		unlock := c.MutationQueue.Lock(path)
		defer unlock()
		if err := ctx.Err(); err != nil {
			return tool.ToolResult[WriteDetails]{}, err
		}
		if err := c.FileSystem.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return tool.ToolResult[WriteDetails]{}, err
		}
		if err := c.FileSystem.WriteFile(path, []byte(p.Content), 0644); err != nil {
			return tool.ToolResult[WriteDetails]{}, err
		}
		d := WriteDetails{Path: path, Bytes: len(p.Content)}
		return tool.ToolResult[WriteDetails]{Content: []message.Content{message.Text(fmt.Sprintf("Wrote %d bytes to %s", d.Bytes, path))}, Details: d}, nil
	}}
}

type EditParams struct {
	Path    string `json:"path"`
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}
type EditDetails struct {
	Path         string
	Replacements int
}

func Edit(c Config) tool.Tool[EditParams, EditDetails] {
	c = c.defaults()
	return tool.Tool[EditParams, EditDetails]{Name: "edit", Description: "Replace one exact occurrence in a file.", ExecutionMode: tool.Parallel, Execute: func(ctx context.Context, _ tool.ToolCall, p EditParams, _ tool.Update[EditDetails]) (tool.ToolResult[EditDetails], error) {
		if p.OldText == "" {
			return tool.ToolResult[EditDetails]{}, errors.New("oldText must not be empty")
		}
		path := resolve(c, p.Path)
		unlock := c.MutationQueue.Lock(path)
		defer unlock()
		b, err := c.FileSystem.ReadFile(path)
		if err != nil {
			return tool.ToolResult[EditDetails]{}, err
		}
		count := bytes.Count(b, []byte(p.OldText))
		if count == 0 {
			return tool.ToolResult[EditDetails]{}, errors.New("oldText was not found")
		}
		if count > 1 {
			return tool.ToolResult[EditDetails]{}, fmt.Errorf("oldText occurs %d times", count)
		}
		if err := ctx.Err(); err != nil {
			return tool.ToolResult[EditDetails]{}, err
		}
		next := bytes.Replace(b, []byte(p.OldText), []byte(p.NewText), 1)
		if err = c.FileSystem.WriteFile(path, next, 0644); err != nil {
			return tool.ToolResult[EditDetails]{}, err
		}
		d := EditDetails{Path: path, Replacements: 1}
		return tool.ToolResult[EditDetails]{Content: []message.Content{message.Text("Updated " + path)}, Details: d}, nil
	}}
}

type MutationQueue struct {
	mu    sync.Mutex
	paths map[string]*sync.Mutex
}

func NewMutationQueue() *MutationQueue { return &MutationQueue{paths: make(map[string]*sync.Mutex)} }
func (q *MutationQueue) Lock(path string) func() {
	q.mu.Lock()
	m := q.paths[path]
	if m == nil {
		m = &sync.Mutex{}
		q.paths[path] = m
	}
	q.mu.Unlock()
	m.Lock()
	return m.Unlock
}

type BashParams struct {
	Command   string `json:"command"`
	TimeoutMS int    `json:"timeoutMs,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
}
type BashDetails struct {
	ExitCode int
	TimedOut bool
	Stream   string `json:"stream,omitempty"`
	Text     string `json:"text,omitempty"`
	Truncation
}
type ShellResult struct {
	Stdout, Stderr []byte
	ExitCode       int
}
type ShellExecutor interface {
	Execute(context.Context, string, string, func(string, []byte)) (ShellResult, error)
}
type OSShellExecutor struct{}
type chunkWriter struct {
	name   string
	mu     *sync.Mutex
	buf    *bytes.Buffer
	update func(string, []byte)
}

func (w chunkWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	if w.update != nil {
		w.update(w.name, append([]byte(nil), p...))
	}
	return len(p), nil
}
func (OSShellExecutor) Execute(ctx context.Context, command, cwd string, update func(string, []byte)) (ShellResult, error) {
	cmd := exec.CommandContext(ctx, "sh", "-lc", command)
	cmd.Dir = cwd
	var stdout, stderr bytes.Buffer
	var mu sync.Mutex
	cmd.Stdout = chunkWriter{name: "stdout", mu: &mu, buf: &stdout, update: update}
	cmd.Stderr = chunkWriter{name: "stderr", mu: &mu, buf: &stderr, update: update}
	err := cmd.Run()
	result := ShellResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err == nil {
		return result, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		result.ExitCode = exit.ExitCode()
		return result, nil
	}
	return result, err
}
func Bash(c Config) tool.Tool[BashParams, BashDetails] {
	c = c.defaults()
	return tool.Tool[BashParams, BashDetails]{Name: "bash", Description: "Run a shell command and stream stdout and stderr.", ExecutionMode: tool.Sequential, Execute: func(ctx context.Context, _ tool.ToolCall, p BashParams, update tool.Update[BashDetails]) (tool.ToolResult[BashDetails], error) {
		runCtx := ctx
		cancel := func() {}
		if p.TimeoutMS > 0 {
			runCtx, cancel = context.WithTimeout(ctx, time.Duration(p.TimeoutMS)*time.Millisecond)
		}
		defer cancel()
		cwd := c.Cwd
		if p.Cwd != "" {
			cwd = resolve(c, p.Cwd)
		}
		res, err := c.Shell.Execute(runCtx, p.Command, cwd, func(stream string, b []byte) { update(BashDetails{Stream: stream, Text: string(b)}) })
		if err != nil {
			return tool.ToolResult[BashDetails]{}, err
		}
		combined := append(append([]byte(nil), res.Stdout...), res.Stderr...)
		text, tr := truncate(ctx, c, "bash-output", combined)
		d := BashDetails{ExitCode: res.ExitCode, TimedOut: errors.Is(runCtx.Err(), context.DeadlineExceeded), Truncation: tr}
		return tool.ToolResult[BashDetails]{Content: []message.Content{message.Text(text)}, Details: d, IsError: res.ExitCode != 0 || d.TimedOut}, nil
	}}
}

type GrepParams struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path,omitempty"`
	Glob       string `json:"glob,omitempty"`
	IgnoreCase bool   `json:"ignoreCase,omitempty"`
	Literal    bool   `json:"literal,omitempty"`
	Context    int    `json:"context,omitempty"`
	Cursor     string `json:"cursor,omitempty"`
	MaxResults int    `json:"maxResults,omitempty"`
}
type GrepDetails struct {
	Matches, Files     int
	Cursor             string `json:"cursor,omitempty"`
	RegexFallbackError string `json:"regexFallbackError,omitempty"`
	Truncation
}

const (
	defaultGrepPageSize = 20
	maxGrepPageSize     = 50
	maxGrepPerFile      = 200
	defaultGrepBudget   = 10 * time.Second
	maxGrepContext      = 20
	defaultFindPageSize = 30
)

func searchCursor(kind string, position int, fingerprint string) string {
	payload := fmt.Sprintf("%s:%d:%s", kind, position, fingerprint)
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

func parseSearchCursor(value, kind, fingerprint string) (int, error) {
	if value == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, errors.New("invalid FFF cursor")
	}
	parts := strings.Split(string(b), ":")
	if len(parts) != 3 || parts[0] != kind || parts[2] != fingerprint {
		return 0, errors.New("FFF cursor does not match this search")
	}
	position, err := strconv.Atoi(parts[1])
	if err != nil || position < 0 {
		return 0, errors.New("invalid FFF cursor position")
	}
	return position, nil
}

func cursorFingerprint(values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return fmt.Sprintf("%x", hash.Sum(nil)[:8])
}

func grepBudget(ctx context.Context) time.Duration {
	budget := defaultGrepBudget
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < budget {
			budget = remaining
		}
	}
	if budget < time.Millisecond {
		return time.Millisecond
	}
	return budget
}

func Grep(c Config) tool.Tool[GrepParams, GrepDetails] {
	c = c.defaults()
	engine, engineErr := searcher(c)
	return tool.Tool[GrepParams, GrepDetails]{Name: "grep", Description: "Search indexed text files with FFF.", ExecutionMode: tool.Parallel, Execute: func(ctx context.Context, _ tool.ToolCall, p GrepParams, _ tool.Update[GrepDetails]) (tool.ToolResult[GrepDetails], error) {
		if engineErr != nil {
			return tool.ToolResult[GrepDetails]{}, engineErr
		}
		if err := ctx.Err(); err != nil {
			return tool.ToolResult[GrepDetails]{}, err
		}
		rawPattern := p.Pattern
		pattern := p.Pattern
		root := resolve(c, p.Path)
		if p.Path == "" {
			root = c.Cwd
		}
		limit := p.MaxResults
		if limit <= 0 {
			limit = defaultGrepPageSize
		}
		if limit > maxGrepPageSize {
			limit = maxGrepPageSize
		}
		mode := fff.GrepRegex
		if p.Literal && !p.IgnoreCase {
			mode = fff.GrepPlain
		}
		if p.IgnoreCase {
			if p.Literal {
				pattern = regexp.QuoteMeta(pattern)
			}
			pattern = "(?i)" + pattern
		}
		if mode == fff.GrepRegex {
			if _, err := regexp.Compile(pattern); err != nil {
				return tool.ToolResult[GrepDetails]{}, err
			}
		}
		contextLines := p.Context
		if contextLines < 0 {
			contextLines = 0
		} else if contextLines > maxGrepContext {
			contextLines = maxGrepContext
		}
		fingerprint := cursorFingerprint(root, rawPattern, p.Glob, strconv.FormatBool(p.IgnoreCase), strconv.FormatBool(p.Literal), strconv.Itoa(contextLines), strconv.Itoa(limit))
		fileOffset, err := parseSearchCursor(p.Cursor, "g", fingerprint)
		if err != nil {
			return tool.ToolResult[GrepDetails]{}, err
		}
		result, err := engine.Grep(ctx, root, fff.GrepOptions{
			Pattern:     pattern,
			Constraints: p.Glob,
			Mode:        mode,
			// The existing tool was case-sensitive by default. Preserve that
			// contract rather than inheriting FFF's smart-case default.
			SmartCase:     false,
			Limit:         limit,
			FileOffset:    fileOffset,
			TimeBudget:    grepBudget(ctx),
			MaxPerFile:    maxGrepPerFile,
			BeforeContext: contextLines,
			AfterContext:  contextLines,
		})
		if err != nil {
			return tool.ToolResult[GrepDetails]{}, err
		}
		var out strings.Builder
		files := make(map[string]struct{})
		for _, match := range result.Matches {
			files[match.Path] = struct{}{}
			for index, line := range match.ContextBefore {
				lineNumber := match.Line - len(match.ContextBefore) + index
				fmt.Fprintf(&out, "%s:%d-:%s\n", match.Path, lineNumber, line)
			}
			fmt.Fprintf(&out, "%s:%d:%s\n", match.Path, match.Line, match.Text)
			for index, line := range match.ContextAfter {
				fmt.Fprintf(&out, "%s:%d-:%s\n", match.Path, match.Line+index+1, line)
			}
		}
		details := GrepDetails{Matches: len(result.Matches), Files: len(files), RegexFallbackError: result.RegexFallbackError}
		if result.NextFilePage > 0 {
			details.Cursor = searchCursor("g", result.NextFilePage, fingerprint)
			fmt.Fprintf(&out, "\nContinue with cursor=\"%s\"", details.Cursor)
		}
		if result.RegexFallbackError != "" {
			fmt.Fprintf(&out, "\nInvalid regex: %s; FFF used literal matching", result.RegexFallbackError)
		}
		text, tr := truncate(ctx, c, "grep-output", []byte(strings.TrimSuffix(out.String(), "\n")))
		details.Truncation = tr
		return tool.ToolResult[GrepDetails]{Content: []message.Content{message.Text(text)}, Details: details}, nil
	}}
}

type FindParams struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path,omitempty"`
	Cursor     string `json:"cursor,omitempty"`
	MaxResults int    `json:"maxResults,omitempty"`
}
type FindDetails struct {
	Results int
	Cursor  string `json:"cursor,omitempty"`
	Truncation
}

func Find(c Config) tool.Tool[FindParams, FindDetails] {
	c = c.defaults()
	engine, engineErr := searcher(c)
	return tool.Tool[FindParams, FindDetails]{Name: "find", Description: "Find files with FFF's indexed fuzzy and glob search.", ExecutionMode: tool.Parallel, Execute: func(ctx context.Context, _ tool.ToolCall, p FindParams, _ tool.Update[FindDetails]) (tool.ToolResult[FindDetails], error) {
		if engineErr != nil {
			return tool.ToolResult[FindDetails]{}, engineErr
		}
		root := resolve(c, p.Path)
		if p.Path == "" {
			root = c.Cwd
		}
		limit := p.MaxResults
		if limit <= 0 {
			limit = defaultFindPageSize
		}
		fingerprint := cursorFingerprint(root, p.Pattern, strconv.Itoa(limit))
		page, err := parseSearchCursor(p.Cursor, "f", fingerprint)
		if err != nil {
			return tool.ToolResult[FindDetails]{}, err
		}
		result, err := engine.Find(ctx, root, fff.FindOptions{Pattern: p.Pattern, Limit: limit, Page: page})
		if err != nil {
			return tool.ToolResult[FindDetails]{}, err
		}
		found := make([]string, 0, len(result.Files))
		for _, file := range result.Files {
			found = append(found, file.Path)
		}
		details := FindDetails{Results: len(found)}
		if result.NextPage > 0 {
			details.Cursor = searchCursor("f", result.NextPage, fingerprint)
			found = append(found, fmt.Sprintf("Continue with cursor=\"%s\"", details.Cursor))
		}
		text, tr := truncate(ctx, c, "find-output", []byte(strings.Join(found, "\n")))
		details.Truncation = tr
		return tool.ToolResult[FindDetails]{Content: []message.Content{message.Text(text)}, Details: details}, nil
	}}
}

type LSParams struct {
	Path string `json:"path,omitempty"`
}
type LSDetails struct {
	Entries int
	Truncation
}

func LS(c Config) tool.Tool[LSParams, LSDetails] {
	c = c.defaults()
	return tool.Tool[LSParams, LSDetails]{Name: "ls", Description: "List directory entries and basic metadata.", ExecutionMode: tool.Parallel, Execute: func(ctx context.Context, _ tool.ToolCall, p LSParams, _ tool.Update[LSDetails]) (tool.ToolResult[LSDetails], error) {
		path := resolve(c, p.Path)
		if p.Path == "" {
			path = c.Cwd
		}
		entries, err := c.FileSystem.ReadDir(path)
		if err != nil {
			return tool.ToolResult[LSDetails]{}, err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		var out strings.Builder
		for _, e := range entries {
			info, err := e.Info()
			if err != nil {
				continue
			}
			kind := "file"
			if e.IsDir() {
				kind = "dir"
			}
			fmt.Fprintf(&out, "%s\t%s\t%s\t%s\n", kind, strconv.FormatInt(info.Size(), 10), info.ModTime().UTC().Format(time.RFC3339), e.Name())
		}
		text, tr := truncate(ctx, c, "ls-output", []byte(strings.TrimSuffix(out.String(), "\n")))
		return tool.ToolResult[LSDetails]{Content: []message.Content{message.Text(text)}, Details: LSDetails{Entries: len(entries), Truncation: tr}}, nil
	}}
}

func RegisterAll(r *tool.Registry, c Config) error {
	_, err := RegisterAllManaged(r, c)
	return err
}

// RegisterAllManaged registers the built-ins with one shared FFF pool and
// returns that pool so the owning Harness can close its native resources.
func RegisterAllManaged(r *tool.Registry, c Config) (*fff.Pool, error) {
	c, pool, err := prepareSearch(c)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*fff.Pool, error) {
		if pool != nil {
			_ = pool.Close()
		}
		return nil, err
	}
	if err := r.Register(Read(c)); err != nil {
		return fail(err)
	}
	if err := r.Register(Bash(c)); err != nil {
		return fail(err)
	}
	if err := r.Register(Edit(c)); err != nil {
		return fail(err)
	}
	if err := r.Register(Write(c)); err != nil {
		return fail(err)
	}
	if err := r.Register(Grep(c)); err != nil {
		return fail(err)
	}
	if err := r.Register(Find(c)); err != nil {
		return fail(err)
	}
	if err := r.Register(LS(c)); err != nil {
		return fail(err)
	}
	return pool, nil
}
