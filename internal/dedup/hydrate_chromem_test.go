// file: internal/dedup/hydrate_chromem_test.go
// version: 1.4.0
// guid: 3c4d5e6f-7a8b-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-08-29

package dedup

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// fakeVectorANNStore is a minimal in-memory database.VectorANNStore double
// that records every Upsert call so tests can assert which entities were (or
// were not) mirrored during HydrateChromem.
// It can also be told to FAIL specific keys, so a test can cover the case
// where a row passes every filter but its write into the index errors — the
// case that used to be invisible, because the mirror helpers swallowed the
// Upsert error and the hydrated counter incremented anyway.
type fakeVectorANNStore struct {
	upserted  map[string]bool // "entityType/entityID" -> Upsert RETURNED SUCCESS
	attempted map[string]bool // "entityType/entityID" -> Upsert was CALLED
	failOn    map[string]bool // "entityType/entityID" -> Upsert returns an error
}

// errFakeUpsert is a package-level sentinel so tests can assert on it with
// errors.Is rather than substring-matching the message.
var errFakeUpsert = errors.New("chromem: simulated upsert failure")

func newFakeVectorANNStore() *fakeVectorANNStore {
	return &fakeVectorANNStore{
		upserted:  map[string]bool{},
		attempted: map[string]bool{},
		failOn:    map[string]bool{},
	}
}

// Upsert records the ATTEMPT separately from the SUCCESS. Without that split,
// "row X is absent from upserted" is satisfied both by a row the engine
// correctly filtered out and by a row whose write failed — so an assertion on
// a failing key would be guaranteed by the double's own construction and would
// test nothing.
func (f *fakeVectorANNStore) Upsert(_ context.Context, entityType, entityID string, _ []float32, _ map[string]string) error {
	key := entityType + "/" + entityID
	f.attempted[key] = true
	if f.failOn[key] {
		return errFakeUpsert
	}
	f.upserted[key] = true
	return nil
}
func (f *fakeVectorANNStore) Get(_ context.Context, _, _ string) (map[string]string, error) {
	return nil, nil
}
func (f *fakeVectorANNStore) Delete(_ context.Context, _, _ string) error { return nil }
func (f *fakeVectorANNStore) FindSimilar(_ context.Context, _ string, _ []float32, _ int, _ map[string]string) ([]database.ChromemSimilarityResult, error) {
	return nil, nil
}
func (f *fakeVectorANNStore) CountByType(_ context.Context, _ string) (int, error) { return 0, nil }
func (f *fakeVectorANNStore) Close() error                                         { return nil }

// TestHydrateChromem_SkipsStaleModelRows verifies the guard added after the
// bge-m3 cutover: a row stamped with a different model than the currently
// wired embed client is skipped instead of being mirrored into the ANN
// store, where it would only fail the store's dimension check and log a
// warning. Covers both the book and author loops, and both the "stale but
// re-embeddable" case (a book/author that still exists) and the "orphaned"
// case (an author ID that no longer resolves via GetAuthorByID, e.g. after a
// merge) — HydrateChromem should skip the stale row before ever needing to
// look the entity up.
func TestHydrateChromem_SkipsStaleModelRows(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	fake := newFakeVectorANNStore()
	engine.SetChromemStore(fake)
	engine.embedClient = ai.NewEmbeddingClientWithOptions("k", "bge-m3", "")

	primary := true
	currentBook := &database.Book{ID: "BOOK_CURRENT", Title: "Current Model Book", IsPrimaryVersion: &primary}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if id == "BOOK_CURRENT" {
			return currentBook, nil
		}
		// BOOK_STALE deliberately still resolves — the row must be skipped
		// on the model check alone, without ever reaching the book lookup.
		return &database.Book{ID: id, Title: "Stale Model Book", IsPrimaryVersion: &primary}, nil
	}

	if err := es.Upsert(database.Embedding{EntityType: "book", EntityID: "BOOK_CURRENT", Vector: []float32{1, 2, 3, 4}, Model: "bge-m3"}); err != nil {
		t.Fatalf("seed current-model book: %v", err)
	}
	if err := es.Upsert(database.Embedding{EntityType: "book", EntityID: "BOOK_STALE", Vector: []float32{1, 2, 3}, Model: "text-embedding-3-large"}); err != nil {
		t.Fatalf("seed stale-model book: %v", err)
	}

	// AUTHOR_ORPHAN mimics the real prod scenario: an embedding row exists
	// for an author ID that no longer exists (merged/deleted), so it isn't
	// in the entity table at all. The guard must skip it purely on model
	// mismatch, never needing to resolve the author.
	if err := es.Upsert(database.Embedding{EntityType: "author", EntityID: "9999", Vector: []float32{5, 6, 7, 8}, Model: "bge-m3"}); err != nil {
		t.Fatalf("seed current-model author: %v", err)
	}
	if err := es.Upsert(database.Embedding{EntityType: "author", EntityID: "AUTHOR_ORPHAN", Vector: []float32{5, 6, 7}, Model: "text-embedding-3-large"}); err != nil {
		t.Fatalf("seed orphaned stale-model author: %v", err)
	}

	stats, err := engine.HydrateChromem(context.Background())
	if err != nil {
		t.Fatalf("HydrateChromem: %v", err)
	}

	if stats.BooksHydrated != 1 {
		t.Errorf("BooksHydrated = %d, want 1", stats.BooksHydrated)
	}
	if stats.AuthorsHydrated != 1 {
		t.Errorf("AuthorsHydrated = %d, want 1", stats.AuthorsHydrated)
	}
	if stats.BooksStaleModel != 1 {
		t.Errorf("BooksStaleModel = %d, want 1", stats.BooksStaleModel)
	}
	if stats.AuthorsStaleModel != 1 {
		t.Errorf("AuthorsStaleModel = %d, want 1", stats.AuthorsStaleModel)
	}
	if !fake.upserted["book/BOOK_CURRENT"] {
		t.Error("expected current-model book to be mirrored into the ANN store")
	}
	if fake.upserted["book/BOOK_STALE"] {
		t.Error("stale-model book should NOT have been mirrored into the ANN store")
	}
	if !fake.upserted["author/9999"] {
		t.Error("expected current-model author to be mirrored into the ANN store")
	}
	if fake.upserted["author/AUTHOR_ORPHAN"] {
		t.Error("orphaned stale-model author should NOT have been mirrored into the ANN store")
	}
}

// TestHydrateChromem_AccountsForEverySkipPath is the fixture that makes the
// 2026-08-29 defect observable. Production read 39,658 book embedding rows and
// indexed 17,706 of them; 21,952 disappeared and exactly one (a stale-model
// row) was reported anywhere, because three of the four book skip paths
// incremented no counter and logged nothing.
//
// The fixture contains rows of EVERY skip category plus rows that must
// survive, at a DISTINCT COUNT PER BUCKET:
//
//	2 x HYDRATED_*    primary=true             -> BooksHydrated
//	1 x NILPRIMARY_*  IsPrimaryVersion==nil    -> BooksHydrated (the DEFAULT
//	                                              case in production; a nil
//	                                              pointer is not "non-primary")
//	3 x EMPTY_*       zero-length vector       -> BooksEmptyVector
//	4 x STALE_*       model=text-embedding-3-* -> BooksStaleModel
//	5 x ORPHAN_*      GetBookByID -> (nil,nil) -> BooksOrphaned
//	6 x LOOKUPERR_*   GetBookByID -> (nil,err) -> BooksLookupError
//	7 x NONPRIMARY_*  primary=false            -> BooksNonPrimary
//	8 x MIRRORERR_*   ANN store Upsert -> err  -> BooksMirrorError
//
// and 1/2/3/4 rows for the author hydrated/empty/stale/mirror-error buckets.
//
// MIRRORERR_* is the bucket that keeps BooksHydrated honest: those rows pass
// every filter and reach the write, so before this bucket existed they were
// counted as hydrated even though the index rejected them.
//
// WHY the counts differ per bucket instead of one row each: the one-row-each
// version of this fixture was written first and a mutation test walked
// straight through it. Swapping the BooksOrphaned and BooksNonPrimary
// increments in engine.go left every assertion green — both buckets held 1, so
// 1↔1 is invisible, and the row totals are unchanged so the arithmetic
// identity below did not notice either. Distinct expected counts make every
// pairwise mis-assignment change two numbers at once.
//
// The counts are asserted one bucket at a time AND as the arithmetic identity
// BookRows == BooksAccounted(). The identity catches a future skip path added
// with no counter — the exact defect class this test exists for — while the
// individual assertions catch an increment landing in the wrong bucket, which
// the identity alone cannot see.
func TestHydrateChromem_AccountsForEverySkipPath(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	fake := newFakeVectorANNStore()
	engine.SetChromemStore(fake)
	engine.embedClient = ai.NewEmbeddingClientWithOptions("k", "bge-m3", "")

	primary, nonPrimary := true, false
	lookupErr := errors.New("pebble: simulated read fault")

	const (
		wantHydratedPrimary    = 2
		wantHydratedNilFlag    = 1
		wantEmptyVector        = 3
		wantStaleModel         = 4
		wantOrphaned           = 5
		wantLookupError        = 6
		wantNonPrimary         = 7
		wantMirrorError        = 8
		wantAuthorsHydrated    = 1
		wantAuthorsEmptyVec    = 2
		wantAuthorsStaleModel  = 3
		wantAuthorsMirrorError = 4
	)

	// Keyed by ID prefix — a catch-all returning a book for any unrecognised
	// ID (as the sibling test above does) would silently reclassify the orphan
	// rows as hydrated and this test would prove nothing.
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		switch {
		case strings.HasPrefix(id, "HYDRATED_"), strings.HasPrefix(id, "MIRRORERR_"):
			// MIRRORERR_* books are perfectly good — it is the ANN store's
			// Upsert that rejects them (see fake.failOn below).
			return &database.Book{ID: id, Title: "Good", IsPrimaryVersion: &primary}, nil
		case strings.HasPrefix(id, "NILPRIMARY_"):
			// IsPrimaryVersion left nil on purpose: nil serializes as ABSENT
			// and is the default across most of the production library.
			return &database.Book{ID: id, Title: "Unset Primary Flag"}, nil
		case strings.HasPrefix(id, "NONPRIMARY_"):
			return &database.Book{ID: id, Title: "Alt Version", IsPrimaryVersion: &nonPrimary}, nil
		case strings.HasPrefix(id, "LOOKUPERR_"):
			return nil, lookupErr
		case strings.HasPrefix(id, "ORPHAN_"):
			// PebbleStore.GetBookByID maps pebble.ErrNotFound to a bare
			// (nil, nil) — no sentinel — so this is production's real
			// not-found shape, not a mock-only convenience.
			return nil, nil
		}
		t.Errorf("unexpected GetBookByID(%q) — the stale-model and empty-vector rows must be skipped before the lookup", id)
		return nil, nil
	}

	vec := []float32{1, 2, 3, 4}
	// seedBooks writes n book rows named "<prefix>_<i>" — the prefix is what
	// GetBookByIDFunc above dispatches on — and returns their "book/<id>" keys
	// so the ANN-store assertions can name every seeded row exactly.
	seedBooks := func(prefix string, n int, v []float32, model string) []string {
		t.Helper()
		keys := make([]string, 0, n)
		for i := range n {
			id := fmt.Sprintf("%s_%d", prefix, i)
			if err := es.Upsert(database.Embedding{EntityType: "book", EntityID: id, Vector: v, Model: model}); err != nil {
				t.Fatalf("seed book/%s: %v", id, err)
			}
			keys = append(keys, "book/"+id)
		}
		return keys
	}
	// seedAuthors mints stringified-int entity IDs from one shared counter, so
	// no two author buckets can collide on a key (which would silently shrink
	// the fixture and make a bucket unobservable).
	nextAuthorID := 1000
	seedAuthors := func(n int, v []float32, model string) []string {
		t.Helper()
		keys := make([]string, 0, n)
		for range n {
			nextAuthorID++
			id := strconv.Itoa(nextAuthorID)
			if err := es.Upsert(database.Embedding{EntityType: "author", EntityID: id, Vector: v, Model: model}); err != nil {
				t.Fatalf("seed author/%s: %v", id, err)
			}
			keys = append(keys, "author/"+id)
		}
		return keys
	}

	wantMirrored := append(
		seedBooks("HYDRATED", wantHydratedPrimary, vec, "bge-m3"),
		seedBooks("NILPRIMARY", wantHydratedNilFlag, vec, "bge-m3")...)
	wantSkipped := seedBooks("EMPTY", wantEmptyVector, nil, "bge-m3")
	wantSkipped = append(wantSkipped, seedBooks("STALE", wantStaleModel, vec, "text-embedding-3-large")...)
	wantSkipped = append(wantSkipped, seedBooks("ORPHAN", wantOrphaned, vec, "bge-m3")...)
	wantSkipped = append(wantSkipped, seedBooks("LOOKUPERR", wantLookupError, vec, "bge-m3")...)
	wantSkipped = append(wantSkipped, seedBooks("NONPRIMARY", wantNonPrimary, vec, "bge-m3")...)

	wantMirrored = append(wantMirrored, seedAuthors(wantAuthorsHydrated, vec, "bge-m3")...)
	wantSkipped = append(wantSkipped, seedAuthors(wantAuthorsEmptyVec, nil, "bge-m3")...)
	wantSkipped = append(wantSkipped, seedAuthors(wantAuthorsStaleModel, vec, "text-embedding-3-large")...)

	// Rows that pass every filter but whose write into the index fails. Seeded
	// last so the keys are known, then armed on the fake store.
	mirrorFailBooks := seedBooks("MIRRORERR", wantMirrorError, vec, "bge-m3")
	mirrorFailAuthors := seedAuthors(wantAuthorsMirrorError, vec, "bge-m3")
	for _, key := range append(append([]string{}, mirrorFailBooks...), mirrorFailAuthors...) {
		fake.failOn[key] = true
	}
	wantSkipped = append(wantSkipped, mirrorFailBooks...)
	wantSkipped = append(wantSkipped, mirrorFailAuthors...)

	wantBookRows := wantHydratedPrimary + wantHydratedNilFlag + wantEmptyVector +
		wantStaleModel + wantOrphaned + wantLookupError + wantNonPrimary + wantMirrorError
	wantAuthorRows := wantAuthorsHydrated + wantAuthorsEmptyVec + wantAuthorsStaleModel +
		wantAuthorsMirrorError

	// Confirm the fixture can actually observe each bucket before trusting the
	// counters: an empty-vector row has to survive the encode/decode round
	// trip as zero-length, or that bucket is untestable through this seam.
	rows, err := es.ListByType("book")
	if err != nil {
		t.Fatalf("ListByType(book): %v", err)
	}
	if len(rows) != wantBookRows {
		t.Fatalf("fixture seeded %d book rows, want %d", len(rows), wantBookRows)
	}
	emptyRows := 0
	for _, r := range rows {
		if strings.HasPrefix(r.EntityID, "EMPTY_") {
			if len(r.Vector) != 0 {
				t.Fatalf("fixture cannot observe the empty-vector bucket: %s round-tripped with %d elements", r.EntityID, len(r.Vector))
			}
			emptyRows++
		}
	}
	if emptyRows != wantEmptyVector {
		t.Fatalf("fixture holds %d empty-vector book rows, want %d", emptyRows, wantEmptyVector)
	}

	stats, err := engine.HydrateChromem(context.Background())
	if err != nil {
		t.Fatalf("HydrateChromem: %v", err)
	}

	// --- every bucket, individually ---
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{"BookRows", stats.BookRows, wantBookRows},
		{"BooksHydrated", stats.BooksHydrated, wantHydratedPrimary + wantHydratedNilFlag},
		{"BooksEmptyVector", stats.BooksEmptyVector, wantEmptyVector},
		{"BooksStaleModel", stats.BooksStaleModel, wantStaleModel},
		{"BooksOrphaned", stats.BooksOrphaned, wantOrphaned},
		{"BooksLookupError", stats.BooksLookupError, wantLookupError},
		{"BooksNonPrimary", stats.BooksNonPrimary, wantNonPrimary},
		{"BooksMirrorError", stats.BooksMirrorError, wantMirrorError},
		{"AuthorRows", stats.AuthorRows, wantAuthorRows},
		{"AuthorsHydrated", stats.AuthorsHydrated, wantAuthorsHydrated},
		{"AuthorsEmptyVector", stats.AuthorsEmptyVector, wantAuthorsEmptyVec},
		{"AuthorsStaleModel", stats.AuthorsStaleModel, wantAuthorsStaleModel},
		{"AuthorsMirrorError", stats.AuthorsMirrorError, wantAuthorsMirrorError},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}

	// --- the arithmetic identity: nothing may vanish unnamed ---
	if stats.BooksAccounted() != stats.BookRows {
		t.Errorf("book buckets do not account for every row: BooksAccounted()=%d, BookRows=%d (%d rows vanished into an uncounted skip path)",
			stats.BooksAccounted(), stats.BookRows, stats.BookRows-stats.BooksAccounted())
	}
	if stats.AuthorsAccounted() != stats.AuthorRows {
		t.Errorf("author buckets do not account for every row: AuthorsAccounted()=%d, AuthorRows=%d (%d rows vanished into an uncounted skip path)",
			stats.AuthorsAccounted(), stats.AuthorRows, stats.AuthorRows-stats.AuthorsAccounted())
	}
	if got, want := stats.BooksSkipped(), wantBookRows-(wantHydratedPrimary+wantHydratedNilFlag); got != want {
		t.Errorf("BooksSkipped() = %d, want %d", got, want)
	}
	if got, want := stats.AuthorsSkipped(), wantAuthorRows-wantAuthorsHydrated; got != want {
		t.Errorf("AuthorsSkipped() = %d, want %d", got, want)
	}

	// --- a bare count gives nobody anything to debug; keep the samples ---
	if stats.FirstBookLookupError == nil {
		t.Error("FirstBookLookupError = nil, want the sampled lookup fault")
	} else if !errors.Is(stats.FirstBookLookupError, lookupErr) {
		t.Errorf("FirstBookLookupError = %v, want it to wrap %v", stats.FirstBookLookupError, lookupErr)
	}
	if stats.FirstMirrorError == nil {
		t.Error("FirstMirrorError = nil, want the sampled ANN-store write failure")
	} else if !errors.Is(stats.FirstMirrorError, errFakeUpsert) {
		t.Errorf("FirstMirrorError = %v, want it to wrap %v", stats.FirstMirrorError, errFakeUpsert)
	}

	// --- a completed run must not be flagged incomplete ---
	if stats.Incomplete {
		t.Error("Incomplete = true on a run with a live context, want false")
	}

	// --- and the counters must describe what actually reached the index ---
	for _, id := range wantMirrored {
		if !fake.upserted[id] {
			t.Errorf("expected %s to be mirrored into the ANN store", id)
		}
	}
	for _, id := range wantSkipped {
		if fake.upserted[id] {
			t.Errorf("%s should NOT have been mirrored into the ANN store", id)
		}
	}
	// The mirror-error rows must have been ATTEMPTED — "absent from upserted"
	// alone is satisfied by the fake's own failOn branch, so without this the
	// assertion above could not tell a failed write from a `continue` placed
	// before the mirror call.
	for _, id := range mirrorFailBooks {
		if !fake.attempted[id] {
			t.Errorf("%s should have been attempted (it passes every filter); the failure must come from the store, not an earlier skip", id)
		}
	}
	for _, id := range mirrorFailAuthors {
		if !fake.attempted[id] {
			t.Errorf("%s should have been attempted; the failure must come from the store, not an earlier skip", id)
		}
	}
	// ...and the rows that were filtered out must NOT have been attempted.
	for _, id := range []string{"book/ORPHAN_0", "book/NONPRIMARY_0", "book/STALE_0", "book/EMPTY_0"} {
		if fake.attempted[id] {
			t.Errorf("%s reached the ANN store; it should have been filtered out first", id)
		}
	}
}

// TestHydrateChromem_CancelledRunIsFlaggedIncomplete pins the one case where
// the BookRows == BooksAccounted() identity is documented NOT to hold. A run
// cut short by its context leaves rows unclassified, and the resulting nonzero
// unaccounted total must be readable as "we stopped early", not as an
// uncounted skip path. It also pins that the partial accounting is returned
// rather than discarded: before this, the cancellation path skipped the
// summary log entirely, so a 30-minute timeout over ~39K rows — the run an
// operator most wants to inspect — was the one with no bucket visibility.
func TestHydrateChromem_CancelledRunIsFlaggedIncomplete(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	engine.SetChromemStore(newFakeVectorANNStore())
	engine.embedClient = ai.NewEmbeddingClientWithOptions("k", "bge-m3", "")

	primary := true
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		return &database.Book{ID: id, Title: "Book", IsPrimaryVersion: &primary}, nil
	}
	for i := range 5 {
		if err := es.Upsert(database.Embedding{
			EntityType: "book",
			EntityID:   fmt.Sprintf("BOOK_%d", i),
			Vector:     []float32{1, 2, 3, 4},
			Model:      "bge-m3",
		}); err != nil {
			t.Fatalf("seed book %d: %v", i, err)
		}
	}

	// Cancelled before the first row, so the loop classifies nothing.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stats, err := engine.HydrateChromem(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("HydrateChromem err = %v, want context.Canceled", err)
	}
	if !stats.Incomplete {
		t.Error("Incomplete = false on a cancelled run, want true — a nonzero unaccounted total would otherwise read as an uncounted skip path")
	}
	if stats.BookRows != 5 {
		t.Errorf("BookRows = %d, want 5 (the rows were read before the loop bailed)", stats.BookRows)
	}
	if stats.BooksAccounted() != 0 {
		t.Errorf("BooksAccounted() = %d, want 0 — nothing was classified", stats.BooksAccounted())
	}
	// The identity is expected to BREAK here; that is the documented contract.
	if stats.BookRows == stats.BooksAccounted() {
		t.Error("BookRows == BooksAccounted() on a cancelled run; this test can no longer observe the incomplete case")
	}
}
