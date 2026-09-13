// file: internal/maintenance/jobs/cleanup_series_hoist_test.go
// version: 1.0.0
// guid: 3e7b91d4-0c5a-4f28-a6e2-d91c47b8f053
// last-edited: 2026-09-13

package jobs

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestCleanupSeriesRun_HoistedMembershipStaysCurrentAcrossPhases pins two
// things about SERIES-MERGE-PERSERIES-SCAN-COST's hoist:
//
//  1. The per-series getter is never called: membership comes from ONE bulk
//     read for the whole job.
//  2. The hoisted map is kept current when phase 1 moves books. Series 7 is a
//     1-book series whose unlink fails part-way: PRIMARY is unlinked, ALT-1's
//     write fails, so the series row survives into phase 2. There it is the
//     merged-from half of a duplicate-name group ("Solo" / "solo"). A fresh
//     read would no longer list PRIMARY (it has no series now). A stale hoisted
//     map still would, and phase 2 would write PRIMARY onto series 8 -- a book
//     the job had just deliberately unlinked. Dropping the members.Move in
//     csUnlinkAndDeleteSeries makes this test fail.
func TestCleanupSeriesRun_HoistedMembershipStaysCurrentAcrossPhases(t *testing.T) {
	var deleted []int
	var unlinked []string
	store := newCsAllVersionsStore(t, map[int]int{7: 3, 8: 5}, &deleted, &unlinked)

	store.MockStore.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{{ID: 7, Name: "Solo"}, {ID: 8, Name: "solo"}}, nil
	}
	store.MockStore.GetAllSeriesBookCountsFunc = func() (map[int]int, error) {
		return map[int]int{7: 1, 8: 5}, nil
	}

	perSeriesCalls := 0
	store.MockStore.GetBooksBySeriesIDAllVersionsFunc = func(int) ([]database.BookCore, error) {
		perSeriesCalls++
		return nil, errors.New("per-series membership read inside the job loop")
	}
	bulkCalls := 0
	store.MockStore.GetBooksBySeriesIDsAllVersionsFunc = func(ids []int) (map[int][]database.BookCore, error) {
		bulkCalls++
		return map[int][]database.BookCore{
			7: {{ID: "PRIMARY"}, {ID: "ALT-1"}, {ID: "ALT-2"}},
			8: {{ID: "K1"}, {ID: "K2"}, {ID: "K3"}, {ID: "K4"}, {ID: "K5"}},
		}, nil
	}

	var rehomed []string
	store.MockStore.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		if id == "ALT-1" {
			return nil, errors.New("simulated write failure")
		}
		if b.SeriesID == nil {
			unlinked = append(unlinked, id)
		} else if *b.SeriesID == 8 {
			rehomed = append(rehomed, id)
		}
		return b, nil
	}

	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, &csPhase1Reporter{}, false); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if perSeriesCalls != 0 {
		t.Errorf("GetBooksBySeriesIDAllVersions called %d times; membership must be hoisted", perSeriesCalls)
	}
	if bulkCalls != 1 {
		t.Errorf("bulk membership read %d times, want exactly 1 per job", bulkCalls)
	}
	if len(unlinked) != 1 || unlinked[0] != "PRIMARY" {
		t.Fatalf("fixture did not produce the partial unlink it depends on: unlinked=%v", unlinked)
	}
	for _, id := range rehomed {
		if id == "PRIMARY" {
			t.Fatalf("phase 2 re-homed PRIMARY onto series 8 after phase 1 unlinked it: the hoisted "+
				"membership map went stale (rehomed=%v)", rehomed)
		}
	}
	for _, id := range deleted {
		if id == 7 {
			t.Fatal("series 7 deleted while ALT-1 still references it")
		}
	}
}

// TestCleanupSeriesRun_FailsClosedWhenMembershipCannotBeLoaded: a failed bulk
// read aborts the job before any write, rather than reading as "every series
// is empty".
func TestCleanupSeriesRun_FailsClosedWhenMembershipCannotBeLoaded(t *testing.T) {
	var deleted []int
	var unlinked []string
	store := newCsAllVersionsStore(t, map[int]int{7: 3}, &deleted, &unlinked)
	store.MockStore.GetBooksBySeriesIDsAllVersionsFunc = func([]int) (map[int][]database.BookCore, error) {
		return nil, errors.New("scan failed")
	}

	err := (&cleanupSeriesJob{}).Run(context.Background(), store, &csPhase1Reporter{}, false)
	if err == nil {
		t.Fatal("Run succeeded with no series membership; it must fail closed")
	}
	if len(deleted) != 0 || len(unlinked) != 0 {
		t.Fatalf("wrote before failing: deleted=%v unlinked=%v", deleted, unlinked)
	}
}
