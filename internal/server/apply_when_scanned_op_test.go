// file: internal/server/apply_when_scanned_op_test.go
// version: 1.0.0
// guid: 9e3b6a14-72c5-4f08-b1d9-58a0c3e7f216
// last-edited: 2026-09-30

package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/scanlock"
	metadatahandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/metadata"
)

// waitingRunner is the queued apply's single-book runner with the real wait
// (metadatahandler.WaitForBook) and a recorded apply in place of the metadata
// core, so the test observes dispatch and the moment the book is taken.
type waitingRunner struct {
	dispatched chan struct{}
	applied    chan struct{}
	// scanRunningAtApply records whether library.scan was still running when
	// the apply took the book: the change must land DURING the scan.
	scanRunningAtApply atomic.Bool
	reg                *opsregistry.Registry
}

func (r *waitingRunner) RunQueuedApply(ctx context.Context, q metadatahandler.QueuedApply, beat func(string)) error {
	close(r.dispatched)
	hold, err := metadatahandler.WaitForBook(ctx, q.BookID, beat)
	if err != nil {
		return err
	}
	defer hold.Release()
	r.scanRunningAtApply.Store(r.reg.LibraryScanRunning())
	close(r.applied)
	return nil
}

// The BLOCKING review requirement: a queued apply must really run while a
// library scan is running, not sit behind it. Enqueued while library.scan is
// registered AND running, it must be dispatched at once (no ConcurrencyKey,
// no Writes conflict at Gate 3b, not held by Gate 3.5 or the pickup gate),
// wait only for its book, and take the book the moment the scanner releases
// it -- with the scan still running.
//
// The library.scan def is the REAL one (RegisterLibraryScanOp) with only its
// Run swapped for a body that holds book-1's scan lock the way the scanner
// does, so its ConcurrencyKey, Writes and priority are production's.
func TestApplyWhenScanned_RunsDuringARunningScanAsSoonAsTheBookFrees(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	store := newOpsFake(t)
	reg := opsregistry.New(store, slog.New(slog.DiscardHandler), 4, nil)

	src := capOpReg(t)
	require.NoError(t, (&Server{}).RegisterLibraryScanOp(src))
	scanDef, ok := src.Def("library.scan")
	require.True(t, ok)
	scanHolding := make(chan struct{})
	releaseBook := make(chan struct{})
	endScan := make(chan struct{})
	scanDef.Run = func(ctx context.Context, _ json.RawMessage, rep opsregistry.Reporter) error {
		hold, err := scanlock.Books.LockSet(ctx, []string{"book-1"})
		if err != nil {
			return err
		}
		close(scanHolding)
		<-releaseBook
		hold.Release()
		// Keep scanning other books until the test is done.
		for {
			select {
			case <-endScan:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(20 * time.Millisecond):
				_ = rep.UpdateProgress(0, 1, "scanning")
			}
		}
	}
	require.NoError(t, reg.RegisterOp(scanDef))

	runner := &waitingRunner{dispatched: make(chan struct{}), applied: make(chan struct{}), reg: reg}
	s := &Server{opRegistry: reg, applyWhenScannedHandler: runner}
	require.NoError(t, s.RegisterApplyWhenScannedOp(reg))
	reg.Start(ctx)
	defer close(endScan)

	_, err := reg.EnqueueOp(ctx, "library.scan", map[string]any{})
	require.NoError(t, err)
	select {
	case <-scanHolding:
	case <-time.After(10 * time.Second):
		t.Fatal("library.scan never started")
	}
	require.True(t, reg.LibraryScanRunning())

	_, err = s.EnqueueApplyWhenScanned(ctx, metadatahandler.QueuedApply{Kind: metadatahandler.QueuedFetch, BookID: "book-1"})
	require.NoError(t, err)
	select {
	case <-runner.dispatched:
	case <-time.After(10 * time.Second):
		t.Fatal("metadata.apply-when-scanned was not dispatched while library.scan ran")
	}
	select {
	case <-runner.applied:
		t.Fatal("the queued apply took book-1 while the scanner held it")
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseBook)
	select {
	case <-runner.applied:
	case <-time.After(5 * time.Second):
		t.Fatal("the queued apply did not run after the scanner released book-1")
	}
	require.True(t, runner.scanRunningAtApply.Load(),
		"the apply must land while the scan is still running, not after it")
}

// batch-apply-candidates never refuses because a scan is running: a book the
// scanner holds past the bound is handed to metadata.apply-when-scanned and
// listed as queued, and every other book is processed as usual.
func TestBatchApplyCandidates_QueuesTheBookTheScannerHoldsInsteadOf409(t *testing.T) {
	withBulkApplyCapServer(t, 100)
	gin.SetMode(gin.TestMode)
	old := batchBookLockWait
	batchBookLockWait = 30 * time.Millisecond
	defer func() { batchBookLockWait = old }()

	store := newOpsFake(t)
	store.EXPECT().GetOperationResults("op").Return(nil, nil)
	reg := opsregistry.New(store, slog.New(slog.DiscardHandler), 1, nil)
	s := &Server{store: store, opRegistry: reg}
	require.NoError(t, s.RegisterApplyWhenScannedOp(reg))

	scan, err := scanlock.Books.LockSet(context.Background(), []string{"b1"})
	require.NoError(t, err)
	defer scan.Release()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/metadata/batch-apply-candidates",
		strings.NewReader(`{"operation_id":"op","book_ids":["b1","b2"],"dry_run":false}`))
	c.Request.Header.Set("Content-Type", "application/json")
	s.handleBatchApplyCandidates(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), "SCAN_RUNNING")
	var resp struct {
		Data struct {
			Skipped            int      `json:"skipped"`
			QueuedBookIDs      []string `json:"queued_book_ids"`
			QueuedCount        int      `json:"queued_count"`
			QueuedOperationIDs []string `json:"queued_operation_ids"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	got := resp.Data
	require.Equal(t, 1, got.QueuedCount, w.Body.String())
	require.Equal(t, []string{"b1"}, got.QueuedBookIDs)
	require.Len(t, got.QueuedOperationIDs, 1)
	require.NotEmpty(t, got.QueuedOperationIDs[0])
	// b2 has no stored candidate: processed (skipped), not held back by b1.
	require.Equal(t, 1, got.Skipped)
}
