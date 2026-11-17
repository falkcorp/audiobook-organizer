// file: internal/metadata/providerhttp/attempt_timeout_test.go
// version: 1.3.0
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

// A timed-out attempt is NOT retried inside the call, for any provider, whether
// the timeout is our attempt deadline or the network's own (a dial timeout): the
// caller's schedule is the retry, and a second try would double the time a hung
// provider ties a worker up.
func TestAttemptTimeout_ATimeoutIsNotRetriedInTheCall(t *testing.T) {
	for name, terr := range map[string]error{
		"attempt deadline": context.DeadlineExceeded,
		"dial timeout":     &net.OpError{Op: "dial", Net: "tcp", Err: timeoutErr{}},
	} {
		for _, attemptTimeout := range []time.Duration{0, 10 * time.Second} {
			t.Run(name, func(t *testing.T) {
				swapSeams(t, func(context.Context, time.Duration) error { t.Error("a backoff after a timeout"); return nil }, nil)
				var calls atomic.Int32
				tr := &throttled{
					base:           roundTripFunc(func(*http.Request) (*http.Response, error) { calls.Add(1); return nil, terr }),
					limiter:        rate.NewLimiter(rate.Inf, 1),
					provider:       "test",
					maxRetries:     3,
					attemptTimeout: attemptTimeout,
				}
				req, _ := http.NewRequest(http.MethodGet, "http://192.0.2.1/x", nil)
				if _, err := tr.RoundTrip(req); !errors.Is(err, terr) {
					t.Fatalf("err = %v, want the timeout surfaced", err)
				}
				if calls.Load() != 1 {
					t.Fatalf("requests = %d, want 1 (attemptTimeout %v)", calls.Load(), attemptTimeout)
				}
			})
		}
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
// via the seam) fails the exchange, which is surfaced as it is.
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

// timeoutErr is a net.Error whose Timeout() is true, the shape of a dial or
// TLS-handshake timeout.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// budgetRig is a clock the test moves and the deadlines the transport gave: the
// attempt-deadline and clock seams are swapped for it, so 30 s of exchange time
// is spent without waiting for it.
type budgetRig struct {
	mu   sync.Mutex
	now  time.Time
	gave []time.Duration
}

func newBudgetRig(t *testing.T) *budgetRig {
	t.Helper()
	r := &budgetRig{now: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)}
	oldNow := attemptNow
	t.Cleanup(func() { attemptNow = oldNow })
	attemptNow = func() time.Time { r.mu.Lock(); defer r.mu.Unlock(); return r.now }
	swapSeams(t, nil, func(p context.Context, d time.Duration) (context.Context, context.CancelFunc) {
		r.mu.Lock()
		r.gave = append(r.gave, d)
		r.mu.Unlock()
		return context.WithCancel(p)
	})
	return r
}

func (r *budgetRig) advance(d time.Duration) { r.mu.Lock(); r.now = r.now.Add(d); r.mu.Unlock() }

func (r *budgetRig) lastGiven() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gave[len(r.gave)-1]
}

func (r *budgetRig) deadlines() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Duration(nil), r.gave...)
}

// transport builds a throttled transport whose exchange spends took of the fake
// clock (or its whole given deadline when took is negative, a hang) and then
// fails with a timeout or answers.
func (r *budgetRig) transport(attemptTimeout time.Duration, calls *atomic.Int32, hang bool, took time.Duration) *throttled {
	return &throttled{
		base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls.Add(1)
			if hang {
				r.advance(r.lastGiven())
				return nil, context.DeadlineExceeded
			}
			r.advance(took)
			return okResponse(req), nil
		}),
		limiter:        rate.NewLimiter(rate.Inf, 1),
		provider:       "test",
		maxRetries:     3,
		attemptTimeout: attemptTimeout,
	}
}

// Three regions that all hang spend exactly the 30 s budget, send exactly three
// requests, and a fourth call (the 9-region list's next region) is refused
// before anything is sent.
func TestAttemptBudget_HungRegionsSpendTheBudgetAndNoMore(t *testing.T) {
	rig := newBudgetRig(t)
	var calls atomic.Int32
	tr := rig.transport(10*time.Second, &calls, true, 0)
	ctx := WithAttemptBudget(context.Background(), 30*time.Second)
	for region := 1; region <= 3; region++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://192.0.2.1/x", nil)
		if _, err := tr.RoundTrip(req); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("region %d err = %v, want a timeout", region, err)
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("requests = %d, want 3", calls.Load())
	}
	if left, _ := AttemptBudgetLeftForTest(ctx); left != 0 {
		t.Fatalf("budget left = %v, want 0 after three 10 s hangs", left)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://192.0.2.1/x", nil)
	if _, err := tr.RoundTrip(req); !errors.Is(err, ErrAttemptBudgetSpent) {
		t.Fatalf("fourth call err = %v, want ErrAttemptBudgetSpent", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("a call past the budget was sent (%d requests)", calls.Load())
	}
	if got := rig.deadlines(); len(got) != 3 || got[0] != 10*time.Second || got[2] != 10*time.Second {
		t.Fatalf("attempt deadlines = %v, want three of 10 s", got)
	}
}

// An attempt never gets more than what is left: with 15 s, the second region is
// held to 5 s and the third is refused.
func TestAttemptBudget_AnAttemptNeverRunsPastWhatIsLeft(t *testing.T) {
	rig := newBudgetRig(t)
	var calls atomic.Int32
	tr := rig.transport(10*time.Second, &calls, true, 0)
	ctx := WithAttemptBudget(context.Background(), 15*time.Second)
	for range 3 {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://192.0.2.1/x", nil)
		_, _ = tr.RoundTrip(req)
	}
	if got := rig.deadlines(); len(got) != 2 || got[0] != 10*time.Second || got[1] != 5*time.Second {
		t.Fatalf("attempt deadlines = %v, want [10s 5s]", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("requests = %d, want 2 (the third had no budget)", calls.Load())
	}
}

// A hang in the first region, an answer in the second: the answer comes back, and
// the budget is charged what was spent (10 s hung + 1 s answering), not the
// queue.
func TestAttemptBudget_ALaterRegionAnswersAfterAHangAndTheQueueIsNotCharged(t *testing.T) {
	rig := newBudgetRig(t)
	var n atomic.Int32
	tr := &throttled{
		base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if n.Add(1) == 1 {
				rig.advance(rig.lastGiven())
				return nil, context.DeadlineExceeded
			}
			rig.advance(time.Second)
			return okResponse(req), nil
		}),
		// The token wait is the queue: 20 s of fake clock per request, which must
		// not be charged to the budget.
		limiter:        clockWaiter{rig, 20 * time.Second},
		provider:       "test",
		attemptTimeout: 10 * time.Second,
	}
	ctx := WithAttemptBudget(context.Background(), 30*time.Second)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://192.0.2.1/x", nil)
	if _, err := tr.RoundTrip(req); err == nil {
		t.Fatal("the first region should have timed out")
	}
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, "http://192.0.2.1/x", nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("second region: %v", err)
	}
	_ = resp.Body.Close()
	if left, _ := AttemptBudgetLeftForTest(ctx); left != 19*time.Second {
		t.Fatalf("budget left = %v, want 19s (10 s hung + 1 s answering; 40 s of queue not charged)", left)
	}
}

type clockWaiter struct {
	rig *budgetRig
	d   time.Duration
}

func (w clockWaiter) Wait(context.Context) error { w.rig.advance(w.d); return nil }

// A provider with no AttemptTimeout ignores a budget on the context: it was
// never attempt-bounded, so nothing is charged and nothing is refused.
func TestAttemptBudget_AProviderWithoutAnAttemptTimeoutIgnoresIt(t *testing.T) {
	rig := newBudgetRig(t)
	var calls atomic.Int32
	tr := rig.transport(0, &calls, false, 5*time.Second)
	ctx := WithAttemptBudget(context.Background(), 0) // already spent
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://192.0.2.1/x", nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("err = %v, want the call sent", err)
	}
	_ = resp.Body.Close()
	if len(rig.deadlines()) != 0 {
		t.Fatalf("attempt deadlines = %v, want none", rig.deadlines())
	}
}
