// file: internal/sweep/backup_cleanup_test.go
// version: 1.0.0
// guid: 8b3f6c04-1d97-4e25-a6b8-5c9e0d72f413
// last-edited: 2026-10-13

package sweep

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

const day = 24 * time.Hour

func writeAged(t *testing.T, path string, mtimeAge time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mt := time.Now().Add(-mtimeAge)
	if err := os.Chtimes(path, mt, mt); err != nil {
		t.Fatal(err)
	}
}

func unixStamp(age time.Duration) string {
	return strconv.FormatInt(time.Now().Add(-age).Unix(), 10)
}

// TestRemoveOldBackupFiles_NeverDeletesANonBackupName is the data-loss guard
// carried over from maintenance.cleanup-old-backups: with the production match
// (fileops.BackupNameTime) a name that merely contains ".bak-" is kept however
// old its mtime. The helper has no mtime fallback; reintroducing one (age by
// info.ModTime() when match says no) fails this test.
func TestRemoveOldBackupFiles_NeverDeletesANonBackupName(t *testing.T) {
	root := t.TempDir()
	names := []string{
		"x.bak-notes.m4b",
		"no-time.m4b.bak-unknown",
		"stamp-not-last.bak-" + unixStamp(60*day) + ".m4b",
		"too-early.m4b.bak-1",
		"future.m4b.bak-" + strconv.FormatInt(time.Now().Add(30*day).Unix(), 10),
		".bak-" + unixStamp(60*day),
		"library.itl.bak-20240101T000000Z",
		"plain.m4b",
	}
	for _, n := range names {
		writeAged(t, filepath.Join(root, "Author", "Book", n), 400*day)
	}

	removed, err := RemoveOldBackupFiles(context.Background(), root, pathutil.AppDirs{},
		fileops.BackupNameTime, 30*day, nil)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(root, "Author", "Book", n)); err != nil {
			t.Errorf("DATA LOSS: %s is not a recognised backup name and must be kept: %v", n, err)
		}
	}
}

// TestRemoveOldBackupFiles_AgesByTheMatchedTime covers the age boundary and the
// rule that the time comes from match, never the mtime: a fresh backup of an
// old file (hardlink, ancient mtime) survives, an old backup with a fresh mtime
// goes.
func TestRemoveOldBackupFiles_AgesByTheMatchedTime(t *testing.T) {
	root := t.TempDir()
	legacy := func(age time.Duration) string { return time.Now().Add(-age).Format("20060102-150405") }
	cases := []struct {
		file     string
		mtimeAge time.Duration
		wantGone bool
	}{
		{"old.m4b.bak-" + unixStamp(60*day), time.Hour, true},
		{"old-suffixed.m4b.bak-" + unixStamp(60*day) + "-2", time.Hour, true},
		{"old-legacy.m4b.bak-" + legacy(60*day), time.Hour, true},
		{"young.m4b.bak-" + unixStamp(5*day), 400 * day, false},
		{"fresh-of-old.m4b.bak-" + unixStamp(time.Hour), 400 * day, false},
		{"just-inside.m4b.bak-" + unixStamp(29*day), 400 * day, false},
		{"just-outside.m4b.bak-" + unixStamp(31*day), time.Hour, true},
	}
	for _, c := range cases {
		writeAged(t, filepath.Join(root, "A", c.file), c.mtimeAge)
	}

	removed, err := RemoveOldBackupFiles(context.Background(), root, pathutil.AppDirs{},
		fileops.BackupNameTime, 30*day, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantRemoved := 0
	for _, c := range cases {
		_, statErr := os.Stat(filepath.Join(root, "A", c.file))
		gone := statErr != nil
		if gone != c.wantGone {
			t.Errorf("%s: gone = %v, want %v", c.file, gone, c.wantGone)
		}
		if c.wantGone {
			wantRemoved++
		}
	}
	if removed != wantRemoved {
		t.Errorf("removed = %d, want %d", removed, wantRemoved)
	}
}

// TestRemoveOldBackupFiles_SkipsAppDirs pins the guard that makes a DELETING
// walk safe at the library root: the application's own directories are not
// entered. The fixture directories are deliberately NON-DOT (a dot-named one
// would be skipped by pathutil's dot rule and prove nothing), and the
// t.TempDir() string is used verbatim for both the root and the AppDirs fields
// (no EvalSymlinks: on macOS /var vs /private/var would make filepath.Rel
// disagree and the guard silently no-op while the test still passed).
func TestRemoveOldBackupFiles_SkipsAppDirs(t *testing.T) {
	tests := []struct {
		name         string
		app          func(root string) pathutil.AppDirs
		wantAppFiles bool // files inside the app dirs survive
		wantRemoved  int
	}{
		{
			name: "app dirs configured: backups inside them survive",
			app: func(root string) pathutil.AppDirs {
				return pathutil.AppDirs{
					BackupDir:          filepath.Join(root, "backups"),
					OpenLibraryDumpDir: filepath.Join(root, "openlibrary-dumps"),
				}
			},
			wantAppFiles: true,
			wantRemoved:  1,
		},
		{
			// Fail-open: with nothing configured the whole tree is swept, as
			// the walk behaved before the guard existed.
			name:        "empty AppDirs: whole tree swept",
			app:         func(string) pathutil.AppDirs { return pathutil.AppDirs{} },
			wantRemoved: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			old := ".bak-" + unixStamp(90*day)
			libraryFile := filepath.Join(root, "Author", "Book", "a.m4b"+old)
			backupFile := filepath.Join(root, "backups", "b.m4b"+old)
			dumpFile := filepath.Join(root, "openlibrary-dumps", "db", "c.m4b"+old)
			for _, p := range []string{libraryFile, backupFile, dumpFile} {
				writeAged(t, p, time.Hour)
			}

			removed, err := RemoveOldBackupFiles(context.Background(), root, tt.app(root),
				fileops.BackupNameTime, 30*day, nil)
			if err != nil {
				t.Fatal(err)
			}
			if removed != tt.wantRemoved {
				t.Errorf("removed = %d, want %d", removed, tt.wantRemoved)
			}
			if _, err := os.Stat(libraryFile); err == nil {
				t.Errorf("library backup should have been removed: %s", libraryFile)
			}
			for _, p := range []string{backupFile, dumpFile} {
				_, err := os.Stat(p)
				if tt.wantAppFiles && err != nil {
					t.Errorf("DATA LOSS: a file inside an app dir was deleted: %s", p)
				}
				if !tt.wantAppFiles && err == nil {
					t.Errorf("empty AppDirs should sweep the whole tree, %s survived", p)
				}
			}
		})
	}
}

func TestRemoveOldBackupFiles_CanceledContextRemovesNothing(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.m4b.bak-"+unixStamp(90*day))
	writeAged(t, p, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	removed, err := RemoveOldBackupFiles(ctx, root, pathutil.AppDirs{}, fileops.BackupNameTime, 30*day, nil)
	if err != context.Canceled {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
	if _, statErr := os.Stat(p); statErr != nil {
		t.Errorf("a canceled sweep must delete nothing: %v", statErr)
	}
}

// TestRemoveOldBackupFiles_LogsEachRemoval pins the log contract the caller
// relies on: one Info line per removed file, and a nil log is allowed.
func TestRemoveOldBackupFiles_LogsEachRemoval(t *testing.T) {
	root := t.TempDir()
	writeAged(t, filepath.Join(root, "a.m4b.bak-"+unixStamp(90*day)), time.Hour)
	writeAged(t, filepath.Join(root, "b.m4b.bak-"+unixStamp(2*day)), time.Hour)

	var mu sync.Mutex
	var levels []slog.Level
	log := func(level slog.Level, _ string, _ ...any) {
		mu.Lock()
		defer mu.Unlock()
		levels = append(levels, level)
	}
	removed, err := RemoveOldBackupFiles(context.Background(), root, pathutil.AppDirs{},
		fileops.BackupNameTime, 30*day, log)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 || len(levels) != 1 || levels[0] != slog.LevelInfo {
		t.Errorf("removed = %d, log levels = %v; want 1 removal and one Info line", removed, levels)
	}
}
