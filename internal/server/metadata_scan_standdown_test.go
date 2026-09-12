// file: internal/server/metadata_scan_standdown_test.go
// version: 1.1.0
// guid: c83e1a5f-02d7-4b69-8f4e-5a91d6c70b28
// last-edited: 2026-09-12

package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// sdGate stands in for the registry's scan stand-down. acquireErr models a scan
// that will not park; tryErr models a scan running at request time.
type sdGate struct {
	acquireErr, tryErr error
	acquired, tries    int
	renewFail          bool
	renews, released   atomic.Int32
}

func (g *sdGate) AcquireScanStandDown(_ context.Context, _, _ string) (func(), error) {
	if g.acquireErr != nil {
		return nil, g.acquireErr
	}
	g.acquired++
	return func() {}, nil
}
func (g *sdGate) RenewScanStandDown(string) bool {
	g.renews.Add(1)
	return !g.renewFail
}
func (g *sdGate) TryAcquireScanStandDown(string, string) (func(), error) {
	g.tries++
	if g.tryErr != nil {
		return nil, g.tryErr
	}
	return func() { g.released.Add(1) }, nil
}

// sdReporter is a registry reporter carrying an op id, which is what makes the
// op take the gate (ops run without one — direct-call tests — are ungated).
type sdReporter struct{ id string }

func (r *sdReporter) OpID() string                               { return r.id }
func (r *sdReporter) UpdateProgress(int, int, string) error      { return nil }
func (r *sdReporter) Log(slog.Level, string, ...slog.Attr) error { return nil }
func (r *sdReporter) Logger() *slog.Logger                       { return slog.New(slog.DiscardHandler) }
func (r *sdReporter) Checkpoint(any) error                       { return nil }
func (r *sdReporter) IsCanceled() bool                           { return false }
func (r *sdReporter) Trigger(context.Context, string, any) error { return nil }
func (r *sdReporter) SetCurrentItem(string)                      {}
func (r *sdReporter) RunPhase(ctx context.Context, _ string, fn func(context.Context, opsregistry.Reporter) error) error {
	return fn(ctx, r)
}

func runGatedOp(t *testing.T, gate *sdGate, opID string, register func(*Server, *opsregistry.Registry) error, params any) error {
	t.Helper()
	reg := capOpReg(t)
	require.NoError(t, register(&Server{scanStandDownGateOverride: gate}, reg))
	def, ok := reg.Def(opID)
	require.True(t, ok, "op %s not registered", opID)
	body, err := json.Marshal(params)
	require.NoError(t, err)
	return def.Run(context.Background(), body, &sdReporter{id: "op-" + opID})
}

var errNoPark = errors.New("scan stand-down: scan did not park within 5m0s")

// Every metadata-applying op registered in package server fails with a clear
// message, before any dependency is touched, when the scan will not stand down.
func TestMetadataOps_RefuseWhileScanHoldsTheLibrary(t *testing.T) {
	withBulkApplyCapServer(t, 100)
	cases := []struct {
		opID     string
		register func(*Server, *opsregistry.Registry) error
		params   any
	}{
		{"metadata.batch-apply-cached", (*Server).RegisterBatchApplyFromCacheOp, batchApplyOpParams{BookIDs: []string{"b1"}}},
		{"metadata.batch-save", (*Server).RegisterBatchSaveToFilesOp, batchSaveOpParams{BookIDs: []string{"b1"}}},
		{"library.bulk-metadata-fetch", (*Server).RegisterBulkMetadataFetchOp, map[string]any{}},
		{"library.bulk-write-back", (*Server).RegisterBulkWriteBackOp, bulkWriteBackOpParams{BookIDs: []string{"b1"}}},
	}
	for _, tc := range cases {
		t.Run(tc.opID, func(t *testing.T) {
			gate := &sdGate{acquireErr: errNoPark}
			err := runGatedOp(t, gate, tc.opID, tc.register, tc.params)
			require.Error(t, err)
			require.Contains(t, err.Error(), "a library scan is running")
			require.Contains(t, err.Error(), "did not park")
			require.Contains(t, err.Error(), tc.opID)
		})
	}
}

// Bogus/known-good pair for the op the mutation check targets: once the scan
// has parked the op proceeds past the gate into its dependency check.
func TestBatchApplyCachedOp_ProceedsOnceScanParks(t *testing.T) {
	withBulkApplyCapServer(t, 100)
	gate := &sdGate{}
	err := runGatedOp(t, gate, "metadata.batch-apply-cached", (*Server).RegisterBatchApplyFromCacheOp,
		batchApplyOpParams{BookIDs: []string{"b1"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not initialized", "must reach the dependency check after the gate")
	require.Equal(t, 1, gate.acquired, "the op must hold the stand-down before applying")
}

func TestBatchApplyCandidates_409WhileScanRuns(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gate := &sdGate{tryErr: opsregistry.ErrScanRunning}
	s := &Server{scanStandDownGateOverride: gate}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/metadata/batch-apply-candidates",
		strings.NewReader(`{"operation_id":"op","book_ids":["b1"]}`))
	c.Request.Header.Set("Content-Type", "application/json")
	s.handleBatchApplyCandidates(c)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "a library scan is running; try again when it finishes")
	require.Equal(t, 1, gate.tries)
}

// batch-apply-candidates renews its request hold once per book, and once the
// hold is lost no book is applied (a scan may already be running again). The
// lost case would reach the nil fetch service if the checkpoint did not stop it.
func TestBatchApplyCandidates_RenewsPerBookAndStopsWhenHoldLost(t *testing.T) {
	withBulkApplyCapServer(t, 100)
	gin.SetMode(gin.TestMode)
	run := func(t *testing.T, gate *sdGate) *httptest.ResponseRecorder {
		store := dbmocks.NewMockStore(t)
		store.EXPECT().GetOperationResults("op").Return(nil, nil)
		s := &Server{store: store, scanStandDownGateOverride: gate}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/metadata/batch-apply-candidates",
			strings.NewReader(`{"operation_id":"op","book_ids":["b1","b2","b3"]}`))
		c.Request.Header.Set("Content-Type", "application/json")
		s.handleBatchApplyCandidates(c)
		return w
	}
	t.Run("renews per book", func(t *testing.T) {
		gate := &sdGate{}
		w := run(t, gate)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.EqualValues(t, 3, gate.renews.Load(), "one renewal per book")
		require.EqualValues(t, 1, gate.released.Load())
	})
	t.Run("lost hold applies nothing", func(t *testing.T) {
		gate := &sdGate{renewFail: true}
		w := run(t, gate)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Contains(t, w.Body.String(), opsregistry.ErrScanStandDownLost.Error())
		require.NotContains(t, w.Body.String(), `"applied":1`)
		require.EqualValues(t, 1, gate.released.Load())
	})
}
