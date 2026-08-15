// file: internal/server/handlers/metadata_cache_test.go
// version: 2.0.0
// guid: 6b1c0a94-2f7d-4c8e-9a15-3d0e7b28c4f1
// last-edited: 2026-08-15

// Tests for BatchApplyFromCache's DISPATCH behaviour.
//
// This endpoint used to apply the whole batch inline, and the tests here
// asserted the file-side work (ApplyMetadataFileIO / WriteBackMetadataForBook)
// actually ran — because the original defect was that applied metadata never
// reached the audio files and nothing logged a failure.
//
// That work now happens in the metadata.batch-apply-cached op, so those
// assertions moved to internal/server/batch_apply_one_test.go, against the same
// extracted function the op calls. They were NOT deleted; testing them here
// would now only prove that an op was enqueued.
//
// What is left to test here is the dispatch contract, and it still matters:
// the params are the only thing carrying write_back to the op, and a dropped or
// defaulted flag would silently turn "database only" into "rewrite every file".
// So every assertion below is on a CAPTURED argument value, never mock.Anything.

package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
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

// capturingEnqueuer records exactly what was enqueued.
type capturingEnqueuer struct {
	defID  string
	params any
	calls  int
	opID   string
	err    error
}

func (e *capturingEnqueuer) EnqueueOp(_ context.Context, defID string, params any, _ ...opsregistry.EnqueueOption) (string, error) {
	e.calls++
	e.defID = defID
	e.params = params
	if e.err != nil {
		return "", e.err
	}
	if e.opID == "" {
		return "op-123", nil
	}
	return e.opID, nil
}

// paramsMap re-reads the captured params through JSON, so the assertion covers
// the shape the op will actually decode rather than the in-memory Go value.
func paramsMap(t *testing.T, v any) map[string]any {
	t.Helper()
	blob, err := json.Marshal(v)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(blob, &out))
	return out
}

func newDispatchHandler(t *testing.T, ops handlers.OpEnqueuer) *handlers.MetadataCacheHandler {
	t.Helper()
	store := handlersmocks.NewMockMetadataCacheBookStore(t)
	svc := handlersmocks.NewMockMetadataCacheFetchService(t)
	batcher := handlersmocks.NewMockWriteBackEnqueuer(t)
	return handlers.NewMetadataCacheHandler(store, svc, batcher, nil, ops)
}

// TestBatchApplyFromCache_EnqueuesOpWithBookIDs pins the core contract: the
// request returns immediately with an op id instead of holding the connection
// open for the whole batch. A 250-book apply measured 2m0s inline; the browser
// gave up first and reported "session expired, nothing was applied" while the
// server kept writing.
func TestBatchApplyFromCache_EnqueuesOpWithBookIDs(t *testing.T) {
	ops := &capturingEnqueuer{opID: "op-abc"}
	h := newDispatchHandler(t, ops)

	c, w := batchApplyCtx(`{"book_ids":["b1","b2","b3"]}`)
	h.BatchApplyFromCache(c)

	require.Equal(t, http.StatusAccepted, w.Code, "must return 202, not a completed result")
	require.Equal(t, 1, ops.calls)
	assert.Equal(t, "metadata.batch-apply-cached", ops.defID)

	p := paramsMap(t, ops.params)
	assert.Equal(t, []any{"b1", "b2", "b3"}, p["book_ids"])

	var body struct {
		Data struct {
			OpID      string `json:"op_id"`
			Requested int    `json:"requested"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "op-abc", body.Data.OpID)
	assert.Equal(t, 3, body.Data.Requested)
}

// TestBatchApplyFromCache_WriteBackDefaultsOn pins the default. An absent
// write_back means TRUE, matching the single-book path. Getting this wrong in
// the params silently turns a tag-writing apply into a database-only one.
func TestBatchApplyFromCache_WriteBackDefaultsOn(t *testing.T) {
	ops := &capturingEnqueuer{}
	h := newDispatchHandler(t, ops)

	c, _ := batchApplyCtx(`{"book_ids":["b1"]}`)
	h.BatchApplyFromCache(c)

	require.Equal(t, 1, ops.calls)
	assert.Equal(t, true, paramsMap(t, ops.params)["write_back"],
		"absent write_back must reach the op as true")
}

// TestBatchApplyFromCache_WriteBackFalseIsForwarded pins the opt-out surviving
// the hop into the op params. The inverse of the test above, and the reason
// both exist: a params bug that hard-coded true would pass the default test.
func TestBatchApplyFromCache_WriteBackFalseIsForwarded(t *testing.T) {
	ops := &capturingEnqueuer{}
	h := newDispatchHandler(t, ops)

	c, _ := batchApplyCtx(`{"book_ids":["b1"],"write_back":false}`)
	h.BatchApplyFromCache(c)

	require.Equal(t, 1, ops.calls)
	assert.Equal(t, false, paramsMap(t, ops.params)["write_back"],
		"write_back=false must reach the op as false")
}

// TestBatchApplyFromCache_NoRegistryIsAnError guards against a silent
// regression to inline applying. There is deliberately no inline fallback: one
// would be a second implementation that only ever ran when the registry was
// absent, so the tested path and the shipped path would diverge.
func TestBatchApplyFromCache_NoRegistryIsAnError(t *testing.T) {
	h := newDispatchHandler(t, nil)

	c, w := batchApplyCtx(`{"book_ids":["b1"]}`)
	h.BatchApplyFromCache(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// TestBatchApplyFromCache_EnqueueFailureIsReported keeps a failed enqueue from
// reading as a successful start. Returning 202 here would tell the UI to poll
// an op id that does not exist.
func TestBatchApplyFromCache_EnqueueFailureIsReported(t *testing.T) {
	ops := &capturingEnqueuer{err: errors.New("registry down")}
	h := newDispatchHandler(t, ops)

	c, w := batchApplyCtx(`{"book_ids":["b1"]}`)
	h.BatchApplyFromCache(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.NotEqual(t, http.StatusAccepted, w.Code)
}

// TestBatchApplyFromCache_RejectsMalformedBody keeps the binding guard.
func TestBatchApplyFromCache_RejectsMalformedBody(t *testing.T) {
	ops := &capturingEnqueuer{}
	h := newDispatchHandler(t, ops)

	c, w := batchApplyCtx(`{"book_ids":`)
	h.BatchApplyFromCache(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, 0, ops.calls, "nothing may be enqueued for an unparseable body")
}
