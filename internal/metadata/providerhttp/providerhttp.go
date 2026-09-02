// file: internal/metadata/providerhttp/providerhttp.go
// version: 1.0.0
// guid: 5c9e2a71-4b83-4f16-9d40-8e73a1b5c206
// last-edited: 2026-08-15

// Package providerhttp hands out rate-limited HTTP clients for outbound
// metadata-provider calls.
//
// Why this exists: before it, five of the six providers (Audible, Audnexus,
// Google Books, OpenLibrary, Wikipedia) had NO outbound throttling whatsoever —
// just an http.Client with a 30s timeout. There was no jitter, no backoff, and
// no 429/Retry-After handling anywhere in the tree. Only Hardcover had a limit,
// and it was a mutex + time.Sleep shared process-wide, which serialized every
// caller rather than pacing them. Fanning metadata fetch out across a worker
// pool on top of that is how an account gets blocked.
//
// The limiter lives in the http.RoundTripper rather than at the call sites on
// purpose: a call site can forget to call Wait(), but a request cannot bypass
// the transport it is issued on. Adding a new provider means asking this package
// for a client, and it is throttled by construction.
package providerhttp

import (
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limits describes the outbound budget for one provider.
type Limits struct {
	// RPS is the sustained request rate. Must be > 0.
	RPS float64
	// Burst is how many requests may be issued back-to-back before pacing
	// kicks in. Must be >= 1.
	Burst int
	// MaxRetries bounds retries for throttling/transient responses. 0 disables
	// retrying (the response is returned as-is).
	MaxRetries int
	// Timeout is the per-request client timeout.
	Timeout time.Duration
}

// defaultLimits are deliberately conservative. Only Hardcover publishes a number
// we actually know (60/min); the rest are chosen to be obviously polite rather
// than tuned, because the failure mode for guessing too high is getting the
// user's IP or account blocked, and the failure mode for guessing too low is
// that a bulk fetch takes longer.
//
// Raise these from measurement, not optimism.
var defaultLimits = map[string]Limits{
	// Hardcover documents 60 requests/minute.
	"hardcover": {RPS: 1.0, Burst: 1, MaxRetries: 3, Timeout: 30 * time.Second},
	// Audible/Audnexus are unofficial surfaces; be gentle.
	"audible":  {RPS: 2.0, Burst: 2, MaxRetries: 3, Timeout: 30 * time.Second},
	"audnexus": {RPS: 2.0, Burst: 2, MaxRetries: 3, Timeout: 30 * time.Second},
	// Google Books' documented anonymous quota is per-day, not per-second; the
	// practical constraint is per-IP throttling, so pace modestly.
	"googlebooks": {RPS: 3.0, Burst: 3, MaxRetries: 3, Timeout: 30 * time.Second},
	// OpenLibrary asks for "reasonable" use and publishes no hard number.
	"openlibrary": {RPS: 3.0, Burst: 3, MaxRetries: 3, Timeout: 30 * time.Second},
	// Wikipedia's API policy asks for serial-ish access from one client.
	"wikipedia": {RPS: 2.0, Burst: 2, MaxRetries: 3, Timeout: 30 * time.Second},
	// Cover art downloads hit arbitrary third-party image hosts.
	"cover": {RPS: 4.0, Burst: 4, MaxRetries: 2, Timeout: 60 * time.Second},
}

// fallbackLimits apply to any provider name with no entry above, so a new
// provider is throttled before anyone remembers to register it.
var fallbackLimits = Limits{RPS: 2.0, Burst: 2, MaxRetries: 3, Timeout: 30 * time.Second}

var (
	mu        sync.Mutex
	limiters  = map[string]*rate.Limiter{}
	clients   = map[string]*http.Client{}
	overrides = map[string]Limits{}
)

// SetLimits overrides the built-in budget for a provider. Call before any
// Client() for that provider (typically once at startup from config); it has no
// effect on a client already handed out, because that client's transport holds
// the limiter it was built with.
func SetLimits(provider string, l Limits) {
	mu.Lock()
	defer mu.Unlock()
	overrides[provider] = l
}

// limitsFor resolves the effective budget for a provider. Caller holds mu.
func limitsFor(provider string) Limits {
	if l, ok := overrides[provider]; ok {
		return normalize(l)
	}
	if l, ok := defaultLimits[provider]; ok {
		return normalize(l)
	}
	return fallbackLimits
}

// normalize repairs a nonsensical config rather than letting it disable
// throttling. A zero RPS on a rate.Limiter blocks forever, and a zero Burst
// makes Wait() fail immediately — both turn a misconfiguration into an outage,
// so clamp instead.
func normalize(l Limits) Limits {
	if l.RPS <= 0 {
		l.RPS = fallbackLimits.RPS
	}
	if l.Burst < 1 {
		l.Burst = 1
	}
	if l.MaxRetries < 0 {
		l.MaxRetries = 0
	}
	if l.Timeout <= 0 {
		l.Timeout = fallbackLimits.Timeout
	}
	return l
}

// Client returns the shared, rate-limited HTTP client for a provider.
//
// The client is shared per provider name for the life of the process, which is
// the point: every worker in a fan-out pool must draw from ONE token bucket, or
// the limit is per-goroutine and means nothing.
func Client(provider string) *http.Client {
	mu.Lock()
	defer mu.Unlock()

	if c, ok := clients[provider]; ok {
		return c
	}

	l := limitsFor(provider)
	lim, ok := limiters[provider]
	if !ok {
		lim = rate.NewLimiter(rate.Limit(l.RPS), l.Burst)
		limiters[provider] = lim
	}

	c := &http.Client{
		Timeout: l.Timeout,
		Transport: &throttled{
			base:       http.DefaultTransport,
			limiter:    lim,
			provider:   provider,
			maxRetries: l.MaxRetries,
		},
	}
	clients[provider] = c
	return c
}

// ClientWithTransport is Client for callers that must keep their own base
// RoundTripper — most importantly the cover downloader, whose transport carries
// an SSRF guard (a DialContext that refuses private/reserved IPs). Replacing
// that client wholesale would silently delete a security control, so the
// throttling wraps the caller's transport instead of displacing it.
//
// Unlike Client, the result is NOT cached: the caller owns the base transport
// and two callers passing different bases must not share a client. The rate
// limiter IS still shared per provider name, which is the part that has to be
// process-wide for a limit to mean anything.
func ClientWithTransport(provider string, base http.RoundTripper) *http.Client {
	if base == nil {
		base = http.DefaultTransport
	}

	mu.Lock()
	l := limitsFor(provider)
	lim, ok := limiters[provider]
	if !ok {
		lim = rate.NewLimiter(rate.Limit(l.RPS), l.Burst)
		limiters[provider] = lim
	}
	mu.Unlock()

	return &http.Client{
		Timeout: l.Timeout,
		Transport: &throttled{
			base:       base,
			limiter:    lim,
			provider:   provider,
			maxRetries: l.MaxRetries,
		},
	}
}

// newTestLimiter builds a limiter for tests without touching the shared maps.
func newTestLimiter(rps float64, burst int) *rate.Limiter {
	return rate.NewLimiter(rate.Limit(rps), burst)
}

// resetForTest clears the shared caches so tests do not leak state into each
// other. Test-only; the production path never resets.
func resetForTest() {
	mu.Lock()
	defer mu.Unlock()
	limiters = map[string]*rate.Limiter{}
	clients = map[string]*http.Client{}
	overrides = map[string]Limits{}
}

// throttled paces requests through a shared token bucket and retries throttling
// responses with backoff.
type throttled struct {
	base       http.RoundTripper
	limiter    *rate.Limiter
	provider   string
	maxRetries int
}

func (t *throttled) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()

	for attempt := 0; ; attempt++ {
		// Wait honors context cancellation, so a canceled fetch stops paying for
		// tokens instead of blocking a worker until its turn arrives.
		if err := t.limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("providerhttp %s: rate limit wait: %w", t.provider, err)
		}

		resp, err := t.base.RoundTrip(req)
		if err != nil {
			return nil, err
		}

		if attempt >= t.maxRetries || !shouldRetry(resp.StatusCode) {
			return resp, nil
		}

		// Decide replayability BEFORE draining. A request body cannot be replayed
		// without GetBody, and retrying without it would send an empty body.
		// Every provider call here is a GET, but guard rather than silently
		// corrupt a future POST. This must happen first: once the response is
		// drained and closed, returning it hands the caller a dead body.
		var replay io.ReadCloser
		if req.Body != nil {
			if req.GetBody == nil {
				return resp, nil
			}
			b, berr := req.GetBody()
			if berr != nil {
				return resp, nil
			}
			replay = b
		}

		delay := retryDelay(resp, attempt)

		// Drain and close before reusing the connection, otherwise it is
		// discarded and we leak sockets under sustained throttling.
		drain(resp)

		select {
		case <-ctx.Done():
			if replay != nil {
				_ = replay.Close()
			}
			return nil, ctx.Err()
		case <-time.After(delay):
		}

		if replay != nil {
			req.Body = replay
		}
	}
}

// shouldRetry reports whether a status code represents throttling or a
// transient server condition worth retrying.
func shouldRetry(code int) bool {
	switch code {
	case http.StatusTooManyRequests, // 429 — the one that matters
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

// retryDelay honors Retry-After when the server sends it, and otherwise backs
// off exponentially with jitter.
//
// Jitter is not decoration: without it, N workers throttled by the same server
// at the same moment all sleep the same duration and retry in lockstep, which
// reproduces the burst that caused the throttling.
func retryDelay(resp *http.Response, attempt int) time.Duration {
	if d, ok := parseRetryAfter(resp.Header.Get("Retry-After")); ok {
		return d
	}
	// 1s, 2s, 4s, ... capped, plus up to 50% jitter.
	base := min(time.Duration(math.Pow(2, float64(attempt)))*time.Second, 30*time.Second)
	jitter := time.Duration(rand.Int63n(int64(base/2) + 1))
	return base + jitter
}

// parseRetryAfter handles both forms the header is allowed to take: a
// delta-seconds integer, and an HTTP-date.
func parseRetryAfter(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0, false
		}
		d := min(time.Duration(secs)*time.Second,
			// never park a worker indefinitely
			5*time.Minute)
		return d, true
	}
	if when, err := http.ParseTime(v); err == nil {
		d := time.Until(when)
		if d <= 0 {
			return 0, false
		}
		if d > 5*time.Minute {
			d = 5 * time.Minute
		}
		return d, true
	}
	return 0, false
}

// drain reads and closes a response body so the underlying connection can be
// reused instead of being dropped. Bounded: an error body from a throttling
// response is small, and we do not want to download an unbounded one just to
// recycle a socket.
func drain(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
}
