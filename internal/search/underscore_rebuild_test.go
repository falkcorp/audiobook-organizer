// file: internal/search/underscore_rebuild_test.go
// version: 1.0.0
// guid: 2f8c6e1a-9d4b-4c37-8a15-6b0e3d7f2c94
// last-edited: 2026-09-25
//
// Mapping v3: '_' folds to a space before tokenising, the v2 -> v3 bump
// recreates the index, and a recreated index stays "rebuilding" across a
// restart until the server confirms coverage.

package search

import (
	"os"
	"path/filepath"
	"testing"
)

// searchIDsFor runs q through the same parse -> translate path the library
// search uses and returns the hit IDs.
func searchIDsFor(t *testing.T, idx *BleveIndex, q string) []string {
	t.Helper()
	node, err := ParseQuery(q)
	if err != nil {
		t.Fatalf("ParseQuery(%q): %v", q, err)
	}
	bq, _, err := Translate(node)
	if err != nil {
		t.Fatalf("Translate(%q): %v", q, err)
	}
	hits, _, err := idx.SearchNative(bq, 0, 10)
	if err != nil {
		t.Fatalf("SearchNative(%q): %v", q, err)
	}
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.BookID)
	}
	return out
}

// The production book 01M26JSRNY5M699C55S3ZPP3HX carried exactly this
// filename-derived title and was unreachable by any word query.
func TestUnderscoreTitleIsFoundByWords(t *testing.T) {
	idx := indexAt(t, filepath.Join(t.TempDir(), "library.bleve"))
	if err := idx.IndexBook(BookDocument{BookID: "arcane", Title: "Arcane_Chef_2__A_LitRPG_Adventure"}); err != nil {
		t.Fatalf("IndexBook: %v", err)
	}
	if err := idx.IndexBook(BookDocument{BookID: "other", Title: "Monster Makers"}); err != nil {
		t.Fatalf("IndexBook: %v", err)
	}

	for _, q := range []string{
		"arcane chef",       // plain words: analysed match queries
		"Arcane Chef",       // capitalised
		`"arcane chef"`,     // phrase: positions must be adjacent after the fold
		"litrpg adventure",  // interior words
		"arcane_chef",       // the query itself carries underscores
		"arcane_ch*",        // prefix bypasses the analyser; must be split
		"title:arcane_chef", // fielded
		"title:arcane_ch*",  // fielded prefix
	} {
		got := searchIDsFor(t, idx, q)
		if len(got) != 1 || got[0] != "arcane" {
			t.Errorf("query %q: hits = %v, want [arcane]", q, got)
		}
	}
}

func TestMappingV2IndexIsRecreatedAsRebuilding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.bleve")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	seedOne(t, first)
	if err := first.MarkRebuilt(); err != nil {
		t.Fatalf("MarkRebuilt: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Production's marker on the day v3 ships.
	if err := os.WriteFile(mappingMarkerPath(path), []byte("2\n"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !second.RecreatedForMappingChange() {
		t.Fatal("a v2 index was opened as-is; the underscore fold would never reach production")
	}
	if n := docCount(t, second); n != 0 {
		t.Fatalf("recreated index DocCount = %d, want 0", n)
	}
	if !second.Rebuilding() {
		t.Fatal("recreated index is not marked rebuilding; search would serve an empty index as complete")
	}
	if err := second.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Restart mid-drain: RecreatedForMappingChange is false now, so the
	// durable marker is the only thing still saying "incomplete".
	third := indexAt(t, path)
	if third.RecreatedForMappingChange() {
		t.Fatal("second open recreated again")
	}
	if !third.Rebuilding() {
		t.Fatal("rebuilding state was lost across a restart; a partial index would be served as complete")
	}
	if err := third.MarkRebuilt(); err != nil {
		t.Fatalf("MarkRebuilt: %v", err)
	}
	if third.Rebuilding() {
		t.Fatal("MarkRebuilt did not clear the in-memory flag")
	}
	if _, err := os.Stat(rebuildingMarkerPath(path)); !os.IsNotExist(err) {
		t.Fatalf("rebuilding marker still on disk after MarkRebuilt (stat err=%v)", err)
	}
}

func TestApplyBatchUpsertsAndDeletesTogether(t *testing.T) {
	idx := indexAt(t, filepath.Join(t.TempDir(), "library.bleve"))
	if err := idx.IndexBook(BookDocument{BookID: "gone", Title: "Old"}); err != nil {
		t.Fatalf("IndexBook: %v", err)
	}
	if err := idx.ApplyBatch([]BookDocument{{BookID: "a", Title: "Alpha"}, {BookID: "b", Title: "Beta"}}, []string{"gone"}); err != nil {
		t.Fatalf("ApplyBatch: %v", err)
	}
	ids, err := idx.AllDocIDs()
	if err != nil {
		t.Fatalf("AllDocIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("doc IDs = %v, want [a b]", ids)
	}
	if d, n := idx.OldestWrite(); n != 0 || d != 0 {
		t.Fatalf("OldestWrite after all writes returned = (%s, %d), want (0, 0)", d, n)
	}
	if st := idx.HealthStats(); len(st) == 0 {
		t.Fatal("HealthStats returned nothing; the stall watchdog would log zeros")
	}
}
