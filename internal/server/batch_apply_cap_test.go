// file: internal/server/batch_apply_cap_test.go
// version: 1.0.0
// guid: 7d3f9a52-6c1e-4b8a-9e07-5a2d8c4f1b63
// last-edited: 2026-09-02

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/applycap"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/reconcile"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// The bulk apply cap (internal/applycap) is a fail-safe against a filter or
// selection bug that materializes the whole library as the target of one
// apply. Every test here is a bogus/known-good pair: cap+1 must be REFUSED
// with nothing done, and exactly cap must get PAST the gate — otherwise an
// inert gate (or one that refuses everything) would pass a one-sided test.

func withBulkApplyCapServer(t *testing.T, n int) {
	t.Helper()
	prev := config.AppConfig.BulkApplyMaxItems
	config.AppConfig.BulkApplyMaxItems = n
	t.Cleanup(func() { config.AppConfig.BulkApplyMaxItems = prev })
}

func capIDs(prefix string, n int) []string {
	ids := make([]string, 0, n)
	for i := range n {
		ids = append(ids, prefix+strconv.Itoa(i))
	}
	return ids
}

func requireCapExceeded(t *testing.T, err error, wantRequested, wantCap int) {
	t.Helper()
	require.Error(t, err)
	var ex *applycap.ExceededError
	require.True(t, errors.As(err, &ex), "want *applycap.ExceededError, got %T: %v", err, err)
	require.Equal(t, wantRequested, ex.Requested)
	require.Equal(t, wantCap, ex.Cap)
	require.Contains(t, err.Error(), "bulk_apply_max_items", "the refusal must name the knob that raises it")
}

// --- mergeBatchApplyQueuedParams -------------------------------------------

// Two under-cap requests must not union into an over-cap run. Declining the
// merge is safe by registry semantics: the incoming request falls through to
// the ConcurrencyKey dedupe, its params byte-differ, and it is queued to run
// separately — where its own Run gate applies.
func TestMergeBatchApplyQueuedParams_DeclinesUnionOverTheCap(t *testing.T) {
	withBulkApplyCapServer(t, 3)
	existing, _ := json.Marshal(batchApplyOpParams{BookIDs: capIDs("a", 2)})
	incoming, _ := json.Marshal(batchApplyOpParams{BookIDs: capIDs("b", 2)})

	raw, merged, err := mergeBatchApplyQueuedParams(existing, incoming)
	require.NoError(t, err)
	require.False(t, merged, "2+2 ids against a cap of 3 must decline the merge")
	require.Nil(t, raw)
}

func TestMergeBatchApplyQueuedParams_MergesExactlyToTheCap(t *testing.T) {
	withBulkApplyCapServer(t, 3)
	existing, _ := json.Marshal(batchApplyOpParams{BookIDs: capIDs("a", 2)})
	incoming, _ := json.Marshal(batchApplyOpParams{BookIDs: capIDs("b", 1)})

	raw, merged, err := mergeBatchApplyQueuedParams(existing, incoming)
	require.NoError(t, err)
	require.True(t, merged, "2+1 ids against a cap of 3 fits and must merge")
	var out batchApplyOpParams
	require.NoError(t, json.Unmarshal(raw, &out))
	require.Len(t, out.BookIDs, 3)
}

// Duplicate ids are counted once, so a union that only LOOKS over the cap
// before dedupe must still merge — the cap is on books applied, not on
// request-list length.
func TestMergeBatchApplyQueuedParams_CountsUniqueIDs(t *testing.T) {
	withBulkApplyCapServer(t, 3)
	existing, _ := json.Marshal(batchApplyOpParams{BookIDs: []string{"a0", "a1", "a2"}})
	incoming, _ := json.Marshal(batchApplyOpParams{BookIDs: []string{"a0", "a1"}})

	raw, merged, err := mergeBatchApplyQueuedParams(existing, incoming)
	require.NoError(t, err)
	require.True(t, merged)
	var out batchApplyOpParams
	require.NoError(t, json.Unmarshal(raw, &out))
	require.Len(t, out.BookIDs, 3)
}

// --- op Run gates ---------------------------------------------------------

func capOpReg(t *testing.T) *opsregistry.Registry {
	t.Helper()
	m := dbmocks.NewMockStore(t)
	m.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()
	return opsregistry.New(m, slog.New(slog.DiscardHandler), 1, nil)
}

// capRunOp registers one op on a zero-value Server and invokes its REAL Run.
// Both ops under test check the cap before touching any dependency, so on
// a zero Server the two outcomes are distinguishable without a store:
//
//	*applycap.ExceededError      → stopped at the gate
//	"... not initialized" error  → got past the gate into the dependency check
func capRunOp(t *testing.T, opID string, register func(*Server, *opsregistry.Registry) error, params any) error {
	t.Helper()
	reg := capOpReg(t)
	require.NoError(t, register(&Server{}, reg))
	def, ok := reg.Def(opID)
	require.True(t, ok, "op %s not registered", opID)
	body, err := json.Marshal(params)
	require.NoError(t, err)
	return def.Run(context.Background(), body, nil)
}

func TestBatchApplyCachedOp_RefusesOverTheCap(t *testing.T) {
	withBulkApplyCapServer(t, 3)
	err := capRunOp(t, "metadata.batch-apply-cached", (*Server).RegisterBatchApplyFromCacheOp,
		batchApplyOpParams{BookIDs: capIDs("b", 4)})
	requireCapExceeded(t, err, 4, 3)
}

func TestBatchApplyCachedOp_ExactlyTheCapPassesTheGate(t *testing.T) {
	withBulkApplyCapServer(t, 3)
	err := capRunOp(t, "metadata.batch-apply-cached", (*Server).RegisterBatchApplyFromCacheOp,
		batchApplyOpParams{BookIDs: capIDs("b", 3)})
	require.Error(t, err)
	var ex *applycap.ExceededError
	require.False(t, errors.As(err, &ex), "exactly the cap must not be refused: %v", err)
	require.True(t, strings.Contains(err.Error(), "not initialized"), "expected to reach the dependency check, got: %v", err)
}

func TestReconcileApplyOp_RefusesOverTheCap(t *testing.T) {
	withBulkApplyCapServer(t, 2)
	matches := make([]reconcile.ReconcileApplyItem, 3)
	for i := range matches {
		matches[i] = reconcile.ReconcileApplyItem{BookID: "b" + strconv.Itoa(i), NewPath: "/x/" + strconv.Itoa(i)}
	}
	err := capRunOp(t, "reconcile.apply", (*Server).RegisterReconcileApplyOp,
		reconcileApplyOpParams{Matches: matches})
	requireCapExceeded(t, err, 3, 2)
}

func TestReconcileApplyOp_ExactlyTheCapPassesTheGate(t *testing.T) {
	withBulkApplyCapServer(t, 2)
	matches := []reconcile.ReconcileApplyItem{{BookID: "b0", NewPath: "/x/0"}, {BookID: "b1", NewPath: "/x/1"}}
	err := capRunOp(t, "reconcile.apply", (*Server).RegisterReconcileApplyOp,
		reconcileApplyOpParams{Matches: matches})
	require.Error(t, err)
	var ex *applycap.ExceededError
	require.False(t, errors.As(err, &ex), "exactly the cap must not be refused: %v", err)
	require.True(t, strings.Contains(err.Error(), "not initialized"), "expected to reach the dependency check, got: %v", err)
}

// A zero (or negative) configured cap means "use the default", never
// "unlimited" — a 0 that leaked into config.yaml must not disarm the fail-safe.
func TestBatchApplyCachedOp_ZeroConfigIsTheDefaultCap(t *testing.T) {
	withBulkApplyCapServer(t, 0)
	err := capRunOp(t, "metadata.batch-apply-cached", (*Server).RegisterBatchApplyFromCacheOp,
		batchApplyOpParams{BookIDs: capIDs("b", applycap.Default+1)})
	requireCapExceeded(t, err, applycap.Default+1, applycap.Default)
}

// --- handleBatchApplyCandidates (HTTP) --------------------------------------

func capCandidatesReq(t *testing.T, srv *Server, n int) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	body, err := json.Marshal(batchApplyRequest{OperationID: "op1", BookIDs: capIDs("b", n)})
	require.NoError(t, err)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/metadata/batch-apply-candidates", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	srv.handleBatchApplyCandidates(c)
	return w
}

func TestHandleBatchApplyCandidates_RefusesOverTheCap(t *testing.T) {
	withBulkApplyCapServer(t, 3)
	var storeReads atomic.Int32
	srv := &Server{store: &database.MockStore{
		GetOperationResultsFunc: func(string) ([]database.OperationResult, error) {
			storeReads.Add(1)
			return nil, errors.New("must not be reached")
		},
	}}
	w := capCandidatesReq(t, srv, 4)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "BULK_APPLY_CAP_EXCEEDED")
	require.Contains(t, w.Body.String(), "cap is 3")
	require.Equal(t, int32(0), storeReads.Load(), "refusal must happen before any store read")
}

func TestHandleBatchApplyCandidates_ExactlyTheCapReachesTheStore(t *testing.T) {
	withBulkApplyCapServer(t, 3)
	var storeReads atomic.Int32
	srv := &Server{store: &database.MockStore{
		GetOperationResultsFunc: func(string) ([]database.OperationResult, error) {
			storeReads.Add(1)
			// Failing the results load keeps the test off the apply machinery;
			// reaching it at all is what proves the gate let the request through.
			return nil, errors.New("synthetic")
		},
	}}
	w := capCandidatesReq(t, srv, 3)
	require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	require.Equal(t, int32(1), storeReads.Load())
}
