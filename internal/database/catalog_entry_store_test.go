// file: internal/database/catalog_entry_store_test.go
// version: 1.2.0
// guid: a6a750aa-9895-4d5f-ba74-2479200df2d3
// last-edited: 2026-10-12

package database

import (
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
)

func openTestCatalogStore(t *testing.T) *CatalogStore {
	t.Helper()
	// In-memory: these writes fsync on a real filesystem and nothing reopens.
	db, err := pebble.Open("catalog.pebble", &pebble.Options{FS: vfs.NewMem()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewCatalogStore(NewGatedDB(db))
}

// TestCatalogList_UnfilteredUsesKeysOnlyIndex: the unfiltered list and the
// entry count must never iterate the cat: prefix (every ~1 KB entry value);
// they walk the keys-only cat_id index, page by offset/limit, and decode only
// the entries in the window.
func TestCatalogList_UnfilteredUsesKeysOnlyIndex(t *testing.T) {
	st := openTestCatalogStore(t)
	items := make([]CatalogUpsert, 7)
	for i := range items {
		items[i] = CatalogUpsert{Entry: CatalogEntry{Provider: "audible", Marketplace: "us",
			ProviderID: fmt.Sprintf("P%d", i), Title: fmt.Sprintf("T%d", i), AuthorKeys: []string{"name:ann"}}}
	}
	res, err := st.UpsertEntries(items, "ann")
	if err != nil {
		t.Fatal(err)
	}
	ids := slices.Clone(res.IDs)
	slices.Sort(ids)

	var mu sync.Mutex
	var scanned []string
	st.scanHook = func(p string) { mu.Lock(); scanned = append(scanned, p); mu.Unlock() }

	page, total, err := st.ListEntries(CatalogListQuery{Limit: 2, Offset: 3})
	if err != nil {
		t.Fatal(err)
	}
	if total != 7 || len(page) != 2 || page[0].ID != ids[3] || page[1].ID != ids[4] {
		t.Fatalf("page = %d entries (total %d); want ids[3:5] of 7 in id order", len(page), total)
	}
	if page, total, _ := st.ListEntries(CatalogListQuery{Limit: 5, Offset: 9}); len(page) != 0 || total != 7 {
		t.Errorf("offset past the end: %d entries, total %d", len(page), total)
	}
	if page, total, _ := st.ListEntries(CatalogListQuery{AuthorKey: "name:ann", Limit: 3}); len(page) != 3 || total != 7 {
		t.Errorf("author filter: %d entries, total %d; want 3 of 7", len(page), total)
	}
	if n, err := st.CountEntries(); err != nil || n != 7 {
		t.Errorf("CountEntries = %d, %v; want 7", n, err)
	}
	if len(scanned) == 0 {
		t.Fatal("no scan was observed")
	}
	for _, p := range scanned {
		if p == catEntryPrefix {
			t.Errorf("a list/count scan iterated the entry values (%q); scans: %v", p, scanned)
		}
	}
	// Re-upserting must not duplicate the id index.
	if _, err := st.UpsertEntries(items, "ann"); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountEntries(); n != 7 {
		t.Errorf("CountEntries after re-upsert = %d; want 7", n)
	}
}

// TestHasAuthorKey_ExactOnly: HasAuthorKey answers for the exact fold only. A
// substring of a stored fold, a longer fold, an unfolded spelling and a key of
// another kind all miss; a co-author's key on a harvested entry hits (the
// report's upper bound). EntryIDsByAuthorName's substring fallback is never
// walked.
func TestHasAuthorKey_ExactOnly(t *testing.T) {
	st := openTestCatalogStore(t)
	if _, err := st.UpsertEntries([]CatalogUpsert{
		{Entry: CatalogEntry{Provider: "audible", Marketplace: "us", ProviderID: "P1", Title: "T1", AuthorKeys: []string{"name:author07"}}},
		{Entry: CatalogEntry{Provider: "audible", Marketplace: "us", ProviderID: "P2", Title: "T2", AuthorKeys: []string{"name:author07", "name:coauthor08", "asin:B000000001"}}},
	}, "author07"); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var scanned []string
	st.scanHook = func(p string) { mu.Lock(); scanned = append(scanned, p); mu.Unlock() }

	for fold, want := range map[string]bool{
		"author07":        true,
		"thor07":          false, // substring of a stored fold
		"author0":         false, // prefix of a stored fold
		"author07x":       false, // longer than a stored fold
		"AUTHOR07":        false, // the store does not fold: callers pass authorjunk.FoldKey output
		"author07 ":       false, // trailing space is not trimmed
		"asin:B000000001": false, // a name lookup never reaches the asin keys
		"coauthor08":      true,  // a co-author's key counts although nothing was harvested for them: why the coverage report is an upper bound
		"":                false,
	} {
		got, err := st.HasAuthorKey(fold)
		if err != nil {
			t.Fatalf("HasAuthorKey(%q): %v", fold, err)
		}
		if got != want {
			t.Errorf("HasAuthorKey(%q) = %v; want %v", fold, got, want)
		}
	}
	for _, p := range scanned {
		if p == catAuthorPrefix+"name:" {
			t.Errorf("HasAuthorKey walked every name key (%q)", p)
		}
	}
	// The substring fallback of EntryIDsByAuthorName does find "thor07": the
	// contrast that makes the exact-only contract worth testing.
	if ids, err := st.EntryIDsByAuthorName("thor07", 0); err != nil || len(ids) != 2 {
		t.Fatalf("EntryIDsByAuthorName(thor07) = %v, %v; want 2 ids via the substring walk", ids, err)
	}
}

// TestCountAuthorNameBlocks counts distinct name keys, not entries or
// non-name author keys.
func TestCountAuthorNameBlocks(t *testing.T) {
	st := openTestCatalogStore(t)
	if n, err := st.CountAuthorNameBlocks(); err != nil || n != 0 {
		t.Fatalf("empty catalog: %d, %v; want 0", n, err)
	}
	if _, err := st.UpsertEntries([]CatalogUpsert{
		{Entry: CatalogEntry{Provider: "audible", Marketplace: "us", ProviderID: "P1", Title: "T1", AuthorKeys: []string{"name:ann", "name:bob", "asin:B000000001"}}},
		{Entry: CatalogEntry{Provider: "audible", Marketplace: "us", ProviderID: "P2", Title: "T2", AuthorKeys: []string{"name:ann"}}},
	}, "ann"); err != nil {
		t.Fatal(err)
	}
	if n, err := st.CountAuthorNameBlocks(); err != nil || n != 2 {
		t.Fatalf("CountAuthorNameBlocks = %d, %v; want 2 (ann, bob)", n, err)
	}
}
