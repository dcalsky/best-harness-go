//go:build windows && amd64

package fff

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var windowsBridgeRegistry = struct {
	sync.Mutex
	bridges map[string]*windowsBridge
}{bridges: make(map[string]*windowsBridge)}

type windowsBridge struct {
	dll *syscall.DLL

	create, destroy, waitForScan                   *syscall.Proc
	search, glob, grep                             *syscall.Proc
	freeResult, freeSearch, freeGrep               *syscall.Proc
	resultSuccess, resultError, resultHandle       *syscall.Proc
	searchItem, searchCount, searchTotal, filePath *syscall.Proc
	grepMatch, grepCount, grepTotalFiles           *syscall.Proc
	grepNextOffset, grepPath, grepText, grepLine   *syscall.Proc
	grepDefinition, grepFallback                   *syscall.Proc
	grepBeforeCount, grepAfterCount                *syscall.Proc
	grepBefore, grepAfter                          *syscall.Proc
}

type windowsCreateOptions struct {
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

type nativeFinder struct {
	bridge *windowsBridge
	handle uintptr
}

func nativeOpen(ctx context.Context, root string, opts Options) (*nativeFinder, error) {
	libraryPath, err := resolveLibrary(ctx, opts, "")
	if err != nil {
		return nil, err
	}
	bridge, err := windowsBridgeFor(libraryPath)
	if err != nil {
		return nil, err
	}
	rootBytes, err := syscall.ByteSliceFromString(root)
	if err != nil {
		return nil, fmt.Errorf("invalid FFF root: %w", err)
	}
	createOpts := windowsCreateOptions{
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
	result, _, _ := bridge.create.Call(uintptr(unsafe.Pointer(&createOpts)))
	runtime.KeepAlive(rootBytes)
	if result == 0 {
		return nil, fmt.Errorf("FFF returned no create result")
	}
	if !bridge.success(result) {
		message := bridge.errorMessage(result)
		bridge.freeResult.Call(result)
		return nil, fmt.Errorf("%s", message)
	}
	handle, _, _ := bridge.resultHandle.Call(result)
	bridge.freeResult.Call(result)
	if handle == 0 {
		return nil, fmt.Errorf("FFF returned an empty instance handle")
	}
	return &nativeFinder{bridge: bridge, handle: handle}, nil
}

// windowsBridgeFor keeps an FFF DLL loaded for the lifetime of the process.
// FFF destroys each instance synchronously, but worker threads can still return
// through DLL code briefly afterwards. FreeLibrary would unmap that code and
// can cause a delayed access violation. Reusing one bridge per path also avoids
// accumulating a LoadLibrary reference for every indexed root.
func windowsBridgeFor(libraryPath string) (*windowsBridge, error) {
	windowsBridgeRegistry.Lock()
	defer windowsBridgeRegistry.Unlock()
	if bridge := windowsBridgeRegistry.bridges[libraryPath]; bridge != nil {
		return bridge, nil
	}
	dll, err := syscall.LoadDLL(libraryPath)
	if err != nil {
		return nil, fmt.Errorf("load FFF %s C library %s: %w", ReleaseVersion, libraryPath, err)
	}
	bridge, err := loadWindowsBridge(dll)
	if err != nil {
		_ = dll.Release()
		return nil, err
	}
	windowsBridgeRegistry.bridges[libraryPath] = bridge
	return bridge, nil
}

func loadWindowsBridge(dll *syscall.DLL) (*windowsBridge, error) {
	find := func(name string) (*syscall.Proc, error) {
		proc, err := dll.FindProc(name)
		if err != nil {
			return nil, fmt.Errorf("FFF %s is missing symbol %s: %w", ReleaseVersion, name, err)
		}
		return proc, nil
	}
	b := &windowsBridge{dll: dll}
	bindings := []struct {
		name string
		dst  **syscall.Proc
	}{
		{"fff_create_instance_with", &b.create},
		{"fff_destroy", &b.destroy},
		{"fff_wait_for_scan", &b.waitForScan},
		{"fff_search", &b.search},
		{"fff_glob", &b.glob},
		{"fff_live_grep", &b.grep},
		{"fff_free_result", &b.freeResult},
		{"fff_free_search_result", &b.freeSearch},
		{"fff_free_grep_result", &b.freeGrep},
		{"fff_result_get_success", &b.resultSuccess},
		{"fff_result_get_error", &b.resultError},
		{"fff_result_get_handle", &b.resultHandle},
		{"fff_search_result_get_item", &b.searchItem},
		{"fff_search_result_get_count", &b.searchCount},
		{"fff_search_result_get_total_matched", &b.searchTotal},
		{"fff_file_item_get_relative_path", &b.filePath},
		{"fff_grep_result_get_match", &b.grepMatch},
		{"fff_grep_result_get_count", &b.grepCount},
		{"fff_grep_result_get_total_files_searched", &b.grepTotalFiles},
		{"fff_grep_result_get_next_file_offset", &b.grepNextOffset},
		{"fff_grep_match_get_relative_path", &b.grepPath},
		{"fff_grep_match_get_line_content", &b.grepText},
		{"fff_grep_match_get_line_number", &b.grepLine},
		{"fff_grep_match_get_is_definition", &b.grepDefinition},
		{"fff_grep_match_get_context_before_count", &b.grepBeforeCount},
		{"fff_grep_match_get_context_after_count", &b.grepAfterCount},
		{"fff_grep_match_get_context_before", &b.grepBefore},
		{"fff_grep_match_get_context_after", &b.grepAfter},
		{"fff_grep_result_get_regex_fallback_error", &b.grepFallback},
	}
	for _, binding := range bindings {
		proc, err := find(binding.name)
		if err != nil {
			return nil, err
		}
		*binding.dst = proc
	}
	return b, nil
}

func (b *windowsBridge) success(result uintptr) bool {
	value, _, _ := b.resultSuccess.Call(result)
	return value != 0
}

func (b *windowsBridge) errorMessage(result uintptr) string {
	value, _, _ := b.resultError.Call(result)
	if value == 0 {
		return "unknown FFF error"
	}
	return windowsCString(value)
}

func (n *nativeFinder) close() {
	if n == nil || n.bridge == nil {
		return
	}
	if n.handle != 0 {
		n.bridge.destroy.Call(n.handle)
		n.handle = 0
	}
	n.bridge = nil
}

func (n *nativeFinder) waitForScan(timeout time.Duration) error {
	result, _, _ := n.bridge.waitForScan.Call(n.handle, uintptr(timeout.Milliseconds()))
	return n.consumeEnvelope(result)
}

func (n *nativeFinder) search(query string, page, limit int) (FindResult, error) {
	queryBytes, err := syscall.ByteSliceFromString(query)
	if err != nil {
		return FindResult{}, err
	}
	result, _, _ := n.bridge.search.Call(n.handle, uintptr(unsafe.Pointer(&queryBytes[0])), 0, 0, uintptr(page), uintptr(limit), 0, 0)
	runtime.KeepAlive(queryBytes)
	return n.collectFind(result)
}

func (n *nativeFinder) glob(query string, page, limit int) (FindResult, error) {
	queryBytes, err := syscall.ByteSliceFromString(query)
	if err != nil {
		return FindResult{}, err
	}
	result, _, _ := n.bridge.glob.Call(n.handle, uintptr(unsafe.Pointer(&queryBytes[0])), 0, 0, uintptr(page), uintptr(limit))
	runtime.KeepAlive(queryBytes)
	return n.collectFind(result)
}

func (n *nativeFinder) collectFind(result uintptr) (FindResult, error) {
	payload, err := n.payload(result)
	if err != nil {
		return FindResult{}, err
	}
	defer n.bridge.freeSearch.Call(payload)
	count, _, _ := n.bridge.searchCount.Call(payload)
	total, _, _ := n.bridge.searchTotal.Call(payload)
	files := make([]File, 0, int(count))
	for index := uintptr(0); index < count; index++ {
		item, _, _ := n.bridge.searchItem.Call(payload, index)
		path, _, _ := n.bridge.filePath.Call(item)
		if path != 0 {
			files = append(files, File{Path: windowsCString(path)})
		}
	}
	return FindResult{Files: files, TotalMatched: int(total)}, nil
}

func (n *nativeFinder) grep(opts GrepOptions) (GrepResult, error) {
	query := strings.TrimSpace(strings.Join([]string{opts.Constraints, opts.Pattern}, " "))
	queryBytes, err := syscall.ByteSliceFromString(query)
	if err != nil {
		return GrepResult{}, err
	}
	maxPerFile := opts.MaxPerFile
	if maxPerFile <= 0 {
		maxPerFile = 200
	}
	result, _, _ := n.bridge.grep.Call(
		n.handle,
		uintptr(unsafe.Pointer(&queryBytes[0])),
		uintptr(opts.Mode),
		10*1024*1024,
		uintptr(maxPerFile),
		boolUintptr(opts.SmartCase),
		uintptr(opts.FileOffset),
		uintptr(opts.Limit),
		uintptr(opts.TimeBudget.Milliseconds()),
		uintptr(opts.BeforeContext),
		uintptr(opts.AfterContext),
		1,
	)
	runtime.KeepAlive(queryBytes)
	payload, err := n.payload(result)
	if err != nil {
		return GrepResult{}, err
	}
	defer n.bridge.freeGrep.Call(payload)
	count, _, _ := n.bridge.grepCount.Call(payload)
	totalFiles, _, _ := n.bridge.grepTotalFiles.Call(payload)
	nextOffset, _, _ := n.bridge.grepNextOffset.Call(payload)
	matches := make([]Match, 0, int(count))
	for index := uintptr(0); index < count; index++ {
		match, _, _ := n.bridge.grepMatch.Call(payload, index)
		path, _, _ := n.bridge.grepPath.Call(match)
		text, _, _ := n.bridge.grepText.Call(match)
		if path == 0 || text == 0 {
			continue
		}
		line, _, _ := n.bridge.grepLine.Call(match)
		definition, _, _ := n.bridge.grepDefinition.Call(match)
		beforeCount, _, _ := n.bridge.grepBeforeCount.Call(match)
		before := make([]string, 0, int(beforeCount))
		for contextIndex := uintptr(0); contextIndex < beforeCount; contextIndex++ {
			contextLine, _, _ := n.bridge.grepBefore.Call(match, contextIndex)
			if contextLine != 0 {
				before = append(before, windowsCString(contextLine))
			}
		}
		afterCount, _, _ := n.bridge.grepAfterCount.Call(match)
		after := make([]string, 0, int(afterCount))
		for contextIndex := uintptr(0); contextIndex < afterCount; contextIndex++ {
			contextLine, _, _ := n.bridge.grepAfter.Call(match, contextIndex)
			if contextLine != 0 {
				after = append(after, windowsCString(contextLine))
			}
		}
		matches = append(matches, Match{Path: windowsCString(path), Line: int(line), Text: windowsCString(text), ContextBefore: before, ContextAfter: after, Definition: definition != 0})
	}
	fallbackPtr, _, _ := n.bridge.grepFallback.Call(payload)
	fallback := ""
	if fallbackPtr != 0 {
		fallback = windowsCString(fallbackPtr)
	}
	return GrepResult{Matches: matches, TotalFiles: int(totalFiles), NextFilePage: int(nextOffset), RegexFallbackError: fallback}, nil
}

func (n *nativeFinder) consumeEnvelope(result uintptr) error {
	if result == 0 {
		return fmt.Errorf("FFF returned no result")
	}
	defer n.bridge.freeResult.Call(result)
	if !n.bridge.success(result) {
		return fmt.Errorf("%s", n.bridge.errorMessage(result))
	}
	return nil
}

func (n *nativeFinder) payload(result uintptr) (uintptr, error) {
	if result == 0 {
		return 0, fmt.Errorf("FFF returned no result")
	}
	defer n.bridge.freeResult.Call(result)
	if !n.bridge.success(result) {
		return 0, fmt.Errorf("%s", n.bridge.errorMessage(result))
	}
	payload, _, _ := n.bridge.resultHandle.Call(result)
	if payload == 0 {
		return 0, fmt.Errorf("FFF returned an empty result payload")
	}
	return payload, nil
}

func boolUintptr(value bool) uintptr {
	if value {
		return 1
	}
	return 0
}

func windowsCString(pointer uintptr) string {
	if pointer == 0 {
		return ""
	}
	length := 0
	for *(*byte)(unsafe.Pointer(pointer + uintptr(length))) != 0 {
		length++
	}
	return string(append([]byte(nil), unsafe.Slice((*byte)(unsafe.Pointer(pointer)), length)...))
}
