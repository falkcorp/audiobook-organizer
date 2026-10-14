// file: internal/server/reconcile_scan_merge_test.go
// version: 1.0.0
// guid: 8b2e4f61-9d03-4a75-b6c8-1e7f3a5d9c02
// last-edited: 2026-10-14

package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/reconcile"
)

// 04-P14c merged maintenance.reconcile-scan (the nightly run) into reconcile.scan.
// The /latest endpoint used to match rows on the literal "reconcile.scan", so
// every nightly row stored under the other id would have vanished from it.

// formerReconcileScanID is the retired nightly op's id, as stored in old rows.
const formerReconcileScanID = "maintenance.reconcile-scan"

// scanPayload returns a ReconcilePreviewResult JSON with one unmatched record,
// the shape both the nightly and the manual run store.
func scanPayload(t *testing.T, bookID string) string {
	t.Helper()
	b, err := json.Marshal(reconcile.ReconcilePreviewResult{
		BrokenRecords:  []reconcile.ReconcileBrokenRecord{{BookID: bookID, Title: "synthetic", FilePath: "/library/gone.m4b"}},
		UntrackedFiles: []string{},
		Matches:        []reconcile.ReconcileMatch{},
		UnmatchedBooks: []reconcile.ReconcileBrokenRecord{{BookID: bookID, Title: "synthetic", FilePath: "/library/gone.m4b"}},
	})
	require.NoError(t, err)
	return string(b)
}

// latestScanBody drives GET /operations/reconcile/scan/latest against a server
// whose store lists rows, and returns the decoded JSON body.
func latestScanBody(t *testing.T, rows []database.OperationV2Row) map[string]any {
	t.Helper()
	srv, _, _ := bootRegisteredOpIDs(t)
	store, ok := srv.store.(*mocks.MockStore)
	require.True(t, ok, "the boot helper hands the server a MockStore")
	store.EXPECT().ListOperationsV2Since(mock.Anything, mock.Anything).Return(rows, nil).Maybe()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/operations/reconcile/scan/latest", nil)
	srv.latestReconcileScan(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	if data, ok := body["data"].(map[string]any); ok {
		return data
	}
	return body
}

func opID(t *testing.T, body map[string]any) string {
	t.Helper()
	op, ok := body["operation"].(map[string]any)
	require.True(t, ok, "operation missing from %v", body)
	id, _ := op["id"].(string)
	return id
}

func TestLatestReconcileScan_FindsNightlyRunStoredUnderFormerID(t *testing.T) {
	now := time.Now()
	payload := scanPayload(t, "book-nightly")
	body := latestScanBody(t, []database.OperationV2Row{
		{ID: "nightly-run", DefID: formerReconcileScanID, Status: "completed", QueuedAt: now, ResultData: &payload},
	})

	assert.Equal(t, "nightly-run", opID(t, body),
		"a row recorded under the retired def id must still be the latest scan")
	preview, ok := body["preview"].(map[string]any)
	require.True(t, ok, "the saved ResultData must decode into a preview, got %v", body["preview"])
	broken, _ := preview["broken_records"].([]any)
	assert.Len(t, broken, 1)
}

func TestLatestReconcileScan_NewestWinsAcrossFormerAndCurrentID(t *testing.T) {
	now := time.Now()
	old := scanPayload(t, "book-old")
	fresh := scanPayload(t, "book-fresh")
	rows := []database.OperationV2Row{
		{ID: "manual-old", DefID: reconcileScanDefIDV2, Status: "completed", QueuedAt: now.Add(-2 * time.Hour), ResultData: &old},
		{ID: "nightly-new", DefID: formerReconcileScanID, Status: "completed", QueuedAt: now, ResultData: &fresh},
		{ID: "apply-newer", DefID: reconcileApplyDefIDV2, Status: "completed", QueuedAt: now.Add(time.Hour)},
	}
	body := latestScanBody(t, rows)
	assert.Equal(t, "nightly-new", opID(t, body), "newest scan wins whichever def id it was stored under; apply rows never count")

	// And the reverse: a newer manual run beats an older nightly row.
	rows[0].QueuedAt, rows[1].QueuedAt = now, now.Add(-2*time.Hour)
	assert.Equal(t, "manual-old", opID(t, latestScanBody(t, rows)))
}

func TestRecentReconcileScans_MatchesOnCanonicalID(t *testing.T) {
	now := time.Now()
	store := &reconcileIndexStore{v2: []database.OperationV2Row{
		{ID: "nightly", DefID: formerReconcileScanID, Status: "completed", QueuedAt: now},
		{ID: "unrelated", DefID: "library.scan", Status: "completed", QueuedAt: now},
	}}
	srv, _, _ := bootRegisteredOpIDs(t)

	got := recentReconcileScans(store, 50, srv.opRegistry.CanonicalDefID)
	require.Len(t, got, 1)
	assert.Equal(t, "nightly", got[0].ID)

	// nil canonicalizer: ids compare as stored, so the former id is not a scan.
	assert.Empty(t, recentReconcileScans(store, 50, nil))
}

// The survivor must keep what the retired op declared: the larger timeout and the
// file-read capability (the scan stats and hashes files), plus the alias itself.
func TestReconcileScanOp_CarriesLoserBehaviour(t *testing.T) {
	srv, _, aliases := bootRegisteredOpIDs(t)

	def, ok := srv.opRegistry.Def(reconcileScanDefIDV2)
	require.True(t, ok)
	assert.GreaterOrEqual(t, def.Timeout, 3*time.Hour, "the retired op's 180 minutes is the larger budget")
	assert.Contains(t, def.Capabilities, opsregistry.CapFilesRead)
	assert.Contains(t, def.Capabilities, opsregistry.CapLibraryRead)
	assert.Equal(t, reconcileScanDefIDV2, aliases[formerReconcileScanID])
	assert.True(t, def.Cancellable, "Cancel is honored: the body checks ctx at every phase")
	assert.Equal(t, opsregistry.LivenessManual, def.Liveness)

	// The former id resolves to the same def, so a script or stored row that
	// names it reaches the survivor.
	viaAlias, ok := srv.opRegistry.Def(formerReconcileScanID)
	require.True(t, ok)
	assert.Equal(t, reconcileScanDefIDV2, viaAlias.ID)
}

// touchCountingReporter counts liveness stamps separately from progress frames.
type touchCountingReporter struct {
	opsregistry.Reporter
	touch func()
}

func (r touchCountingReporter) TouchLiveness() { r.touch() }

// Drives the registered Run end to end: the heartbeat hook must reach the
// reporter's liveness clock (TouchLiveness), Cancel must be honored with no
// preview saved (the strict store mock fails on any unexpected write), and a call with no params at all (what the retired op was
// enqueued with, and what an alias caller may still send) must not fail to decode.
func TestReconcileScanOp_RunStampsLivenessAndHonorsCancel(t *testing.T) {
	srv, _, _ := bootRegisteredOpIDs(t)
	store, ok := srv.store.(*mocks.MockStore)
	require.True(t, ok)
	store.EXPECT().GetAllBooksCore(mock.Anything, mock.Anything).Return([]database.BookCore{
		{ID: "b1", Title: "synthetic", FilePath: filepath.Join(t.TempDir(), "gone.m4b")},
	}, nil).Maybe()

	store.EXPECT().UpdateOpProgressV2(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

	def, ok := srv.opRegistry.Def(reconcileScanDefIDV2)
	require.True(t, ok)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var touches atomic.Int64
	// The wrapper's TouchLiveness is reached only through the op's Heartbeat hook
	// (opsregistry.TouchLiveness): UpdateProgress goes to the embedded reporter,
	// so progress frames cannot be mistaken for the heartbeat.
	rep := touchCountingReporter{
		Reporter: opsregistry.NewDBReporterForTest(ctx, "op-scan", reconcileScanDefIDV2, "reconcile", "", "", store, nil, slog.New(slog.DiscardHandler)),
		touch: func() {
			touches.Add(1)
			cancel() // the first heartbeat is the stat worker's; cancel from there
		},
	}

	err := def.Run(ctx, nil, rep)
	require.ErrorIs(t, err, context.Canceled, "empty params must decode and Cancel must stop the scan")
	assert.GreaterOrEqual(t, touches.Load(), int64(1), "the scan must stamp the reporter's liveness clock")
}
