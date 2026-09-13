// file: internal/pathutil/activity_dir_skipped_test.go
// version: 1.0.1
// guid: e17b4a90-3c62-4d85-b0f1-9a5e6c283d47
// last-edited: 2026-09-12

// External test package: config imports database, and database imports
// pathutil for its containment checks, so an in-package test importing config
// would be an import cycle.
package pathutil_test

import (
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// TestActivityDBDirIsSkippedByLibraryWalks pins a load-bearing assumption of the
// activity database's default location.
//
// The default is {RootDir}/.activity/activity.sqlite — inside the library tree —
// and it is safe there only because the dot prefix makes every library walk skip
// it. That is not a free property: ShouldSkipDir carves `.alternates` out of the
// hidden-directory rule so deliberately dot-named CONTENT stays visible, and
// adding `.activity` to that carve-out would silently march every scan through a
// multi-gigabyte SQLite file and its WAL.
//
// The constants are referenced rather than retyped so that renaming the directory
// cannot quietly leave this test guarding a name nothing uses any more.
func TestActivityDBDirIsSkippedByLibraryWalks(t *testing.T) {
	root := "/lib/audiobook-organizer"
	activityDir := filepath.Join(root, config.ActivityDBDirName)

	if pathutil.IsVisibleHiddenDir(config.ActivityDBDirName) {
		t.Fatalf("%s is carved out of the hidden-directory skip — library walks would "+
			"descend into the activity database", config.ActivityDBDirName)
	}
	if !pathutil.ShouldSkipDir(root, activityDir, pathutil.AppDirs{}) {
		t.Errorf("ShouldSkipDir(%q) = false; the activity database directory must never be walked", activityDir)
	}

	// The database file itself must sit inside that skipped directory rather than
	// loose in the library root, where no rule would exclude it.
	cfg := config.Config{RootDir: root}
	dbPath := cfg.ResolveActivityDBPath()
	if filepath.Dir(dbPath) != activityDir {
		t.Errorf("default activity DB is at %q, expected it inside the skipped directory %q", dbPath, activityDir)
	}
}
