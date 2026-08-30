// file: internal/sweep/temp_cleanup.go
// version: 1.1.0
// guid: f7e6d5c4-b3a2-1908-7654-321fedcba987
// last-edited: 2026-08-30

package sweep

import (
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// CleanupOrphanedTempFiles removes *.tmp.m4b, *.tmp.m4a, *.tmp.mp3, and
// *.remux.tmp files left behind by ffmpeg operations that were interrupted
// by a crash or server restart. Returns the number of files removed.
// w and opID are optional — if provided, each removal is submitted to the
// activity batcher instead of emitting a per-file log line.
//
// `app` names the application-owned directories under `root` that must not be
// swept. It is a REQUIRED parameter for the reason pathutil.AppDirs documents:
// this function DELETES, and its callers pass the library root, inside which
// the application keeps a backup directory (multi-GB archives) and an
// OpenLibrary dump directory (thousands of files including an embedded
// database). Both are operator-settable to names with no leading dot, so the
// dot rule alone does not cover them.
//
// Today's predicate happens not to match anything in those trees — see
// isOrphanedTempFile — but that is a NAMING COINCIDENCE, which is exactly the
// protection PR #2974 replaced with a rule. A required parameter turns "a new
// caller forgot the exclusion" into a compile error; a variadic or optional
// one would let it be omitted silently. Passing pathutil.AppDirs{} is still
// possible and reproduces the pre-#2974 behaviour exactly, but it is a visible
// deliberate act rather than an omission nobody can see in review.
//
// sweep does not import internal/config: it is called from packages that
// already hold one, and they build the value with appdirs.Current(). Taking a
// resolved value also makes the guard directly testable without touching
// process-wide config.
func CleanupOrphanedTempFiles(root string, app pathutil.AppDirs, w *activity.Writer, opID string) int {
	if root == "" {
		return 0
	}

	removed := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			// Prune whole app-owned subtrees rather than filtering their files
			// one at a time: SkipDir also saves descending 90+ GB of archives
			// and an embedded database that can never contain a temp file.
			if pathutil.ShouldSkipDir(root, path, app) {
				return filepath.SkipDir
			}
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
