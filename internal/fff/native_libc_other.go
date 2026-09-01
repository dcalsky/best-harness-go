//go:build (darwin && arm64) || (windows && amd64)

package fff

func nativeLibc() string { return "" }
