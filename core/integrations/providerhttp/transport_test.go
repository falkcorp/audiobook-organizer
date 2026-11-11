// file: core/integrations/providerhttp/transport_test.go
// version: 1.0.0
// guid: e41f8797-cbc5-47da-9318-e0b625e78606
// last-edited: 2026-11-11

package providerhttp_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
)

func failThenOK(n int, status int, retryAfter string) http.HandlerFunc {
	var calls atomic.Int64
	return func(w http.ResponseWriter, r *http.Request) {
		if int(calls.Add(1)) <= n {
			if retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
			w.WriteHeader(status)
			return
		}
		ok(w, r)
	}
}

func retryLimits(retries int) integrations.Limits {
	return integrations.Limits{RPS: 1000, Burst: 1000, MaxRetries: retries, Timeout: 5 * time.Second}
}

// TestRetryHonoursRetryAfterAndClamp: a Retry-After in either form is waited
// out, a huge one is clamped to the maximum, and with none the back-off is
// 1s, 2s, 4s.
func TestRetryHonoursRetryAfterAndClamp(t *testing.T) {
	cases := []struct {
		name       string
		retryAfter string
		retries    int
		fails      int
		max        time.Duration
		want       []time.Duration
	}{
		{"seconds", "2", 3, 1, 0, []time.Duration{2 * time.Second}},
		{"clamped to the default 5m", "86400", 3, 1, 0, []time.Duration{5 * time.Minute}},
		{"clamped to the instance maximum", "3600", 3, 1, 10 * time.Second, []time.Duration{10 * time.Second}},
		{"exponential without the header", "", 3, 3, 0, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}},
		{"unusable header falls back", "soon", 3, 1, 0, []time.Duration{time.Second}},
		{"negative header falls back", "-5", 3, 1, 0, []time.Duration{time.Second}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clk := newClock()
			r := newRegistry(t, clk)
			srv, hits := server(t, failThenOK(tc.fails, http.StatusServiceUnavailable, tc.retryAfter))
			l := retryLimits(tc.retries)
			l.RetryAfterMax = tc.max
			c := r.Client(batcave, providerhttp.Policy{Limits: l, Private: anyPrivate})
			resp, err := get(t, c, srv.URL)
			if err != nil || resp.StatusCode != 200 {
				t.Fatalf("got %v, %v", resp, err)
			}
			if got := clk.Sleeps(); !equalDurations(got, tc.want) {
				t.Fatalf("waits = %v, want %v", got, tc.want)
			}
			if hits.Load() != int64(tc.fails+1) {
				t.Fatalf("hits = %d, want %d", hits.Load(), tc.fails+1)
			}
			if _, held := r.HoldFor(batcave); held {
				t.Fatal("a request that recovered by retry must not hold the scope")
			}
		})
	}
	t.Run("http date", func(t *testing.T) {
		clk := newClock()
		r := newRegistry(t, clk)
		srv, _ := server(t, failThenOK(1, http.StatusTooManyRequests, clk.Now().Add(90*time.Second).UTC().Format(http.TimeFormat)))
		c := r.Client(batcave, providerhttp.Policy{Limits: retryLimits(3), Private: anyPrivate})
		if _, err := get(t, c, srv.URL); err != nil {
			t.Fatal(err)
		}
		if got := clk.Sleeps(); len(got) != 1 || got[0] < 88*time.Second || got[0] > 90*time.Second {
			t.Fatalf("waits = %v, want about 90s", got)
		}
	})
}

func equalDurations(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestProbeNeverRetries: the half-open probe is ONE request. A failing
// instance with MaxRetries 3 costs four requests while the breaker is closed;
// the probe that follows the cooldown costs one.
func TestProbeNeverRetries(t *testing.T) {
	clk := newClock()
	r := newRegistry(t, clk)
	srv, hits := server(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	lim := retryLimits(3)
	lim.Breaker = integrations.BreakerPolicy{Threshold: 1, Cooldown: 10 * time.Second, ProbeTimeout: 5 * time.Second}
	lim.Holds = integrations.HoldPolicy{Durations: map[integrations.Class]time.Duration{integrations.ClassTransient: time.Millisecond}}
	c := r.Client(batcave, providerhttp.Policy{Limits: lim, Private: anyPrivate})
	if _, err := get(t, c, srv.URL+"/a"); err != nil {
		t.Fatal(err)
	}
	if got := hits.Load(); got != 4 {
		t.Fatalf("a closed breaker: %d requests, want 1 + 3 retries", got)
	}
	clk.Advance(11 * time.Second) // hold and cooldown are over
	if _, err := get(t, c, srv.URL+"/b"); err != nil {
		t.Fatal(err)
	}
	if got := hits.Load(); got != 5 {
		t.Errorf("the probe sent %d requests, want exactly 1", got-4)
	}
}

// TestRetryExhaustedReturnsLastAnswerAndHolds: retries are bounded; the last
// answer reaches the caller and the REQUEST (not each attempt) is the one
// failure that holds the scope.
func TestRetryExhaustedReturnsLastAnswerAndHolds(t *testing.T) {
	clk := newClock()
	r := newRegistry(t, clk)
	srv, hits := server(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	c := r.Client(batcave, providerhttp.Policy{Limits: retryLimits(2), Private: anyPrivate})
	resp, err := get(t, c, srv.URL)
	if err != nil || resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("got %v, %v; want the last 502", resp, err)
	}
	if hits.Load() != 3 {
		t.Fatalf("hits = %d, want 3 (1 + 2 retries)", hits.Load())
	}
	var held []integrations.Hold
	for h := range r.Holds() {
		held = append(held, h)
	}
	if len(held) != 1 || held[0].Until.Sub(clk.Now()) != 30*time.Minute || held[0].Class != integrations.ClassTransient {
		t.Fatalf("holds = %+v, want one 30m transient hold (5xx)", held)
	}
	// While held, nothing is sent.
	if _, err := get(t, c, srv.URL); !refusedFor(err, providerhttp.ReasonHold) || hits.Load() != 3 {
		t.Fatalf("held request: %v (hits %d)", err, hits.Load())
	}
}

// TestNoRetryWithoutGetBody: a request whose body cannot be replayed is never
// retried: the retryable answer is returned as is.
func TestNoRetryWithoutGetBody(t *testing.T) {
	clk := newClock()
	r := newRegistry(t, clk)
	srv, hits := server(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	c := r.Client(batcave, providerhttp.Policy{Limits: retryLimits(3), Private: anyPrivate})
	req, _ := http.NewRequest(http.MethodPost, srv.URL, io.NopCloser(strings.NewReader("batarang")))
	if req.GetBody != nil {
		t.Fatal("test setup: this body must have no GetBody")
	}
	resp, err := c.Do(req)
	if err != nil || resp.StatusCode != 503 {
		t.Fatalf("got %v, %v", resp, err)
	}
	_ = resp.Body.Close()
	if hits.Load() != 1 || len(clk.Sleeps()) != 0 {
		t.Fatalf("hits %d waits %v: a body that cannot be replayed was resent", hits.Load(), clk.Sleeps())
	}
	// With GetBody the same request retries, and every attempt carries the body.
	var bodies []string
	srv2, hits2 := server(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	req2, _ := http.NewRequest(http.MethodPost, srv2.URL, bytes.NewReader([]byte("batarang")))
	c = r.Client(ref("alfred"), providerhttp.Policy{Limits: retryLimits(3), Private: anyPrivate}) // batcave is held now
	resp2, err := c.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if hits2.Load() != 4 || len(bodies) != 4 {
		t.Fatalf("hits = %d, want 4", hits2.Load())
	}
	for i, b := range bodies {
		if b != "batarang" {
			t.Errorf("attempt %d body = %q: an empty body was resent", i, b)
		}
	}
}

// TestRetrySpendsQuotaPerAttempt: a retry spends a bucket token and a quota
// unit like the first try.
func TestRetrySpendsQuotaPerAttempt(t *testing.T) {
	r := newRegistry(t, newClock())
	srv, hits := server(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	l := retryLimits(5)
	l.Daily = daily(3, 3)
	c := r.Client(batcave, providerhttp.Policy{Limits: l, Private: anyPrivate})
	_, err := get(t, c, srv.URL)
	if !refusedFor(err, providerhttp.ReasonQuota) {
		t.Fatalf("err = %v, want the quota to stop the retries", err)
	}
	if hits.Load() != 3 {
		t.Fatalf("hits = %d, want exactly the 3 units of quota", hits.Load())
	}
	if _, held := r.HoldFor(batcave); !held {
		t.Fatal("the failures the retries saw must still hold the scope")
	}
}

// TestRedactsApikeyInURLError: no error from Do, GetJSON or the gate carries
// the credential of a request URL.
func TestRedactsApikeyInURLError(t *testing.T) {
	const secret = "joker-card-8675309"
	r := newRegistry(t, newClock())
	srv, _ := server(t, ok)
	c := r.Client(batcave, providerhttp.Policy{Limits: limits(100, 100), Redact: []string{"x_gotham_token"}}) // no trust: refused
	for _, u := range []string{
		srv.URL + "/search?q=batman&apikey=" + secret,
		srv.URL + "/search?q=batman&API_KEY=" + secret + "&page=2",
		srv.URL + "/search?key=" + secret,
		srv.URL + "/search?x_gotham_token=" + secret,
		strings.Replace(srv.URL, "http://", "http://alfred:"+secret+"@", 1) + "/x",
	} {
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		_, err := c.Do(req)
		if err == nil {
			t.Fatalf("%s was sent", u)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error leaks the credential: %v", err)
		}
		var ue *url.Error
		if errors.As(err, &ue) && strings.Contains(ue.URL, secret) {
			t.Errorf("url.Error.URL leaks the credential: %v", ue.URL)
		}
		if !errors.Is(err, providerhttp.ErrBlockedAddress) || integrations.ClassOf(context.Background(), err) != integrations.ClassRefusedLocal {
			t.Errorf("redaction lost the cause: %v", err)
		}
		if strings.Contains(u, "alfred:") && strings.Contains(err.Error(), "alfred") {
			t.Errorf("error keeps the userinfo name: %v", err)
		}
		if !strings.Contains(err.Error(), "search") && strings.Contains(u, "search") {
			t.Errorf("redaction removed the whole URL: %v", err)
		}
	}
	// A transport failure (connection refused) is redacted the same way.
	trusted := r.Client(ref("alfred"), providerhttp.Policy{Limits: limits(100, 100), Private: anyPrivate})
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/x?apikey="+secret, nil)
	if _, err := trusted.Do(req); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("connection failure error = %v", err)
	}
	// The remote's own error body is bounded and scrubbed.
	echo, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `bad request: GET /x?apikey=`+r.URL.Query().Get("apikey")+` {"token": "`+secret+`"} `+strings.Repeat("z", 2000))
	})
	var out any
	trusted = r.Client(ref("selina"), providerhttp.Policy{Limits: limits(100, 100), Private: anyPrivate})
	err := trusted.GetJSON(context.Background(), echo.URL+"/x?apikey="+secret, &out)
	e, ok := errors.AsType[*integrations.Error](err)
	if !ok || e.Class != integrations.ClassInvalid || e.Status != 400 {
		t.Fatalf("GetJSON err = %#v", err)
	}
	if strings.Contains(e.Detail, secret) || strings.Contains(err.Error(), secret) || strings.Contains(e.GoString(), secret) {
		t.Errorf("the remote's echo of the credential reached the error: %q", e.Detail)
	}
	if len(e.Detail) > integrations.MaxDetail || !strings.Contains(e.Detail, "REDACTED") {
		t.Errorf("detail = %d bytes %q", len(e.Detail), e.Detail[:min(80, len(e.Detail))])
	}
}

// TestNoSecretInGateState: nothing the gate keeps (hold detail, scope records,
// metrics labels) carries a request's credential.
func TestNoSecretInGateState(t *testing.T) {
	const secret = "riddler-key-424242"
	clk := newClock()
	gs := providerhttp.NativeGateStore(store(t))
	r := newRegistry(t, clk, func(o *providerhttp.Options) { o.Store = gs })
	srv, _ := server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "denied for apikey="+secret)
	})
	c := r.Client(batcave, providerhttp.Policy{Limits: limits(100, 100), Private: anyPrivate})
	_, _ = get(t, c, srv.URL+"/x?apikey="+secret)
	hs, err := gs.Holds(context.Background())
	if err != nil || len(hs) != 1 {
		t.Fatalf("holds = %+v, %v", hs, err)
	}
	if hs[0].Class != integrations.ClassAuth || strings.Contains(hs[0].Detail, secret) || strings.Contains(hs[0].Scope, secret) {
		t.Fatalf("hold = %+v", hs[0])
	}
}

// TestResponseSizeLimit: a body past Policy.MaxBody fails the read with
// ErrResponseTooLarge, whether the size is announced or not, for the copied
// client as well.
func TestResponseSizeLimit(t *testing.T) {
	r := newRegistry(t, newClock())
	chunked, _ := server(t, func(w http.ResponseWriter, _ *http.Request) {
		for range 10 {
			_, _ = w.Write(bytes.Repeat([]byte("a"), 1024))
			w.(http.Flusher).Flush()
		}
	})
	announced, _ := server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "10240")
		_, _ = w.Write(bytes.Repeat([]byte("a"), 10240))
	})
	exact, _ := server(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(bytes.Repeat([]byte("a"), 4096)) })
	c := r.Client(batcave, providerhttp.Policy{Limits: limits(1000, 1000), Private: anyPrivate, MaxBody: 4096})
	cp := *c.Client()

	resp, err := cp.Get(chunked.URL)
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if !errors.Is(err, providerhttp.ErrResponseTooLarge) || n != 4096 {
		t.Fatalf("unannounced: read %d bytes, err %v; want 4096 then ErrResponseTooLarge", n, err)
	}
	if _, err := cp.Get(announced.URL); !errors.Is(err, providerhttp.ErrResponseTooLarge) {
		t.Fatalf("announced: err = %v", err)
	} else if integrations.ClassOf(context.Background(), err) != integrations.ClassBadOutput {
		t.Fatalf("announced: class = %v, want bad output", integrations.ClassOf(context.Background(), err))
	}
	resp, err = cp.Get(exact.URL)
	if err != nil {
		t.Fatal(err)
	}
	n, err = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if err != nil || n != 4096 {
		t.Fatalf("exactly at the limit: read %d, %v; want a clean read", n, err)
	}
	var out struct{}
	if err := c.GetJSON(context.Background(), chunked.URL, &out); !errors.Is(err, providerhttp.ErrResponseTooLarge) {
		t.Fatalf("GetJSON err = %v", err)
	}
}

// TestAttemptTimeoutOutlivesRoundTrip: the per-attempt deadline is released on
// Close, so a body is readable after Do returns; and a hung attempt times out
// as a transient failure on a live parent.
func TestAttemptTimeoutOutlivesRoundTrip(t *testing.T) {
	r := newRegistry(t, newClock())
	srv, _ := server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(w, "robin")
	})
	l := limits(1000, 1000)
	l.Timeout = 5 * time.Second
	c := r.Client(batcave, providerhttp.Policy{Limits: l, Private: anyPrivate})
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || string(b) != "robin" {
		t.Fatalf("body = %q, %v: the deadline was released before the body was read", b, err)
	}

	hang, _ := server(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	l.Timeout = 40 * time.Millisecond
	c2 := r.Client(ref("alfred"), providerhttp.Policy{Limits: l, Private: anyPrivate})
	_, err = getCtx(t, context.Background(), c2, hang.URL)
	if err == nil || integrations.ClassOf(context.Background(), err) != integrations.ClassTransient {
		t.Fatalf("hung attempt: %v (class %v), want transient", err, integrations.ClassOf(context.Background(), err))
	}
	if _, held := r.HoldFor(ref("alfred")); !held {
		t.Fatal("a hung provider must hold the scope")
	}
}

// TestCanceledCountsForNothing: the caller's cancellation is not the
// provider's failure, over the transport as over the gate.
func TestCanceledCountsForNothing(t *testing.T) {
	r := newRegistry(t, newClock())
	srv, _ := server(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	c := r.Client(batcave, providerhttp.Policy{Limits: limits(1000, 1000), Private: anyPrivate})
	for range 8 {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, err := getCtx(t, ctx, c, srv.URL)
		cancel()
		if integrations.ClassOf(ctx, err) != integrations.ClassCanceled {
			t.Fatalf("err = %v, want canceled", err)
		}
	}
	if st := c.State(); st.Breaker != integrations.BreakerClosed || st.Hold != nil {
		t.Fatalf("state after cancellations = %+v", st)
	}
}

// TestUserAgentAndSharedLimitsAcrossGateAndHTTP: the Registry's user agent is
// sent when the request has none, and the HTTP client and the bare gate of
// one scope are held together.
func TestUserAgentAndSharedGate(t *testing.T) {
	var ua atomic.Value
	srv, _ := server(t, func(w http.ResponseWriter, r *http.Request) { ua.Store(r.UserAgent()); ok(w, r) })
	r := newRegistry(t, newClock(), func(o *providerhttp.Options) { o.UserAgent = "batbooks/1.0 (+mediacore/1)" })
	c := r.Client(batcave, providerhttp.Policy{Limits: limits(1000, 1000), Private: anyPrivate})
	if _, err := get(t, c, srv.URL); err != nil || ua.Load() != "batbooks/1.0 (+mediacore/1)" {
		t.Fatalf("user agent = %v, %v", ua.Load(), err)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("User-Agent", "robin")
	if resp, err := c.Do(req); err == nil {
		_ = resp.Body.Close()
	}
	if ua.Load() != "robin" {
		t.Fatalf("a request's own user agent was replaced: %v", ua.Load())
	}
	tk, _ := r.GateFor(batcave, providerhttp.Policy{Limits: limits(1000, 1000)}).Acquire(context.Background(), "")
	tk.Done(&integrations.Error{Class: integrations.ClassAuth, Status: 401})
	if _, err := get(t, c, srv.URL); !refusedFor(err, providerhttp.ReasonHold) {
		t.Fatalf("a hold set through the gate must stop the HTTP client: %v", err)
	}
}

// TestClassify: the body is read before the status.
// TestClassify_Google403RateLimitIsRateLimited and
// TestClassify_DailyQuotaBodyBeatsStatus are the two named cases of M2.9: the
// BODY is read before the status (Google answers 403 for both).
func TestClassify_Google403RateLimitIsRateLimited(t *testing.T) {
	resp := &http.Response{StatusCode: 403, Header: http.Header{}}
	body := `{"error":{"code":403,"errors":[{"reason":"rateLimitExceeded"}]}}`
	if got := providerhttp.Classify(context.Background(), resp, []byte(body), nil); got != integrations.ClassRateLimited {
		t.Errorf("403 rateLimitExceeded: %v, want rate_limited (not auth)", got)
	}
}

func TestClassify_DailyQuotaBodyBeatsStatus(t *testing.T) {
	body := `{"error":{"message":"Quota exceeded for quota metric 'Queries' and limit 'Queries per day'"}}`
	for _, status := range []int{403, 429} {
		resp := &http.Response{StatusCode: status, Header: http.Header{}}
		if got := providerhttp.Classify(context.Background(), resp, []byte(body), nil); got != integrations.ClassQuota {
			t.Errorf("%d daily quota body: %v, want quota", status, got)
		}
	}
}

func TestClassify(t *testing.T) {
	ctx := context.Background()
	resp := func(status int, h ...string) *http.Response {
		r := &http.Response{StatusCode: status, Header: http.Header{}}
		for i := 0; i+1 < len(h); i += 2 {
			r.Header.Set(h[i], h[i+1])
		}
		return r
	}
	google403 := `{"error":{"code":403,"errors":[{"reason":"rateLimitExceeded"}]}}`
	dayQuota := `{"error":{"message":"Quota exceeded for quota metric 'Queries' and limit 'Queries per day'"}}`
	cases := []struct {
		name   string
		status int
		body   string
		want   integrations.Class
	}{
		{"google 403 rate limit is rate limited", 403, google403, integrations.ClassRateLimited},
		{"google 403 userRateLimit", 403, `{"reason":"userRateLimitExceeded"}`, integrations.ClassRateLimited},
		{"bare 403 is auth", 403, "forbidden", integrations.ClassAuth},
		{"401 is auth", 401, "", integrations.ClassAuth},
		{"daily quota body beats 429", 429, dayQuota, integrations.ClassQuota},
		{"daily quota body beats 403", 403, dayQuota, integrations.ClassQuota},
		{"dailyLimitExceeded", 403, `{"reason":"dailyLimitExceeded"}`, integrations.ClassQuota},
		{"plain 429 is rate limited", 429, "", integrations.ClassRateLimited},
		{"daily words in a 400 mean nothing", 400, dayQuota, integrations.ClassInvalid},
		{"500", 500, "", integrations.ClassTransient},
		{"503", 503, "", integrations.ClassTransient},
		{"408", 408, "", integrations.ClassTransient},
		{"404 is not found", 404, "", integrations.ClassNotFound},
		{"410 is not found", 410, "", integrations.ClassNotFound},
		{"422 is invalid", 422, "", integrations.ClassInvalid},
		{"400 is invalid", 400, "", integrations.ClassInvalid},
		{"200 has nothing to classify", 200, "", integrations.ClassUnknown},
		{"302 has nothing to classify", 302, "", integrations.ClassUnknown},
	}
	for _, tc := range cases {
		if got := providerhttp.Classify(ctx, resp(tc.status), []byte(tc.body), nil); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	// Errors.
	done, cancel := context.WithCancel(ctx)
	cancel()
	for name, tc := range map[string]struct {
		ctx  context.Context
		err  error
		want integrations.Class
	}{
		"parent canceled":           {done, errors.New("anything"), integrations.ClassCanceled},
		"deadline on a live parent": {ctx, context.DeadlineExceeded, integrations.ClassTransient},
		"egress refusal":            {ctx, providerhttp.ErrBlockedAddress, integrations.ClassRefusedLocal},
		"too large":                 {ctx, providerhttp.ErrResponseTooLarge, integrations.ClassBadOutput},
		"unrecognised":              {ctx, errors.New("odd"), integrations.ClassUnknown},
	} {
		if got := providerhttp.Classify(tc.ctx, nil, nil, tc.err); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

// TestGetJSON: decoding, classification of a non-2xx answer with its
// Retry-After, and an undecodable answer as bad output.
func TestGetJSON(t *testing.T) {
	r := newRegistry(t, newClock())
	srv, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = io.WriteString(w, `{"hero":"Batman"}`)
		case "/limited":
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(429)
		case "/junk":
			_, _ = io.WriteString(w, "<html>")
		}
	})
	c := r.Client(batcave, providerhttp.Policy{Limits: limits(1000, 1000), Private: anyPrivate})
	var got struct{ Hero string }
	if err := c.GetJSON(context.Background(), srv.URL+"/ok", &got); err != nil || got.Hero != "Batman" {
		t.Fatalf("ok: %+v, %v", got, err)
	}
	err := c.GetJSON(context.Background(), srv.URL+"/limited", &got)
	e, isErr := errors.AsType[*integrations.Error](err)
	if !isErr || e.Class != integrations.ClassRateLimited || e.Status != 429 || e.RetryAfter != 2*time.Minute {
		t.Fatalf("limited: %#v", err)
	}
	r2 := newRegistry(t, newClock())
	c2 := r2.Client(batcave, providerhttp.Policy{Limits: limits(1000, 1000), Private: anyPrivate})
	err = c2.GetJSON(context.Background(), srv.URL+"/junk", &got)
	if e, isErr := errors.AsType[*integrations.Error](err); !isErr || e.Class != integrations.ClassBadOutput {
		t.Fatalf("junk: %#v", err)
	}
	if err := c2.GetJSON(context.Background(), "://bad", &got); integrations.ClassOf(context.Background(), err) != integrations.ClassInvalid {
		t.Fatalf("bad url: %v", err)
	}
}
