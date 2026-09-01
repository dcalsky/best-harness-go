//go:build (darwin && arm64) || (linux && (amd64 || arm64)) || (windows && amd64)

package fff

import (
	"testing"
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
