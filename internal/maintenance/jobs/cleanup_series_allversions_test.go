// file: internal/maintenance/jobs/cleanup_series_allversions_test.go
// version: 1.1.0
// guid: 6e2a9f13-84c7-4d05-b1e6-3a7f52cd8091
// last-edited: 2026-08-24

package jobs

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// These cover the BEHAVIOUR CHANGE in phase 1: it used to compare the unfiltered
// ref count against the literal 1, so a series holding one primary book plus its
// alternate rips refused forever -- refCounts counted all of them and the
// listing getter showed one. It now unlinks the complete set and compares
// against that, so the same series collapses.
//
// The guard is NOT weakened, which is what the second test exists to prove: a
// row neither getter can see (trashed, or counted by the memdb but unhydratable
// from Pebble) still refuses the delete.

// newCsAllVersionsStore builds a series 7 whose two getters DISAGREE: Core shows
// the primary alone, AllVersions shows the primary plus two alternate rips. It
// records every unlink so a test can assert the whole set was written, not just
// the visible row.
func newCsAllVersionsStore(
	t *testing.T, refCounts map[int]int, deleted *[]int, unlinked *[]string,
) csPhase1Store {
	t.Helper()
	mock := &database.MockStore{}
	mock.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{{ID: 7, Name: "Solo"}}, nil
	}
	// The FILTERED display count says one book, which is what makes series 7
	// look collapsible. Alternate rips must not make it read as a 3-book series.
	mock.GetAllSeriesBookCountsFunc = func() (map[int]int, error) {
		return map[int]int{7: 1}, nil
	}
	mock.GetBooksBySeriesIDCoreFunc = func(id int) ([]database.BookCore, error) {
		if id == 7 {
			return []database.BookCore{{ID: "PRIMARY"}}, nil
		}
		return nil, nil
	}
	mock.GetBooksBySeriesIDAllVersionsFunc = func(id int) ([]database.BookCore, error) {
		if id == 7 {
			return []database.BookCore{{ID: "PRIMARY"}, {ID: "ALT-1"}, {ID: "ALT-2"}}, nil
		}
		return nil, nil
	}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		sid := 7
		return &database.Book{ID: id, SeriesID: &sid}, nil
	}
	mock.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		// Record only genuine unlinks, so a write that left SeriesID set cannot
		// be mistaken for one.
		if b.SeriesID == nil {
			*unlinked = append(*unlinked, id)
		}
		return b, nil
	}
	mock.DeleteSeriesFunc = func(id int) error {
		*deleted = append(*deleted, id)
		return nil
	}
	return csPhase1Store{MockStore: mock, refCounts: refCounts}
}

// TestCleanupSeriesRun_Phase1CollapsesASeriesWhoseExtraRowsAreAllVersions is the
// behaviour change. refCounts is 3 and the complete-set getter returns all 3, so
// there is nothing this run cannot see and the series is genuinely collapsible.
// Before, this refused on every run forever.
func TestCleanupSeriesRun_Phase1CollapsesASeriesWhoseExtraRowsAreAllVersions(t *testing.T) {
	var deleted []int
	var unlinked []string
	store := newCsAllVersionsStore(t, map[int]int{7: 3}, &deleted, &unlinked)
	rep := &csPhase1Reporter{}

	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(deleted) != 1 || deleted[0] != 7 {
		t.Fatalf("every referencing row is visible to the complete-set getter, so series 7 "+
			"is collapsible and must be deleted; got deleted=%v", deleted)
	}

	// The whole point: ALL THREE are unlinked. Deleting after unlinking only
	// PRIMARY would strand the two rips -- the exact bug the old guard avoided
	// by refusing instead.
	for _, want := range []string{"PRIMARY", "ALT-1", "ALT-2"} {
		if !slices.Contains(unlinked, want) {
			t.Errorf("%s was not unlinked before series 7 was deleted (unlinked=%v) -- "+
				"it now references a series that does not exist", want, unlinked)
		}
	}
}

// TestCleanupSeriesRun_Phase1StillRefusesWhenARowIsInvisible proves the guard
// survived the change. AllVersions returns 3 but refCounts says 4: the fourth is
// trashed or unhydratable, and neither getter can reach it.
//
// Without this, swapping the comparison to len(unlink) could have disabled the
// guard entirely -- len(unlink) always equals what we just read, so a careless
// version of this change is a tautology that never refuses.
func TestCleanupSeriesRun_Phase1StillRefusesWhenARowIsInvisible(t *testing.T) {
	var deleted []int
	var unlinked []string
	store := newCsAllVersionsStore(t, map[int]int{7: 4}, &deleted, &unlinked)
	rep := &csPhase1Reporter{}

	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false); err != nil {
		t.Fatalf("Run returned an error, want a clean skip: %v", err)
	}

	for _, id := range deleted {
		if id == 7 {
			t.Fatal("deleted series 7 while a row neither getter can see still references it")
		}
	}

	var found string
	for _, l := range rep.logs {
		if strings.Contains(l, "Kept 1-book series 7") {
			found = l
		}
	}
	if found == "" {
		t.Fatalf("the refusal must be visible in the job log an operator reads, got logs=%v", rep.logs)
	}
	// The log has to carry BOTH sides of the comparison, or an operator cannot
	// tell a 4-vs-3 refusal from a 4-vs-1 one.
	if !strings.Contains(found, "4 books reference it") || !strings.Contains(found, "only 3") {
		t.Fatalf("the log must report both the unfiltered count and how many could be unlinked, got %q", found)
	}
}

// TestCleanupSeriesRun_Phase1DoesNotDeleteAfterAPartialUnlink pins the
// fail-closed ordering in csUnlinkAndDeleteSeries.
//
// Every row here is visible, so the guard passes and the job proceeds -- this is
// not about the guard. It is about what happens when a write fails midway: the
// series row must survive, because leaving it means the rows that failed still
// point at something that exists and a later run can retry. Deleting it would
// strand exactly the rows the failure prevented us from unlinking.
//
// A delete placed before the unlink loop, or one that ignored the loop's error,
// passes every other test in this file.
func TestCleanupSeriesRun_Phase1DoesNotDeleteAfterAPartialUnlink(t *testing.T) {
	var deleted []int
	var unlinked []string
	store := newCsAllVersionsStore(t, map[int]int{7: 3}, &deleted, &unlinked)

	// ALT-1 refuses to be written. PRIMARY (ordered first) succeeds, so this is
	// a genuine PARTIAL unlink rather than a total failure.
	store.MockStore.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		if id == "ALT-1" {
			return nil, errors.New("simulated write failure")
		}
		if b.SeriesID == nil {
			unlinked = append(unlinked, id)
		}
		return b, nil
	}

	rep := &csPhase1Reporter{}
	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false); err != nil {
		t.Fatalf("Run must absorb a per-series write failure, not abort the job: %v", err)
	}

	for _, id := range deleted {
		if id == 7 {
			t.Fatal("series 7 was deleted after an unlink failed -- ALT-1 and ALT-2 " +
				"still reference it and are now stranded. The delete must happen only " +
				"after EVERY unlink has succeeded.")
		}
	}
}
