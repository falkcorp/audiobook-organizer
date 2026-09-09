// file: internal/scanner/ai_batch_configurable_test.go
// version: 1.0.0
// guid: 6f3c1a94-2d58-4b07-9e61-8c05d7f2ab13
// last-edited: 2026-09-09

package scanner

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// ctxAwareParser blocks for `work` but ABORTS when the batch deadline fires.
//
// The existing fakeAIParser cannot be used for the timeout tests: it does a
// bare time.Sleep and ignores ctx, so it returns success even after the
// deadline has passed and every assertion below would pass no matter what
// timeout the phase applied. A fixture that cannot observe the thing under
// test is the failure mode these tests exist to avoid -- the deadline has to
// actually elapse INSIDE the call for the phase to see an error.
type ctxAwareParser struct {
	work time.Duration

	mu         sync.Mutex
	batchSizes []int
}

func (p *ctxAwareParser) ParseBatch(ctx context.Context, filenames []string) ([]*ai.ParsedMetadata, error) {
	p.mu.Lock()
	p.batchSizes = append(p.batchSizes, len(filenames))
	p.mu.Unlock()

	select {
	case <-time.After(p.work):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	out := make([]*ai.ParsedMetadata, len(filenames))
	for i := range out {
		out[i] = &ai.ParsedMetadata{Title: "parsed"}
	}
	return out, nil
}

func (p *ctxAwareParser) sizes() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int(nil), p.batchSizes...)
}

// withAIParseBatchConfig sets the two knobs for one test and restores whatever
// was there. Restoring matters: config.AppConfig is process-global, and a test
// that leaks a 1-second timeout into the rest of the package would turn every
// later AI test into a flake that only reproduces in full-package runs.
func withAIParseBatchConfig(t *testing.T, size, timeoutSeconds int) {
	t.Helper()
	prevSize := config.AppConfig.AIBackend.ParseBatchSize
	prevTimeout := config.AppConfig.AIBackend.ParseBatchTimeoutSeconds
	t.Cleanup(func() {
		config.AppConfig.AIBackend.ParseBatchSize = prevSize
		config.AppConfig.AIBackend.ParseBatchTimeoutSeconds = prevTimeout
	})
	config.AppConfig.AIBackend.ParseBatchSize = size
	config.AppConfig.AIBackend.ParseBatchTimeoutSeconds = timeoutSeconds
}

// The bug this whole change exists for: on a CPU-only backend a batch takes
// minutes, the deadline was a hardcoded 30s, so EVERY batch failed and the
// phase parsed 0 books while looking externally identical to a healthy run.
//
// Asserted as a pair on purpose. "Slow parser fails" alone is satisfied by the
// old hardcoded constant and would pass on unfixed code; it is the fact that
// the SAME parser succeeds once the configured deadline is wide enough that
// pins the value to config rather than to a constant.
func TestRunAIBatchPhase_TimeoutComesFromConfig(t *testing.T) {
	// 1s is the smallest configurable deadline (the knob is whole seconds), so
	// the two cases straddle it: 2s of work against a 1s deadline must fail,
	// and 400ms of work against a 10s deadline must succeed.
	const work = 400 * time.Millisecond

	t.Run("work exceeding the configured deadline fails the batch", func(t *testing.T) {
		withAIParseBatchConfig(t, 4, 1)
		books, cands := makeCandidates(4)
		p := &ctxAwareParser{work: 2 * time.Second}

		s := runAIBatchPhase(context.Background(), p, books, cands, logger.New("test"), saveBookAndReportPath)

		if s.BooksParsed != 0 {
			t.Fatalf("BooksParsed = %d, want 0: a batch that outran its deadline must not count as parsed", s.BooksParsed)
		}
		if s.BatchesFailed == 0 {
			t.Fatal("BatchesFailed = 0: the deadline fired but the phase recorded no failure, " +
				"which is exactly how 81 prod runs reported success while parsing nothing")
		}
	})

	t.Run("the same work inside a wider configured deadline succeeds", func(t *testing.T) {
		withAIParseBatchConfig(t, 4, 10)
		books, cands := makeCandidates(4)
		p := &ctxAwareParser{work: work}

		s := runAIBatchPhase(context.Background(), p, books, cands, logger.New("test"), saveBookAndReportPath)

		if s.BatchesFailed != 0 {
			t.Fatalf("BatchesFailed = %d, want 0: %s of work fits inside a 10s configured deadline, "+
				"so a failure here means the phase is still using a hardcoded timeout", s.BatchesFailed, work)
		}
		if s.BooksParsed != 4 {
			t.Fatalf("BooksParsed = %d, want 4", s.BooksParsed)
		}
	})
}

// The batch SIZE half. Prod needs a small batch on slow hardware, and a size
// that is configured but ignored would look identical to one that is honored
// right up until the deadline math stops working.
func TestRunAIBatchPhase_BatchSizeComesFromConfig(t *testing.T) {
	withAIParseBatchConfig(t, 3, 10)
	books, cands := makeCandidates(9)
	p := &ctxAwareParser{}

	runAIBatchPhase(context.Background(), p, books, cands, logger.New("test"), saveBookAndReportPath)

	sizes := p.sizes()
	if len(sizes) != 3 {
		t.Fatalf("got %d batches %v, want 3 batches of 3: the configured size is not reaching the split", len(sizes), sizes)
	}
	for _, n := range sizes {
		if n != 3 {
			t.Fatalf("batch of %d in %v, want every batch to be the configured 3", n, sizes)
		}
	}
}

// Unset means "what this code did before the knob existed". An install pointed
// at a hosted API must see no behavior change from this becoming configurable.
func TestRunAIBatchPhase_UnsetConfigKeepsHistoricalBatchSize(t *testing.T) {
	withAIParseBatchConfig(t, 0, 0)
	books, cands := makeCandidates(20)
	p := &ctxAwareParser{}

	runAIBatchPhase(context.Background(), p, books, cands, logger.New("test"), saveBookAndReportPath)

	sizes := p.sizes()
	if len(sizes) != 1 || sizes[0] != config.DefaultAIParseBatchSize {
		t.Fatalf("got %v, want one batch of %d (the historical hardcoded size)", sizes, config.DefaultAIParseBatchSize)
	}
}
