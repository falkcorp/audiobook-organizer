// file: internal/pathutil/database_dir_skipped_test.go
// version: 1.0.0
// guid: 7c3d9e15-4b82-46a0-91fe-25d8b0a6c374
// last-edited: 2026-09-09

package pathutil

import (
	"path/filepath"
	"testing"
)

const dbTestRoot = "/mnt/bigdata/books/audiobook-organizer"

// TestDatabaseDirSkipped_WhenItHasNoDot is the point of adding DatabaseDir to
// AppDirs, and the negative control is what gives it meaning.
//
// A dot-named database directory was already skipped — but only by the
// hidden-name rule, i.e. by a naming coincidence. Production moved its database
// to <root>/.appdata on 2026-09-09 and that dot was the ONLY thing keeping every
// library walker out of a live Pebble store, its TLS material in certs/, its
// Bleve index and its .bootstrap-token. The same day, database_path became
// settable from Settings > Paths, so "<root>/appdata" — no dot, entirely
// ordinary to type — is now one keystroke away.
//
// Walking that directory is not merely wasted I/O: the scanner hashes what it
// finds as candidate audiobooks, and the organizer's move paths operate on
// what the scanner records.
func TestDatabaseDirSkipped_WhenItHasNoDot(t *testing.T) {
	noDotDir := filepath.Join(dbTestRoot, "appdata")
	inside := filepath.Join(noDotDir, "audiobooks.pebble", "000123.sst")

	// NEGATIVE CONTROL, first: with no DatabaseDir configured, nothing skips a
	// directory whose name has no dot. If this ever starts returning true, some
	// OTHER rule is doing the work and the assertions below stop proving that
	// DatabaseDir is what protects the database.
	if ShouldSkipDir(dbTestRoot, noDotDir, AppDirs{}) {
		t.Fatal("control broken: a non-dot directory is skipped with an empty AppDirs, " +
			"so this test can no longer tell whether DatabaseDir is load-bearing")
	}

	app := AppDirs{DatabaseDir: noDotDir}
	if !ShouldSkipDir(dbTestRoot, noDotDir, app) {
		t.Errorf("ShouldSkipDir(%q) = false; the database directory must never be walked "+
			"regardless of whether its name happens to start with a dot", noDotDir)
	}
	if !ShouldSkipDir(dbTestRoot, filepath.Dir(inside), app) {
		t.Errorf("ShouldSkipDir(%q) = false; everything BENEATH the database directory "+
			"must be skipped too, not just the directory itself", filepath.Dir(inside))
	}
}

// TestActivityDBDirSkipped_WhenConfiguredWithoutADot covers the sibling gap.
//
// TestActivityDBDirIsSkippedByLibraryWalks already pins the DEFAULT location
// ({RootDir}/.activity) and states outright that it is safe "only because the
// dot prefix makes every library walk skip it." But activity_db_path is
// operator- and UI-settable, so that test guards a default nobody is obliged to
// keep, and nothing guarded the configured value.
func TestActivityDBDirSkipped_WhenConfiguredWithoutADot(t *testing.T) {
	noDotDir := filepath.Join(dbTestRoot, "activity")

	if ShouldSkipDir(dbTestRoot, noDotDir, AppDirs{}) {
		t.Fatal("control broken: a non-dot directory is skipped with an empty AppDirs")
	}
	if !ShouldSkipDir(dbTestRoot, noDotDir, AppDirs{ActivityDBDir: noDotDir}) {
		t.Errorf("ShouldSkipDir(%q) = false; a configured activity-database directory "+
			"must be excluded whatever it is named", noDotDir)
	}
}

// TestDatabaseDirEqualToRootIsIgnored guards the failure mode that is far worse
// than walking the database: skipping the entire library.
//
// If an operator points database_path at the library root itself, DatabaseDir
// becomes the walk root. Honouring it would make every scan return ZERO BOOKS
// with no error anywhere — the exact silent outcome skipsUnderRoot was written
// to prevent for backup_dir. Failing OPEN here (walking a directory we are
// unsure about) costs I/O; failing closed is silent data loss.
func TestDatabaseDirEqualToRootIsIgnored(t *testing.T) {
	book := filepath.Join(dbTestRoot, "Some Author", "Some Title")

	for name, app := range map[string]AppDirs{
		"database dir == root": {DatabaseDir: dbTestRoot},
		"activity dir == root": {ActivityDBDir: dbTestRoot},
		"database dir above root": {
			DatabaseDir: filepath.Dir(dbTestRoot),
		},
	} {
		t.Run(name, func(t *testing.T) {
			if ShouldSkipDir(dbTestRoot, book, app) {
				t.Errorf("ShouldSkipDir(%q) = true; an app dir at or above the walk root "+
					"must be ignored, or the whole library scans to zero books "+
					"with no error", book)
			}
		})
	}
}

// TestEmptyDatabaseDirMatchesNothing pins the hazard cleanAbs exists for.
//
// The default database_path is the RELATIVE "audiobooks.pebble", which cleanAbs
// drops to "". filepath.Clean("") is "." — a live relative prefix that would
// match every relative path handed to it — so an empty field must be rejected
// before any Clean call. Getting this wrong would exclude the entire library on
// a default install: the single most destructive outcome available here, and it
// would look like "the scanner finds nothing" rather than like a bug.
func TestEmptyDatabaseDirMatchesNothing(t *testing.T) {
	book := filepath.Join(dbTestRoot, "Some Author", "Some Title")

	if ShouldSkipDir(dbTestRoot, book, AppDirs{DatabaseDir: "", ActivityDBDir: ""}) {
		t.Error("an empty DatabaseDir/ActivityDBDir must match nothing; it just " +
			"excluded ordinary library content")
	}
	// A relative value must be dropped too — it cannot be compared against the
	// absolute paths a walk yields, and resolving it against the process CWD
	// would fabricate a match.
	if ShouldSkipDir(dbTestRoot, book, AppDirs{DatabaseDir: "audiobooks.pebble"}) {
		t.Error("a relative DatabaseDir must match nothing")
	}
}

// TestDatabaseDirDoesNotMatchASiblingByPrefix pins component-wise comparison.
// A naive strings.HasPrefix makes "<root>/appdata-archive" match
// "<root>/appdata", quietly hiding real library content next to the database.
func TestDatabaseDirDoesNotMatchASiblingByPrefix(t *testing.T) {
	dbDir := filepath.Join(dbTestRoot, "appdata")
	sibling := filepath.Join(dbTestRoot, "appdata-archive")

	if ShouldSkipDir(dbTestRoot, sibling, AppDirs{DatabaseDir: dbDir}) {
		t.Errorf("ShouldSkipDir(%q) = true; a sibling sharing a name PREFIX with the "+
			"database directory is ordinary library content", sibling)
	}
}
