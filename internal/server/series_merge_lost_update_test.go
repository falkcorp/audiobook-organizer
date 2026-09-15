// file: internal/server/series_merge_lost_update_test.go
// version: 1.0.0
// guid: 8329b422-af7c-49ab-b6ae-fb8dd2711b9f
// last-edited: 2026-09-14

package server

import (
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

// TestMergeSeriesGroupHelper_RepointDoesNotRevertConcurrentColumns pins the
// lost-update fix on the maintenance series-merge repoint (audit A1#15): the
// repoint must write SeriesID alone, so a Duration another writer commits
// between its read and its write survives. Against the old GetBookByID ->
// UpdateBook(whole row) it fails with "Duration reverted".
func TestMergeSeriesGroupHelper_RepointDoesNotRevertConcurrentColumns(t *testing.T) {
	const keepID, fromID = 1, 2
	from := fromID
	store := newLostUpdateBookStore(&database.Book{ID: "b1", Title: "One", SeriesID: &from})
	store.concurrent = setDurationConcurrently
	var deleted []int
	store.DeleteSeriesFunc = func(id int) error { deleted = append(deleted, id); return nil }

	members := database.SeriesBooksMap{fromID: {{ID: "b1"}}}
	merged, refused, err := mergeSeriesGroupHelper(store, keepID, []int{fromID}, map[int]int{fromID: 1}, members)
	if err != nil {
		t.Fatalf("mergeSeriesGroupHelper: %v", err)
	}
	if merged != 1 || refused != 0 || len(deleted) != 1 {
		t.Fatalf("merged=%d refused=%d deleted=%v, want 1, 0 and [2]", merged, refused, deleted)
	}
	got := store.stored("b1")
	if got == nil || got.SeriesID == nil || *got.SeriesID != keepID {
		t.Fatalf("book was not repointed to series %d: %+v", keepID, got)
	}
	if got.Duration == nil || *got.Duration != 4242 {
		t.Fatalf("Duration reverted by the repoint write: got %v, want 4242 (the concurrent writer's value)", got.Duration)
	}
}
