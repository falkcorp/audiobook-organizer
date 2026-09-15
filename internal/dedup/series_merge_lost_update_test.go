// file: internal/dedup/series_merge_lost_update_test.go
// version: 1.0.0
// guid: 5cdf8fef-1d80-433f-81c5-b44a8916ae81
// last-edited: 2026-09-14

package dedup

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// lostUpdateBookStore is a stateful book store for the lost-update race:
// GetBookByID returns a copy of the stored row, UpdateBook stores a copy, and
// ModifyBook is a locked read-modify-write. concurrent is the OTHER writer: it
// lands on the stored row right after every raw GetBookByID and right before
// every ModifyBook takes the row lock, which is where a real concurrent
// UpdateBook of another column lands in production. A site that reads a row,
// changes one column and writes the WHOLE row back reverts that writer's
// column; a site that goes through ModifyBook keeps it.
type lostUpdateBookStore struct {
	*database.MockStore
	mu         sync.Mutex
	rows       map[string]*database.Book
	concurrent func(*database.Book)
}

func newLostUpdateBookStore(books ...*database.Book) *lostUpdateBookStore {
	s := &lostUpdateBookStore{MockStore: &database.MockStore{}, rows: map[string]*database.Book{}}
	for _, b := range books {
		cp := *b
		s.rows[b.ID] = &cp
	}
	return s
}

func (s *lostUpdateBookStore) landConcurrentLocked(id string) {
	if b := s.rows[id]; b != nil && s.concurrent != nil {
		s.concurrent(b)
	}
}

func (s *lostUpdateBookStore) GetBookByID(id string) (*database.Book, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.rows[id]
	if b == nil {
		return nil, nil
	}
	cp := *b
	s.landConcurrentLocked(id)
	return &cp, nil
}

func (s *lostUpdateBookStore) UpdateBook(id string, b *database.Book) (*database.Book, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *b
	s.rows[id] = &cp
	return &cp, nil
}

func (s *lostUpdateBookStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.landConcurrentLocked(id)
	b := s.rows[id]
	if b == nil {
		return nil, nil
	}
	cp := *b
	if err := fn(&cp); err != nil {
		if errors.Is(err, database.ErrSkipBookWrite) {
			return &cp, nil
		}
		return nil, err
	}
	stored := cp
	s.rows[id] = &stored
	return &cp, nil
}

func (s *lostUpdateBookStore) stored(id string) *database.Book {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows[id]
}

// setDurationConcurrently is the other writer: it commits Duration, a column
// none of the sites under test own.
func setDurationConcurrently(b *database.Book) {
	d := 4242
	b.Duration = &d
}

// TestMergeSeries_RepointDoesNotRevertConcurrentColumns pins the lost-update
// fix on the series-merge repoint (audit A1#15): the repoint must write
// SeriesID alone, so a Duration another writer commits between the hydrate and
// the write survives. Against the old GetBookByID -> UpdateBook(whole row) it
// fails with "Duration reverted".
func TestMergeSeries_RepointDoesNotRevertConcurrentColumns(t *testing.T) {
	from := 2
	store := newLostUpdateBookStore(&database.Book{ID: "b1", Title: "One", SeriesID: &from})
	store.concurrent = setDurationConcurrently
	store.GetSeriesByIDFunc = func(id int) (*database.Series, error) {
		return &database.Series{ID: id, Name: "Saga"}, nil
	}
	store.GetAllSeriesBookRefCountsFunc = func() (map[int]int, error) { return map[int]int{2: 1}, nil }
	store.GetBooksBySeriesIDsAllVersionsFunc = func(ids []int) (map[int][]database.BookCore, error) {
		return map[int][]database.BookCore{2: {{ID: "b1"}}}, nil
	}
	var deleted []int
	store.DeleteSeriesFunc = func(id int) error { deleted = append(deleted, id); return nil }

	res, err := MergeSeries(context.Background(), store, testDedupOpID, 1, []int{2}, "", nil)
	if err != nil {
		t.Fatalf("MergeSeries: %v", err)
	}
	if res.MergedCount != 1 || len(deleted) != 1 {
		t.Fatalf("merged=%d deleted=%v, want 1 and [2] (errors=%v)", res.MergedCount, deleted, res.Errors)
	}
	got := store.stored("b1")
	if got == nil || got.SeriesID == nil || *got.SeriesID != 1 {
		t.Fatalf("book was not repointed to series 1: %+v", got)
	}
	if got.Duration == nil || *got.Duration != 4242 {
		t.Fatalf("Duration reverted by the repoint write: got %v, want 4242 (the concurrent writer's value)", got.Duration)
	}
}
