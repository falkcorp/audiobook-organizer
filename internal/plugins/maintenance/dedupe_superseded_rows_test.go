// file: internal/plugins/maintenance/dedupe_superseded_rows_test.go
// version: 1.0.0
// guid: 6b04e19d-83f7-4c52-a1e6-9d275f3b0c48
// last-edited: 2026-09-21

package maintenance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestRankKeeper_PresentRowAlwaysWins is the data-loss-critical property of the
// superseded-row fold: the survivor must be a row whose file can actually be
// opened, because the survivor is what a download fetches and what the duration
// pass probes.
//
// The absent row here is deliberately the BEST-evidenced one — it carries a
// fingerprint, a duration and a hash, and the present row carries nothing. The
// old ranking, which scored only evidence, would have kept the absent row and
// deleted the only openable file's row: a book that plays today would stop
// resolving entirely. Presence therefore outranks every evidence field.
//
// The absent row's evidence is not lost; mergeMissingFields salvages it onto
// the keeper before any delete, which is covered by the dedupe salvage tests.
func TestRankKeeper_PresentRowAlwaysWins(t *testing.T) {
	present := map[string]bool{"/lib/B/real.m4b": true, "/lib/B/phantom.m4b": false}
	rows := []database.BookFile{
		{ID: "phantom", FilePath: "/lib/B/phantom.m4b", Duration: 1800, FileHash: "h",
			AcoustIDFingerprint: []byte("fp")},
		{ID: "real", FilePath: "/lib/B/real.m4b"},
	}
	if got := rankKeeper(rows, present)[0]; got.ID != "real" {
		t.Errorf("keeper = %q, want \"real\": a row whose file is gone must never outrank a present one", got.ID)
	}
	// nil disables the check (the same-path case: presence cannot discriminate).
	if got := rankKeeper(rows, nil)[0]; got.ID != "phantom" {
		t.Errorf("with no presence map the best-evidenced row should win, got %q", got.ID)
	}
}

// TestPruneSupersededEnabled_DefaultsOn pins that omitting the flag enables
// folding, while an explicit false restricts the run to exact duplicate paths.
func TestPruneSupersededEnabled_DefaultsOn(t *testing.T) {
	var p DedupeBookFileRowsParams
	if !p.pruneSupersededEnabled() {
		t.Error("omitted pruneSuperseded must default to enabled")
	}
	off := false
	p.PruneSuperseded = &off
	if p.pruneSupersededEnabled() {
		t.Error("pruneSuperseded:false must disable folding")
	}
	on := true
	p.PruneSuperseded = &on
	if !p.pruneSupersededEnabled() {
		t.Error("pruneSuperseded:true must enable folding")
	}
}

// seedDir writes the named files and returns the directory.
func seedDir(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("audio"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestSupersededFold_OnlyWithExactlyOnePresentSibling pins the guard that keeps
// this op from guessing. Folding rewrites which rows get DELETED, so it is only
// safe where the destination is unambiguous.
//
// This exercises the decision directly rather than through the op, so the three
// shapes stay legible: one present + one absent folds; two present + one absent
// must not (which of the two did the absent row belong to?); and all-present
// folds nothing.
func TestSupersededFold_OnlyWithExactlyOnePresentSibling(t *testing.T) {
	fold := func(paths []string) int {
		present := map[string]bool{}
		for _, p := range paths {
			fi, err := os.Stat(p)
			present[p] = err == nil && fi.Mode().IsRegular()
		}
		var here, gone []string
		for _, p := range paths {
			if present[p] {
				here = append(here, p)
			} else {
				gone = append(gone, p)
			}
		}
		if len(here) != 1 || len(gone) == 0 {
			return 0
		}
		return len(gone)
	}

	dir := seedDir(t, "real.m4b")
	if n := fold([]string{filepath.Join(dir, "real.m4b"), filepath.Join(dir, "phantom.m4b")}); n != 1 {
		t.Errorf("one present + one absent must fold 1 row, got %d", n)
	}

	dir2 := seedDir(t, "a.m4b", "b.m4b")
	if n := fold([]string{filepath.Join(dir2, "a.m4b"), filepath.Join(dir2, "b.m4b"), filepath.Join(dir2, "gone.m4b")}); n != 0 {
		t.Errorf("two present files make the destination ambiguous; must fold nothing, got %d", n)
	}

	dir3 := seedDir(t, "a.m4b", "b.m4b")
	if n := fold([]string{filepath.Join(dir3, "a.m4b"), filepath.Join(dir3, "b.m4b")}); n != 0 {
		t.Errorf("all files present: nothing to fold, got %d", n)
	}

	dir4 := t.TempDir()
	if n := fold([]string{filepath.Join(dir4, "x.m4b"), filepath.Join(dir4, "y.m4b")}); n != 0 {
		t.Errorf("no present file means no survivor to fold into; must fold nothing, got %d", n)
	}
}
