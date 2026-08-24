// file: internal/server/series_prune_phase2_test.go
// version: 1.2.0
// guid: 8f3d21c6-47ba-4e09-95d1-6c027ea3b4d8
// last-edited: 2026-08-24

package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Phase 1 of executeSeriesPrune now REFUSES to delete a series when a book could
// not be repointed. Phase 2 of the same function then deletes any series whose
// unfiltered reference count is zero -- and it computes those counts AFTER phase
// 1 has run.
//
// So the refusal is only durable if the book that failed still counts as a
// reference. If it does not, phase 2 silently deletes the series phase 1 just
// refused, and the guard is decorative.
//
// No existing test could answer that. seriesRefCountingStore takes a STATIC
// refCounts map, and every fixture passes non-zero counts for every series, so
// phase 2's delete branch is never entered anywhere in the suite -- including by
// all five mutants used to verify the phase-1 gate. The store below derives its
// counts from the books' actual series assignments instead, which is the only
// way the two phases can be observed disagreeing.

// seriesLiveRefStore answers GetAllSeriesBookRefCounts from live book state
// rather than a fixed map, so a repoint (or a failed repoint) moves the count the
// way it would in a real store.
type seriesLiveRefStore struct {
	*database.MockStore
	assignments map[string]int // bookID -> seriesID currently on the row
}

func (s seriesLiveRefStore) GetAllSeriesBookRefCounts() (map[int]int, error) {
	counts := map[int]int{}
	for _, sid := range s.assignments {
		counts[sid]++
	}
	return counts, nil
}

// TestExecuteSeriesPrune_Phase2DoesNotUndoPhase1Refusal is the load-bearing one.
//
// Series 2 holds a primary and an alternate rip. The rip's write fails, so phase
// 1 refuses to delete series 2. The rip's row therefore still carries series 2,
// phase 2 counts it, and series 2 survives.
//
// Series 3 is empty and exists as a POSITIVE CONTROL: if phase 2 never ran at
// all, the absence of a delete on series 2 would prove nothing.
func TestExecuteSeriesPrune_Phase2DoesNotUndoPhase1Refusal(t *testing.T) {
	const (
		keepID  = 1
		mergeID = 2
		emptyID = 3
		keeper  = "keeper-book"
		primary = "prune-primary"
		altRip  = "prune-alternate-rip"
	)

	s := newSeriesPruneServer(t)
	mock, _ := seriesPruneMergeFixture(keepID, mergeID, primary, altRip)

	// Series 3 joins the list but belongs to no group, so phase 1 ignores it and
	// only phase 2 can remove it.
	mock.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{
			{ID: keepID, Name: "Discworld"},
			{ID: mergeID, Name: "  discworld "},
			{ID: emptyID, Name: "Abandoned Series"},
		}, nil
	}

	assignments := map[string]int{keeper: keepID, primary: mergeID, altRip: mergeID}

	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		sid, ok := assignments[id]
		if !ok {
			return nil, nil
		}
		return &database.Book{ID: id, SeriesID: &sid}, nil
	}
	// The alternate rip refuses to be written; the primary succeeds first, so
	// this is a genuine PARTIAL repoint.
	mock.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		if id == altRip {
			return nil, errors.New("simulated write failure")
		}
		if b.SeriesID != nil {
			assignments[id] = *b.SeriesID
		}
		return b, nil
	}

	var deleted []int
	mock.DeleteSeriesFunc = func(id int) error {
		deleted = append(deleted, id)
		return nil
	}

	store := seriesLiveRefStore{MockStore: mock, assignments: assignments}
	// Non-nil by design: this fixture drives phase 1 into a refusal, and a refusal
	// is now reported as a failure so the "re-run after resolving" instruction
	// reaches an operator. What matters to THIS test is what phase 2 did next,
	// asserted below.
	if err := s.executeSeriesPrune(context.Background(), store, seriesPruneNoopProgress{}, ""); err == nil {
		t.Fatal("expected the phase-1 refusal to be reported as an error")
	}

	// Positive control. Without this, a phase 2 that silently did nothing would
	// make the real assertion below pass for the wrong reason.
	var sawEmpty bool
	for _, id := range deleted {
		if id == emptyID {
			sawEmpty = true
		}
	}
	if !sawEmpty {
		t.Fatalf("phase 2 did not delete the unreferenced series %d (deleted=%v) -- "+
			"phase 2 never ran, so this test cannot tell whether it respects phase 1", emptyID, deleted)
	}

	// The real assertion.
	for _, id := range deleted {
		if id == mergeID {
			t.Fatalf("phase 2 deleted series %d after phase 1 REFUSED to, because %s could "+
				"not be repointed. %s still references it and is now stranded -- the phase-1 "+
				"guard is undone by the sweep that runs after it.", mergeID, altRip, altRip)
		}
	}

	// And the repoint that DID succeed must have stuck, or the refusal above is
	// being proven by a merge that simply never happened.
	if assignments[primary] != keepID {
		t.Errorf("%s was not repointed to %d (got %d) -- phase 1 did not merge, so the "+
			"refusal proves nothing", primary, keepID, assignments[primary])
	}
}

// TestExecuteSeriesPrune_ACancellationKeepsTheRecordedErrors pins the exit that
// used to throw everything away.
//
// Phase 1 refuses a delete and records "REFUSING to delete it ... Re-run after
// resolving the errors above". The context is then cancelled. Both cancellation
// exits used to `return ctx.Err()` bare, so the operator got "context canceled"
// and no record that a series had been left half-merged — books repointed, the
// old row still present, and nothing saying so.
//
// This is the same defect the same change fixes one file over with
// errors.Join(opErr, ctx.Err()). Building the error in the deferred block means
// the next early return added inherits the behaviour rather than reintroducing
// the bug, and this test is what holds that.
func TestExecuteSeriesPrune_ACancellationKeepsTheRecordedErrors(t *testing.T) {
	const (
		keepID  = 1
		mergeID = 2
		primary = "cancel-primary"
		altRip  = "cancel-alternate-rip"
	)

	s := newSeriesPruneServer(t)
	mock, assignments := seriesPruneMergeFixture(keepID, mergeID, primary, altRip)

	ctx, cancel := context.WithCancel(context.Background())

	// The rip's write fails, so phase 1 refuses the delete and records it. The
	// cancellation fires immediately afterwards, on the next loop check.
	mock.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		if id == altRip {
			cancel() // refusal recorded, now the run is cancelled
			return nil, errors.New("simulated write failure")
		}
		if b.SeriesID != nil {
			assignments[id] = *b.SeriesID
		}
		return b, nil
	}
	mock.DeleteSeriesFunc = func(int) error { return nil }

	store := seriesRefCountingStore{MockStore: mock, refCounts: map[int]int{keepID: 2, mergeID: 2}}
	err := s.executeSeriesPrune(ctx, store, seriesPruneNoopProgress{}, "")
	if err == nil {
		t.Fatal("a cancelled prune that had already recorded a refusal returned nil")
	}

	// Both facts must survive: that it was cancelled, and WHAT it had recorded.
	// Reporting only the cancellation is what made the half-merged series
	// invisible.
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the cancellation was lost from the error: %v", err)
	}
	if !strings.Contains(err.Error(), "REFUSING to delete") {
		t.Errorf("the recorded refusal was discarded by the cancellation exit; the operator "+
			"cannot tell a series was left half-merged. got: %v", err)
	}
}
