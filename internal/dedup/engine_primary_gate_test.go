// file: internal/dedup/engine_primary_gate_test.go
// version: 1.2.0
// guid: 2f7b4c19-6d83-4e50-9a12-7c5e0a8b3d46
// last-edited: 2026-07-05

// Regression guard for DEDUP-CANDIDATE-EXPLOSION-2026-06-18: the exact-family
// emitters must never produce a candidate that involves a NON-primary version-group
// member. Non-primary members are already known duplicates of their group's primary,
// so pairing them (with siblings or primaries) re-discovers resolved duplicates and
// balloons the candidate set (observed: 387k exact candidates against ~49k final
// books). Every exact emitter routes through Engine.upsertExactCandidate, which gates
// on IsPrimaryVersion — these tests lock that invariant in place.
//
// Also covers DEDUP-NONPRIMARY-EMBED-KEEP-2026-07: non-primary books must still
// get a real embedding computed/cached (a calibration/QA datapoint), they just
// must never seed a new dedup candidate via findSimilarBooks.
package dedup

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// wireExactTitleOnly configures the mock so only the exact-title emitter can fire
// (no file hash, ISBN, metadata-source-hash, or full-scan path) — isolating the
// primary gate. books are returned by author for checkExactTitle.
func wireExactTitleOnly(mock *database.MockStore, books map[string]*database.Book, byAuthor []database.Book) {
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) { return books[id], nil }
	mock.GetAuthorByIDFunc = func(id int) (*database.Author, error) {
		return &database.Author{ID: 1, Name: "Test Author"}, nil
	}
	mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) { return nil, nil }
	mock.GetBookByFileHashFunc = func(hash string) (*database.Book, error) { return nil, nil }
	mock.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) { return nil, nil }
	mock.GetBooksByAuthorIDCoreFunc = func(authorID int) ([]database.BookCore, error) {
		out := make([]database.BookCore, len(byAuthor))
		for i := range byAuthor {
			out[i] = byAuthor[i].Core()
		}
		return out, nil
	}
	mock.GetBooksByMetadataSourceHashFunc = func(h string) ([]database.Book, error) { return nil, nil }
}

func primaryBook(id, title string) *database.Book {
	authorID, dur, primary := 1, 3600, true
	return &database.Book{ID: id, Title: title, AuthorID: &authorID, Duration: &dur, IsPrimaryVersion: &primary}
}

func nonPrimaryBook(id, title string) *database.Book {
	b := primaryBook(id, title)
	np := false
	b.IsPrimaryVersion = &np
	return b
}

func pendingCandidates(t *testing.T, es *database.EmbeddingStore) []database.DedupCandidate {
	t.Helper()
	cands, _, err := es.ListCandidates(database.CandidateFilter{EntityType: "book", Status: "pending"})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	return cands
}

// TestExactEmitters_SkipNonPrimaryVersions: A(primary), B(non-primary), C(primary),
// all same title/author. Running CheckBook over each must pair the two PRIMARIES
// (A↔C) but never involve the non-primary B.
func TestExactEmitters_SkipNonPrimaryVersions(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	engine.AutoMergeEnabled = false

	a := primaryBook("BOOK_A", "Moby Dick")
	b := nonPrimaryBook("BOOK_B", "Moby Dick")
	c := primaryBook("BOOK_C", "Moby Dick")
	byAuthor := []database.Book{*a, *b, *c}
	wireExactTitleOnly(mock, map[string]*database.Book{"BOOK_A": a, "BOOK_B": b, "BOOK_C": c}, byAuthor)

	for _, id := range []string{"BOOK_A", "BOOK_B", "BOOK_C"} {
		if _, err := engine.CheckBook(context.Background(), id); err != nil {
			t.Fatalf("CheckBook(%s): %v", id, err)
		}
	}

	cands := pendingCandidates(t, es)
	involvesNonPrimary := false
	primaryPairFound := false
	for _, c := range cands {
		if c.EntityAID == "BOOK_B" || c.EntityBID == "BOOK_B" {
			involvesNonPrimary = true
		}
		if (c.EntityAID == "BOOK_A" && c.EntityBID == "BOOK_C") || (c.EntityAID == "BOOK_C" && c.EntityBID == "BOOK_A") {
			primaryPairFound = true
		}
	}
	if involvesNonPrimary {
		t.Errorf("non-primary BOOK_B leaked into a candidate; candidates=%+v", cands)
	}
	if !primaryPairFound {
		t.Errorf("expected a primary↔primary candidate (BOOK_A↔BOOK_C); candidates=%+v", cands)
	}
}

// TestExactEmitters_NoBalloonFromVersionCopies is the anti-balloon property: one
// final book with N extra non-primary COPIES (1 primary + N non-primary, all same
// title) must produce ZERO candidates — there is no second final book to pair with.
// Pre-fix this produced O(N^2) within-group candidates.
func TestExactEmitters_NoBalloonFromVersionCopies(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	engine.AutoMergeEnabled = false

	books := map[string]*database.Book{"PRIMARY": primaryBook("PRIMARY", "Dune")}
	byAuthor := []database.Book{*books["PRIMARY"]}
	ids := []string{"PRIMARY"}
	for _, id := range []string{"COPY_1", "COPY_2", "COPY_3", "COPY_4"} {
		nb := nonPrimaryBook(id, "Dune")
		books[id] = nb
		byAuthor = append(byAuthor, *nb)
		ids = append(ids, id)
	}
	wireExactTitleOnly(mock, books, byAuthor)

	for _, id := range ids {
		if _, err := engine.CheckBook(context.Background(), id); err != nil {
			t.Fatalf("CheckBook(%s): %v", id, err)
		}
	}

	if cands := pendingCandidates(t, es); len(cands) != 0 {
		t.Errorf("expected 0 candidates for one final book + copies, got %d: %+v", len(cands), cands)
	}
}

// TestUpsertExactCandidate_GateUnit directly exercises the central gate so a future
// emitter that routes through it is covered regardless of call path.
func TestUpsertExactCandidate_GateUnit(t *testing.T) {
	engine, _, es := setupTestEngine(t)
	p1 := primaryBook("P1", "T")
	p2 := primaryBook("P2", "T")
	np := nonPrimaryBook("NP", "T")

	if err := engine.upsertExactCandidate(p1, np, "exact", 1.0); err != nil {
		t.Fatalf("upsert p1↔np: %v", err)
	}
	if err := engine.upsertExactCandidate(np, p1, "exact", 1.0); err != nil {
		t.Fatalf("upsert np↔p1: %v", err)
	}
	if err := engine.upsertExactCandidate(p1, p2, "exact", 1.0); err != nil {
		t.Fatalf("upsert p1↔p2: %v", err)
	}

	cands := pendingCandidates(t, es)
	if len(cands) != 1 {
		t.Fatalf("expected exactly 1 candidate (primary↔primary), got %d: %+v", len(cands), cands)
	}
	if cands[0].EntityAID == "NP" || cands[0].EntityBID == "NP" {
		t.Errorf("non-primary leaked: %+v", cands[0])
	}
}

// bareBook returns a minimal Book with no AuthorID/FileHash/ISBN/
// MetadataSourceHash/Duration set, so every Layer-1 exact emitter in
// CheckBook short-circuits to a no-op without needing extra mock wiring —
// isolating the Layer-2 embedding/findSimilarBooks behavior under test.
func bareBook(id, title string, primary *bool) *database.Book {
	return &database.Book{ID: id, Title: title, IsPrimaryVersion: primary}
}

// wireMinimalMock wires just enough of MockStore for CheckBook's Layer-1
// no-ops (via bareBook) plus GetBookByID lookups from the given map.
func wireMinimalMock(mock *database.MockStore, books map[string]*database.Book) {
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) { return books[id], nil }
	mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) { return nil, nil }
	mock.GetBookByFileHashFunc = func(hash string) (*database.Book, error) { return nil, nil }
}

// TestEmbedBook_NonPrimary_KeepsEmbeddingInsteadOfDeleting is the core
// regression guard for DEDUP-NONPRIMARY-EMBED-KEEP-2026-07: prepBookEmbed
// used to delete any embedding row for a non-primary book and skip
// generating a new one. Now embeddings are free (local Ollama) and a
// non-primary book's embedding is a useful calibration/QA datapoint, so it
// must be computed and stored exactly like any other book.
func TestEmbedBook_NonPrimary_KeepsEmbeddingInsteadOfDeleting(t *testing.T) {
	engine, mock, es := setupTestEngine(t)

	primary := false
	book := bareBook("NP_BOOK", "A Non-Primary Version", &primary)
	wireMinimalMock(mock, map[string]*database.Book{"NP_BOOK": book})

	client := ai.NewEmbeddingClientWithOptions("test-key", "test-model", "")
	client.SetRawEmbedForTest(func(ctx context.Context, texts []string) ([][]float32, error) {
		out := make([][]float32, len(texts))
		for i := range out {
			out[i] = []float32{1, 0, 0, 0}
		}
		return out, nil
	})
	engine.embedClient = client

	status, err := engine.EmbedBook(context.Background(), "NP_BOOK")
	if err != nil {
		t.Fatalf("EmbedBook: %v", err)
	}
	if status != EmbedStatusEmbedded {
		t.Fatalf("status = %v, want EmbedStatusEmbedded (non-primary books must be embedded, not skipped)", status)
	}

	stored, err := es.Get("book", "NP_BOOK")
	if err != nil {
		t.Fatalf("Get embedding: %v", err)
	}
	if stored == nil {
		t.Fatal("expected a stored embedding for the non-primary book, got nil (still being deleted?)")
	}
}

// TestEmbedBook_NonPrimary_PreExistingEmbeddingSurvives verifies that a
// non-primary book's PRE-EXISTING embedding row is no longer deleted by
// EmbedBook — it must be refreshed (or left as-is on a cache hit) like any
// other book's row.
func TestEmbedBook_NonPrimary_PreExistingEmbeddingSurvives(t *testing.T) {
	engine, mock, es := setupTestEngine(t)

	primary := false
	book := bareBook("NP_BOOK2", "Another Non-Primary Version", &primary)
	wireMinimalMock(mock, map[string]*database.Book{"NP_BOOK2": book})

	// Seed a pre-existing row with a stale hash so EmbedBook must recompute
	// (not hit the cache path) — the pre-fix code would have deleted this
	// row outright before ever reaching the hash check.
	if err := es.Upsert(database.Embedding{
		EntityType: "book",
		EntityID:   "NP_BOOK2",
		TextHash:   "stale-hash-from-before-a-title-edit",
		Vector:     []float32{0.5, 0.5, 0, 0},
		Model:      "test-model",
	}); err != nil {
		t.Fatalf("seed pre-existing embedding: %v", err)
	}

	client := ai.NewEmbeddingClientWithOptions("test-key", "test-model", "")
	client.SetRawEmbedForTest(func(ctx context.Context, texts []string) ([][]float32, error) {
		out := make([][]float32, len(texts))
		for i := range out {
			out[i] = []float32{1, 0, 0, 0}
		}
		return out, nil
	})
	engine.embedClient = client

	status, err := engine.EmbedBook(context.Background(), "NP_BOOK2")
	if err != nil {
		t.Fatalf("EmbedBook: %v", err)
	}
	if status != EmbedStatusEmbedded {
		t.Fatalf("status = %v, want EmbedStatusEmbedded (stale hash must trigger a refresh)", status)
	}

	stored, err := es.Get("book", "NP_BOOK2")
	if err != nil {
		t.Fatalf("Get embedding: %v", err)
	}
	if stored == nil {
		t.Fatal("pre-existing embedding row was deleted; non-primary rows must survive")
	}
}

// TestCheckBook_NonPrimary_SkipsFindSimilarBooks_ButPrimaryStillMatches is the
// call-site regression guard for DEDUP-NONPRIMARY-EMBED-KEEP-2026-07: CheckBook
// must still embed a non-primary book (calibration datapoint) but must NOT call
// findSimilarBooks for it, while a primary book in the same conditions still
// gets a Layer-2 embedding candidate.
func TestCheckBook_NonPrimary_SkipsFindSimilarBooks_ButPrimaryStillMatches(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	engine.AutoMergeEnabled = false

	primaryFlag := true
	nonPrimaryFlag := false
	other := bareBook("OTHER", "Unrelated Title", &primaryFlag)
	npBook := bareBook("NP_TARGET", "Some Different Title", &nonPrimaryFlag)
	pBook := bareBook("P_TARGET", "Yet Another Title", &primaryFlag)

	wireMinimalMock(mock, map[string]*database.Book{
		"OTHER":     other,
		"NP_TARGET": npBook,
		"P_TARGET":  pBook,
	})

	// Pre-seed "OTHER"'s embedding so any book whose own vector is identical
	// will land above BookLowThreshold (0.85) via cosine similarity 1.0 and
	// findSimilarBooks (if called) would emit a "embedding"-layer candidate.
	if err := es.Upsert(database.Embedding{
		EntityType: "book",
		EntityID:   "OTHER",
		TextHash:   "other-hash",
		Vector:     []float32{1, 0, 0, 0},
		Model:      "test-model",
	}); err != nil {
		t.Fatalf("seed OTHER embedding: %v", err)
	}

	client := ai.NewEmbeddingClientWithOptions("test-key", "test-model", "")
	client.SetRawEmbedForTest(func(ctx context.Context, texts []string) ([][]float32, error) {
		out := make([][]float32, len(texts))
		for i := range out {
			out[i] = []float32{1, 0, 0, 0}
		}
		return out, nil
	})
	engine.embedClient = client

	if _, err := engine.CheckBook(context.Background(), "NP_TARGET"); err != nil {
		t.Fatalf("CheckBook(NP_TARGET): %v", err)
	}

	// The non-primary book must still have been embedded...
	if stored, err := es.Get("book", "NP_TARGET"); err != nil || stored == nil {
		t.Fatalf("expected NP_TARGET to have a stored embedding, err=%v stored=%v", err, stored)
	}
	// ...but findSimilarBooks must not have run for it.
	npCands, _, err := es.ListCandidates(database.CandidateFilter{EntityType: "book", Layer: "embedding"})
	if err != nil {
		t.Fatalf("ListCandidates after NP_TARGET: %v", err)
	}
	for _, c := range npCands {
		if c.EntityAID == "NP_TARGET" || c.EntityBID == "NP_TARGET" {
			t.Fatalf("findSimilarBooks ran for non-primary NP_TARGET; candidate leaked: %+v", c)
		}
	}

	// Positive control: the same setup for a PRIMARY book must produce an
	// embedding-layer candidate against OTHER, proving findSimilarBooks
	// would have fired here if not gated.
	if _, err := engine.CheckBook(context.Background(), "P_TARGET"); err != nil {
		t.Fatalf("CheckBook(P_TARGET): %v", err)
	}
	pCands, _, err := es.ListCandidates(database.CandidateFilter{EntityType: "book", Layer: "embedding"})
	if err != nil {
		t.Fatalf("ListCandidates after P_TARGET: %v", err)
	}
	found := false
	for _, c := range pCands {
		if (c.EntityAID == "P_TARGET" && c.EntityBID == "OTHER") || (c.EntityAID == "OTHER" && c.EntityBID == "P_TARGET") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an embedding-layer candidate between P_TARGET and OTHER, got: %+v", pCands)
	}
}

// TestFullScan_NonPrimary_EmbedsButSkipsFindSimilarBooks is the FullScan
// flushChunk regression guard: non-primary books flow through getAllBooksUnfiltered
// and get embedded, but flushChunk's chunkIsPrimary gate must keep them out of
// findSimilarBooks while a primary book in the same chunk still matches.
func TestFullScan_NonPrimary_EmbedsButSkipsFindSimilarBooks(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	engine.AutoMergeEnabled = false

	primaryFlag := true
	nonPrimaryFlag := false
	other := bareBook("FS_OTHER", "Unrelated FullScan Title", &primaryFlag)
	npBook := bareBook("FS_NP", "Some Different FullScan Title", &nonPrimaryFlag)
	pBook := bareBook("FS_P", "Yet Another FullScan Title", &primaryFlag)

	books := []database.Book{*other, *npBook, *pBook}
	byID := map[string]*database.Book{"FS_OTHER": other, "FS_NP": npBook, "FS_P": pBook}

	mock.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) { return books, nil }
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) { return byID[id], nil }
	mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) { return nil, nil }
	mock.GetBookByFileHashFunc = func(hash string) (*database.Book, error) { return nil, nil }

	if err := es.Upsert(database.Embedding{
		EntityType: "book",
		EntityID:   "FS_OTHER",
		TextHash:   "fs-other-hash",
		Vector:     []float32{1, 0, 0, 0},
		Model:      "test-model",
	}); err != nil {
		t.Fatalf("seed FS_OTHER embedding: %v", err)
	}

	client := ai.NewEmbeddingClientWithOptions("test-key", "test-model", "")
	client.SetRawEmbedForTest(func(ctx context.Context, texts []string) ([][]float32, error) {
		out := make([][]float32, len(texts))
		for i := range out {
			out[i] = []float32{1, 0, 0, 0}
		}
		return out, nil
	})
	engine.embedClient = client

	if err := engine.FullScan(context.Background(), nil); err != nil {
		t.Fatalf("FullScan: %v", err)
	}

	if stored, err := es.Get("book", "FS_NP"); err != nil || stored == nil {
		t.Fatalf("expected FS_NP to have a stored embedding after FullScan, err=%v stored=%v", err, stored)
	}

	cands, _, err := es.ListCandidates(database.CandidateFilter{EntityType: "book", Layer: "embedding"})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	foundPrimaryPair := false
	for _, c := range cands {
		if c.EntityAID == "FS_NP" || c.EntityBID == "FS_NP" {
			t.Fatalf("findSimilarBooks ran for non-primary FS_NP in FullScan; candidate leaked: %+v", c)
		}
		if (c.EntityAID == "FS_P" && c.EntityBID == "FS_OTHER") || (c.EntityAID == "FS_OTHER" && c.EntityBID == "FS_P") {
			foundPrimaryPair = true
		}
	}
	if !foundPrimaryPair {
		t.Fatalf("expected an embedding-layer candidate between FS_P and FS_OTHER, got: %+v", cands)
	}
}
