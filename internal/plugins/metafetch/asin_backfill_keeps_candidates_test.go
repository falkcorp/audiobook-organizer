// file: internal/plugins/metafetch/asin_backfill_keeps_candidates_test.go
// version: 1.0.0
// guid: 0d5ebda3-5398-4145-9b58-a7ada44bd718
// last-edited: 2026-10-05

package metafetch

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestASINBackfill_KeepsTheCachedCandidateItConfirms is the production shape
// of the 2026-10-05 loss, end to end on a real store: a book whose cached
// candidate carries ASIN X has no ASIN, the backfill finds X on Audible and
// writes it. The write used to drop the book's candidate cache (the store
// treated any ASIN change as an identity change), so 1,078 books ended with
// an ASIN, no applied status and no candidate. The candidate must survive.
func TestASINBackfill_KeepsTheCachedCandidateItConfirms(t *testing.T) {
	fa := rrFake()
	p, s := newASINTestPlugin(t, fa)
	aid := mkAuthor(t, s, "Pierce Brown")
	b := mkBook(t, s, &database.Book{Title: "Red Rising", AuthorID: &aid, ISBN13: new(isbnRR), FilePath: "/x/rr.m4b"})
	raw, err := json.Marshal(map[string]any{
		"title": "Red Rising", "author": "Pierce Brown", "asin": rrProduct().ASIN, "source": "audible", "score": 0.93,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutMetadataCache(&database.MetadataCandidateCache{
		BookID: b.ID, FetchedAt: time.Now(), Candidates: []json.RawMessage{raw}, SourceHash: "seeded",
	}); err != nil {
		t.Fatal(err)
	}
	gen := s.MetadataCacheGeneration()

	rep := runASIN(t, p, `{"dry_run":false}`)
	if got := asinOf(t, s, b.ID); got != rrProduct().ASIN {
		t.Fatalf("asin = %q, want %s", got, rrProduct().ASIN)
	}
	if res := rep.res(t); res.MatchedWritten != 1 {
		t.Fatalf("result = %+v, want one ASIN written", res)
	}
	entry, err := s.GetMetadataCache(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil || len(entry.Candidates) != 1 {
		t.Fatalf("cached candidates after the backfill = %+v, want the one it confirmed", entry)
	}
	if got := s.MetadataCacheGeneration(); got != gen {
		t.Fatalf("cache generation moved %d -> %d: a cache row was written or deleted", gen, got)
	}
}
