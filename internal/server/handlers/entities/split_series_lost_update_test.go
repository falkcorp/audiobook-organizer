// file: internal/server/handlers/entities/split_series_lost_update_test.go
// version: 1.0.0
// guid: e9a3c5d1-7b2f-4e8a-b6c4-1f0d9a8e7c53
// last-edited: 2026-09-14

package entities_test

import (
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/entities"
	entitiesmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/entities/mocks"
)

// lostUpdateBookStore is a stateful book store for the lost-update race,
// modelled on internal/merge/soft_delete_lost_update_test.go: GetBookByID
// returns a copy of the stored row, UpdateBook stores a copy, and ModifyBook
// is a locked read-modify-write. concurrent is the OTHER writer: it lands on
// the stored row right after every raw GetBookByID and right before every
// ModifyBook takes the row lock. The rest of EntitiesStore is the generated
// mock, so an unexpected store call still fails the test.
type lostUpdateBookStore struct {
	*entitiesmocks.MockEntitiesStore
	mu         sync.Mutex
	rows       map[string]*database.Book
	concurrent func(*database.Book)
}

func newLostUpdateBookStore(m *entitiesmocks.MockEntitiesStore, books ...*database.Book) *lostUpdateBookStore {
	s := &lostUpdateBookStore{MockEntitiesStore: m, rows: map[string]*database.Book{}}
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

// TestSplitSeries_DoesNotRevertConcurrentColumns pins the lost-update fix on
// POST /series/:id/split (audit A1#15): the handler must write only SeriesID,
// so a Duration another writer commits between its read and its write
// survives. Against the old GetBookByID -> UpdateBook(whole row) it fails
// with "Duration reverted".
func TestSplitSeries_DoesNotRevertConcurrentColumns(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := entitiesmocks.NewMockEntitiesStore(t)
	oldSeries := 5
	store := newLostUpdateBookStore(m, &database.Book{ID: "b1", Title: "Book", SeriesID: &oldSeries})
	store.concurrent = func(b *database.Book) {
		d := 4242
		b.Duration = &d
	}
	m.EXPECT().GetSeriesByID(5).Return(&database.Series{ID: 5, Name: "S"}, nil)
	m.EXPECT().CreateSeries("S (Split)", (*int)(nil)).Return(&database.Series{ID: 6, Name: "S (Split)"}, nil)

	h := entities.New(store,
		entitiesmocks.NewMockWorkService(t),
		entitiesmocks.NewMockAuthorSeriesService(t),
		entitiesmocks.NewMockOperationsRegistry(t),
		cache.NewWithLimit[*audiobooks.AuthorWithCountListResponse]("authors-lu", time.Hour, 1),
		cache.NewWithLimit[*audiobooks.SeriesWithCountsResponse]("series-lu", time.Hour, 1),
		cache.NewWithLimit[gin.H]("dedup-lu", time.Hour, 16),
		nil)

	c, w := newCtx(http.MethodPost, "/series/5/split", `{"book_ids":["b1"]}`, idParam("5"))
	h.SplitSeries(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"books_moved":1`)
	got := store.stored("b1")
	if got == nil || got.SeriesID == nil || *got.SeriesID != 6 {
		t.Fatalf("book was not moved to the new series: %+v", got)
	}
	if got.Duration == nil || *got.Duration != 4242 {
		t.Fatalf("Duration reverted by the series-split write: got %v, want 4242 (the concurrent writer's value)", got.Duration)
	}
}
