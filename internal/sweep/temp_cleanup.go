// file: internal/sweep/temp_cleanup.go
// version: 1.0.2
// guid: f7e6d5c4-b3a2-1908-7654-321fedcba987
// last-edited: 2026-07-03

package sweep

import (
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
)

// CleanupOrphanedTempFiles removes *.tmp.m4b, *.tmp.m4a, *.tmp.mp3, and
// *.remux.tmp files left behind by ffmpeg operations that were interrupted
// by a crash or server restart. Returns the number of files removed.
// w and opID are optional — if provided, each removal is submitted to the
// activity batcher instead of emitting a per-file log line.
func CleanupOrphanedTempFiles(root string, w *activity.Writer, opID string) int {
	if root == "" {
		return 0
	}

	removed := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := filepath.Base(path)
		if isOrphanedTempFile(name) {
			if rmErr := os.Remove(path); rmErr != nil {
				slog.Warn("temp file cleanup could not remove", "path", path, "rmErr", rmErr)
			} else {
				removed++
				activity.LogBatch(w, opID, "temp-file-cleanup", "temp-file-cleanup",
					activity.BatchItem{Name: name, Detail: path})
			}
		}
		return nil
	})
	return removed
}

func isOrphanedTempFile(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, ".tmp.m4b") ||
		strings.Contains(lower, ".tmp.m4a") ||
		strings.Contains(lower, ".tmp.mp3") ||
		strings.Contains(lower, ".tmp.flac") ||
		strings.HasSuffix(lower, ".remux.tmp")
}
