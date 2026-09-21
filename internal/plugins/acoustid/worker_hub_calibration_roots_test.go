// file: internal/plugins/acoustid/worker_hub_calibration_roots_test.go
// version: 1.0.0
// guid: 0d2f1c6e-9a44-4c1b-8d7f-2b6e5a13c904
// last-edited: 2026-09-21

package acoustid

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// TestBuildIdentityCalibration_CoversEveryRoot pins the contract the worker
// parity gate actually enforces: workerclient's calibrationTargets requires a
// calibration file for EVERY root a worker was started with, so every root
// that has a statable candidate must appear in the offered set.
//
// The regression this guards is a global cap. calibrationFiles used to stop
// the loop after 3 files total, in candidate order — so whichever root sorted
// first consumed the whole budget and the others were offered nothing. On
// 2026-09-21 that left both production workers exiting on
//
//	parity gate failed: the server offered no calibration file under root "books"
//
// for ~6 hours, with 72% of the corpus under the starved root and the run
// stuck at "0 leased to remote workers".
//
// The candidates here are ordered libroot-first with more than the cap, which
// is the ordering that reproduces the bug: a correct implementation caps per
// root and still reaches the later root.
func TestBuildIdentityCalibration_CoversEveryRoot(t *testing.T) {
	// rootDir's PARENT is the second root, exactly as pathutil.PathVars
	// derives it in production (libroot=<rootDir>, books=<parent>).
	base := t.TempDir()
	libDir := filepath.Join(base, "audiobook-organizer")
	booksOnly := filepath.Join(base, "abooks")
	for _, d := range []string{libDir, booksOnly} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	roots := pathutil.PathVars(libDir)
	if len(roots) != 2 || roots[0].Name != "libroot" || roots[1].Name != "books" {
		t.Fatalf("unexpected roots %+v", roots)
	}

	write := func(p string) string {
		if err := os.WriteFile(p, []byte("audio bytes for "+filepath.Base(p)), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// Deliberately more libroot candidates than calibrationFiles, all ahead of
	// the single books-only candidate.
	var identity []windowItem
	for i := range calibrationFiles + 3 {
		identity = append(identity, windowItem{Path: write(filepath.Join(libDir, string(rune('a'+i))+".m4b"))})
	}
	identity = append(identity, windowItem{Path: write(filepath.Join(booksOnly, "under-books.m4b"))})

	r := &hubRun{roots: roots, identity: identity, headHash: headSHA256}
	got := r.buildIdentityCalibration()

	perRoot := map[string]int{}
	for _, cf := range got {
		perRoot[cf.Root]++
	}
	for _, v := range roots {
		if perRoot[v.Name] == 0 {
			t.Errorf("root %q was offered no calibration file; the parity gate refuses every worker holding it (offered: %v)", v.Name, perRoot)
		}
		if perRoot[v.Name] > calibrationFiles {
			t.Errorf("root %q offered %d files, over the per-root cap of %d", v.Name, perRoot[v.Name], calibrationFiles)
		}
	}
}
