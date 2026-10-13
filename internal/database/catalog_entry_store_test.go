// file: internal/database/catalog_entry_store_test.go
// version: 1.4.0
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

// TestEntriesForAuthorKey_ExactCappedAndSilent: the bulk matcher's block read
// takes the exact key only (a substring of a stored fold misses, where
// EntryIDsByAuthorName's fallback would walk every name key and find it), caps
// the block (an oversize block returns no ids and says so), and walks only its
// own exact prefix: the hook never sees the bare prefix of the substring walk.
func TestEntriesForAuthorKey_ExactCappedAndSilent(t *testing.T) {
	st := openTestCatalogStore(t)
	items := make([]CatalogUpsert, 0, 6)
	for i := range 5 {
		items = append(items, CatalogUpsert{Entry: CatalogEntry{Provider: "audible", Marketplace: "us",
			ProviderID: fmt.Sprintf("P%d", i), Title: fmt.Sprintf("Title %06d", i), AuthorKeys: []string{"name:author07"}}})
	}
	items = append(items, CatalogUpsert{Entry: CatalogEntry{Provider: "audible", Marketplace: "us",
		ProviderID: "P9", Title: "Title 000009", AuthorKeys: []string{"name:otherauthor08"}}})
	if _, err := st.UpsertEntries(items, "author07"); err != nil {
		t.Fatal(err)
	}
	var walked []string
	st.scanHook = func(p string) { walked = append(walked, p) }

	ids, oversize, err := st.EntriesForAuthorKey("author07", 5)
	if err != nil || oversize || len(ids) != 5 {
		t.Fatalf("exact block at the cap = %d ids, oversize %v, %v; want 5, false", len(ids), oversize, err)
	}
	if ids, oversize, err = st.EntriesForAuthorKey("author07", 4); err != nil || !oversize || ids != nil {
		t.Fatalf("block over the cap = %v, oversize %v, %v; want nil, true", ids, oversize, err)
	}
	if ids, _, _ = st.EntriesForAuthorKey("author07", 0); len(ids) != 5 {
		t.Errorf("cap 0 (no cap) = %d ids; want 5", len(ids))
	}
	for _, fold := range []string{"thor07", "author0", "author07x", "", "name:author07", "author07:"} {
		if ids, over, err := st.EntriesForAuthorKey(fold, 100); err != nil || over || len(ids) != 0 {
			t.Errorf("EntriesForAuthorKey(%q) = %v, %v, %v; want nothing", fold, ids, over, err)
		}
	}
	walked = nil
	_, _, _ = st.EntriesForAuthorKey("author07", 100)
	_, _, _ = st.EntriesForAuthorKey("thor07", 100)
	_, _, _ = st.ResolveAuthorAlias("anything")
	_, _ = st.GetEntryByProviderID("audible", "us", "P0")
	_, _ = st.GetAuthorState("author07")
	if len(ids) > 0 {
		_, _ = st.GetEntry(ids[0])
	}
	// Everything the bulk matcher calls: only exact-key walks, each reporting
	// its own exact prefix, and never the bare prefix of the substring walk.
	want := []string{catAuthorPrefix + "name:author07:", catAuthorPrefix + "name:thor07:"}
	if !slices.Equal(walked, want) {
		t.Errorf("the bulk matcher's store calls walked %q; want only the exact prefixes %q", walked, want)
	}
	for _, p := range walked {
		if p == catAuthorPrefix+"name:" {
			t.Errorf("a bulk-path call walked every author name key (%q)", p)
		}
	}
	// Control: the substring walk does find the block, and does report the bare prefix.
	walked = nil
	if sub, _ := st.EntryIDsByAuthorName("thor07", 0); len(sub) != 5 {
		t.Fatalf("control: the substring walk should find the block (got %d ids)", len(sub))
	}
	if !slices.Contains(walked, catAuthorPrefix+"name:") {
		t.Fatalf("control: the substring walk did not report the bare prefix (walked %q)", walked)
	}
}

// TestPutAuthorAliases_ResolveAmbiguousAndIdempotent: an alias resolves to its
// block; the same pair twice is one row; a second, different target makes the
// alias ambiguous for good (resolves to nothing, even for the first target);
// degenerate pairs are skipped.
func TestPutAuthorAliases_ResolveAmbiguousAndIdempotent(t *testing.T) {
	st := openTestCatalogStore(t)
	n, err := st.PutAuthorAliases([]CatalogAuthorAlias{
		{Alias: "authorseven", Target: "author07"},
		{Alias: "a07", Target: "author07"},
		{Alias: "author07", Target: "author07"},  // alias == target
		{Alias: "", Target: "author07"},          // empty
		{Alias: "x:y", Target: "author07"},       // ':' would forge a key shape
		{Alias: "jauthor", Target: "janeauthor"}, // two claimants in one call
		{Alias: "jauthor", Target: "johnauthor"}, // ... make it ambiguous
	})
	if err != nil || n != 4 {
		t.Fatalf("first write = %d, %v; want 4 (two plain, one claimed, one demoted)", n, err)
	}
	if got, ok, err := st.ResolveAuthorAlias("authorseven"); err != nil || !ok || got != "author07" {
		t.Fatalf("ResolveAuthorAlias(authorseven) = %q, %v, %v", got, ok, err)
	}
	if _, ok, _ := st.ResolveAuthorAlias("jauthor"); ok {
		t.Error("an alias claimed by two blocks in one call still resolves")
	}
	if n, _ := st.PutAuthorAliases([]CatalogAuthorAlias{{Alias: "authorseven", Target: "author07"}}); n != 0 {
		t.Errorf("rewriting the same pair wrote %d rows; want 0", n)
	}
	// A later, different claimant demotes the alias, and it stays demoted.
	if n, _ := st.PutAuthorAliases([]CatalogAuthorAlias{{Alias: "authorseven", Target: "author08"}}); n != 1 {
		t.Errorf("a conflicting claimant wrote %d rows; want 1 (the demotion)", n)
	}
	for _, target := range []string{"author07", "author08"} {
		if _, err := st.PutAuthorAliases([]CatalogAuthorAlias{{Alias: "authorseven", Target: target}}); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := st.ResolveAuthorAlias("authorseven"); ok {
			t.Errorf("a demoted alias resolved again after a write for %s", target)
		}
	}
	for _, alias := range []string{"missing", "", "a:b"} {
		if _, ok, err := st.ResolveAuthorAlias(alias); ok || err != nil {
			t.Errorf("ResolveAuthorAlias(%q) = %v, %v; want not found", alias, ok, err)
		}
	}
}
