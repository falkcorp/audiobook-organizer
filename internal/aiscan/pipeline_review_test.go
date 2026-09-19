// file: internal/aiscan/pipeline_review_test.go
// version: 1.1.0
// guid: 8a6674cc-107e-46c1-bf79-9bd8302491e2
// last-edited: 2026-09-19

package aiscan

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/lifecycle"
	"github.com/stretchr/testify/require"
)

// runWithCtx starts RunScan under ctx.
func runWithCtx(ctx context.Context, pm *PipelineManager, scanID int) <-chan error {
	out := make(chan error, 1)
	go func() { out <- pm.RunScan(ctx, scanID, nil) }()
	return out
}

func phaseStatus(t *testing.T, pm *PipelineManager, scanID int, phase string) string {
	t.Helper()
	p, err := pm.scanStore.GetPhase(scanID, phase)
	require.NoError(t, err)
	require.NotNil(t, p)
	return p.Status
}

// --- finding 1: a restart is not a cancel ------------------------------------

// TestShutdownLeavesBatchRunningForResume: the registry cancels every running
// op on a graceful restart. For a batch scan that must NOT cancel the paid
// OpenAI batch or mark the scan canceled — the resumed op re-attaches to it.
func TestShutdownLeavesBatchRunningForResume(t *testing.T) {
	main := newFakeMainStore(4)
	store := newScanStore(t)
	acct := newFakeAccount()
	llm1 := newFakeLLM(acct)
	pm1 := NewPipelineManager(store, main, llm1)
	scan, err := pm1.CreateScan("batch")
	require.NoError(t, err)

	ctx, cancel := context.WithCancelCause(context.Background())
	run := runWithCtx(ctx, pm1, scan.ID)
	waitPhase(t, store, scan.ID, "full_scan", "submitted")
	cancel(lifecycle.ErrShutdown)
	require.ErrorIs(t, awaitRun(t, run), context.Canceled)

	llm1.mu.Lock()
	cancels := llm1.cancels
	llm1.mu.Unlock()
	require.Zero(t, cancels, "a restart must not cancel the batch OpenAI is running")
	require.Equal(t, "submitted", phaseStatus(t, pm1, scan.ID, "full_scan"))
	got, err := store.GetScan(scan.ID)
	require.NoError(t, err)
	require.Equal(t, "scanning", got.Status)

	llm2 := newFakeLLM(acct)
	pm2 := NewPipelineManager(store, main, llm2)
	run2 := runAsync(pm2, scan.ID)
	acct.setAllStatus("completed")
	require.NoError(t, pollUntilDone(t, pm2, run2))
	require.Len(t, resultKeys(t, store, scan.ID), 4)
}

// TestShutdownLeavesRealtimeScanResumable: the in-flight chunk's request dies
// with the shutdown. The phase must stay "processing" (not failed) so the
// resumed run continues from the persisted chunks.
func TestShutdownLeavesRealtimeScanResumable(t *testing.T) {
	withChunkSize(t, 2)
	main := newFakeMainStore(5)
	store := newScanStore(t)
	llm1 := newFakeLLM(newFakeAccount())
	llm1.blockDiscoverUntilCtx = 1
	pm1 := NewPipelineManager(store, main, llm1)
	scan, err := pm1.CreateScan("realtime")
	require.NoError(t, err)

	ctx, cancel := context.WithCancelCause(context.Background())
	run := runWithCtx(ctx, pm1, scan.ID)
	<-llm1.entered
	waitPhase(t, store, scan.ID, "groups_enrich", "complete")
	cancel(lifecycle.ErrShutdown)
	require.ErrorIs(t, awaitRun(t, run), context.Canceled)
	// Let the dying chunk goroutine observe the cancel and return.
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, "processing", phaseStatus(t, pm1, scan.ID, "full_scan"))

	llm2 := newFakeLLM(newFakeAccount())
	pm2 := NewPipelineManager(store, main, llm2)
	require.NoError(t, awaitRun(t, runAsync(pm2, scan.ID)))
	llm2.mu.Lock()
	calls := llm2.discoverCalls
	llm2.mu.Unlock()
	require.Equal(t, [][]int{{3, 4}, {5}}, calls, "chunk 0 was persisted before the restart")
	require.Len(t, resultKeys(t, store, scan.ID), 5)
}

// TestOperatorCancelStillCancelsBatch: only a real cancel stops the batch.
func TestOperatorCancelStillCancelsBatch(t *testing.T) {
	main := newFakeMainStore(4)
	store := newScanStore(t)
	llm := newFakeLLM(newFakeAccount())
	pm := NewPipelineManager(store, main, llm)
	scan, err := pm.CreateScan("batch")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	run := runWithCtx(ctx, pm, scan.ID)
	waitPhase(t, store, scan.ID, "full_scan", "submitted")
	cancel()
	require.Error(t, awaitRun(t, run))
	llm.mu.Lock()
	require.Equal(t, 1, llm.cancels)
	llm.mu.Unlock()
	got, err := store.GetScan(scan.ID)
	require.NoError(t, err)
	require.Equal(t, "canceled", got.Status)
}

// --- finding 5: an ambiguous create or a failed lookup must not orphan a batch

// TestCreateErrorAfterAcceptReattaches: CreateBatch errored but OpenAI had
// accepted the batch. The phase stays "submitting" and the next poll finds and
// attaches the batch — one batch, not a failed phase plus an orphan.
func TestCreateErrorAfterAcceptReattaches(t *testing.T) {
	main := newFakeMainStore(4)
	store := newScanStore(t)
	acct := newFakeAccount()
	llm := newFakeLLM(acct)
	llm.createErr, llm.createErrAfterAccept = true, true
	pm := NewPipelineManager(store, main, finderLLM{fakeLLM: llm})
	scan, err := pm.CreateScan("batch")
	require.NoError(t, err)
	run := runAsync(pm, scan.ID)
	waitPhase(t, store, scan.ID, "full_scan", "submitting")
	require.Eventually(t, func() bool { return acct.count() == 1 }, 5*time.Second, 5*time.Millisecond)

	acct.setAllStatus("completed")
	require.NoError(t, pollUntilDone(t, pm, run))
	_, creates, _ := llm.counts()
	require.Equal(t, 1, creates)
	require.Equal(t, 1, acct.count())
	require.Len(t, resultKeys(t, store, scan.ID), 4)
}

// TestCreateErrorConfirmedAbsentResubmits: CreateBatch errored and the lookup
// confirms no batch exists, so submitting again is the only submission.
func TestCreateErrorConfirmedAbsentResubmits(t *testing.T) {
	main := newFakeMainStore(4)
	store := newScanStore(t)
	acct := newFakeAccount()
	llm := newFakeLLM(acct)
	llm.createErr = true
	pm := NewPipelineManager(store, main, finderLLM{fakeLLM: llm})
	clock := &fakeClock{t: time.Now()}
	pm.now = clock.now
	scan, err := pm.CreateScan("batch")
	require.NoError(t, err)
	run := runAsync(pm, scan.ID)
	waitPhase(t, store, scan.ID, "full_scan", "submitting")
	require.Eventually(t, func() bool { return !pm.phaseClaimed(scan.ID, "full_scan") }, 5*time.Second, 5*time.Millisecond)
	clock.advance(submitGrace + time.Minute) // past the grace: absence is now confirmed
	// Poll as the heartbeat does until the confirmed-absent resubmit lands. A
	// poll during the first (still running) submit is a no-op.
	require.Eventually(t, func() bool {
		pm.PollBatchPhases(context.Background())
		return phaseStatus(t, pm, scan.ID, "full_scan") == "submitted"
	}, 5*time.Second, 10*time.Millisecond)

	acct.setAllStatus("completed")
	require.NoError(t, pollUntilDone(t, pm, run))
	_, creates, _ := llm.counts()
	require.Equal(t, 2, creates)
	require.Equal(t, 1, acct.count(), "only the second create reached OpenAI")
}

// TestReattachLookupErrorKeepsSubmitting: a failed lookup at resume is not a
// verdict. The phase stays "submitting" and a later poll attaches it.
func TestReattachLookupErrorKeepsSubmitting(t *testing.T) {
	main := newFakeMainStore(4)
	store := newScanStore(t)
	acct := newFakeAccount()
	llm1 := newFakeLLM(acct)
	llm1.blockAfterCreate = true
	pm1 := NewPipelineManager(store, main, llm1)
	scan, err := pm1.CreateScan("batch")
	require.NoError(t, err)
	_ = runAsync(pm1, scan.ID)
	<-llm1.entered
	waitPhase(t, store, scan.ID, "full_scan", "submitting")

	lookupErr := errors.New("list batches: 503")
	llm2 := newFakeLLM(acct)
	pm2 := NewPipelineManager(store, main, finderLLM{fakeLLM: llm2, err: &lookupErr})
	run := runAsync(pm2, scan.ID)
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, "submitting", phaseStatus(t, pm2, scan.ID, "full_scan"))

	lookupErr = nil
	acct.setAllStatus("completed")
	require.NoError(t, pollUntilDone(t, pm2, run))
	_, creates, _ := llm2.counts()
	require.Zero(t, creates)
	require.Equal(t, 1, acct.count())
}

// TestBatchCarriesScanNonce: the owner metadata names the scan by id, phase
// AND a per-scan nonce, so a reused scan id cannot attach another scan's batch.
func TestBatchCarriesScanNonce(t *testing.T) {
	main := newFakeMainStore(3)
	store := newScanStore(t)
	acct := newFakeAccount()
	pm := NewPipelineManager(store, main, newFakeLLM(acct))
	scan, err := pm.CreateScan("batch")
	require.NoError(t, err)
	_ = runAsync(pm, scan.ID)
	waitPhase(t, store, scan.ID, "full_scan", "submitted")
	acct.mu.Lock()
	defer acct.mu.Unlock()
	for _, b := range acct.batches {
		require.Equal(t, strconv.Itoa(scan.ID), b.owner[ai.BatchMetaScanID])
		require.Equal(t, strconv.FormatInt(scan.CreatedAt.UnixNano(), 10), b.owner[ai.BatchMetaScanNonce])
	}
}

// --- finding 9: partial output and transient download errors -----------------

// TestExpiredBatchWithOutputIsCollected: OpenAI expired the batch but it holds
// output for the requests that finished; that output is collected.
func TestExpiredBatchWithOutputIsCollected(t *testing.T) {
	main := newFakeMainStore(4)
	store := newScanStore(t)
	acct := newFakeAccount()
	pm := NewPipelineManager(store, main, newFakeLLM(acct))
	scan, err := pm.CreateScan("batch")
	require.NoError(t, err)
	run := runAsync(pm, scan.ID)
	waitPhase(t, store, scan.ID, "full_scan", "submitted")
	acct.setAllStatus("expired")
	require.NoError(t, pollUntilDone(t, pm, run))
	require.Len(t, resultKeys(t, store, scan.ID), 4)
}

// TestExpiredBatchWithoutOutputFails: nothing to collect is a real failure.
func TestExpiredBatchWithoutOutputFails(t *testing.T) {
	main := newFakeMainStore(4)
	store := newScanStore(t)
	acct := newFakeAccount()
	pm := NewPipelineManager(store, main, newFakeLLM(acct))
	scan, err := pm.CreateScan("batch")
	require.NoError(t, err)
	run := runAsync(pm, scan.ID)
	waitPhase(t, store, scan.ID, "full_scan", "submitted")
	acct.mu.Lock()
	for _, b := range acct.batches {
		b.status, b.noOutput = "expired", true
	}
	acct.mu.Unlock()
	require.Error(t, pollUntilDone(t, pm, run))
	require.Equal(t, "failed", phaseStatus(t, pm, scan.ID, "full_scan"))
}

// TestTransientDownloadErrorRetries: a download error leaves the phase
// submitted; the next poll collects.
func TestTransientDownloadErrorRetries(t *testing.T) {
	main := newFakeMainStore(4)
	store := newScanStore(t)
	acct := newFakeAccount()
	llm := newFakeLLM(acct)
	llm.downloadFails = 2
	pm := NewPipelineManager(store, main, llm)
	scan, err := pm.CreateScan("batch")
	require.NoError(t, err)
	run := runAsync(pm, scan.ID)
	waitPhase(t, store, scan.ID, "full_scan", "submitted")
	acct.setAllStatus("completed")
	pm.PollBatchPhases(context.Background())
	require.Equal(t, "submitted", phaseStatus(t, pm, scan.ID, "full_scan"), "one failed download is not a failed phase")
	require.NoError(t, pollUntilDone(t, pm, run))
	require.Len(t, resultKeys(t, store, scan.ID), 4)
}

// --- finding 2: the poller handler reports whether the batch was collected ----

func TestCollectBatchReportsUncollected(t *testing.T) {
	main := newFakeMainStore(4)
	store := newScanStore(t)
	acct := newFakeAccount()
	pm := NewPipelineManager(store, main, newFakeLLM(acct))
	scan, err := pm.CreateScan("batch")
	require.NoError(t, err)
	run := runAsync(pm, scan.ID)
	waitPhase(t, store, scan.ID, "full_scan", "submitted")
	p, err := store.GetPhase(scan.ID, "full_scan")
	require.NoError(t, err)

	// Not complete at OpenAI yet: the phase is still owed, so not handled.
	require.Error(t, pm.CollectBatch(context.Background(), p.BatchID))

	acct.setAllStatus("completed")
	require.Eventually(t, func() bool { return pm.CollectBatch(context.Background(), p.BatchID) == nil },
		5*time.Second, 10*time.Millisecond)
	require.NoError(t, awaitRun(t, run))

	// A batch no scan phase owns (and no phase awaiting attachment) is not ours.
	require.NoError(t, pm.CollectBatch(context.Background(), "batch_not_ours"))
}
