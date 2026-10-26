// file: internal/plugins/deluge/link_backfill_retired_test.go
// version: 1.0.0
// guid: c58e1a37-9f04-4d62-8b73-0a2d6e91f4b8
// last-edited: 2026-10-26

package deluge

import (
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A torrent is never linked to a retired row: the store's hash and path
// lookups return retired rows by design (a retired row still owns its hash and
// path for claim purposes), so the candidate list drops them.
func TestRowsToCandidates_DropsRetiredRows(t *testing.T) {
	rows := []database.BookFile{
		{ID: "live1", BookID: "holder", FilePath: "/lib/h/01.mp3"},
		{ID: "ret1", BookID: "holder", FilePath: "/lib/h/02.mp3", Retired: true, VersionID: "v1"},
		{ID: "live2", BookID: "other", FilePath: "/lib/o/01.mp3"},
	}
	got := rowsToCandidates(rows)
	if len(got) != 2 || got[0].ID != "live1" || got[1].ID != "live2" {
		t.Fatalf("candidates = %+v, want only the live rows live1 and live2 in order", got)
	}
	if only := rowsToCandidates(rows[1:2]); len(only) != 0 {
		t.Fatalf("a retired-only list must give no candidates, got %+v", only)
	}
}

// A retired row stays out of the library index (rule a by original path, rule d
// by base name and size) and out of the relink hold-back counts, but the torrent
// it carries still counts as already linked, so the limit window orders on it.
func TestBuildLinkLibraryIndex_RetiredRowIsNotACandidate(t *testing.T) {
	root := "/lib"
	rows := []database.BookFileCore{
		{ID: "live1", BookID: "holder", FilePath: filepath.Join(root, "h", "01.mp3"), FileSize: 100,
			DelugeOriginalPath: "/dl/live/01.mp3"},
		{ID: "ret1", BookID: "holder", FilePath: filepath.Join(root, "h", "02.mp3"), FileSize: 200,
			DelugeOriginalPath: "/dl/ret/02.mp3", VersionID: "v-retired", DelugeHash: "ABCDEF0123", Retired: true},
		{ID: "ret2", BookID: "holder", FilePath: filepath.Join(root, "h", "03.mp3"), FileSize: 300, Retired: true},
	}
	idx := buildLinkLibraryIndex(root, rows)

	if got := idx.byOrigPath["/dl/ret/02.mp3"]; len(got) != 0 {
		t.Errorf("rule (a) must not offer a retired row, got %+v", got)
	}
	if got := idx.byOrigPath["/dl/live/01.mp3"]; len(got) != 1 || got[0].ID != "live1" {
		t.Errorf("rule (a) must still offer the live row, got %+v", got)
	}
	if got := idx.byBaseSize[baseSizeKey{base: "02.mp3", size: 200}]; len(got) != 0 {
		t.Errorf("rule (d) must not offer a retired row, got %+v", got)
	}
	if idx.namedVersions["holder"]["v-retired"] {
		t.Error("a retired row's version must not count as one the holder's rows name")
	}
	if n := idx.hashlessRows["holder"]; n != 1 {
		t.Errorf("hashlessRows[holder] = %d, want 1 (only the live row; the retired row carries no torrent state of the holder's)", n)
	}
	if !idx.rowHashes["abcdef0123"] {
		t.Error("the torrent a retired row carries is still linked to something and must stay in rowHashes")
	}
}
