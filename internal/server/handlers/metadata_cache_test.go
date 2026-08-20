// file: internal/server/handlers/metadata_cache_test.go
// version: 2.1.0
// guid: 6b1c0a94-2f7d-4c8e-9a15-3d0e7b28c4f1
// last-edited: 2026-08-20

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

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
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

// ---------------------------------------------------------------------------
// GetCacheReviewResults — the counts must describe the rows actually returned.
// ---------------------------------------------------------------------------

// reviewCtx builds a GET gin context for the cache review listing.
func reviewCtx(query string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/audiobooks/metadata/cache/review?"+query, nil)
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	return c, w
}

// TestGetCacheReviewResults_CountsOnlyReviewableRows pins the fix for a real
// production reporting bug.
//
// The counts were tallied over every row whose BOOK resolved, while the results
// list additionally dropped rows with no cached candidate. On the live server
// that was 10,952 counted against 5,774 returned, so the review rail advertised
// "10730 matched" over a list that could never hold more than 5,774 rows — and
// `errors` was hardcoded 0, so nothing in the payload hinted at the gap.
//
// The fixture reproduces that shape in miniature: four cache summaries, one
// whose book is gone and one with no stored candidate, leaving two reviewable.
func TestGetCacheReviewResults_CountsOnlyReviewableRows(t *testing.T) {
	store := handlersmocks.NewMockMetadataCacheBookStore(t)
	svc := handlersmocks.NewMockMetadataCacheFetchService(t)

	ids := []string{"b1", "b2", "b3", "gone"}
	summaries := make([]metafetch.MetadataCacheSummary, 0, len(ids))
	for _, id := range ids {
		summaries = append(summaries, metafetch.MetadataCacheSummary{BookID: id})
	}
	svc.EXPECT().ListCachedSummaries(mock.Anything).Return(summaries, nil)

	// "gone" is a cache entry whose book no longer exists — the class that made
	// total_count overstate by thousands.
	books := []database.Book{{ID: "b1"}, {ID: "b2"}, {ID: "b3"}}
	store.EXPECT().GetBooksByIDs(mock.Anything).Return(books, nil)
	store.EXPECT().GetBookByID("gone").Return(nil, nil).Maybe()
	store.EXPECT().GetBookFiles(mock.Anything).Return(nil, nil).Maybe()

	raw, err := json.Marshal(map[string]any{"title": "T"})
	require.NoError(t, err)
	withCandidate := &metafetch.MetadataCandidateCache{Candidates: []json.RawMessage{raw}}

	svc.EXPECT().GetCachedCandidates("b1").Return(withCandidate, true, nil)
	svc.EXPECT().GetCachedCandidates("b2").Return(withCandidate, true, nil)
	// b3 is cached but holds no candidate: nothing to review, and it must not be
	// counted as though there were.
	svc.EXPECT().GetCachedCandidates("b3").Return(&metafetch.MetadataCandidateCache{}, true, nil)

	h := handlers.NewMetadataCacheHandler(store, svc, nil, nil, nil)
	c, w := reviewCtx("limit=0&offset=0")
	h.GetCacheReviewResults(c)

	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Data struct {
			Results      []map[string]any `json:"results"`
			TotalCount   int              `json:"total_count"`
			Matched      int              `json:"matched"`
			NoMatch      int              `json:"no_match"`
			Errors       int              `json:"errors"`
			Unreviewable int              `json:"unreviewable"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))

	// The load-bearing assertion: the advertised total is the number of rows the
	// caller was actually given, not the number of cache entries that exist.
	assert.Len(t, body.Data.Results, 2, "b1 and b2 are the only reviewable rows")
	assert.Equal(t, 2, body.Data.TotalCount, "total_count must not count rows it cannot return")
	assert.Equal(t, 2, body.Data.Matched, "matched must not count the bookless or candidateless rows")
	assert.Equal(t, 0, body.Data.Errors)
	// 4 summaries, 2 reviewable — the gap is reported rather than left to be
	// discovered by subtracting two numbers that never agreed.
	assert.Equal(t, 2, body.Data.Unreviewable)
}

// TestGetCacheReviewResults_UnreviewableSplitByCause pins the breakdown that
// replaced a bare subtraction.
//
// `unreviewable` used to be `total - len(reviewable)`. The number was right and
// useless: on production it read 8,532 with nothing to say about what caused it,
// even though each of the three `continue` statements that drops a row knows
// exactly why it fired. The causes call for opposite remedies — an orphaned row
// can only be reaped, a candidateless one can be refetched — so collapsing them
// into one integer threw away the part an operator needs.
//
// The fixture puts one row in each cause and one reviewable row beside them.
func TestGetCacheReviewResults_UnreviewableSplitByCause(t *testing.T) {
	store := handlersmocks.NewMockMetadataCacheBookStore(t)
	svc := handlersmocks.NewMockMetadataCacheFetchService(t)

	ids := []string{"ok", "gone", "empty", "bad"}
	summaries := make([]metafetch.MetadataCacheSummary, 0, len(ids))
	for _, id := range ids {
		summaries = append(summaries, metafetch.MetadataCacheSummary{BookID: id})
	}
	svc.EXPECT().ListCachedSummaries(mock.Anything).Return(summaries, nil)

	// "gone" resolves to no book at all — the orphan cause.
	books := []database.Book{{ID: "ok"}, {ID: "empty"}, {ID: "bad"}}
	store.EXPECT().GetBooksByIDs(mock.Anything).Return(books, nil)
	store.EXPECT().GetBookByID("gone").Return(nil, nil).Maybe()
	store.EXPECT().GetBookFiles(mock.Anything).Return(nil, nil).Maybe()

	raw, err := json.Marshal(map[string]any{"title": "T"})
	require.NoError(t, err)

	svc.EXPECT().GetCachedCandidates("ok").
		Return(&metafetch.MetadataCandidateCache{Candidates: []json.RawMessage{raw}}, true, nil)
	// Cached, but holding nothing — the refetchable cause.
	svc.EXPECT().GetCachedCandidates("empty").
		Return(&metafetch.MetadataCandidateCache{}, true, nil)
	// Cached and non-empty, but the payload will not decode — the repair cause.
	svc.EXPECT().GetCachedCandidates("bad").
		Return(&metafetch.MetadataCandidateCache{
			Candidates: []json.RawMessage{json.RawMessage(`{"title":`)},
		}, true, nil)

	h := handlers.NewMetadataCacheHandler(store, svc, nil, nil, nil)
	c, w := reviewCtx("limit=0&offset=0")
	h.GetCacheReviewResults(c)

	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Data struct {
			Results      []map[string]any `json:"results"`
			TotalCount   int              `json:"total_count"`
			Errors       int              `json:"errors"`
			Unreviewable int              `json:"unreviewable"`
			ByCause      struct {
				Orphaned     int `json:"orphaned"`
				NoCandidates int `json:"no_candidates"`
				DecodeErrors int `json:"decode_errors"`
			} `json:"unreviewable_by_cause"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))

	require.Len(t, body.Data.Results, 1, "only \"ok\" is reviewable")
	assert.Equal(t, 1, body.Data.TotalCount)

	// Each cause is attributed to the row that actually caused it.
	assert.Equal(t, 1, body.Data.ByCause.Orphaned, "\"gone\" has no book")
	assert.Equal(t, 1, body.Data.ByCause.NoCandidates, "\"empty\" has no stored candidate")
	assert.Equal(t, 1, body.Data.ByCause.DecodeErrors, "\"bad\" will not unmarshal")
	assert.Equal(t, 1, body.Data.Errors, "decode failures are still reported on their own")

	// The identity that makes the breakdown trustworthy: every dropped row is
	// counted once and only once, so the causes sum to the total, and the total
	// still equals what the old subtraction produced.
	byCause := body.Data.ByCause.Orphaned + body.Data.ByCause.NoCandidates + body.Data.ByCause.DecodeErrors
	assert.Equal(t, body.Data.Unreviewable, byCause, "causes must account for every unreviewable row")
	assert.Equal(t, len(summaries)-body.Data.TotalCount, body.Data.Unreviewable,
		"unreviewable must still equal total minus reviewable")
}
