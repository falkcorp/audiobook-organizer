// file: internal/playlist/evaluator_prop_test.go
// version: 1.3.0
// guid: bcc094f5-1645-44d3-be21-3087888fdaea

// Property-based tests for the smart-playlist evaluator.
//
// Performance note: most tests share ONE PebbleStore + BleveIndex for the
// entire rapid.Check run (created outside the lambda, closed via t.Cleanup).
// Books accumulate across iterations but the invariants still hold:
//   - LimitIsRespected: len(got) <= limit regardless of total book count.
//   - EmptyQueryErrors: error is returned before the store is touched.
//   - DeterministicEvaluation: both calls within one iteration see the same set.
//   - SortIsStable: same analysis.
//
// PerUserFilterIsolation is the exception — it asserts len(aliceGot)==n for
// THIS iteration's books only, which breaks when alice state from prior
// iterations lingers. That test uses per-iteration fixtures with rt.Cleanup.

package playlist

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/falkcorp/audiobook-organizer/internal/testutil/rapidgen"
	"pgregory.net/rapid"
)

// openPropFixture creates a PebbleStore + BleveIndex once, closed in t.Cleanup.
func openPropFixture(t *testing.T) (*database.PebbleStore, *search.BleveIndex) {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	idx, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatalf("bleve open: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	return store, idx
}

// seedBooks adds n random books under "common-author" and returns their IDs.
// Uses batch indexing so all n documents hit Bleve in a single commit.
// Safe to call repeatedly on the same store+index.
func seedBooks(rt *rapid.T, store *database.PebbleStore, idx *search.BleveIndex, n int) []string {
	ids := make([]string, 0, n)
	docs := make([]search.BookDocument, 0, n)
	for range n {
		b := rapidgen.Book(rt)
		created, err := store.CreateBook(b)
		if err != nil {
			rt.Fatalf("create book: %v", err)
		}
		yr := 0
		if b.PrintYear != nil {
			yr = *b.PrintYear
		}
		docs = append(docs, search.BookDocument{
			BookID: created.ID,
			Title:  b.Title,
			Author: "common-author",
			Format: b.Format,
			Year:   yr,
		})
		ids = append(ids, created.ID)
	}
	if err := idx.IndexBookBatch(docs); err != nil {
		rt.Fatalf("batch index books: %v", err)
	}
	return ids
}

// TestProp_LimitIsRespected verifies that
// EvaluateSmartPlaylist(limit=N) returns at most N IDs for any query
// that produces a non-trivial candidate set.
func TestProp_LimitIsRespected(t *testing.T) {
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	store, idx := openPropFixture(t)
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 12).Draw(rt, "n_books")
		limit := rapid.IntRange(1, 20).Draw(rt, "limit")

		seedBooks(rt, store, idx, n)

		got, err := EvaluateSmartPlaylist(store, idx, "author:common-author", "", limit, "_local")
		if err != nil {
			rt.Fatalf("evaluate: %v", err)
		}
		if len(got) > limit {
			rt.Fatalf("limit=%d violated: got %d ids", limit, len(got))
		}
	})
}

// TestProp_EmptyQueryErrors verifies that empty / whitespace-only
// queries always return a non-nil error and a nil result slice.
func TestProp_EmptyQueryErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	// EvaluateSmartPlaylist checks for an empty query before touching
	// the store or index — only a non-nil idx is required.
	store, idx := openPropFixture(t)

	rapid.Check(t, func(rt *rapid.T) {
		ws := rapid.StringMatching(`[ \t\n\r]{0,16}`).Draw(rt, "whitespace")

		got, err := EvaluateSmartPlaylist(store, idx, ws, "", 0, "_local")
		if err == nil {
			rt.Fatalf("empty query %q should error, got %v", ws, got)
		}
		if got != nil {
			rt.Fatalf("empty query should return nil ids, got %v", got)
		}
	})
}

// TestProp_DeterministicEvaluation verifies that evaluating the same
// query against the same seeded store+index twice yields the same
// result IDs in the same order.
func TestProp_DeterministicEvaluation(t *testing.T) {
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	store, idx := openPropFixture(t)
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 10).Draw(rt, "n_books")
		limit := rapid.IntRange(0, 20).Draw(rt, "limit")

		seedBooks(rt, store, idx, n)

		first, err := EvaluateSmartPlaylist(store, idx, "author:common-author", "", limit, "_local")
		if err != nil {
			rt.Fatalf("first evaluate: %v", err)
		}
		second, err := EvaluateSmartPlaylist(store, idx, "author:common-author", "", limit, "_local")
		if err != nil {
			rt.Fatalf("second evaluate: %v", err)
		}
		if len(first) != len(second) {
			rt.Fatalf("non-deterministic lengths: %d vs %d", len(first), len(second))
		}
		for i := range first {
			if first[i] != second[i] {
				rt.Fatalf("non-deterministic at %d: %q vs %q (first=%v second=%v)",
					i, first[i], second[i], first, second)
			}
		}
	})
}

// TestProp_SortIsStable verifies that evaluating twice with the same
// sortJSON produces byte-identical order. sortBookIDs uses
// sort.SliceStable with a deterministic comparator, so the second
// call must reproduce the first call exactly.
func TestProp_SortIsStable(t *testing.T) {
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	store, idx := openPropFixture(t)
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(2, 10).Draw(rt, "n_books")
		field := rapid.SampledFrom([]string{"title", "year", "date_added"}).Draw(rt, "sort_field")
		direction := rapid.SampledFrom([]string{"asc", "desc"}).Draw(rt, "sort_dir")
		sortJSON := `[{"field":"` + field + `","direction":"` + direction + `"}]`

		seedBooks(rt, store, idx, n)

		first, err := EvaluateSmartPlaylist(store, idx, "author:common-author", sortJSON, 0, "_local")
		if err != nil {
			rt.Fatalf("first evaluate: %v", err)
		}
		second, err := EvaluateSmartPlaylist(store, idx, "author:common-author", sortJSON, 0, "_local")
		if err != nil {
			rt.Fatalf("second evaluate: %v", err)
		}
		if len(first) != len(second) {
			rt.Fatalf("sort produced different lengths: %d vs %d", len(first), len(second))
		}
		for i := range first {
			if first[i] != second[i] {
				rt.Fatalf("sort not stable at idx %d: %q vs %q", i, first[i], second[i])
			}
		}
	})
}

// TestProp_PerUserFilterIsolation verifies that per-user state
// written for user A never leaks into user B's evaluation. Filtering
// B on `read_status:finished` must return zero books when only A has
// any finished rows.
//
// User IDs are scoped to each iteration using the first created book's
// ULID as a suffix, so accumulated state from prior iterations never
// contaminates the current check. This allows sharing one store+index.
func TestProp_PerUserFilterIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	store, idx := openPropFixture(t)
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 8).Draw(rt, "n_books")
		bookIDs := seedBooks(rt, store, idx, n)

		// Unique per-iteration user IDs prevent cross-iteration state leakage.
		aliceID := "alice-" + bookIDs[0]
		bobID := "bob-" + bookIDs[0]

		now := time.Now()
		for _, id := range bookIDs {
			if err := store.SetUserBookState(&database.UserBookState{
				UserID:         aliceID,
				BookID:         id,
				Status:         database.UserBookStatusFinished,
				StatusManual:   true,
				LastActivityAt: now,
			}); err != nil {
				rt.Fatalf("seed alice state for %s: %v", id, err)
			}
		}

		aliceGot, err := EvaluateSmartPlaylist(
			store, idx, "author:common-author read_status:finished", "", 0, aliceID)
		if err != nil {
			rt.Fatalf("evaluate alice: %v", err)
		}
		if len(aliceGot) != n {
			rt.Fatalf("alice should see %d finished books, got %d: %v",
				n, len(aliceGot), aliceGot)
		}

		bobGot, err := EvaluateSmartPlaylist(
			store, idx, "author:common-author read_status:finished", "", 0, bobID)
		if err != nil {
			rt.Fatalf("evaluate bob: %v", err)
		}
		if len(bobGot) != 0 {
			rt.Fatalf("bob should see 0 finished books (no state), got %d: %v",
				len(bobGot), bobGot)
		}
	})
}
