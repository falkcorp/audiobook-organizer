// file: internal/server/handlers/audiobooks/reconcile_lost_update_test.go
// version: 1.0.0
// guid: b4d7e2c9-51a3-4f6e-9c08-7d2a3e5f1b64
// last-edited: 2026-09-14

package audiobookshandler_test

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	audiobookshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/audiobooks"
	audiobooksmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/audiobooks/mocks"
)

// lostUpdateBookStore is a stateful book store for the lost-update race,
// modelled on internal/merge/soft_delete_lost_update_test.go: GetBookByID
// returns a copy of the stored row, UpdateBook stores a copy, and ModifyBook
// is a locked read-modify-write. concurrent is the OTHER writer: it lands on
// the stored row right after every raw GetBookByID and right before every
// ModifyBook takes the row lock. The rest of AudiobooksStore is the generated
// mock, so an unexpected store call still fails the test.
type lostUpdateBookStore struct {
	*audiobooksmocks.MockAudiobooksStore
	mu         sync.Mutex
	rows       map[string]*database.Book
	files      map[string][]database.BookFile
	concurrent func(*database.Book)
}

func newLostUpdateBookStore(m *audiobooksmocks.MockAudiobooksStore, books ...*database.Book) *lostUpdateBookStore {
	s := &lostUpdateBookStore{MockAudiobooksStore: m, rows: map[string]*database.Book{}, files: map[string][]database.BookFile{}}
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

func (s *lostUpdateBookStore) GetBookFiles(bookID string) ([]database.BookFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]database.BookFile(nil), s.files[bookID]...), nil
}

func (s *lostUpdateBookStore) UpdateBookFile(id string, f *database.BookFile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for bookID, fs := range s.files {
		for i := range fs {
			if fs[i].ID == id {
				s.files[bookID][i] = *f
			}
		}
	}
	return nil
}

func (s *lostUpdateBookStore) stored(id string) *database.Book {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows[id]
}

// TestReconcileAudiobookFiles_DoesNotRevertConcurrentColumns pins the
// lost-update fix on POST /audiobooks/:id/reconcile-files (audit A1#15): the
// handler must write only FileSize, so a Duration another writer commits
// between its read and its write survives. Against the old
// GetBookByID -> stat files -> UpdateBook(whole row) it fails with
// "Duration reverted".
func TestReconcileAudiobookFiles_DoesNotRevertConcurrentColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "01.m4b")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	var store *lostUpdateBookStore
	h, _ := newHandlerWithStore(t, func(m *audiobooksmocks.MockAudiobooksStore) audiobookshandler.AudiobooksStore {
		store = newLostUpdateBookStore(m, &database.Book{ID: "b1", Title: "Book"})
		store.files["b1"] = []database.BookFile{{ID: "f1", BookID: "b1", FilePath: path, FileSize: 0}}
		store.concurrent = func(b *database.Book) {
			d := 4242
			b.Duration = &d
		}
		return store
	})

	c, w := newCtx("POST", "/audiobooks/b1/reconcile-files", nil, p("id", "b1"))
	h.ReconcileAudiobookFiles(c)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	got := store.stored("b1")
	if got == nil || got.FileSize == nil || *got.FileSize != 10 {
		t.Fatalf("FileSize was not reconciled to 10: %+v", got)
	}
	if got.Duration == nil || *got.Duration != 4242 {
		t.Fatalf("Duration reverted by the reconcile write: got %v, want 4242 (the concurrent writer's value)", got.Duration)
	}
}
