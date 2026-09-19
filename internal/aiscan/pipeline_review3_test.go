// file: internal/aiscan/pipeline_review3_test.go
// version: 1.0.0
// guid: f2827fb9-b355-4830-8e1c-f6fe5a025cd8
// last-edited: 2026-09-19

package aiscan

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/stretchr/testify/require"
)

// --- 1: a batch created or found after a cancel is canceled, not attached ----

// TestBatchCreatedAfterCancelIsCanceled: the operator cancels while CreateBatch
// is in flight. When CreateBatch returns, its batch id must not be written
// onto the canceled phase; the batch it just paid for is canceled instead.
func TestBatchCreatedAfterCancelIsCanceled(t *testing.T) {
	main := newFakeMainStore(3)
	store := newScanStore(t)
	acct := newFakeAccount()
	llm := newFakeLLM(acct)
	llm.createGate = make(chan struct{})
	pm := NewPipelineManager(store, main, llm)
	scan, err := pm.CreateScan("batch")
	require.NoError(t, err)
	run := runAsync(pm, scan.ID)
	<-llm.entered // OpenAI has accepted batch_1; the response is in flight

	require.NoError(t, pm.CancelScan(scan.ID))
	require.ErrorIs(t, awaitRun(t, run), ErrScanCanceled)
	close(llm.createGate)

	require.Eventually(t, func() bool {
		for _, id := range llm.canceled() {
			if id == "batch_1" {
				return true
			}
		}
		return false
	}, 5*time.Second, 5*time.Millisecond, "the batch created after the cancel must be canceled")
	require.Equal(t, "canceled", phaseStatus(t, pm, scan.ID, "full_scan"))
}

// TestCancelScanCancelsBatchOfSubmittingPhase: a phase left "submitting" (its
// process died after OpenAI accepted the batch) has no recorded batch id.
// Canceling the scan — here with no op attached in this process — must look
// the batch up and cancel it.
func TestCancelScanCancelsBatchOfSubmittingPhase(t *testing.T) {
	main := newFakeMainStore(3)
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

	llm2 := newFakeLLM(acct)
	pm2 := NewPipelineManager(store, main, finderLLM{fakeLLM: llm2})
	require.NoError(t, pm2.CancelScan(scan.ID), "a live scan row is cancelable without an attached op")
	require.Equal(t, []string{"batch_1"}, llm2.canceled())
	got, err := store.GetScan(scan.ID)
	require.NoError(t, err)
	require.Equal(t, "canceled", got.Status)
}

// --- 2: a failed phase closes out its sibling's live batch --------------------

func TestFailedPhaseCancelsSiblingBatch(t *testing.T) {
	main := newNamedMainStore("Brandon Sanderson", "Brandon Sandersen", "Isaac Asimov")
	store := newScanStore(t)
	acct := newFakeAccount()
	llm := newFakeLLM(acct)
	pm := NewPipelineManager(store, main, llm)
	scan, err := pm.CreateScan("batch")
	require.NoError(t, err)
	run := runAsync(pm, scan.ID)
	waitPhase(t, store, scan.ID, "groups_scan", "submitted")
	waitPhase(t, store, scan.ID, "full_scan", "submitted")
	full, err := store.GetPhase(scan.ID, "full_scan")
	require.NoError(t, err)
	groups, err := store.GetPhase(scan.ID, "groups_scan")
	require.NoError(t, err)

	acct.mu.Lock()
	acct.batches[full.BatchID].status, acct.batches[full.BatchID].noOutput = "failed", true
	acct.mu.Unlock()
	require.Eventually(t, func() bool { return !pm.phaseClaimed(scan.ID, "full_scan") }, 5*time.Second, 5*time.Millisecond)
	pm.PollBatchPhases(context.Background())
	require.Error(t, awaitRun(t, run))

	require.Eventually(t, func() bool {
		for _, id := range llm.canceled() {
			if id == groups.BatchID {
				return true
			}
		}
		return false
	}, 5*time.Second, 5*time.Millisecond, "the sibling's live batch must be canceled")
	require.NotEqual(t, "submitted", phaseStatus(t, pm, scan.ID, "groups_scan"))
	require.NoError(t, pm.CollectBatch(context.Background(), groups.BatchID), "a dead scan's batch is handled, not retried forever")
}

// --- 3: slow collection never starves the heartbeat ---------------------------

type countingSink struct {
	mu sync.Mutex
	n  int
}

func (c *countingSink) UpdateProgress(int, int, string) error {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return nil
}

func (c *countingSink) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// TestSlowCollectionKeepsHeartbeat: the heartbeat both reports progress (the
// registry watchdog cancels an op silent past ProgressTimeout, and that cancel
// would cancel the paid batches) and polls the scan's batches. A slow poll must
// not stall the progress reports or the response to ctx.Done.
func TestSlowCollectionKeepsHeartbeat(t *testing.T) {
	main := newFakeMainStore(3)
	store := newScanStore(t)
	acct := newFakeAccount()
	llm := newFakeLLM(acct)
	pm := NewPipelineManager(store, main, llm)
	pm.heartbeat = 10 * time.Millisecond
	scan, err := pm.CreateScan("batch")
	require.NoError(t, err)
	sink := &countingSink{}
	ctx, cancel := context.WithCancel(context.Background())
	run := make(chan error, 1)
	go func() { run <- pm.RunScan(ctx, scan.ID, sink) }()
	waitPhase(t, store, scan.ID, "full_scan", "submitted")

	llm.mu.Lock()
	llm.checkDelay = 400 * time.Millisecond // each status check is slow
	llm.mu.Unlock()
	before := sink.count()
	time.Sleep(300 * time.Millisecond)
	require.GreaterOrEqual(t, sink.count()-before, 5, "progress must keep flowing while a slow poll runs")

	start := time.Now()
	cancel()
	require.Error(t, awaitRun(t, run))
	require.Less(t, time.Since(start), 200*time.Millisecond, "ctx.Done must be serviced during a slow poll")
}

// --- 4: external results replay ------------------------------------------------

// TestRecordFullScanResultsReplayOfSupersededScanIsDone: a replay of a run
// whose scan was since superseded must leave it superseded — not rewrite its
// results and mark it complete again.
func TestRecordFullScanResultsReplayOfSupersededScanIsDone(t *testing.T) {
	store := newScanStore(t)
	sugg := []ai.AuthorDiscoverySuggestion{{AuthorIDs: []int{1, 2}, Action: "merge", CanonicalName: "A", Confidence: "high"}}
	first, err := RecordFullScanResults(store, "night-1", sugg)
	require.NoError(t, err)
	_, err = RecordFullScanResults(store, "night-2", sugg)
	require.NoError(t, err)
	before, err := store.GetScanResults(first)
	require.NoError(t, err)

	id, err := RecordFullScanResults(store, "night-1", sugg)
	require.NoError(t, err)
	require.Equal(t, first, id)
	got, err := store.GetScan(first)
	require.NoError(t, err)
	require.Equal(t, "superseded", got.Status)
	after, err := store.GetScanResults(first)
	require.NoError(t, err)
	require.Equal(t, before[0].ID, after[0].ID, "results untouched")
}
