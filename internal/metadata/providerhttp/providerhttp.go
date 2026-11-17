// file: internal/metadata/providerhttp/providerhttp.go
// version: 1.7.0
// guid: 5c9e2a71-4b83-4f16-9d40-8e73a1b5c206
// last-edited: 2026-11-17

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
	"context"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/falkcorp/audiobook-organizer/internal/metadata/dailyquota"
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
	// Timeout is the per-request client timeout. It covers the WHOLE call: the
	// wait for a token, every retry and its backoff, and the exchange.
	Timeout time.Duration
	// AttemptTimeout bounds one exchange with the provider -- connect, send, the
	// response headers AND the body read (the attempt's context ends when the
	// caller closes the body, so a stalled body is cut off too) -- and starts
	// only AFTER the request holds its token (and its daily-quota reservation),
	// so time spent queued behind the other workers on the shared bucket is not
	// charged to it. 0 means no per-attempt bound (Timeout alone governs) and no
	// timeout retry: a provider without one behaves exactly as it did before the
	// field existed.
	//
	// A deadline set by the caller around the request covers the queue, and with
	// N workers on a bucket of rate r the queue alone is N/r seconds: 20 workers
	// on 2 rps spent a 10 s caller deadline entirely waiting (Audnexus, 2026-11-16).
	//
	// A timed-out attempt is NOT retried here: the caller's own schedule is the
	// retry (the candidate cache re-asks a source that could not answer, on a
	// backoff, MetadataCandidateCache.OwedSources), and a second try inside the
	// call would double the time a hung provider ties a worker up.
	//
	// A group of calls that must share one time budget (the Audnexus ASIN lookup's
	// regions) carries it on the context: WithAttemptBudget. Each attempt is then
	// bounded by the smaller of AttemptTimeout and what is left of the budget, and
	// is charged against it when it ends.
	AttemptTimeout time.Duration
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
	"audible": {RPS: 2.0, Burst: 2, MaxRetries: 3, Timeout: 30 * time.Second},
	// AttemptTimeout 10 s is the bound the lookup used to impose itself, before
	// the queue.
	"audnexus": {RPS: 2.0, Burst: 2, MaxRetries: 3, Timeout: 30 * time.Second, AttemptTimeout: 10 * time.Second},
	// Google Books' documented anonymous quota is per-day, not per-second; the
	// practical constraint is per-IP throttling, so pace modestly.
	"google-books": {RPS: 3.0, Burst: 3, MaxRetries: 3, Timeout: 30 * time.Second},
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

// ResetProvider drops the cached client AND limiter for one provider so the
// next Client() call rebuilds both from the current limits.
//
// This is what makes SetLimits usable after startup. Client() caches per
// provider for the life of the process and a handed-out client keeps the
// limiter it was constructed with, so a SetLimits call on its own changes
// nothing that is already running: the settings UI would move a slider, store
// a number, report success, and leave the actual request rate untouched.
//
// Callers should SetLimits first, then ResetProvider, so the rebuild picks up
// the new budget rather than racing the old one.
func ResetProvider(provider string) {
	mu.Lock()
	defer mu.Unlock()
	delete(clients, provider)
	delete(limiters, provider)
}

// BuiltinLimitsFor returns the compiled-in budget for a provider, ignoring any
// override. Callers scaling a budget (e.g. a low/medium/high tier) need the
// built-in figure as the anchor, because the built-ins differ per provider for
// real reasons and a tier expressed in absolute terms would be wrong for most
// of them.
func BuiltinLimitsFor(provider string) Limits {
	if l, ok := defaultLimits[provider]; ok {
		return l
	}
	return fallbackLimits
}

// KnownProviders lists every provider with a compiled-in budget. Used by tests
// to prove that the names other packages resolve to actually exist here — a
// budget stored under a name nothing requests is silently inert.
func KnownProviders() []string {
	out := make([]string, 0, len(defaultLimits))
	for k := range defaultLimits {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// HasOverride reports whether SetLimits has stored a budget for this provider.
//
// Exported because "was an override created?" is not answerable from
// KnownProviders (which lists compiled-in budgets) or from EffectiveLimitsFor
// (which falls back to the built-in and so reads the same either way). A test
// that cannot see a spurious override cannot catch a caller that stores one
// under a misspelled or empty key -- a budget that then applies to no traffic.
func HasOverride(provider string) bool {
	mu.Lock()
	defer mu.Unlock()
	_, ok := overrides[provider]
	return ok
}

// EffectiveLimitsFor returns the budget a provider would actually be built
// with right now: an override if one was set, else the built-in, normalized.
// Exported so callers and tests can assert on the limits that were really
// INSTALLED rather than recomputing the expected value -- recomputation passes
// even when the value never reached this package.
func EffectiveLimitsFor(provider string) Limits {
	mu.Lock()
	defer mu.Unlock()
	return limitsFor(provider)
}

// limitsFor resolves the effective budget for a provider. Caller holds mu.
func limitsFor(provider string) Limits {
	if l, ok := overrides[provider]; ok {
		// An override replaces the budget field by field; it does not name an
		// attempt bound, so the provider's built-in one carries over (the
		// settings UI has no such field, and dropping it silently would put
		// the queue back inside the caller's deadline).
		if l.AttemptTimeout == 0 {
			l.AttemptTimeout = defaultLimits[provider].AttemptTimeout
		}
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
	if l.AttemptTimeout < 0 {
		l.AttemptTimeout = 0
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
			base:           http.DefaultTransport,
			limiter:        lim,
			provider:       provider,
			maxRetries:     l.MaxRetries,
			attemptTimeout: l.AttemptTimeout,
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
			base:           base,
			limiter:        lim,
			provider:       provider,
			maxRetries:     l.MaxRetries,
			attemptTimeout: l.AttemptTimeout,
		},
	}
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

// waitFor sleeps for d, or until ctx is done (then it returns the context's
// error). A variable so tests record the backoff instead of waiting it out.
var waitFor = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// attemptNow is the clock an attempt is timed with. A variable so tests advance
// it by the deadline they fire instead of waiting.
var attemptNow = time.Now

// ErrAttemptBudgetSpent is returned, with nothing sent, by a call whose
// WithAttemptBudget budget has no time left. It wraps context.DeadlineExceeded,
// so it classifies as a timeout, and a caller walking a list of regions can end
// the walk on it.
var ErrAttemptBudgetSpent = fmt.Errorf("attempt budget spent: %w", context.DeadlineExceeded)

// attemptBudget is the time a group of calls may spend in exchanges with the
// provider, measured from when each attempt holds its token (the queue is not
// charged: that is what AttemptTimeout is for).
type attemptBudget struct {
	mu        sync.Mutex
	remaining time.Duration
}

func (b *attemptBudget) left() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.remaining
}

func (b *attemptBudget) charge(d time.Duration) {
	if d < 0 {
		d = 0
	}
	b.mu.Lock()
	b.remaining -= d
	b.mu.Unlock()
}

type attemptBudgetKey struct{}

// WithAttemptBudget returns a context whose calls through a provider that has an
// AttemptTimeout share total of exchange time: each attempt gets at most what is
// left, is charged what it used when it ends, and a call that finds none left
// fails with ErrAttemptBudgetSpent before sending. A provider without an
// AttemptTimeout ignores it. It bounds the sum over a loop (the regions of one
// lookup), which a per-attempt bound cannot.
func WithAttemptBudget(ctx context.Context, total time.Duration) context.Context {
	return context.WithValue(ctx, attemptBudgetKey{}, &attemptBudget{remaining: total})
}

// AttemptBudgetLeftForTest reports what is left of the budget ctx carries, and whether
// it carries one. A test seam (the name keeps the deadcode ratchet from counting it):
// the audnexus tests in another package read the budget a request's context carries.
func AttemptBudgetLeftForTest(ctx context.Context) (time.Duration, bool) {
	if b, ok := ctx.Value(attemptBudgetKey{}).(*attemptBudget); ok {
		return b.left(), true
	}
	return 0, false
}

// attemptContext derives the context one exchange runs under. A variable so
// tests can fire the attempt deadline on demand instead of waiting for the
// clock.
var attemptContext = context.WithTimeout

// tokenWaiter is the part of *rate.Limiter the transport uses: block until a
// token is granted, or ctx is done. An interface so a test can order the wait
// against the attempt's deadline without waiting on a clock.
type tokenWaiter interface {
	Wait(ctx context.Context) error
}

// throttled paces requests through a shared token bucket and retries throttling
// responses with backoff.
type throttled struct {
	base       http.RoundTripper
	limiter    tokenWaiter
	provider   string
	maxRetries int
	// attemptTimeout bounds one exchange, started after the token is held
	// (Limits.AttemptTimeout); 0 disables it.
	attemptTimeout time.Duration
}

// attemptBody ends the attempt's context when the caller finishes with the
// response body: cancelling it at RoundTrip's return would abort the read.
type attemptBody struct {
	io.ReadCloser
	end context.CancelFunc
}

func (b *attemptBody) Close() error {
	err := b.ReadCloser.Close()
	b.end()
	return err
}

func (t *throttled) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()

	for attempt := 0; ; attempt++ {
		// Wait honors context cancellation, so a canceled fetch stops paying for
		// tokens instead of blocking a worker until its turn arrives.
		if err := t.limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("providerhttp %s: rate limit wait: %w", t.provider, err)
		}

		// The provider's daily key quota (dailyquota), counted per request
		// SENT -- a retry spends quota like the first try -- at the tier the
		// request's context carries (unmarked = background). Checked here, in
		// the transport every client of this provider is built on, so no
		// caller can reach the provider around it. A refusal sends nothing.
		if err := dailyquota.ReserveFor(ctx, t.provider); err != nil {
			return nil, fmt.Errorf("providerhttp %s: %w", t.provider, err)
		}

		// The attempt's own deadline starts here, after the queue: see
		// Limits.AttemptTimeout.
		attemptReq, endAttempt := req, context.CancelFunc(func() {})
		if t.attemptTimeout > 0 {
			d := t.attemptTimeout
			budget, _ := ctx.Value(attemptBudgetKey{}).(*attemptBudget)
			if budget != nil {
				left := budget.left()
				if left <= 0 {
					return nil, fmt.Errorf("providerhttp %s: %w", t.provider, ErrAttemptBudgetSpent)
				}
				d = min(d, left)
			}
			started := attemptNow()
			actx, cancel := attemptContext(ctx, d)
			var once sync.Once
			attemptReq, endAttempt = req.WithContext(actx), func() {
				once.Do(func() {
					cancel()
					if budget != nil {
						budget.charge(attemptNow().Sub(started))
					}
				})
			}
		}
		// deliver hands resp to the caller, tying the attempt's context to its
		// body.
		deliver := func(resp *http.Response) *http.Response {
			if t.attemptTimeout > 0 && resp.Body != nil {
				resp.Body = &attemptBody{ReadCloser: resp.Body, end: endAttempt}
			} else {
				endAttempt()
			}
			return resp
		}

		resp, err := t.base.RoundTrip(attemptReq)
		if err != nil {
			endAttempt()
			// A timed-out attempt is returned as it is: the caller's own
			// schedule retries (see Limits.AttemptTimeout).
			return nil, err
		}

		if attempt >= t.maxRetries || !shouldRetry(resp.StatusCode) {
			return deliver(resp), nil
		}

		// Decide replayability BEFORE draining. A request body cannot be replayed
		// without GetBody, and retrying without it would send an empty body.
		// Every provider call here is a GET, but guard rather than silently
		// corrupt a future POST. This must happen first: once the response is
		// drained and closed, returning it hands the caller a dead body.
		var replay io.ReadCloser
		if req.Body != nil {
			if req.GetBody == nil {
				return deliver(resp), nil
			}
			b, berr := req.GetBody()
			if berr != nil {
				return deliver(resp), nil
			}
			replay = b
		}

		delay := retryDelay(resp, attempt)

		// Drain and close before reusing the connection, otherwise it is
		// discarded and we leak sockets under sustained throttling.
		drain(resp)
		endAttempt()

		if werr := waitFor(ctx, delay); werr != nil {
			if replay != nil {
				_ = replay.Close()
			}
			return nil, werr
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
	return backoffDelay(attempt)
}

// backoffDelay is 1s, 2s, 4s, ... capped, plus up to 50% jitter.
func backoffDelay(attempt int) time.Duration {
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
