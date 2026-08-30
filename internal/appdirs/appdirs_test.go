// file: internal/appdirs/appdirs_test.go
// version: 1.1.0
// guid: b4f2917d-30ec-4a68-85c1-6d9e0f27a3b5
// last-edited: 2026-08-30

package appdirs

import (
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// The directories that matter carry NO leading dot. Resolving them is the
// whole point: the dot rule already covered `.backups`, and covered
// `openlibrary-dumps` not at all.
func TestFromConfig_ResolvesAbsoluteAppDirs(t *testing.T) {
	cfg := &config.Config{
		RootDir:            "/srv/books",
		BackupDir:          "/srv/books/backups",
		OpenLibraryDumpDir: "/srv/books/openlibrary-dumps",
		PlaylistDir:        "/srv/books/playlists",
	}
	got := FromConfig(cfg)
	want := pathutil.AppDirs{
		BackupDir:          "/srv/books/backups",
		OpenLibraryDumpDir: "/srv/books/openlibrary-dumps",
		PlaylistDir:        "/srv/books/playlists",
	}
	if got != want {
		t.Fatalf("FromConfig = %+v, want %+v", got, want)
	}
	// And the resolved value actually excludes the tree in practice.
	if !pathutil.ShouldSkipDir("/srv/books", "/srv/books/openlibrary-dumps/pebble", got) {
		t.Error("the resolved dump dir did not exclude its own subtree")
	}
}

// backup.ResolveDir is the single authority for backup_dir: a RELATIVE value
// is anchored to the database's directory. This asserts we call it rather than
// re-deriving the rule.
func TestFromConfig_RelativeBackupDirAnchoredToDatabase(t *testing.T) {
	cfg := &config.Config{
		DatabasePath: "/var/lib/abo/library.db",
		BackupDir:    "backups",
	}
	got := FromConfig(cfg)
	if want := filepath.Clean("/var/lib/abo/backups"); got.BackupDir != want {
		t.Fatalf("BackupDir = %q, want %q (relative backup_dir must anchor to the database dir)", got.BackupDir, want)
	}
}

// A relative value with nothing to anchor it stays relative, and a relative
// path cannot be compared against the absolute paths a walk yields. Dropping
// it is fail-OPEN; resolving it against the process CWD could fabricate a
// match on a subtree nobody configured.
func TestFromConfig_UnanchorableRelativeIsDropped(t *testing.T) {
	got := FromConfig(&config.Config{BackupDir: "backups"})
	if got.BackupDir != "" {
		t.Fatalf("BackupDir = %q, want \"\" -- an unanchorable relative path must be dropped, not guessed at", got.BackupDir)
	}
}

// An UNSET backup_dir is not the same as "no backup directory".
//
// backup.ResolveDir substitutes the literal default "backups" when the setting
// is empty and then anchors it to the database's own directory, so a config
// that never mentions backup_dir still yields a live absolute exclusion as
// soon as database_path is set. This is correct -- it is where backups
// actually land -- but it is surprising, and it is the reason a test cannot
// establish an "empty AppDirs" precondition by zeroing BackupDir alone.
// Tests that need a genuinely empty AppDirs must zero database_path too, and
// should assert the resolved result rather than trust the inputs.
func TestFromConfig_UnsetBackupDirStillResolvesFromDatabasePath(t *testing.T) {
	got := FromConfig(&config.Config{DatabasePath: "/var/lib/abo/library.db"})
	if want := filepath.Clean("/var/lib/abo/backups"); got.BackupDir != want {
		t.Fatalf("BackupDir = %q, want %q -- an unset backup_dir still resolves against the database dir", got.BackupDir, want)
	}
	if got == (pathutil.AppDirs{}) {
		t.Fatal("AppDirs is empty; zeroing BackupDir alone is NOT enough to make it empty")
	}
}

// An unset setting must come back EMPTY, never as "." (filepath.Clean("")),
// which would be a live prefix matching the entire library.
func TestFromConfig_EmptySettingsStayEmpty(t *testing.T) {
	got := FromConfig(&config.Config{DatabasePath: "", BackupDir: ""})
	if got.OpenLibraryDumpDir != "" || got.PlaylistDir != "" {
		t.Fatalf("unset settings resolved to %+v; they must stay empty", got)
	}
	for _, d := range []string{got.BackupDir, got.OpenLibraryDumpDir, got.PlaylistDir} {
		if d == "." {
			t.Fatal(`a setting resolved to "." -- that is a live prefix and would skip the whole library`)
		}
	}
	// Nothing is excluded beyond the dot rule.
	if pathutil.ShouldSkipDir("/srv/books", "/srv/books/abooks", got) {
		t.Error("ordinary content was skipped with an empty config")
	}
}

// A nil config must not panic and must exclude nothing.
func TestFromConfig_NilConfigExcludesNothing(t *testing.T) {
	got := FromConfig(nil)
	if got != (pathutil.AppDirs{}) {
		t.Fatalf("FromConfig(nil) = %+v, want zero value", got)
	}
}

// The scanner's discovery walk (scanner.go) and its file-count walk
// (service.go) both build AppDirs from the process config. If they ever
// diverged, the count walk would count files the discovery walk skips and the
// progress bar could never reach 100%. Both call Current(); this pins that
// Current() is deterministic for a given config.
func TestCurrent_IsDeterministic(t *testing.T) {
	if a, b := Current(), Current(); a != b {
		t.Fatalf("Current() returned %+v then %+v; the scanner's two walks must agree exactly", a, b)
	}
}
