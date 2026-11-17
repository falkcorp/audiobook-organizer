// file: internal/metadata/audnexus_bad_asin_test.go
// version: 1.3.0
// guid: 5e8a2d93-7c14-4b60-8f3e-a1d9c06b7e52
// last-edited: 2026-11-17

package metadata

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// These build the client on a plain http.Client: the 2 rps token bucket of the
// production client is orthogonal to what is counted here (requests sent).

const badASINBody = `{"error":{"code":"BAD_REQUEST","message":"Bad ASIN"}}`

// plainAudnexus returns a client for srv with no pacing, and a counter of the
// requests the server saw, with their paths and region queries.
func plainAudnexus(handler func(w http.ResponseWriter, r *http.Request)) (*AudnexusClient, *requestLog, func()) {
	rl := &requestLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rl.add(r.URL.Path + "?" + r.URL.RawQuery)
		handler(w, r)
	}))
	return &AudnexusClient{httpClient: srv.Client(), baseURL: srv.URL}, rl, srv.Close
}

type requestLog struct {
	mu   sync.Mutex
	seen []string
}

func (l *requestLog) add(s string) { l.mu.Lock(); l.seen = append(l.seen, s); l.mu.Unlock() }
func (l *requestLog) n() int       { l.mu.Lock(); defer l.mu.Unlock(); return len(l.seen) }

func TestAudnexusLookup_NeverSendsAnInvalidASIN(t *testing.T) {
	c, rl, done := plainAudnexus(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
	defer done()
	for _, bad := range []string{"Batmobiles", "batmobiles", "Gothamcity", "B0GOTHAM9", "", "B0GOTHAM99!", "ABCDEFGHIJ"} {
		_, err := c.LookupByASIN(context.Background(), bad)
		if !IsBadASIN(err) {
			t.Errorf("LookupByASIN(%q): err = %v, want ErrBadASIN", bad, err)
		}
		if IsNotFound(err) {
			t.Errorf("LookupByASIN(%q): a bad ASIN must not read as a not-found (the caller would ask five more regions)", bad)
		}
	}
	if rl.n() != 0 {
		t.Fatalf("server saw %d requests for ASINs that cannot be valid, want 0", rl.n())
	}
}

func TestAudnexusLookup_NormalisesAStoredASINsCase(t *testing.T) {
	c, rl, done := plainAudnexus(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"asin":"B0GOTHAM99","title":"Year One"}`))
	})
	defer done()
	meta, err := c.LookupByASIN(context.Background(), " b0gotham99 ")
	if err != nil || meta == nil {
		t.Fatalf("lookup = %v, %v", meta, err)
	}
	if rl.n() != 1 || rl.seen[0] != "/books/B0GOTHAM99?" {
		t.Fatalf("request = %v, want /books/B0GOTHAM99 (upper case: Audnexus 400s a lower-case ASIN)", rl.seen)
	}
}

// A 400 from the provider is final for the ASIN: the first region's answer is
// the answer, no other region is asked.
func TestAudnexusLookup_BadASIN400StopsAtTheFirstRegion(t *testing.T) {
	c, rl, done := plainAudnexus(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(badASINBody))
	})
	defer done()
	_, err := c.LookupByASINInRegions(context.Background(), "B0GOTHAM99", []string{"", "uk", "au", "ca", "in"})
	if !IsBadASIN(err) {
		t.Fatalf("err = %v, want a bad ASIN", err)
	}
	if IsNotFound(err) {
		t.Fatalf("a 400 must not read as not-found: %v", err)
	}
	if rl.n() != 1 {
		t.Fatalf("server saw %d requests, want 1 (was 3 on the search path, 9 on the full list)", rl.n())
	}
	if _, _, hold := ClassifyProviderError(err); hold {
		t.Fatalf("a bad ASIN must not hold the provider: %v", err)
	}
}

// A 400 that is not the provider's own JSON (a proxy's HTML page) says nothing
// about the ASIN: it is a failure and the other regions are still asked.
func TestAudnexusLookup_NonJSON400IsAFailureNotABadASIN(t *testing.T) {
	for name, body := range map[string]string{
		"html":                "<html>Bad Request</html>",
		"json about a region": `{"error":{"code":"BAD_REQUEST","message":"Bad region"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			c, rl, done := plainAudnexus(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(body))
			})
			defer done()
			_, err := c.LookupByASINInRegions(context.Background(), "B0GOTHAM99", []string{"", "uk", "au"})
			if err == nil || IsBadASIN(err) || IsNotFound(err) {
				t.Fatalf("err = %v, want a plain failure", err)
			}
			if rl.n() != 3 {
				t.Fatalf("server saw %d requests, want 3", rl.n())
			}
		})
	}
}

// A search context whose ASIN is not one has nothing to be asked: no request,
// no error (an error would mark Audnexus failed and feed its breaker).
func TestAudnexusSearchByContext_NotAnASINIsNothingToAsk(t *testing.T) {
	c, rl, done := plainAudnexus(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(badASINBody))
	})
	defer done()
	got, err := c.SearchByContext(context.Background(), &SearchContext{ASIN: "Batmobiles"})
	if err != nil || got != nil {
		t.Fatalf("SearchByContext(word) = %v, %v; want nil, nil", got, err)
	}
	if rl.n() != 0 {
		t.Fatalf("server saw %d requests, want 0", rl.n())
	}
	// A well-formed ASIN the provider still refuses is the provider's answer.
	got, err = c.SearchByContext(context.Background(), &SearchContext{ASIN: "B0GOTHAM99"})
	if err != nil || got != nil {
		t.Fatalf("SearchByContext(refused ASIN) = %v, %v; want nil, nil", got, err)
	}
	if rl.n() != 1 {
		t.Fatalf("server saw %d requests, want 1", rl.n())
	}
}

// The lookup sets no deadline of its own: the request's time bound is the
// transport's attempt timeout, which starts after the token. A deadline set
// here would run while the request queued on the shared bucket.
func TestAudnexusLookup_SetsNoDeadlineOfItsOwn(t *testing.T) {
	var hadDeadline bool
	rt := roundTripFn(func(r *http.Request) (*http.Response, error) {
		_, hadDeadline = r.Context().Deadline()
		return nil, errors.New("stop")
	})
	c := &AudnexusClient{httpClient: &http.Client{Transport: rt}, baseURL: "http://192.0.2.1"}
	_, _ = c.LookupByASINInRegions(context.Background(), "B0GOTHAM99", []string{""})
	if hadDeadline {
		t.Fatal("the region request carries a deadline set by the lookup (it would include the queue)")
	}
}

type roundTripFn func(*http.Request) (*http.Response, error)

func (f roundTripFn) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestIsBadASIN(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&ProviderStatusError{Provider: "x", Status: 400, Body: badASINBody}, true},
		{fmt.Errorf("region=: %w", &ProviderStatusError{Provider: "x", Status: 400, Body: `{"error":{"code":"BAD_REQUEST","message":"Bad ASIN"}}`}), true},
		// The provider's JSON about something else is not a verdict on the ASIN.
		{&ProviderStatusError{Provider: "x", Status: 400, Body: `{"error":{"code":"BAD_REQUEST","message":"Bad region"}}`}, false},
		{&ProviderStatusError{Provider: "x", Status: 400, Body: `{"error":{"code":"invalid_argument","message":"Invalid query"}}`}, false},
		{&ProviderStatusError{Provider: "x", Status: 400, Body: `["Bad ASIN"]`}, false},
		{fmt.Errorf("not sent: %w", ErrBadASIN), true},
		{&ProviderStatusError{Provider: "x", Status: 400, Body: "<html>"}, false},
		{&ProviderStatusError{Provider: "x", Status: 400}, false},
		{&ProviderStatusError{Provider: "x", Status: 400, Body: "<body unreadable: EOF>", BodyUnreadable: true}, false},
		{&ProviderStatusError{Provider: "x", Status: 404, Body: `{"a":1}`}, false},
		{&ProviderStatusError{Provider: "x", Status: 500, Body: `{"a":1}`}, false},
		{context.DeadlineExceeded, false},
		{nil, false},
	} {
		if got := IsBadASIN(tc.err); got != tc.want {
			t.Errorf("IsBadASIN(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

type timeoutNetErr struct{ timeout bool }

func (e timeoutNetErr) Error() string   { return "net error" }
func (e timeoutNetErr) Timeout() bool   { return e.timeout }
func (e timeoutNetErr) Temporary() bool { return false }

var _ net.Error = timeoutNetErr{}

func TestFailureCause(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"bad ASIN", &ProviderStatusError{Provider: "x", Status: 400, Body: badASINBody}, CauseBadASIN},
		{"not sent", ErrBadASIN, CauseBadASIN},
		{"our deadline through url.Error", fmt.Errorf(`Get "http://192.0.2.1/x": %w`, context.DeadlineExceeded), CauseTimeout},
		{"network timeout", timeoutNetErr{true}, CauseTimeout},
		{"network failure", timeoutNetErr{false}, CauseTransport},
		{"limiter deadline refusal, wrapped as providerhttp and url.Error wrap it",
			fmt.Errorf(`Get "http://192.0.2.1/x": %w`, fmt.Errorf("providerhttp audnexus: rate limit wait: %w",
				errors.New("rate: Wait(n=1) would exceed context deadline"))), CauseTimeout},
		{"cancel", context.Canceled, CauseCanceled},
		{"429", &ProviderStatusError{Provider: "x", Status: 429}, CauseRateLimited},
		{"throttled", ErrProviderThrottled, CauseHeld},
		{"breaker open", ErrCircuitOpen, CauseHeld},
		{"502", &ProviderStatusError{Provider: "x", Status: 502}, CauseUpstream},
		{"plain 403", &ProviderStatusError{Provider: "x", Status: 403, Body: "forbidden"}, CauseClientError},
		{"non-JSON 400", &ProviderStatusError{Provider: "x", Status: 400, Body: "<html>"}, CauseClientError},
		{"another provider's JSON 400", &ProviderStatusError{Provider: "google-books", Status: 400, Body: `{"error":{"code":400,"message":"Invalid value"}}`}, CauseClientError},
		{"anything else", errors.New("decode failed"), CauseOther},
	} {
		if got := FailureCause(tc.err); got != tc.want {
			t.Errorf("%s: FailureCause(%v) = %q, want %q", tc.name, tc.err, got, tc.want)
		}
	}
}

// The limiter's refusal to queue a call past the caller's deadline has no
// sentinel, so FailureCause matches its text. This builds it with the real
// limiter and the real transport wrap, so a reworded library message or a changed
// wrap fails here.
func TestFailureCause_LimiterDeadlineRefusal(t *testing.T) {
	lim := rate.NewLimiter(rate.Every(time.Hour), 1)
	if err := lim.Wait(context.Background()); err != nil { // spends the burst token
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := lim.Wait(ctx) // the next token is an hour away
	if err == nil {
		t.Fatal("the limiter granted a token it did not have")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Skipf("the limiter now wraps the context error (%v); FailureCause needs no text match", err)
	}
	wrapped := fmt.Errorf(`Get "http://192.0.2.1/x": %w`, fmt.Errorf("providerhttp audnexus: rate limit wait: %w", err))
	if got := FailureCause(wrapped); got != CauseTimeout {
		t.Fatalf("FailureCause(%v) = %q, want %q", wrapped, got, CauseTimeout)
	}
	// Burst exceeded is a different refusal and stays "other".
	if got := FailureCause(errors.New("rate: Wait(n=2) exceeds limiter's burst 1")); got != CauseOther {
		t.Fatalf("burst refusal classified %q, want other", got)
	}
}

// A region that times out ends the lookup: the next region would wait the same
// way. One request, not one per region, bounds a hung Audnexus to one region's
// worst case. A region that answers 5xx is another matter (the ASIN may live in
// the next one) and still tries on, pinned by the next test.
type timeoutTransport struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (tt *timeoutTransport) RoundTrip(*http.Request) (*http.Response, error) {
	tt.mu.Lock()
	tt.calls++
	tt.mu.Unlock()
	return nil, tt.err
}

func TestAudnexusLookup_StopsAtTheFirstRegionThatTimesOut(t *testing.T) {
	for name, terr := range map[string]error{
		"attempt deadline": context.DeadlineExceeded,
		"limiter refusal":  errors.New("rate limit wait: rate: Wait(n=1) would exceed context deadline"),
	} {
		t.Run(name, func(t *testing.T) {
			tt := &timeoutTransport{err: terr}
			c := &AudnexusClient{httpClient: &http.Client{Transport: tt}, baseURL: "http://192.0.2.1"}
			_, err := c.LookupByASINInRegions(context.Background(), "B0GOTHAM99", []string{"", "uk", "au"})
			if FailureCause(err) != CauseTimeout {
				t.Fatalf("err = %v (cause %q), want a timeout", err, FailureCause(err))
			}
			if tt.calls != 1 {
				t.Fatalf("requests = %d, want 1: a timed-out region must end the lookup", tt.calls)
			}
		})
	}
}

func TestAudnexusLookup_A5xxRegionStillTriesTheNext(t *testing.T) {
	c, rl, closeSrv := plainAudnexus(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	defer closeSrv()
	_, err := c.LookupByASINInRegions(context.Background(), "B0GOTHAM99", []string{"", "uk", "au"})
	if FailureCause(err) != CauseUpstream {
		t.Fatalf("err = %v, want upstream_5xx", err)
	}
	if rl.n() != 3 {
		t.Fatalf("requests = %d, want 3: a 5xx is not a hang", rl.n())
	}
}
