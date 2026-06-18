// file: internal/dedup/engine_primary_gate_test.go
// version: 1.0.0
// guid: 2f7b4c19-6d83-4e50-9a12-7c5e0a8b3d46
// last-edited: 2026-06-18

// Regression guard for DEDUP-CANDIDATE-EXPLOSION-2026-06-18: the exact-family
// emitters must never produce a candidate that involves a NON-primary version-group
// member. Non-primary members are already known duplicates of their group's primary,
// so pairing them (with siblings or primaries) re-discovers resolved duplicates and
// balloons the candidate set (observed: 387k exact candidates against ~49k final
// books). Every exact emitter routes through Engine.upsertExactCandidate, which gates
// on IsPrimaryVersion — these tests lock that invariant in place.
package dedup

import (
	"context"
	"testing"

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
	mock.GetBooksByAuthorIDFunc = func(authorID int) ([]database.Book, error) { return byAuthor, nil }
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
