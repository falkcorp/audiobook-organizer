// file: internal/batch/lost_update_test.go
// version: 1.0.0
// guid: af201c82-ca1b-42f1-8c4e-2733940375e8
// last-edited: 2026-09-14

package batch

import (
	"errors"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// lostUpdateStore is a stateful book store: GetBookByID returns a copy,
// UpdateBook stores a copy of what it is handed, and ModifyBook mutates the
// stored row in place under the store lock, as PebbleStore does under the
// book's write stripe. concurrentWrite plays a second writer that commits
// Duration on the stored row once, just before the caller's first read or
// locked write -- the window a GetBookByID -> mutate -> UpdateBook round trip
// cannot see.
type lostUpdateStore struct {
	*database.MockStore
	mu              sync.Mutex
	books           map[string]*database.Book
	interposed      bool
	concurrentWrite func(stored *database.Book)
}

func newLostUpdateStore(seed ...*database.Book) *lostUpdateStore {
	s := &lostUpdateStore{MockStore: &database.MockStore{}, books: map[string]*database.Book{}}
	for _, b := range seed {
		s.books[b.ID] = b
	}
	return s
}

func (s *lostUpdateStore) interposeLocked(stored *database.Book) {
	if s.concurrentWrite != nil && !s.interposed {
		s.interposed = true
		s.concurrentWrite(stored)
	}
}

func (s *lostUpdateStore) GetBookByID(id string) (*database.Book, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.books[id]
	if b == nil {
		return nil, nil
	}
	out, err := database.SnapshotBook(b)
	if err != nil {
		return nil, err
	}
	s.interposeLocked(b)
	return out, nil
}

func (s *lostUpdateStore) UpdateBook(id string, book *database.Book) (*database.Book, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp, err := database.SnapshotBook(book)
	if err != nil {
		return nil, err
	}
	s.books[id] = cp
	return database.SnapshotBook(cp)
}

func (s *lostUpdateStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.books[id]
	if b == nil {
		return nil, nil
	}
	s.interposeLocked(b)
	if err := fn(b); err != nil {
		if errors.Is(err, database.ErrSkipBookWrite) {
			return database.SnapshotBook(b)
		}
		return nil, err
	}
	return database.SnapshotBook(b)
}

func (s *lostUpdateStore) stored(t *testing.T, id string) *database.Book {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	out, err := database.SnapshotBook(s.books[id])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A concurrent writer's Duration must survive a batch title edit: the edit
// sets the requested fields on the stored row and nothing else.
func TestUpdateAudiobooks_KeepsConcurrentWrite(t *testing.T) {
	store := newLostUpdateStore(&database.Book{ID: "b1", Title: "Old"})
	duration := 4242
	store.concurrentWrite = func(stored *database.Book) { stored.Duration = &duration }
	bs := NewBatchService(store)

	resp := bs.UpdateAudiobooks(&BatchUpdateRequest{IDs: []string{"b1"}, Updates: map[string]any{"title": "New"}})
	if resp.Success != 1 || resp.Failed != 0 {
		t.Fatalf("resp = %+v, want one success", resp)
	}

	got := store.stored(t, "b1")
	if got.Title != "New" {
		t.Fatalf("Title = %q, want New", got.Title)
	}
	if got.Duration == nil || *got.Duration != duration {
		t.Fatalf("lost update: Duration written by a concurrent writer between the batch read and its write was reverted: got %v, want %d", got.Duration, duration)
	}
}
