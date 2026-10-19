// file: internal/pathutil/activity_dir_skipped_test.go
// version: 1.1.0
// guid: e17b4a90-3c62-4d85-b0f1-9a5e6c283d47
// last-edited: 2026-10-19

// External test package: config imports database, and database imports
// pathutil for its containment checks, so an in-package test importing config
// would be an import cycle.
package pathutil_test

import (
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// retiredActivityDirName is where the removed SQLite activity backend kept its
// database by default ({RootDir}/.activity/activity.sqlite). 01-P75 deleted
// the backend but deliberately left the file on disk (tens of gigabytes on
// production), so the directory can still exist inside the library tree.
const retiredActivityDirName = ".activity"

// TestActivityDBDirIsSkippedByLibraryWalks pins that the retired activity
// database's default directory stays out of every library walk.
//
// It is safe inside the library tree only because the dot prefix makes every
// walk skip it. That is not a free property: ShouldSkipDir carves `.alternates`
// out of the hidden-directory rule so deliberately dot-named CONTENT stays
// visible, and adding `.activity` to that carve-out would march every scan
// through the multi-gigabyte SQLite file the removed backend left behind.
func TestActivityDBDirIsSkippedByLibraryWalks(t *testing.T) {
	root := "/lib/audiobook-organizer"
	activityDir := filepath.Join(root, retiredActivityDirName)

	if pathutil.IsVisibleHiddenDir(retiredActivityDirName) {
		t.Fatalf("%s is carved out of the hidden-directory skip — library walks would "+
			"descend into the retired activity database", retiredActivityDirName)
	}
	if !pathutil.ShouldSkipDir(root, activityDir, pathutil.AppDirs{}) {
		t.Errorf("ShouldSkipDir(%q) = false; the retired activity database directory must never be walked", activityDir)
	}
}
