// file: internal/server/series_merge_faildelete_test.go
// version: 1.1.0
// guid: 4c9e17ab-52d3-4f80-b6a1-9e35b7c0284f
// last-edited: 2026-08-24

package server

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
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
	mock, assignments := seriesPruneMergeFixture(keepID, mergeID, primary, altRip)

	var repointed []string
	mock.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		if id == altRip {
			return nil, errors.New("simulated write failure")
		}
		if b.SeriesID != nil {
			// Write through to the live state so the membership getters reflect it,
			// exactly as a real store would.
			assignments[id] = *b.SeriesID
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
	// Two things at once, and they are not the same thing.
	//
	// The run must NOT abort on a per-book write failure — it keeps going and the
	// assertions below prove it processed the whole group. But it must REPORT the
	// failure, because the refusal it just made ends "Re-run after resolving the
	// errors above" and until 2026-08-24 the function returned nil regardless, so
	// the caller marked the operation "success" and emitted "Series prune
	// completed". An instruction to re-run, delivered on a run that reported
	// itself green, reaches nobody.
	err := s.executeSeriesPrune(context.Background(), store, seriesPruneNoopProgress{}, "")
	if err == nil {
		t.Fatal("executeSeriesPrune returned nil after REFUSING a delete; the caller marks that success " +
			"and nobody learns the merge needs re-running")
	}
	if !strings.Contains(err.Error(), "REFUSING to delete") {
		t.Errorf("the error must name the refusal so the operator knows what to fix, got: %v", err)
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
	mock, assignments := seriesPruneMergeFixture(keepID, mergeID, primary, altRip)

	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if id == altRip {
			return nil, nil // found by the getter, gone by the point-get
		}
		sid, ok := assignments[id]
		if !ok {
			return nil, nil
		}
		s := sid
		return &database.Book{ID: id, SeriesID: &s}, nil
	}

	var repointed []string
	mock.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		if b.SeriesID != nil {
			assignments[id] = *b.SeriesID
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
	// As above: the unresolvable book must not abort the run, but it must make the
	// run report failure rather than a silent "success".
	err := s.executeSeriesPrune(context.Background(), store, seriesPruneNoopProgress{}, "")
	if err == nil {
		t.Fatal("executeSeriesPrune returned nil after refusing a delete over an unresolvable book")
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
//
// BOTH membership getters derive from the returned `assignments` map, which
// UpdateBook mutates. This is not incidental — it is what makes the fixture able
// to fail.
//
// The getters used to return STATIC slices, so `GetBooksBySeriesIDCore(mergeID)`
// kept answering `[primary]` forever, even after `primary` had been repointed
// away. That kept one specific mutant alive: swapping phase 2's unfiltered
// `refCounts[ser.ID]` back to the filtered `len(GetBooksBySeriesIDCore(ser.ID))`
// — precisely the bug that produced 6,893 phantom series IDs in production —
// left the whole suite green, because the static getter reported 1 remaining
// book for a series that in reality had none Core could see. A real store would
// have answered 0, phase 2 would have deleted the series, and the alternate rip
// would have been stranded.
//
// A fixture that cannot reach a code path cannot host a mutant on it, and a
// mutation score only ever covers the mutants the fixture makes reachable.
func seriesPruneMergeFixture(keepID, mergeID int, primary, altRip string) (*database.MockStore, map[string]int) {
	// Live book→series state. The whole point is that reads reflect writes.
	assignments := map[string]int{
		"keeper-book": keepID,
		primary:       mergeID,
		altRip:        mergeID,
	}
	// altRip is a non-primary version: the Core (listing) getter hides it, the
	// AllVersions (complete-set) getter does not. That single disagreement is the
	// subject of these tests.
	nonPrimary := map[string]bool{altRip: true}

	booksIn := func(seriesID int, includeNonPrimary bool) []database.BookCore {
		ids := make([]string, 0, len(assignments))
		for id, sid := range assignments {
			if sid != seriesID || (!includeNonPrimary && nonPrimary[id]) {
				continue
			}
			ids = append(ids, id)
		}
		sort.Strings(ids) // map iteration order is random; keep results stable
		out := make([]database.BookCore, 0, len(ids))
		for _, id := range ids {
			out = append(out, database.BookCore{ID: id})
		}
		return out
	}

	mock := &database.MockStore{}
	mock.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{
			{ID: keepID, Name: "Discworld"},
			{ID: mergeID, Name: "  discworld "},
		}, nil
	}
	mock.GetBooksBySeriesIDCoreFunc = func(id int) ([]database.BookCore, error) {
		return booksIn(id, false), nil
	}
	mock.GetBooksBySeriesIDAllVersionsFunc = func(id int) ([]database.BookCore, error) {
		return booksIn(id, true), nil
	}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		sid, ok := assignments[id]
		if !ok {
			return nil, nil
		}
		s := sid
		return &database.Book{ID: id, SeriesID: &s}, nil
	}
	mock.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		if b.SeriesID != nil {
			assignments[id] = *b.SeriesID
		}
		return b, nil
	}
	return mock, assignments
}

// TestExecuteSeriesPrune_AFailedCountDisqualifiesTheGroup pins the canonical
// vote.
//
// The vote picks which of several duplicate series survives; every other series
// in the group is merged away and DELETED. A series whose book count failed to
// load used to count as zero books, so it lost — and losing means deletion.
//
// The fixture is the damaging shape: series 1 holds 400 books, series 2 is a
// two-book typo of it, and the count for series 1 errors. Under the old
// behaviour series 2 won on bc=2 > 0, the 400 books were repointed into the typo,
// and series 1 — the real one, with every external reference to it — was deleted,
// with the run reporting success.
//
// Skipping the group costs a deferred merge and is retryable. Deleting the wrong
// survivor is not recoverable from the run summary.
func TestExecuteSeriesPrune_AFailedCountDisqualifiesTheGroup(t *testing.T) {
	const (
		realID = 1 // the 400-book series whose count fails
		typoID = 2 // the 2-book near-duplicate that would win by default
	)

	s := newSeriesPruneServer(t)
	mock := &database.MockStore{}
	mock.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{
			{ID: realID, Name: "Discworld"},
			{ID: typoID, Name: "  discworld "},
		}, nil
	}
	mock.GetBooksBySeriesIDCoreFunc = func(id int) ([]database.BookCore, error) {
		if id == realID {
			return nil, errors.New("simulated: transient read failure")
		}
		return []database.BookCore{{ID: "typo-a"}, {ID: "typo-b"}}, nil
	}
	// ONLY the vote getter fails. AllVersions succeeds and returns the real 400
	// books, because that is what makes this test able to fail: if the repoint
	// getter errored too, the merge would abort for an unrelated reason and the
	// assertions below would pass whether or not the vote disqualifies the group.
	// Measured — with both getters failing, removing the disqualification left the
	// test green.
	mock.GetBooksBySeriesIDAllVersionsFunc = func(id int) ([]database.BookCore, error) {
		if id == realID {
			out := make([]database.BookCore, 0, 400)
			for i := range 400 {
				out = append(out, database.BookCore{ID: fmt.Sprintf("real-%d", i)})
			}
			return out, nil
		}
		return []database.BookCore{{ID: "typo-a"}, {ID: "typo-b"}}, nil
	}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		sid := realID
		return &database.Book{ID: id, SeriesID: &sid}, nil
	}

	var deleted []int
	mock.DeleteSeriesFunc = func(id int) error {
		deleted = append(deleted, id)
		return nil
	}
	var repointed []string
	mock.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		repointed = append(repointed, id)
		return b, nil
	}

	store := seriesRefCountingStore{MockStore: mock, refCounts: map[int]int{realID: 400, typoID: 2}}
	err := s.executeSeriesPrune(context.Background(), store, seriesPruneNoopProgress{}, "")
	if err == nil {
		t.Fatal("a failed canonical count was not reported; the run looks successful while a " +
			"group was silently left unmerged (or worse, merged the wrong way)")
	}

	for _, id := range deleted {
		if id == realID {
			t.Fatalf("series %d was DELETED because its book count could not be read. A read "+
				"error must not decide which duplicate survives — it made the real series "+
				"lose the vote to a 2-book near-duplicate.", realID)
		}
	}
	if len(repointed) > 0 {
		t.Errorf("books were repointed (%v) for a group whose canonical series could not be "+
			"determined; the merge must not proceed on an unresolved vote", repointed)
	}
}
