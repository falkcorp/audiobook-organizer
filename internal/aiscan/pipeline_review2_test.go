// file: internal/aiscan/pipeline_review2_test.go
// version: 1.0.0
// guid: a536e5a3-ceaa-46f0-a713-da92ab243ef1
// last-edited: 2026-09-19

package aiscan

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// fakeClock is a settable clock for pm.now.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func pollN(pm *PipelineManager, n int) {
	for range n {
		pm.PollBatchPhases(context.Background())
		time.Sleep(5 * time.Millisecond)
	}
}

// --- F1: no resubmit inside the grace window ----------------------------------

// TestAmbiguousCreateWaitsGraceBeforeResubmit: CreateBatch errored. Inside
// submitGrace an empty listing is not proof the batch does not exist, so
// polls must not resubmit; once the grace has passed they do, once.
func TestAmbiguousCreateWaitsGraceBeforeResubmit(t *testing.T) {
	main := newFakeMainStore(3)
	store := newScanStore(t)
	acct := newFakeAccount()
	llm := newFakeLLM(acct)
	llm.createErr = true
	clock := &fakeClock{t: time.Now()}
	pm := NewPipelineManager(store, main, finderLLM{fakeLLM: llm})
	pm.now = clock.now
	scan, err := pm.CreateScan("batch")
	require.NoError(t, err)
	run := runAsync(pm, scan.ID)
	waitPhase(t, store, scan.ID, "full_scan", "submitting")
	require.Eventually(t, func() bool { return !pm.phaseClaimed(scan.ID, "full_scan") }, 5*time.Second, 5*time.Millisecond)

	pollN(pm, 5)
	_, creates, _ := llm.counts()
	require.Equal(t, 1, creates, "no resubmit inside the grace window")

	clock.advance(submitGrace + time.Minute)
	require.Eventually(t, func() bool {
		pm.PollBatchPhases(context.Background())
		return phaseStatus(t, pm, scan.ID, "full_scan") == "submitted"
	}, 5*time.Second, 10*time.Millisecond)
	_, creates, _ = llm.counts()
	require.Equal(t, 2, creates)
	acct.setAllStatus("completed")
	require.NoError(t, pollUntilDone(t, pm, run))
}

// TestAcceptedButUnlistedBatchIsNotDuplicated: OpenAI accepted the batch but
// the create call errored, and the listing does not show it yet. Polls inside
// the grace must wait; when the listing catches up the batch is attached. One
// batch, never two.
func TestAcceptedButUnlistedBatchIsNotDuplicated(t *testing.T) {
	main := newFakeMainStore(3)
	store := newScanStore(t)
	acct := newFakeAccount()
	llm := newFakeLLM(acct)
	llm.createErr, llm.createErrAfterAccept = true, true
	hide := &atomic.Bool{}
	hide.Store(true)
	pm := NewPipelineManager(store, main, finderLLM{fakeLLM: llm, hide: hide})
	scan, err := pm.CreateScan("batch")
	require.NoError(t, err)
	run := runAsync(pm, scan.ID)
	waitPhase(t, store, scan.ID, "full_scan", "submitting")
	require.Eventually(t, func() bool { return acct.count() == 1 && !pm.phaseClaimed(scan.ID, "full_scan") }, 5*time.Second, 5*time.Millisecond)

	pollN(pm, 5)
	require.Equal(t, 1, acct.count(), "an unlisted batch inside the grace is not proof of absence")

	hide.Store(false)
	acct.setAllStatus("completed")
	require.NoError(t, pollUntilDone(t, pm, run))
	_, creates, _ := llm.counts()
	require.Equal(t, 1, creates)
	require.Equal(t, 1, acct.count())
}

// --- F2: two resolvers, one with a stale view ----------------------------------

// TestConcurrentResolversSubmitOnce: resolver A reads an empty (stale)
// listing and stalls; resolver B runs meanwhile. Whatever B does, A must not
// submit a second batch on its stale view.
func TestConcurrentResolversSubmitOnce(t *testing.T) {
	main := newFakeMainStore(3)
	store := newScanStore(t)
	acct := newFakeAccount()
	llm := newFakeLLM(acct)
	gate := make(chan struct{})
	pm := NewPipelineManager(store, main, finderLLM{fakeLLM: llm, staleFirst: gate, lookups: &atomic.Int32{}})
	pm.now = func() time.Time { return time.Now().Add(2 * submitGrace) }
	scan, err := pm.CreateScan("batch")
	require.NoError(t, err)
	require.NoError(t, store.UpdateScanStatus(scan.ID, "scanning"))
	_, err = store.CreatePhase(scan.ID, "full_scan", "m")
	require.NoError(t, err)
	require.NoError(t, store.UpdatePhaseStatus(scan.ID, "full_scan", "submitting", ""))

	done := make(chan struct{})
	go func() { pm.PollBatchPhases(context.Background()); close(done) }() // A
	<-llm.entered                                                         // A holds its stale view
	pm.PollBatchPhases(context.Background())                              // B
	// Let B's work (if it did any) finish entirely before A acts on its view.
	time.Sleep(100 * time.Millisecond)
	require.Eventually(t, func() bool {
		return !pm.phaseClaimed(scan.ID, "full_scan") || phaseStatus(t, pm, scan.ID, "full_scan") == "submitting"
	}, 5*time.Second, 5*time.Millisecond)
	close(gate)
	<-done
	require.Eventually(t, func() bool {
		return phaseStatus(t, pm, scan.ID, "full_scan") == "submitted" && !pm.phaseClaimed(scan.ID, "full_scan")
	}, 5*time.Second, 5*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	_, creates, _ := llm.counts()
	require.Equal(t, 1, creates, "two resolvers, one batch")
	require.Equal(t, 1, acct.count())
}

// --- F4: a pre-PR scan's pending phase with a nonce-less batch ----------------

// TestLegacyPendingPhaseReattachesNoNonceBatch: before this PR a batch phase
// stayed "pending" until its id was recorded, and batches carried scan_id and
// scan_phase but no nonce. Such a scan resumed by this build must look the
// batch up instead of submitting a paid duplicate.
func TestLegacyPendingPhaseReattachesNoNonceBatch(t *testing.T) {
	main := newFakeMainStore(3)
	store := newScanStore(t)
	acct := newFakeAccount()
	llm := newFakeLLM(acct)
	pm := NewPipelineManager(store, main, finderLLM{fakeLLM: llm})
	scan, err := pm.CreateScan("batch")
	require.NoError(t, err)
	require.NoError(t, store.UpdateScanStatus(scan.ID, "scanning"))
	require.NoError(t, store.UpdatePhaseStatus(scan.ID, "groups_scan", "complete", ""))
	acct.mu.Lock()
	acct.nextID++
	acct.batches["batch_legacy"] = &fakeBatch{id: "batch_legacy", phase: "full_scan", status: "in_progress",
		owner:  map[string]string{ai.BatchMetaScanID: "1", ai.BatchMetaScanPhase: "full_scan"},
		inputs: []ai.AuthorDiscoveryInput{{ID: 1, Name: "A"}}}
	acct.mu.Unlock()

	run := runAsync(pm, scan.ID)
	waitPhase(t, store, scan.ID, "full_scan", "submitted")
	p, err := store.GetPhase(scan.ID, "full_scan")
	require.NoError(t, err)
	require.Equal(t, "batch_legacy", p.BatchID)
	_, creates, _ := llm.counts()
	require.Zero(t, creates, "no paid duplicate of the legacy batch")
	acct.setAllStatus("completed")
	require.NoError(t, pollUntilDone(t, pm, run))
}

// --- F6: a canceled download is not a failed download ------------------------

func TestCanceledDownloadIsNotCounted(t *testing.T) {
	main := newFakeMainStore(3)
	store := newScanStore(t)
	acct := newFakeAccount()
	pm := NewPipelineManager(store, main, newFakeLLM(acct))
	scan, err := pm.CreateScan("batch")
	require.NoError(t, err)
	run := runAsync(pm, scan.ID)
	waitPhase(t, store, scan.ID, "full_scan", "submitted")
	acct.setAllStatus("completed")
	require.Eventually(t, func() bool { return !pm.phaseClaimed(scan.ID, "full_scan") }, 5*time.Second, 5*time.Millisecond)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for range maxDownloadAttempts + 1 {
		pm.PollBatchPhases(canceled)
	}
	require.Equal(t, "submitted", phaseStatus(t, pm, scan.ID, "full_scan"), "cancel-induced download errors are not attempts")
	require.NoError(t, pollUntilDone(t, pm, run))
}

// --- F7: work the poller starts obeys a scan cancel ---------------------------

// TestCanceledScanNotCompletedByPollerStartedWork: the poller collects a batch
// and starts enrichment on its own context. An operator cancels the scan while
// that enrichment runs. The enrichment must stop, and nothing may later
// overwrite the canceled scan as "complete".
func TestCanceledScanNotCompletedByPollerStartedWork(t *testing.T) {
	main := newFakeMainStore(3)
	store := newScanStore(t)
	acct := newFakeAccount()
	llm := newFakeLLM(acct)
	llm.uncertainFirst = true
	llm.enrichGate = make(chan struct{})
	pm := NewPipelineManager(store, main, llm)
	scan, err := pm.CreateScan("batch")
	require.NoError(t, err)
	run := runAsync(pm, scan.ID)
	waitPhase(t, store, scan.ID, "full_scan", "submitted")
	acct.setAllStatus("completed")

	// Collect as the batch poller does (its own context), until full_enrich
	// is in flight.
	func() {
		for {
			select {
			case <-llm.entered:
				return
			case <-time.After(5 * time.Millisecond):
				pm.PollBatchPhases(context.Background())
			}
		}
	}()
	require.NoError(t, pm.CancelScan(scan.ID))
	close(llm.enrichGate)
	require.ErrorIs(t, awaitRun(t, run), ErrScanCanceled)

	time.Sleep(100 * time.Millisecond)
	got, err := store.GetScan(scan.ID)
	require.NoError(t, err)
	require.Equal(t, "canceled", got.Status)
	cv, err := store.GetPhase(scan.ID, "cross_validate")
	require.NoError(t, err)
	if cv != nil {
		require.NotEqual(t, "complete", cv.Status)
	}
	var _ database.Scan
}
