// file: core/internal/integrationhttp/stage13_test.go
// version: 0.1.0
// guid: 5e0a7d31-94c2-4b68-a1f3-7d2c68b0e945
// last-edited: 2026-11-22

package integrationhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/internal/authz"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// The stage-13 harness (F4-08): every cell serves one request to the real
// routes through the router's own stages 12 and 13 (Router.SetAuth), over a
// booted Registry, as a caller whose checker is the one rbac's compiler
// builds. A refusal is therefore the router's, and the handlers' own
// backstops (guard, guardManage) cannot stand in for it: stage 13's 401 is
// authentication_required where the backstop's is unauthenticated, and its
// 403 names required_permission and leaves a core.permission.deny row where
// the backstop's names neither.

// s13Caller is who a cell calls as. The zero value is no principal at all.
type s13Caller struct {
	name      string
	anonymous bool
	scopes    []rbac.Permission
}

var (
	anon = s13Caller{name: "no principal", anonymous: true}
	// noScope is a signed-in session whose roles carry no integration scope.
	noScope = s13Caller{name: "no integration scope"}
	// readHolder holds core:integrations.read alone: the tightest negative for a
	// write, since it is the scope the manage scope implies.
	readHolder = s13Caller{name: "read only", scopes: []rbac.Permission{permRead}}
	// manageHolder holds core:integrations.manage, which implies read.
	manageHolder = s13Caller{name: "manage", scopes: []rbac.Permission{integrations.ManagePerm}}
)

// principal is the caller's principal as stage 12 binds it: an interactive
// session over the caller's role patterns, compiled against core's catalog.
func (c s13Caller) principal(cat *rbac.Catalog) *auth.Principal {
	return &auth.Principal{
		User: &auth.User{ID: "01GOTHAMBRUCEWAYNE0000000", Username: "bruce.wayne"}, Method: auth.MethodSession,
		CredentialID: "cred-gotham",
		Perms:        authz.Compile(rbac.CompileInput{Interactive: true, RolePatterns: slices.Clone(c.scopes)}, cat),
	}
}

// s13Stage12 stands in for auth.Service's stage 12: it binds the caller's
// principal with auth.WithPrincipal, as stage 12 does for a valid credential,
// and binds nothing for an anonymous request.
type s13Stage12 struct{ p *auth.Principal }

func (s s13Stage12) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.p != nil {
				r = r.WithContext(auth.WithPrincipal(r.Context(), s.p))
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s13Stage12) SessionCookie(auth.ClientInfo, string, time.Time) *http.Cookie {
	return &http.Cookie{Name: "hello_session", Path: "/", MaxAge: -1}
}

// s13Denials keeps the core.permission.deny events stage 13 emits, which is
// all it calls on its audit logger.
type s13Denials struct {
	mu     sync.Mutex
	events []audit.Event
}

func (d *s13Denials) Record(context.Context, audit.Event) error {
	panic("stage 13 emits its refusals, it never records one")
}

func (d *s13Denials) RecordIn(context.Context, *storage.Batch, audit.Event) error {
	panic("stage 13 emits its refusals, it never records one in a batch")
}

func (d *s13Denials) Emit(_ context.Context, e audit.Event) {
	d.mu.Lock()
	d.events = append(d.events, e)
	d.mu.Unlock()
}

func (d *s13Denials) all() []audit.Event {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.events)
}

// s13Env is an env with the seeded instance, a probe counter on every health
// check, and a snapshot of what a refused request must leave alone.
type s13Env struct {
	*env
	ref     integrations.Ref
	probed  int64    // the health probes made before the request
	actions []string // the audit actions committed before the request
	sent    int      // the outbound requests made before the request
}

// newS13Env boots an env and creates gcpd-roof (a notify instance holding the
// secret alfred-1) through the handler, as an admin. The seed's own audit rows
// and sends are in the snapshot, so a cell compares what its request adds.
func newS13Env(t *testing.T) *s13Env {
	t.Helper()
	e := &s13Env{env: newEnv(t), ref: integrations.Ref{Kind: integrations.KindNotify, Instance: roofID}}
	e.create(t, adminCtx(), kindNotify, signalBody(roofID, "http://192.0.2.20/", "alfred-1"))
	e.snapshot(t)
	return e
}

func (e *s13Env) snapshot(t *testing.T) {
	t.Helper()
	e.actions = e.env.actions(t)
	e.sent = len(e.out.Sent())
	e.probed = e.probeCount()
}

// probeCount is every health probe the fake adapter's instances have run (a
// test of an instance without a Tester probes as well).
func (e *s13Env) probeCount() int64 {
	var n int64
	for _, i := range e.fake.Built() {
		n += i.Probes()
	}
	return n
}

// probes is the health probes run since the snapshot.
func (e *s13Env) probes() int64 { return e.probeCount() - e.probed }

// added is the audit actions committed since the snapshot, sorted.
func (e *s13Env) added(t *testing.T) []string {
	t.Helper()
	all := e.env.actions(t)
	return slices.Sorted(slices.Values(all[len(e.actions):]))
}

// s13Result is one cell's answer: the response, the deny rows the router
// emitted, and the env it ran over.
type s13Result struct {
	rec     *httptest.ResponseRecorder
	denials []audit.Event
	env     *s13Env
}

// serve sends method path (with body JSON-encoded when non-nil) to the real
// routes, built into a router whose stages 12 and 13 are wired as a booted core
// wires them, as caller c.
func (e *s13Env) serve(t *testing.T, method, path string, body any, c s13Caller) s13Result {
	t.Helper()
	cat, err := rbac.NewCatalog(rbac.CoreScopes()...)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	var p *auth.Principal
	if !c.anonymous {
		p = c.principal(cat)
	}
	deny := &s13Denials{}
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: slog.New(slog.DiscardHandler)})
	if err := r.SetAuth(httpapi.AuthWiring{
		Service: s13Stage12{p: p}, Catalog: cat, Audit: deny, Log: slog.New(slog.DiscardHandler),
		OwnerRequired: func() bool { return true },
	}); err != nil {
		t.Fatalf("SetAuth: %v", err)
	}
	Register(r.CoreGroup(), e.reg, e.holds, e.log)
	h, err := r.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var rdr *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, "/api/core/v1"+path, rdr)
	req.RemoteAddr = "192.0.2.10:4000"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return s13Result{rec: rec, denials: deny.all(), env: e}
}

// s13Want is what a cell expects of a refusal: its status, problem code and,
// for a permission_denied, the scope it names.
type s13Want struct {
	status   int
	code     string
	required string
}

// unauthenticatedWant is stage 13's answer to a request stage 12 bound nobody to.
var unauthenticatedWant = s13Want{http.StatusUnauthorized, "authentication_required", ""}

// lackingWant is the 403 of a caller who does not hold scope.
func lackingWant(scope rbac.Permission) s13Want {
	return s13Want{http.StatusForbidden, "permission_denied", string(scope)}
}

// wantRefusal judges a refused cell: the status, the problem code and the scope
// it names (stage 13's own answers, which the handlers' backstops do not
// give), exactly one core.permission.deny row on the route for a 403 and none
// for a 401, and a request that changed nothing and reached nothing.
func (e *s13Env) wantRefusal(t *testing.T, res s13Result, want s13Want) {
	t.Helper()
	rec := res.rec
	if rec.Code != want.status {
		t.Fatalf("status = %d (%s), want %d %s", rec.Code, rec.Body.String(), want.status, want.code)
	}
	var prob struct {
		Code               string `json:"code"`
		RequiredPermission string `json:"required_permission"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &prob); err != nil {
		t.Fatalf("problem body %q: %v", rec.Body.String(), err)
	}
	if prob.Code != want.code || prob.RequiredPermission != want.required {
		t.Errorf("problem = %q (required_permission %q), want %q (%q)", prob.Code, prob.RequiredPermission, want.code, want.required)
	}
	switch want.status {
	case http.StatusUnauthorized:
		if len(res.denials) != 0 {
			t.Errorf("a 401 emitted deny rows %+v", res.denials)
		}
	case http.StatusForbidden:
		if len(res.denials) != 1 {
			t.Fatalf("a 403 emitted %d deny rows %+v, want exactly one", len(res.denials), res.denials)
		}
		d := res.denials[0]
		if d.Action != "core.permission.deny" || d.Outcome != audit.Denied || d.Reason != want.code ||
			d.Permission != want.required || d.Route == "" {
			t.Errorf("deny row = %+v, want core.permission.deny / denied / reason %q / permission %q on a named route", d, want.code, want.required)
		}
	}
	e.wantUntouched(t)
}

// wantUntouched is what a refused request must leave alone: no committed audit
// row, no outbound request, no probe, no hold read or released, and the seeded
// instance as the seed made it (the instance a create would add absent).
func (e *s13Env) wantUntouched(t *testing.T) {
	t.Helper()
	if got := e.added(t); len(got) != 0 {
		t.Errorf("a refused request committed audit rows %v", got)
	}
	if n := len(e.out.Sent()) - e.sent; n != 0 {
		t.Errorf("a refused request made %d outbound requests", n)
	}
	if n := e.probes(); n != 0 {
		t.Errorf("a refused request probed %d times", n)
	}
	if n := e.holds.heldScopeCalls(); n != 0 {
		t.Errorf("a refused request read holds %d times", n)
	}
	if got := e.holds.releaseCalls(); len(got) != 0 {
		t.Errorf("a refused request released holds %v", got)
	}
	cfg, ok := e.reg.Config(e.ref)
	if !ok || cfg.Version != 1 {
		t.Errorf("the seeded instance = %+v, present %v, want version 1", cfg, ok)
	}
	if got := e.secretValue(t, kindNotify, roofID, "password"); got != "alfred-1" {
		t.Errorf("the seeded secret = %q, want it unchanged", got)
	}
	if _, ok := e.reg.Config(integrations.Ref{Kind: integrations.KindNotify, Instance: annexID}); ok {
		t.Errorf("a refused request created %s", annexID)
	}
	e.holds.mu.Lock()
	defer e.holds.mu.Unlock()
	if len(e.holds.released_) != 0 {
		t.Errorf("a refused request ended holds %v", e.holds.released_)
	}
}

// annexID is the instance the create cell adds.
const annexID = "gcpd-annex"

// s13Route is one route's stage-13 matrix. A caller that holds the route's
// scope gets success, with its shape and the audit actions the spec requires of
// it; every other caller is refused by stage 13.
type s13Route struct {
	name, method, path string
	body               func() any
	perm               rbac.Permission
	// setup runs after the seed and before the snapshot.
	setup func(t *testing.T, e *s13Env)
	// holders are the callers that hold perm (others are refused); success
	// judges what they get.
	holders []s13Caller
	success func(t *testing.T, e *s13Env, res s13Result)
}

// run runs the matrix: anonymous is 401, every caller outside holders is 403
// naming perm, every holder gets success. Each cell has its own env.
func (r s13Route) run(t *testing.T) {
	t.Helper()
	cell := func(c s13Caller, check func(t *testing.T, e *s13Env, res s13Result)) {
		t.Run(c.name, func(t *testing.T) {
			e := newS13Env(t)
			if r.setup != nil {
				r.setup(t, e)
				e.snapshot(t)
			}
			var body any
			if r.body != nil {
				body = r.body()
			}
			check(t, e, e.serve(t, r.method, r.path, body, c))
		})
	}
	cell(anon, func(t *testing.T, e *s13Env, res s13Result) { e.wantRefusal(t, res, unauthenticatedWant) })
	for _, c := range []s13Caller{noScope, readHolder, manageHolder} {
		if slices.ContainsFunc(r.holders, func(h s13Caller) bool { return h.name == c.name }) {
			cell(c, r.success)
			continue
		}
		cell(c, func(t *testing.T, e *s13Env, res s13Result) { e.wantRefusal(t, res, lackingWant(r.perm)) })
	}
}
