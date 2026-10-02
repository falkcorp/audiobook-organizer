// file: internal/database/catalog_entry_store_test.go
// version: 1.0.0
// guid: a6a750aa-9895-4d5f-ba74-2479200df2d3
// last-edited: 2026-10-01

package database

import (
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

func openTestCatalogStore(t *testing.T) *CatalogStore {
	t.Helper()
	db, err := pebble.Open(t.TempDir(), &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewCatalogStore(db)
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
