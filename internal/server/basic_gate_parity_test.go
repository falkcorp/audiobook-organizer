// file: internal/server/basic_gate_parity_test.go
// version: 1.0.0
// guid: 0c4f9a73-5b18-4e2d-b6a1-8d3e7f205c96
// last-edited: 2026-11-19

package server

import (
	"bytes"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/mediacore/httpapi"
)

// These tests hold stage 8 to what the deleted gin gate did. The reference
// is head_basic_gate_test.go's frozen copy, run live on the HEAD stack of
// perimeterStacks; the subject is the R1 stack, which has only core's stage 8.

func basicHeader(userPass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(userPass))
}

// basicRefusal reports a response the gate itself wrote: 401 with the
// challenge, and no body.
func basicRefusal(w *httptest.ResponseRecorder) bool {
	return w.Code == http.StatusUnauthorized &&
		w.Header().Get("WWW-Authenticate") == `Basic realm="Audiobook Organizer"` &&
		w.Body.Len() == 0
}

// TestBasicGate_ParityWithHEAD is the parity table: every row of gate
// configuration against every kind of request and every kind of credential,
// through HEAD's gin gate and through core's stage 8, compared on the status,
// every header and the body.
func TestBasicGate_ParityWithHEAD(t *testing.T) {
	f := legacyEngine(t, false, false)
	_, head, r1 := perimeterStacks(t, f, "")

	paths := []string{
		"/api/v1/audiobooks",    // a gated API route
		"/library/riddler",      // the SPA shell
		"/api/health",           // exempt: health
		"/api/v1/health",        // exempt: health
		"/assets/cowl.js",       // exempt: asset prefix
		"/favicon.ico",          // exempt: asset
		"/api/v1/riddler.js",    // exempt: asset suffix on an unrouted path
		"/api/v1/riddler.woff2", // exempt: asset suffix
		"/api/core/v1/riddler",  // a core-owned answer: gated, never gin's
		"/api/audiobooks",       // the /api redirect
		"/api/v1/riddler-unrouted",
	}
	type row struct {
		name         string
		enabled      bool
		user, stored string
		env          *string
		secret       string // the password a client would send to pass
	}
	rows := []row{
		{name: "disabled", enabled: false, user: "bwayne", stored: "Wayne-Manor-1", secret: "Wayne-Manor-1"},
		{name: "enabled", enabled: true, user: "bwayne", stored: "Wayne-Manor-1", secret: "Wayne-Manor-1"},
		{name: "enabled, saved wins over env", enabled: true, user: "bwayne", stored: "Wayne-Manor-1", env: new("Env-Cave-2"), secret: "Wayne-Manor-1"},
		{name: "env recovery", enabled: true, user: "bwayne", env: new("Env-Cave-2"), secret: "Env-Cave-2"},
		{name: "env recovery is trimmed", enabled: true, user: "bwayne", env: new("  Env-Cave-2 \n"), secret: "Env-Cave-2"},
		{name: "no password", enabled: true, user: "bwayne", secret: "Anything-3"},
		{name: "blank env is no password", enabled: true, user: "bwayne", env: new("   "), secret: "   "},
		{name: "saved blanks are kept", enabled: true, user: "bwayne", stored: "  Bat Signal  ", secret: "  Bat Signal  "},
		{name: "empty username", enabled: true, user: "", stored: "Wayne-Manor-1", secret: "Wayne-Manor-1"},
	}
	for _, rw := range rows {
		t.Run(rw.name, func(t *testing.T) {
			setBasicGate(t, rw.enabled, rw.user, rw.stored, rw.env)
			creds := []string{
				"",                                     // no Authorization header
				basicHeader(rw.user + ":" + rw.secret), // the right pair
				basicHeader("alfred:" + rw.secret),     // wrong user
				basicHeader(strings.ToUpper(rw.user) + ":" + rw.secret), // user in another case
				basicHeader(rw.user + ":Joker-Laugh-9"),                 // wrong password
				basicHeader(rw.user + ":"),                              // empty password
				basicHeader(":" + rw.secret),                            // empty user
				basicHeader(":"),                                        // both empty
				basicHeader(rw.user + ":" + rw.secret + " "),            // the password plus a blank
				basicHeader(rw.user + " :" + rw.secret),                 // the user plus a blank
				basicHeader(rw.user),                                    // no colon
				"Basic",                                                 // scheme alone
				"Basic ",                                                // scheme and blank
				"Basic !!!not-base64!!!",                                // malformed payload
				"Bearer " + base64.StdEncoding.EncodeToString([]byte(rw.user+":"+rw.secret)), // another scheme
				strings.Replace(basicHeader(rw.user+":"+rw.secret), "Basic", "basic", 1),     // lower-case scheme
				strings.Replace(basicHeader(rw.user+":"+rw.secret), "Basic", "BASIC", 1),     // upper-case scheme
			}
			n, refused := 0, 0
			for _, p := range paths {
				for i, auth := range creds {
					c := perimeterCase{name: rw.name + ": " + p + " cred#" + string(rune('a'+i)), method: http.MethodGet, path: p,
						coreBody: strings.HasPrefix(p, "/api/core/")} // core writes this answer with its own body
					if auth != "" {
						c.header = map[string]string{"Authorization": auth}
					}
					perimeterParity(t, head, r1, c)
					n++
					if basicRefusal(serveCase(r1, c)) {
						refused++
					}
				}
			}
			switch {
			case !rw.enabled:
				assert.Zero(t, refused, "a disabled gate refused something")
			case rw.name == "no password" || rw.name == "blank env is no password":
				// Fail closed: every non-exempt path refuses every credential,
				// "user:" included.
				assert.GreaterOrEqual(t, refused, 5*len(creds), "an enabled gate with no password must refuse all non-exempt requests")
			default:
				assert.Greater(t, refused, 0, "the enabled gate refused nothing")
				assert.Less(t, refused, n, "the enabled gate refused everything, exempt paths included")
			}
		})
	}
}

// TestBasicGate_EveryRouteIsGatedOrExempt walks the routes of the mounted
// router, legacy manifest and core's own, with the gate on and no
// credentials: each is either refused by stage 8 or on the exempt list, the
// list is exactly what the manifest flags and the asset predicate say, and
// the manifest part agrees with HEAD's gate route by route.
func TestBasicGate_EveryRouteIsGatedOrExempt(t *testing.T) {
	for _, abs := range absModes() {
		t.Run(absName(abs), func(t *testing.T) {
			f := legacyEngine(t, abs, false)
			router, head, r1 := perimeterStacks(t, f, "")
			setBasicGate(t, true, "bwayne", "Wayne-Manor-1", nil)

			manifest, err := f.srv.LegacyManifest()
			require.NoError(t, err)
			flagged := map[string]bool{} // "METHOD gin-path" -> BasicExempt or OwnAuth
			for _, lr := range manifest {
				flagged[lr.Method+" "+lr.GinPath] = lr.BasicExempt || lr.OwnAuth
			}

			gated, exempt := 0, 0
			seen := map[string]bool{}
			check := func(method, path string, wantExempt bool, label string) {
				t.Helper()
				key := method + " " + path
				if seen[key] {
					return
				}
				seen[key] = true
				c := perimeterCase{name: label + " " + key, method: method, path: path}
				got := basicRefusal(serveCase(r1, c))
				if wantExempt {
					exempt++
					assert.False(t, got, "%s is exempt but stage 8 refused it", c.name)
					return
				}
				gated++
				assert.True(t, got, "%s is not exempt but stage 8 let it through (%d)", c.name, serveCase(r1, c).Code)
			}

			// The manifest, route by route, against HEAD's own gate.
			for _, lr := range manifest {
				path := legacyConcretePath(lr.GinPath)
				c := perimeterCase{name: lr.Method + " " + path, method: lr.Method, path: path}
				headRefused := serveCase(head, c).Code == http.StatusUnauthorized &&
					serveCase(head, c).Header().Get("WWW-Authenticate") != ""
				assert.Equal(t, headRefused, basicRefusal(serveCase(r1, c)), "%s: HEAD and stage 8 disagree", c.name)
				check(lr.Method, path, flagged[lr.Method+" "+lr.GinPath] || LegacyBasicExemptAssets(path), "manifest")
			}
			// Every route the router knows, core's own included.
			param := regexp.MustCompile(`\{[^}]*\}|:[A-Za-z_]+|\*[A-Za-z_]+`)
			for ri := range router.Routes() {
				method := ri.Method
				if method == "" {
					method = http.MethodGet
				}
				pat := strings.TrimSuffix(ri.Pattern, "{$}")
				path := param.ReplaceAllString(pat, "riddler")
				if ri.Kind == httpapi.RouteKindLegacy {
					continue // walked above, against the manifest's flags
				}
				probe := ri.Kind == httpapi.RouteKindInfra && (ri.Pattern == "/livez" || ri.Pattern == "/readyz")
				check(method, path, probe || LegacyBasicExemptAssets(path) && ri.Kind == httpapi.RouteKindSPA, "router")
			}
			assert.Greater(t, gated, 150, "too few gated routes; the walk is broken")
			assert.GreaterOrEqual(t, exempt, 2, "too few exempt routes; the walk is broken")
		})
	}
}

// TestBasicGate_NeverLogsThePassword: whatever the gate sees, the saved
// password, the environment password, the presented ones and their base64
// forms appear in no log line (core's or the server's) and in no response.
func TestBasicGate_NeverLogsThePassword(t *testing.T) {
	var mu sync.Mutex
	var buf bytes.Buffer
	w := &lockedWriter{mu: &mu, w: &buf}
	logger := slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
	prev := slog.Default()
	slog.SetDefault(logger)
	basicGateEnvWarnOnce = sync.Once{}
	t.Cleanup(func() { slog.SetDefault(prev); basicGateEnvWarnOnce = sync.Once{} })

	f := legacyEngine(t, false, false)
	_, stack := mountBooksWith(t, f.srv, httpapi.Deps{Log: logger})

	const saved, env, wrong = "Wayne-Vault-4242", "Env-Batcave-7777", "Joker-Laugh-1313"
	secrets := []string{saved, env, wrong}
	for _, s := range append([]string(nil), secrets...) {
		secrets = append(secrets,
			base64.StdEncoding.EncodeToString([]byte("bwayne:"+s)),
			base64.StdEncoding.EncodeToString([]byte("alfred:"+s)))
	}
	var responses strings.Builder
	serve := func(auth string) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks", nil)
		req.RemoteAddr = "127.0.0.1:41000"
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		stack.ServeHTTP(rec, req)
		responses.WriteString(rec.Body.String())
		for k, v := range rec.Header() {
			responses.WriteString(k + ": " + strings.Join(v, ",") + "\n")
		}
	}
	for _, row := range []struct {
		stored string
		env    *string
		pass   string
	}{{saved, nil, saved}, {saved, new(env), saved}, {"", new(env), env}, {"", nil, ""}} {
		setBasicGate(t, true, "bwayne", row.stored, row.env)
		serve(basicHeader("bwayne:" + row.pass))
		serve(basicHeader("bwayne:" + wrong))
		serve(basicHeader("alfred:" + row.pass))
		serve("")
	}
	// Resolve through the posture reader as /health's source and the startup
	// line do.
	setBasicGate(t, true, "bwayne", "", new(env))
	_ = basicGateUsesEnvPassword()
	_ = basicGateNoPassword()

	mu.Lock()
	logged := buf.String()
	mu.Unlock()
	assert.Contains(t, logged, BasicGateEnvPasswordWarning, "the env recovery must warn, so the test sees the log")
	for _, s := range secrets {
		assert.NotContains(t, logged, s, "a secret reached the log")
		assert.NotContains(t, responses.String(), s, "a secret reached a response")
	}
}
