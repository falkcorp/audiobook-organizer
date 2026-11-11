// file: core/integrations/providerhttp/review_edges_internal_test.go
// version: 1.0.0
// guid: 074b0fd6-cb7c-47f2-ac1e-898cda0f5f32
// last-edited: 2026-11-11

package providerhttp

import (
	"context"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/integrations"
)

// TestScrubEdges_Rules: the edges of the name and value rules that a mutation
// sweep of redact.go found unpinned.
func TestScrubEdges_Rules(t *testing.T) {
	const s = "SECRETvalue"
	sc := scrubber{}
	t.Run("pwd ends a name", func(t *testing.T) {
		for _, n := range []string{"dbpwd", "x-db-pwd", "DB_PWD"} {
			if !sc.secret(n) {
				t.Errorf("secret(%q) = false", n)
			}
		}
	})
	t.Run("a name judged whole or by its tail", func(t *testing.T) {
		// Up to the window a name is judged whole: the sig prefix applies.
		if !sc.secret("sig" + strings.Repeat("a", 100)) {
			t.Error("a 103-byte name starting with sig is judged whole")
		}
		// Past the window only the tail is seen: neither the prefix nor an
		// exact name can be judged, however the padding collapses.
		if sc.secret("sig" + strings.Repeat("a", 300)) {
			t.Error("a cut name must not match a prefix rule on its tail")
		}
		if sc.secret(strings.Repeat("-", 200) + "pass") {
			t.Error("a cut name must not match an exact name on its tail")
		}
		if !sc.secret(strings.Repeat("-", 200) + "token") {
			t.Error("a cut name is still judged by its suffix")
		}
		if !sc.secret(strings.Repeat("a", 200) + "session" + strings.Repeat("a", 5)) {
			t.Error("a cut name is still judged by what its tail contains")
		}
	})
	t.Run("a bearer token needs no secret name", func(t *testing.T) {
		got := sc.text("the proxy said Bearer " + s + " is not valid")
		if got != "the proxy said Bearer REDACTED is not valid" {
			t.Errorf("got %q", got)
		}
		if got := sc.text("bearer"); got != "bearer" {
			t.Errorf("a lone word changed: %q", got)
		}
		if got := sc.text("Bearer "); got != "Bearer " {
			t.Errorf("no token, nothing to redact: %q", got)
		}
	})
	t.Run("a scheme and a token under any secret name", func(t *testing.T) {
		for in, want := range map[string]string{
			"token: Basic " + s + " ok":   "token: REDACTED ok",
			"token: Bearer " + s + " ok":  "token: REDACTED ok",
			"key=bearer   " + s + "&a=1":  "key=REDACTED&a=1",
			"token: Basicfoo " + s + " x": "token: REDACTED " + s + " x", // not a scheme word: the value is one word
		} {
			if got := sc.text(in); got != want {
				t.Errorf("text(%q) = %q, want %q", in, got, want)
			}
		}
	})
	t.Run("bot token shape", func(t *testing.T) {
		const tail = "AAEhBOweik6ad9r_QXMENQjcrTu" // 27
		for in, want := range map[string]string{
			"https://h.test/bot12345:" + tail + "/x":       "https://h.test/bot12345:REDACTED/x", // five digits
			"https://h.test/bot1234:" + tail + "/x":        "https://h.test/bot1234:" + tail + "/x",
			"https://h.test/bot:" + tail + "/x":            "https://h.test/bot:" + tail + "/x",
			"https://h.test/bot123456:" + tail[:16] + "/x": "https://h.test/bot123456:REDACTED/x", // sixteen
			"https://h.test/bot123456:" + tail[:15] + "/x": "https://h.test/bot123456:" + tail[:15] + "/x",
			"https://h.test/bot123456:" + tail + "!/x":     "https://h.test/bot123456:" + tail + "!/x",
			"https://h.test/bot12x456:" + tail + "/x":      "https://h.test/bot12x456:" + tail + "/x",
		} {
			if got := sc.url(in); got != want {
				t.Errorf("url(%q) = %q, want %q", in, got, want)
			}
		}
	})
	t.Run("unparseable urls print scheme and host at most", func(t *testing.T) {
		for in, want := range map[string]string{
			// the query ends the host even with no path in between
			"http://alfred:p[ss@h.test?key=" + s: "http://h.test",
			"http://alfred:p[ss@h.test#" + s:     "http://h.test",
			// no scheme
			"://alfred:p[ss@h.test/x":                           unparseableURL,
			strings.Repeat("a", 17) + "://alfred:p[ss@h.test/x": unparseableURL,
			strings.Repeat("a", 16) + "://alfred:p[ss@h.test/x": strings.Repeat("a", 16) + "://h.test",
		} {
			if got := sc.url(in); got != want {
				t.Errorf("url(%q) = %q, want %q", in, got, want)
			}
		}
	})
	t.Run("an encoded query name is judged decoded", func(t *testing.T) {
		got := sc.url("https://h.test/x?to%6Ben=" + s + "&q=1")
		if got != "https://h.test/x?to%6Ben=REDACTED&q=1" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("an error is scrubbed through its text", func(t *testing.T) {
		got := sc.err(&textErr{"upstream said token=" + s})
		if strings.Contains(got.Error(), s) {
			t.Errorf("err leaks %q", got)
		}
	})
	t.Run("the adapter's own names", func(t *testing.T) {
		x := newScrubber([]string{"Gotham-Pin"})
		if !x.secret("gotham_pin") || sc.secret("gotham_pin") {
			t.Error("an extra name is secret for the scrubber that has it only")
		}
		if got := x.text("pin=1&gotham_pin=" + s); strings.Contains(got, s) {
			t.Errorf("leaks %q", got)
		}
		y := x.withExtra("Riddler-Key-2")
		if !y.secret("gotham_pin") || !y.secret("riddlerkey2") {
			t.Error("withExtra must keep the names it extends")
		}
	})
}

type textErr struct{ s string }

func (e *textErr) Error() string { return e.s }

// TestCut_BacksOffTheRune: a cut inside a multi-byte rune drops the whole
// rune, never half of it.
func TestCut_BacksOffTheRune(t *testing.T) {
	if got := cut("aé", 2); got != "a" {
		t.Errorf("cut inside a rune = %q, want a", got)
	}
	if got := cut("aé", 3); got != "aé" {
		t.Errorf("cut on the boundary = %q", got)
	}
}

// TestAttemptTrace_EitherHookMeansSent (N1): a connection obtained, or a
// request written, each says the request may have reached the remote; neither
// says nothing was spent.
func TestAttemptTrace_EitherHookMeansSent(t *testing.T) {
	var none attemptTrace
	if none.sent() {
		t.Fatal("a fresh attempt has sent nothing")
	}
	var conn attemptTrace
	conn.client().GotConn(httptrace.GotConnInfo{})
	if !conn.sent() || !conn.gotConn.Load() || conn.wrote.Load() {
		t.Errorf("GotConn: sent=%v gotConn=%v wrote=%v", conn.sent(), conn.gotConn.Load(), conn.wrote.Load())
	}
	var wrote attemptTrace
	wrote.client().WroteRequest(httptrace.WroteRequestInfo{})
	if !wrote.sent() || wrote.gotConn.Load() || !wrote.wrote.Load() {
		t.Errorf("WroteRequest: sent=%v gotConn=%v wrote=%v", wrote.sent(), wrote.gotConn.Load(), wrote.wrote.Load())
	}
}

// TestCrossesEndpoint_WholeChain (M1): a hop is crossed when ANY request of its
// chain, back to the first, had another endpoint, or when a response names no
// request at all.
func TestCrossesEndpoint_WholeChain(t *testing.T) {
	mk := func(raw string, prev *http.Request) *http.Request {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		r := &http.Request{URL: u}
		if prev != nil {
			r.Response = &http.Response{Request: prev}
		}
		return r
	}
	a := mk("https://a.test/x", nil)
	b := mk("https://b.test/x", a)
	tr := &transport{}
	for name, tc := range map[string]struct {
		req  *http.Request
		want bool
	}{
		"the first request":                {a, false},
		"same origin":                      {mk("https://a.test/y", a), false},
		"default port is the same":         {mk("https://A.test:443/y", a), false},
		"another host":                     {b, true},
		"another port":                     {mk("https://a.test:8443/y", a), true},
		"A to B to B":                      {mk("https://b.test/y", b), true},
		"A to B to A":                      {mk("https://a.test/z", b), true},
		"A to A to B":                      {mk("https://b.test/z", mk("https://a.test/y", a)), true},
		"A to A to A":                      {mk("https://a.test/z", mk("https://a.test/y", a)), false},
		"a response that names no request": {&http.Request{URL: a.URL, Response: &http.Response{}}, true},
	} {
		if got := tr.crossesEndpoint(tc.req); got != tc.want {
			t.Errorf("%s: crossesEndpoint = %v, want %v", name, got, tc.want)
		}
	}
}

// TestAddQuery_EscapesAndReplaces (M3): the credential is one parameter whatever
// its bytes, and a same-named parameter already on the URL is replaced, however
// its name is spelled.
func TestAddQuery_EscapesAndReplaces(t *testing.T) {
	const val = "a&b=c d%26#?+/"
	u, _ := url.Parse("https://h.test/x?q=1&gotham_pin=forged&gotham%5Fpin=forged2&other=2")
	addQuery(u, "gotham_pin", val)
	q := u.Query()
	if got := q["gotham_pin"]; len(got) != 1 || got[0] != val {
		t.Errorf("gotham_pin = %q, want exactly [%q]", got, val)
	}
	if q.Get("q") != "1" || q.Get("other") != "2" || q.Has("b") || len(q) != 3 {
		t.Errorf("the other parameters changed: %v", q)
	}
	if strings.Contains(u.RawQuery, "forged") {
		t.Errorf("a forged value survived: %q", u.RawQuery)
	}
	e, _ := url.Parse("https://h.test/x")
	addQuery(e, "a b", "1")
	if e.RawQuery != "a+b=1" {
		t.Errorf("the name is escaped too: %q", e.RawQuery)
	}
}

// TestPrivateFor_OriginsAreCaseInsensitive (P4): a trusted origin matches the
// endpoint (always lower-case) however it was written, and nothing else.
func TestPrivateFor_OriginsAreCaseInsensitive(t *testing.T) {
	r, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	priv := r.privateFor(integrations.Trust{PrivateOrigins: []string{"http://Batcave.Example.Test:8080", "not an origin", "http://"}})
	for hp, want := range map[string]bool{
		"batcave.example.test:8080": true,
		"batcave.example.test:8081": false,
		"other.example.test:8080":   false,
		"":                          false,
	} {
		if got := priv(hp); got != want {
			t.Errorf("private(%q) = %v, want %v", hp, got, want)
		}
	}
}

// TestMemStore_EmptyScopeDeletes: the in-memory store keeps the contract the
// native one does (the shared suite cannot run on it from here).
func TestMemStore_EmptyScopeDeletes(t *testing.T) {
	ctx := context.Background()
	m := newMemStore()
	a := integrations.Ref{Kind: integrations.KindMetadata, Instance: "alfred"}
	if err := m.PutScope(ctx, a, "shared"); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.Scopes(ctx); got[a] != "shared" {
		t.Fatalf("Scopes = %v", got)
	}
	if err := m.PutScope(ctx, a, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.Scopes(ctx); len(got) != 0 {
		t.Fatalf("an empty scope must delete the record: %v", got)
	}
}
