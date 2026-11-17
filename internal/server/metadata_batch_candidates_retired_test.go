// file: internal/server/metadata_batch_candidates_retired_test.go
// version: 1.0.0
// guid: 8b1f4d27-3c60-4e95-b2a8-1d7e9f0c5a36
// last-edited: 2026-11-17

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
)

// The batch candidate-fetch handler leaves retired books out of every fetch it
// starts and says how many in `retired_skipped`, on every path that answers:
// the started op, the "nothing left" paths, and the all-retired selection.

type batchFetchReply struct {
	Code int
	Data batchFetchStartResponse
	Raw  map[string]any
}

func postBatchFetch(t *testing.T, s *Server, body string) batchFetchReply {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/metadata/batch-fetch-candidates", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	s.handleBatchFetchCandidates(c)
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env), w.Body.String())
	reply := batchFetchReply{Code: w.Code}
	require.NoError(t, json.Unmarshal(env.Data, &reply.Data), w.Body.String())
	require.NoError(t, json.Unmarshal(env.Data, &reply.Raw), w.Body.String())
	return reply
}

func idsJSON(t *testing.T, ids ...string) string {
	t.Helper()
	b, err := json.Marshal(ids)
	require.NoError(t, err)
	return string(b)
}

// Started: one live book and both retired shapes. The op fetches the live book
// only, and the response counts the two retired ones apart from `skipped`.
func TestRetiredFetch_HandlerReportsRetiredSkippedOnStartedOp(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	quietFetchService(s)
	fx := newRetiredServerFixture(t, s)
	reply := postBatchFetch(t, s, `{"book_ids":`+idsJSON(t, fx.live, fx.retired["soft_deleted"], fx.retired["merged_into"])+`}`)

	assert.Equal(t, http.StatusAccepted, reply.Code)
	assert.NotEmpty(t, reply.Data.OperationID)
	assert.Equal(t, 1, reply.Data.BookCount, "only the live book is fetched")
	assert.Equal(t, 1, reply.Data.TotalBooks)
	assert.Equal(t, 2, reply.Data.RetiredSkipped)
	assert.Equal(t, 0, reply.Data.Skipped, "a retired book is not an already-being-fetched book")
}

// Every retired: a 200 with no operation, the count of retired books, and a
// message that names why. Nothing is enqueued.
func TestRetiredFetch_HandlerAllRetiredAnswers200WithNoOperation(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	quietFetchService(s)
	fx := newRetiredServerFixture(t, s)
	reply := postBatchFetch(t, s, `{"book_ids":`+idsJSON(t, fx.retired["soft_deleted"], fx.retired["merged_into"])+`}`)

	assert.Equal(t, http.StatusOK, reply.Code)
	assert.Empty(t, reply.Data.OperationID, "nothing may be enqueued")
	assert.Equal(t, 0, reply.Data.BookCount)
	assert.Equal(t, 2, reply.Data.RetiredSkipped)
	assert.Contains(t, reply.Data.Message, "all 2 selected books are retired")
	// One shape on every path: the keys a client reads are all present.
	for _, k := range []string{"message", "operation_id", "total_books", "book_count", "skipped", "retired_skipped"} {
		assert.Contains(t, reply.Raw, k, "key %q missing from the all-retired reply", k)
	}
}

// The single-book "Search again" (marked interactive, one explicit id) is the
// owner working on that book by hand and is not filtered, so a retired book
// searched alone still starts an op. Without the mark it is an ordinary
// one-book selection and is dropped.
func TestRetiredFetch_HandlerSingleBookSearchAgainIsNotFiltered(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	quietFetchService(s)
	fx := newRetiredServerFixture(t, s)
	reply := postBatchFetch(t, s, `{"interactive":true,"book_ids":`+idsJSON(t, fx.retired["merged_into"])+`}`)

	assert.Equal(t, http.StatusAccepted, reply.Code)
	assert.Equal(t, 1, reply.Data.BookCount)
	assert.Equal(t, 0, reply.Data.RetiredSkipped)

}

// Without the interactive mark the same request is an ordinary one-book
// selection: the retired book is dropped and nothing is enqueued. A separate
// test, not a second server in the one above, because a server's op worker
// reads package-level fetch settings that a second setupTestServer rewrites.
func TestRetiredFetch_HandlerOneBookWithoutInteractiveMarkIsDropped(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	quietFetchService(s)
	fx := newRetiredServerFixture(t, s)
	plain := postBatchFetch(t, s, `{"book_ids":`+idsJSON(t, fx.retired["merged_into"])+`}`)
	assert.Equal(t, http.StatusOK, plain.Code)
	assert.Empty(t, plain.Data.OperationID)
	assert.Equal(t, 1, plain.Data.RetiredSkipped)
}

// OnlyUnmatched: when the live books left after the retired ones are dropped
// all have a matched candidate already, the early "nothing to do" reply still
// says how many retired books were left out.
func TestRetiredFetch_HandlerOnlyUnmatchedEarlyReturnCountsRetired(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	quietFetchService(s)
	fx := newRetiredServerFixture(t, s)

	// A finished candidate-fetch run whose ledger says the live book matched.
	store := s.storeForWiring()
	now := time.Now().UTC()
	require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
		ID: "op-matched-before", DefID: metabatch.CandidateFetchDefID, Plugin: "metadata", Status: "completed",
		Params: `{}`, QueuedAt: now, CompletedAt: &now,
	}))
	require.NoError(t, store.CreateOperationResult(&database.OperationResult{
		OperationID: "op-matched-before", BookID: fx.live, Status: "matched", CreatedAt: now,
	}))

	reply := postBatchFetch(t, s, `{"only_unmatched":true,"book_ids":`+idsJSON(t, fx.live, fx.retired["soft_deleted"], fx.retired["merged_into"])+`}`)

	assert.Equal(t, http.StatusOK, reply.Code)
	assert.Empty(t, reply.Data.OperationID)
	assert.Contains(t, reply.Data.Message, "already have matched candidates")
	assert.Equal(t, 2, reply.Data.RetiredSkipped, "the early return must still report the retired books")
}
