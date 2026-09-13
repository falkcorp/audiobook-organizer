// file: internal/dedup/series_membership_hoist_test.go
// version: 1.0.0
// guid: 6b2d8f47-1e93-4a05-b7c8-0f5e3a9d2c71
// last-edited: 2026-09-13

package dedup

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// newHoistCountingStore wires a MockStore whose PER-SERIES membership getter
// counts calls and fails, and whose bulk read serves series 1..3. Any loop that
// still reads membership per series shows up as a non-zero count and an error.
func newHoistCountingStore(perSeries, bulk *int) *database.MockStore {
	m := &database.MockStore{}
	m.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{{ID: 1, Name: "Saga"}, {ID: 2, Name: "Saga"}, {ID: 3, Name: "Saga"}}, nil
	}
	m.GetSeriesByIDFunc = func(id int) (*database.Series, error) {
		return &database.Series{ID: id, Name: "Saga"}, nil
	}
	m.GetAllSeriesBookRefCountsFunc = func() (map[int]int, error) {
		return map[int]int{2: 1, 3: 2}, nil
	}
	m.GetBooksBySeriesIDAllVersionsFunc = func(int) ([]database.BookCore, error) {
		*perSeries++
		return nil, errors.New("per-series membership read inside a merge loop")
	}
	m.GetBooksBySeriesIDsAllVersionsFunc = func(ids []int) (map[int][]database.BookCore, error) {
		*bulk++
		return map[int][]database.BookCore{
			1: {{ID: "k"}},
			2: {{ID: "b2"}},
			3: {{ID: "b3a"}, {ID: "b3b"}},
		}, nil
	}
	m.GetBookByIDFunc = func(id string) (*database.Book, error) { return &database.Book{ID: id}, nil }
	m.UpdateBookFunc = func(_ string, b *database.Book) (*database.Book, error) { return b, nil }
	return m
}

func TestDedupSeries_ReadsMembershipOnceNotPerSeries(t *testing.T) {
	var perSeries, bulk int
	store := newHoistCountingStore(&perSeries, &bulk)
	var deleted []int
	store.DeleteSeriesFunc = func(id int) error { deleted = append(deleted, id); return nil }

	res, err := DedupSeries(context.Background(), store, testDedupOpID, newFakeScanController(), nil, false)
	if err != nil {
		t.Fatalf("DedupSeries: %v", err)
	}
	if perSeries != 0 || bulk != 1 {
		t.Fatalf("membership reads: per-series=%d bulk=%d, want 0 and 1 (errors=%v)", perSeries, bulk, res.Errors)
	}
	if res.TotalMerged != 2 || res.TotalBooksReassigned != 3 {
		t.Fatalf("merged=%d reassigned=%d, want 2 and 3 (errors=%v)", res.TotalMerged, res.TotalBooksReassigned, res.Errors)
	}
}

func TestMergeSeries_ReadsMembershipOnceNotPerSeries(t *testing.T) {
	var perSeries, bulk int
	store := newHoistCountingStore(&perSeries, &bulk)
	var deleted []int
	store.DeleteSeriesFunc = func(id int) error { deleted = append(deleted, id); return nil }

	res, err := MergeSeries(context.Background(), store, testDedupOpID, 1, []int{2, 3}, "", nil)
	if err != nil {
		t.Fatalf("MergeSeries: %v", err)
	}
	if perSeries != 0 || bulk != 1 {
		t.Fatalf("membership reads: per-series=%d bulk=%d, want 0 and 1 (errors=%v)", perSeries, bulk, res.Errors)
	}
	if len(deleted) != 2 {
		t.Fatalf("deleted=%v, want series 2 and 3 (errors=%v)", deleted, res.Errors)
	}
}

func TestDedupSeries_FailsClosedWhenMembershipCannotBeLoaded(t *testing.T) {
	var perSeries, bulk int
	store := newHoistCountingStore(&perSeries, &bulk)
	store.GetBooksBySeriesIDsAllVersionsFunc = func([]int) (map[int][]database.BookCore, error) {
		return nil, errors.New("scan failed")
	}
	store.DeleteSeriesFunc = func(id int) error {
		t.Errorf("DeleteSeries(%d) after a failed membership read", id)
		return nil
	}
	if _, err := DedupSeries(context.Background(), store, testDedupOpID, newFakeScanController(), nil, false); err == nil {
		t.Fatal("DedupSeries succeeded with no series membership; it must fail closed")
	}
}
