// file: internal/metadata/providerhttp/attempt_timeout_test.go
// version: 1.1.1
// guid: 9b4f7e21-0c58-4d3a-a6e9-3f1d82b7c405
// last-edited: 2026-11-17

package providerhttp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// None of these wait on a clock: the backoff and the attempt deadline are the
// package's waitFor / attemptContext seams, and the token queue is a fake.

// events records the order things happened in.
type events struct {
	mu  sync.Mutex
	log []string
}

func (e *events) add(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.log = append(e.log, s)
}

func (e *events) list() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.log...)
}

type recordingWaiter struct{ ev *events }

func (w recordingWaiter) Wait(context.Context) error { w.ev.add("token"); return nil }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func okResponse(req *http.Request) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(http.NoBody), Request: req}
}

// swapSeams installs fakes for waitFor and attemptContext for one test.
func swapSeams(t *testing.T, w func(context.Context, time.Duration) error,
	a func(context.Context, time.Duration) (context.Context, context.CancelFunc)) {
	t.Helper()
	oldW, oldA := waitFor, attemptContext
	t.Cleanup(func() { waitFor, attemptContext = oldW, oldA })
	if w != nil {
		waitFor = w
	}
	if a != nil {
		attemptContext = a
	}
}

// The attempt's deadline must be created AFTER the request holds its token:
// otherwise the queue behind the shared bucket is charged to it, which is how
// 482 Audnexus lookups died in the 2026-11-16 candidate-fetch run.
func TestAttemptTimeout_StartsAfterTheTokenIsHeld(t *testing.T) {
	ev := &events{}
	swapSeams(t, nil, func(p context.Context, d time.Duration) (context.Context, context.CancelFunc) {
		ev.add("attempt-deadline")
		if d != 10*time.Second {
			t.Errorf("attempt deadline = %v, want the configured 10s", d)
		}
		return context.WithCancel(p)
	})
	tr := &throttled{
		base: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			ev.add("exchange")
			return okResponse(r), nil
		}),
		limiter:        recordingWaiter{ev},
		provider:       "test",
		attemptTimeout: 10 * time.Second,
	}
	req, _ := http.NewRequest(http.MethodGet, "http://192.0.2.1/x", nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	got := ev.list()
	want := []string{"token", "attempt-deadline", "exchange"}
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want %v", got, want)
		}
	}
}

// With no AttemptTimeout configured the transport adds no deadline of its own.
func TestAttemptTimeout_ZeroAddsNoDeadline(t *testing.T) {
	swapSeams(t, nil, func(context.Context, time.Duration) (context.Context, context.CancelFunc) {
		t.Error("attemptContext called with attemptTimeout 0")
		return nil, func() {}
	})
	tr := &throttled{
		base: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if _, has := r.Context().Deadline(); has {
				t.Error("request carries a deadline the transport added")
			}
			return okResponse(r), nil
		}),
		limiter:  rate.NewLimiter(rate.Inf, 1),
		provider: "test",
	}
	req, _ := http.NewRequest(http.MethodGet, "http://192.0.2.1/x", nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

// An attempt that times out is retried ONCE, after a backoff, and the retry
// takes a fresh token.
func TestAttemptTimeout_TimeoutRetriedOnceWithBackoff(t *testing.T) {
	var delays []time.Duration
	swapSeams(t, func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil }, nil)
	ev := &events{}
	var calls atomic.Int32
	tr := &throttled{
		base: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if calls.Add(1) == 1 {
				return nil, context.DeadlineExceeded
			}
			return okResponse(r), nil
		}),
		limiter:        recordingWaiter{ev},
		provider:       "test",
		maxRetries:     3,
		attemptTimeout: 10 * time.Second,
	}
	req, _ := http.NewRequest(http.MethodGet, "http://192.0.2.1/x", nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("the retry should have succeeded: %v", err)
	}
	_ = resp.Body.Close()

	if calls.Load() != 2 {
		t.Fatalf("exchanges = %d, want 2 (timeout + one retry)", calls.Load())
	}
	if len(delays) != 1 || delays[0] < time.Second || delays[0] > 1500*time.Millisecond {
		t.Fatalf("backoff = %v, want one delay in [1s, 1.5s]", delays)
	}
	if got := ev.list(); len(got) != 2 {
		t.Fatalf("tokens taken = %v, want one per exchange (the retry re-queues)", got)
	}
}

// A provider that keeps timing out is not hammered: one retry, then the error.
func TestAttemptTimeout_RetryIsBounded(t *testing.T) {
	var delays []time.Duration
	swapSeams(t, func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil }, nil)
	var calls atomic.Int32
	tr := &throttled{
		base: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, context.DeadlineExceeded
		}),
		limiter:        rate.NewLimiter(rate.Inf, 1),
		provider:       "test",
		maxRetries:     3, // the throttling bound is larger; the timeout bound still holds
		attemptTimeout: 10 * time.Second,
	}
	req, _ := http.NewRequest(http.MethodGet, "http://192.0.2.1/x", nil)
	_, err := tr.RoundTrip(req)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the timeout surfaced", err)
	}
	if calls.Load() != 1+maxTimeoutRetries {
		t.Fatalf("exchanges = %d, want %d", calls.Load(), 1+maxTimeoutRetries)
	}
	if len(delays) != maxTimeoutRetries {
		t.Fatalf("backoffs = %v, want %d", delays, maxTimeoutRetries)
	}
}

// A caller that has given up gets its error at once: no backoff, no retry.
func TestAttemptTimeout_CallerGivingUpIsNotRetried(t *testing.T) {
	swapSeams(t, func(context.Context, time.Duration) error { t.Error("backoff after the caller gave up"); return nil }, nil)
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	tr := &throttled{
		base: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			cancel()
			return nil, context.DeadlineExceeded
		}),
		limiter:        rate.NewLimiter(rate.Inf, 1),
		provider:       "test",
		maxRetries:     3,
		attemptTimeout: 10 * time.Second,
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://192.0.2.1/x", nil)
	if _, err := tr.RoundTrip(req); err == nil {
		t.Fatal("want an error")
	}
	if calls.Load() != 1 {
		t.Fatalf("exchanges = %d, want 1", calls.Load())
	}
}

// End to end over the real transport: the attempt deadline (already expired,
// via the seam) fails the exchange, which is retried once and then surfaced.
func TestAttemptTimeout_RealTransportExpiredAttempt(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	swapSeams(t, func(context.Context, time.Duration) error { return nil },
		func(p context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
			return context.WithDeadline(p, time.Now().Add(-time.Hour))
		})
	c := &http.Client{Transport: &throttled{
		base:           http.DefaultTransport,
		limiter:        rate.NewLimiter(rate.Inf, 1),
		provider:       "test",
		maxRetries:     3,
		attemptTimeout: 10 * time.Second,
	}}
	_, err := c.Get(srv.URL)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline error", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("server saw %d requests, want 0 (the deadline had already passed)", hits.Load())
	}
}

// The attempt's context must outlive RoundTrip: the caller reads the body after
// it returns, and ends the attempt by closing it.
func TestAttemptTimeout_BodyReadableAfterRoundTripAndCloseEndsAttempt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("Gotham"))
	}))
	defer srv.Close()
	var ended atomic.Int32
	swapSeams(t, nil, func(p context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(p)
		return ctx, func() { ended.Add(1); cancel() }
	})
	c := &http.Client{Transport: &throttled{
		base:           http.DefaultTransport,
		limiter:        rate.NewLimiter(rate.Inf, 1),
		provider:       "test",
		attemptTimeout: 10 * time.Second,
	}}
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if ended.Load() != 0 {
		t.Fatal("the attempt ended before the body was read")
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil || string(b) != "Gotham" {
		t.Fatalf("body = %q, %v", b, err)
	}
	_ = resp.Body.Close()
	if ended.Load() != 1 {
		t.Fatalf("attempt ended %d times after Close, want 1", ended.Load())
	}
}

// The built-in attempt bound survives a configured override of the budget,
// which replaces RPS, burst, retries and timeout but names no attempt bound.
func TestAttemptTimeout_AudnexusDefaultSurvivesOverride(t *testing.T) {
	resetForTest()
	t.Cleanup(resetForTest)
	if got := EffectiveLimitsFor("audnexus").AttemptTimeout; got != 10*time.Second {
		t.Fatalf("built-in audnexus AttemptTimeout = %v, want 10s", got)
	}
	SetLimits("audnexus", Limits{RPS: 1, Burst: 1, MaxRetries: 2, Timeout: 20 * time.Second})
	if got := EffectiveLimitsFor("audnexus").AttemptTimeout; got != 10*time.Second {
		t.Fatalf("overridden audnexus AttemptTimeout = %v, want the built-in 10s kept", got)
	}
	if got := EffectiveLimitsFor("hardcover").AttemptTimeout; got != 0 {
		t.Fatalf("hardcover AttemptTimeout = %v, want none", got)
	}
}

// A provider with no AttemptTimeout configured must behave exactly as it did
// before the field existed: a dial timeout (a net.Error that reports Timeout)
// is one request and the error, not a second request after a backoff.
func TestAttemptTimeout_ZeroValueProviderSendsOneRequestOnDialTimeout(t *testing.T) {
	swapSeams(t, func(context.Context, time.Duration) error {
		t.Error("backoff for a provider with no attempt timeout")
		return nil
	}, nil)
	var calls atomic.Int32
	dialTimeout := &net.OpError{Op: "dial", Net: "tcp", Err: timeoutErr{}}
	tr := &throttled{
		base: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, dialTimeout
		}),
		limiter:    rate.NewLimiter(rate.Inf, 1),
		provider:   "test",
		maxRetries: 3,
		// attemptTimeout is zero: the zero-value provider.
	}
	req, _ := http.NewRequest(http.MethodGet, "http://192.0.2.1/x", nil)
	_, err := tr.RoundTrip(req)
	if !errors.Is(err, dialTimeout) {
		t.Fatalf("err = %v, want the dial timeout surfaced", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("requests = %d, want 1 for a provider with no AttemptTimeout", calls.Load())
	}
}

// timeoutErr is a net.Error whose Timeout() is true, the shape of a dial or
// TLS-handshake timeout.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// The worst case of one call, by the clock the transport would see: every
// attempt times out, and the time charged is each attempt's full deadline plus
// the backoff actually taken (the injectable wait records it). It must stay
// inside the provider's Timeout -- the Audnexus built-ins give 21.5 s of 30 s
// -- and, with the lookup stopping at the first region that times out, a whole
// lookup inside what three of the old 10 s per-region deadlines were.
func TestAttemptTimeout_WorstCaseStaysInsideTimeout(t *testing.T) {
	resetForTest()
	t.Cleanup(resetForTest)
	l := EffectiveLimitsFor("audnexus")
	if l.AttemptTimeout <= 0 {
		t.Fatal("audnexus has no AttemptTimeout; the bound below is moot")
	}
	var charged time.Duration
	swapSeams(t, func(_ context.Context, d time.Duration) error { charged += d; return nil },
		func(p context.Context, d time.Duration) (context.Context, context.CancelFunc) {
			charged += d // the attempt ran to its deadline
			return context.WithCancel(p)
		})
	var calls atomic.Int32
	tr := &throttled{
		base: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, context.DeadlineExceeded
		}),
		limiter:        rate.NewLimiter(rate.Inf, 1),
		provider:       "audnexus",
		maxRetries:     l.MaxRetries,
		attemptTimeout: l.AttemptTimeout,
	}
	req, _ := http.NewRequest(http.MethodGet, "http://192.0.2.1/x", nil)
	_, _ = tr.RoundTrip(req)
	if calls.Load() != 1+maxTimeoutRetries {
		t.Fatalf("requests = %d, want %d", calls.Load(), 1+maxTimeoutRetries)
	}
	if charged > l.Timeout {
		t.Fatalf("one region's worst case = %v, over the provider Timeout %v", charged, l.Timeout)
	}
	const (
		regions        = 3                // audnexusSearchRegions
		oldRegionLimit = 10 * time.Second // audnexusPerRegionTimeout, queue included
	)
	if before := regions * oldRegionLimit; charged > before {
		t.Fatalf("a hung lookup's worst case = %v, over the %v it was before (%d regions x %v)", charged, before, regions, oldRegionLimit)
	}
	if charged > 21500*time.Millisecond {
		t.Fatalf("one region's worst case = %v, documented as at most 21.5s", charged)
	}
}
