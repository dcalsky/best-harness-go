//go:build (darwin && arm64) || (linux && (amd64 || arm64)) || (windows && amd64)

package fff

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"
)

func TestCreateOptionsABI(t *testing.T) {
	var options createOptions
	if size := unsafe.Sizeof(options); size != 88 {
		t.Fatalf("size=%d", size)
	}
	offsets := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"basePath", unsafe.Offsetof(options.BasePath), 8},
		{"enableMMapCache", unsafe.Offsetof(options.EnableMMapCache), 32},
		{"logFilePath", unsafe.Offsetof(options.LogFilePath), 40},
		{"cacheBudgetMaxFiles", unsafe.Offsetof(options.CacheBudgetMaxFiles), 56},
		{"enableFSRootScanning", unsafe.Offsetof(options.EnableFSRootScanning), 80},
	}
	for _, offset := range offsets {
		if offset.got != offset.want {
			t.Fatalf("%s offset=%d want=%d", offset.name, offset.got, offset.want)
		}
	}
}

// v0.10.6's fff_wait_for_scan returns success=true even when int_value=0
// (timeout): crates/fff-c/src/lib.rs, fff_wait_for_scan.
func TestWaitForScanResultContract(t *testing.T) {
	for _, tc := range []struct {
		name      string
		result    uintptr
		success   bool
		completed int64
		wantError string
	}{
		{"completed", 2, true, 1, ""},
		{"timeout", 2, true, 0, "timeout"},
		{"native-error", 2, false, 0, "scan failed"},
		{"null-result", 0, false, 0, "FFF returned no result"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			freed := 0
			n := &nativeFinder{handle: 1, bridge: &puregoBridge{
				waitForScan:   func(uintptr, uint64) uintptr { return tc.result },
				resultSuccess: func(uintptr) bool { return tc.success },
				resultInt:     func(uintptr) int64 { return tc.completed },
				resultError:   func(uintptr) string { return "scan failed" },
				freeResult:    func(uintptr) { freed++ },
			}}
			err := n.waitForScan(time.Millisecond)
			switch tc.wantError {
			case "":
				if err != nil {
					t.Fatal(err)
				}
			case "timeout":
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("timeout error=%v", err)
				}
			default:
				if err == nil || err.Error() != tc.wantError {
					t.Fatalf("error=%v want=%s", err, tc.wantError)
				}
			}
			wantFreed := 1
			if tc.result == 0 {
				wantFreed = 0
			}
			if freed != wantFreed {
				t.Fatalf("freed=%d want=%d", freed, wantFreed)
			}
		})
	}
}

// v0.10.6 marks the filesystem scan complete before post-scan content
// indexing finishes. Cursor pagination must not begin until both are stable.
func TestWaitForIndexingWaitsForPostScanWatcher(t *testing.T) {
	watcherCalls := 0
	n := &nativeFinder{handle: 1, bridge: &puregoBridge{
		waitForScan:   func(uintptr, uint64) uintptr { return 1 },
		waitForWatch:  func(uintptr, uint64) uintptr { watcherCalls++; return 2 },
		resultSuccess: func(uintptr) bool { return true },
		resultInt:     func(uintptr) int64 { return 1 },
		freeResult:    func(uintptr) {},
	}}
	if err := n.waitForIndexing(time.Second); err != nil {
		t.Fatal(err)
	}
	if watcherCalls != 1 {
		t.Fatalf("watcher waits=%d", watcherCalls)
	}
}

func TestWaitForIndexingReportsWatcherTimeout(t *testing.T) {
	n := &nativeFinder{handle: 1, bridge: &puregoBridge{
		waitForScan:   func(uintptr, uint64) uintptr { return 1 },
		waitForWatch:  func(uintptr, uint64) uintptr { return 2 },
		resultSuccess: func(uintptr) bool { return true },
		resultInt: func(result uintptr) int64 {
			if result == 2 {
				return 0
			}
			return 1
		},
		freeResult: func(uintptr) {},
	}}
	if err := n.waitForIndexing(time.Second); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
}

func TestGrepNativeBudget(t *testing.T) {
	for _, tc := range []struct {
		name     string
		budget   time.Duration
		deadline bool
		want     uint64
	}{
		{"unlimited", 0, false, 0},
		{"sub-millisecond", time.Nanosecond, false, 1},
		{"explicit", 5 * time.Millisecond, false, 5},
		{"deadline", 0, true, 0},
		{"deadline-caps-budget", time.Hour, true, 0},
		{"shorter-budget", time.Millisecond, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.deadline {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Second)
				defer cancel()
			}
			called := false
			f := &Finder{native: &nativeFinder{handle: 1, bridge: &puregoBridge{
				grep: func(_ uintptr, _ string, _ uint8, _ uint64, _ uint32, _ bool, _ uint32, _ uint32, budget uint64, _ uint32, _ uint32, _ bool) uintptr {
					called = true
					if tc.deadline && tc.want == 0 {
						if budget == 0 || budget > 1000 {
							t.Errorf("deadline budget=%d", budget)
						}
					} else if budget != tc.want {
						t.Errorf("budget=%d want=%d", budget, tc.want)
					}
					return 0 // No payload needed to observe the native argument.
				},
			}}}
			_, _ = f.grep(ctx, GrepOptions{TimeBudget: tc.budget})
			if !called {
				t.Fatal("native grep not called")
			}
		})
	}
}

// Exercise the integer getter against the pinned library, independently of
// waitForScan's Go result conversion. The contract is in v0.10.6/include/fff.h.
func TestNativeScanCompletionPayload(t *testing.T) {
	library := os.Getenv("BEST_HARNESS_FFF_LIBRARY")
	if library == "" && os.Getenv("BEST_HARNESS_FFF_INTEGRATION") == "" {
		t.Skip("set BEST_HARNESS_FFF_INTEGRATION=1 to test the pinned release asset")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "probe.txt"), []byte("probe"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n, err := nativeOpen(ctx, root, Options{LibraryPath: library})
	if err != nil {
		t.Fatal(err)
	}
	defer n.close()
	result := n.bridge.waitForScan(n.handle, 10000)
	if result == 0 {
		t.Fatal("null scan result")
	}
	defer n.bridge.freeResult(result)
	if !n.bridge.resultSuccess(result) {
		t.Fatal(n.bridge.errorMessage(result))
	}
	if got := n.bridge.resultInt(result); got != 1 {
		t.Fatalf("completion payload=%d want=1", got)
	}
	if err := n.waitForScan(time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestGrepCancellationSkipsNativeCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// An empty bridge makes any unexpected native call fail the test.
	f := &Finder{native: &nativeFinder{bridge: &puregoBridge{}}}
	if _, err := f.grep(ctx, GrepOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}
