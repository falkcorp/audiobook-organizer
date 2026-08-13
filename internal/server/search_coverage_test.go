// file: internal/server/search_coverage_test.go
// version: 1.0.0
// guid: 000a3aed-48a5-49fd-b36a-d52d7d4de58e
// last-edited: 2026-08-13
//
// Regression tests for a PARTIALLY built search index.
//
// The prod bug (2026-08-13): a Library search for "All Jobs and Classes"
// returned five unrelated books. The two rows that actually matched had
// never been indexed, because buildSearchIndexIfEmpty gates on
// DocCount() == 0 and a prior shutdown had cancelled the build part-way.
// The five survivors matched only on the description field.
//
// These tests are COVERAGE tests, not query-quality tests. The query layer
// is already covered by internal/search/zz_repro_alljobs_test.go, which
// seeds its own index and therefore cannot fail on an index that is missing
// documents — the failure mode reproduced here.

package server

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// seedPartialIndex creates n books and indexes only the first indexed of
// them, reproducing the state a bgCtx-cancelled backfill leaves behind.
// Book IDs are zero-padded so their lexical order matches creation order,
// mirroring the ULID ordering the real backfill walks.
func seedPartialIndex(t *testing.T, store *database.PebbleStore, idx *search.BleveIndex, books []database.Book, indexed int) {
	t.Helper()
	for i := range books {
		if _, err := store.CreateBook(&books[i]); err != nil {
			t.Fatalf("create %s: %v", books[i].ID, err)
		}
		if i < indexed {
			if err := idx.IndexBook(search.BookToDoc(store, &books[i])); err != nil {
				t.Fatalf("index %s: %v", books[i].ID, err)
			}
		}
	}
}

func strptr(s string) *string { return &s }

// coverageBooks returns three books whose titles carry the query terms and
// three decoys that carry them only in the description — the exact shape of
// the prod result set.
func coverageBooks() []database.Book {
	return []database.Book{
		// Indexed first (oldest): description-only matches. These are the
		// rows that survived on prod and were shown to the owner.
		{ID: "cov-01", Title: "Dragon Conjurer", FilePath: "/tmp/cov01", Format: "m4b",
			Description: strptr("I went to class, worked my minimum-wage job, and studied.")},
		{ID: "cov-02", Title: "Parallax Rising", FilePath: "/tmp/cov02", Format: "m4b",
			Description: strptr("She left class early and took a second job.")},
		{ID: "cov-03", Title: "All in Charisma", FilePath: "/tmp/cov03", Format: "m4b",
			Description: strptr("A job, a class, and a very long night.")},
		// Never reached by the cancelled backfill (newest): the real matches.
		{ID: "cov-04", Title: "All Jobs and Classes! I Just Wanted One Skill", FilePath: "/tmp/cov04", Format: "m4b"},
		{ID: "cov-05", Title: "All Jobs and Classes! Book II: Ascension", FilePath: "/tmp/cov05", Format: "m4b"},
		{ID: "cov-06", Title: "All Jobs and Classes! Book III: Dominion", FilePath: "/tmp/cov06", Format: "m4b"},
	}
}

// coverageProbes maps each book to a title term unique to it, so "is this
// book in the index?" is a real query rather than a DocCount inference.
// Bleve has no queryable document-ID field here, so a per-book term is the
// only way to assert reachability of a specific row.
var coverageProbes = map[string]string{
	"cov-01": "title:conjurer",
	"cov-02": "title:parallax",
	"cov-03": "title:charisma",
	"cov-04": "title:skill",
	"cov-05": "title:ascension",
	"cov-06": "title:dominion",
}

// TestSearchCoverage_PartialBackfillIsRepaired is the regression test.
//
// It fails on the pre-fix code: reconcileSearchIndexCoverage did not exist,
// buildSearchIndexIfEmpty returned early because DocCount() was 3 (> 0), and
// cov-04..06 stayed invisible forever.
func TestSearchCoverage_PartialBackfillIsRepaired(t *testing.T) {
	srv, store, idx := newDropOnlyServer(t)
	books := coverageBooks()
	seedPartialIndex(t, store, idx, books, 3)

	// Precondition: the index really is partial, or the test proves nothing.
	docs, err := idx.DocCount()
	if err != nil {
		t.Fatalf("doccount: %v", err)
	}
	if docs != 3 {
		t.Fatalf("precondition: indexed %d docs, want 3", docs)
	}

	// The old path is a no-op on a non-empty index. Assert that explicitly so
	// this test also documents WHY a second mechanism is needed.
	srv.buildSearchIndexIfEmpty()
	if d, _ := idx.DocCount(); d != 3 {
		t.Fatalf("buildSearchIndexIfEmpty changed a non-empty index: %d docs", d)
	}

	// The repair under test: seed the dirty set, then let the reconciler drain.
	srv.reconcileSearchIndexCoverage()
	srv.reconcileOnce()

	if d, _ := idx.DocCount(); d != uint64(len(books)) {
		t.Fatalf("after repair: %d docs indexed, want %d", d, len(books))
	}

	// Every book must now be reachable, especially the newest cohort — the
	// one a ULID-ordered cancellation always loses.
	for _, b := range books {
		probe, ok := coverageProbes[b.ID]
		if !ok {
			t.Fatalf("no probe term defined for %s", b.ID)
		}
		hits, _, err := idx.Search(probe, 0, 10)
		if err != nil {
			t.Fatalf("search %s: %v", b.ID, err)
		}
		found := false
		for _, h := range hits {
			if h.BookID == b.ID {
				found = true
			}
		}
		if !found {
			t.Errorf("book %s (%q) still missing from the index after repair (probe %q)", b.ID, b.Title, probe)
		}
	}
}

// TestSearchCoverage_PrecisionAfterRepair asserts the owner-visible symptom
// is gone: the real match ranks FIRST and the description-only decoys are
// not what the user is handed.
//
// Presence alone is not enough — internal/search/multiword_repro_test.go
// asserts only presence, which is why a MatchAll passes all ten of its
// cases. This asserts precision.
func TestSearchCoverage_PrecisionAfterRepair(t *testing.T) {
	srv, store, idx := newDropOnlyServer(t)
	books := coverageBooks()
	seedPartialIndex(t, store, idx, books, 3)

	srv.reconcileSearchIndexCoverage()
	srv.reconcileOnce()

	hits, total, err := idx.Search("title:jobs title:classes", 0, 20)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if total == 0 || len(hits) == 0 {
		t.Fatal("query returned nothing; the index is still not serving these books")
	}

	// Correct book first.
	want := map[string]bool{"cov-04": true, "cov-05": true, "cov-06": true}
	if !want[hits[0].BookID] {
		t.Errorf("top hit = %s, want one of the real title matches (cov-04..06)", hits[0].BookID)
	}

	// Unrelated books absent: a title-scoped query must not return the rows
	// whose only connection is a description mentioning a job and a class.
	decoys := map[string]string{"cov-01": "Dragon Conjurer", "cov-02": "Parallax Rising", "cov-03": "All in Charisma"}
	for _, h := range hits {
		if title, isDecoy := decoys[h.BookID]; isDecoy {
			t.Errorf("unrelated book %s (%q) present in title-scoped results", h.BookID, title)
		}
	}
}
