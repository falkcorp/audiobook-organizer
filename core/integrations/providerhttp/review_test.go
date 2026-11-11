// file: core/integrations/providerhttp/review_test.go
// version: 1.0.0
// guid: 1ac0cd3e-0f0c-4b8e-9fb3-5d3d1f0f6a21
// last-edited: 2026-11-11

package providerhttp_test

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
)

// Security review of the providerhttp PR: the credential-leak and the retry,
// refund and downgrade findings, each with the test that pins it.

const otherName = "penguin.example.test"

// byName makes fake public names stand for the loopback servers a test starts:
// every name resolves to the documentation address 192.0.2.10 (public as far as
// the guard goes) and the dial connects that address, on the URL's port, to
// loopback. Servers are told apart by port.
func byName(o *providerhttp.Options) {
	o.Resolver = func(_ context.Context, host string) ([]net.IP, error) {
		if ip := net.ParseIP(host); ip != nil {
			return []net.IP{ip}, nil
		}
		return ips("192.0.2.10"), nil
	}
	o.DialAddr = func(ctx context.Context, network, addr string) (net.Conn, error) {
		h, p, _ := net.SplitHostPort(addr)
		if strings.HasPrefix(h, "192.0.2.") {
			h = "127.0.0.1"
		}
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(h, p))
	}
	o.Base = &http.Transport{DisableKeepAlives: true} // every request dials
}

func urlOn(srv *httptest.Server, name, path string) string {
	_, port, _ := net.SplitHostPort(hostOf(srv))
	return "http://" + name + ":" + port + path
}

// headerLog records the headers and the query of the requests a server saw.
type headerLog struct {
	mu   sync.Mutex
	last http.Header
	qry  url.Values
	n    int
}

func (h *headerLog) record(r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.last, h.qry, h.n = r.Header.Clone(), r.URL.Query(), h.n+1
}

func (h *headerLog) get() (http.Header, url.Values, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.last, h.qry, h.n
}

var credentialHeaders = map[string]string{
	"X-Api-Key":           "joker-key-1",
	"X-Plex-Token":        "joker-token-2",
	"Authorization":       "Bearer joker-bearer-3",
	"Cookie":              "session=joker-cookie-4",
	"Proxy-Authorization": "Basic am9rZXI6Y2FyZA==",
	"X-Batcave-Pin":       "joker-pin-5", // named by the adapter (Redact)
	"X-Refresh-Token":     "joker-refresh-6",
}

func withCredentials(req *http.Request) {
	for k, v := range credentialHeaders {
		req.Header.Set(k, v)
	}
	req.Header.Set("X-Trace", "robin-1")
}

// TestRedirectToOtherEndpoint_DropsCredentialHeaders (M1): a 302 to another
// host reaches it without any credential header (net/http drops Authorization
// and Cookie by itself; the others it forwards); a redirect within the same
// endpoint keeps them.
func TestRedirectToOtherEndpoint_DropsCredentialHeaders(t *testing.T) {
	var other, landed headerLog
	b, _ := server(t, func(w http.ResponseWriter, r *http.Request) { other.record(r); ok(w, r) })
	a, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/same":
			http.Redirect(w, r, "/landed", http.StatusFound)
		case "/landed":
			landed.record(r)
			ok(w, r)
		default:
			http.Redirect(w, r, urlOn(b, otherName, "/"), http.StatusFound)
		}
	})
	r := newRegistry(t, newClock(), byName)
	c := r.Client(batcave, providerhttp.Policy{Limits: limits(100, 100), Redact: []string{"x-batcave-pin"}})

	stock := c.Client()
	follows := *c.Client()
	follows.CheckRedirect = follow // an SDK with its own policy keeps the control too
	for name, cl := range map[string]*http.Client{"stock": stock, "copied": &follows} {
		req, _ := http.NewRequest(http.MethodGet, urlOn(a, publicName, "/away"), nil)
		withCredentials(req)
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		_ = resp.Body.Close()
		got, _, n := other.get()
		if n == 0 {
			t.Fatalf("%s: the redirect target was never reached; the test proves nothing", name)
		}
		for k := range credentialHeaders {
			if v := got.Get(k); v != "" {
				t.Errorf("%s: %s = %q reached another host", name, k, v)
			}
		}
		if got.Get("X-Trace") != "robin-1" {
			t.Errorf("%s: a harmless header was dropped too: %v", name, got)
		}
		other = headerLog{}
	}

	req, _ := http.NewRequest(http.MethodGet, urlOn(a, publicName, "/same"), nil)
	withCredentials(req)
	resp, err := stock.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	got, _, _ := landed.get()
	for _, k := range []string{"X-Api-Key", "X-Plex-Token", "X-Batcave-Pin", "X-Refresh-Token"} {
		if got.Get(k) != credentialHeaders[k] {
			t.Errorf("same-endpoint redirect lost %s: the credential must follow within the endpoint", k)
		}
	}
}

func prev(scheme string, srv *httptest.Server, host string) *http.Response {
	_, port, _ := net.SplitHostPort(hostOf(srv))
	u, _ := url.Parse(scheme + "://" + host + ":" + port + "/")
	return &http.Response{StatusCode: http.StatusFound, Request: &http.Request{URL: u}}
}

// TestRedirect_HTTPSToHTTPRefused (M1): a redirect from https to http is
// refused (the credentials of a request that left over TLS must not follow it
// onto plain text), unless the instance's trust covers that private origin; a
// plain-to-plain hop is not touched.
func TestRedirect_HTTPSToHTTPRefused(t *testing.T) {
	srv, hits := server(t, ok)
	r := newRegistry(t, newClock(), byName)
	public := r.Client(batcave, providerhttp.Policy{Limits: limits(100, 100)})
	roundTrip := func(c *providerhttp.Client, from *http.Response, to string) error {
		req, _ := http.NewRequest(http.MethodGet, to, nil)
		req.Response = from
		resp, err := c.Client().Transport.RoundTrip(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}
	err := roundTrip(public, prev("https", srv, publicName), urlOn(srv, publicName, "/x"))
	if !errors.Is(err, providerhttp.ErrInsecureRedirect) || !refusedFor(err, providerhttp.ReasonEgress) {
		t.Fatalf("https to http on the same host: %v, want ErrInsecureRedirect (egress)", err)
	}
	if hits.Load() != 0 {
		t.Fatal("the plain-text hop was sent")
	}
	if err := roundTrip(public, prev("http", srv, publicName), urlOn(srv, publicName, "/x")); err != nil {
		t.Fatalf("http to http: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want the plain hop sent", hits.Load())
	}

	// A bound private origin may leave https for its own plain address.
	rl := newRegistry(t, newClock(), noLAN)
	trusted := rl.Client(batcave, providerhttp.Policy{Limits: limits(100, 100),
		Trust: integrations.Trust{PrivateOrigins: []string{"http://" + hostOf(srv)}}})
	if err := roundTrip(trusted, prev("https", srv, "127.0.0.1"), srv.URL+"/x"); err != nil {
		t.Fatalf("a trusted private origin: %v", err)
	}
	// ... but never for EgressPublicOnly, whose private reach is nil.
	only := rl.Client(ref("alfred"), providerhttp.Policy{Limits: limits(100, 100), Egress: integrations.EgressPublicOnly,
		Trust: integrations.Trust{PrivateOrigins: []string{"http://" + hostOf(srv)}}})
	if err := roundTrip(only, prev("https", srv, "127.0.0.1"), srv.URL+"/x"); !errors.Is(err, providerhttp.ErrInsecureRedirect) {
		t.Fatalf("public-only instance: %v, want the downgrade refused", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2 (plain hop + trusted hop)", hits.Load())
	}
}

// TestQueryCredential_NeverInCallerURL (M3): the credential is added by the
// transport to its own clone, so the request a caller holds, Response.Request
// and every error the raw Client() returns are free of it, and it is dropped
// from a redirect hop to another endpoint.
func TestQueryCredential_NeverInCallerURL(t *testing.T) {
	const secret = "riddler-secret-777"
	var seen, other headerLog
	b, _ := server(t, func(w http.ResponseWriter, r *http.Request) { other.record(r); ok(w, r) })
	a, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/away" {
			http.Redirect(w, r, urlOn(b, otherName, "/?q=hop"), http.StatusFound)
			return
		}
		seen.record(r)
		ok(w, r)
	})
	reads := atomic.Int64{}
	r := newRegistry(t, newClock(), byName)
	h, _ := r.Outbound(integrations.OutboundSpec{Ref: batcave, Limits: limits(100, 100),
		QueryCredential: &integrations.QueryCredential{Name: "gotham_pin", Value: func(context.Context) (string, error) {
			reads.Add(1)
			return secret, nil
		}}})

	req, _ := http.NewRequest(http.MethodGet, urlOn(a, publicName, "/lookup?q=batman&gotham_pin=stale"), nil)
	resp, err := h.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	_, q, _ := seen.get()
	if q.Get("gotham_pin") != secret || q.Get("q") != "batman" || len(q["gotham_pin"]) != 1 {
		t.Fatalf("the provider saw %v, want the credential once (a caller's stale value replaced) and q kept", q)
	}
	if strings.Contains(req.URL.String(), secret) || strings.Contains(resp.Request.URL.String(), secret) {
		t.Fatalf("the credential reached the caller's request: %v / %v", req.URL, resp.Request.URL)
	}

	// A hop to another endpoint gets no credential.
	resp, err = h.Client().Get(urlOn(a, publicName, "/away"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	_, q, n := other.get()
	if n == 0 || q.Get("gotham_pin") != "" || q.Get("q") != "hop" {
		t.Fatalf("redirect target saw %v (n=%d): the credential must not follow to another endpoint", q, n)
	}

	// Errors of the raw client: an unreachable server and an egress refusal.
	for name, u := range map[string]string{
		"connection refused": "http://127.0.0.1:1/lookup?q=x",
		"egress refusal":     "http://169.254.169.254/latest?q=x",
	} {
		req, _ := http.NewRequestWithContext(providerhttp.WithBypassHold(context.Background()), http.MethodGet, u, nil)
		_, err := h.Client().Do(req)
		if err == nil {
			t.Fatalf("%s: sent", name)
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(fmt.Sprintf("%#v", err), secret) {
			t.Errorf("%s: the raw client's error carries the credential: %v", name, err)
		}
	}

	// The credential's name is always redacted from a URL a caller built.
	req, _ = http.NewRequestWithContext(providerhttp.WithBypassHold(context.Background()), http.MethodGet, "http://169.254.169.254/x?gotham_pin="+secret, nil)
	if _, err := h.Do(req); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("Do error = %v, want the caller's own gotham_pin value redacted", err)
	}
}

// TestQueryCredential_UnreadableRefusesBeforeSending: a credential that cannot
// be read refuses the request locally; the reader's error is not carried.
func TestQueryCredential_UnreadableRefusesBeforeSending(t *testing.T) {
	srv, hits := server(t, ok)
	r := newRegistry(t, newClock())
	for name, val := range map[string]func(context.Context) (string, error){
		"error": func(context.Context) (string, error) { return "", errors.New("vault says riddler-9") },
		"empty": func(context.Context) (string, error) { return "", nil },
	} {
		h, _ := r.Outbound(integrations.OutboundSpec{Ref: ref(name), Limits: limits(100, 100),
			QueryCredential: &integrations.QueryCredential{Name: "gotham_pin", Value: val}})
		_, err := h.Client().Get(srv.URL)
		if !errors.Is(err, providerhttp.ErrCredentialUnavailable) ||
			integrations.ClassOf(context.Background(), err) != integrations.ClassRefusedLocal || strings.Contains(err.Error(), "riddler") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatal("a request without its credential was sent")
	}
}

// TestBaseTransport_CannotDisableVerification (NIT): a Base that skips TLS
// verification does not make the Registry's transports skip it.
func TestBaseTransport_CannotDisableVerification(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(ok)) // a certificate no root vouches for
	t.Cleanup(srv.Close)
	r := newRegistry(t, newClock(), func(o *providerhttp.Options) {
		o.Base = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // the test proves the Registry neutralises this
	})
	c := r.Client(batcave, providerhttp.Policy{Limits: limits(100, 100),
		Trust: integrations.Trust{PrivateOrigins: []string{"https://" + hostOf(srv)}}})
	_, err := get(t, c, srv.URL)
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("err = %v, want a certificate verification failure", err)
	}
}

// TestDialRefusal_GivesBackWhatItTook (N1, N2): a request refused at the dial
// spent no quota and no bucket token; a request that was sent and then failed
// did.
func TestDialRefusal_GivesBackWhatItTook(t *testing.T) {
	clk := newClock()
	r := newRegistry(t, clk, noLAN)
	srv, _ := server(t, ok)
	lim := limits(1, 1)
	lim.Daily = daily(1, 1)
	c := r.Client(batcave, providerhttp.Policy{Limits: lim})
	for i := range 3 { // each refused dial must leave the single token and the single unit
		if _, err := get(t, c, srv.URL); !blockedBy(err) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	st := c.State()
	if st.QuotaUsed["requests"] != 0 || st.BucketTokens < 0.99 {
		t.Fatalf("after three refusals: quota %v, bucket %v; want nothing spent", st.QuotaUsed, st.BucketTokens)
	}
	if len(clk.Sleeps()) != 0 {
		t.Fatalf("waited %v: a refusal used the pace", clk.Sleeps())
	}
	// A request that is sent and then fails spends both.
	hang, _ := server(t, func(w http.ResponseWriter, _ *http.Request) {
		conn, _, _ := w.(http.Hijacker).Hijack()
		_ = conn.(*net.TCPConn).SetLinger(0)
		_ = conn.Close()
	})
	rl := newRegistry(t, clk)
	c2 := rl.Client(ref("alfred"), providerhttp.Policy{Limits: lim, Trust: lanTrust})
	if _, err := get(t, c2, hang.URL); err == nil {
		t.Fatal("a closed connection must fail")
	}
	st = c2.State()
	if st.QuotaUsed["requests"] != 1 {
		t.Fatalf("a request that was sent and failed: quota %v, want 1", st.QuotaUsed)
	}
	// A connect failure sent nothing: refunded too.
	c3 := rl.Client(ref("barbara"), providerhttp.Policy{Limits: lim, Trust: lanTrust})
	if _, err := get(t, c3, "http://127.0.0.1:1/"); err == nil {
		t.Fatal("port 1 must refuse")
	}
	if got := c3.State().QuotaUsed["requests"]; got != 0 {
		t.Fatalf("a connect failure spent quota %d", got)
	}
}

// TestRetry_DialRefusalAfterFailureStillTeachesTheGate (N3): an attempt that
// answered 503 and then met an egress refusal on its retry leaves the breaker
// and the hold with the 503, not with a refusal that counts for nothing.
func TestRetry_DialRefusalAfterFailureStillTeachesTheGate(t *testing.T) {
	var calls atomic.Int64
	srv, _ := server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Connection", "close") // the retry must dial again
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	rec := &dialRecorder{real: hostOf(srv), answers: func(int, string) []net.IP {
		if calls.Add(1) == 1 {
			return ips("192.0.2.10")
		}
		return ips("10.0.0.5") // the name now resolves into the private network
	}}
	r := newRegistry(t, newClock(), rec.options)
	c := r.Client(batcave, providerhttp.Policy{Limits: retryLimits(3)})
	if _, err := get(t, c, nameURL(srv, "/")); !blockedBy(err) {
		t.Fatalf("err = %v, want the egress refusal of the retry", err)
	}
	h, held := r.HoldFor(batcave)
	if !held || h.Class != integrations.ClassTransient {
		t.Fatalf("hold = %+v, %v; want the 503's transient hold", h, held)
	}
}

// TestRetry_NonIdempotentOnlyWhenNothingWasSent (N9): a method that is not
// idempotent is resent only when nothing was written, or the server itself says
// it did not process the request (429, 503 with Retry-After).
func TestRetry_NonIdempotentOnlyWhenNothingWasSent(t *testing.T) {
	type tc struct {
		name, method string
		status       int
		reqHeader    map[string]string
		respHeader   map[string]string
		reset        bool // answer by resetting the connection after reading the request
		hits         int64
	}
	cases := []tc{
		{name: "GET 502", method: "GET", status: 502, hits: 4},
		{name: "PUT 502", method: "PUT", status: 502, hits: 4},
		{name: "DELETE 502", method: "DELETE", status: 502, hits: 4},
		{name: "POST 502", method: "POST", status: 502, hits: 1},
		{name: "POST 500", method: "POST", status: 500, hits: 1},
		{name: "POST 503 without Retry-After", method: "POST", status: 503, hits: 1},
		{name: "POST 503 with Retry-After", method: "POST", status: 503, respHeader: map[string]string{"Retry-After": "1"}, hits: 4},
		{name: "POST 429", method: "POST", status: 429, hits: 4},
		{name: "POST 502 with Idempotency-Key", method: "POST", status: 502, reqHeader: map[string]string{"Idempotency-Key": "k"}, hits: 4},
		{name: "GET reset", method: "GET", reset: true, hits: 4},
		{name: "POST reset", method: "POST", reset: true, hits: 1},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, hits := server(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = r.Body.Read(make([]byte, 16))
				if tc.reset {
					conn, _, _ := w.(http.Hijacker).Hijack()
					_ = conn.(*net.TCPConn).SetLinger(0) // close sends a RST
					_ = conn.Close()
					return
				}
				for k, v := range tc.respHeader {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
			})
			r := newRegistry(t, newClock())
			c := r.Client(ref(fmt.Sprintf("robin%d", i)), providerhttp.Policy{Limits: retryLimits(3), Trust: lanTrust})
			req, _ := http.NewRequest(tc.method, srv.URL, strings.NewReader("batarang"))
			for k, v := range tc.reqHeader {
				req.Header.Set(k, v)
			}
			resp, err := c.Do(req)
			if err == nil {
				_ = resp.Body.Close()
			}
			if hits.Load() != tc.hits {
				t.Fatalf("hits = %d, want %d (err %v)", hits.Load(), tc.hits, err)
			}
		})
	}

	// A request that wrote nothing (the dial failed) is resent whatever its
	// method.
	srv, hits := server(t, ok)
	var dials atomic.Int64
	r := newRegistry(t, newClock(), func(o *providerhttp.Options) {
		o.DialAddr = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if dials.Add(1) <= 2 {
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
			}
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}
	})
	c := r.Client(ref("selina"), providerhttp.Policy{Limits: retryLimits(3), Trust: lanTrust})
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("batarang"))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("POST over a failing dial: %v", err)
	}
	_ = resp.Body.Close()
	if dials.Load() != 3 || hits.Load() != 1 {
		t.Fatalf("dials %d hits %d, want 3 dials and one delivery", dials.Load(), hits.Load())
	}
}

// TestBucket_TinyRateIsClamped (NIT): a rate far below any real limit waits a
// bounded time instead of overflowing into a negative delay.
func TestBucket_TinyRateIsClamped(t *testing.T) {
	clk := newClock()
	r := newRegistry(t, clk)
	srv, _ := server(t, ok)
	c := r.Client(batcave, providerhttp.Policy{Limits: integrations.Limits{RPS: 1e-300, Burst: 1, MaxRetries: -1, Timeout: time.Second}, Trust: lanTrust})
	for range 3 {
		if _, err := get(t, c, srv.URL); err != nil {
			t.Fatal(err)
		}
	}
	sleeps := clk.Sleeps()
	if len(sleeps) != 2 {
		t.Fatalf("sleeps = %v, want two waits", sleeps)
	}
	for i, d := range sleeps {
		if d <= 0 || d > 24*time.Hour*time.Duration(i+1) {
			t.Errorf("wait %d = %v, want a positive wait clamped to the day-per-request floor", i, d)
		}
	}
}
