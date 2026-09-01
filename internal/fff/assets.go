package fff

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const (
	ReleaseVersion = "0.10.6"
	releaseBaseURL = "https://github.com/dmtrKovalenko/fff/releases/download/v" + ReleaseVersion
	maxAssetBytes  = 64 << 20
)

type releaseAsset struct {
	Name   string
	SHA256 string
}

var releaseAssets = map[string]releaseAsset{
	"darwin/arm64": {
		Name:   "c-lib-aarch64-apple-darwin.dylib",
		SHA256: "5d66ccbe80d9506ef55f5fa0c3f05fb97a024de4c6d00cb6428d22155cad01aa",
	},
	"windows/amd64": {
		Name:   "c-lib-x86_64-pc-windows-msvc.dll",
		SHA256: "7da6fd485b8d8a3398cc403d37e5f1d0c5d7771840970576298e36e61e13249d",
	},
	"linux/amd64/gnu": {
		Name:   "c-lib-x86_64-unknown-linux-gnu.so",
		SHA256: "c2d5b0acd0c86a412fa4c71ef32e0931c84a1f6022858a9b1bde49fba62ec940",
	},
	"linux/amd64/musl": {
		Name:   "c-lib-x86_64-unknown-linux-musl.so",
		SHA256: "7f4335963f629ec00ac2e4764d16f82d48b973e136afb0102bd22f1e0bd7ac62",
	},
	"linux/arm64/gnu": {
		Name:   "c-lib-aarch64-unknown-linux-gnu.so",
		SHA256: "707c0f2f4e09f79592f4c6f347bc43ccc65c11a543c3ca7bce78502249cb8d0f",
	},
	"linux/arm64/musl": {
		Name:   "c-lib-aarch64-unknown-linux-musl.so",
		SHA256: "2d49d947478494b559493eb01d1e0d6be08e6d74ed7361487a79524592af30f2",
	},
}

var releaseHTTPClient = &http.Client{Timeout: 2 * time.Minute}

func assetFor(goos, goarch, libc string) (releaseAsset, error) {
	key := goos + "/" + goarch
	if goos == "linux" {
		key += "/" + libc
	}
	asset, ok := releaseAssets[key]
	if !ok {
		return releaseAsset{}, fmt.Errorf("FFF %s has no supported C FFI asset for %s", ReleaseVersion, key)
	}
	return asset, nil
}

func resolveLibrary(ctx context.Context, opts Options, libc string) (string, error) {
	if opts.LibraryPath != "" {
		return filepath.Clean(opts.LibraryPath), nil
	}
	asset, err := assetFor(runtime.GOOS, runtime.GOARCH, libc)
	if err != nil {
		return "", err
	}
	cacheDir := opts.CacheDir
	if cacheDir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("resolve FFF cache directory: %w", err)
		}
		cacheDir = filepath.Join(base, "best-harness-go", "fff", "v"+ReleaseVersion)
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("create FFF cache directory: %w", err)
	}
	target := filepath.Join(cacheDir, asset.Name)
	if ok, err := fileHasSHA256(target, asset.SHA256); err == nil && ok {
		return target, nil
	}
	baseURL := opts.releaseBaseURL
	if baseURL == "" {
		baseURL = releaseBaseURL
	}
	if err := downloadAsset(ctx, baseURL+"/"+asset.Name, target, asset.SHA256); err != nil {
		return "", fmt.Errorf("download FFF %s asset %s (set BEST_HARNESS_FFF_LIBRARY for offline use): %w", ReleaseVersion, asset.Name, err)
	}
	return target, nil
}

func downloadAsset(ctx context.Context, url, target, expectedSHA string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "best-harness-go/fff-"+ReleaseVersion)
	resp, err := releaseHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status %s", resp.Status)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".download-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(resp.Body, maxAssetBytes+1))
	closeErr := tmp.Close()
	if copyErr != nil {
		return copyErr
	}
	if written > maxAssetBytes {
		return fmt.Errorf("release asset exceeds %d bytes", maxAssetBytes)
	}
	if closeErr != nil {
		return closeErr
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != expectedSHA {
		return fmt.Errorf("SHA-256 mismatch: got %s, want %s", actual, expectedSHA)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		if ok, hashErr := fileHasSHA256(target, expectedSHA); hashErr == nil && ok {
			return nil
		}
		if removeErr := os.Remove(target); removeErr != nil && !os.IsNotExist(removeErr) {
			return err
		}
		if retryErr := os.Rename(tmpName, target); retryErr != nil {
			return retryErr
		}
	}
	return nil
}

func fileHasSHA256(path, expected string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return false, err
	}
	return hex.EncodeToString(hash.Sum(nil)) == expected, nil
}
