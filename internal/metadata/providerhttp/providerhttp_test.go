// file: internal/metadata/providerhttp/providerhttp_test.go
// version: 1.0.0
// guid: c81f4d0a-3b76-4e29-9152-6a0d7e4b8f35
// last-edited: 2026-08-15

package providerhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// These run against a real httptest server rather than asserting on internal
// state. A rate limiter that is present but not wired into the request path
// would pass any test that only inspects the struct; only issuing actual
// requests and timing them proves the pacing happens.

func TestThrottled_PacesRequests(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// 10 rps, burst 1: the 3rd request cannot land before ~200ms.
	c := &http.Client{Transport: &throttled{
		base:     http.DefaultTransport,
		limiter:  newTestLimiter(10, 1),
		provider: "test",
	}}

	start := time.Now()
	for i := 0; i < 3; i++ {
		resp, err := c.Get(srv.URL)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		resp.Body.Close()
	}
	elapsed := time.Since(start)

	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("server saw %d requests, want 3", got)
	}
	// Burst 1 covers the first; the next two each wait ~100ms.
	if elapsed < 150*time.Millisecond {
		t.Fatalf("3 requests at 10rps took %v; too fast to have been rate limited", elapsed)
	}
}

func TestThrottled_RetriesOn429AndHonorsRetryAfter(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("slow down"))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := &http.Client{Transport: &throttled{
		base:       http.DefaultTransport,
		limiter:    newTestLimiter(1000, 1000), // pacing out of the way
		provider:   "test",
		maxRetries: 3,
	}}

	start := time.Now()
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	elapsed := time.Since(start)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("final status = %d, want 200 (the retry should have succeeded)", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("server saw %d attempts, want 2 (one 429 + one retry)", got)
	}
	if elapsed < 900*time.Millisecond {
		t.Fatalf("retry happened after %v; Retry-After: 1 was not honored", elapsed)
	}
}

func TestThrottled_GivesUpAfterMaxRetries(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := &http.Client{Transport: &throttled{
		base:       http.DefaultTransport,
		limiter:    newTestLimiter(1000, 1000),
		provider:   "test",
		maxRetries: 2,
	}}

	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	// maxRetries=2 means the initial attempt plus 2 retries.
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("server saw %d attempts, want 3 (initial + 2 retries)", got)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want the final 429 surfaced to the caller", resp.StatusCode)
	}
	// The body must still be readable: the retry loop drains intermediate
	// responses, and returning a drained body would hand the caller a dead read.
	buf := make([]byte, 1)
	if _, err := resp.Body.Read(buf); err != nil && err.Error() == "http: read on closed response body" {
		t.Fatal("final response body was closed by the retry loop")
	}
}

func TestThrottled_ContextCancellationAborts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// 1 rps burst 1: the second request must wait ~1s, which we cancel.
	c := &http.Client{Transport: &throttled{
		base:     http.DefaultTransport,
		limiter:  newTestLimiter(1, 1),
		provider: "test",
	}}

	resp, err := c.Get(srv.URL) // consumes the burst token
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	resp.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)

	start := time.Now()
	if _, err := c.Do(req); err == nil {
		t.Fatal("expected cancellation error while waiting for a rate-limit token")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("cancellation took %v; the limiter wait is not honoring context", elapsed)
	}
}

// TestClient_SharesLimiterPerProvider is the property that makes the limit mean
// anything: every worker in a fan-out pool must draw from ONE bucket. If each
// caller got its own limiter, N workers would each get the full rate and the
// effective rate would be N times the configured one.
func TestClient_SharesLimiterPerProvider(t *testing.T) {
	resetForTest()
	SetLimits("sharedtest", Limits{RPS: 5, Burst: 1, MaxRetries: 0, Timeout: 5 * time.Second})

	a := Client("sharedtest")
	b := Client("sharedtest")

	if a != b {
		t.Fatal("Client returned distinct clients for one provider; the token bucket would not be shared")
	}

	ta, oka := a.Transport.(*throttled)
	tb, okb := b.Transport.(*throttled)
	if !oka || !okb {
		t.Fatal("transport is not the throttling transport; requests would bypass the limiter entirely")
	}
	if ta.limiter != tb.limiter {
		t.Fatal("two clients for one provider hold different limiters")
	}
}

// TestClientWithTransport_PreservesBase guards a security property: the cover
// downloader's transport carries an SSRF dialer that refuses private IPs.
// Wrapping must not discard it.
func TestClientWithTransport_PreservesBase(t *testing.T) {
	resetForTest()

	marker := &markerTransport{}
	c := ClientWithTransport("covertest", marker)

	tr, ok := c.Transport.(*throttled)
	if !ok {
		t.Fatal("expected the throttling transport")
	}
	if tr.base != http.RoundTripper(marker) {
		t.Fatal("caller-supplied base transport was replaced; an SSRF guard would be silently dropped here")
	}

	req, _ := http.NewRequest(http.MethodGet, "http://example.invalid", nil)
	if _, err := c.Transport.RoundTrip(req); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if !marker.called {
		t.Fatal("base transport was never invoked; the wrapper bypassed it")
	}
}

func TestNormalize_ClampsNonsenseInsteadOfDisablingThrottling(t *testing.T) {
	// A zero RPS on rate.Limiter blocks forever and a zero burst makes Wait fail
	// immediately: a misconfiguration must degrade to the fallback, not an outage.
	got := normalize(Limits{RPS: 0, Burst: 0, MaxRetries: -5, Timeout: 0})

	if got.RPS <= 0 {
		t.Fatalf("RPS = %v, want a positive fallback", got.RPS)
	}
	if got.Burst < 1 {
		t.Fatalf("Burst = %d, want >= 1", got.Burst)
	}
	if got.MaxRetries < 0 {
		t.Fatalf("MaxRetries = %d, want >= 0", got.MaxRetries)
	}
	if got.Timeout <= 0 {
		t.Fatalf("Timeout = %v, want a positive fallback", got.Timeout)
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name string
		in   string
		ok   bool
		want time.Duration
	}{
		{"delta seconds", "5", true, 5 * time.Second},
		{"zero", "0", true, 0},
		{"negative rejected", "-3", false, 0},
		{"garbage rejected", "soon", false, 0},
		{"empty rejected", "", false, 0},
		{"absurd value capped", "99999", true, 5 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseRetryAfter(tt.in)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Fatalf("delay = %v, want %v", got, tt.want)
			}
		})
	}
}

// --- helpers ---

type markerTransport struct{ called bool }

func (m *markerTransport) RoundTrip(*http.Request) (*http.Response, error) {
	m.called = true
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       http.NoBody,
		Header:     make(http.Header),
	}, nil
}
