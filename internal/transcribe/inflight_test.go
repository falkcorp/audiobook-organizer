// file: internal/transcribe/inflight_test.go
// version: 1.1.1
// guid: ca92ba48-3205-42c3-b911-885ce0ba2b40
// last-edited: 2026-09-02

package transcribe

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// resetInFlight clears the process-wide registries. The cap is deliberately
// global state (that is the whole point -- independent dispatches must contend
// for the same slots), so each test has to start from a known one.
func resetInFlight(t *testing.T, maxTotal int) {
	t.Helper()
	inflightMu.Lock()
	inflightPools = map[string]*slotPool{}
	inflightMu.Unlock()
	poolWideMu.Lock()
	poolWide = nil
	poolWideMu.Unlock()

	prev := config.AppConfig.WhisperMaxInFlight
	config.AppConfig.WhisperMaxInFlight = maxTotal
	t.Cleanup(func() { config.AppConfig.WhisperMaxInFlight = prev })
}

// hammer runs n concurrent acquire/hold/release cycles against url and reports
// the highest number seen holding a slot simultaneously.
func hammer(t *testing.T, url string, limit, n int, peak *int64) {
	t.Helper()
	var live atomic.Int64
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			release, err := acquireInFlight(context.Background(), url, limit)
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			defer release()
			cur := live.Add(1)
			for {
				old := atomic.LoadInt64(peak)
				if cur <= old || atomic.CompareAndSwapInt64(peak, old, cur) {
					break
				}
			}
			// Hold long enough that an uncapped implementation would overlap.
			time.Sleep(20 * time.Millisecond)
			live.Add(-1)
		})
	}
	wg.Wait()
}

// The regression this whole file exists for: BEFORE the cap, Concurrency was an
// allocation weight and 20 callers produced 20 simultaneous requests. This
// asserts on the peak, so it fails loudly against the old behaviour rather than
// merely passing faster.
func TestAcquireInFlightCapsConcurrency(t *testing.T) {
	resetInFlight(t, 0)
	var peak int64
	hammer(t, "http://a", 3, 20, &peak)
	if peak > 3 {
		t.Fatalf("peak concurrency %d exceeded per-endpoint limit 3", peak)
	}
	if peak < 2 {
		t.Fatalf("peak concurrency %d — the test never actually overlapped, so it proves nothing", peak)
	}
}

// Independent dispatches (the 6 parallel intro-transcribe pages) must contend
// for ONE set of slots. A per-dispatch limiter would pass the test above and
// still fail this one.
func TestAcquireInFlightIsSharedAcrossIndependentCallers(t *testing.T) {
	resetInFlight(t, 0)
	var peak int64
	var wg sync.WaitGroup
	for range 4 { // four separate "dispatches"
		wg.Go(func() {
			hammer(t, "http://shared", 2, 5, &peak)
		})
	}
	wg.Wait()
	if peak > 2 {
		t.Fatalf("peak %d across independent callers exceeded limit 2", peak)
	}
}

// Different endpoints are independent of each other...
func TestAcquireInFlightIsPerEndpoint(t *testing.T) {
	resetInFlight(t, 0)
	relA, err := acquireInFlight(context.Background(), "http://a", 1)
	if err != nil {
		t.Fatalf("acquire a: %v", err)
	}
	defer relA()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	relB, err := acquireInFlight(ctx, "http://b", 1)
	if err != nil {
		t.Fatalf("a full endpoint must not block a different endpoint: %v", err)
	}
	relB()
}

// ...until the pool-wide cap says otherwise.
func TestPoolWideCapAppliesAcrossEndpoints(t *testing.T) {
	resetInFlight(t, 2)
	var peak int64
	var live atomic.Int64
	var wg sync.WaitGroup
	for i := range 12 {
		url := []string{"http://a", "http://b", "http://c"}[i%3]
		wg.Go(func() {
			release, err := acquireInFlight(context.Background(), url, 10)
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			defer release()
			cur := live.Add(1)
			for {
				old := atomic.LoadInt64(&peak)
				if cur <= old || atomic.CompareAndSwapInt64(&peak, old, cur) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			live.Add(-1)
		})
	}
	wg.Wait()
	if peak > 2 {
		t.Fatalf("peak %d exceeded pool-wide cap 2 (per-endpoint limits were 10)", peak)
	}
}

// A caller that gives up while waiting must not strand the pool-wide slot it
// may already hold -- otherwise the global cap leaks down to zero over time.
func TestAcquireInFlightCancelDoesNotStrandPoolSlot(t *testing.T) {
	resetInFlight(t, 1)
	release, err := acquireInFlight(context.Background(), "http://a", 1)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := acquireInFlight(ctx, "http://a", 1); err == nil {
		t.Fatal("second acquire should have timed out while the only slot was held")
	}
	release()

	// If the timed-out attempt leaked the pool-wide slot, this blocks forever.
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	rel2, err := acquireInFlight(ctx2, "http://a", 1)
	if err != nil {
		t.Fatalf("pool-wide slot was stranded by the cancelled acquire: %v", err)
	}
	rel2()
}

func TestWhisperBatchSizeIsConfigurable(t *testing.T) {
	prev := config.AppConfig.WhisperBatchSize
	t.Cleanup(func() { config.AppConfig.WhisperBatchSize = prev })

	config.AppConfig.WhisperBatchSize = 0
	if got := whisperBatchSize(); got != defaultWhisperBatchSize {
		t.Fatalf("0 must mean default, got %d", got)
	}
	config.AppConfig.WhisperBatchSize = 5
	if got := whisperBatchSize(); got != 5 {
		t.Fatalf("configured size not honoured: got %d", got)
	}
}

// Finding 1 regression. The original implementation took the pool-wide slot
// FIRST and then parked on the per-endpoint channel, so a goroutine merely
// QUEUING for a busy endpoint consumed global capacity and starved idle ones.
// Both caps must bind for this to be observable -- the earlier pool-wide test
// used a per-endpoint limit of 10, so nothing ever blocked there and it could
// not have caught this.
func TestBusyEndpointDoesNotStarveIdleEndpoint(t *testing.T) {
	resetInFlight(t, 2) // pool-wide 2

	// Fill endpoint A (limit 1) and keep it held.
	relA1, err := acquireInFlight(context.Background(), "http://a", 1)
	if err != nil {
		t.Fatalf("first A acquire: %v", err)
	}

	// A second A-bound caller now QUEUES on A. Under the old order it would be
	// holding one of the two global slots while parked.
	queued := make(chan struct{})
	var qwg sync.WaitGroup
	qwg.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		close(queued)
		if rel, err := acquireInFlight(ctx, "http://a", 1); err == nil {
			rel()
		}
	})
	<-queued
	time.Sleep(100 * time.Millisecond) // let it reach the park

	// B is idle and the pool-wide cap has room. It MUST be servable.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	relB, err := acquireInFlight(ctx, "http://b", 1)
	cancel()
	if err != nil {
		relA1()
		qwg.Wait()
		t.Fatalf("idle endpoint B was starved by a caller merely queuing on A: %v", err)
	}
	relB()

	// Join the queued goroutine BEFORE returning: it reads config.AppConfig,
	// which t.Cleanup restores, and a leaked goroutine would race the next test.
	relA1()
	qwg.Wait()
}

// Finding 2 regression. Two callers disagreeing about one URL's limit must not
// re-create the pool: that installs a fresh EMPTY channel and admission becomes
// unbounded while every signal still says the cap works.
func TestConflictingLimitsForOneURLDoNotRemoveTheCap(t *testing.T) {
	resetInFlight(t, 0)
	var peak int64
	var live atomic.Int64
	var wg sync.WaitGroup
	for i := range 20 {
		limit := 2 + (i % 3) // callers disagree: 2, 3, 4, 2, 3, 4, ...
		wg.Go(func() {
			release, err := acquireInFlight(context.Background(), "http://same", limit)
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			defer release()
			cur := live.Add(1)
			for {
				old := atomic.LoadInt64(&peak)
				if cur <= old || atomic.CompareAndSwapInt64(&peak, old, cur) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			live.Add(-1)
		})
	}
	wg.Wait()
	// Whichever limit was established first wins; the cap must still be SOME
	// small bound, never the 20 an empty-channel thrash would allow.
	if peak > 4 {
		t.Fatalf("peak %d: conflicting limits removed the cap", peak)
	}
}

// Finding 4 regression. A slot-acquisition failure must be distinguishable from
// an endpoint failure, or the dispatcher benches servers it never contacted.
func TestAcquireFailureIsIdentifiableAsSlotWait(t *testing.T) {
	resetInFlight(t, 0)
	release, err := acquireInFlight(context.Background(), "http://a", 1)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = acquireInFlight(ctx, "http://a", 1)
	if err == nil {
		t.Fatal("expected the second acquire to fail")
	}
	if !errors.Is(err, ErrSlotWait) {
		t.Fatalf("slot-wait failure must be identifiable so the dispatcher does not bench an uncontacted endpoint; got %v", err)
	}
	if !strings.Contains(err.Error(), "http://a") {
		t.Fatalf("error should name the endpoint, got %v", err)
	}
}
