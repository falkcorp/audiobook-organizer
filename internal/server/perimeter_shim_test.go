// file: internal/server/perimeter_shim_test.go
// version: 1.0.3
// guid: 9e2c5a17-6f4b-4d83-b1a0-3c7e8f2d4b56
// last-edited: 2026-11-08

package server

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// perimeterStacks are the two listener handlers for one engine: HEAD's, the
// peer allow-list gate over the gin engine, and R1's, the gate over the
// perimeter shim over the kernel's router with books' legacy mount (what
// listenR1 serves).
func perimeterStacks(t *testing.T, f *credGuardFixture, altSvc string) (head, r1 http.Handler) {
	t.Helper()
	_, mounted := booksMount(t, f)
	return newPeerAllowListHandler(f.srv.router), newPeerAllowListHandler(f.srv.perimeterShim(mounted, altSvc))
}

// perimeterCase is one request of the parity table.
type perimeterCase struct {
	name, method, path string
	header             map[string]string
	remote             string // RemoteAddr; "" = a loopback peer
	// coreBody: the kernel writes this answer itself with core's own body
	// (a problem 404 under core's /api/core/ group), so only the status and
	// the perimeter's headers are compared.
	coreBody bool
}

// volatileHeaders differ between two runs of the same request.
func volatileHeaders() []string { return []string{"Date", "X-Request-Id", "Content-Length"} }

// perimeterParity sends c through both stacks and compares the status, every
// header but the volatile ones, and the body.
func perimeterParity(t *testing.T, head, r1 http.Handler, c perimeterCase) {
	t.Helper()
	do := func(h http.Handler) *httptest.ResponseRecorder {
		req := httptest.NewRequest(c.method, c.path, nil)
		req.Host = "gotham.example"
		req.RemoteAddr = "127.0.0.1:41000"
		if c.remote != "" {
			req.RemoteAddr = c.remote
		}
		for k, v := range c.header {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	g, k := do(head), do(r1)
	assert.Equal(t, g.Code, k.Code, "%s: status", c.name)
	gh, kh := g.Header().Clone(), k.Header().Clone()
	for _, v := range volatileHeaders() {
		gh.Del(v)
		kh.Del(v)
	}
	if c.coreBody {
		// Core's problem body brings its own Content-Type.
		gh.Del("Content-Type")
		kh.Del("Content-Type")
	} else {
		assert.Equal(t, readableBody(g.Header(), g.Body.Bytes()), readableBody(k.Header(), k.Body.Bytes()), "%s: body", c.name)
	}
	if !maps.EqualFunc(gh, kh, slices.Equal) {
		t.Errorf("%s: headers differ\nHEAD: %v\nR1:   %v", c.name, gh, kh)
	}
}

// readableBody is a response body as the parity check compares and prints
// it. A gzip-encoded body is decompressed, so the check compares what a
// client reads (Content-Encoding and Vary are compared with the headers) and
// a failure shows text rather than raw compressed bytes: binary in go test's
// output breaks the CI shard runner's UTF-8 decode of a failing package. A
// body that is not a readable gzip stream, or is not printable text, is
// shown as its length and SHA-256 instead.
func readableBody(h http.Header, b []byte) string {
	opaque := func(what string, b []byte) string {
		return fmt.Sprintf("<%s: %d bytes, sha256 %x>", what, len(b), sha256.Sum256(b))
	}
	prefix := ""
	if h.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return opaque("unreadable gzip ("+err.Error()+")", b)
		}
		plain, err := io.ReadAll(zr)
		if err != nil {
			return opaque("unreadable gzip ("+err.Error()+")", b)
		}
		b, prefix = plain, "gzip: "
	}
	if !utf8.Valid(b) || strings.ContainsFunc(string(b), func(r rune) bool {
		return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t'
	}) {
		return opaque(prefix+"binary", b)
	}
	return prefix + string(b)
}

// perimeterCases cover each kind of answer: an engine route, the SPA, the
// legacy 404 (gin's NoRoute and the kernel's post-match one), the /api
// redirect and an unmatched method (both written by the kernel), core's own
// group, health, the Engine.IO probe and CORS preflights.
func perimeterCases() []perimeterCase {
	same := map[string]string{"Origin": "https://gotham.example"}
	foreign := map[string]string{"Origin": "https://arkham.example"}
	preflight := map[string]string{"Origin": "https://gotham.example", "Access-Control-Request-Method": "PUT"}
	return []perimeterCase{
		{name: "API route, public", method: http.MethodGet, path: "/api/v1/health"},
		{name: "API route, signed out", method: http.MethodGet, path: "/api/v1/audiobooks"},
		{name: "API route, same origin", method: http.MethodGet, path: "/api/v1/health", header: same},
		{name: "API route, foreign origin", method: http.MethodGet, path: "/api/v1/health", header: foreign},
		{name: "SPA page", method: http.MethodGet, path: "/library/riddler"},
		{name: "SPA shared segment", method: http.MethodGet, path: "/login"},
		{name: "legacy 404 under /api/v1", method: http.MethodGet, path: "/api/v1/riddler-unrouted"},
		{name: "legacy 404 under /api/v1, same origin", method: http.MethodGet, path: "/api/v1/riddler-unrouted", header: same},
		{name: "legacy 404 under a top segment", method: http.MethodGet, path: "/ping/riddler"},
		{name: "unmatched method", method: http.MethodDelete, path: "/api/v1/health"},
		{name: "unmatched method, same origin", method: http.MethodDelete, path: "/api/v1/health", header: same},
		{name: "/api redirect", method: http.MethodGet, path: "/api/audiobooks?batman=1"},
		{name: "/api redirect, same origin", method: http.MethodGet, path: "/api/audiobooks", header: same},
		{name: "core group", method: http.MethodGet, path: "/api/core/v1/riddler", coreBody: true},
		{name: "core group, same origin", method: http.MethodGet, path: "/api/core/v1/riddler", header: same, coreBody: true},
		{name: "health", method: http.MethodGet, path: "/api/health"},
		{name: "Engine.IO probe", method: http.MethodGet, path: "/socket.io/?EIO=4&transport=polling"},
		{name: "CORS preflight, same origin", method: http.MethodOptions, path: "/api/v1/audiobooks", header: preflight},
		{name: "CORS preflight, foreign origin", method: http.MethodOptions, path: "/api/v1/audiobooks",
			header: map[string]string{"Origin": "https://arkham.example", "Access-Control-Request-Method": "PUT"}},
		{name: "CORS preflight, unrouted path", method: http.MethodOptions, path: "/api/v1/riddler-unrouted", header: preflight},
		{name: "CORS preflight, core group", method: http.MethodOptions, path: "/api/core/v1/riddler", header: preflight},
	}
}

// withHeader is cs with k: v added to every request, prefix on every name.
func withHeader(cs []perimeterCase, prefix, k, v string) []perimeterCase {
	out := make([]perimeterCase, len(cs))
	for i, c := range cs {
		h := maps.Clone(c.header)
		if h == nil {
			h = map[string]string{}
		}
		h[k] = v
		c.header, c.name = h, prefix+c.name
		out[i] = c
	}
	return out
}

// basicCredentials is the Authorization value for the gate's test account.
func basicCredentials() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("alfred:synthetic-wayne-manor-gate"))
}

// TestPerimeterShim_ParityWithHEAD pins what the perimeter shim must keep
// until R1-17 builds core's perimeter: for every kind of answer, the R1
// listener stack answers as HEAD's did, with the same security headers,
// CORS headers and preflight answers, the Basic gate's refusals and
// exemptions, and the peer allow-list's allow and deny. Core's own group is
// the one body difference (a problem 404 where HEAD's NoRoute wrote its
// legacy 404); its status and perimeter headers still match.
func TestPerimeterShim_ParityWithHEAD(t *testing.T) {
	f := legacyEngine(t, false, false)
	head, r1 := perimeterStacks(t, f, "")

	t.Run("open", func(t *testing.T) {
		for _, c := range perimeterCases() {
			perimeterParity(t, head, r1, c)
		}
	})

	// HEAD's compressionMiddleware ran on every answer gin wrote, the
	// unrouted ones included: the /api redirect's 301 went out gzipped with
	// Vary: Accept-Encoding, and an error status lost every Vary, CORS's
	// Origin included (review m5).
	t.Run("gzip", func(t *testing.T) {
		for _, c := range withHeader(perimeterCases(), "gzip: ", "Accept-Encoding", "gzip") {
			perimeterParity(t, head, r1, c)
		}
	})

	t.Run("basic gate", func(t *testing.T) {
		orig := config.AppConfig
		t.Cleanup(func() { config.AppConfig = orig })
		config.AppConfig.BasicAuthEnabled = true
		config.AppConfig.BasicAuthUsername = "alfred"
		config.AppConfig.BasicAuthPassword = "synthetic-wayne-manor-gate"
		for _, c := range perimeterCases() {
			c.name = "gated: " + c.name
			perimeterParity(t, head, r1, c)
		}
		authed := withHeader(perimeterCases(), "gated, signed in: ", "Authorization", basicCredentials())
		for _, c := range authed {
			perimeterParity(t, head, r1, c)
		}
		for _, c := range withHeader(authed, "gzip: ", "Accept-Encoding", "gzip") {
			perimeterParity(t, head, r1, c)
		}
		// Valid credentials pass the gate on the kernel's own answers too.
		for p, want := range map[string]int{"/api/core/v1/riddler": http.StatusNotFound,
			"/api/audiobooks": http.StatusMovedPermanently, "/api/v1/riddler-unrouted": http.StatusNotFound} {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, p, nil)
			req.Header.Set("Authorization", basicCredentials())
			r1.ServeHTTP(w, req)
			assert.Equal(t, want, w.Code, "signed-in GET %s", p)
		}
		// The gate refuses the kernel's own answers as HEAD refused gin's
		// unrouted ones, and keeps the health exemption.
		for _, p := range []string{"/api/core/v1/riddler", "/api/audiobooks", "/api/v1/riddler-unrouted"} {
			w := httptest.NewRecorder()
			r1.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
			assert.Equal(t, http.StatusUnauthorized, w.Code, "gated GET %s", p)
			assert.NotEmpty(t, w.Header().Get("WWW-Authenticate"), "gated GET %s", p)
			assert.Empty(t, w.Body.String(), "gated GET %s: the refusal carries core's body", p)
		}
		w := httptest.NewRecorder()
		r1.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
		assert.Equal(t, http.StatusOK, w.Code, "the gate refused the health exemption")
	})

	t.Run("peer gate", func(t *testing.T) {
		orig := config.AppConfig
		t.Cleanup(func() { config.AppConfig = orig })
		config.AppConfig.AllowedPeerCIDRs = "192.0.2.0/28"
		for _, remote := range []string{"192.0.2.5:41000", "198.51.100.7:41000"} {
			for _, c := range perimeterCases() {
				c.name = "peer " + remote + ": " + c.name
				c.remote = remote
				perimeterParity(t, head, r1, c)
			}
		}
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/core/v1/riddler", nil)
		req.RemoteAddr = "198.51.100.7:41000"
		r1.ServeHTTP(w, req)
		assert.Equal(t, http.StatusForbidden, w.Code, "the peer gate let a denied peer reach the kernel")
	})
}

// TestPerimeterShim_AltSvcOnUnroutedAnswers: with HTTP/3 served, HEAD added
// Alt-Svc through a middleware registered on the engine at listen time,
// which gin applies to the NoRoute chain only. The shim adds the same value
// to the kernel's own answers, except the /api redirect's 301, which HEAD's
// redirect middleware wrote before Alt-Svc ran.
func TestPerimeterShim_AltSvcOnUnroutedAnswers(t *testing.T) {
	const altSvc = `h3=":8443"; ma=2592000`
	f := legacyEngine(t, false, false)
	head, r1 := perimeterStacks(t, f, altSvc)
	f.srv.router.Use(func(c *gin.Context) {
		c.Header("Alt-Svc", altSvc)
		c.Next()
	})
	for _, c := range perimeterCases() {
		perimeterParity(t, head, r1, c)
	}
	for _, c := range withHeader(perimeterCases(), "gzip: ", "Accept-Encoding", "gzip") {
		perimeterParity(t, head, r1, c)
	}
	w := httptest.NewRecorder()
	r1.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/core/v1/riddler", nil))
	assert.Equal(t, altSvc, w.Header().Get("Alt-Svc"), "a kernel-written 404 lacks Alt-Svc")
}

// TestPerimeterShim_UnmatchedMethodMatchesGin is
// TestLegacyMount_UnmatchedMethodMatchesGin through the shim: every method a
// manifest path does not route, OPTIONS included (the shim sends it to the
// engine's CORS middleware, as HEAD did), answers with HEAD's status,
// headers and body.
func TestPerimeterShim_UnmatchedMethodMatchesGin(t *testing.T) {
	for _, abs := range absModes() {
		t.Run(absName(abs), func(t *testing.T) {
			f := legacyEngine(t, abs, false)
			head, r1 := perimeterStacks(t, f, "")
			manifest, err := f.srv.LegacyManifest()
			if err != nil {
				t.Fatal(err)
			}
			routed := map[string]bool{}
			var paths []string
			for _, lr := range manifest {
				p := legacyConcretePath(lr.GinPath)
				if !slices.Contains(paths, p) {
					paths = append(paths, p)
				}
				routed[lr.Method+" "+p] = true
			}
			methods := []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions}
			n := 0
			for _, p := range paths {
				for _, m := range methods {
					if routed[m+" "+p] {
						continue
					}
					n++
					c := perimeterCase{name: m + " " + p, method: m, path: p}
					if strings.HasPrefix(p, "/api/core/") {
						c.coreBody = true
					}
					perimeterParity(t, head, r1, c)
				}
			}
			assert.Greater(t, n, 1000, "too few unrouted method/path pairs; the scan is broken")
		})
	}
}

// accessLineVolatile matches the two fields of gin's access line that differ
// between two runs of the same request: the timestamp and the latency.
var accessLineVolatile = regexp.MustCompile(`^\[GIN\] \d{4}/\d\d/\d\d - \d\d:\d\d:\d\d \|(.*?)\|\s*\S+\s*\|`)

// TestPerimeterShim_AccessLogLikeHEAD (R1-15 review m6): HEAD's gin logger,
// the engine's second middleware, wrote an access line for every request,
// the unrouted ones included. Behind the kernel, an answer core writes itself
// never reaches it, so the shim writes the same line: same status, client,
// method and redacted path, the Basic refusal included. A request gin serves
// is logged once, by gin.
func TestPerimeterShim_AccessLogLikeHEAD(t *testing.T) {
	var log bytes.Buffer
	prev := gin.DefaultWriter
	gin.DefaultWriter = &log // before the engine and the shim read it
	t.Cleanup(func() { gin.DefaultWriter = prev })
	f := legacyEngine(t, false, false)
	head, r1 := perimeterStacks(t, f, "")

	lines := func(h http.Handler, method, path string, header map[string]string) []string {
		log.Reset()
		req := httptest.NewRequest(method, path, nil)
		req.RemoteAddr = "192.0.2.9:41000"
		for k, v := range header {
			req.Header.Set(k, v)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
		var out []string
		for _, l := range strings.Split(strings.TrimRight(log.String(), "\n"), "\n") {
			if strings.HasPrefix(l, "[GIN] ") {
				out = append(out, accessLineVolatile.ReplaceAllString(l, "[GIN] <time> |$1| <latency> |"))
			}
		}
		return out
	}
	check := func(name, method, path string, header map[string]string) {
		t.Helper()
		h, k := lines(head, method, path, header), lines(r1, method, path, header)
		if len(h) != 1 {
			t.Fatalf("%s: HEAD wrote %d access lines: %q", name, len(h), h)
		}
		assert.Equal(t, h, k, "%s: access line", name)
	}
	cases := []struct{ name, method, path string }{
		{"/api redirect, token masked", http.MethodGet, "/api/audiobooks?token=synthetic-joker-card"},
		{"legacy 404", http.MethodGet, "/api/v1/riddler-unrouted"},
		{"core group", http.MethodGet, "/api/core/v1/riddler"},
		{"unmatched method", http.MethodDelete, "/api/v1/health"},
		{"routed, logged by gin only", http.MethodGet, "/api/v1/health"},
	}
	for _, c := range cases {
		check(c.name, c.method, c.path, nil)
	}
	if got := lines(r1, http.MethodGet, "/api/audiobooks?token=synthetic-joker-card", nil); len(got) != 1 || strings.Contains(got[0], "synthetic-joker-card") {
		t.Errorf("the access line must mask the token: %q", got)
	}

	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.BasicAuthEnabled = true
	config.AppConfig.BasicAuthUsername = "alfred"
	config.AppConfig.BasicAuthPassword = "synthetic-wayne-manor-gate"
	for _, c := range cases {
		check("gated: "+c.name, c.method, c.path, nil)
		check("gated, signed in: "+c.name, c.method, c.path, map[string]string{"Authorization": basicCredentials()})
	}
}
