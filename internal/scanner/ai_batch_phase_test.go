// file: internal/scanner/ai_batch_phase_test.go
// version: 1.1.0
// guid: db86f424-3881-4c7b-8ca5-4e00086f62cf
// last-edited: 2026-08-24

package scanner

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

type fakeAIParser struct {
	mu        sync.Mutex
	inFlight  int
	maxSeen   int
	calls     atomic.Int64
	delay     time.Duration
	err       error
	errNTimes int64
}

func (f *fakeAIParser) ParseBatch(ctx context.Context, filenames []string) ([]*ai.ParsedMetadata, error) {
	f.mu.Lock()
	f.inFlight++
	if f.inFlight > f.maxSeen {
		f.maxSeen = f.inFlight
	}
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.inFlight--; f.mu.Unlock() }()

	n := f.calls.Add(1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.err != nil && (f.errNTimes == 0 || n <= f.errNTimes) {
		return nil, f.err
	}
	out := make([]*ai.ParsedMetadata, len(filenames))
	for i := range out {
		out[i] = &ai.ParsedMetadata{Title: "parsed"}
	}
	return out, nil
}

func makeCandidates(n int) ([]Book, []int) {
	books := make([]Book, n)
	idx := make([]int, n)
	for i := range books {
		books[i] = Book{FilePath: "/x/file.m4b"}
		idx[i] = i
	}
	return books, idx
}

// The point of the change: batches must overlap. A serial loop yields
// maxSeen == 1 no matter how many batches there are.
func TestRunAIBatchPhase_RunsBatchesConcurrently(t *testing.T) {
	books, cands := makeCandidates(20 * 8) // 8 batches
	f := &fakeAIParser{delay: 60 * time.Millisecond}

	runAIBatchPhase(context.Background(), f, books, cands, logger.New("test"), saveBookAndReportPath)

	if f.maxSeen < 2 {
		t.Fatalf("max concurrent batches was %d: the phase is still serial, which is "+
			"the whole cost this change exists to remove", f.maxSeen)
	}
	if f.maxSeen > aiBatchWorkers {
		t.Errorf("max concurrent batches %d exceeds the bound of %d: unbounded fan-out "+
			"at a single model host", f.maxSeen, aiBatchWorkers)
	}
	if got := f.calls.Load(); got != 8 {
		t.Errorf("expected all 8 batches attempted, got %d", got)
	}
}

// A permanent backend failure must stop the phase, not be retried once per
// remaining batch. The serial version did this and the concurrent one must too.
func TestRunAIBatchPhase_PermanentFailureAbortsRemainingBatches(t *testing.T) {
	books, cands := makeCandidates(20 * 40) // 40 batches
	// Fails ONLY on the very first invocation, every subsequent call succeeds.
	// This is the assertion that actually distinguishes "abort on the first
	// permanent failure" from "abort after maxTotalFailures": with
	// aiBatchWorkers(4) > maxTotalFailures(3), a backend that fails on EVERY
	// call trips the count-based threshold within the same first concurrent
	// wave immediate-abort would also stop at, so the two are indistinguishable
	// by call count alone (verified: that was this test's original, useless
	// form). A single permanent failure can never reach a threshold of 3 by
	// itself -- only the immediate-abort path stops the run over it.
	f := &fakeAIParser{err: errors.New("insufficient_quota: credit balance exhausted"), errNTimes: 1}

	runAIBatchPhase(context.Background(), f, books, cands, logger.New("test"), saveBookAndReportPath)

	if got := f.calls.Load(); got >= 40 {
		t.Errorf("%d/40 batches ran after ONE permanent failure: a permanent failure "+
			"must abort immediately, not wait for a failure count that a single "+
			"non-retryable error can never reach on its own "+
			"(this is the 25-minutes-of-useless-work incident)", got)
	}
}

// Transient failures must also stop the phase once enough of them accumulate.
func TestRunAIBatchPhase_RepeatedFailuresAbort(t *testing.T) {
	books, cands := makeCandidates(20 * 40)
	f := &fakeAIParser{err: errors.New("connection reset by peer")}

	start := time.Now()
	runAIBatchPhase(context.Background(), f, books, cands, logger.New("test"), saveBookAndReportPath)
	elapsed := time.Since(start)

	if got := f.calls.Load(); got >= 40 {
		t.Errorf("all %d batches attempted despite repeated failures: the failure "+
			"threshold never fired", got)
	}
	if elapsed > 60*time.Second {
		t.Errorf("phase took %v: it is grinding through batches instead of aborting", elapsed)
	}
}

// The converse, so the abort cannot pass by aborting everything: a healthy
// backend must see every batch through.
func TestRunAIBatchPhase_HealthyBackendCompletesEveryBatch(t *testing.T) {
	books, cands := makeCandidates(20 * 6)
	f := &fakeAIParser{}

	runAIBatchPhase(context.Background(), f, books, cands, logger.New("test"), saveBookAndReportPath)

	if got := f.calls.Load(); got != 6 {
		t.Errorf("expected 6 batches on a healthy backend, got %d -- the phase is "+
			"aborting when nothing is wrong", got)
	}
}

// A cancelled scan must not keep issuing AI requests.
func TestRunAIBatchPhase_HonoursContextCancellation(t *testing.T) {
	books, cands := makeCandidates(20 * 40)
	f := &fakeAIParser{delay: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() { defer close(done); runAIBatchPhase(ctx, f, books, cands, logger.New("test"), saveBookAndReportPath) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("phase did not return promptly on a cancelled context")
	}
	if got := f.calls.Load(); got >= 40 {
		t.Errorf("issued %d batches on a cancelled context", got)
	}
}
