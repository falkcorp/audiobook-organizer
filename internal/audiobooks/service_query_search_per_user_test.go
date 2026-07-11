// file: internal/audiobooks/service_query_search_per_user_test.go
// version: 1.1.0
// guid: e3a7c1d9-4b2f-4a68-9c1e-5f8a2d3b7c40
// last-edited: 2026-07-11

// Tests for the searchWithBleve per-user DSL filter fix (INIT-4 T2).
// read_status / progress_pct / last_played filters are peeled off by
// search.Translate and were previously discarded with `_`; these
// tests pin the restored behavior: over-fetch -> per-user match ->
// offset/limit slicing, fail-open state-read errors, window-
// exhaustion warning, and the DisablePerUserSearchFilters kill
// switch.

package audiobooks

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// buildSearchTestIndex opens a throwaway on-disk Bleve index and seeds
// it with docs all sharing Author "sanderson" so a single DSL clause
// (author:sanderson) selects every seeded book, letting the per-user
// clause do all the narrowing in each test below.
func buildSearchTestIndex(t *testing.T, bookIDs ...string) *search.BleveIndex {
	t.Helper()
	idx, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatalf("open bleve: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	docs := make([]search.BookDocument, 0, len(bookIDs))
	for _, id := range bookIDs {
		docs = append(docs, search.BookDocument{BookID: id, Title: id, Author: "sanderson"})
	}
	if err := idx.IndexBookBatch(docs); err != nil {
		t.Fatalf("index batch: %v", err)
	}
	return idx
}

// captureWarnLog redirects the default slog logger to a buffer for
// the duration of the test and restores it on cleanup.
func captureWarnLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	prev := slog.Default()
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestSearchWithBleveAppliesReadStatusFilter is acceptance case (a):
// read_status:finished returns only the finished book.
func TestSearchWithBleveAppliesReadStatusFilter(t *testing.T) {
	idx := buildSearchTestIndex(t, "b1", "b2", "b3")
	mockStore := mocks.NewMockStore(t)
	mockStore.EXPECT().GetBooksByIDs(mock.Anything).RunAndReturn(func(ids []string) ([]database.Book, error) {
		books := make([]database.Book, 0, len(ids))
		for _, id := range ids {
			books = append(books, database.Book{ID: id})
		}
		return books, nil
	})
	mockStore.EXPECT().GetUserBookState("alice", "b1").
		Return(&database.UserBookState{Status: database.UserBookStatusInProgress}, nil)
	mockStore.EXPECT().GetUserBookState("alice", "b2").
		Return(&database.UserBookState{Status: database.UserBookStatusFinished}, nil)
	mockStore.EXPECT().GetUserBookState("alice", "b3").Return(nil, nil)

	svc := NewAudiobookService(mockStore)
	svc.SetSearchIndex(idx)

	got, err := svc.GetAudiobooks(context.Background(), 50, 0, "author:sanderson read_status:finished", nil, nil, ListFilters{UserID: "alice"})
	assert.NoError(t, err)
	if assert.Len(t, got, 1) {
		assert.Equal(t, "b2", got[0].ID)
	}
}

// TestSearchWithBleveNoFilterQueryUnchanged is the positive control for
// (c): a plain query with no per-user DSL clause returns Bleve's hits
// unchanged (fast path untouched).
func TestSearchWithBleveNoFilterQueryUnchanged(t *testing.T) {
	idx := buildSearchTestIndex(t, "b1", "b2", "b3")
	mockStore := mocks.NewMockStore(t)
	mockStore.EXPECT().GetBooksByIDs(mock.Anything).RunAndReturn(func(ids []string) ([]database.Book, error) {
		books := make([]database.Book, 0, len(ids))
		for _, id := range ids {
			books = append(books, database.Book{ID: id})
		}
		return books, nil
	})

	svc := NewAudiobookService(mockStore)
	svc.SetSearchIndex(idx)

	got, err := svc.GetAudiobooks(context.Background(), 50, 0, "author:sanderson", nil, nil, ListFilters{UserID: "alice"})
	assert.NoError(t, err)
	assert.Len(t, got, 3, "no per-user clause -> all 3 Bleve matches returned")
}

// TestSearchWithBlevePaginationAfterFilter is acceptance case (b):
// pagination must slice AFTER the per-user filter pass, not before.
// Seeds 5 Bleve hits where 2 fail the per-user filter, leaving 3
// post-filter matches; limit=2 offset=2 must yield exactly 1 book.
// If pagination were (incorrectly) applied to the raw 5 pre-filter
// hits instead, limit=2 offset=2 would select 2 raw hits BEFORE
// filtering — which could yield 0, 1, or 2 books depending on which
// 2 of the 5 landed in that window, so this only discriminates
// correctly because the filter-passing books (b1, b3, b5) are
// deliberately not contiguous in seed order.
func TestSearchWithBlevePaginationAfterFilter(t *testing.T) {
	idx := buildSearchTestIndex(t, "b1", "b2", "b3", "b4", "b5")
	mockStore := mocks.NewMockStore(t)
	mockStore.EXPECT().GetBooksByIDs(mock.Anything).RunAndReturn(func(ids []string) ([]database.Book, error) {
		books := make([]database.Book, 0, len(ids))
		for _, id := range ids {
			books = append(books, database.Book{ID: id})
		}
		return books, nil
	})
	inProgress := &database.UserBookState{Status: database.UserBookStatusInProgress}
	finished := &database.UserBookState{Status: database.UserBookStatusFinished}
	mockStore.EXPECT().GetUserBookState("alice", "b1").Return(inProgress, nil)
	mockStore.EXPECT().GetUserBookState("alice", "b2").Return(finished, nil) // filtered out
	mockStore.EXPECT().GetUserBookState("alice", "b3").Return(inProgress, nil)
	mockStore.EXPECT().GetUserBookState("alice", "b4").Return(finished, nil) // filtered out
	mockStore.EXPECT().GetUserBookState("alice", "b5").Return(inProgress, nil)

	svc := NewAudiobookService(mockStore)
	svc.SetSearchIndex(idx)

	got, err := svc.GetAudiobooks(context.Background(), 2, 2, "author:sanderson read_status:in_progress", nil, nil, ListFilters{UserID: "alice"})
	assert.NoError(t, err)
	assert.Len(t, got, 1, "5 raw hits, 3 post-filter matches, limit=2 offset=2 -> 1 book")
}

// TestSearchWithBleveEmptyUserIDReturnsAllMatches is acceptance case
// (c): anti-over-suppression. Without a userID, per-user filters are
// skipped (not applied as an all-fail filter) so results aren't
// silently emptied.
func TestSearchWithBleveEmptyUserIDReturnsAllMatches(t *testing.T) {
	idx := buildSearchTestIndex(t, "b1", "b2", "b3")
	mockStore := mocks.NewMockStore(t)
	mockStore.EXPECT().GetBooksByIDs(mock.Anything).RunAndReturn(func(ids []string) ([]database.Book, error) {
		books := make([]database.Book, 0, len(ids))
		for _, id := range ids {
			books = append(books, database.Book{ID: id})
		}
		return books, nil
	})
	// No GetUserBookState expectation set — asserting the code path
	// never calls it when userID == "".

	buf := captureWarnLog(t)

	svc := NewAudiobookService(mockStore)
	svc.SetSearchIndex(idx)

	got, err := svc.GetAudiobooks(context.Background(), 50, 0, "author:sanderson read_status:finished", nil, nil, ListFilters{})
	assert.NoError(t, err)
	assert.Len(t, got, 3, "empty userID must never produce an empty page when matches exist")
	assert.Contains(t, buf.String(), "per-user filters dropped, no user context")
}

// TestSearchWithBleveStateErrorFailsOpen covers spec Decision 5 and
// the Testing table: a GetUserBookState error on one hit must NOT
// silently drop the row or fail the whole request. It's evaluated as
// the zero-value state (loudly, via a warn) and the request keeps
// serving. Uses a NEGATED filter so the zero-value evaluation is
// directly observable: an erroring state read must still be treated
// as unfinished, so `-read_status:finished` keeps it in the results.
func TestSearchWithBleveStateErrorFailsOpen(t *testing.T) {
	idx := buildSearchTestIndex(t, "b1", "b2")
	mockStore := mocks.NewMockStore(t)
	mockStore.EXPECT().GetBooksByIDs(mock.Anything).RunAndReturn(func(ids []string) ([]database.Book, error) {
		books := make([]database.Book, 0, len(ids))
		for _, id := range ids {
			books = append(books, database.Book{ID: id})
		}
		return books, nil
	})
	mockStore.EXPECT().GetUserBookState("alice", "b1").
		Return(nil, fmt.Errorf("pebble: i/o error"))
	mockStore.EXPECT().GetUserBookState("alice", "b2").
		Return(&database.UserBookState{Status: database.UserBookStatusFinished}, nil)

	buf := captureWarnLog(t)

	svc := NewAudiobookService(mockStore)
	svc.SetSearchIndex(idx)

	got, err := svc.GetAudiobooks(context.Background(), 50, 0, "author:sanderson -read_status:finished", nil, nil, ListFilters{UserID: "alice"})
	assert.NoError(t, err)
	if assert.Len(t, got, 1, "erroring state read -> zero-value eval keeps b1 (not finished); b2 genuinely finished is excluded") {
		assert.Equal(t, "b1", got[0].ID)
	}
	assert.Contains(t, buf.String(), "per-user state read failed; evaluating zero-value state")
	assert.Contains(t, buf.String(), "b1")
}

// TestSearchWithBleveWindowExhaustionWarns covers the Testing-table
// Decision 4 contract: when the pre-filter hit count reaches
// searchPostFilterWindow, a truncation warning is logged.
func TestSearchWithBleveWindowExhaustionWarns(t *testing.T) {
	ids := make([]string, searchPostFilterWindow+5)
	for i := range ids {
		ids[i] = fmt.Sprintf("book-%05d", i)
	}
	idx := buildSearchTestIndex(t, ids...)

	mockStore := mocks.NewMockStore(t)
	mockStore.EXPECT().GetBooksByIDs(mock.Anything).RunAndReturn(func(ids []string) ([]database.Book, error) {
		books := make([]database.Book, 0, len(ids))
		for _, id := range ids {
			books = append(books, database.Book{ID: id})
		}
		return books, nil
	})
	// Every state read returns nil,nil (no record) -> zero-value state.
	// Filter is negated read_status:finished so the zero-value state
	// always passes, keeping the assembled slice large without needing
	// per-ID stubbing.
	mockStore.EXPECT().GetUserBookState(mock.Anything, mock.Anything).Return(nil, nil)

	buf := captureWarnLog(t)

	svc := NewAudiobookService(mockStore)
	svc.SetSearchIndex(idx)

	_, err := svc.GetAudiobooks(context.Background(), 10, 0, "author:sanderson -read_status:finished", nil, nil, ListFilters{UserID: "alice"})
	assert.NoError(t, err)
	assert.Contains(t, buf.String(), "post-filter window exhausted; results beyond it are truncated")
}

// TestSearchWithBleveKillSwitchDrops covers spec Decision 11: with
// DisablePerUserSearchFilters=true, per-user filters are skipped
// (today's pre-fix drop-and-warn behavior) even though a userID is
// present.
func TestSearchWithBleveKillSwitchDrops(t *testing.T) {
	config.Mutate(func(c *config.Config) { c.DisablePerUserSearchFilters = true })
	t.Cleanup(func() {
		config.Mutate(func(c *config.Config) { c.DisablePerUserSearchFilters = false })
	})

	idx := buildSearchTestIndex(t, "b1", "b2", "b3")
	mockStore := mocks.NewMockStore(t)
	mockStore.EXPECT().GetBooksByIDs(mock.Anything).RunAndReturn(func(ids []string) ([]database.Book, error) {
		books := make([]database.Book, 0, len(ids))
		for _, id := range ids {
			books = append(books, database.Book{ID: id})
		}
		return books, nil
	})
	// No GetUserBookState expectation: the kill switch must prevent any
	// state reads at all — an unexpected call here fails the test.

	buf := captureWarnLog(t)

	svc := NewAudiobookService(mockStore)
	svc.SetSearchIndex(idx)

	got, err := svc.GetAudiobooks(context.Background(), 50, 0, "author:sanderson read_status:finished", nil, nil, ListFilters{UserID: "alice"})
	assert.NoError(t, err)
	assert.Len(t, got, 3, "kill switch on -> today's unfiltered behavior restored")
	assert.Contains(t, buf.String(), "per-user filters dropped, no user context")
	assert.Contains(t, buf.String(), "disabled_by_config")
}
