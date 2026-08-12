// file: internal/server/handlers/metadata_cache_test.go
// version: 1.0.0
// guid: 6b1c0a94-2f7d-4c8e-9a15-3d0e7b28c4f1
// last-edited: 2026-08-11

// Tests for BatchApplyFromCache's file-I/O behaviour.
//
// These exist because the endpoint the Metadata Review screen calls updated
// only the database: it never scheduled ApplyMetadataFileIO /
// WriteBackMetadataForBook, so applied metadata never reached the audio files
// and no cover art was embedded. A test that only asserted "Submit was called"
// would not have caught it, and neither would one that asserted with
// mock.Anything — so every assertion below is on a CAPTURED argument value,
// and the captured pool closure is actually INVOKED so the work inside it is
// exercised rather than merely scheduled.

package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// batchApplyCtx builds a POST gin context carrying the given JSON body.
func batchApplyCtx(body string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/audiobooks/metadata/batch-apply-cached", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	return c, w
}

// cachedCandidateFor returns a cache entry whose single candidate carries the
// given title, so the decode step has something real to decode.
func cachedCandidateFor(t *testing.T, title string) *metafetch.MetadataCandidate {
	t.Helper()
	return &metafetch.MetadataCandidate{Title: title}
}

func cacheEntry(t *testing.T, title string) *metafetch.MetadataCandidateCache {
	t.Helper()
	raw, err := json.Marshal(cachedCandidateFor(t, title))
	require.NoError(t, err)
	return &metafetch.MetadataCandidateCache{Candidates: []json.RawMessage{raw}}
}

// recordedPoolCall is one captured pool.Submit invocation.
type recordedPoolCall struct {
	bookID string
	fn     func()
}

// TestBatchApplyFromCache_SubmitsFileIOForEachAppliedBook is the regression
// test for the defect: applying from the review screen must schedule the audio
// tag / cover-art work for every book it applies, with the default (absent)
// write_back treated as ON.
func TestBatchApplyFromCache_SubmitsFileIOForEachAppliedBook(t *testing.T) {
	store := handlersmocks.NewMockMetadataCacheBookStore(t)
	svc := handlersmocks.NewMockMetadataCacheFetchService(t)
	pool := handlersmocks.NewMockFileIOPool(t)
	batcher := handlersmocks.NewMockWriteBackEnqueuer(t)

	bookIDs := []string{"book-1", "book-2"}

	for _, id := range bookIDs {
		svc.EXPECT().GetCachedCandidates(id).Return(cacheEntry(t, "Title "+id), true, nil).Once()
		svc.EXPECT().ApplyMetadataCandidate(id, mock.AnythingOfType("metafetch.MetadataCandidate"), []string(nil)).
			Return(&metafetch.FetchMetadataResponse{}, nil).Once()
		svc.EXPECT().InvalidateCachedCandidates(id).Return(nil).Once()
		batcher.EXPECT().Enqueue(id).Once()
	}

	// Capture the real arguments. Asserting only that Submit happened is what
	// let a previous bug on this exact endpoint survive a passing test.
	var mu sync.Mutex
	var submitted []recordedPoolCall
	pool.EXPECT().Submit(mock.AnythingOfType("string"), mock.AnythingOfType("func()")).
		Run(func(bookID string, fn func()) {
			mu.Lock()
			submitted = append(submitted, recordedPoolCall{bookID: bookID, fn: fn})
			mu.Unlock()
		}).Times(len(bookIDs))

	// The pool closure must actually DO the file work when run, so assert on
	// what it calls, not merely that it exists.
	var fileIOCalls []string
	var writeBackCalls []string
	for _, id := range bookIDs {
		svc.EXPECT().ApplyMetadataFileIO(id).Run(func(gotID string) {
			mu.Lock()
			fileIOCalls = append(fileIOCalls, gotID)
			mu.Unlock()
		}).Once()
		svc.EXPECT().WriteBackMetadataForBook(id).Run(func(gotID string, _ ...[]string) {
			mu.Lock()
			writeBackCalls = append(writeBackCalls, gotID)
			mu.Unlock()
		}).Return(3, nil).Once()
	}

	h := handlers.NewMetadataCacheHandler(store, svc, batcher, pool)
	c, w := batchApplyCtx(`{"book_ids":["book-1","book-2"]}`)
	h.BatchApplyFromCache(c)

	require.Equal(t, http.StatusOK, w.Code)

	// Exactly one submission per applied book, carrying that book's ID.
	require.Len(t, submitted, 2)
	gotIDs := []string{submitted[0].bookID, submitted[1].bookID}
	assert.ElementsMatch(t, bookIDs, gotIDs, "pool.Submit must receive each applied book ID")

	// Run the captured closures: a mockery mock does NOT invoke fn on its own,
	// so without this the test would prove scheduling and nothing else.
	for _, call := range submitted {
		require.NotNil(t, call.fn, "captured pool closure must not be nil")
		call.fn()
	}

	assert.ElementsMatch(t, bookIDs, fileIOCalls, "ApplyMetadataFileIO must run for each applied book")
	assert.ElementsMatch(t, bookIDs, writeBackCalls, "WriteBackMetadataForBook must run for each applied book")

	// Response reports the truth, per book.
	var resp struct {
		Data struct {
			Applied    int      `json:"applied"`
			AppliedIDs []string `json:"applied_ids"`
			Skipped    []struct {
				BookID string `json:"book_id"`
				Reason string `json:"reason"`
			} `json:"skipped"`
			Requested int  `json:"requested"`
			WriteBack bool `json:"write_back"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 2, resp.Data.Applied)
	assert.ElementsMatch(t, bookIDs, resp.Data.AppliedIDs)
	assert.Empty(t, resp.Data.Skipped)
	assert.Equal(t, 2, resp.Data.Requested)
	assert.True(t, resp.Data.WriteBack, "write_back defaults to true when the field is absent")
}

// TestBatchApplyFromCache_WriteBackFalseSuppressesFileIO pins the opt-out:
// write_back:false must apply to the database and schedule NO file work.
func TestBatchApplyFromCache_WriteBackFalseSuppressesFileIO(t *testing.T) {
	store := handlersmocks.NewMockMetadataCacheBookStore(t)
	svc := handlersmocks.NewMockMetadataCacheFetchService(t)
	pool := handlersmocks.NewMockFileIOPool(t)
	batcher := handlersmocks.NewMockWriteBackEnqueuer(t)

	svc.EXPECT().GetCachedCandidates("book-1").Return(cacheEntry(t, "Title"), true, nil).Once()
	svc.EXPECT().ApplyMetadataCandidate("book-1", mock.AnythingOfType("metafetch.MetadataCandidate"), []string(nil)).
		Return(&metafetch.FetchMetadataResponse{}, nil).Once()
	svc.EXPECT().InvalidateCachedCandidates("book-1").Return(nil).Once()

	// No pool.Submit, no batcher.Enqueue, no ApplyMetadataFileIO and no
	// WriteBackMetadataForBook expectations are registered — mockery fails the
	// test on any unexpected call, which is the assertion.

	h := handlers.NewMetadataCacheHandler(store, svc, batcher, pool)
	c, w := batchApplyCtx(`{"book_ids":["book-1"],"write_back":false}`)
	h.BatchApplyFromCache(c)

	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data struct {
			Applied   int  `json:"applied"`
			WriteBack bool `json:"write_back"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 1, resp.Data.Applied, "the DB apply still happens with write_back:false")
	assert.False(t, resp.Data.WriteBack)
}

// TestBatchApplyFromCache_ReportsSkippedBooks covers the second defect: a book
// with no cached candidates was counted as nothing at all, and the UI reported
// the REQUESTED count as applied. The response must name the skip and its
// reason so the caller can leave that row in the queue.
func TestBatchApplyFromCache_ReportsSkippedBooks(t *testing.T) {
	store := handlersmocks.NewMockMetadataCacheBookStore(t)
	svc := handlersmocks.NewMockMetadataCacheFetchService(t)
	pool := handlersmocks.NewMockFileIOPool(t)
	batcher := handlersmocks.NewMockWriteBackEnqueuer(t)

	// book-1 applies; book-2 has an empty cache entry (the prod case: the
	// entry expired between the review list loading and APPLY being clicked).
	svc.EXPECT().GetCachedCandidates("book-1").Return(cacheEntry(t, "Title"), true, nil).Once()
	svc.EXPECT().ApplyMetadataCandidate("book-1", mock.AnythingOfType("metafetch.MetadataCandidate"), []string(nil)).
		Return(&metafetch.FetchMetadataResponse{}, nil).Once()
	svc.EXPECT().InvalidateCachedCandidates("book-1").Return(nil).Once()
	batcher.EXPECT().Enqueue("book-1").Once()
	svc.EXPECT().ApplyMetadataFileIO("book-1").Once()
	svc.EXPECT().WriteBackMetadataForBook("book-1").Return(1, nil).Once()

	svc.EXPECT().GetCachedCandidates("book-2").
		Return(&metafetch.MetadataCandidateCache{}, false, nil).Once()

	var submitted []recordedPoolCall
	pool.EXPECT().Submit(mock.AnythingOfType("string"), mock.AnythingOfType("func()")).
		Run(func(bookID string, fn func()) {
			submitted = append(submitted, recordedPoolCall{bookID: bookID, fn: fn})
		}).Once()

	h := handlers.NewMetadataCacheHandler(store, svc, batcher, pool)
	c, w := batchApplyCtx(`{"book_ids":["book-1","book-2"]}`)
	h.BatchApplyFromCache(c)

	require.Equal(t, http.StatusOK, w.Code)

	// Only the applied book got file work — assert on the captured ID, not that
	// "a" submission happened.
	require.Len(t, submitted, 1)
	assert.Equal(t, "book-1", submitted[0].bookID)
	submitted[0].fn()

	var resp struct {
		Data struct {
			Applied    int      `json:"applied"`
			AppliedIDs []string `json:"applied_ids"`
			Skipped    []struct {
				BookID string `json:"book_id"`
				Reason string `json:"reason"`
			} `json:"skipped"`
			Requested int `json:"requested"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 1, resp.Data.Applied, "applied must be the count actually applied, not requested")
	assert.Equal(t, []string{"book-1"}, resp.Data.AppliedIDs)
	assert.Equal(t, 2, resp.Data.Requested)
	require.Len(t, resp.Data.Skipped, 1)
	assert.Equal(t, "book-2", resp.Data.Skipped[0].BookID)
	assert.Equal(t, "no_cached_candidates", resp.Data.Skipped[0].Reason)
}

// TestBatchApplyFromCache_NilPoolStillApplies pins the nil-pool path: the DB
// apply must still happen and the handler must not panic. The warn log this
// emits is the point — a silent skip here is the defect being fixed.
func TestBatchApplyFromCache_NilPoolStillApplies(t *testing.T) {
	store := handlersmocks.NewMockMetadataCacheBookStore(t)
	svc := handlersmocks.NewMockMetadataCacheFetchService(t)
	batcher := handlersmocks.NewMockWriteBackEnqueuer(t)

	svc.EXPECT().GetCachedCandidates("book-1").Return(cacheEntry(t, "Title"), true, nil).Once()
	svc.EXPECT().ApplyMetadataCandidate("book-1", mock.AnythingOfType("metafetch.MetadataCandidate"), []string(nil)).
		Return(&metafetch.FetchMetadataResponse{}, nil).Once()
	svc.EXPECT().InvalidateCachedCandidates("book-1").Return(nil).Once()
	batcher.EXPECT().Enqueue("book-1").Once()

	h := handlers.NewMetadataCacheHandler(store, svc, batcher, nil)
	c, w := batchApplyCtx(`{"book_ids":["book-1"]}`)
	h.BatchApplyFromCache(c)

	require.Equal(t, http.StatusOK, w.Code)
}
