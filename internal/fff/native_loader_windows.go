//go:build windows && amd64

package fff

import "syscall"

func openNativeLibrary(path string) (uintptr, error) {
	handle, err := syscall.LoadLibrary(path)
	return uintptr(handle), err
}
