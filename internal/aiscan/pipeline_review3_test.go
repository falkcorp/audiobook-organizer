// file: internal/aiscan/pipeline_review3_test.go
// version: 1.3.0
// guid: f2827fb9-b355-4830-8e1c-f6fe5a025cd8
// last-edited: 2026-10-14

package aiscan

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/lifecycle"
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
	awaitGroupsSettled(t, pm1, store, scan.ID)

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
//
// The poll is held at a gate the test controls rather than slowed by a sleep,
// so both properties are asserted without a wall-clock bound: progress must
// arrive and RunScan must return while the poll is provably still in flight.
// A loop that waited for the poll would never report or return before the
// gate opens. (This used to sleep 400ms per status check and require cancel
// to return within 200ms, which flaked under -race at 201ms and could not
// catch a regression anyway: the poll in flight at cancel had only ~110ms
// of its 400ms left.)
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
	defer cancel()
	// run carries RunScan's result to the test body; runDone closes when the
	// goroutine exits, so the cleanup can wait on it whether or not the body
	// already received from run.
	run, runDone := make(chan error, 1), make(chan struct{})
	go func() {
		defer close(runDone)
		run <- pm.RunScan(ctx, scan.ID, sink)
	}()
	waitPhase(t, store, scan.ID, "full_scan", "submitted")

	gate, entered := make(chan struct{}), make(chan struct{}, 1)
	llm.mu.Lock()
	llm.checkGate, llm.checkEntered = gate, entered
	llm.mu.Unlock()
	// Release the held poll, stop RunScan, and wait for both to drain before
	// the store closes (cleanups run LIFO, so this runs before newScanStore's).
	// RunScan must be drained here too: a failure before the test's own
	// cancel() would otherwise leave it running against a closed store.
	t.Cleanup(func() {
		close(gate)
		cancel()
		select {
		case <-runDone:
		case <-time.After(5 * time.Second):
			t.Error("RunScan did not return after cancel in cleanup")
		}
		require.Eventually(t, func() bool {
			pm.mu.Lock()
			defer pm.mu.Unlock()
			return !pm.polling[scan.ID]
		}, 5*time.Second, 5*time.Millisecond, "the released poll must finish")
	})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no heartbeat poll reached CheckBatchStatus")
	}

	before := sink.count()
	require.Eventually(t, func() bool { return sink.count()-before >= 5 }, 5*time.Second, 5*time.Millisecond,
		"progress must keep flowing while a poll is held")

	cancel()
	select {
	case err := <-run:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("ctx.Done must be serviced while a poll is held: RunScan did not return")
	}
}

// --- 4: external results replay ------------------------------------------------

// TestRecordFullScanResultsReplayOfSupersededScanIsDone: a replay of a run
// whose scan was since superseded must leave it superseded — not rewrite its
// results and mark it complete again.
func TestRecordFullScanResultsReplayOfSupersededScanIsDone(t *testing.T) {
	store := newScanStore(t)
	sugg := []ai.AuthorDiscoverySuggestion{{AuthorIDs: []int{1, 2}, Action: "merge", CanonicalName: "A", Confidence: "high"}}
	first, err := RecordFullScanResults(store, "night-1", sugg, nil)
	require.NoError(t, err)
	_, err = RecordFullScanResults(store, "night-2", sugg, nil)
	require.NoError(t, err)
	before, err := store.GetScanResults(first)
	require.NoError(t, err)

	id, err := RecordFullScanResults(store, "night-1", sugg, nil)
	require.NoError(t, err)
	require.Equal(t, first, id)
	got, err := store.GetScan(first)
	require.NoError(t, err)
	require.Equal(t, "superseded", got.Status)
	after, err := store.GetScanResults(first)
	require.NoError(t, err)
	require.Equal(t, before[0].ID, after[0].ID, "results untouched")
}

// --- a scan that could not be run is ended, not left blocking every start -----

// authorsFailAfterCreate lets CreateScan read the library, then fails the read RunScan
// makes when it launches the first source phase: a drive error.
type authorsFailAfterCreate struct {
	*fakeMainStore
	fail atomic.Bool
}

func (a *authorsFailAfterCreate) GetAllAuthors() ([]database.Author, error) {
	if a.fail.Load() {
		return nil, errors.New("authors unreadable")
	}
	return a.fakeMainStore.GetAllAuthors()
}

// shortStoreRetries makes retryStore's waits a millisecond for the test.
func shortStoreRetries(t *testing.T) {
	t.Helper()
	old := storeRetryDelays
	storeRetryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { storeRetryDelays = old })
}

// A drive error that outlasts every retry still ends the scan.
func TestRunScan_ADriveErrorEndsTheScanSoANewOneCanStart(t *testing.T) {
	shortStoreRetries(t)
	main := &authorsFailAfterCreate{fakeMainStore: newFakeMainStore(3)}
	store := newScanStore(t)
	pm := NewPipelineManager(store, main, newFakeLLM(newFakeAccount()))
	scan, err := pm.CreateScan("realtime")
	require.NoError(t, err)
	main.fail.Store(true)

	err = pm.RunScan(context.Background(), scan.ID, nil)

	require.ErrorContains(t, err, "authors unreadable")
	got, gerr := store.GetScan(scan.ID)
	require.NoError(t, gerr)
	require.Equal(t, "failed", got.Status, "an op that returned an error leaves no live scan behind")
	require.Equal(t, "failed", phaseStatus(t, pm, scan.ID, "groups_scan"))
	require.Equal(t, "failed", phaseStatus(t, pm, scan.ID, "full_scan"))

	main.fail.Store(false)
	next, nerr := pm.CreateScan("realtime")
	require.NoError(t, nerr, "the failed scan must not block the next start")
	require.NotEqual(t, scan.ID, next.ID)
}

// A phase left "submitting" may own a paid batch. Ending the scan cancels it, by
// looking it up when no id was ever recorded, exactly as CancelScan does.
func TestAbortScan_CancelsTheBatchOfASubmittingPhaseAndFailsTheScan(t *testing.T) {
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
	awaitGroupsSettled(t, pm1, store, scan.ID)

	llm2 := newFakeLLM(acct)
	pm2 := NewPipelineManager(store, main, finderLLM{fakeLLM: llm2})
	pm2.abortScan(context.Background(), scan.ID, errors.New("could not be driven"))

	require.Equal(t, []string{"batch_1"}, llm2.canceled())
	got, gerr := store.GetScan(scan.ID)
	require.NoError(t, gerr)
	require.Equal(t, "failed", got.Status)
	require.Equal(t, "failed", phaseStatus(t, pm2, scan.ID, "full_scan"))
}

// A server shutdown is not a failure: the op resumes the scan after the restart.
func TestAbortScan_LeavesTheScanForTheResumeOnShutdown(t *testing.T) {
	main := newFakeMainStore(3)
	store := newScanStore(t)
	pm := NewPipelineManager(store, main, newFakeLLM(newFakeAccount()))
	scan, err := pm.CreateScan("realtime")
	require.NoError(t, err)

	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(lifecycle.ErrShutdown)
	pm.abortScan(ctx, scan.ID, errors.New("store closing"))

	got, gerr := store.GetScan(scan.ID)
	require.NoError(t, gerr)
	require.Equal(t, "pending", got.Status)
}

// authorsFailNTimes fails the author read RunScan makes (after CreateScan's) the
// first n times, then answers: a transient local store error.
type authorsFailNTimes struct {
	*fakeMainStore
	armed  atomic.Bool
	left   atomic.Int32
	failed atomic.Int32
}

func (a *authorsFailNTimes) GetAllAuthors() ([]database.Author, error) {
	if a.armed.Load() && a.left.Add(-1) >= 0 {
		a.failed.Add(1)
		return nil, errors.New("authors briefly unreadable")
	}
	return a.fakeMainStore.GetAllAuthors()
}

// A transient store error while starting the phases is retried, not answered by
// abortScan, which fails the scan and cancels its paid batches. The retried scan
// runs exactly as an undisturbed one: no phase is launched twice.
func TestRunScan_ATransientDriveErrorIsRetriedAndLaunchesEachPhaseOnce(t *testing.T) {
	shortStoreRetries(t)
	run := func(failures int32) (*database.Scan, *fakeLLM, *database.AIScanStore) {
		main := &authorsFailNTimes{fakeMainStore: newFakeMainStore(3)}
		store := newScanStore(t)
		llm := newFakeLLM(newFakeAccount())
		pm := NewPipelineManager(store, main, llm)
		scan, err := pm.CreateScan("realtime")
		require.NoError(t, err)
		main.left.Store(failures)
		main.armed.Store(true)
		require.NoError(t, pm.RunScan(context.Background(), scan.ID, nil))
		require.Equal(t, failures, main.failed.Load(), "every injected failure was hit")
		return scan, llm, store
	}
	_, base, baseStore := run(0)
	scan, retried, store := run(2)

	got, err := store.GetScan(scan.ID)
	require.NoError(t, err)
	require.Equal(t, "complete", got.Status, "the scan ran to the end")
	base.mu.Lock()
	want := len(base.discoverCalls)
	base.mu.Unlock()
	retried.mu.Lock()
	have := len(retried.discoverCalls)
	retried.mu.Unlock()
	require.Equal(t, want, have, "the same LLM calls as an undisturbed run: nothing launched twice")
	baseResults, err := baseStore.GetScanResults(1)
	require.NoError(t, err)
	results, err := store.GetScanResults(scan.ID)
	require.NoError(t, err)
	require.Len(t, results, len(baseResults))
}

// retryStore stops waiting when its context ends: a canceled scan does not sit
// out the backoff before it is let go.
func TestRetryStore_StopsWaitingWhenTheContextEnds(t *testing.T) {
	old := storeRetryDelays
	storeRetryDelays = []time.Duration{time.Hour, time.Hour}
	t.Cleanup(func() { storeRetryDelays = old })

	pm := NewPipelineManager(newScanStore(t), newFakeMainStore(1), newFakeLLM(newFakeAccount()))
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	boom := errors.New("store unreadable")
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	start := time.Now()
	err := pm.retryStore(ctx, 1, "testing", func() error { calls++; return boom })
	require.ErrorIs(t, err, boom, "the last error comes back")
	require.Equal(t, 1, calls, "no attempt after the context ended")
	require.Less(t, time.Since(start), 10*time.Second)
}

// retryStore makes len(storeRetryDelays)+1 attempts and no more.
func TestRetryStore_IsBounded(t *testing.T) {
	shortStoreRetries(t)
	pm := NewPipelineManager(newScanStore(t), newFakeMainStore(1), newFakeLLM(newFakeAccount()))
	calls := 0
	err := pm.retryStore(context.Background(), 1, "testing", func() error { calls++; return errors.New("down") })
	require.Error(t, err)
	require.Equal(t, len(storeRetryDelays)+1, calls)
}
