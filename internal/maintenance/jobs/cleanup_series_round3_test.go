// file: internal/maintenance/jobs/cleanup_series_round3_test.go
// version: 1.0.0
// guid: c9f23247-f94e-4264-897d-925979b69bf4
// last-edited: 2026-11-18

package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// A delete that fails for a reason about ONE row (a series row that will not
// decode) is not a stop: the series is counted failed, logged with its real
// cause, and the run goes on to the next candidate. Before this, any delete error
// stopped the run and failed the op, and the store's next 40,000 series went
// unexamined because of one bad row.
func TestCleanupSeriesRun_Phase1RowLocalDeleteErrorIsCountedAndTheRunGoesOn(t *testing.T) {
	store := newCsCondStore(
		[]database.Series{{ID: 7, Name: "Year One"}, {ID: 8, Name: "year one"}, {ID: 9, Name: "Zero Year"}},
		database.Book{ID: "year-one-1", SeriesID: intp(7)},
		database.Book{ID: "year-one-2", SeriesID: intp(8)},
		database.Book{ID: "year-one-3", SeriesID: intp(8)},
		database.Book{ID: "year-one-4", SeriesID: intp(8)},
		database.Book{ID: "zero-year-1", SeriesID: intp(9)},
	)
	cause := errors.New("decode series 7: invalid character 'x' looking for beginning of value")
	store.deleteErrFor = map[int]error{7: cause}
	rep := &csPhase1Reporter{}
	done, text := csCaptureLogs(t)

	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false); err != nil {
		t.Fatalf("Run: %v, want a row-local failure not to fail the op", err)
	}

	d := done()
	for k, want := range map[string]int{
		"singleFound": 2, "singleDeleteFailed": 1, "singleApplied": 1, "singleNotReached": 0,
		// Series 7 is settled by phase 1, so phase 2 does not group it with 8 and
		// refuse it again under the reference guard's cause.
		"dupFound": 0, "dupRefused": 0, "dupNotReached": 0,
	} {
		if got := csDoneInt(t, d, k); got != want {
			t.Errorf("Done %s = %d, want %d: %v", k, got, want, d)
		}
	}
	if csDoneBool(t, d, "stoppedPhase1") || csDoneBool(t, d, "stoppedPhase2") {
		t.Errorf("a row-local failure must not stop either phase: %v", d)
	}
	csAssertBucketsAddUp(t, d)
	if !store.hasSeries(7) || store.hasSeries(9) {
		t.Errorf("series 7 kept=%v (want true), series 9 kept=%v (want false: the run went on)", store.hasSeries(7), store.hasSeries(9))
	}
	if csLogContains(rep, "Reference guard kept") {
		t.Errorf("the guard must not name a cause that is not the real one: %v", rep.logs)
	}
	if !csLogContains(rep, "could not verify 1 single-book series and 0 merged-from series") {
		t.Errorf("the failed delete must be in the warn line: %v", rep.logs)
	}
	if !strings.Contains(text(), "invalid character 'x'") {
		t.Errorf("the real cause must be logged, got %q", text())
	}
}

// The same in phase 2: the series after the one whose delete failed row-locally
// is still settled.
func TestCleanupSeriesRun_Phase2RowLocalDeleteErrorIsCountedAndTheGroupGoesOn(t *testing.T) {
	store := csOwlsTripleStore()
	store.deleteErrFor = map[int]error{7: errors.New("decode series 7: invalid character 'x'")}
	rep := &csPhase1Reporter{}
	done, text := csCaptureLogs(t)

	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false); err != nil {
		t.Fatalf("Run: %v, want a row-local failure not to fail the op", err)
	}

	d := done()
	for k, want := range map[string]int{
		"dupFound": 1, "dupMergeFromSeries": 2, "dupDeleteFailed": 1, "dupSeriesDeleted": 1, "dupNotReached": 0,
		"dupGroupsNotReached": 0,
	} {
		if got := csDoneInt(t, d, k); got != want {
			t.Errorf("Done %s = %d, want %d: %v", k, got, want, d)
		}
	}
	if csDoneBool(t, d, "stoppedPhase1") || csDoneBool(t, d, "stoppedPhase2") {
		t.Errorf("a row-local failure must not stop either phase: %v", d)
	}
	csAssertBucketsAddUp(t, d)
	if !store.hasSeries(7) || store.hasSeries(9) {
		t.Errorf("series 7 kept=%v (want true), series 9 kept=%v (want false: the group went on)", store.hasSeries(7), store.hasSeries(9))
	}
	if !csLogContains(rep, "could not verify 0 single-book series and 1 merged-from series") {
		t.Errorf("the failed delete must be in the warn line: %v", rep.logs)
	}
	if !strings.Contains(text(), "invalid character 'x'") {
		t.Errorf("the real cause must be logged, got %q", text())
	}
}

// A write failure on the delete is also about one row, not the store.
func TestCleanupSeriesRun_ADeleteWriteFailureIsRowLocalToo(t *testing.T) {
	store := csHushStore()
	store.deleteErrFor = map[int]error{7: errors.New("pebble: write failed")}
	done := csCaptureDone(t)

	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, &csPhase1Reporter{}, false); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d := done(); csDoneInt(t, d, "singleDeleteFailed") != 1 || csDoneBool(t, d, "stoppedPhase1") {
		t.Fatalf("a failed write is counted failed and does not stop the run: %v", d)
	}
}

// n-C. Phase 1 unlinked one of a series' books and found another already filed
// elsewhere, then failed to unlink the third (a transient write error). Phase 2
// re-homes the third. Its start-of-run reference count still counts the two phase
// 1 verified, so it would see two stranded rows: it refused the series under the
// reference guard's cause (trashed or unhydratable rows) when nothing of the kind
// exists, and the series was left behind for the next run. Phase 1's verified
// moves are subtracted, and the series goes.
func TestCleanupSeriesRun_Phase2DoesNotRefuseASeriesPhase1PartlyUnlinked(t *testing.T) {
	store := newCsCondStore(
		[]database.Series{{ID: 7, Name: "Year One"}, {ID: 8, Name: "year one"}},
		// Series 7: one primary book and two non-primary versions, so the display
		// count says one book and the write set holds three.
		database.Book{ID: "year-one-1", SeriesID: intp(7)},
		database.Book{ID: "year-one-2", SeriesID: intp(7)},
		database.Book{ID: "year-one-3", SeriesID: intp(7)},
		database.Book{ID: "year-one-4", SeriesID: intp(8)},
		database.Book{ID: "year-one-5", SeriesID: intp(8)},
	)
	store.hidden = map[string]bool{"year-one-2": true, "year-one-3": true}
	// A concurrent writer files year-one-2 in series 8 after the membership read:
	// phase 1's compare-and-set skips it (Skipped), year-one-1 is unlinked
	// (Unlinked), and year-one-3 fails once.
	store.afterMembership = func(s *csCondStore) { s.books["year-one-2"].SeriesID = intp(8) }
	store.modifyFailOnce = map[string]error{"year-one-3": errors.New("simulated transient write failure")}
	rep := &csPhase1Reporter{}
	done := csCaptureDone(t)

	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false); err != nil {
		t.Fatalf("Run: %v", err)
	}

	d := done()
	for k, want := range map[string]int{
		"singleFound": 1, "singleUnlinkFailed": 1, "dupFound": 1, "dupMergeFromSeries": 1,
		"dupRefused": 0, "dupSeriesDeleted": 1, "dupNotReached": 0,
	} {
		if got := csDoneInt(t, d, k); got != want {
			t.Errorf("Done %s = %d, want %d: %v", k, got, want, d)
		}
	}
	csAssertBucketsAddUp(t, d)
	if store.hasSeries(7) {
		t.Errorf("series 7 was kept: its books are all off it")
	}
	if csLogContains(rep, "Reference guard kept") {
		t.Errorf("the guard named a cause that is not the real one: %v", rep.logs)
	}
	if dangling := store.dangling(); len(dangling) != 0 {
		t.Errorf("books name a series with no row: %v", dangling)
	}
}

// n-D. After a store-wide stop in phase 1, the candidates behind the failed delete
// and the groups phase 2 never got to are counted, so the Done line accounts for
// everything the run found.
func TestCleanupSeriesRun_AStopCountsTheCandidatesAndGroupsItNeverReached(t *testing.T) {
	store := newCsCondStore(
		[]database.Series{
			{ID: 7, Name: "Year One"}, {ID: 8, Name: "Zero Year"}, {ID: 9, Name: "Endgame"},
			{ID: 10, Name: "Court of Owls"}, {ID: 11, Name: "court of owls"},
			{ID: 12, Name: "Night of Owls"}, {ID: 13, Name: "night of owls"},
		},
		database.Book{ID: "year-one-1", SeriesID: intp(7)},
		database.Book{ID: "zero-year-1", SeriesID: intp(8)},
		database.Book{ID: "endgame-1", SeriesID: intp(9)},
		database.Book{ID: "court-of-owls-1", SeriesID: intp(10), SeriesSequence: intp(2)},
		database.Book{ID: "court-of-owls-2", SeriesID: intp(11)},
		database.Book{ID: "court-of-owls-3", SeriesID: intp(11)},
		database.Book{ID: "night-of-owls-1", SeriesID: intp(12), SeriesSequence: intp(2)},
		database.Book{ID: "night-of-owls-2", SeriesID: intp(13)},
		database.Book{ID: "night-of-owls-3", SeriesID: intp(13)},
	)
	cause := csStoreWideErr(database.ErrSeriesDeleteMemdbNotServing)
	store.deleteIfErr = cause
	rep := &csPhase1Reporter{}
	done := csCaptureDone(t)

	err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false)
	csRequireStopError(t, err, cause, "phase 1")

	d := done()
	for k, want := range map[string]int{
		"singleFound": 3, "singleDeleteFailed": 1, "singleNotReached": 2,
		"dupFound": 2, "dupGroupsNotReached": 2, "dupNotReached": 2, "dupMergeFromSeries": 2,
	} {
		if got := csDoneInt(t, d, k); got != want {
			t.Errorf("Done %s = %d, want %d: %v", k, got, want, d)
		}
	}
	csAssertBucketsAddUp(t, d)
	if !csLogContains(rep, "2 single-book series and 2 duplicate groups were not reached") {
		t.Errorf("the warn line must say what was not reached: %v", rep.logs)
	}
	// Reads only after the stop: nothing past the first delete was written.
	if len(store.conditionalCalls) != 1 {
		t.Errorf("conditional deletes = %v, want exactly the first", store.conditionalCalls)
	}
	for _, id := range []string{"court-of-owls-1", "night-of-owls-1"} {
		if got := store.books[id].SeriesID; got == nil || (*got != 10 && *got != 12) {
			t.Errorf("book %s was re-homed after phase 1 had stopped: %v", id, got)
		}
	}
}
