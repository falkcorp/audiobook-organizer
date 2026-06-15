// file: internal/tools/downloader.go
// version: 1.0.0
// guid: d0e1f2a3-b4c5-6789-defa-789012345678
// last-edited: 2026-06-15

package tools

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
	"strings"
)

// ProgressFunc is called periodically during download with bytes written so far.
type ProgressFunc func(bytesWritten int64)

// Download fetches the binary for def, verifies its SHA256, and installs it
// atomically to destDir/<name>/<version>/<name>. Returns the final path.
// progress may be nil.
func Download(ctx context.Context, def ToolDef, destDir string, progress ProgressFunc) (string, error) {
	platform := runtime.GOOS + "/" + runtime.GOARCH
	expectedSum, ok := def.Release.SHA256[platform]
	if !ok {
		return "", fmt.Errorf("tools: no SHA256 for platform %s (tool %s)", platform, def.Name)
	}

	url := buildURL(def.Release.URLTemplate, def.Release.Version)

	dir := filepath.Join(destDir, def.Name, def.Release.Version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("tools: mkdir %s: %w", dir, err)
	}

	finalPath := filepath.Join(dir, def.Name)
	tmpPath := finalPath + ".tmp"

	if err := downloadToFile(ctx, url, tmpPath, expectedSum, progress); err != nil {
		os.Remove(tmpPath)
		return "", err
	}

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("tools: chmod %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("tools: rename to %s: %w", finalPath, err)
	}
	return finalPath, nil
}

// StatFile is a thin os.Stat wrapper used by the install handler.
func StatFile(path string) (os.FileInfo, error) { return os.Stat(path) }

func buildURL(template, version string) string {
	arch := runtime.GOARCH
	u := strings.ReplaceAll(template, "{VERSION}", version)
	u = strings.ReplaceAll(u, "{ARCH}", arch)
	return u
}

func downloadToFile(ctx context.Context, url, path, expectedHex string, progress ProgressFunc) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("tools: build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("tools: GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tools: GET %s returned %d", url, resp.StatusCode)
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("tools: create %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	var written int64
	buf := make([]byte, 32*1024)
	r := io.TeeReader(resp.Body, h)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return fmt.Errorf("tools: write: %w", werr)
			}
			written += int64(n)
			if progress != nil {
				progress(written)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tools: read body: %w", err)
		}
	}

	got := hex.EncodeToString(h.Sum(nil))
	if got != expectedHex {
		return fmt.Errorf("tools: checksum mismatch for %s: got %s want %s", path, got, expectedHex)
	}
	return nil
}
