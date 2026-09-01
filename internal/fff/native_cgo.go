//go:build cgo && (darwin || linux)

package fff

/*
#cgo linux LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

typedef struct FffResult FffResult;

typedef struct FffCreateOptions {
  uint32_t version;
  const char *base_path;
  const char *frecency_db_path;
  const char *history_db_path;
  bool enable_mmap_cache;
  bool enable_content_indexing;
  bool watch;
  bool ai_mode;
  const char *log_file_path;
  const char *log_level;
  uint64_t cache_budget_max_files;
  uint64_t cache_budget_max_bytes;
  uint64_t cache_budget_max_file_size;
  bool enable_fs_root_scanning;
  bool enable_home_dir_scanning;
  bool follow_symlinks;
} FffCreateOptions;

typedef struct FffFileItem {
  char *relative_path;
  char *file_name;
  char *git_status;
  uint64_t size;
  uint64_t modified;
  int64_t access_frecency_score;
  int64_t modification_frecency_score;
  int64_t total_frecency_score;
  bool is_binary;
} FffFileItem;

typedef struct FffSearchResult {
  FffFileItem *items;
  void *scores;
  uint32_t count;
  uint32_t total_matched;
  uint32_t total_files;
  uint8_t location[20];
} FffSearchResult;

typedef struct FffGrepMatch {
  char *relative_path;
  char *file_name;
  char *git_status;
  char *line_content;
  void *match_ranges;
  char **context_before;
  char **context_after;
  uint64_t size;
  uint64_t modified;
  int64_t total_frecency_score;
  int64_t access_frecency_score;
  int64_t modification_frecency_score;
  uint64_t line_number;
  uint64_t byte_offset;
  uint32_t col;
  uint32_t match_ranges_count;
  uint32_t context_before_count;
  uint32_t context_after_count;
  uint16_t fuzzy_score;
  bool has_fuzzy_score;
  bool is_binary;
  bool is_definition;
} FffGrepMatch;

typedef struct FffGrepResult {
  FffGrepMatch *items;
  uint32_t count;
  uint32_t total_matched;
  uint32_t total_files_searched;
  uint32_t total_files;
  uint32_t filtered_file_count;
  uint32_t next_file_offset;
  char *regex_fallback_error;
} FffGrepResult;

typedef FffResult *(*fff_create_fn)(const FffCreateOptions *);
typedef void (*fff_destroy_fn)(void *);
typedef FffResult *(*fff_wait_fn)(void *, uint64_t);
typedef FffResult *(*fff_search_fn)(void *, const char *, const char *, uint32_t, uint32_t, uint32_t, int32_t, uint32_t);
typedef FffResult *(*fff_glob_fn)(void *, const char *, const char *, uint32_t, uint32_t, uint32_t);
typedef FffResult *(*fff_grep_fn)(void *, const char *, uint8_t, uint64_t, uint32_t, bool, uint32_t, uint32_t, uint64_t, uint32_t, uint32_t, bool);
typedef void (*fff_free_result_fn)(FffResult *);
typedef void (*fff_free_search_fn)(FffSearchResult *);
typedef void (*fff_free_grep_fn)(FffGrepResult *);
typedef bool (*fff_result_success_fn)(const FffResult *);
typedef const char *(*fff_result_error_fn)(const FffResult *);
typedef void *(*fff_result_handle_fn)(const FffResult *);
typedef int64_t (*fff_result_int_fn)(const FffResult *);
typedef const FffFileItem *(*fff_search_item_fn)(const FffSearchResult *, uint32_t);
typedef uint32_t (*fff_search_count_fn)(const FffSearchResult *);
typedef const char *(*fff_file_path_fn)(const FffFileItem *);
typedef const FffGrepMatch *(*fff_grep_match_fn)(const FffGrepResult *, uint32_t);
typedef uint32_t (*fff_grep_count_fn)(const FffGrepResult *);
typedef const char *(*fff_grep_string_fn)(const FffGrepMatch *);
typedef uint64_t (*fff_grep_line_fn)(const FffGrepMatch *);
typedef bool (*fff_grep_bool_fn)(const FffGrepMatch *);
typedef uint32_t (*fff_grep_context_count_fn)(const FffGrepMatch *);
typedef const char *(*fff_grep_context_fn)(const FffGrepMatch *, uint32_t);
typedef const char *(*fff_grep_fallback_fn)(const FffGrepResult *);

typedef struct FFFBridge {
  void *library;
  fff_create_fn create;
  fff_destroy_fn destroy;
  fff_wait_fn wait_for_scan;
  fff_search_fn search;
  fff_glob_fn glob;
  fff_grep_fn grep;
  fff_free_result_fn free_result;
  fff_free_search_fn free_search;
  fff_free_grep_fn free_grep;
  fff_result_success_fn result_success;
  fff_result_error_fn result_error;
  fff_result_handle_fn result_handle;
  fff_result_int_fn result_int;
  fff_search_item_fn search_item;
  fff_search_count_fn search_count;
  fff_search_count_fn search_total;
  fff_file_path_fn file_path;
  fff_grep_match_fn grep_match;
  fff_grep_count_fn grep_count;
  fff_grep_count_fn grep_total_files;
  fff_grep_count_fn grep_next_offset;
  fff_grep_string_fn grep_path;
  fff_grep_string_fn grep_text;
  fff_grep_line_fn grep_line;
  fff_grep_bool_fn grep_definition;
  fff_grep_context_count_fn grep_before_count;
  fff_grep_context_count_fn grep_after_count;
  fff_grep_context_fn grep_before;
  fff_grep_context_fn grep_after;
  fff_grep_fallback_fn grep_fallback;
} FFFBridge;

static char *bridge_error(const char *message) {
  if (!message) message = "unknown FFF error";
  size_t size = strlen(message) + 1;
  char *copy = malloc(size);
  if (copy) memcpy(copy, message, size);
  return copy;
}

#define LOAD_SYMBOL(bridge, field, symbol) do { \
  *(void **)(&(bridge)->field) = dlsym((bridge)->library, symbol); \
  if (!(bridge)->field) { \
    if (err_out) *err_out = bridge_error(dlerror()); \
    dlclose((bridge)->library); \
    free(bridge); \
    return NULL; \
  } \
} while (0)

static FFFBridge *bridge_load(const char *library_path, char **err_out) {
  if (err_out) *err_out = NULL;
  void *library = dlopen(library_path, RTLD_NOW | RTLD_LOCAL);
  if (!library) {
    if (err_out) *err_out = bridge_error(dlerror());
    return NULL;
  }
  FFFBridge *bridge = calloc(1, sizeof(FFFBridge));
  if (!bridge) {
    dlclose(library);
    if (err_out) *err_out = bridge_error("out of memory");
    return NULL;
  }
  bridge->library = library;
  LOAD_SYMBOL(bridge, create, "fff_create_instance_with");
  LOAD_SYMBOL(bridge, destroy, "fff_destroy");
  LOAD_SYMBOL(bridge, wait_for_scan, "fff_wait_for_scan");
  LOAD_SYMBOL(bridge, search, "fff_search");
  LOAD_SYMBOL(bridge, glob, "fff_glob");
  LOAD_SYMBOL(bridge, grep, "fff_live_grep");
  LOAD_SYMBOL(bridge, free_result, "fff_free_result");
  LOAD_SYMBOL(bridge, free_search, "fff_free_search_result");
  LOAD_SYMBOL(bridge, free_grep, "fff_free_grep_result");
  LOAD_SYMBOL(bridge, result_success, "fff_result_get_success");
  LOAD_SYMBOL(bridge, result_error, "fff_result_get_error");
  LOAD_SYMBOL(bridge, result_handle, "fff_result_get_handle");
  LOAD_SYMBOL(bridge, result_int, "fff_result_get_int_value");
  LOAD_SYMBOL(bridge, search_item, "fff_search_result_get_item");
  LOAD_SYMBOL(bridge, search_count, "fff_search_result_get_count");
  LOAD_SYMBOL(bridge, search_total, "fff_search_result_get_total_matched");
  LOAD_SYMBOL(bridge, file_path, "fff_file_item_get_relative_path");
  LOAD_SYMBOL(bridge, grep_match, "fff_grep_result_get_match");
  LOAD_SYMBOL(bridge, grep_count, "fff_grep_result_get_count");
  LOAD_SYMBOL(bridge, grep_total_files, "fff_grep_result_get_total_files_searched");
  LOAD_SYMBOL(bridge, grep_next_offset, "fff_grep_result_get_next_file_offset");
  LOAD_SYMBOL(bridge, grep_path, "fff_grep_match_get_relative_path");
  LOAD_SYMBOL(bridge, grep_text, "fff_grep_match_get_line_content");
  LOAD_SYMBOL(bridge, grep_line, "fff_grep_match_get_line_number");
  LOAD_SYMBOL(bridge, grep_definition, "fff_grep_match_get_is_definition");
  LOAD_SYMBOL(bridge, grep_before_count, "fff_grep_match_get_context_before_count");
  LOAD_SYMBOL(bridge, grep_after_count, "fff_grep_match_get_context_after_count");
  LOAD_SYMBOL(bridge, grep_before, "fff_grep_match_get_context_before");
  LOAD_SYMBOL(bridge, grep_after, "fff_grep_match_get_context_after");
  LOAD_SYMBOL(bridge, grep_fallback, "fff_grep_result_get_regex_fallback_error");
  return bridge;
}

static void bridge_unload(FFFBridge *bridge) {
  if (!bridge) return;
  if (bridge->library) dlclose(bridge->library);
  free(bridge);
}

static void *bridge_create(FFFBridge *bridge, const char *path, char **err_out) {
  if (err_out) *err_out = NULL;
  FffCreateOptions options = {0};
  options.version = 2;
  options.base_path = path;
  options.enable_mmap_cache = true;
  options.enable_content_indexing = true;
  options.watch = true;
  options.ai_mode = true;
  FffResult *result = bridge->create(&options);
  if (!result || !bridge->result_success(result)) {
    if (err_out) *err_out = bridge_error(result ? bridge->result_error(result) : "FFF returned no result");
    if (result) bridge->free_result(result);
    return NULL;
  }
  void *handle = bridge->result_handle(result);
  bridge->free_result(result);
  return handle;
}

static int bridge_wait(FFFBridge *bridge, void *handle, uint64_t timeout_ms, char **err_out) {
  if (err_out) *err_out = NULL;
  FffResult *result = bridge->wait_for_scan(handle, timeout_ms);
  if (!result || !bridge->result_success(result)) {
    if (err_out) *err_out = bridge_error(result ? bridge->result_error(result) : "FFF returned no result");
    if (result) bridge->free_result(result);
    return 0;
  }
  bridge->free_result(result);
  return 1;
}

static int bridge_success(FFFBridge *bridge, FffResult *result) {
  return result && bridge->result_success(result);
}

static char *bridge_result_error(FFFBridge *bridge, FffResult *result) {
  return bridge_error(result ? bridge->result_error(result) : "FFF returned no result");
}

static void *bridge_result_handle(FFFBridge *bridge, FffResult *result) {
  return result ? bridge->result_handle(result) : NULL;
}

static FffResult *bridge_search(FFFBridge *bridge, void *handle, const char *query, uint32_t page, uint32_t limit) {
  return bridge->search(handle, query, NULL, 0, page, limit, 0, 0);
}

static FffResult *bridge_glob(FFFBridge *bridge, void *handle, const char *query, uint32_t page, uint32_t limit) {
  return bridge->glob(handle, query, NULL, 0, page, limit);
}

static FffResult *bridge_grep(FFFBridge *bridge, void *handle, const char *query, uint8_t mode, bool smart_case, uint32_t file_offset, uint32_t limit, uint32_t max_per_file, uint64_t time_budget_ms, uint32_t before_context, uint32_t after_context) {
  return bridge->grep(handle, query, mode, 10 * 1024 * 1024, max_per_file, smart_case, file_offset, limit, time_budget_ms, before_context, after_context, true);
}

static void bridge_destroy(FFFBridge *bridge, void *handle) { bridge->destroy(handle); }
static void bridge_free_result(FFFBridge *bridge, FffResult *result) { bridge->free_result(result); }
static void bridge_free_search(FFFBridge *bridge, FffSearchResult *result) { bridge->free_search(result); }
static void bridge_free_grep(FFFBridge *bridge, FffGrepResult *result) { bridge->free_grep(result); }

static uint32_t bridge_search_count(FFFBridge *bridge, FffSearchResult *result) { return bridge->search_count(result); }
static uint32_t bridge_search_total(FFFBridge *bridge, FffSearchResult *result) { return bridge->search_total(result); }
static const char *bridge_search_path(FFFBridge *bridge, FffSearchResult *result, uint32_t index) {
  const FffFileItem *item = bridge->search_item(result, index);
  return bridge->file_path(item);
}
static uint32_t bridge_grep_count(FFFBridge *bridge, FffGrepResult *result) { return bridge->grep_count(result); }
static uint32_t bridge_grep_total_files(FFFBridge *bridge, FffGrepResult *result) { return bridge->grep_total_files(result); }
static uint32_t bridge_grep_next_offset(FFFBridge *bridge, FffGrepResult *result) { return bridge->grep_next_offset(result); }
static const char *bridge_grep_path(FFFBridge *bridge, FffGrepResult *result, uint32_t index) {
  return bridge->grep_path(bridge->grep_match(result, index));
}
static const char *bridge_grep_text(FFFBridge *bridge, FffGrepResult *result, uint32_t index) {
  return bridge->grep_text(bridge->grep_match(result, index));
}
static uint64_t bridge_grep_line(FFFBridge *bridge, FffGrepResult *result, uint32_t index) {
  return bridge->grep_line(bridge->grep_match(result, index));
}
static int bridge_grep_definition(FFFBridge *bridge, FffGrepResult *result, uint32_t index) {
  return bridge->grep_definition(bridge->grep_match(result, index));
}
static uint32_t bridge_grep_before_count(FFFBridge *bridge, FffGrepResult *result, uint32_t index) { return bridge->grep_before_count(bridge->grep_match(result, index)); }
static uint32_t bridge_grep_after_count(FFFBridge *bridge, FffGrepResult *result, uint32_t index) { return bridge->grep_after_count(bridge->grep_match(result, index)); }
static const char *bridge_grep_before(FFFBridge *bridge, FffGrepResult *result, uint32_t index, uint32_t context_index) { return bridge->grep_before(bridge->grep_match(result, index), context_index); }
static const char *bridge_grep_after(FFFBridge *bridge, FffGrepResult *result, uint32_t index, uint32_t context_index) { return bridge->grep_after(bridge->grep_match(result, index), context_index); }
static const char *bridge_grep_fallback(FFFBridge *bridge, FffGrepResult *result) { return bridge->grep_fallback(result); }
static int bridge_libc_is_glibc(void) {
#if defined(__linux__) && defined(__GLIBC__)
  return 1;
#else
  return 0;
#endif
}
*/
import "C"

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"
	"unsafe"
)

type nativeFinder struct {
	bridge *C.FFFBridge
	handle unsafe.Pointer
}

func nativeOpen(ctx context.Context, root string, opts Options) (*nativeFinder, error) {
	libc := ""
	if runtime.GOOS == "linux" {
		if C.bridge_libc_is_glibc() != 0 {
			libc = "gnu"
		} else {
			libc = "musl"
		}
	}
	libraryPath, err := resolveLibrary(ctx, opts, libc)
	if err != nil {
		return nil, err
	}
	cPath := C.CString(libraryPath)
	var cErr *C.char
	bridge := C.bridge_load(cPath, &cErr)
	C.free(unsafe.Pointer(cPath))
	if bridge == nil {
		return nil, fmt.Errorf("load FFF %s C library %s: %w", ReleaseVersion, libraryPath, takeError(cErr))
	}
	cRoot := C.CString(root)
	var createErr *C.char
	handle := C.bridge_create(bridge, cRoot, &createErr)
	C.free(unsafe.Pointer(cRoot))
	if handle == nil {
		err := takeError(createErr)
		C.bridge_unload(bridge)
		return nil, err
	}
	return &nativeFinder{bridge: bridge, handle: handle}, nil
}

func (n *nativeFinder) close() {
	if n == nil || n.bridge == nil {
		return
	}
	if n.handle != nil {
		C.bridge_destroy(n.bridge, n.handle)
		n.handle = nil
	}
	C.bridge_unload(n.bridge)
	n.bridge = nil
}

func (n *nativeFinder) waitForScan(timeout time.Duration) error {
	var cErr *C.char
	if C.bridge_wait(n.bridge, n.handle, C.uint64_t(timeout.Milliseconds()), &cErr) == 0 {
		return takeError(cErr)
	}
	return nil
}

func (n *nativeFinder) search(query string, page, limit int) (FindResult, error) {
	cQuery := C.CString(query)
	result := C.bridge_search(n.bridge, n.handle, cQuery, C.uint32_t(page), C.uint32_t(limit))
	C.free(unsafe.Pointer(cQuery))
	return n.collectFind(result)
}

func (n *nativeFinder) glob(query string, page, limit int) (FindResult, error) {
	cQuery := C.CString(query)
	result := C.bridge_glob(n.bridge, n.handle, cQuery, C.uint32_t(page), C.uint32_t(limit))
	C.free(unsafe.Pointer(cQuery))
	return n.collectFind(result)
}

func (n *nativeFinder) collectFind(result *C.FffResult) (FindResult, error) {
	if C.bridge_success(n.bridge, result) == 0 {
		err := takeError(C.bridge_result_error(n.bridge, result))
		if result != nil {
			C.bridge_free_result(n.bridge, result)
		}
		return FindResult{}, err
	}
	payload := (*C.FffSearchResult)(C.bridge_result_handle(n.bridge, result))
	defer C.bridge_free_result(n.bridge, result)
	defer C.bridge_free_search(n.bridge, payload)
	count := int(C.bridge_search_count(n.bridge, payload))
	files := make([]File, 0, count)
	for i := 0; i < count; i++ {
		if path := C.bridge_search_path(n.bridge, payload, C.uint32_t(i)); path != nil {
			files = append(files, File{Path: C.GoString(path)})
		}
	}
	return FindResult{Files: files, TotalMatched: int(C.bridge_search_total(n.bridge, payload))}, nil
}

func (n *nativeFinder) grep(opts GrepOptions) (GrepResult, error) {
	query := strings.TrimSpace(strings.Join([]string{opts.Constraints, opts.Pattern}, " "))
	maxPerFile := opts.MaxPerFile
	if maxPerFile <= 0 {
		maxPerFile = 200
	}
	cQuery := C.CString(query)
	result := C.bridge_grep(n.bridge, n.handle, cQuery, C.uint8_t(opts.Mode), C.bool(opts.SmartCase), C.uint32_t(opts.FileOffset), C.uint32_t(opts.Limit), C.uint32_t(maxPerFile), C.uint64_t(opts.TimeBudget.Milliseconds()), C.uint32_t(opts.BeforeContext), C.uint32_t(opts.AfterContext))
	C.free(unsafe.Pointer(cQuery))
	if C.bridge_success(n.bridge, result) == 0 {
		err := takeError(C.bridge_result_error(n.bridge, result))
		if result != nil {
			C.bridge_free_result(n.bridge, result)
		}
		return GrepResult{}, err
	}
	payload := (*C.FffGrepResult)(C.bridge_result_handle(n.bridge, result))
	defer C.bridge_free_result(n.bridge, result)
	defer C.bridge_free_grep(n.bridge, payload)
	count := int(C.bridge_grep_count(n.bridge, payload))
	matches := make([]Match, 0, count)
	for i := 0; i < count; i++ {
		index := C.uint32_t(i)
		path := C.bridge_grep_path(n.bridge, payload, index)
		text := C.bridge_grep_text(n.bridge, payload, index)
		if path == nil || text == nil {
			continue
		}
		before := make([]string, 0, int(C.bridge_grep_before_count(n.bridge, payload, index)))
		for contextIndex := C.uint32_t(0); contextIndex < C.bridge_grep_before_count(n.bridge, payload, index); contextIndex++ {
			if line := C.bridge_grep_before(n.bridge, payload, index, contextIndex); line != nil {
				before = append(before, C.GoString(line))
			}
		}
		after := make([]string, 0, int(C.bridge_grep_after_count(n.bridge, payload, index)))
		for contextIndex := C.uint32_t(0); contextIndex < C.bridge_grep_after_count(n.bridge, payload, index); contextIndex++ {
			if line := C.bridge_grep_after(n.bridge, payload, index, contextIndex); line != nil {
				after = append(after, C.GoString(line))
			}
		}
		matches = append(matches, Match{
			Path:          C.GoString(path),
			Line:          int(C.bridge_grep_line(n.bridge, payload, index)),
			Text:          C.GoString(text),
			ContextBefore: before,
			ContextAfter:  after,
			Definition:    C.bridge_grep_definition(n.bridge, payload, index) != 0,
		})
	}
	fallback := ""
	if value := C.bridge_grep_fallback(n.bridge, payload); value != nil {
		fallback = C.GoString(value)
	}
	return GrepResult{
		Matches:            matches,
		TotalFiles:         int(C.bridge_grep_total_files(n.bridge, payload)),
		NextFilePage:       int(C.bridge_grep_next_offset(n.bridge, payload)),
		RegexFallbackError: fallback,
	}, nil
}

func takeError(value *C.char) error {
	if value == nil {
		return fmt.Errorf("unknown FFF error")
	}
	defer C.free(unsafe.Pointer(value))
	return fmt.Errorf("%s", C.GoString(value))
}
