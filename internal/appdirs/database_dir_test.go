// file: internal/appdirs/database_dir_test.go
// version: 1.1.0
// guid: 1a06f2b8-5d47-4e93-8c20-b7e14a385c96
// last-edited: 2026-09-09

package appdirs

import (
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// TestFromConfig_ExcludesProductionsRealLayout uses the exact paths production
// ran with after the 2026-09-09 database relocation, because the whole point of
// this change is a deployment that already exists.
//
// Everything the app owns now sits INSIDE the library root. Before this,
// `.appdata` was kept out of library walks purely by its leading dot.
func TestFromConfig_ExcludesProductionsRealLayout(t *testing.T) {
	const root = "/mnt/bigdata/books/audiobook-organizer"
	cfg := config.Config{
		RootDir:      root,
		DatabasePath: root + "/.appdata/audiobooks.pebble",
	}

	app := FromConfig(&cfg)

	if got, want := app.DatabaseDir, root+"/.appdata"; got != want {
		t.Errorf("DatabaseDir = %q, want %q — it must be the DIRECTORY holding the "+
			"database, so the siblings that resolve beside it (certs/, "+
			"library.bleve, .bootstrap-token) are excluded too", got, want)
	}
	if got, want := app.ActivityDBDir, filepath.Join(root, config.ActivityDBDirName); got != want {
		t.Errorf("ActivityDBDir = %q, want %q — resolved through "+
			"ResolveActivityDBPath, not read from the raw (empty) field", got, want)
	}

	// The properties that actually matter, asserted through the real predicate.
	for _, dir := range []string{
		root + "/.appdata",
		root + "/.appdata/certs",
		root + "/.appdata/audiobooks.pebble",
		filepath.Join(root, config.ActivityDBDirName),
	} {
		if !pathutil.ShouldSkipDir(root, dir, app) {
			t.Errorf("ShouldSkipDir(%q) = false; application state must never be walked", dir)
		}
	}

	// And ordinary library content must still be walked. Without this the test
	// would pass on an AppDirs that excluded everything.
	book := filepath.Join(root, "Brandon Sanderson", "Mistborn")
	if pathutil.ShouldSkipDir(root, book, app) {
		t.Errorf("ShouldSkipDir(%q) = true; that is library content", book)
	}
}

// TestFromConfig_ProtectsADatabasePathWithNoDot is the case the dot rule cannot
// reach, and the one Settings > Paths now makes reachable by typing.
func TestFromConfig_ProtectsADatabasePathWithNoDot(t *testing.T) {
	const root = "/mnt/bigdata/books/audiobook-organizer"
	cfg := config.Config{
		RootDir:      root,
		DatabasePath: root + "/appdata/audiobooks.pebble", // no dot
	}

	app := FromConfig(&cfg)
	dbDir := root + "/appdata"

	if pathutil.ShouldSkipDir(root, dbDir, pathutil.AppDirs{}) {
		t.Fatal("control broken: a non-dot directory is skipped with an empty AppDirs")
	}
	if !pathutil.ShouldSkipDir(root, dbDir, app) {
		t.Errorf("ShouldSkipDir(%q) = false; a live Pebble store inside the library "+
			"tree would be scanned as content", dbDir)
	}
}

// TestFromConfig_RelativeDatabasePathExcludesNothing pins the default install.
// database_path defaults to the relative "audiobooks.pebble"; cleanAbs drops it,
// and it must not turn into a "." prefix that matches the whole library.
func TestFromConfig_RelativeDatabasePathExcludesNothing(t *testing.T) {
	const root = "/srv/books"
	cfg := config.Config{RootDir: root, DatabasePath: "audiobooks.pebble"}

	app := FromConfig(&cfg)
	if app.DatabaseDir != "" {
		t.Errorf("DatabaseDir = %q, want \"\" for a relative database_path", app.DatabaseDir)
	}

	book := filepath.Join(root, "Some Author", "Some Title")
	if pathutil.ShouldSkipDir(root, book, app) {
		t.Error("a relative database_path just excluded the entire library")
	}
}

// TestClearedBaseline_StillFailsOnAnUnclearedField is the negative control for
// ClearedBaseline itself.
//
// ClearedBaseline was introduced because the `empty AppDirs` guard controls
// could no longer compare against the zero value once ActivityDBDir existed.
// The obvious way to make those tests pass again is to loosen the comparison
// until it stops complaining -- which would silently retire eight negative
// controls across the codebase and nobody would notice for months.
//
// So: with a source field left uncleared, Current() MUST still differ from
// ClearedBaseline(). If this test ever passes trivially, the guard controls
// have stopped guarding.
func TestClearedBaseline_StillFailsOnAnUnclearedField(t *testing.T) {
	const root = "/mnt/bigdata/books/audiobook-organizer"
	prev := config.AppConfig
	t.Cleanup(func() { config.AppConfig = prev })

	// Everything cleared except RootDir: this is the state the guard helpers
	// set up, and it must compare equal.
	config.AppConfig = config.Config{RootDir: root}
	if got, want := Current(), ClearedBaseline(); got != want {
		t.Fatalf("a fully cleared config must equal the baseline; got %+v want %+v", got, want)
	}

	// Now leave ONE source field set, exactly as a future careless caller would.
	for name, mutate := range map[string]func(*config.Config){
		"BackupDir":          func(c *config.Config) { c.BackupDir = root + "/backups" },
		"OpenLibraryDumpDir": func(c *config.Config) { c.OpenLibraryDumpDir = root + "/dumps" },
		"PlaylistDir":        func(c *config.Config) { c.PlaylistDir = root + "/playlists" },
		"DatabasePath":       func(c *config.Config) { c.DatabasePath = root + "/.appdata/db.pebble" },
	} {
		t.Run(name, func(t *testing.T) {
			config.AppConfig = config.Config{RootDir: root}
			mutate(&config.AppConfig)
			if got, want := Current(), ClearedBaseline(); got == want {
				t.Errorf("%s was left set and the baseline comparison still matched (%+v) — "+
					"the empty-AppDirs negative controls are no longer guarding anything", name, got)
			}
		})
	}
}

// TestFromConfig_NilConfigIsSafe — Current() passes &config.AppConfig, which is
// a real struct, but FromConfig is exported and the nil branch already exists.
func TestFromConfig_NilConfigIsSafe(t *testing.T) {
	app := FromConfig(nil)
	if app.DatabaseDir != "" || app.ActivityDBDir != "" {
		t.Errorf("FromConfig(nil) must be the zero value, got %+v", app)
	}
}
