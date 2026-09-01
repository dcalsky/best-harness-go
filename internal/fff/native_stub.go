//go:build (!cgo && (darwin || linux)) || (!darwin && !linux && !windows) || (windows && !amd64)

package fff

import (
	"context"
	"fmt"
	"time"
)

type nativeFinder struct{}

func nativeOpen(context.Context, string, Options) (*nativeFinder, error) {
	return nil, fmt.Errorf("FFF requires cgo on darwin/linux and supports windows/amd64")
}

func (*nativeFinder) close() {}

func (*nativeFinder) waitForScan(time.Duration) error { return nil }

func (*nativeFinder) search(string, int, int) (FindResult, error) {
	return FindResult{}, fmt.Errorf("FFF is unavailable")
}

func (*nativeFinder) glob(string, int, int) (FindResult, error) {
	return FindResult{}, fmt.Errorf("FFF is unavailable")
}

func (*nativeFinder) grep(GrepOptions) (GrepResult, error) {
	return GrepResult{}, fmt.Errorf("FFF is unavailable")
}
