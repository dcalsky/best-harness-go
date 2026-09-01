// Package fff exposes the small portion of FFF's C ABI that the harness uses
// for long-lived file and content search.
package fff

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	defaultScanTimeout = 15 * time.Second
	defaultMaxRoots    = 4
)

var ErrClosed = errors.New("FFF search pool is closed")

// Options controls an FFF finder instance. When LibraryPath is empty, the
// pinned prebuilt FFF release is downloaded to CacheDir and verified.
type Options struct {
	LibraryPath string
	CacheDir    string
	ScanTimeout time.Duration
	MaxRoots    int

	// releaseBaseURL is overridden by tests. Production callers always use the
	// pinned GitHub release URL from assets.go.
	releaseBaseURL string
}

// FindOptions configures a file search.
type FindOptions struct {
	Pattern string
	Limit   int
	Page    int
}

// File is a path returned by FFF, relative to the indexed root.
type File struct {
	Path string
}

// FindResult is one page of file-search results.
type FindResult struct {
	Files        []File
	TotalMatched int
	NextPage     int
}

// GrepMode selects FFF's content matching algorithm.
type GrepMode uint8

const (
	GrepPlain GrepMode = iota
	GrepRegex
)

// GrepOptions configures a content search.
type GrepOptions struct {
	Pattern       string
	Constraints   string
	Mode          GrepMode
	SmartCase     bool
	Limit         int
	FileOffset    int
	TimeBudget    time.Duration
	MaxPerFile    int
	BeforeContext int
	AfterContext  int
}

// Match is a content match returned by FFF, relative to the indexed root.
type Match struct {
	Path          string
	Line          int
	Text          string
	ContextBefore []string
	ContextAfter  []string
	Definition    bool
}

// GrepResult is one page of content-search results.
type GrepResult struct {
	Matches            []Match
	TotalFiles         int
	NextFilePage       int
	RegexFallbackError string
}

// Searcher permits tests and callers inside the repository to substitute the
// native backend without changing builtin tool behavior.
type Searcher interface {
	Find(context.Context, string, FindOptions) (FindResult, error)
	Grep(context.Context, string, GrepOptions) (GrepResult, error)
}

// Pool holds one long-lived, watcher-backed FFF instance for each indexed
// root. Find and grep acquire the same instance for a root, so later calls use
// the already-warm index.
type Pool struct {
	opts Options

	mu     sync.Mutex
	byRoot map[string]*poolEntry
	clock  uint64
	closed bool
	active sync.WaitGroup
}

type poolEntry struct {
	finder   *Finder
	inUse    int
	lastUsed uint64
}

// NewPool creates a pool of FFF finder instances.
func NewPool(opts Options) *Pool {
	if opts.ScanTimeout <= 0 {
		opts.ScanTimeout = defaultScanTimeout
	}
	if opts.MaxRoots <= 0 {
		opts.MaxRoots = defaultMaxRoots
	}
	pool := &Pool{opts: opts, byRoot: make(map[string]*poolEntry)}
	runtime.SetFinalizer(pool, func(pool *Pool) { _ = pool.Close() })
	return pool
}

func (p *Pool) acquire(root string) (*poolEntry, []*Finder, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve FFF root %s: %w", root, err)
	}
	root = filepath.Clean(absoluteRoot)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, nil, ErrClosed
	}
	p.clock++
	entry := p.byRoot[root]
	if entry == nil {
		entry = &poolEntry{finder: &Finder{root: root, opts: p.opts}}
		p.byRoot[root] = entry
	}
	entry.inUse++
	entry.lastUsed = p.clock
	p.active.Add(1)
	return entry, p.evictLocked(), nil
}

func (p *Pool) release(entry *poolEntry) {
	p.mu.Lock()
	entry.inUse--
	evicted := p.evictLocked()
	p.mu.Unlock()
	for _, finder := range evicted {
		finder.close()
	}
	p.active.Done()
}

func (p *Pool) evictLocked() []*Finder {
	if p.closed || len(p.byRoot) <= p.opts.MaxRoots {
		return nil
	}
	var evicted []*Finder
	for len(p.byRoot) > p.opts.MaxRoots {
		var oldestRoot string
		var oldest *poolEntry
		for root, entry := range p.byRoot {
			if entry.inUse != 0 || oldest != nil && entry.lastUsed >= oldest.lastUsed {
				continue
			}
			oldestRoot, oldest = root, entry
		}
		if oldest == nil {
			break
		}
		delete(p.byRoot, oldestRoot)
		evicted = append(evicted, oldest.finder)
	}
	return evicted
}

// Close destroys every native finder and watcher owned by the pool. It waits
// for in-flight searches and is safe to call more than once.
func (p *Pool) Close() error {
	runtime.SetFinalizer(p, nil)
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()

	p.active.Wait()
	p.mu.Lock()
	finders := make([]*Finder, 0, len(p.byRoot))
	for _, entry := range p.byRoot {
		finders = append(finders, entry.finder)
	}
	p.byRoot = make(map[string]*poolEntry)
	p.mu.Unlock()
	for _, finder := range finders {
		finder.close()
	}
	return nil
}

// Find performs fuzzy search unless Pattern is a glob, in which case it uses
// FFF's exact glob API. Both variants retain FFF's frecency ranking.
func (p *Pool) Find(ctx context.Context, root string, opts FindOptions) (FindResult, error) {
	if opts.Limit <= 0 {
		opts.Limit = 1000
	}
	if opts.Page < 0 {
		return FindResult{}, errors.New("FFF find page must not be negative")
	}
	entry, evicted, err := p.acquire(root)
	for _, finder := range evicted {
		finder.close()
	}
	if err != nil {
		return FindResult{}, err
	}
	defer p.release(entry)
	return entry.finder.find(ctx, opts)
}

// Grep performs an indexed content search.
func (p *Pool) Grep(ctx context.Context, root string, opts GrepOptions) (GrepResult, error) {
	if opts.Limit <= 0 {
		opts.Limit = 1000
	}
	if opts.FileOffset < 0 {
		return GrepResult{}, errors.New("FFF grep file offset must not be negative")
	}
	if opts.TimeBudget < 0 {
		return GrepResult{}, errors.New("FFF grep time budget must not be negative")
	}
	if opts.BeforeContext < 0 || opts.AfterContext < 0 {
		return GrepResult{}, errors.New("FFF grep context must not be negative")
	}
	entry, evicted, err := p.acquire(root)
	for _, finder := range evicted {
		finder.close()
	}
	if err != nil {
		return GrepResult{}, err
	}
	defer p.release(entry)
	return entry.finder.grep(ctx, opts)
}

// Finder lazily initializes its native instance on its first query.
type Finder struct {
	root string
	opts Options

	mu     sync.Mutex
	native *nativeFinder
}

func (f *Finder) ensure(ctx context.Context) (*nativeFinder, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.native != nil {
		return f.native, nil
	}
	n, err := nativeOpen(ctx, f.root, f.opts)
	if err != nil {
		return nil, fmt.Errorf("open FFF index for %s: %w", f.root, err)
	}
	scanTimeout := f.opts.ScanTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < scanTimeout {
			scanTimeout = remaining
		}
	}
	if scanTimeout < time.Millisecond {
		scanTimeout = time.Millisecond
	}
	if err := n.waitForScan(scanTimeout); err != nil {
		n.close()
		return nil, fmt.Errorf("initialize FFF index for %s: %w", f.root, err)
	}
	if err := ctx.Err(); err != nil {
		n.close()
		return nil, err
	}
	f.native = n
	return n, nil
}

func (f *Finder) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.native != nil {
		f.native.close()
		f.native = nil
	}
}

func (f *Finder) find(ctx context.Context, opts FindOptions) (FindResult, error) {
	n, err := f.ensure(ctx)
	if err != nil {
		return FindResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return FindResult{}, err
	}
	var result FindResult
	if hasGlob(opts.Pattern) {
		result, err = n.glob(opts.Pattern, opts.Page, opts.Limit)
	} else {
		result, err = n.search(opts.Pattern, opts.Page, opts.Limit)
	}
	if err == nil && result.TotalMatched > (opts.Page+1)*opts.Limit {
		result.NextPage = opts.Page + 1
	}
	if err == nil {
		err = ctx.Err()
	}
	return result, err
}

func (f *Finder) grep(ctx context.Context, opts GrepOptions) (GrepResult, error) {
	n, err := f.ensure(ctx)
	if err != nil {
		return GrepResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return GrepResult{}, err
	}
	result, err := n.grep(opts)
	if err == nil {
		err = ctx.Err()
	}
	return result, err
}

func hasGlob(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[") || strings.Contains(pattern, "{")
}
