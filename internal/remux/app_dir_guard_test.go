// file: internal/remux/app_dir_guard_test.go
// version: 1.1.0
// guid: 3a17c8d2-6b40-4e95-81af-27c50e6b93d1
// last-edited: 2026-08-30

package remux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/appdirs"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// withRemuxAppDirs points the process config's root and app directories at a
// temp tree. NON-DOT app dir names are essential: a dot-named fixture is
// skipped by pathutil's pre-existing dot rule and would prove nothing. The root
// string is used verbatim, never EvalSymlinks'd, so filepath.Rel inside AppDirs
// compares like with like on macOS (/var vs /private/var).
func withRemuxAppDirs(t *testing.T, root string, enabled bool) {
	t.Helper()
	prevRoot := config.AppConfig.RootDir
	prevBackup, prevDump := config.AppConfig.BackupDir, config.AppConfig.OpenLibraryDumpDir
	prevDB, prevPlaylist := config.AppConfig.DatabasePath, config.AppConfig.PlaylistDir
	config.AppConfig.RootDir = root
	if enabled {
		config.AppConfig.BackupDir = filepath.Join(root, "backups")
		config.AppConfig.OpenLibraryDumpDir = filepath.Join(root, "openlibrary-dumps")
	} else {
		// Zero EVERY field appdirs.FromConfig reads, not just these two.
		// backup.ResolveDir SYNTHESIZES "backups" when BackupDir is unset and
		// anchors it to the database's own directory, so clearing BackupDir alone
		// still produces a live absolute exclusion whenever DatabasePath is set --
		// and sibling tests in this tree do set it. The assertion below is the real
		// guarantee: it fails loudly if FromConfig ever grows a source field that
		// nobody zeroed here, instead of letting this subtest quietly stop testing
		// the empty case while still passing.
		config.AppConfig.BackupDir, config.AppConfig.OpenLibraryDumpDir = "", ""
		config.AppConfig.DatabasePath, config.AppConfig.PlaylistDir = "", ""
		if got := appdirs.Current(); got != (pathutil.AppDirs{}) {
			t.Fatalf("the empty-AppDirs case is not actually empty: %+v", got)
		}
	}
	t.Cleanup(func() {
		config.AppConfig.RootDir = prevRoot
		config.AppConfig.BackupDir, config.AppConfig.OpenLibraryDumpDir = prevBackup, prevDump
		config.AppConfig.DatabasePath, config.AppConfig.PlaylistDir = prevDB, prevPlaylist
	})
}

// seedCandidates writes one malformed .m4b in the library and one inside each
// app directory. Malformed content is enough: taglib cannot parse it, so all
// three are candidates for the walk under test.
func seedCandidates(t *testing.T, root string) {
	t.Helper()
	for _, p := range []string{
		filepath.Join(root, "Author", "Book", "library.m4b"),
		filepath.Join(root, "backups", "archived.m4b"),
		filepath.Join(root, "openlibrary-dumps", "db", "dumped.m4b"),
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("not a real m4b"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRemuxMalformedFiles_SkipsAppDirs pins BOTH walks in remux.go: the
// pre-count pass (observed via `total`) and the processing pass (observed via
// `processed`). They MUST agree — the first is the progress denominator for the
// second — so asserting on both is what catches a guard added to only one.
func TestRemuxMalformedFiles_SkipsAppDirs(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available, skipping")
	}
	for _, tc := range []struct {
		name      string
		appDirs   bool
		wantTotal int
	}{
		{"app dirs configured: only library candidates counted and processed", true, 1},
		{"empty AppDirs: whole tree counted, exactly as before", false, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			withRemuxAppDirs(t, root, tc.appDirs)
			seedCandidates(t, root)

			var lastTotal, lastProcessed int
			err := New(&MockStore{}).RemuxMalformedFiles(context.Background(),
				func(processed, total int, _ string) {
					lastProcessed, lastTotal = processed, total
				})
			if err != nil {
				t.Fatalf("RemuxMalformedFiles: %v", err)
			}
			if lastTotal != tc.wantTotal {
				t.Errorf("pre-count walk total = %d, want %d", lastTotal, tc.wantTotal)
			}
			if lastProcessed != tc.wantTotal {
				t.Errorf("processing walk processed = %d, want %d "+
					"(the two walks must agree, or progress can never reach 100%%)",
					lastProcessed, tc.wantTotal)
			}
		})
	}
}

// TestTranscodeMalformedFiles_SkipsAppDirs pins both walks in transcode.go, the
// same shape as above. Its per-file work is a full ffmpeg transcode, so an
// unguarded walk would transcode files out of the backup tree.
func TestTranscodeMalformedFiles_SkipsAppDirs(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available, skipping")
	}
	for _, tc := range []struct {
		name      string
		appDirs   bool
		wantTotal int
	}{
		{"app dirs configured: only library candidates counted and processed", true, 1},
		{"empty AppDirs: whole tree counted, exactly as before", false, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			withRemuxAppDirs(t, root, tc.appDirs)
			seedCandidates(t, root)

			var lastTotal, lastProcessed int
			err := NewTranscoder(&MockStore{}).TranscodeMalformedFiles(context.Background(),
				func(processed, total int, _ string) {
					lastProcessed, lastTotal = processed, total
				})
			if err != nil {
				t.Fatalf("TranscodeMalformedFiles: %v", err)
			}
			if lastTotal != tc.wantTotal {
				t.Errorf("pre-count walk total = %d, want %d", lastTotal, tc.wantTotal)
			}
			if lastProcessed != tc.wantTotal {
				t.Errorf("processing walk processed = %d, want %d", lastProcessed, tc.wantTotal)
			}
		})
	}
}
