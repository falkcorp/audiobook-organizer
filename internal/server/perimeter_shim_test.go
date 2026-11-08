// file: internal/server/perimeter_shim_test.go
// version: 1.1.3
// guid: 9e2c5a17-6f4b-4d83-b1a0-3c7e8f2d4b56
// last-edited: 2026-11-08

package server

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/mediacore/httpapi"
)

// perimeterStacks are the two listener handlers for one engine: HEAD's, the
// peer allow-list gate over HEAD's gin perimeter (headPerimeter, frozen in
// perimeter_stages_test.go) over the engine, and R1's, the perimeter shim
// over the kernel's router with books' legacy mount, core's stages 0-11
// included (what listenR1 serves). router is R1's, for SetPerimeter.
func perimeterStacks(t *testing.T, f *credGuardFixture, altSvc string) (router *httpapi.Router, head, r1 http.Handler) {
	t.Helper()
	router, mounted := booksMount(t, f)
	return router, newPeerAllowListHandler(headPerimeter(f.srv.router)), f.srv.perimeterShim(mounted, altSvc)
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
	// peerRefused: the peer is outside the allow-list; only the refusal is
	// compared.
	peerRefused bool
	// ginGateVary: gin's own Basic gate refused it with an allowed Origin and
	// Accept-Encoding: gzip. HEAD's 401 kept CORS's Vary: Origin (its gate
	// ran before gzip); stage 9 deletes it from R1's (deviation 16, until
	// F3 moves the gate into stage 8). The difference is asserted, so a fix
	// shows up here.
	ginGateVary bool
}

// volatileHeaders differ between two runs of the same request. X-App-Build
// is new in R1 (stage 5) and checked on its own.
func volatileHeaders() []string {
	return []string{"Date", "X-Request-Id", "Content-Length", "X-App-Build"}
}

// perimeterParity sends c through both stacks and compares the status, every
// header but the volatile ones, and the body.
func perimeterParity(t *testing.T, head, r1 http.Handler, c perimeterCase) {
	t.Helper()
	g, k := serveCase(head, c), serveCase(r1, c)
	assert.Equal(t, g.Code, k.Code, "%s: status", c.name)
	if c.peerRefused {
		// Stage 0's refusal is core's problem answer (05-http-api §4.2),
		// HEAD's was http.Error's text; both come before any other stage.
		assert.Equal(t, http.StatusForbidden, k.Code, "%s: status", c.name)
		assert.Contains(t, k.Body.String(), "peer_refused", "%s: body", c.name)
		return
	}
	assert.Equal(t, "dev", k.Header().Get("X-App-Build"), "%s: X-App-Build", c.name)
	gh, kh := g.Header().Clone(), k.Header().Clone()
	if strings.HasPrefix(c.path, "/api/core/") {
		// Core owns the path: core's default policy and no report URL
		// (05-http-api §4.2 stage 5), where HEAD's gin chain gave every
		// answer ABO's.
		assert.Equal(t, "default-src 'none'; frame-ancestors 'self'", kh.Get("Content-Security-Policy"), "%s: CSP", c.name)
		assert.Empty(t, kh.Get("Reporting-Endpoints"), "%s: Reporting-Endpoints", c.name)
		for _, h := range []http.Header{gh, kh} {
			h.Del("Content-Security-Policy")
			h.Del("Reporting-Endpoints")
		}
	}
	for _, v := range volatileHeaders() {
		gh.Del(v)
		kh.Del(v)
	}
	if c.ginGateVary {
		assert.Equal(t, []string{"Origin"}, gh.Values("Vary"), "%s: HEAD's Vary", c.name)
		assert.Empty(t, kh.Values("Vary"), "%s: R1's Vary (deviation 16 fixed? drop ginGateVary)", c.name)
		gh.Del("Vary")
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

// serveCase sends c's request through h.
func serveCase(h http.Handler, c perimeterCase) *httptest.ResponseRecorder {
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

// TestPerimeter_ParityWithHEAD diffs HEAD's gin perimeter against core's
// stages 0-11 and the shim (R1-17): for every kind of answer, the R1
// listener stack answers as HEAD's did, with the same security headers,
// CORS headers and preflight answers, the Basic gate's refusals and
// exemptions, and the peer allow-list's allow and deny. The differences the
// spec makes are asserted instead: core's own group gets core's CSP and
// body, stage 0's refusal is a problem, and every answer carries
// X-App-Build.
func TestPerimeter_ParityWithHEAD(t *testing.T) {
	f := legacyEngine(t, false, false)
	router, head, r1 := perimeterStacks(t, f, "")

	t.Run("open", func(t *testing.T) {
		for _, c := range perimeterCases() {
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
		// With Accept-Encoding: gzip, HEAD's refusal was the same as without
		// it: the gate ran after CORS and before gin-contrib/gzip, so its 401
		// had no Content-Encoding and no Vary: Accept-Encoding and kept
		// CORS's Vary: Origin. headPerimeter, which does not compress, is an
		// exact reference for a refused request, and only for one (an
		// answer the gate lets through would be compressed), so the cases
		// HEAD did not refuse are skipped. Stage 9 has run on core's answer
		// before the shim refuses it, so the shim must undo it (review m1,
		// m3).
		gated := append(withHeader(perimeterCases(), "gated, gzip: ", "Accept-Encoding", "gzip"),
			perimeterCase{name: "gated, gzip: API route, signed out, same origin", method: http.MethodGet,
				path: "/api/v1/audiobooks", ginGateVary: true,
				header: map[string]string{"Accept-Encoding": "gzip", "Origin": "https://gotham.example"}},
			perimeterCase{name: "gated, gzip: SPA page, same origin", method: http.MethodGet,
				path: "/library/riddler", ginGateVary: true,
				header: map[string]string{"Accept-Encoding": "gzip", "Origin": "https://gotham.example"}})
		refused := 0
		for _, c := range gated {
			if serveCase(head, c).Code != http.StatusUnauthorized {
				continue
			}
			refused++
			perimeterParity(t, head, r1, c)
		}
		assert.GreaterOrEqual(t, refused, 10, "too few refused gzip cases; the gate is not on")
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
		// What the kernel does at start step 7 with books' R1Values.
		require.NoError(t, router.SetPerimeter(httpapi.Options{
			PeerAllowlist: []httpapi.TrustedPeer{{Prefix: netip.MustParsePrefix("192.0.2.0/28")}},
		}))
		t.Cleanup(func() { _ = router.SetPerimeter(httpapi.Options{}) })
		for _, remote := range []string{"192.0.2.5:41000", "198.51.100.7:41000"} {
			for _, c := range perimeterCases() {
				c.name = "peer " + remote + ": " + c.name
				c.remote = remote
				c.peerRefused = remote == "198.51.100.7:41000"
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
	_, head, r1 := perimeterStacks(t, f, altSvc)
	f.srv.router.Use(func(c *gin.Context) {
		c.Header("Alt-Svc", altSvc)
		c.Next()
	})
	for _, c := range perimeterCases() {
		perimeterParity(t, head, r1, c)
	}
	w := httptest.NewRecorder()
	r1.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/core/v1/riddler", nil))
	assert.Equal(t, altSvc, w.Header().Get("Alt-Svc"), "a kernel-written 404 lacks Alt-Svc")
}

// TestPerimeterShim_GatesEveryCoreAnswer: with the Basic gate on, an
// answer core writes itself (gin never ran) is refused whatever its status,
// a native 200 included, so a core route added later cannot skip the gate;
// the stages' own refusals pass ungated, and a request gin served is left
// to gin's own gate.
func TestPerimeterShim_GatesEveryCoreAnswer(t *testing.T) {
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.BasicAuthEnabled = true
	config.AppConfig.BasicAuthUsername = "alfred"
	config.AppConfig.BasicAuthPassword = "synthetic-wayne-manor-gate"
	srv := &Server{}
	for _, c := range []struct {
		status int
		gin    bool
		want   int
	}{
		{http.StatusOK, false, http.StatusUnauthorized},
		{http.StatusCreated, false, http.StatusUnauthorized},
		{http.StatusNoContent, false, http.StatusUnauthorized},
		{http.StatusMovedPermanently, false, http.StatusUnauthorized},
		{http.StatusNotFound, false, http.StatusUnauthorized},
		{http.StatusMethodNotAllowed, false, http.StatusUnauthorized},
		{http.StatusGone, false, http.StatusUnauthorized},
		{http.StatusForbidden, false, http.StatusForbidden},
		{http.StatusRequestEntityTooLarge, false, http.StatusRequestEntityTooLarge},
		{http.StatusTooEarly, false, http.StatusTooEarly},
		{http.StatusTooManyRequests, false, http.StatusTooManyRequests},
		{http.StatusInternalServerError, false, http.StatusInternalServerError},
		{http.StatusOK, true, http.StatusOK}, // gin served it: its own gate decided
	} {
		kernel := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c.gin {
				r.Context().Value(perimeterKey{}).(*perimeterState).gin.Store(true)
			}
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte("Riddler's answer"))
		})
		w := httptest.NewRecorder()
		srv.perimeterShim(kernel, "").ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/core/v1/riddles", nil))
		assert.Equal(t, c.want, w.Code, "core status %d (gin ran: %v)", c.status, c.gin)
		if c.want == http.StatusUnauthorized {
			assert.NotEmpty(t, w.Header().Get("WWW-Authenticate"), "core status %d", c.status)
			assert.Empty(t, w.Body.String(), "core status %d: the refusal carries core's body", c.status)
		}
	}
}

// flushErrWriter is a connection's writer whose flush fails.
type flushErrWriter struct {
	http.ResponseWriter
	err error
}

func (w flushErrWriter) FlushError() error { return w.err }

// TestPerimeterShim_FlushErrorCrossesTheShim: a handler's
// http.ResponseController flush gets the connection's error through the
// shim's writer, as it does through core's stage writers (review n2).
func TestPerimeterShim_FlushErrorCrossesTheShim(t *testing.T) {
	want := errors.New("the Batmobile's uplink dropped")
	var got error
	kernel := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Context().Value(perimeterKey{}).(*perimeterState).gin.Store(true) // gin served it
		got = http.NewResponseController(w).Flush()
	})
	(&Server{}).perimeterShim(kernel, "").ServeHTTP(flushErrWriter{httptest.NewRecorder(), want},
		httptest.NewRequest(http.MethodGet, "/api/v1/events", nil))
	if !errors.Is(got, want) {
		t.Fatalf("flush error = %v, want %v", got, want)
	}
}

// TestPerimeterShim_UnmatchedMethodMatchesGin is
// TestLegacyMount_UnmatchedMethodMatchesGin through core's stages and the
// shim: every method a manifest path does not route, OPTIONS included
// (stage 6 answers it, as HEAD's CORS middleware did), answers with HEAD's
// status, headers and body.
func TestPerimeterShim_UnmatchedMethodMatchesGin(t *testing.T) {
	for _, abs := range absModes() {
		t.Run(absName(abs), func(t *testing.T) {
			f := legacyEngine(t, abs, false)
			_, head, r1 := perimeterStacks(t, f, "")
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
