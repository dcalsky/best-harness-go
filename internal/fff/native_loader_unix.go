//go:build (darwin && arm64) || (linux && (amd64 || arm64))

package fff

import "github.com/ebitengine/purego"

func openNativeLibrary(path string) (uintptr, error) {
	return purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
}
