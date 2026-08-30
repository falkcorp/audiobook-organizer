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
// progress bar could never reach 100%. Both call Current(), so Current() must
// actually READ the process config — not merely return the same thing twice.
//
// The previous version of this test asserted only `Current() == Current()`.
// Nothing in this test binary calls InitConfig, so config.AppConfig is the zero
// value and both sides were the empty AppDirs{}: the assertion held for the
// exact failure it was meant to catch. Verified 2026-08-30 by stubbing
// Current() to `return pathutil.AppDirs{}` — the old test stayed green.
//
// So this seeds a production-shaped config and asserts on the resolved value.
func TestCurrent_ResolvesTheProcessConfig(t *testing.T) {
	before := config.Snapshot()
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = before }) })

	config.Mutate(func(c *config.Config) {
		*c = config.Config{
			RootDir:            "/srv/books",
			DatabasePath:       "/var/lib/abo/library.db",
			BackupDir:          "backups", // relative: must anchor to the DB dir
			OpenLibraryDumpDir: "/srv/books/openlibrary-dumps",
			PlaylistDir:        "/srv/books/playlists",
		}
	})

	got := Current()

	if got == (pathutil.AppDirs{}) {
		t.Fatal("Current() returned the zero AppDirs from a fully populated config. " +
			"Every walker would then exclude nothing and the scanner would descend " +
			"into its own backup and dump directories.")
	}
	want := pathutil.AppDirs{
		BackupDir:          filepath.Clean("/var/lib/abo/backups"),
		OpenLibraryDumpDir: "/srv/books/openlibrary-dumps",
		PlaylistDir:        "/srv/books/playlists",
	}
	if got != want {
		t.Fatalf("Current() = %+v, want %+v", got, want)
	}

	// The resolved value must exclude in practice, not just compare equal.
	if !pathutil.ShouldSkipDir("/srv/books", "/srv/books/openlibrary-dumps/pebble", got) {
		t.Error("Current() did not exclude the configured dump dir's subtree")
	}
	if pathutil.ShouldSkipDir("/srv/books", "/srv/books/abooks", got) {
		t.Error("Current() excluded ordinary library content")
	}

	// Determinism still matters — the two scanner walks must agree — but it is
	// now asserted on top of a value proven to be non-empty.
	if a, b := Current(), Current(); a != b {
		t.Fatalf("Current() returned %+v then %+v; the scanner's two walks must agree exactly", a, b)
	}
}

// Current() must agree with FromConfig on the very config it reads, or the
// walkers that thread a *config.Config and the walkers that call Current()
// would exclude different trees.
func TestCurrent_MatchesFromConfigOnTheSameConfig(t *testing.T) {
	before := config.Snapshot()
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = before }) })

	cfg := config.Config{
		RootDir:            "/srv/books",
		DatabasePath:       "/var/lib/abo/library.db",
		BackupDir:          "/srv/books/backups",
		OpenLibraryDumpDir: "/srv/books/openlibrary-dumps",
		PlaylistDir:        "/srv/books/playlists",
	}
	config.Mutate(func(c *config.Config) { *c = cfg })

	if got, want := Current(), FromConfig(&cfg); got != want {
		t.Fatalf("Current() = %+v but FromConfig(same config) = %+v; the two "+
			"walker entry points must resolve identically", got, want)
	}
}
