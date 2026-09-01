//go:build linux && (amd64 || arm64)

package fff

import "github.com/ebitengine/purego"

func nativeLibc() string {
	handle, err := purego.Dlopen("libc.so.6", purego.RTLD_LAZY|purego.RTLD_LOCAL)
	if err != nil {
		return "musl"
	}
	_ = purego.Dlclose(handle)
	return "gnu"
}
