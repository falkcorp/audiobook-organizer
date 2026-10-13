// file: internal/sweep/backup_cleanup.go
// version: 1.1.0
// guid: 4d7a1e92-6b35-4c08-9e1f-a2c8d5b03f76
// last-edited: 2026-10-13

package sweep

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// RemoveOldBackupFiles walks root and removes every non-directory entry (Lstat;
// a symlink is removed, never its target) that match recognises as a backup and whose backup time is more than maxAge ago. It
// returns how many files it removed.
//
// match decides both whether a file is a backup and how old it is. It gets the
// file's base name and returns the time the backup was taken, or false for a
// file that is not a backup. A false answer keeps the file whatever its mtime:
// there is deliberately no mtime fallback here, because a hardlinked backup
// keeps the original audio's (often years old) mtime and a name that merely
// resembles a backup is not ours to delete. The scheduled caller passes
// fileops.BackupNameTime, which accepts only the names fileops writes
// (<file>.bak-<unix>[-N] and the legacy <file>.bak-YYYYMMDD-HHMMSS).
//
// `app` names the application-owned directories under root that are never
// entered, for the reason CleanupOrphanedTempFiles documents: this DELETES,
// root is the library root, and the backup and OpenLibrary dump directories
// inside it are operator-settable to names with no leading dot. Passing
// pathutil.AppDirs{} sweeps the whole tree.
//
// log is optional. It receives one Info line per removal, one Warn per failed
// removal (the walk goes on and the file is not counted) and one Debug line,
// with the full path, for a file whose name contains ".bak-" but that match
// refuses (so a kept look-alike can be explained). Files without ".bak-" in the
// name are not logged: that would be a line per library file.
//
// The walk returns ctx.Err() as soon as the context is done, so a canceled run
// reports removed so far together with the error. Any other walk error on an
// entry skips that entry.
//
// Single goroutine on purpose: this is one directory walk with a string test
// per entry and no DB, network or hash work, so a worker pool would add
// ordering and error-handling risk to a delete path for no measurable gain.
func RemoveOldBackupFiles(
	ctx context.Context,
	root string,
	app pathutil.AppDirs,
	match func(name string) (taken time.Time, isBackup bool),
	maxAge time.Duration,
	log func(level slog.Level, msg string, kv ...any),
) (removed int, err error) {
	if log == nil {
		log = func(slog.Level, string, ...any) {}
	}
	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			return nil
		}
		if info.IsDir() {
			if pathutil.ShouldSkipDir(root, path, app) {
				return filepath.SkipDir
			}
			return nil
		}
		taken, isBackup := match(info.Name())
		if !isBackup {
			if strings.Contains(info.Name(), ".bak-") {
				log(slog.LevelDebug, "keeping .bak- file whose name is not a recognised backup name", "path", path)
			}
			return nil
		}
		age := time.Since(taken)
		if age <= maxAge {
			return nil
		}
		if rmErr := os.Remove(path); rmErr != nil {
			log(slog.LevelWarn, "failed to remove old backup", "path", path, "err", rmErr)
			return nil
		}
		removed++
		log(slog.LevelInfo, "cleaned up old backup", "path", path, "age", age.Round(time.Hour))
		return nil
	})
	return removed, err
}
