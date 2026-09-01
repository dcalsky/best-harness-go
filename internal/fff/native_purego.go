//go:build (darwin && arm64) || (linux && (amd64 || arm64)) || (windows && amd64)

package fff

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

type createOptions struct {
	Version uint32
	_       uint32

	BasePath       *byte
	FrecencyDBPath *byte
	HistoryDBPath  *byte

	EnableMMapCache       uint8
	EnableContentIndexing uint8
	Watch                 uint8
	AIMode                uint8
	_                     [4]byte

	LogFilePath *byte
	LogLevel    *byte

	CacheBudgetMaxFiles    uint64
	CacheBudgetMaxBytes    uint64
	CacheBudgetMaxFileSize uint64

	EnableFSRootScanning uint8
	EnableHomeScanning   uint8
	FollowSymlinks       uint8
	_                    [5]byte
}

type puregoBridge struct {
	library uintptr

	create      func(*createOptions) uintptr
	destroy     func(uintptr)
	waitForScan func(uintptr, uint64) uintptr
	search      func(uintptr, string, uintptr, uint32, uint32, uint32, int32, uint32) uintptr
	glob        func(uintptr, string, uintptr, uint32, uint32, uint32) uintptr
	grep        func(uintptr, string, uint8, uint64, uint32, bool, uint32, uint32, uint64, uint32, uint32, bool) uintptr

	freeResult func(uintptr)
	freeSearch func(uintptr)
	freeGrep   func(uintptr)

	resultSuccess func(uintptr) bool
	resultError   func(uintptr) string
	resultHandle  func(uintptr) uintptr

	searchItem  func(uintptr, uint32) uintptr
	searchCount func(uintptr) uint32
	searchTotal func(uintptr) uint32
	filePath    func(uintptr) string

	grepMatch       func(uintptr, uint32) uintptr
	grepCount       func(uintptr) uint32
	grepTotalFiles  func(uintptr) uint32
	grepNextOffset  func(uintptr) uint32
	grepPath        func(uintptr) string
	grepText        func(uintptr) string
	grepLine        func(uintptr) uint64
	grepDefinition  func(uintptr) bool
	grepFallback    func(uintptr) string
	grepBeforeCount func(uintptr) uint32
	grepAfterCount  func(uintptr) uint32
	grepBefore      func(uintptr, uint32) string
	grepAfter       func(uintptr, uint32) string
}

var puregoBridgeRegistry = struct {
	sync.Mutex
	bridges map[string]*puregoBridge
}{bridges: make(map[string]*puregoBridge)}

type nativeFinder struct {
	bridge *puregoBridge
	handle uintptr
}

func nativeOpen(ctx context.Context, root string, opts Options) (*nativeFinder, error) {
	libraryPath, err := resolveLibrary(ctx, opts, nativeLibc())
	if err != nil {
		return nil, err
	}
	bridge, err := puregoBridgeFor(libraryPath)
	if err != nil {
		return nil, err
	}
	rootBytes := append([]byte(root), 0)
	createOpts := createOptions{
		Version:               2,
		BasePath:              &rootBytes[0],
		EnableMMapCache:       1,
		EnableContentIndexing: 1,
		Watch:                 1,
		AIMode:                1,
	}
	if unsafe.Sizeof(createOpts) != 88 {
		return nil, fmt.Errorf("unexpected FFF create-options layout: %d bytes", unsafe.Sizeof(createOpts))
	}
	result := bridge.create(&createOpts)
	runtime.KeepAlive(rootBytes)
	if result == 0 {
		return nil, fmt.Errorf("FFF returned no create result")
	}
	if !bridge.resultSuccess(result) {
		message := bridge.errorMessage(result)
		bridge.freeResult(result)
		return nil, fmt.Errorf("%s", message)
	}
	handle := bridge.resultHandle(result)
	bridge.freeResult(result)
	if handle == 0 {
		return nil, fmt.Errorf("FFF returned an empty instance handle")
	}
	return &nativeFinder{bridge: bridge, handle: handle}, nil
}

// puregoBridgeFor keeps a loaded FFF module for the process lifetime and
// shares its purego call stubs across indexed roots. In particular, unloading
// the Windows DLL while FFF worker threads return through it causes a delayed
// access violation even after fff_destroy has completed.
func puregoBridgeFor(libraryPath string) (*puregoBridge, error) {
	puregoBridgeRegistry.Lock()
	defer puregoBridgeRegistry.Unlock()
	if bridge := puregoBridgeRegistry.bridges[libraryPath]; bridge != nil {
		return bridge, nil
	}
	library, err := openNativeLibrary(libraryPath)
	if err != nil {
		return nil, fmt.Errorf("load FFF %s C library %s: %w", ReleaseVersion, libraryPath, err)
	}
	bridge := &puregoBridge{library: library}
	bindings := []struct {
		name string
		dst  any
	}{
		{"fff_create_instance_with", &bridge.create},
		{"fff_destroy", &bridge.destroy},
		{"fff_wait_for_scan", &bridge.waitForScan},
		{"fff_search", &bridge.search},
		{"fff_glob", &bridge.glob},
		{"fff_live_grep", &bridge.grep},
		{"fff_free_result", &bridge.freeResult},
		{"fff_free_search_result", &bridge.freeSearch},
		{"fff_free_grep_result", &bridge.freeGrep},
		{"fff_result_get_success", &bridge.resultSuccess},
		{"fff_result_get_error", &bridge.resultError},
		{"fff_result_get_handle", &bridge.resultHandle},
		{"fff_search_result_get_item", &bridge.searchItem},
		{"fff_search_result_get_count", &bridge.searchCount},
		{"fff_search_result_get_total_matched", &bridge.searchTotal},
		{"fff_file_item_get_relative_path", &bridge.filePath},
		{"fff_grep_result_get_match", &bridge.grepMatch},
		{"fff_grep_result_get_count", &bridge.grepCount},
		{"fff_grep_result_get_total_files_searched", &bridge.grepTotalFiles},
		{"fff_grep_result_get_next_file_offset", &bridge.grepNextOffset},
		{"fff_grep_match_get_relative_path", &bridge.grepPath},
		{"fff_grep_match_get_line_content", &bridge.grepText},
		{"fff_grep_match_get_line_number", &bridge.grepLine},
		{"fff_grep_match_get_is_definition", &bridge.grepDefinition},
		{"fff_grep_match_get_context_before_count", &bridge.grepBeforeCount},
		{"fff_grep_match_get_context_after_count", &bridge.grepAfterCount},
		{"fff_grep_match_get_context_before", &bridge.grepBefore},
		{"fff_grep_match_get_context_after", &bridge.grepAfter},
		{"fff_grep_result_get_regex_fallback_error", &bridge.grepFallback},
	}
	for _, binding := range bindings {
		if err := registerNativeFunction(binding.dst, library, binding.name); err != nil {
			return nil, fmt.Errorf("FFF %s is missing symbol %s: %w", ReleaseVersion, binding.name, err)
		}
	}
	puregoBridgeRegistry.bridges[libraryPath] = bridge
	return bridge, nil
}

func registerNativeFunction(dst any, library uintptr, name string) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%v", recovered)
		}
	}()
	purego.RegisterLibFunc(dst, library, name)
	return nil
}

func (b *puregoBridge) errorMessage(result uintptr) string {
	if result == 0 {
		return "FFF returned no result"
	}
	if message := b.resultError(result); message != "" {
		return message
	}
	return "unknown FFF error"
}

func (n *nativeFinder) close() {
	if n == nil || n.bridge == nil {
		return
	}
	if n.handle != 0 {
		n.bridge.destroy(n.handle)
		n.handle = 0
	}
	n.bridge = nil
}

func (n *nativeFinder) waitForScan(timeout time.Duration) error {
	return n.consumeEnvelope(n.bridge.waitForScan(n.handle, uint64(timeout.Milliseconds())))
}

func (n *nativeFinder) search(query string, page, limit int) (FindResult, error) {
	result := n.bridge.search(n.handle, query, 0, 0, uint32(page), uint32(limit), 0, 0)
	return n.collectFind(result)
}

func (n *nativeFinder) glob(query string, page, limit int) (FindResult, error) {
	result := n.bridge.glob(n.handle, query, 0, 0, uint32(page), uint32(limit))
	return n.collectFind(result)
}

func (n *nativeFinder) collectFind(result uintptr) (FindResult, error) {
	payload, err := n.payload(result)
	if err != nil {
		return FindResult{}, err
	}
	defer n.bridge.freeSearch(payload)
	count := n.bridge.searchCount(payload)
	files := make([]File, 0, int(count))
	for index := uint32(0); index < count; index++ {
		item := n.bridge.searchItem(payload, index)
		if path := n.bridge.filePath(item); path != "" {
			files = append(files, File{Path: path})
		}
	}
	return FindResult{Files: files, TotalMatched: int(n.bridge.searchTotal(payload))}, nil
}

func (n *nativeFinder) grep(opts GrepOptions) (GrepResult, error) {
	query := strings.TrimSpace(strings.Join([]string{opts.Constraints, opts.Pattern}, " "))
	maxPerFile := opts.MaxPerFile
	if maxPerFile <= 0 {
		maxPerFile = 200
	}
	result := n.bridge.grep(
		n.handle,
		query,
		uint8(opts.Mode),
		10*1024*1024,
		uint32(maxPerFile),
		opts.SmartCase,
		uint32(opts.FileOffset),
		uint32(opts.Limit),
		uint64(opts.TimeBudget.Milliseconds()),
		uint32(opts.BeforeContext),
		uint32(opts.AfterContext),
		true,
	)
	payload, err := n.payload(result)
	if err != nil {
		return GrepResult{}, err
	}
	defer n.bridge.freeGrep(payload)
	count := n.bridge.grepCount(payload)
	matches := make([]Match, 0, int(count))
	for index := uint32(0); index < count; index++ {
		match := n.bridge.grepMatch(payload, index)
		path := n.bridge.grepPath(match)
		text := n.bridge.grepText(match)
		if path == "" || text == "" {
			continue
		}
		beforeCount := n.bridge.grepBeforeCount(match)
		before := make([]string, 0, int(beforeCount))
		for contextIndex := uint32(0); contextIndex < beforeCount; contextIndex++ {
			if line := n.bridge.grepBefore(match, contextIndex); line != "" {
				before = append(before, line)
			}
		}
		afterCount := n.bridge.grepAfterCount(match)
		after := make([]string, 0, int(afterCount))
		for contextIndex := uint32(0); contextIndex < afterCount; contextIndex++ {
			if line := n.bridge.grepAfter(match, contextIndex); line != "" {
				after = append(after, line)
			}
		}
		matches = append(matches, Match{
			Path:          path,
			Line:          int(n.bridge.grepLine(match)),
			Text:          text,
			ContextBefore: before,
			ContextAfter:  after,
			Definition:    n.bridge.grepDefinition(match),
		})
	}
	return GrepResult{
		Matches:            matches,
		TotalFiles:         int(n.bridge.grepTotalFiles(payload)),
		NextFilePage:       int(n.bridge.grepNextOffset(payload)),
		RegexFallbackError: n.bridge.grepFallback(payload),
	}, nil
}

func (n *nativeFinder) consumeEnvelope(result uintptr) error {
	if result == 0 {
		return fmt.Errorf("FFF returned no result")
	}
	defer n.bridge.freeResult(result)
	if !n.bridge.resultSuccess(result) {
		return fmt.Errorf("%s", n.bridge.errorMessage(result))
	}
	return nil
}

func (n *nativeFinder) payload(result uintptr) (uintptr, error) {
	if result == 0 {
		return 0, fmt.Errorf("FFF returned no result")
	}
	defer n.bridge.freeResult(result)
	if !n.bridge.resultSuccess(result) {
		return 0, fmt.Errorf("%s", n.bridge.errorMessage(result))
	}
	payload := n.bridge.resultHandle(result)
	if payload == 0 {
		return 0, fmt.Errorf("FFF returned an empty result payload")
	}
	return payload, nil
}
