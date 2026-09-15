// file: internal/versions/lost_update_test.go
// version: 1.0.0
// guid: 0c0c0ae9-fe36-4aca-b0d2-9091ac881ca5
// last-edited: 2026-09-14

package versions

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// lostUpdateStore is a stateful book store: GetBookByID returns a copy,
// UpdateBook stores a copy of what it is handed, and ModifyBook mutates the
// stored row in place under the store lock, as PebbleStore does under the
// book's write stripe. concurrentWrite plays a second writer that commits
// Duration on the stored row once, just before the caller's first read or
// locked write -- the window a GetBookByID -> move files -> UpdateBook round
// trip cannot see. Versions and file rows live in the embedded MockStore's
// hooks so RunVersionSwap can run end to end.
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

// A concurrent writer's Duration must survive a version swap: the file moves
// run between the book read and the book write, and the write sets only
// file_path on the stored row.
func TestVersionSwap_KeepsConcurrentWrite(t *testing.T) {
	bookDir := t.TempDir()
	activePath := filepath.Join(bookDir, "Book.m4b")
	writeTestFile(t, activePath, "active-content-v1")
	altPath := filepath.Join(bookDir, ".versions", "v-alt", "Book.mp3")
	writeTestFile(t, altPath, "alt-content-mp3")

	store := newLostUpdateStore(&database.Book{ID: "b1", Title: "Book", FilePath: activePath, Format: "m4b"})
	duration := 4242
	store.concurrentWrite = func(stored *database.Book) { stored.Duration = &duration }

	var mu sync.Mutex
	versions := map[string]*database.BookVersion{
		"v-active": {ID: "v-active", BookID: "b1", Status: database.BookVersionStatusActive, Format: "m4b"},
		"v-alt":    {ID: "v-alt", BookID: "b1", Status: database.BookVersionStatusAlt, Format: "mp3"},
	}
	files := map[string]*database.BookFile{
		"f1": {ID: "f1", BookID: "b1", VersionID: "v-active", FilePath: activePath, Format: "m4b"},
		"f2": {ID: "f2", BookID: "b1", VersionID: "v-alt", FilePath: altPath, Format: "mp3"},
	}
	store.GetBookVersionFunc = func(id string) (*database.BookVersion, error) {
		mu.Lock()
		defer mu.Unlock()
		if v := versions[id]; v != nil {
			cp := *v
			return &cp, nil
		}
		return nil, nil
	}
	store.UpdateBookVersionFunc = func(v *database.BookVersion) error {
		mu.Lock()
		defer mu.Unlock()
		cp := *v
		versions[v.ID] = &cp
		return nil
	}
	store.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
		mu.Lock()
		defer mu.Unlock()
		var out []database.BookFile
		for _, f := range files {
			if f.BookID == bookID {
				out = append(out, *f)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out, nil
	}
	store.UpdateBookFileFunc = func(id string, f *database.BookFile) error {
		mu.Lock()
		defer mu.Unlock()
		cp := *f
		files[id] = &cp
		return nil
	}

	err := RunVersionSwap(context.Background(), store, VersionSwapParams{
		BookID: "b1", FromVersionID: "v-active", ToVersionID: "v-alt",
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("swap: %v", err)
	}

	got := store.stored(t, "b1")
	if want := filepath.Join(bookDir, "Book.mp3"); got.FilePath != want {
		t.Fatalf("FilePath = %q, want %q", got.FilePath, want)
	}
	if got.Duration == nil || *got.Duration != duration {
		t.Fatalf("lost update: Duration written by a concurrent writer between the swap's read and its write was reverted: got %v, want %d", got.Duration, duration)
	}
}
