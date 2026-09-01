// Package builtin contains explicitly registered file and shell tools.
package builtin

import (
	"bytes"
	"context"
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
	Pattern       string   `json:"pattern"`
	Path          string   `json:"path,omitempty"`
	Exclude       []string `json:"exclude,omitempty"`
	CaseSensitive bool     `json:"caseSensitive,omitempty"`
	Context       int      `json:"context,omitempty"`
	Limit         int      `json:"limit,omitempty"`
	Cursor        string   `json:"cursor,omitempty"`
}
type GrepDetails struct {
	Matches, Files     int
	TotalMatched       int    `json:"totalMatched"`
	TotalFiles         int    `json:"totalFiles"`
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

type fffCursorStore[T any] struct {
	mu     sync.Mutex
	prefix string
	next   uint64
	values map[string]T
	order  []string
}

func newFFFCursorStore[T any](prefix string) *fffCursorStore[T] {
	return &fffCursorStore[T]{prefix: prefix, values: make(map[string]T)}
}

func (s *fffCursorStore[T]) put(value T) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	id := fmt.Sprintf("%s%d", s.prefix, s.next)
	s.values[id] = value
	s.order = append(s.order, id)
	if len(s.order) > 200 {
		delete(s.values, s.order[0])
		s.order = s.order[1:]
	}
	return id
}

func (s *fffCursorStore[T]) get(id string) (T, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[id]
	return value, ok
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

const maxGrepLineLength = 500

const (
	hotFrecency        = 25
	warmFrecency       = 20
	findWeakSampleSize = 5
)

var (
	wildcardOnlyPattern  = regexp.MustCompile(`^(?:[.^$]*(?:[.][*+?]|\*|\+)[.^$]*|[.^$\s]*|\.\*\??|\.\*[+?]?|\.\+\??|\.|\*|\?)$`)
	recursiveDirPattern  = regexp.MustCompile(`^(.*)/\*\*(?:/\*)?$`)
	fileExtensionPattern = regexp.MustCompile(`\.[A-Za-z][A-Za-z0-9]{0,9}$`)
)

func normalizeFFFPathConstraint(value, cwd string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed == "." || trimmed == "./" {
		return "", nil
	}
	if filepath.IsAbs(trimmed) {
		relative, err := filepath.Rel(cwd, trimmed)
		if err != nil {
			return "", err
		}
		if relative == "." {
			return "", nil
		}
		if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			return "", fmt.Errorf("path constraint must be relative to the workspace: %s", value)
		}
		trimmed = relative
	}
	trimmed = filepath.ToSlash(trimmed)
	trimmed = strings.TrimPrefix(trimmed, "./")
	if trimmed == "**" || trimmed == "**/" || trimmed == "**/*" {
		return "", nil
	}
	if match := recursiveDirPattern.FindStringSubmatch(trimmed); len(match) == 2 && match[1] != "" && !strings.ContainsAny(match[1], "*?[{") {
		return match[1] + "/", nil
	}
	if strings.HasPrefix(trimmed, "/") || strings.HasSuffix(trimmed, "/") || strings.ContainsAny(trimmed, "*?[{") {
		return trimmed, nil
	}
	last := trimmed
	if index := strings.LastIndexByte(last, '/'); index >= 0 {
		last = last[index+1:]
	}
	if fileExtensionPattern.MatchString(last) {
		return trimmed, nil
	}
	return trimmed + "/", nil
}

func fffSearchScope(c Config, requested string) (root, constraint string, err error) {
	root = c.Cwd
	trimmed := strings.TrimSpace(requested)
	if trimmed == "" {
		return root, "", nil
	}
	if trimmed == "~" || strings.HasPrefix(trimmed, "~/") || strings.HasPrefix(trimmed, `~\`) {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", "", homeErr
		}
		trimmed = filepath.Join(home, strings.TrimLeft(trimmed[1:], `/\`))
	}
	absolute := trimmed
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(c.Cwd, trimmed)
	}
	relative, relErr := filepath.Rel(c.Cwd, absolute)
	outside := relErr == nil && (relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)))
	if outside {
		return resolveFFFAuxScope(c.FileSystem, absolute)
	}
	constraint, err = normalizeFFFPathConstraint(trimmed, c.Cwd)
	return root, constraint, err
}

func resolveFFFAuxScope(fileSystem FileSystem, absolute string) (root, constraint string, err error) {
	candidate := filepath.Clean(absolute)
	var suffix []string
	for {
		info, statErr := fileSystem.Stat(candidate)
		if statErr == nil {
			if !info.IsDir() {
				suffix = append([]string{filepath.Base(candidate)}, suffix...)
				candidate = filepath.Dir(candidate)
			}
			constraint, err = normalizeFFFPathConstraint(filepath.ToSlash(filepath.Join(suffix...)), candidate)
			return candidate, constraint, err
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", "", fmt.Errorf("cannot resolve an existing directory for path constraint %q", absolute)
		}
		suffix = append([]string{filepath.Base(candidate)}, suffix...)
		candidate = parent
	}
}

func fffConstraints(pathConstraint string, excludes []string, root string) (string, error) {
	parts := make([]string, 0, 1+len(excludes))
	if pathConstraint != "" {
		parts = append(parts, pathConstraint)
	}
	for _, group := range excludes {
		for _, raw := range strings.FieldsFunc(group, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
			raw = strings.TrimPrefix(raw, "!")
			normalized, err := normalizeFFFPathConstraint(raw, root)
			if err != nil {
				return "", err
			}
			if normalized != "" {
				parts = append(parts, "!"+normalized)
			}
		}
	}
	return strings.Join(parts, " "), nil
}

func truncateFFFLine(line string) string {
	trimmed := strings.TrimSpace(line)
	runes := []rune(trimmed)
	if len(runes) <= maxGrepLineLength {
		return trimmed
	}
	return string(runes[:maxGrepLineLength]) + "..."
}

func fffFileAnnotation(gitStatus string, totalFrecency, accessFrecency int64) string {
	if gitStatus != "" && gitStatus != "clean" && gitStatus != "unknown" {
		return fmt.Sprintf("  [%s in git]", gitStatus)
	}
	frecency := totalFrecency
	if frecency == 0 {
		frecency = accessFrecency
	}
	if frecency >= hotFrecency {
		return "  [VERY often touched file]"
	}
	if frecency >= warmFrecency {
		return "  [often touched file]"
	}
	return ""
}

func formatFFFGrep(matches []fff.Match) string {
	if len(matches) == 0 {
		return "No matches found"
	}
	var out strings.Builder
	currentFile := ""
	for _, match := range matches {
		if match.Path != currentFile {
			if out.Len() > 0 {
				out.WriteByte('\n')
			}
			currentFile = match.Path
			fmt.Fprintln(&out, currentFile+fffFileAnnotation(match.GitStatus, match.TotalFrecencyScore, match.AccessFrecencyScore))
		}
		for index, line := range match.ContextBefore {
			lineNumber := match.Line - len(match.ContextBefore) + index
			fmt.Fprintf(&out, " %d- %s\n", lineNumber, truncateFFFLine(line))
		}
		fmt.Fprintf(&out, " %d: %s\n", match.Line, truncateFFFLine(match.Text))
		for index, line := range match.ContextAfter {
			fmt.Fprintf(&out, " %d- %s\n", match.Line+index+1, truncateFFFLine(line))
		}
	}
	return strings.TrimSuffix(out.String(), "\n")
}

func weakFindScoreThreshold(pattern string) int {
	perfect := len(pattern) * 12
	return perfect / 2
}

func formatFFFFind(result fff.FindResult, limit int, pattern string) (output string, weak bool, shown int) {
	if len(result.Files) == 0 {
		return "No files found matching pattern", false, 0
	}
	weak = result.Files[0].Score < weakFindScoreThreshold(pattern)
	effective := limit
	if weak && effective > findWeakSampleSize {
		effective = findWeakSampleSize
	}
	if effective > len(result.Files) {
		effective = len(result.Files)
	}
	lines := make([]string, 0, effective)
	for _, file := range result.Files[:effective] {
		lines = append(lines, file.Path+fffFileAnnotation(file.GitStatus, file.TotalFrecencyScore, file.AccessFrecencyScore))
	}
	return strings.Join(lines, "\n"), weak, effective
}

func truncateFFFOutput(ctx context.Context, c Config, name, body string, notices []string) (string, Truncation) {
	suffix := ""
	if len(notices) > 0 {
		suffix = "\n\n[" + strings.Join(notices, ". ") + "]"
	}
	full := body + suffix
	details := Truncation{OriginalBytes: len(full)}
	if len(full) <= c.MaxOutputBytes {
		return full, details
	}
	details.Truncated = true
	if c.OutputStore != nil {
		details.StoredAt, _ = c.OutputStore.Save(ctx, name, []byte(full))
	}
	if len(suffix) >= c.MaxOutputBytes {
		// A continuation cursor must remain intact even with an unusually small
		// caller-provided output budget.
		return suffix, details
	}
	marker := "\n... output truncated ..."
	budget := c.MaxOutputBytes - len(suffix)
	if budget <= len(marker) {
		return marker[:budget] + suffix, details
	}
	bodyBytes := []byte(body)
	cut := budget - len(marker)
	for cut > 0 && cut < len(bodyBytes) && bodyBytes[cut]&0xc0 == 0x80 {
		cut--
	}
	return string(bodyBytes[:cut]) + marker + suffix, details
}

func Grep(c Config) tool.Tool[GrepParams, GrepDetails] {
	c = c.defaults()
	engine, engineErr := searcher(c)
	cursors := newFFFCursorStore[int]("fff_c")
	return tool.Tool[GrepParams, GrepDetails]{Name: "grep", Description: "Grep file contents with FFF. Smart-case, auto-detects regex versus literal, and preserves native frecency order.", ExecutionMode: tool.Parallel, Execute: func(ctx context.Context, _ tool.ToolCall, p GrepParams, _ tool.Update[GrepDetails]) (tool.ToolResult[GrepDetails], error) {
		if engineErr != nil {
			return tool.ToolResult[GrepDetails]{}, engineErr
		}
		if err := ctx.Err(); err != nil {
			return tool.ToolResult[GrepDetails]{}, err
		}
		root, pathConstraint, err := fffSearchScope(c, p.Path)
		if err != nil {
			return tool.ToolResult[GrepDetails]{}, err
		}
		constraints, err := fffConstraints(pathConstraint, p.Exclude, root)
		if err != nil {
			return tool.ToolResult[GrepDetails]{}, err
		}
		limit := p.Limit
		if limit == 0 {
			limit = defaultGrepPageSize
		} else if limit < 0 {
			limit = 1
		}
		if limit > maxGrepPageSize {
			limit = maxGrepPageSize
		}
		rawPattern := p.Pattern
		hasRegexSyntax := regexp.QuoteMeta(rawPattern) != rawPattern
		mode := fff.GrepPlain
		if hasRegexSyntax {
			if _, compileErr := regexp.Compile(rawPattern); compileErr == nil {
				mode = fff.GrepRegex
			}
		}
		if hasRegexSyntax && wildcardOnlyPattern.MatchString(strings.TrimSpace(rawPattern)) {
			messageText := fmt.Sprintf("Pattern '%s' matches everything — grep needs a concrete substring or identifier. Example: `pattern: 'MyClass'` or `pattern: 'export function'`.", rawPattern)
			return tool.ToolResult[GrepDetails]{Content: []message.Content{message.Text(messageText)}, Details: GrepDetails{}}, nil
		}
		contextLines := p.Context
		if contextLines < 0 {
			contextLines = 0
		} else if contextLines > maxGrepContext {
			contextLines = maxGrepContext
		}
		smartCase := !p.CaseSensitive
		fileOffset := 0
		if p.Cursor != "" {
			var ok bool
			fileOffset, ok = cursors.get(p.Cursor)
			if !ok {
				return tool.ToolResult[GrepDetails]{}, errors.New("invalid FFF grep cursor")
			}
		}
		result, err := engine.Grep(ctx, root, fff.GrepOptions{
			Pattern:       rawPattern,
			Constraints:   constraints,
			Mode:          mode,
			SmartCase:     smartCase,
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
		fuzzyNotice := ""
		if len(result.Matches) == 0 && result.NextFilePage == 0 && p.Cursor == "" && mode != fff.GrepRegex {
			fuzzyConstraints := constraints
			lastSegment := filepath.Base(filepath.ToSlash(p.Path))
			if fileExtensionPattern.MatchString(lastSegment) {
				fuzzyConstraints = ""
			}
			fuzzy, fuzzyErr := engine.Grep(ctx, root, fff.GrepOptions{
				Pattern: rawPattern, Constraints: fuzzyConstraints, Mode: fff.GrepFuzzy,
				SmartCase: smartCase, Limit: limit, TimeBudget: grepBudget(ctx),
				MaxPerFile: maxGrepPerFile,
			})
			if fuzzyErr == nil && len(fuzzy.Matches) > 0 {
				result = fuzzy
				fuzzyNotice = "0 exact matches. Maybe you meant this?"
			}
		}
		files := make(map[string]struct{})
		for _, match := range result.Matches {
			files[match.Path] = struct{}{}
		}
		details := GrepDetails{
			Matches: len(result.Matches), Files: len(files), TotalMatched: result.TotalMatched,
			TotalFiles: result.TotalFiles, RegexFallbackError: result.RegexFallbackError,
		}
		output := formatFFFGrep(result.Matches)
		notices := make([]string, 0, 3)
		if fuzzyNotice != "" {
			output = "[" + fuzzyNotice + "]\n" + output
		}
		if result.RegexFallbackError != "" {
			notices = append(notices, "Invalid regex: "+result.RegexFallbackError+", used literal match")
		}
		if result.NextFilePage > 0 {
			details.Cursor = cursors.put(result.NextFilePage)
			notices = append(notices, fmt.Sprintf("Continue with cursor=\"%s\"", details.Cursor))
		}
		text, tr := truncateFFFOutput(ctx, c, "grep-output", output, notices)
		details.Truncation = tr
		return tool.ToolResult[GrepDetails]{Content: []message.Content{message.Text(text)}, Details: details}, nil
	}}
}

type FindParams struct {
	Pattern string   `json:"pattern"`
	Path    string   `json:"path,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
	Limit   int      `json:"limit,omitempty"`
	Cursor  string   `json:"cursor,omitempty"`
}
type FindDetails struct {
	Results      int
	TotalMatched int    `json:"totalMatched"`
	TotalFiles   int    `json:"totalFiles"`
	PageIndex    int    `json:"pageIndex"`
	HasMore      bool   `json:"hasMore"`
	Cursor       string `json:"cursor,omitempty"`
	Truncation
}

type findCursor struct {
	Root     string
	Query    string
	Pattern  string
	PageSize int
	NextPage int
}

func Find(c Config) tool.Tool[FindParams, FindDetails] {
	c = c.defaults()
	engine, engineErr := searcher(c)
	cursors := newFFFCursorStore[findCursor]("")
	return tool.Tool[FindParams, FindDetails]{Name: "find", Description: "Fuzzy path and glob search with FFF. Matches the whole repository-relative path and preserves native frecency order.", ExecutionMode: tool.Parallel, Execute: func(ctx context.Context, _ tool.ToolCall, p FindParams, _ tool.Update[FindDetails]) (tool.ToolResult[FindDetails], error) {
		if engineErr != nil {
			return tool.ToolResult[FindDetails]{}, engineErr
		}
		root, query, pattern, limit, page := "", "", p.Pattern, p.Limit, 0
		if p.Cursor != "" {
			resumed, ok := cursors.get(p.Cursor)
			if !ok {
				return tool.ToolResult[FindDetails]{}, errors.New("invalid FFF find cursor")
			}
			root, query, pattern, limit, page = resumed.Root, resumed.Query, resumed.Pattern, resumed.PageSize, resumed.NextPage
		} else {
			var pathConstraint string
			var err error
			root, pathConstraint, err = fffSearchScope(c, p.Path)
			if err != nil {
				return tool.ToolResult[FindDetails]{}, err
			}
			constraints, constraintErr := fffConstraints(pathConstraint, p.Exclude, root)
			if constraintErr != nil {
				return tool.ToolResult[FindDetails]{}, constraintErr
			}
			if limit == 0 {
				limit = defaultFindPageSize
			} else if limit < 0 {
				limit = 1
			}
			query = strings.TrimSpace(strings.Join([]string{constraints, pattern}, " "))
		}
		result, err := engine.Find(ctx, root, fff.FindOptions{Pattern: query, Limit: limit, Page: page, UseQueryParser: true})
		if err != nil {
			return tool.ToolResult[FindDetails]{}, err
		}
		output, weak, shown := formatFFFFind(result, limit, pattern)
		shownSoFar := page*limit + len(result.Files)
		hasMore := len(result.Files) >= limit && result.TotalMatched > shownSoFar
		details := FindDetails{
			Results: shown, TotalMatched: result.TotalMatched, TotalFiles: result.TotalFiles,
			PageIndex: page, HasMore: hasMore,
		}
		notices := make([]string, 0, 1)
		if weak && shown > 0 {
			notices = append(notices, fmt.Sprintf("Query %q produced only weak scattered fuzzy matches. Output capped at %d/%d.", pattern, shown, result.TotalMatched))
		}
		if !weak && hasMore {
			details.Cursor = cursors.put(findCursor{Root: root, Query: query, Pattern: pattern, PageSize: limit, NextPage: page + 1})
			remaining := result.TotalMatched - shownSoFar
			word := "matches"
			if remaining == 1 {
				word = "match"
			}
			notices = append(notices, fmt.Sprintf("%d more %s available. cursor=\"%s\" to continue", remaining, word, details.Cursor))
		}
		text, tr := truncateFFFOutput(ctx, c, "find-output", output, notices)
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
