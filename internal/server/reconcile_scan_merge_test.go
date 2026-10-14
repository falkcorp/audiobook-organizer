// file: internal/server/reconcile_scan_merge_test.go
// version: 1.2.0
// guid: 8b2e4f61-9d03-4a75-b6c8-1e7f3a5d9c02
// last-edited: 2026-10-14

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	"github.com/falkcorp/audiobook-organizer/internal/activity"
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

// attemptID returns latest_attempt.id, or "" when the field is null/absent.
func attemptID(t *testing.T, body map[string]any) string {
	t.Helper()
	att, ok := body["latest_attempt"].(map[string]any)
	if !ok {
		return ""
	}
	id, _ := att["id"].(string)
	return id
}

func attemptStatus(body map[string]any) string {
	att, _ := body["latest_attempt"].(map[string]any)
	st, _ := att["status"].(string)
	return st
}

// A newer run that did not produce a scan must not hide the last good one. The
// nightly run is the same op as a manual scan now, so a single canceled night
// would otherwise blank the tab; the caller instead gets the good preview AND
// the newer attempt, so it can say why the scan it is showing is old.
func TestLatestReconcileScan_NewerCanceledRunDoesNotHideTheLastGoodScan(t *testing.T) {
	now := time.Now()
	good := scanPayload(t, "book-good")
	body := latestScanBody(t, []database.OperationV2Row{
		{ID: "manual-good", DefID: reconcileScanDefIDV2, Status: "completed", QueuedAt: now.Add(-20 * time.Hour), ResultData: &good},
		{ID: "nightly-canceled", DefID: formerReconcileScanID, Status: "canceled", QueuedAt: now},
	})

	assert.Equal(t, "manual-good", opID(t, body), "operation is the scan whose preview is returned")
	preview, ok := body["preview"].(map[string]any)
	require.True(t, ok, "the good scan's preview must still be served, got %v", body["preview"])
	broken, _ := preview["broken_records"].([]any)
	assert.Len(t, broken, 1)
	assert.Equal(t, "nightly-canceled", attemptID(t, body), "the newer canceled run must be reported")
	assert.Equal(t, "canceled", attemptStatus(body))
}

func TestLatestReconcileScan_NewerFailedAndInterruptedRunsAreReportedToo(t *testing.T) {
	now := time.Now()
	good := scanPayload(t, "book-good")
	for _, status := range []string{"failed", "interrupted_dropped", "interrupted_quiesced"} {
		body := latestScanBody(t, []database.OperationV2Row{
			{ID: "good", DefID: reconcileScanDefIDV2, Status: "completed", QueuedAt: now.Add(-time.Hour), ResultData: &good},
			{ID: "newer", DefID: reconcileScanDefIDV2, Status: status, QueuedAt: now},
		})
		assert.Equal(t, "good", opID(t, body), status)
		assert.NotNil(t, body["preview"], status)
		assert.Equal(t, "newer", attemptID(t, body), status)
		assert.Equal(t, status, attemptStatus(body))
	}
}

// A scan in flight must stay discoverable: the tab polls it, and before this the
// running run WAS the operation field. It now arrives as the attempt, next to
// the previous good preview.
func TestLatestReconcileScan_RunningScanArrivesAsTheAttemptBesideTheGoodPreview(t *testing.T) {
	now := time.Now()
	good := scanPayload(t, "book-good")
	body := latestScanBody(t, []database.OperationV2Row{
		{ID: "good", DefID: reconcileScanDefIDV2, Status: "completed", QueuedAt: now.Add(-time.Hour), ResultData: &good},
		{ID: "live", DefID: reconcileScanDefIDV2, Status: "running", QueuedAt: now},
	})
	assert.Equal(t, "good", opID(t, body))
	assert.NotNil(t, body["preview"])
	assert.Equal(t, "live", attemptID(t, body))
	assert.Equal(t, "running", attemptStatus(body))
}

// A newest run that completed with a result that does not decode falls back to
// the older scan that does (todo.d 20260825: it used to answer preview:null and
// stop), and is still reported so the caller knows the newest result is unusable.
func TestLatestReconcileScan_UnreadableNewestResultFallsBackToAnOlderScan(t *testing.T) {
	now := time.Now()
	good := scanPayload(t, "book-good")
	bad := "{not json"
	body := latestScanBody(t, []database.OperationV2Row{
		{ID: "older-good", DefID: reconcileScanDefIDV2, Status: "completed", QueuedAt: now.Add(-3 * time.Hour), ResultData: &good},
		{ID: "newest-corrupt", DefID: reconcileScanDefIDV2, Status: "completed", QueuedAt: now, ResultData: &bad},
	})
	assert.Equal(t, "older-good", opID(t, body))
	assert.NotNil(t, body["preview"])
	assert.Equal(t, "newest-corrupt", attemptID(t, body))
	assert.Equal(t, "completed", attemptStatus(body))
}

func TestLatestReconcileScan_NewestGoodScanHasNoAttempt(t *testing.T) {
	now := time.Now()
	older, newest := scanPayload(t, "book-old"), scanPayload(t, "book-new")
	body := latestScanBody(t, []database.OperationV2Row{
		{ID: "older", DefID: reconcileScanDefIDV2, Status: "completed", QueuedAt: now.Add(-time.Hour), ResultData: &older},
		{ID: "newest", DefID: formerReconcileScanID, Status: "completed", QueuedAt: now, ResultData: &newest},
	})
	assert.Equal(t, "newest", opID(t, body))
	assert.Nil(t, body["latest_attempt"], "nothing is newer than the scan being shown")
}

// With no usable scan the answer is what it always was: the newest run, no
// preview, and no separate attempt (the operation field already is that run).
func TestLatestReconcileScan_NoUsableScanAnswersWithTheNewestRun(t *testing.T) {
	now := time.Now()
	body := latestScanBody(t, []database.OperationV2Row{
		{ID: "old-failed", DefID: reconcileScanDefIDV2, Status: "failed", QueuedAt: now.Add(-time.Hour)},
		{ID: "live", DefID: reconcileScanDefIDV2, Status: "running", QueuedAt: now},
	})
	assert.Equal(t, "live", opID(t, body))
	assert.Nil(t, body["preview"])
	assert.Nil(t, body["latest_attempt"])

	empty := latestScanBody(t, nil)
	assert.Nil(t, empty["operation"])
	assert.Nil(t, empty["preview"])
	assert.Nil(t, empty["latest_attempt"])
}

// GET /operations/reconcile/preview is a synchronous full-library sweep. It runs
// under the request's context so a client that goes away stops it: with a
// canceled request the handler must not even load the books (the strict mock
// fails on an unexpected call) and must answer nothing.
func TestReconcilePreview_StopsWhenTheRequestIsCanceled(t *testing.T) {
	srv, _, _ := bootRegisteredOpIDs(t)
	require.NotNil(t, srv.storeForWiring())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/operations/reconcile/preview", nil).WithContext(ctx)

	srv.reconcilePreview(c)
	assert.Empty(t, w.Body.String(), "a canceled request gets no body, and in particular no 500")
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
	// A reconcile scan interrupted by a deploy is dropped, never resumed: the def
	// says so, and (since the registry's old "reconcile_scan" special case was
	// removed) the def is the only thing that does.
	assert.Equal(t, opsregistry.ResumeDrop, def.ResumePolicy)
	assert.Equal(t, opsregistry.LivenessManual, def.Liveness)

	// The former id resolves to the same def, so a script or stored row that
	// names it reaches the survivor.
	viaAlias, ok := srv.opRegistry.Def(formerReconcileScanID)
	require.True(t, ok)
	assert.Equal(t, reconcileScanDefIDV2, viaAlias.ID)
	assert.Equal(t, opsregistry.ResumeDrop, viaAlias.ResumePolicy, "a row stored under the former id resumes through the same def")
}

// touchCountingReporter counts liveness stamps separately from progress frames.
type touchCountingReporter struct {
	opsregistry.Reporter
	touch func()
}

func (r touchCountingReporter) TouchLiveness() { r.touch() }

// OpID is not promoted from the embedded interface, and the op's activity lines
// are keyed on it.
func (r touchCountingReporter) OpID() string { return "op-scan" }

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

	srv.activityWriter = activity.NewWriter(nil, 16)
	err := def.Run(ctx, nil, rep)
	require.ErrorIs(t, err, context.Canceled, "empty params must decode and Cancel must stop the scan")
	assert.GreaterOrEqual(t, touches.Load(), int64(1), "the scan must stamp the reporter's liveness clock")
	assert.Equal(t, []string{"Reconcile scan canceled"}, scanSummaries(srv.activityWriter, "op-scan"),
		"a canceled scan is logged as canceled, not failed")
}

// scanSummaries returns the summaries queued on w for opID.
func scanSummaries(w *activity.Writer, opID string) []string {
	var out []string
	for _, e := range drainActivity(w) {
		if e.OperationID == opID && e.Type == "reconcile.scan" {
			out = append(out, e.Summary)
		}
	}
	return out
}

// The activity line for a finished scan: a canceled scan is not a failure, and a
// failure's text is not announced twice.
func TestReconcileScanSummary(t *testing.T) {
	assert.Equal(t, "Reconcile scan completed", reconcileScanSummary(nil))
	assert.Equal(t, "Reconcile scan canceled", reconcileScanSummary(context.Canceled))
	assert.Equal(t, "Reconcile scan canceled",
		reconcileScanSummary(fmt.Errorf("reconcile scan canceled while matching titles: %w", context.Canceled)))
	assert.Equal(t, "Reconcile scan failed: failed to list books: boom",
		reconcileScanSummary(errors.New("failed to list books: boom")))
	assert.Equal(t, "Reconcile scan failed: context deadline exceeded", reconcileScanSummary(context.DeadlineExceeded),
		"a timeout is a failure, not an operator cancel")
}

// The error the op returns carries one prefix: the def's. RunReconcileScan used
// to add "reconcile scan failed:" on top of a message that already said so.
func TestReconcileScanOp_RunReturnsAFailureWithASinglePrefix(t *testing.T) {
	srv, _, _ := bootRegisteredOpIDs(t)
	store, ok := srv.store.(*mocks.MockStore)
	require.True(t, ok)
	store.EXPECT().GetAllBooksCore(mock.Anything, mock.Anything).Return(nil, errors.New("boom")).Maybe()
	store.EXPECT().UpdateOpProgressV2(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

	def, ok := srv.opRegistry.Def(reconcileScanDefIDV2)
	require.True(t, ok)
	ctx := context.Background()
	rep := opsregistry.NewDBReporterForTest(ctx, "op-fail", reconcileScanDefIDV2, "reconcile", "", "", store, nil, slog.New(slog.DiscardHandler))

	srv.activityWriter = activity.NewWriter(nil, 16)
	err := def.Run(ctx, nil, rep)
	require.Error(t, err)
	assert.Equal(t, "reconcile.scan: failed to list books: boom", err.Error())
	assert.Equal(t, []string{"Reconcile scan failed: failed to list books: boom"}, scanSummaries(srv.activityWriter, "op-fail"))
}

// The Repairs fixer reconcile.missing-files reads the latest scan through
// Server.LatestReconcileScan (03-PR9b). It must pick the scan the endpoint
// shows, by the same rule over the same rows: the former-id nightly row, the
// newest good scan beside a newer attempt, and no usable scan at all.
func TestLatestReconcileScan_FixerDepPicksTheEndpointsScan(t *testing.T) {
	now := time.Now()
	good := scanPayload(t, "book-good")
	nightly := scanPayload(t, "book-nightly")
	cases := []struct {
		name              string
		rows              []database.OperationV2Row
		wantOp, wantTry   string
		wantTryStatus     string
		wantPreviewBookID string
	}{
		{"nightly row under the former id", []database.OperationV2Row{
			{ID: "nightly-run", DefID: formerReconcileScanID, Status: "completed", QueuedAt: now, ResultData: &nightly},
		}, "nightly-run", "", "", "book-nightly"},
		{"newer canceled run beside the good scan", []database.OperationV2Row{
			{ID: "good", DefID: reconcileScanDefIDV2, Status: "completed", QueuedAt: now.Add(-time.Hour), ResultData: &good},
			{ID: "canceled", DefID: formerReconcileScanID, Status: "canceled", QueuedAt: now},
		}, "good", "canceled", "canceled", "book-good"},
		{"only a running scan", []database.OperationV2Row{
			{ID: "live", DefID: reconcileScanDefIDV2, Status: "running", QueuedAt: now},
		}, "", "live", "running", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, _ := bootRegisteredOpIDs(t)
			store, ok := srv.store.(*mocks.MockStore)
			require.True(t, ok)
			store.EXPECT().ListOperationsV2Since(mock.Anything, mock.Anything).Return(tc.rows, nil).Maybe()

			got, err := srv.LatestReconcileScan()
			require.NoError(t, err)
			assert.Equal(t, tc.wantOp, got.OpID)
			assert.Equal(t, tc.wantTry, got.AttemptOpID)
			assert.Equal(t, tc.wantTryStatus, got.AttemptStatus)
			if tc.wantPreviewBookID == "" {
				assert.Nil(t, got.Preview)
				return
			}
			require.NotNil(t, got.Preview)
			require.Len(t, got.Preview.UnmatchedBooks, 1)
			assert.Equal(t, tc.wantPreviewBookID, got.Preview.UnmatchedBooks[0].BookID)

			// The endpoint answers the same scan from the same rows.
			body := latestScanBody(t, tc.rows)
			assert.Equal(t, tc.wantOp, opID(t, body))
			assert.Equal(t, tc.wantTry, attemptID(t, body))
		})
	}
}
