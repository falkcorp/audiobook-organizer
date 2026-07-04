// file: internal/openlibrary/downloader.go
// version: 1.1.2
// guid: b2c3d4e5-f6a7-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-07-03

package openlibrary

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Download source URLs — try direct first, then Internet Archive mirror.
var DumpSources = []string{
	"https://openlibrary.org/data",
	"https://archive.org/download/ol_exports",
}

// DumpFilename returns the expected filename for a dump type.
func DumpFilename(dumpType string) string {
	return fmt.Sprintf("ol_dump_%s_latest.txt.gz", dumpType)
}

// DumpURL returns the primary download URL for a dump type.
func DumpURL(dumpType string) string {
	return fmt.Sprintf("%s/%s", DumpSources[0], DumpFilename(dumpType))
}

// DownloadProgress tracks live download state for a single dump type.
type DownloadProgress struct {
	DumpType   string    `json:"dump_type"`
	Status     string    `json:"status"` // "idle", "downloading", "complete", "error"
	Downloaded int64     `json:"downloaded"`
	TotalSize  int64     `json:"total_size"` // -1 if unknown
	Error      string    `json:"error,omitempty"`
	Source     string    `json:"source,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
}

// DownloadTracker holds live progress for all download types.
type DownloadTracker struct {
	mu       sync.RWMutex
	progress map[string]*DownloadProgress
}

// NewDownloadTracker creates a new tracker.
func NewDownloadTracker() *DownloadTracker {
	return &DownloadTracker{
		progress: make(map[string]*DownloadProgress),
	}
}

// Get returns the current progress for a dump type.
func (dt *DownloadTracker) Get(dumpType string) *DownloadProgress {
	dt.mu.RLock()
	defer dt.mu.RUnlock()
	if p, ok := dt.progress[dumpType]; ok {
		cp := *p
		return &cp
	}
	return &DownloadProgress{DumpType: dumpType, Status: "idle", TotalSize: -1}
}

// GetAll returns progress for all tracked types.
func (dt *DownloadTracker) GetAll() map[string]*DownloadProgress {
	dt.mu.RLock()
	defer dt.mu.RUnlock()
	result := make(map[string]*DownloadProgress, len(dt.progress))
	for k, v := range dt.progress {
		cp := *v
		result[k] = &cp
	}
	return result
}

func (dt *DownloadTracker) set(dumpType string, p *DownloadProgress) {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	dt.progress[dumpType] = p
}

// DownloadDump downloads an Open Library data dump to the target directory.
// Tries each source URL in order, falling back on failure.
// Progress is tracked in the provided tracker (may be nil).
func DownloadDump(dumpType string, targetDir string, tracker *DownloadTracker) error {
	if err := os.MkdirAll(targetDir, 0o775); err != nil {
		return fmt.Errorf("failed to create target dir: %w", err)
	}

	filename := DumpFilename(dumpType)
	targetPath := filepath.Join(targetDir, filename)

	var lastErr error
	for _, baseURL := range DumpSources {
		sourceURL := fmt.Sprintf("%s/%s", baseURL, filename)
		slog.Info("Trying OL dump download from", "sourceURL", sourceURL)

		err := downloadFromURL(dumpType, sourceURL, targetPath, tracker)
		if err == nil {
			return nil
		}
		slog.Warn("Download from failed, trying next source", "sourceURL", sourceURL, "err", err)
		lastErr = err
	}

	errMsg := fmt.Sprintf("all download sources failed: %v", lastErr)
	if tracker != nil {
		tracker.set(dumpType, &DownloadProgress{
			DumpType: dumpType, Status: "error", Error: errMsg, TotalSize: -1,
		})
	}
	return fmt.Errorf("%s", errMsg)
}

func downloadFromURL(dumpType, sourceURL, targetPath string, tracker *DownloadTracker) error {
	// Check if partial file exists for resume
	var existingSize int64
	if info, err := os.Stat(targetPath); err == nil {
		existingSize = info.Size()
	}

	req, err := http.NewRequest("GET", sourceURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	if existingSize > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existingSize))
	}

	client := &http.Client{Timeout: 0} // no timeout for large downloads
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		existingSize = 0
	case http.StatusPartialContent:
		// resume OK
	case http.StatusRequestedRangeNotSatisfiable:
		if tracker != nil {
			tracker.set(dumpType, &DownloadProgress{
				DumpType: dumpType, Status: "complete",
				Downloaded: existingSize, TotalSize: existingSize, Source: sourceURL,
			})
		}
		return nil
	default:
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, sourceURL)
	}

	var totalSize int64 = -1
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		if n, err := strconv.ParseInt(cl, 10, 64); err == nil {
			totalSize = n + existingSize
		}
	}

	if tracker != nil {
		tracker.set(dumpType, &DownloadProgress{
			DumpType: dumpType, Status: "downloading",
			Downloaded: existingSize, TotalSize: totalSize,
			Source: sourceURL, StartedAt: time.Now(),
		})
	}

	flags := os.O_CREATE | os.O_WRONLY
	if existingSize > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(targetPath, flags, 0o644)
	if err != nil {
		return fmt.Errorf("failed to open target file: %w", err)
	}
	defer f.Close()

	buf := make([]byte, 256*1024)
	downloaded := existingSize
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := f.Write(buf[:n]); writeErr != nil {
				return fmt.Errorf("failed to write: %w", writeErr)
			}
			downloaded += int64(n)
			if tracker != nil {
				tracker.set(dumpType, &DownloadProgress{
					DumpType: dumpType, Status: "downloading",
					Downloaded: downloaded, TotalSize: totalSize,
					Source: sourceURL, StartedAt: time.Now(),
				})
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return fmt.Errorf("download interrupted: %w", readErr)
		}
	}

	if tracker != nil {
		tracker.set(dumpType, &DownloadProgress{
			DumpType: dumpType, Status: "complete",
			Downloaded: downloaded, TotalSize: downloaded, Source: sourceURL,
		})
	}

	return nil
}
