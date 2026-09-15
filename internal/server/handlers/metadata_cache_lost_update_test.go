// file: internal/server/handlers/metadata_cache_lost_update_test.go
// version: 1.0.0
// guid: 6f1c2a84-9d3e-4b7a-a5c1-2e8f0d4b9c71
// last-edited: 2026-09-14

package handlers_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
)

// lostUpdateCacheStore is a stateful MetadataCacheBookStore for the lost-update
// race, modelled on internal/merge/soft_delete_lost_update_test.go:
// GetBookByID returns a copy of the stored row, UpdateBook stores a copy, and
// ModifyBook is a locked read-modify-write. concurrent is the OTHER writer: it
// lands on the stored row right after every raw GetBookByID and right before
// every ModifyBook takes the row lock, which is where a real concurrent
// UpdateBook of another column lands in production.
type lostUpdateCacheStore struct {
	mu         sync.Mutex
	rows       map[string]*database.Book
	concurrent func(*database.Book)
}

func newLostUpdateCacheStore(books ...*database.Book) *lostUpdateCacheStore {
	s := &lostUpdateCacheStore{rows: map[string]*database.Book{}}
	for _, b := range books {
		cp := *b
		s.rows[b.ID] = &cp
	}
	return s
}

func (s *lostUpdateCacheStore) landConcurrentLocked(id string) {
	if b := s.rows[id]; b != nil && s.concurrent != nil {
		s.concurrent(b)
	}
}

func (s *lostUpdateCacheStore) GetBookByID(id string) (*database.Book, error) {
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

func (s *lostUpdateCacheStore) GetBooksByIDs(ids []string) ([]database.Book, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]database.Book, 0, len(ids))
	for _, id := range ids {
		if b := s.rows[id]; b != nil {
			out = append(out, *b)
		}
	}
	return out, nil
}

func (s *lostUpdateCacheStore) GetBookFiles(string) ([]database.BookFile, error) { return nil, nil }

func (s *lostUpdateCacheStore) UpdateBook(id string, b *database.Book) (*database.Book, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *b
	s.rows[id] = &cp
	return &cp, nil
}

func (s *lostUpdateCacheStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
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

func (s *lostUpdateCacheStore) stored(id string) *database.Book {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows[id]
}

// TestClearMetadataNoMatch_DoesNotRevertConcurrentColumns pins the lost-update
// fix on POST /audiobooks/:id/clear-no-match (audit A1#15): the handler must
// write only MetadataReviewStatus, so a Duration another writer commits
// between its read and its write survives. Against the old
// GetBookByID -> UpdateBook(whole row) it fails with "Duration reverted".
func TestClearMetadataNoMatch_DoesNotRevertConcurrentColumns(t *testing.T) {
	gin.SetMode(gin.TestMode)
	noMatch := "no_match"
	store := newLostUpdateCacheStore(&database.Book{ID: "b1", Title: "Book", MetadataReviewStatus: &noMatch})
	store.concurrent = func(b *database.Book) {
		d := 4242
		b.Duration = &d
	}
	h := handlers.NewMetadataCacheHandler(store, handlersmocks.NewMockMetadataCacheFetchService(t), nil, nil, nil, nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/audiobooks/b1/clear-no-match", nil)
	c.Params = gin.Params{{Key: "id", Value: "b1"}}
	h.ClearMetadataNoMatch(c)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	got := store.stored("b1")
	if got == nil || got.MetadataReviewStatus != nil {
		t.Fatalf("review status was not cleared: %+v", got)
	}
	if got.Duration == nil || *got.Duration != 4242 {
		t.Fatalf("Duration reverted by the clear-no-match write: got %v, want 4242 (the concurrent writer's value)", got.Duration)
	}
}

// A missing book is a 404, not a silent success: ModifyBook's (nil, nil).
func TestClearMetadataNoMatch_MissingBookIs404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := newLostUpdateCacheStore()
	h := handlers.NewMetadataCacheHandler(store, handlersmocks.NewMockMetadataCacheFetchService(t), nil, nil, nil, nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/audiobooks/gone/clear-no-match", nil)
	c.Params = gin.Params{{Key: "id", Value: "gone"}}
	h.ClearMetadataNoMatch(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", w.Code, w.Body.String())
	}
}
