// file: core/httpapi/basicgate_test.go
// version: 1.1.0
// guid: bdca9e39-c6b8-4939-8a50-7d290cb39a9b
// last-edited: 2026-11-10

package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/secrets"
)

// headBasicAssets is HEAD's static-asset rule (basicauth.go:40-46 at HEAD),
// the predicate F3-07 hands over as LegacyMount.BasicExemptAssets.
func headBasicAssets(p string) bool {
	return strings.HasPrefix(p, "/assets/") || p == "/favicon.ico" ||
		strings.HasSuffix(p, ".js") || strings.HasSuffix(p, ".css") || strings.HasSuffix(p, ".png") ||
		strings.HasSuffix(p, ".svg") || strings.HasSuffix(p, ".woff2")
}

// gcpdBasicRoutes is the fixture manifest: HEAD's two BasicExempt health
// routes, an OwnAuth ABS-shaped route, an ABS-shaped route without the ABS
// authenticator, a generalised OwnAuth collision set and a generalised
// BasicExempt one, a gated wildcard route, OwnAuth and BasicExempt routes
// ending in a rest wildcard or a "/", gated routes whose slashless spelling
// looks like an asset, and /ping, /status, /login.
func gcpdBasicRoutes() []LegacyRoute {
	public := func(method, path string) LegacyRoute {
		return LegacyRoute{Method: method, GinPath: path, Perm: rbac.Public, Public: true}
	}
	ownAuth := func(path string) LegacyRoute {
		return LegacyRoute{Method: http.MethodGet, GinPath: path, Perm: rbac.Delegated("signal"),
			OwnAuth: true, AuthMethod: "gcpd.signal"}
	}
	health := func(path string) LegacyRoute {
		lr := public(http.MethodGet, path)
		lr.BasicExempt = true
		return lr
	}
	return []LegacyRoute{
		health("/api/health"),
		health("/api/v1/health"),
		ownAuth("/api/v1/items/:id"),
		public(http.MethodGet, "/api/v1/items/:id/cover"),
		ownAuth("/api/v1/libraries/:id/items"),
		ownAuth("/api/v1/libraries/recent/:kind"),
		gothamRoute(http.MethodGet, "/api/v1/cases/:id"),
		ownAuth("/api/v1/files/*path"),
		ownAuth("/api/v1/feeds/"),
		health("/api/v2/health/"),
		health("/api/v1/watchtower/:id/beacon"),
		health("/api/v1/watchtower/recent/:kind"),
		gothamRoute(http.MethodGet, "/api/v1/batsignal.js/"),
		gothamRoute(http.MethodGet, "/api/v1/riddles.js/*page"),
		public(http.MethodGet, "/ping"),
		public(http.MethodGet, "/status"),
		public(http.MethodPost, "/login"),
	}
}

func ginRoutesOf(routes []LegacyRoute) []string {
	out := make([]string, 0, len(routes))
	for _, lr := range routes {
		out = append(out, lr.Method+" "+lr.GinPath)
	}
	return out
}

// gcpdBasicRouter is the fixture router: /livez and /readyz, /metrics and a
// native /build.js (Public infra that is not a probe), and the gcpd mount
// owning "/" with assets as its BasicExemptAssets. It returns the built
// handler and stage 8's route facts.
func gcpdBasicRouter(t *testing.T, assets func(string) bool) (http.Handler, func(*http.Request) bool) {
	t.Helper()
	r, h := gcpdBasicRouterBuilt(t, assets)
	return h, routeFacts(t, r)
}

// gcpdBasicRouterBuilt is gcpdBasicRouter's router and built handler.
func gcpdBasicRouterBuilt(t *testing.T, assets func(string) bool) (*Router, http.Handler) {
	t.Helper()
	r := NewRouter(Options{}, Deps{})
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	for _, p := range []string{"GET /livez", "GET /readyz", "GET /metrics", "GET /build.js"} {
		r.Infra(p, rbac.Public, ok)
	}
	for _, c := range []string{"/api/", "/ping", "/status", "/login"} {
		if err := r.Claim("gcpd", c); err != nil {
			t.Fatal(err)
		}
	}
	r.AllowAuthMethods("gcpd", "gcpd.signal")
	routes := gcpdBasicRoutes()
	if err := r.MountLegacy(LegacyMount{Owner: "gcpd", Handler: newFakeGin(ginRoutesOf(routes)...), Auth: LegacyAuthGin,
		ServesSPA: true, TopSegments: []string{"ping", "status"}, SPAShared: []string{"login"},
		BasicExemptAssets: assets, Routes: manifestOf(routes...)}); err != nil {
		t.Fatal(err)
	}
	h := mustBuild(t, r)
	// The collision set must really have folded into one generalised
	// pattern, or the generalised cases below test nothing.
	generalised := map[string]bool{}
	for ri := range r.Routes() {
		if strings.HasSuffix(ri.Pattern, "/{p4}/{p5}") {
			generalised[ri.Name] = true
		}
	}
	for _, n := range []string{"/api/v1/libraries/:id/items", "/api/v1/watchtower/:id/beacon"} {
		if !generalised[n] {
			t.Fatalf("fixture: %s did not fold into a generalised pattern", n)
		}
	}
	return r, h
}

func routeFacts(t *testing.T, r *Router) func(*http.Request) bool {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	exempt, err := r.basicGateRoutes()
	if err != nil {
		t.Fatalf("basicGateRoutes: %v", err)
	}
	return exempt
}

// fixedPassword is a BasicGatePassword with a fixed answer.
type fixedPassword struct {
	v   secrets.Value
	err error
}

func (f fixedPassword) BasicGatePassword(context.Context) (secrets.Value, error) { return f.v, f.err }

// lastReport is a health.Reporter that keeps every Result.
type lastReport struct {
	mu  sync.Mutex
	got []health.Result
}

func (l *lastReport) Report(res health.Result) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = append(l.got, res)
}

func (l *lastReport) last() (health.Result, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.got) == 0 {
		return health.Result{}, false
	}
	return l.got[len(l.got)-1], true
}

func settingsOf(enabled bool, username string) func() BasicGateSettings {
	return func() BasicGateSettings { return BasicGateSettings{Enabled: enabled, Username: username} }
}

// headRealm is HEAD's realm (basicauth.go basicChallenge), which F3-07
// passes as BasicGateOptions.Realm; mustGate passes it too.
const headRealm = "Audiobook Organizer"

func mustGate(t *testing.T, o BasicGateOptions, exempt func(*http.Request) bool) *basicGate {
	t.Helper()
	if o.Realm == "" {
		o.Realm = headRealm
	}
	g, err := newBasicGate(o, exempt)
	if err != nil {
		t.Fatalf("newBasicGate: %v", err)
	}
	return g
}

// basicRequest serves one request through h; creds "" sends no
// Authorization header, otherwise "user:pass".
func basicRequest(h http.Handler, method, target, creds string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	if creds != "" {
		user, pass, _ := strings.Cut(creds, ":")
		req.SetBasicAuth(user, pass)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// refused reports the gate's own refusal: 401 with HEAD's challenge and no
// body. No fixture handler ever answers 401.
func refused(t *testing.T, rec *httptest.ResponseRecorder) bool {
	t.Helper()
	if rec.Code != http.StatusUnauthorized {
		return false
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != `Basic realm="`+headRealm+`"` {
		t.Fatalf("401 challenge = %q, want HEAD's realm", got)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("401 body = %q, want none", rec.Body.String())
	}
	return true
}

// TestBasicGate_HEADExemptRules: on a fixture router, stage 8's verdicts are
// HEAD's (basicauth.go:34,40-46,69): an enabled gate with a password, and a
// request without credentials, passes exactly the exempt set. F3-07 turns
// this into the reviewed golden over the real manifest
// (TestBasicGate_ExemptSetGolden).
func TestBasicGate_HEADExemptRules(t *testing.T) {
	type verdict struct {
		method, target string
		exempt         bool
	}
	cases := []verdict{
		// Public infra probes; other Public infra stays gated (HEAD gated /metrics).
		{http.MethodGet, "/livez", true},
		{http.MethodGet, "/readyz", true},
		{http.MethodGet, "/metrics", false},
		{http.MethodGet, "/build.js", false}, // a native route never takes the asset rule
		// BasicExempt health routes, per manifest entry as OwnAuth is: a
		// method the manifest does not list is gated, HEAD included (HEAD's
		// path test exempted both into gin's 404), and a path only the
		// generalised pattern of a BasicExempt collision set matches is gated.
		{http.MethodGet, "/api/health", true},
		{http.MethodHead, "/api/health", false},
		{http.MethodGet, "/api/v1/health", true},
		{http.MethodPost, "/api/health", false},
		{http.MethodGet, "/api/v1/watchtower/42/beacon", true},
		{http.MethodGet, "/api/v1/watchtower/recent/lanterns", true},
		{http.MethodGet, "/api/v1/watchtower/42/lanterns", false},
		// A route ending in a rest wildcard or a "/": the spelling without
		// the slash is gin's FullPath "", so it is gated, never exempt.
		{http.MethodGet, "/api/v1/files", false},
		{http.MethodGet, "/api/v1/files/", true},
		{http.MethodGet, "/api/v1/files/joker/cards", true},
		{http.MethodGet, "/api/v1/feeds", false},
		{http.MethodGet, "/api/v1/feeds/", true},
		{http.MethodGet, "/api/v2/health", false},
		{http.MethodGet, "/api/v2/health/", true},
		// A slashless spelling matches no route, so the "/" owner's asset
		// rule applies to it as HEAD's did; the slashed one is the gated
		// route itself.
		{http.MethodGet, "/api/v1/batsignal.js", true},
		{http.MethodGet, "/api/v1/batsignal.js/", false},
		{http.MethodGet, "/api/v1/riddles.js", true},
		{http.MethodGet, "/api/v1/riddles.js/", false},
		// OwnAuth: the route gin runs carries the ABS authenticator.
		{http.MethodGet, "/api/v1/items/42", true},
		{http.MethodHead, "/api/v1/items/42", false}, // gin has no HEAD route there
		{http.MethodGet, "/api/v1/items/42/cover", false},
		{http.MethodGet, "/api/v1/libraries/42/items", true},     // a member of the generalised pattern
		{http.MethodGet, "/api/v1/libraries/recent/books", true}, // the kept route of the set
		{http.MethodGet, "/api/v1/libraries/42/batmobiles", false},
		{http.MethodGet, "/api/v1/libraries/a%2Fb/items", false}, // gin routes the decoded path
		// No ".." spelling widens the OwnAuth exemption.
		{http.MethodGet, "/api/v1/items/../cases/1", false},
		{http.MethodGet, "/api/v1/items/%2e%2e/cases/1", false},
		{http.MethodGet, "/api/v1/items/42/..", false},
		// An unclean path goes to the "/" owner, whose asset rule then
		// applies to the path as sent, as HEAD's did, even when its clean
		// form is a native route.
		{http.MethodGet, "/batcave/../build.js", true},
		// HEAD's asset rule on the "/" owner: the fallback and the mount's routes.
		{http.MethodGet, "/assets/cowl-3f2a.js", true},
		{http.MethodGet, "/assets/batarang", true},
		{http.MethodGet, "/favicon.ico", true},
		{http.MethodGet, "/app.js", true},
		{http.MethodGet, "/static/cape.css", true},
		{http.MethodGet, "/img/signal.png", true},
		{http.MethodGet, "/img/signal.svg", true},
		{http.MethodGet, "/fonts/gotham.woff2", true},
		{http.MethodGet, "/api/v1/cases/clue.js", true}, // a wildcard route of the mount, as at HEAD
		{http.MethodGet, "/api/v1/nowhere.css", true},   // an unmatched path, as at HEAD
		{http.MethodGet, "/fonts/gotham.woff", false},
		// Everything else is gated.
		{http.MethodGet, "/api/v1/cases/42", false},
		{http.MethodGet, "/ping", false},
		{http.MethodGet, "/status", false},
		{http.MethodPost, "/login", false},
		{http.MethodGet, "/login", false},
		{http.MethodGet, "/", false},
		{http.MethodGet, "/batcave", false},
		{http.MethodGet, "/api/v1/nowhere", false},
	}
	h, exempt := gcpdBasicRouter(t, headBasicAssets)
	gate := mustGate(t, BasicGateOptions{Settings: settingsOf(true, "alfred"), Password: fixedPassword{v: secrets.NewValue("Arkham-Asylum")}}, exempt)
	gated := gate.wrap(h)
	for _, c := range cases {
		rec := basicRequest(gated, c.method, c.target, "")
		if got := !refused(t, rec); got != c.exempt {
			t.Errorf("%s %s: exempt = %v (status %d), want %v", c.method, c.target, got, rec.Code, c.exempt)
		}
		// The same request with the right credentials always passes.
		if rec := basicRequest(gated, c.method, c.target, "alfred:Arkham-Asylum"); refused(t, rec) {
			t.Errorf("%s %s with the right credentials: 401", c.method, c.target)
		}
	}

	t.Run("nil predicate exempts no asset", func(t *testing.T) {
		h, exempt := gcpdBasicRouter(t, nil)
		gated := mustGate(t, BasicGateOptions{Settings: settingsOf(true, ""), Password: fixedPassword{v: secrets.NewValue("Arkham-Asylum")}}, exempt).wrap(h)
		for _, c := range []verdict{
			{http.MethodGet, "/assets/cowl-3f2a.js", false},
			{http.MethodGet, "/api/v1/cases/clue.js", false},
			{http.MethodGet, "/favicon.ico", false},
			{http.MethodGet, "/api/health", true},
			{http.MethodGet, "/api/v1/items/42", true},
			{http.MethodGet, "/livez", true},
		} {
			if got := !refused(t, basicRequest(gated, c.method, c.target, "")); got != c.exempt {
				t.Errorf("%s %s: exempt = %v, want %v", c.method, c.target, got, c.exempt)
			}
		}
	})

	t.Run("Router.SPA owns the root", func(t *testing.T) {
		r := NewRouter(Options{}, Deps{})
		ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		r.Infra("GET /livez", rbac.Public, ok)
		r.Infra("GET /build.js", rbac.Public, ok)
		if err := r.Claim("gcpd", "/api/v1/"); err != nil {
			t.Fatal(err)
		}
		cases := gothamRoute(http.MethodGet, "/api/v1/cases/:id")
		if err := r.MountLegacy(LegacyMount{Owner: "gcpd", Handler: newFakeGin("GET /api/v1/cases/:id"), Auth: LegacyAuthGin,
			BasicExemptAssets: headBasicAssets, Routes: manifestOf(cases)}); err != nil {
			t.Fatal(err)
		}
		if err := r.SPA(ok, SPAOptions{BasicExemptAssets: headBasicAssets}); err != nil {
			t.Fatal(err)
		}
		h := mustBuild(t, r)
		gated := mustGate(t, BasicGateOptions{Settings: settingsOf(true, ""), Password: fixedPassword{v: secrets.NewValue("Arkham-Asylum")}}, routeFacts(t, r)).wrap(h)
		for _, c := range []verdict{
			{http.MethodGet, "/assets/cowl-3f2a.js", true}, // the shell's own path
			{http.MethodGet, "/livez", true},
			{http.MethodGet, "/build.js", false},             // core is the "/" owner, yet a native route is gated
			{http.MethodGet, "/api/v1/cases/clue.js", false}, // a mount that does not own "/"
			{http.MethodGet, "/batcave", false},
		} {
			if got := !refused(t, basicRequest(gated, c.method, c.target, "")); got != c.exempt {
				t.Errorf("%s %s: exempt = %v, want %v", c.method, c.target, got, c.exempt)
			}
		}
	})
}

// TestBasicGate_UsernameCompared: the username is compared as well as the
// password; an empty configured username keeps HEAD's behaviour, so
// ":<password>" passes (§10, SEC6-9).
func TestBasicGate_UsernameCompared(t *testing.T) {
	h, exempt := gcpdBasicRouter(t, headBasicAssets)
	pw := fixedPassword{v: secrets.NewValue("Wayne-Manor")}
	named := mustGate(t, BasicGateOptions{Settings: settingsOf(true, "alfred"), Password: pw}, exempt).wrap(h)
	anonymous := mustGate(t, BasicGateOptions{Settings: settingsOf(true, ""), Password: pw}, exempt).wrap(h)
	cases := []struct {
		name   string
		h      http.Handler
		creds  string
		refuse bool
	}{
		{"wrong username, right password", named, "bruce:Wayne-Manor", true},
		{"right username, right password", named, "alfred:Wayne-Manor", false},
		{"right username, wrong password", named, "alfred:Wayne-Mano", true},
		{"right username, longer password", named, "alfred:Wayne-Manor!", true},
		{"right username, empty password", named, "alfred:", true},
		{"empty username for a named gate", named, ":Wayne-Manor", true},
		{"empty configured username, :<password>", anonymous, ":Wayne-Manor", false},
		{"empty configured username, a username presented", anonymous, "alfred:Wayne-Manor", true},
		{"empty configured username, empty password", anonymous, ":", true},
	}
	for _, c := range cases {
		if got := refused(t, basicRequest(c.h, http.MethodGet, "/api/v1/cases/42", c.creds)); got != c.refuse {
			t.Errorf("%s: refused = %v, want %v", c.name, got, c.refuse)
		}
	}
	for name, hdr := range map[string]string{"none": "", "bearer": "Bearer Wayne-Manor", "not base64": "Basic !!!", "no colon": "Basic YWxmcmVk"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/cases/42", nil)
		if hdr != "" {
			req.Header.Set("Authorization", hdr)
		}
		rec := httptest.NewRecorder()
		named.ServeHTTP(rec, req)
		if !refused(t, rec) {
			t.Errorf("Authorization %s: status %d, want 401", name, rec.Code)
		}
	}
}

// Stored and operator states of the property test.
type storedPassword uint8

const (
	storedUnset storedPassword = iota
	storedCleared
	storedEmpty
	storedSet
	storedUnreadable
)

func (s storedPassword) String() string {
	return [...]string{"unset", "cleared", "empty", "set", "unreadable"}[s]
}

type operatorValue uint8

const (
	operatorAbsent operatorValue = iota
	operatorEmpty
	operatorSet
)

func (o operatorValue) String() string { return [...]string{"absent", "empty", "set"}[o] }

// errTestNotSet and errTestKeyUnavailable stand for secrets.ErrNotSet and
// secrets.ErrKeyUnavailable, which F3-03 adds; F3-07's binding maps them onto
// the seam's error.
var (
	errTestNotSet         = errors.New("secrets: not set")
	errTestKeyUnavailable = errors.New("secrets: key unavailable")
)

// basicGateState is one draw of the property test.
type basicGateState struct {
	storedEnabled bool
	storedPwd     storedPassword
	opEnabled     operatorValue // empty = false, set = true
	opPwd         operatorValue
	username      string
	method        string
	target        string
	exempt        bool
	creds         string // "" = no Authorization header
}

const (
	storedSecret   = "Oracle-Clocktower-17"
	operatorSecret = "Batcave-Override-42"
)

// effective is core config's resolution of the state, which stage 8 only
// reads: enabled is fail-closed (an operator false cannot switch off a gate
// enabled in the stored settings, §10), and an operator password, empty
// included, locks the key like any key (05-config §1.3.5), so the stored row
// is read only when the operator sets none.
func (s basicGateState) effective() (enabled bool, pw fixedPassword) {
	enabled = s.storedEnabled || s.opEnabled == operatorSet
	switch s.opPwd {
	case operatorEmpty:
		return enabled, fixedPassword{v: secrets.NewValue("")}
	case operatorSet:
		return enabled, fixedPassword{v: secrets.NewValue(operatorSecret)}
	}
	switch s.storedPwd {
	case storedUnset:
		return enabled, fixedPassword{err: errTestNotSet}
	case storedCleared:
		return enabled, fixedPassword{}
	case storedEmpty:
		return enabled, fixedPassword{v: secrets.NewValue("")}
	case storedSet:
		return enabled, fixedPassword{v: secrets.NewValue(storedSecret)}
	default:
		return enabled, fixedPassword{err: errTestKeyUnavailable}
	}
}

// TestBasicGate_EmptyPasswordRefuses is early fix 5 ported as a property test
// (05-http-api §12; 05-authn §10, CONFORM-14): over random states of the
// stored enabled and password (unset, cleared, empty, set, unreadable) and
// the operator enabled and password (absent, empty, set), with a random
// presented username and password, a non-exempt request is admitted only
// when the gate is off, or when the effective password is non-empty and the
// presented credentials match it; "<username>:" is always refused; the exempt
// set always passes; an enabled gate with no usable password raises
// basic_gate_no_password and names the key in the startup log; an operator
// password admits whatever the stored row holds. No log line carries a
// credential.
func TestBasicGate_EmptyPasswordRefuses(t *testing.T) {
	h, exempt := gcpdBasicRouter(t, headBasicAssets)
	targets := []struct {
		method, target string
		exempt         bool
	}{
		{http.MethodGet, "/livez", true},
		{http.MethodGet, "/api/health", true},
		{http.MethodGet, "/api/v1/health", true},
		{http.MethodGet, "/api/v1/items/42", true},
		{http.MethodGet, "/assets/cowl-3f2a.js", true},
		{http.MethodGet, "/api/v1/cases/clue.css", true},
		{http.MethodGet, "/api/v1/cases/42", false},
		{http.MethodGet, "/api/v1/items/42/cover", false},
		{http.MethodGet, "/ping", false},
		{http.MethodGet, "/status", false},
		{http.MethodPost, "/login", false},
		{http.MethodGet, "/metrics", false},
		{http.MethodGet, "/batcave", false},
	}
	usernames := []string{"", "alfred"}
	presentedUsers := []string{"", "alfred", "bruce"}
	presentedPwds := []string{"", storedSecret, operatorSecret, "Joker-Card", storedSecret + "x"}
	rng := rand.New(rand.NewPCG(0x5eed_ba75, 0xc0de_cafe))
	const iterations = 4000
	seen := map[string]bool{}
	for i := range iterations {
		tg := targets[rng.IntN(len(targets))]
		s := basicGateState{
			storedEnabled: rng.IntN(2) == 1,
			storedPwd:     storedPassword(rng.IntN(5)),
			opEnabled:     operatorValue(rng.IntN(3)),
			opPwd:         operatorValue(rng.IntN(3)),
			username:      usernames[rng.IntN(len(usernames))],
			method:        tg.method,
			target:        tg.target,
			exempt:        tg.exempt,
		}
		if rng.IntN(6) > 0 {
			s.creds = presentedUsers[rng.IntN(len(presentedUsers))] + ":" + presentedPwds[rng.IntN(len(presentedPwds))]
		}
		enabled, pw := s.effective()
		var logs bytes.Buffer
		rep := &lastReport{}
		g := mustGate(t, BasicGateOptions{Settings: settingsOf(enabled, s.username), Password: pw, Health: rep,
			Log: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}, exempt)
		// The startup check (F3-07 calls it at start).
		noPassword := g.checkPosture(context.Background())
		effective := pw.v.Reveal()
		usable := pw.err == nil && effective != ""
		if want := enabled && !usable; noPassword != want {
			t.Fatalf("#%d %+v: checkPosture = %v, want %v", i, s, noPassword, want)
		}
		rec := basicRequest(g.wrap(h), s.method, s.target, s.creds)
		gotRefused := refused(t, rec)

		user, pass, hasCreds := strings.Cut(s.creds, ":")
		admitted := !enabled || s.exempt || (usable && hasCreds && user == s.username && pass == effective)
		if gotRefused == admitted {
			t.Fatalf("#%d %+v: refused = %v, want admitted = %v", i, s, gotRefused, admitted)
		}
		if enabled && !s.exempt && hasCreds && pass == "" && !gotRefused {
			t.Fatalf("#%d %+v: %q was admitted; an empty password never matches", i, s, s.creds)
		}
		if enabled && s.opPwd == operatorSet && !s.exempt && s.creds == s.username+":"+operatorSecret && gotRefused {
			t.Fatalf("#%d %+v: the operator password was refused (stored %s)", i, s, s.storedPwd)
		}
		last, reported := rep.last()
		switch {
		case !reported:
			t.Fatalf("#%d %+v: no health report", i, s)
		case enabled && !usable:
			if last.Status != health.StatusDegraded || last.Reason != BasicGateNoPasswordToken {
				t.Fatalf("#%d %+v: health = %+v, want degraded %s", i, s, last, BasicGateNoPasswordToken)
			}
			if !strings.Contains(logs.String(), "key="+BasicGatePasswordKey) || !strings.Contains(logs.String(), "level=ERROR") {
				t.Fatalf("#%d %+v: startup log does not name %s at ERROR: %q", i, s, BasicGatePasswordKey, logs.String())
			}
		default:
			if last.Status != health.StatusUp || last.Reason != "" {
				t.Fatalf("#%d %+v: health = %+v, want up", i, s, last)
			}
			if logs.Len() != 0 {
				t.Fatalf("#%d %+v: unexpected log %q", i, s, logs.String())
			}
		}
		for _, secret := range []string{storedSecret, operatorSecret, "Joker-Card", "bruce"} {
			if strings.Contains(logs.String(), secret) {
				t.Fatalf("#%d %+v: the log carries a credential: %q", i, s, logs.String())
			}
		}
		seen[fmt.Sprintf("%v/%s/%s/%s", s.storedEnabled, s.storedPwd, s.opEnabled, s.opPwd)] = true
	}
	// Every layered state was drawn: 2 stored enabled * 5 stored passwords *
	// 3 operator enabled * 3 operator passwords.
	if len(seen) != 2*5*3*3 {
		t.Fatalf("%d of the 90 layered states drawn; raise the iterations", len(seen))
	}
}

// TestBasicGate_PostureTransitions: the health token and the log follow the
// password without a restart; the resolver's error is named, never a value.
func TestBasicGate_PostureTransitions(t *testing.T) {
	var logs bytes.Buffer
	rep := &lastReport{}
	enabled := true
	pw := &switchablePassword{err: errTestKeyUnavailable}
	g := mustGate(t, BasicGateOptions{Settings: func() BasicGateSettings { return BasicGateSettings{Enabled: enabled} },
		Password: pw, Health: rep, Log: slog.New(slog.NewTextHandler(&logs, nil))}, func(*http.Request) bool { return false })
	ctx := context.Background()

	if !g.checkPosture(ctx) {
		t.Fatal("unreadable password: checkPosture = false")
	}
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), BasicGatePasswordKey) ||
		!strings.Contains(logs.String(), errTestKeyUnavailable.Error()) {
		t.Fatalf("log = %q, want an ERROR naming the key and the resolver's error", logs.String())
	}
	g.checkPosture(ctx)
	if n := strings.Count(logs.String(), "level=ERROR"); n != 1 {
		t.Fatalf("an unchanged posture logged again: %d ERROR lines", n)
	}
	if got := len(rep.got); got != 1 {
		t.Fatalf("an unchanged posture reported again: %d reports", got)
	}

	pw.set(secrets.NewValue("Wayne-Manor"), nil)
	if g.checkPosture(ctx) {
		t.Fatal("password saved: checkPosture = true")
	}
	if last, _ := rep.last(); last.Status != health.StatusUp || last.Reason != "" {
		t.Fatalf("password saved: health = %+v, want up", last)
	}
	if !strings.Contains(logs.String(), "level=INFO") {
		t.Fatalf("log = %q, want the recovery line", logs.String())
	}

	// A request refuses on a cleared password at once, but reports nothing:
	// health follows the next check (F3-07 runs one after each change).
	pw.set(secrets.Value{}, nil)
	if !refused(t, basicRequest(g.wrap(http.NotFoundHandler()), http.MethodGet, "/batcave", ":Wayne-Manor")) {
		t.Fatal("cleared password: request admitted")
	}
	if last, _ := rep.last(); last.Status != health.StatusUp {
		t.Fatalf("cleared password: a request reported %+v", last)
	}
	if !g.checkPosture(ctx) {
		t.Fatal("cleared password: checkPosture = false")
	}
	if last, _ := rep.last(); last.Reason != BasicGateNoPasswordToken {
		t.Fatalf("cleared password: health = %+v", last)
	}

	enabled = false
	if g.checkPosture(ctx) {
		t.Fatal("gate off: checkPosture = true")
	}
	if last, _ := rep.last(); last.Status != health.StatusUp {
		t.Fatalf("gate off: health = %+v, want up", last)
	}
	if strings.Contains(logs.String(), "Wayne-Manor") {
		t.Fatalf("the log carries the password: %q", logs.String())
	}

	// A resolver that errs is refused even if it also hands back bytes.
	pw.set(secrets.NewValue("Wayne-Manor"), errTestKeyUnavailable)
	enabled = true
	if !refused(t, basicRequest(g.wrap(http.NotFoundHandler()), http.MethodGet, "/batcave", ":Wayne-Manor")) {
		t.Fatal("resolver error with a value: request admitted")
	}

	// No reporter and no logger: the gate still decides.
	quiet := mustGate(t, BasicGateOptions{Settings: settingsOf(true, ""), Password: fixedPassword{err: errTestNotSet}},
		func(*http.Request) bool { return false })
	if !quiet.checkPosture(ctx) {
		t.Fatal("quiet gate: checkPosture = false")
	}
}

// switchablePassword is a BasicGatePassword whose answer the test changes.
type switchablePassword struct {
	mu  sync.Mutex
	v   secrets.Value
	err error
}

func (s *switchablePassword) set(v secrets.Value, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.v, s.err = v, err
}

func (s *switchablePassword) BasicGatePassword(context.Context) (secrets.Value, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.v, s.err
}

// TestBasicGate_Inputs: a gate that cannot read its switch, its password or
// its route facts is never built, and route facts exist only after Build.
func TestBasicGate_Inputs(t *testing.T) {
	exempt := func(*http.Request) bool { return false }
	pw := fixedPassword{v: secrets.NewValue("Wayne-Manor")}
	for name, c := range map[string]struct {
		o      BasicGateOptions
		exempt func(*http.Request) bool
	}{
		"no settings": {BasicGateOptions{Password: pw}, exempt},
		"no password": {BasicGateOptions{Settings: settingsOf(true, "")}, exempt},
		"no facts":    {BasicGateOptions{Settings: settingsOf(true, ""), Password: pw}, nil},
	} {
		if _, err := newBasicGate(c.o, c.exempt); !errors.Is(err, errBasicGateInput) {
			t.Errorf("%s: err = %v, want errBasicGateInput", name, err)
		}
	}
	r := NewRouter(Options{}, Deps{})
	r.mu.Lock()
	_, err := r.basicGateRoutes()
	r.mu.Unlock()
	if !errors.Is(err, errBasicGateInput) {
		t.Fatalf("route facts before Build: err = %v", err)
	}
	// An empty router: nothing is exempt, nothing panics.
	mustBuild(t, r)
	facts := routeFacts(t, r)
	if facts(httptest.NewRequest(http.MethodGet, "/assets/cowl.js", nil)) {
		t.Fatal("a router without a \"/\" owner exempted an asset path")
	}
}

// TestGinPathMatches pins the matcher the OwnAuth exemption uses against
// gin's route syntax.
func TestGinPathMatches(t *testing.T) {
	cases := []struct {
		gin, path string
		want      bool
	}{
		{"/api/v1/items/:id", "/api/v1/items/42", true},
		{"/api/v1/items/:id", "/api/v1/items/", false},
		{"/api/v1/items/:id", "/api/v1/items/42/cover", false},
		{"/api/v1/items/:id/cover", "/api/v1/items/42", false},
		{"/api/v1/items/batch", "/api/v1/items/42", false},
		{"/files/*path", "/files/a/b", true},
		{"/files/*path", "/files/", true},
		{"/files/*path", "/files", false}, // gin's FullPath is "" there
		{"/api/v1/files/*path", "/api/v1", false},
		{"/api/feeds/", "/api/feeds/", true},
		{"/api/feeds/", "/api/feeds", false},
	}
	for _, c := range cases {
		if got := ginPathMatches(c.gin, c.path); got != c.want {
			t.Errorf("ginPathMatches(%q, %q) = %v, want %v", c.gin, c.path, got, c.want)
		}
	}
	if !slices.Equal(ginRoutesOf(gcpdBasicRoutes()[:1]), []string{"GET /api/health"}) {
		t.Fatal("fixture: ginRoutesOf")
	}
}

// concreteGinPath turns a gin route path into a request path gin routes to
// it: ":name" becomes one segment, "*name" two.
func concreteGinPath(gin string) string {
	segs := strings.Split(gin, "/")
	for i, s := range segs {
		switch {
		case strings.HasPrefix(s, ":"):
			segs[i] = "x" + s[1:]
		case strings.HasPrefix(s, "*"):
			segs[i] = "x/" + s[1:]
		}
	}
	return strings.Join(segs, "/")
}

// TestBasicGate_FactsAreBuilds: every pattern Build serves resolves, in the
// gate, to the facts Build recorded for it, and those facts are the route's
// own (a probe; BasicExempt and OwnAuth per manifest entry; the "/" owner's
// mount). The gate reads Build's ServeMux itself, so a request reaches the
// same pattern in both.
func TestBasicGate_FactsAreBuilds(t *testing.T) {
	r, h := gcpdBasicRouterBuilt(t, nil) // no asset rule: the verdict is the facts alone
	exempt := routeFacts(t, r)
	manifest := map[string]LegacyRoute{}
	for _, lr := range gcpdBasicRoutes() {
		manifest[lr.Method+" "+lr.GinPath] = lr
	}
	n := 0
	for ri := range r.Routes() {
		if ri.Kind == RouteKindSPA {
			continue
		}
		n++
		target := ri.Pattern
		if ri.Kind == RouteKindLegacy {
			target = concreteGinPath(ri.Name)
		}
		req := httptest.NewRequest(ri.Method, target, nil)
		if rec := basicRequest(h, ri.Method, target, ""); rec.Code != http.StatusOK {
			t.Errorf("%s %s (%s): served %d, want the route's 200", ri.Method, target, ri.Name, rec.Code)
		}
		r.mu.Lock()
		got, pattern := r.gateMux.Handler(req)
		r.mu.Unlock()
		if want := ri.Method + " " + ri.Pattern; pattern != want {
			t.Errorf("%s %s: the gate matched %q, Build registered %q", ri.Method, target, pattern, want)
			continue
		}
		gr, ok := got.(gateRoute)
		if !ok {
			t.Errorf("%s: handler %T carries no facts", pattern, got)
			continue
		}
		var wantExempt bool
		switch ri.Kind {
		case RouteKindLegacy:
			lr, found := manifest[ri.Method+" "+ri.Name]
			if !found {
				t.Fatalf("%s: no manifest route %s %s", pattern, ri.Method, ri.Name)
			}
			entry := ginRoute{method: ri.Method, path: ri.Name}
			if gr.facts.probe || !gr.facts.rootOwned ||
				slices.Contains(gr.facts.basicExempt, entry) != lr.BasicExempt || (gr.facts.basicExempt != nil) != lr.BasicExempt ||
				slices.Contains(gr.facts.ownAuth, entry) != lr.OwnAuth || (gr.facts.ownAuth != nil) != lr.OwnAuth {
				t.Errorf("%s (%s): facts %+v, want BasicExempt %v, OwnAuth %v, the \"/\" owner's", pattern, ri.Name, gr.facts, lr.BasicExempt, lr.OwnAuth)
			}
			wantExempt = lr.BasicExempt || lr.OwnAuth
		default:
			wantExempt = ri.Pattern == "/livez" || ri.Pattern == "/readyz"
			if gr.facts.probe != wantExempt || gr.facts.basicExempt != nil || gr.facts.ownAuth != nil || gr.facts.rootOwned {
				t.Errorf("%s: facts %+v, want probe %v only", pattern, gr.facts, wantExempt)
			}
		}
		if got := exempt(req); got != wantExempt {
			t.Errorf("%s %s: exempt = %v, want %v", ri.Method, target, got, wantExempt)
		}
	}
	if want := len(gcpdBasicRoutes()) + 4; n != want {
		t.Fatalf("checked %d routes, want %d", n, want)
	}
}

// TestBasicGate_FactsOnlyFromASuccessfulBuild: a failed Build records no
// facts, so the gate cannot be built over a router that serves nothing; a
// registration after it is still refused.
func TestBasicGate_FactsOnlyFromASuccessfulBuild(t *testing.T) {
	r := NewRouter(Options{}, Deps{})
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Infra("GET /livez", rbac.Public, ok)
	r.Infra("GET /livez", rbac.Public, ok) // a duplicate: Build fails
	if _, err := r.Build(); !errors.Is(err, ErrDuplicateRoute) {
		t.Fatalf("Build = %v, want ErrDuplicateRoute", err)
	}
	r.mu.Lock()
	_, err := r.basicGateRoutes()
	built, gm := r.built, r.gateMux
	r.mu.Unlock()
	if !errors.Is(err, errBasicGateInput) || built || gm != nil {
		t.Fatalf("after a failed Build: basicGateRoutes err = %v, built = %v, facts recorded = %v", err, built, gm != nil)
	}
	if err := r.Claim("batcave", "/batcave/"); !errors.Is(err, errBuilt) {
		t.Fatalf("Claim after a failed Build = %v, want errBuilt", err)
	}
}

// blockingPassword answers v; while hold is set, the first call takes its
// answer, signals entered and waits for hold to close before returning it.
type blockingPassword struct {
	mu      sync.Mutex
	v       secrets.Value
	hold    chan struct{}
	entered chan struct{}
	calls   int
}

func (b *blockingPassword) set(v secrets.Value) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.v = v
}

func (b *blockingPassword) BasicGatePassword(context.Context) (secrets.Value, error) {
	b.mu.Lock()
	v, hold, entered := b.v, b.hold, b.entered
	b.calls++
	first := b.calls == 1
	b.mu.Unlock()
	if hold != nil && first {
		close(entered)
		<-hold
	}
	return v, nil
}

// TestBasicGate_RequestNeverReportsPosture is the stale-posture race made
// deterministic: a request reads the password, a save clears it and the
// check reports basic_gate_no_password, and only then does the request
// finish. Its read was the old one, so it must not report: health keeps the
// check's posture.
func TestBasicGate_RequestNeverReportsPosture(t *testing.T) {
	rep := &lastReport{}
	pw := &blockingPassword{v: secrets.NewValue("Wayne-Manor"), hold: make(chan struct{}), entered: make(chan struct{})}
	g := mustGate(t, BasicGateOptions{Settings: settingsOf(true, ""), Password: pw, Health: rep}, func(*http.Request) bool { return false })
	done := make(chan *httptest.ResponseRecorder)
	go func() {
		done <- basicRequest(g.wrap(http.NotFoundHandler()), http.MethodGet, "/batcave", ":Wayne-Manor")
	}()
	<-pw.entered // the request holds the old password
	pw.set(secrets.Value{})
	if !g.checkPosture(context.Background()) {
		t.Fatal("cleared password: checkPosture = false")
	}
	close(pw.hold)
	if rec := <-done; refused(t, rec) {
		t.Fatal("the request that read the old password was refused")
	}
	last, _ := rep.last()
	if last.Reason != BasicGateNoPasswordToken || len(rep.got) != 1 {
		t.Fatalf("health after the stale request = %+v (%d reports), want the check's %s alone", last, len(rep.got), BasicGateNoPasswordToken)
	}
}

// TestBasicGate_CheckPostureOrdered: two checks never interleave. A check
// that read the old password reports before a later check can read at all,
// so the last report is the later check's.
func TestBasicGate_CheckPostureOrdered(t *testing.T) {
	rep := &lastReport{}
	pw := &blockingPassword{v: secrets.NewValue("Wayne-Manor"), hold: make(chan struct{}), entered: make(chan struct{})}
	g := mustGate(t, BasicGateOptions{Settings: settingsOf(true, ""), Password: pw, Health: rep}, func(*http.Request) bool { return false })
	first := make(chan bool)
	go func() { first <- g.checkPosture(context.Background()) }()
	<-pw.entered // the first check holds the old password
	pw.set(secrets.Value{})
	second := make(chan bool)
	go func() { second <- g.checkPosture(context.Background()) }()
	select {
	case <-second:
		t.Fatal("a second check finished while the first was still reading")
	case <-time.After(50 * time.Millisecond):
	}
	close(pw.hold)
	if <-first {
		t.Fatal("first check: noPassword = true, it read the old password")
	}
	if !<-second {
		t.Fatal("second check: noPassword = false, it read the cleared password")
	}
	rep.mu.Lock()
	defer rep.mu.Unlock()
	if len(rep.got) != 2 || rep.got[0].Status != health.StatusUp || rep.got[1].Reason != BasicGateNoPasswordToken {
		t.Fatalf("reports = %+v, want up then %s", rep.got, BasicGateNoPasswordToken)
	}
}

// TestBasicGate_PostureConcurrent runs requests and checks in parallel while
// the password changes (run it with -race): every request refuses or admits
// on its own read, and once the changes stop, one check leaves health on the
// final posture whatever ran before.
func TestBasicGate_PostureConcurrent(t *testing.T) {
	rep := &lastReport{}
	pw := &switchablePassword{v: secrets.NewValue("Wayne-Manor")}
	g := mustGate(t, BasicGateOptions{Settings: settingsOf(true, ""), Password: pw, Health: rep,
		Log: slog.New(slog.DiscardHandler)}, func(*http.Request) bool { return false })
	h := g.wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 200 {
				switch {
				case i%4 == 0 && j%10 == 0:
					if j%20 == 0 {
						pw.set(secrets.Value{}, nil)
					} else {
						pw.set(secrets.NewValue("Wayne-Manor"), nil)
					}
				case i%2 == 0:
					g.checkPosture(ctx)
				default:
					if rec := basicRequest(h, http.MethodGet, "/batcave", ":Wayne-Manor"); rec.Code != http.StatusOK && rec.Code != http.StatusUnauthorized {
						t.Errorf("status %d", rec.Code)
					}
				}
			}
		})
	}
	wg.Wait()
	for _, final := range []secrets.Value{{}, secrets.NewValue("Wayne-Manor")} {
		pw.set(final, nil)
		g.checkPosture(ctx)
		last, _ := rep.last()
		if want := final.IsZero(); (last.Reason == BasicGateNoPasswordToken) != want {
			t.Fatalf("final password zero = %v: health = %+v", want, last)
		}
	}
}

// TestBasicGate_CredentialDigests: the constant-time compares always see two
// SHA-256 digests, whatever the lengths presented and configured, and both
// are always made, so neither a length nor which credential failed leaks.
func TestBasicGate_CredentialDigests(t *testing.T) {
	_, exempt := gcpdBasicRouter(t, headBasicAssets)
	g := mustGate(t, BasicGateOptions{Settings: settingsOf(true, "alfred"), Password: fixedPassword{v: secrets.NewValue("Wayne-Manor")}}, exempt)
	var lens [][2]int
	equal := g.equal
	g.equal = func(x, y []byte) int {
		lens = append(lens, [2]int{len(x), len(y)})
		return equal(x, y)
	}
	h := g.wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	for _, c := range []struct {
		creds  string
		refuse bool
	}{
		{"alfred:Wayne-Manor", false},
		{"alfred:W", true},
		{"alfred:Wayne-Manor-and-the-whole-Batcave-beneath-it", true},
		{"a:Wayne-Manor", true},
		{"alfred-pennyworth-the-butler:Wayne-Manor", true},
		{":", true},
		{"bruce:Joker", true},
	} {
		lens = nil
		if got := refused(t, basicRequest(h, http.MethodGet, "/api/v1/cases/42", c.creds)); got != c.refuse {
			t.Errorf("%q: refused = %v, want %v", c.creds, got, c.refuse)
		}
		if len(lens) != 2 || lens[0] != [2]int{32, 32} || lens[1] != [2]int{32, 32} {
			t.Errorf("%q: compares saw lengths %v, want two compares of two 32-byte digests", c.creds, lens)
		}
	}
}

// TestBasicGate_SettingsReadBeforePassword: Apply commits a saved password
// before it publishes the settings, so the gate reads the settings first;
// the one mixed pair a save produces, the old username with the new
// password, admits only the new password (see BasicGateSettings).
func TestBasicGate_SettingsReadBeforePassword(t *testing.T) {
	var mu sync.Mutex
	var reads []string
	user := "alfred"
	pass := secrets.NewValue("Wayne-Manor")
	settings := func() BasicGateSettings {
		mu.Lock()
		defer mu.Unlock()
		reads = append(reads, "settings")
		return BasicGateSettings{Enabled: true, Username: user}
	}
	g := mustGate(t, BasicGateOptions{Settings: settings, Password: passwordFunc(func() (secrets.Value, error) {
		mu.Lock()
		defer mu.Unlock()
		reads = append(reads, "password")
		return pass, nil
	})}, func(*http.Request) bool { return false })
	h := g.wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	if refused(t, basicRequest(h, http.MethodGet, "/batcave", "alfred:Wayne-Manor")) {
		t.Fatal("the configured credentials were refused")
	}
	if !slices.Equal(reads, []string{"settings", "password"}) {
		t.Fatalf("reads = %v, want the settings before the password", reads)
	}
	// A save to oracle:Clocktower has committed, not yet published.
	mu.Lock()
	pass = secrets.NewValue("Clocktower")
	mu.Unlock()
	if !refused(t, basicRequest(h, http.MethodGet, "/batcave", "alfred:Wayne-Manor")) {
		t.Fatal("the replaced password was admitted during the save")
	}
	if refused(t, basicRequest(h, http.MethodGet, "/batcave", "alfred:Clocktower")) {
		t.Fatal("the new password with the still-published username was refused")
	}
}

// passwordFunc is a BasicGatePassword from a function.
type passwordFunc func() (secrets.Value, error)

func (f passwordFunc) BasicGatePassword(context.Context) (secrets.Value, error) { return f() }

// TestBasicGate_Realm: the challenge carries the configured realm, a neutral
// default when none is set, and a realm that would break the quoted-string
// is refused at build.
func TestBasicGate_Realm(t *testing.T) {
	pw := fixedPassword{v: secrets.NewValue("Wayne-Manor")}
	none := func(*http.Request) bool { return false }
	for realm, want := range map[string]string{"": `Basic realm="Restricted"`, "Gotham PD": `Basic realm="Gotham PD"`} {
		g, err := newBasicGate(BasicGateOptions{Settings: settingsOf(true, ""), Password: pw, Realm: realm}, none)
		if err != nil {
			t.Fatalf("realm %q: %v", realm, err)
		}
		rec := basicRequest(g.wrap(http.NotFoundHandler()), http.MethodGet, "/batcave", "")
		if got := rec.Header().Get("WWW-Authenticate"); rec.Code != http.StatusUnauthorized || got != want {
			t.Errorf("realm %q: %d %q, want 401 %q", realm, rec.Code, got, want)
		}
	}
	for _, realm := range []string{`Arkham"`, `Arkham\`, "Arkham\r\nX-Joker: 1", "Arkham\x7f"} {
		if _, err := newBasicGate(BasicGateOptions{Settings: settingsOf(true, ""), Password: pw, Realm: realm}, none); !errors.Is(err, errBasicGateInput) {
			t.Errorf("realm %q: err = %v, want errBasicGateInput", realm, err)
		}
	}
}
