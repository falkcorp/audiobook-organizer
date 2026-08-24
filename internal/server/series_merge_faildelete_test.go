// file: internal/server/series_merge_faildelete_test.go
// version: 1.0.0
// guid: 4c9e17ab-52d3-4f80-b6a1-9e35b7c0284f
// last-edited: 2026-08-24

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// These pin the FAIL-CLOSED ordering on the two series-merge paths in
// duplicates_helpers.go: the series row must survive if any of its books could
// not be repointed first.
//
// Reading the complete-set getter (the rest of this PR) fixes WHICH books the
// merge tries to move. It does nothing about what happens when moving one
// FAILS. Both loops recorded the failure and deleted the series anyway, which
// strands the row that failed -- the same end state as the bug being fixed,
// but reached through the error path instead of the getter, and reported to
// the operator as a success.
//
// The sibling tests in series_merge_strand_test.go cannot catch this: their
// stores always succeed and never return a missing book, so every one of them
// stays green against the un-gated delete.

// TestExecuteSeriesPrune_DoesNotDeleteAfterAFailedRepoint covers a write that
// errors. PRIMARY is ordered first and succeeds, so this is a genuine PARTIAL
// repoint rather than a total failure -- the case that strands rows.
func TestExecuteSeriesPrune_DoesNotDeleteAfterAFailedRepoint(t *testing.T) {
	const (
		keepID  = 1
		mergeID = 2
		primary = "prune-primary"
		altRip  = "prune-alternate-rip"
	)

	s := newSeriesPruneServer(t)
	mock := seriesPruneMergeFixture(keepID, mergeID, primary, altRip)

	var repointed []string
	mock.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		if id == altRip {
			return nil, errors.New("simulated write failure")
		}
		if b.SeriesID != nil {
			repointed = append(repointed, id)
		}
		return b, nil
	}

	var deleted []int
	mock.DeleteSeriesFunc = func(id int) error {
		deleted = append(deleted, id)
		return nil
	}

	store := seriesRefCountingStore{MockStore: mock, refCounts: map[int]int{keepID: 2, mergeID: 2}}
	if err := s.executeSeriesPrune(context.Background(), store, seriesPruneNoopProgress{}, ""); err != nil {
		t.Fatalf("executeSeriesPrune must absorb a per-book write failure, not abort: %v", err)
	}

	// Guard against a vacuous pass: if the merge never ran at all, the absence
	// of a delete below would prove nothing.
	if len(repointed) == 0 {
		t.Fatal("no book was repointed -- phase 1 never merged, so this test is vacuous")
	}

	for _, id := range deleted {
		if id == mergeID {
			t.Fatalf("series %d was deleted after repointing %s FAILED -- that row still "+
				"references it and is now stranded, and the operation reports success. "+
				"The delete must happen only after every repoint has succeeded.", mergeID, altRip)
		}
	}
}

// TestExecuteSeriesPrune_DoesNotDeleteWhenABookDoesNotResolve covers the silent
// branch: GetBookByID returning (nil, nil).
//
// That is reachable, not theoretical -- the Pebble store returns (nil, nil) on
// ErrNotFound, and the membership getter can serve a row from the memdb that a
// subsequent point-get cannot hydrate. The old code neither recorded it nor
// skipped the delete, so it was the one failure mode that left no trace at all.
func TestExecuteSeriesPrune_DoesNotDeleteWhenABookDoesNotResolve(t *testing.T) {
	const (
		keepID  = 1
		mergeID = 2
		primary = "prune-primary"
		altRip  = "prune-unhydratable"
	)

	s := newSeriesPruneServer(t)
	mock := seriesPruneMergeFixture(keepID, mergeID, primary, altRip)

	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if id == altRip {
			return nil, nil // found by the getter, gone by the point-get
		}
		sid := mergeID
		return &database.Book{ID: id, SeriesID: &sid}, nil
	}

	var repointed []string
	mock.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		if b.SeriesID != nil {
			repointed = append(repointed, id)
		}
		return b, nil
	}

	var deleted []int
	mock.DeleteSeriesFunc = func(id int) error {
		deleted = append(deleted, id)
		return nil
	}

	store := seriesRefCountingStore{MockStore: mock, refCounts: map[int]int{keepID: 2, mergeID: 2}}
	if err := s.executeSeriesPrune(context.Background(), store, seriesPruneNoopProgress{}, ""); err != nil {
		t.Fatalf("executeSeriesPrune: %v", err)
	}

	if len(repointed) == 0 {
		t.Fatal("no book was repointed -- phase 1 never merged, so this test is vacuous")
	}

	for _, id := range deleted {
		if id == mergeID {
			t.Fatalf("series %d was deleted even though %s could not be hydrated and was "+
				"never repointed. A book the getter listed but the point-get cannot "+
				"resolve must block the delete, not be skipped silently.", mergeID, altRip)
		}
	}
}

// TestMergeSeriesGroupHelper_DoesNotDeleteWhenABookDoesNotResolve is the same
// hole in the other merge path.
//
// mergeSeriesGroupHelper already returns early on a GetBookByID error and on an
// UpdateBook error, so those two branches are correct. The (nil, nil) branch was
// the single way through to an unconditional DeleteSeries.
func TestMergeSeriesGroupHelper_DoesNotDeleteWhenABookDoesNotResolve(t *testing.T) {
	const (
		keepID  = 1
		mergeID = 2
		primary = "book-primary"
		altRip  = "book-unhydratable"
	)

	store := &database.MockStore{}
	store.GetBooksBySeriesIDCoreFunc = func(id int) ([]database.BookCore, error) {
		if id == mergeID {
			return []database.BookCore{{ID: primary}}, nil
		}
		return nil, nil
	}
	store.GetBooksBySeriesIDAllVersionsFunc = func(id int) ([]database.BookCore, error) {
		if id == mergeID {
			return []database.BookCore{{ID: primary}, {ID: altRip}}, nil
		}
		return nil, nil
	}
	store.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if id == altRip {
			return nil, nil
		}
		sid := mergeID
		return &database.Book{ID: id, SeriesID: &sid}, nil
	}
	store.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) { return b, nil }

	var deleted []int
	store.DeleteSeriesFunc = func(id int) error {
		deleted = append(deleted, id)
		return nil
	}

	err := mergeSeriesGroupHelper(store, keepID, []int{mergeID})
	if err == nil {
		t.Fatal("a book that cannot be hydrated must fail the merge, not be skipped -- " +
			"got nil error, so the caller records this merge as successful")
	}

	for _, id := range deleted {
		if id == mergeID {
			t.Fatalf("series %d deleted even though %s was never repointed: %v", mergeID, altRip, err)
		}
	}
}

// seriesPruneMergeFixture builds the two-series setup shared by the prune tests
// above: same normalized name so phase 1 groups them, equal Core counts so the
// lower ID wins the canonical vote, and getters that disagree by exactly one
// alternate rip on the series being merged away.
func seriesPruneMergeFixture(keepID, mergeID int, primary, altRip string) *database.MockStore {
	mock := &database.MockStore{}
	mock.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{
			{ID: keepID, Name: "Discworld"},
			{ID: mergeID, Name: "  discworld "},
		}, nil
	}
	mock.GetBooksBySeriesIDCoreFunc = func(id int) ([]database.BookCore, error) {
		switch id {
		case keepID:
			return []database.BookCore{{ID: "keeper-book"}}, nil
		case mergeID:
			return []database.BookCore{{ID: primary}}, nil
		}
		return nil, nil
	}
	mock.GetBooksBySeriesIDAllVersionsFunc = func(id int) ([]database.BookCore, error) {
		switch id {
		case keepID:
			return []database.BookCore{{ID: "keeper-book"}}, nil
		case mergeID:
			return []database.BookCore{{ID: primary}, {ID: altRip}}, nil
		}
		return nil, nil
	}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		sid := mergeID
		return &database.Book{ID: id, SeriesID: &sid}, nil
	}
	return mock
}
