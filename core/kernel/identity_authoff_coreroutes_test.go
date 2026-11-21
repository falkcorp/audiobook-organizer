// file: core/kernel/identity_authoff_coreroutes_test.go
// version: 0.1.0
// guid: 3df8ec9a-e647-4f9f-b1d5-e2b9c68678f0
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
)

// Batman names only. F4-08, the core-route half of F4-07's reach oracle (09
// section 9.2.6): the auth-disabled principal against every core route the
// identity boot mounts, loopback-only and remote-reachable, held to the
// expected table in identity_authoff_coreroutes_table_test.go.
//
// A real boot is Run, so stage 0 attributes each request to its peer (the
// trusted proxies and the client IP are read there; a boot and Handler() alone
// have none, see newProxiedFx). Every probe comes from its own source address,
// the way F4-07's recorder spreads its probes, so the RateAuth buckets never
// answer 429 in place of the route; a 429 or a 5xx is a failure, never a
// verdict.

// offPosture is one way an auth-off install can be reached.
type offPosture struct {
	name string
	// listen and proxies are core.http.listen and core.http.trusted_proxies;
	// header is core.http.client_ip_header, set so a trusted proxy's forwarded
	// client address is the one the rate limit and the audit see.
	listen, proxies, header string
	// allowRemote is auth.Options.DefaultDisabledAllowRemote, ABO's default
	// (true, SEC4-8); the loopback posture leaves it false, the secure default
	// the install boots under only because nothing remote can reach it.
	allowRemote bool
	// remote is auth.Posture.DisabledRemote (health's auth_disabled_remote);
	// reach is what the kernel tells auth about who can connect.
	remote bool
	reach  auth.Reach
}

var (
	offLoopback = offPosture{name: "loopback-only", listen: "127.0.0.1:0"}
	offRemote   = offPosture{name: "remote-reachable", listen: "192.0.2.5:8080", allowRemote: true, remote: true,
		reach: auth.Reach{NonLoopbackListener: true}}
	offProxied = offPosture{name: "behind-a-trusted-proxy", listen: "127.0.0.1:0", proxies: `["` + proxyPeer + `"]`,
		header: "X-Forwarded-For", allowRemote: true, remote: true, reach: auth.Reach{TrustedProxy: true}}

	offPostures = []offPosture{offLoopback, offRemote, offProxied}
)

// source attributes the n-th probe to its own client address.
func (p offPosture) source(n int, r *http.Request) {
	switch p.name {
	case offRemote.name:
		r.RemoteAddr = fmt.Sprintf("192.0.2.%d:4000", 10+n%200)
	case offProxied.name:
		r.RemoteAddr = proxyPeer + ":4000"
		r.Header.Set(p.header, fmt.Sprintf("192.0.2.%d", 10+n%200))
	default:
		r.RemoteAddr = fmt.Sprintf("127.0.%d.%d:4000", n/250, n%250+2)
	}
}

// Fixture permissions of the module "alfred": signal is an ordinary scope,
// vault is AuthRequired.
const (
	offSignal = rbac.Permission("alfred:signal")
	offVault  = rbac.Permission("alfred:vault")
)

// offConfig is mountConfig's module plus one route per guard and per scope
// class (offControlRows), booted with core.auth.mode disabled in posture p.
func offConfig(t *testing.T, p offPosture) (*swapHost, Config) {
	t.Helper()
	h := newSwapHost(t)
	c := h.envConfig(nil)
	c.IdentityBoot = true
	c.AuthOptions.OIDCCallbackBase = callbackBase
	c.AuthOptions.DefaultDisabledAllowRemote = p.allowRemote
	ok := func(context.Context, *httpapi.In[struct{}]) (mountOut, error) { return mountOut{OK: true}, nil }
	c.Modules = []module.Module{storageMod("alfred", func(r *module.Registrar) error {
		g := r.HTTP()
		httpapi.Handle(g, httpapi.Route{Method: http.MethodGet, Path: "/relay", Name: "getRelay", Perm: rbac.Need(offSignal)}, ok)
		httpapi.Handle(g, httpapi.Route{Method: http.MethodPost, Path: "/relay-lock", Name: "lockRelay", Perm: rbac.Need(offSignal),
			Guards: httpapi.Guards{Interactive: true}}, ok)
		httpapi.Handle(g, httpapi.Route{Method: http.MethodPost, Path: "/relay-session", Name: "sessionRelay", Perm: rbac.Need(offSignal),
			Guards: httpapi.Guards{Session: true}}, ok)
		httpapi.Handle(g, httpapi.Route{Method: http.MethodPost, Path: "/relay-owner", Name: "ownerRelay", Perm: rbac.Need(offSignal),
			Guards: httpapi.Guards{Owner: true}}, ok)
		httpapi.Handle(g, httpapi.Route{Method: http.MethodGet, Path: "/vault", Name: "getVault", Perm: rbac.Need(offVault)}, ok)
		return nil
	}, seedStep(1, "cave")).with(func(m *module.Manifest) {
		m.Permissions = []rbac.PermissionSpec{
			{Name: offSignal, Title: "Signal"},
			{Name: offVault, Title: "Vault", AuthRequired: true},
		}
		m.Roles = []rbac.RoleSeed{{Name: "relay-worker", Title: "Relay worker", Permissions: []rbac.Permission{offSignal}}}
		m.ImportedRoleIDs = []rbac.RoleName{"relay-worker"}
	})}
	withCore(&c, "core.auth.mode", "disabled", "core.http.listen", p.listen)
	if p.proxies != "" {
		withCore(&c, "core.http.trusted_proxies", p.proxies)
	}
	if p.header != "" {
		withCore(&c, "core.http.client_ip_header", p.header)
	}
	return h, c
}

// offFx is a Run kernel in auth-off mode with one user, one invite, one key and
// one session, so a route that takes an ID is probed with a real one and its
// handler's own check is reached, not a 404.
type offFx struct {
	k       *Kernel
	h       http.Handler
	posture offPosture
	n       int
	ids     map[string]string // user, invite, key, session, role, file, provider
	token   string            // the invite's raw token
}

func newOffFx(t *testing.T, p offPosture) *offFx {
	t.Helper()
	h, c := offConfig(t, p)
	rec := &recorder{}
	listening := make(chan http.Handler, 1)
	c.Notifier = notifier{rec}
	c.Exit = func(code int) { rec.add("exit:%d", code) }
	c.Listen = func(_ httpapi.ListenOptions, handler http.Handler, _ *slog.Logger) (Server, error) {
		listening <- handler
		return &fakeServer{rec: rec, stopped: make(chan struct{})}, nil
	}
	k := h.mustBoot(c)
	ctx, cancel := context.WithCancel(t.Context())
	done := run(ctx, k)
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	})
	var handler http.Handler
	select {
	case handler = <-listening:
	case err := <-done:
		t.Fatalf("Run ended before it listened: %v", err)
	}
	fx := &offFx{k: k, h: handler, posture: p, ids: map[string]string{"role": "relay-worker", "file": "core.json", "provider": "joker"}}

	// The fixture's records are written through auth.Service by an actor that
	// holds everything, as an importer or a CLI would; the auth-disabled
	// principal under test never writes them.
	admin := &auth.Principal{Method: auth.MethodSession, Perms: holdsAll{}, User: &auth.User{ID: "u-alfred", Username: "alfred"}}
	u, err := k.ident.svc.Auth.CreateUser(t.Context(), admin, auth.NewUser{
		Username: "lucius", Password: fixturePassword, Roles: []rbac.RoleName{"relay-worker"},
	})
	if err != nil {
		t.Fatal(err)
	}
	fx.ids["user"] = string(u.ID)
	owner := &auth.Principal{Method: auth.MethodSession, Perms: holdsAll{}, User: u}
	link, inv, err := k.ident.svc.Auth.CreateInvite(t.Context(), owner, auth.InviteSpec{Username: "dick", Role: "relay-worker", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	_, after, found := strings.Cut(link, "token=")
	if !found || after == "" {
		t.Fatalf("the invite link carries no token: %q", link)
	}
	fx.ids["invite"], fx.token = inv.ID, after
	_, key, err := k.ident.svc.Auth.CreateAPIKey(t.Context(), owner, auth.KeySpec{Owner: u.ID, Name: "batarang", Scopes: []rbac.Permission{offSignal}})
	if err != nil {
		t.Fatal(err)
	}
	fx.ids["key"] = key.ID
	if _, err := k.ident.svc.Auth.IssueSession(t.Context(), u, auth.OriginPassword, false, auth.ClientInfo{}); err != nil {
		t.Fatal(err)
	}
	for s, err := range k.ident.svc.Auth.Sessions(t.Context(), u.ID) {
		if err != nil {
			t.Fatal(err)
		}
		fx.ids["session"] = s.PublicID
	}
	return fx
}

// fill puts the fixture's real IDs into a route pattern. The order is the
// point: "/users/invites/{id}" and "/api-keys/{id}" before "/users/{id}".
func (fx *offFx) fill(pattern string) string {
	for _, r := range [][2]string{
		{"/api-keys/{id}", "/api-keys/" + fx.ids["key"]},
		{"/users/invites/{id}", "/users/invites/" + fx.ids["invite"]},
		{"/users/{id}", "/users/" + fx.ids["user"]},
		{"{publicID}", fx.ids["session"]},
		{"/roles/{name}", "/roles/" + fx.ids["role"]},
		{"{file}", fx.ids["file"]},
		{"{provider}", fx.ids["provider"]},
	} {
		pattern = strings.ReplaceAll(pattern, r[0], r[1])
	}
	return pattern
}

// offCell is what one probe answered: the status and, on an error, the
// problem code.
type offCell struct {
	status int
	code   string
}

func (c offCell) String() string {
	if c.code == "" {
		return fmt.Sprint(c.status)
	}
	return fmt.Sprintf("%d %s", c.status, c.code)
}

// denied is the brief's letter rule: 401 and 403 are a denial, any other status
// below 500 is an allow.
func (c offCell) denied() bool { return c.status == http.StatusUnauthorized || c.status == http.StatusForbidden }

// send makes one probe from its own source address.
func (fx *offFx) send(method, path, body string) offCell {
	fx.n++
	var rd io.Reader
	if body == "" && method != http.MethodGet {
		body = "{}"
	}
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	fx.posture.source(fx.n, req)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	fx.h.ServeHTTP(rec, req)
	c := offCell{status: rec.Code}
	if rec.Code >= 400 {
		var pb struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &pb)
		c.code = pb.Code
	}
	return c
}

// offProbe is one table row against its route, in one posture.
type offProbe struct {
	row  offRow
	info httpapi.RouteInfo
	path string
	cell offCell
	// derive is why the route must deny the auth-disabled principal whatever
	// the table says, from the route's own guards and the catalog: a guard
	// that needs more than the auth-disabled principal is, or a requirement
	// whose every scope is AuthRequired or Sensitive (05-rbac 4.11). Empty for
	// a route nothing forces, and for a variant.
	derive string
}

// offSweep is everything one posture's boot showed.
type offSweep struct {
	posture offPosture
	routes  []httpapi.RouteInfo
	probes  []offProbe
	state   auth.Posture
	reach   auth.Reach
	// statusDisabled is GET /auth/status's auth_disabled.
	statusDisabled bool
}

func routeKey(ri httpapi.RouteInfo) string { return ri.Method + " " + ri.Pattern }

// derivedDeny is the reason the auth-disabled principal must be refused on ri,
// from the route and the catalog alone, "" when nothing forces a refusal.
func derivedDeny(cat *rbac.Catalog, ri httpapi.RouteInfo) string {
	switch {
	case ri.Guards.Interactive:
		return "Guards.Interactive: the auth-disabled principal is non-interactive (05-rbac 4.11 (a))"
	case ri.Guards.Session:
		return "Guards.Session: the auth-disabled principal is not a user's own sign-in (05-authn 4.11)"
	case ri.Guards.Owner:
		return "Guards.Owner: the auth-disabled principal has no owner assurance (05-authn 4.8)"
	}
	ps := ri.Perm.Permissions()
	if len(ps) == 0 || cat == nil {
		return ""
	}
	for _, p := range ps {
		spec, found := cat.Lookup(p)
		if !found || !(spec.AuthRequired || spec.Sensitive) {
			return ""
		}
	}
	return fmt.Sprintf("every scope of the requirement is AuthRequired or Sensitive (%v): 05-rbac 4.11's auth-off evaluation rule refuses it", ps)
}

// sweep probes every row whose route is mounted and records the posture.
func (fx *offFx) sweep(t *testing.T) *offSweep {
	t.Helper()
	sw := &offSweep{posture: fx.posture, reach: fx.k.reach(), state: fx.k.ident.svc.Auth.Posture()}
	byKey := map[string]httpapi.RouteInfo{}
	for ri := range fx.k.Routes() {
		sw.routes = append(sw.routes, ri)
		byKey[routeKey(ri)] = ri
	}
	cat := fx.k.ident.svc.RBAC.Catalog()
	for _, row := range slices.Concat(offCoreRows, offControlRows) {
		ri, mounted := byKey[row.Route]
		if !mounted {
			continue
		}
		body := strings.ReplaceAll(row.Body, "@invite-token@", fx.token)
		p := offProbe{row: row, info: ri, path: fx.fill(ri.Pattern)}
		p.cell = fx.send(ri.Method, p.path, body)
		if row.Name == "" {
			p.derive = derivedDeny(cat, ri)
		}
		sw.probes = append(sw.probes, p)
	}
	st := fx.send(http.MethodGet, "/api/core/v1/auth/status", "")
	sw.statusDisabled = st.status == http.StatusOK
	if st.status == http.StatusOK {
		// The body is read by a second request: send returns only the cell.
		fx.n++
		req := httptest.NewRequest(http.MethodGet, "/api/core/v1/auth/status", nil)
		fx.posture.source(fx.n, req)
		rec := httptest.NewRecorder()
		fx.h.ServeHTTP(rec, req)
		var body struct {
			AuthDisabled bool `json:"auth_disabled"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		sw.statusDisabled = body.AuthDisabled
	}
	return sw
}

// The sweeps are taken once per test binary run: a boot per posture, shared by
// the tests below. The data is plain values, so it outlives the kernels, which
// close with the test that took it.
var (
	offSweepOnce sync.Once
	offSweeps    []*offSweep
	offSweepErr  string
)

// sweeps returns the three postures' sweeps, taking them on first use.
func sweeps(t *testing.T) []*offSweep {
	t.Helper()
	offSweepOnce.Do(func() {
		for _, p := range offPostures {
			ok := t.Run("boot "+p.name, func(t *testing.T) {
				offSweeps = append(offSweeps, newOffFx(t, p).sweep(t))
			})
			if !ok {
				offSweepErr = "the " + p.name + " boot or sweep failed"
				return
			}
		}
	})
	if offSweepErr != "" {
		t.Fatal(offSweepErr)
	}
	return offSweeps
}

// ---- the table ------------------------------------------------------------

// TestAuthOffCoreRoutes_EveryCoreRouteHasARow: the table cannot go stale. A
// core route the router lists with no row fails by name, so does a row whose
// route is not mounted, a row with no clause, a route listed twice, and a
// deny or handler row with no code.
func TestAuthOffCoreRoutes_EveryCoreRouteHasARow(t *testing.T) {
	for _, sw := range sweeps(t) {
		have := map[string]bool{}
		for _, r := range slices.Concat(offCoreRows, offControlRows) {
			have[r.Route] = true
		}
		for _, ri := range sw.routes {
			if (ri.Owner == "core" || ri.Owner == "alfred") && !have[routeKey(ri)] {
				t.Errorf("[%s] route %q (owner %s, perm %v, guards %+v) has no row in the expected table: add one citing the spec clause that gives the auth-disabled principal its verdict",
					sw.posture.name, routeKey(ri), ri.Owner, ri.Perm, ri.Guards)
			}
		}
	}
	mounted := map[string]string{}
	for _, ri := range sweeps(t)[0].routes {
		mounted[routeKey(ri)] = ri.Owner
	}
	seen := map[string]bool{}
	for _, r := range slices.Concat(offCoreRows, offControlRows) {
		if r.Clause == "" {
			t.Errorf("row %q cites no spec clause", r.key())
		}
		if seen[r.key()] {
			t.Errorf("row %q is listed twice", r.key())
		}
		seen[r.key()] = true
		if _, ok := mounted[r.Route]; !ok {
			t.Errorf("row %q names a route the identity boot does not mount", r.key())
		}
		if r.Want == 0 {
			t.Errorf("row %q has no verdict", r.key())
		}
		if (r.Want == offDeny || r.Want == offHandler) && r.Code == "" {
			t.Errorf("row %q is a refusal with no problem code", r.key())
		}
		if r.Want == offAllow && r.Code != "" {
			t.Errorf("row %q allows and names a refusal code %q", r.key(), r.Code)
		}
		if r.Head != "" && r.Head != "A" && r.Head != "D" {
			t.Errorf("row %q has HEAD letter %q, want A or D", r.key(), r.Head)
		}
		if r.Head == "A" && r.Want == offDeny && r.Tightens == "" {
			t.Errorf("row %q denies where HEAD allowed (%s) and names no tightening clause", r.key(), r.HeadRoute)
		}
	}
	// Core's routes are the rows; the module's are the controls.
	for _, ri := range sweeps(t)[0].routes {
		if ri.Owner == "core" && !slices.ContainsFunc(offCoreRows, func(r offRow) bool { return r.Route == routeKey(ri) }) {
			t.Errorf("core route %q is in the controls or nowhere, not in the core table", routeKey(ri))
		}
	}
}

// offMatches reports whether the probe's answer is the one its row says.
func offMatches(p offProbe) bool {
	c := p.cell
	switch p.row.Want {
	case offAllow:
		return !c.denied() && c.status < http.StatusInternalServerError && c.status != http.StatusTooManyRequests
	case offDeny, offHandler:
		return c.denied() && c.code == p.row.Code
	}
	return false
}

// TestAuthOffCoreRoutes_VerdictsMatchTheSpecTable probes every core route as
// the auth-disabled principal, loopback-only, remote-reachable and behind a
// trusted proxy, and holds each answer to its row. A route whose answer is no
// row's fails by name with the clause it was expected to follow, a 5xx or a 429
// fails as a recorder failure, and a "deny" is judged by status and problem code
// together, so a refusal from the wrong layer fails too.
func TestAuthOffCoreRoutes_VerdictsMatchTheSpecTable(t *testing.T) {
	counts := map[offWant]int{}
	for _, sw := range sweeps(t) {
		for _, p := range sw.probes {
			c := p.cell
			if c.status >= http.StatusInternalServerError || c.status == http.StatusTooManyRequests {
				t.Errorf("[%s] %s %s answered %v: a 5xx or a 429 is a recorder failure, never a verdict", sw.posture.name, p.row.key(), p.path, c)
				continue
			}
			if !offMatches(p) {
				t.Errorf("[%s] %s %s answered %v, but the table expects %s%s (%s)", sw.posture.name, p.row.key(), p.path, c,
					p.row.Want, map[bool]string{true: " " + p.row.Code, false: ""}[p.row.Code != ""], p.row.Clause)
			}
			if sw.posture.name == offRemote.name {
				counts[p.row.Want]++
			}
		}
		if len(sw.probes) != len(offCoreRows)+len(offControlRows) {
			t.Errorf("[%s] %d probes for %d rows: a row's route is not mounted", sw.posture.name, len(sw.probes), len(offCoreRows)+len(offControlRows))
		}
	}
	t.Logf("remote-reachable cells: %d allow, %d deny, %d handler-refusal", counts[offAllow], counts[offDeny], counts[offHandler])
}

// TestAuthOffCoreRoutes_GuardedAndAuthRequiredRoutesAreRefused is the table's
// cross-check, derived from the routes' own declarations and the catalog, not
// from any row: the auth-disabled principal never reaches a Guards.Interactive,
// Guards.Session or Guards.Owner route (it cannot change credentials, has no
// user and no owner assurance), and never a route whose every required scope is
// AuthRequired or Sensitive (core:admin, core:audit.view, core:system.manage,
// core:backup.restore, books:admin.debug). It holds in every posture.
func TestAuthOffCoreRoutes_GuardedAndAuthRequiredRoutesAreRefused(t *testing.T) {
	derived := 0
	for _, sw := range sweeps(t) {
		for _, p := range sw.probes {
			if p.derive == "" {
				continue
			}
			if sw.posture.name == offRemote.name {
				derived++
			}
			if !p.cell.denied() {
				t.Errorf("[%s] STOP: %s %s answered %v and admits the auth-disabled principal, but %s", sw.posture.name, p.row.key(), p.path, p.cell, p.derive)
			}
		}
	}
	if derived < 20 {
		t.Errorf("only %d routes were forced to deny by their own guards or scopes: the derivation lost its inputs", derived)
	}
	t.Logf("%d routes forced to deny by their guards or AuthRequired scopes", derived)
}

// ---- remote against loopback ------------------------------------------------

// TestAuthOffCoreRoutes_RemoteReachableMatchesLoopback: no route's verdict
// depends on the install being remote-reachable. 05-authn 4.11 and 05-rbac 4.11
// (a)-(d) tighten auth-off for every install alike; the two differ in the
// start-up refusal and the health posture only (the next two tests). Any cell
// that differs between postures fails by name.
func TestAuthOffCoreRoutes_RemoteReachableMatchesLoopback(t *testing.T) {
	all := sweeps(t)
	base := all[0]
	for _, sw := range all[1:] {
		if len(sw.probes) != len(base.probes) {
			t.Fatalf("[%s] %d probes, [%s] %d", base.posture.name, len(base.probes), sw.posture.name, len(sw.probes))
		}
		for i, p := range sw.probes {
			if q := base.probes[i]; p.row.key() != q.row.key() || p.cell != q.cell {
				t.Errorf("%s answered %v when %s, %v when %s: a route's verdict must not depend on who can reach the install",
					p.row.key(), q.cell, base.posture.name, p.cell, sw.posture.name)
			}
		}
	}
}

// TestAuthOffCoreRoutes_PostureFollowsReach: the kernel tells auth who can
// connect (core.http.listen and the trusted proxies), and auth reports
// DisabledRemote, health's auth_disabled_remote, exactly when a remote caller
// can reach the install: never on a loopback listener with no proxy, always on
// a non-loopback listener or behind a trusted proxy. The posture is Disabled in
// all three, and the public status route says so.
func TestAuthOffCoreRoutes_PostureFollowsReach(t *testing.T) {
	for _, sw := range sweeps(t) {
		if sw.reach != sw.posture.reach {
			t.Errorf("[%s] the kernel reports reach %+v, want %+v", sw.posture.name, sw.reach, sw.posture.reach)
		}
		want := auth.Posture{Disabled: true, DisabledRemote: sw.posture.remote}
		if sw.state != want {
			t.Errorf("[%s] auth posture %+v, want %+v (DisabledRemote is what health reports as auth_disabled_remote)", sw.posture.name, sw.state, want)
		}
		if !sw.statusDisabled {
			t.Errorf("[%s] GET /auth/status does not report auth_disabled", sw.posture.name)
		}
	}
}

// TestAuthOffCoreRoutes_RemoteWithoutAllowRefusesToBoot: auth off boots only
// where no remote caller can reach it, unless core.auth.disabled_allow_remote
// (here, the app's default) says otherwise (05-authn 4.11 bullet 4, SEC-30):
// a non-loopback listener, an unspecified host and a trusted proxy in front of a
// loopback listener are each refused with auth.ErrAuthDisabledRemote.
func TestAuthOffCoreRoutes_RemoteWithoutAllowRefusesToBoot(t *testing.T) {
	for _, c := range []struct {
		name            string
		listen, proxies string
		allow           bool
		refused         bool
	}{
		{"loopback listener, no proxy, no allow", "127.0.0.1:0", "", false, false},
		{"localhost listener, no proxy, no allow", "localhost:0", "", false, false},
		{"non-loopback listener, no allow", "192.0.2.5:8080", "", false, true},
		{"unspecified host, no allow", ":8080", "", false, true},
		{"loopback listener behind a trusted proxy, no allow", "127.0.0.1:0", `["` + proxyPeer + `"]`, false, true},
		{"non-loopback listener, allowed", "192.0.2.5:8080", "", true, false},
		{"loopback listener behind a trusted proxy, allowed", "127.0.0.1:0", `["` + proxyPeer + `"]`, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, cfg := offConfig(t, offPosture{name: c.name, listen: c.listen, proxies: c.proxies, allowRemote: c.allow})
			k, err := h.boot(cfg)
			switch {
			case c.refused && !errors.Is(err, auth.ErrAuthDisabledRemote):
				t.Fatalf("boot = %v, want auth.ErrAuthDisabledRemote", err)
			case !c.refused && err != nil:
				t.Fatalf("boot = %v, want it to start", err)
			case !c.refused && k == nil:
				t.Fatal("no kernel")
			}
		})
	}
}

// ---- against HEAD -------------------------------------------------------------

// TestAuthOffCoreRoutes_NeverWiderThanHEAD: where R0 recorded the auth-off
// principal's verdict on the equivalent route (internal/books/rbacreach/
// verdicts.json, principal "authoff"), core never admits what HEAD refused, and
// every route core now refuses that HEAD allowed names its tightening clause.
// The cells that differ are logged: they are the report's HEAD comparison.
func TestAuthOffCoreRoutes_NeverWiderThanHEAD(t *testing.T) {
	var same, tightened, beyond int
	var lines []string
	for _, sw := range sweeps(t) {
		for _, p := range sw.probes {
			if p.row.Head == "" || p.row.Name != "" {
				continue
			}
			reaches := !p.cell.denied() || p.row.Want == offHandler
			switch {
			case p.row.Head == "D" && reaches:
				t.Errorf("STOP: [%s] %s answered %v and admits the auth-disabled principal where HEAD refused it (%s: D)", sw.posture.name, p.row.key(), p.cell, p.row.HeadRoute)
			case p.row.Head == "A" && !reaches:
				if p.row.Tightens == "" {
					t.Errorf("[%s] %s answered %v where HEAD allowed (%s: A) and the row names no tightening clause", sw.posture.name, p.row.key(), p.cell, p.row.HeadRoute)
				}
				if sw.posture.name == offRemote.name {
					tightened++
					if p.row.Beyond {
						beyond++
					}
					lines = append(lines, fmt.Sprintf("tightened: %s (HEAD %s: A) now %v", p.row.key(), p.row.HeadRoute, p.cell))
				}
			default:
				if sw.posture.name == offRemote.name {
					same++
				}
			}
		}
	}
	sort.Strings(lines)
	for _, l := range lines {
		t.Log(l)
	}
	t.Logf("HEAD comparison: %d routes agree, %d tightened (%d of them beyond the spec text), 0 widened", same, tightened, beyond)
}
