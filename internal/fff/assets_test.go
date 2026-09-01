package fff

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestReleaseAssetMatrix(t *testing.T) {
	tests := []struct {
		goos, goarch, libc, name string
	}{
		{"windows", "amd64", "", "c-lib-x86_64-pc-windows-msvc.dll"},
		{"darwin", "arm64", "", "c-lib-aarch64-apple-darwin.dylib"},
		{"linux", "amd64", "gnu", "c-lib-x86_64-unknown-linux-gnu.so"},
		{"linux", "amd64", "musl", "c-lib-x86_64-unknown-linux-musl.so"},
		{"linux", "arm64", "gnu", "c-lib-aarch64-unknown-linux-gnu.so"},
		{"linux", "arm64", "musl", "c-lib-aarch64-unknown-linux-musl.so"},
	}
	for _, test := range tests {
		asset, err := assetFor(test.goos, test.goarch, test.libc)
		if err != nil {
			t.Fatalf("assetFor(%s, %s, %s): %v", test.goos, test.goarch, test.libc, err)
		}
		if asset.Name != test.name || len(asset.SHA256) != 64 {
			t.Fatalf("asset=%#v", asset)
		}
	}
	if _, err := assetFor("windows", "386", ""); err == nil {
		t.Fatal("expected unsupported windows/386 error")
	}
	if _, err := assetFor("linux", "arm", "gnu"); err == nil {
		t.Fatal("expected unsupported linux/arm error")
	}
}

func TestDownloadAssetVerifiesAndAtomicallyInstalls(t *testing.T) {
	content := []byte("prebuilt-fff-library")
	hash := fmt.Sprintf("%x", sha256.Sum256(content))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(content)
	}))
	defer server.Close()
	target := filepath.Join(t.TempDir(), "libfff")
	if err := downloadAsset(context.Background(), server.URL, target, hash); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(target)
	if err != nil || string(b) != string(content) {
		t.Fatalf("content=%q err=%v", b, err)
	}
	if err := downloadAsset(context.Background(), server.URL, target, "0000000000000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Fatal("expected checksum error")
	}
}
