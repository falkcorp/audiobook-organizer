// file: internal/quarantine/lost_update_test.go
// version: 1.0.0
// guid: a07e362f-63c1-4df5-9bb8-6e1183202317
// last-edited: 2026-09-14

package quarantine

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// lostUpdateStore is a stateful book store: GetBookByID returns a copy,
// UpdateBook stores a copy of what it is handed, and ModifyBook mutates the
// stored row in place under the store lock, as PebbleStore does under the
// book's write stripe. concurrentWrite plays a second writer that commits
// Duration on the stored row once, just before the caller's first read or
// locked write -- the window a GetBookByID -> move files -> UpdateBook round
// trip cannot see.
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
	require.NoError(t, err)
	return out
}

// A concurrent writer's Duration must survive QuarantineBook: the file moves
// run between the book read and the book write, and the write sets only the
// path and quarantine columns on the stored row.
func TestQuarantineBook_KeepsConcurrentWrite(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "incoming", "Book.mp3")
	writeAudio(t, src, "audio")
	store := newLostUpdateStore(&database.Book{ID: "b1", Title: "Book", FilePath: src, Format: "mp3"})
	duration := 4242
	store.concurrentWrite = func(stored *database.Book) { stored.Duration = &duration }
	qs := svcWith(store, root)

	require.NoError(t, qs.QuarantineBook("b1", "taglib failed"))

	got := store.stored(t, "b1")
	require.NotNil(t, got.QuarantinedAt, "book must be stamped quarantined")
	require.NotEqual(t, src, got.FilePath, "book path must move under .failed")
	if got.Duration == nil || *got.Duration != duration {
		t.Fatalf("lost update: Duration written by a concurrent writer between the quarantine read and its write was reverted: got %v, want %d", got.Duration, duration)
	}
}
