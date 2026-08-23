// file: internal/maintenance/jobs/cleanup_series_phase1_test.go
// version: 1.0.0
// guid: 7c4e1a90-3d52-4b18-8f6a-2ad9e5b71c34
// last-edited: 2026-08-23

package jobs

import (
	"context"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TASK-044 follow-up. Phase 1 of cleanup-series was the one guarded call site
// with no test: nothing in the repo drove cleanupSeriesJob.Run, so mutating the
// `refCounts[ser.ID] > 1` guard left the whole suite green.
//
// Phase 1 is also the site whose trigger condition matches the recorded
// production damage exactly -- it fires when the filtered count reads 1 and the
// unfiltered count is higher, which is the "one primary book plus N non-primary
// versions" shape behind the 6,893 phantom series IDs in
// database/series_bookref.go.

// csPhase1Reporter records what an operator would actually see in the job log.
type csPhase1Reporter struct {
	logs []string
	incr int
}

func (r *csPhase1Reporter) SetTotal(int) {}
func (r *csPhase1Reporter) Increment()   { r.incr++ }
func (r *csPhase1Reporter) Log(level, message string, _ *string) {
	r.logs = append(r.logs, level+": "+message)
}

// csPhase1Store embeds *database.MockStore (which satisfies maintenance.JobStore)
// and overrides the unfiltered counter. AsSeriesBookRefStore finds the override
// through the promoted-method set. The receiver is a VALUE, matching how the
// store is passed -- a pointer receiver here would silently fail the interface
// assertion and the guard would read MockStore's own empty map instead.
type csPhase1Store struct {
	*database.MockStore
	refCounts map[int]int
}

func (s csPhase1Store) GetAllSeriesBookRefCounts() (map[int]int, error) {
	return s.refCounts, nil
}

func newCsPhase1Store(t *testing.T, refCounts map[int]int, deleted *[]int) csPhase1Store {
	t.Helper()
	mock := &database.MockStore{}
	mock.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{{ID: 7, Name: "Solo"}}, nil
	}
	// The FILTERED display count says exactly one book — this is what makes
	// series 7 look like a collapsible 1-book series.
	mock.GetAllSeriesBookCountsFunc = func() (map[int]int, error) {
		return map[int]int{7: 1}, nil
	}
	mock.GetBooksBySeriesIDCoreFunc = func(id int) ([]database.BookCore, error) {
		if id == 7 {
			return []database.BookCore{{ID: "VISIBLE"}}, nil
		}
		return nil, nil
	}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		sid := 7
		return &database.Book{ID: id, SeriesID: &sid}, nil
	}
	mock.UpdateBookFunc = func(_ string, b *database.Book) (*database.Book, error) { return b, nil }
	mock.DeleteSeriesFunc = func(id int) error {
		*deleted = append(*deleted, id)
		return nil
	}
	return csPhase1Store{MockStore: mock, refCounts: refCounts}
}

func TestCleanupSeriesRun_Phase1KeepsSeriesWithHiddenReferences(t *testing.T) {
	// Filtered count 1, unfiltered count 4: one primary book plus three
	// trashed or non-primary versions. Unlinking the visible book and deleting
	// the row would strand the other three.
	var deleted []int
	store := newCsPhase1Store(t, map[int]int{7: 4}, &deleted)
	rep := &csPhase1Reporter{}

	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false); err != nil {
		t.Fatalf("Run returned an error, want a clean skip: %v", err)
	}

	for _, id := range deleted {
		if id == 7 {
			t.Fatal("deleted series 7 while 3 hidden rows still reference it — this is the phantom-series-ID bug")
		}
	}

	// Assert the override actually reached the guard. Without this the test
	// could pass for the wrong reason: if AsSeriesBookRefStore unwrapped past
	// the wrapper to MockStore's own empty map, refCounts[7] would be 0, the
	// guard would not fire, and "nothing deleted" would have to come from
	// somewhere else entirely.
	var found string
	for _, l := range rep.logs {
		if strings.Contains(l, "Kept 1-book series 7") {
			found = l
		}
	}
	if found == "" {
		t.Fatalf("the refusal must be visible in the job log an operator reads, got logs=%v", rep.logs)
	}
	if !strings.Contains(found, "4 books reference it") {
		t.Fatalf("the log must carry the unfiltered count that triggered the skip, got %q", found)
	}
}

func TestCleanupSeriesRun_Phase1StillCollapsesAGenuine1BookSeries(t *testing.T) {
	// POSITIVE CONTROL. Without it, a guard that skips every series passes the
	// test above while silently turning phase 1 into a no-op.
	var deleted []int
	store := newCsPhase1Store(t, map[int]int{7: 1}, &deleted)
	rep := &csPhase1Reporter{}

	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(deleted) != 1 || deleted[0] != 7 {
		t.Fatalf("the one reference IS the visible book, so the series is genuinely collapsible and must be deleted, got deleted=%v", deleted)
	}
}
