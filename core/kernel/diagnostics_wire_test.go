// file: core/kernel/diagnostics_wire_test.go
// version: 0.1.0
// guid: d350b53d-f039-45ff-9317-8e81563652c3
// last-edited: 2026-11-20

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/health/diagnostics"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/internal/authz"
	"github.com/falkcorp/mediacore/internal/healthhttp"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// dwUsers is a user table.
type dwUsers map[auth.UserID]*auth.User

func (u dwUsers) User(_ context.Context, id auth.UserID) (*auth.User, error) {
	if x, ok := u[id]; ok {
		return x, nil
	}
	return nil, auth.ErrNotFound
}

// dwKeys is a key table. It answers APIKeys with every key it holds whatever
// the owner asked for, so a key owned by someone else reaches ViewerFor's own
// owner check instead of being hidden by the store's scoping.
type dwKeys struct {
	keys []*auth.APIKey
	err  error
	// errThenKeys yields err first and then still yields the keys, as a scan
	// does when it fails on one record and the caller keeps ranging.
	errThenKeys bool
}

func (k *dwKeys) APIKeys(context.Context, auth.UserID) iter.Seq2[*auth.APIKey, error] {
	return func(yield func(*auth.APIKey, error) bool) {
		if k.err != nil {
			if !yield(nil, k.err) || !k.errThenKeys {
				return
			}
		}
		for _, x := range k.keys {
			if !yield(x, nil) {
				return
			}
		}
	}
}

// dwKey is an active key of owner that expires in a day.
func dwKey(id, owner string, scopes ...rbac.Permission) *auth.APIKey {
	return &auth.APIKey{ID: id, Owner: auth.UserID(owner), Name: "batphone", Scopes: scopes, Status: auth.KeyActive,
		CreatedAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(24 * time.Hour)}
}

// dwCompiler is rbac.Service.Compile's shape over the real authz.Compile and
// the core catalog: it resolves role names to patterns, then compiles.
type dwCompiler struct {
	t     *testing.T
	cat   *rbac.Catalog
	roles map[rbac.RoleName][]rbac.Permission
	seen  []rbac.CheckerInput
}

func newDWCompiler(t *testing.T) *dwCompiler {
	t.Helper()
	cat, err := rbac.NewCatalog(rbac.CoreScopes()...)
	if err != nil {
		t.Fatal(err)
	}
	return &dwCompiler{t: t, cat: cat, roles: map[rbac.RoleName][]rbac.Permission{
		"admin":  {"core:admin"},
		"viewer": {"core:metrics.read", "core:self"},
	}}
}

func (c *dwCompiler) Compile(in rbac.CheckerInput) rbac.Checker {
	c.seen = append(c.seen, in)
	var pats []rbac.Permission
	for _, r := range in.Roles {
		pats = append(pats, c.roles[r]...)
	}
	return authz.Compile(rbac.CompileInput{CheckerInput: in, RolePatterns: pats}, c.cat)
}

func dwUser(id string, status auth.UserStatus, roles ...rbac.RoleName) *auth.User {
	return &auth.User{ID: auth.UserID(id), Username: "user." + id, Status: status, Roles: roles}
}

// TestDiagnosticsViewerFor_InteractiveOnlyForSession is F8-03 obligation 4
// over the real checker: only a "session" actor gets the interactive checker,
// so an administrator's API key (and a delegated session, the auth-disabled
// principal, or a method nobody classified) is refused the Sensitive
// core:backup.restore that the same administrator's session is admitted to.
// The non-Sensitive core:system.manage is admitted either way.
func TestDiagnosticsViewerFor_InteractiveOnlyForSession(t *testing.T) {
	comp := newDWCompiler(t)
	keyScopes := []rbac.Permission{"core:system.manage", "core:backup.restore"}
	vf := diagnosticsViewerFor(dwUsers{"bruce": dwUser("bruce", auth.StatusActive, "admin")},
		&dwKeys{keys: []*auth.APIKey{dwKey("key-batphone", "bruce", keyScopes...)}}, comp)
	for _, c := range []struct {
		method      string
		credential  string
		interactive bool
	}{
		{"session", "", true},
		{"token", "key-batphone", false},
		{"books.abs", "key-batphone", false}, // a compat surface's key principal: key-bound by its id
		{"session_delegated", "", false},
		{"auth_disabled", "", false},
		{"system", "", false},
	} {
		t.Run("method="+c.method+"/credential="+c.credential, func(t *testing.T) {
			comp.seen = nil
			v, err := vf(t.Context(), tasks.Actor{UserID: "bruce", AuthMethod: c.method, CredentialID: c.credential})
			if err != nil {
				t.Fatal(err)
			}
			if v.ActorID != "bruce" {
				t.Errorf("ActorID = %q, want bruce", v.ActorID)
			}
			if got := v.Can("core:backup.restore"); got != c.interactive {
				t.Errorf("Can(core:backup.restore) = %v, want %v", got, c.interactive)
			}
			if !v.Can("core:system.manage") {
				t.Error("an administrator lost core:system.manage")
			}
			wantScopes := []rbac.Permission(nil)
			if c.credential != "" {
				wantScopes = keyScopes // the key's own scopes, and only a key-bound actor carries any
			}
			if len(comp.seen) != 1 || comp.seen[0].Interactive != c.interactive || !slices.Equal(comp.seen[0].Scopes, wantScopes) ||
				(wantScopes == nil) != (comp.seen[0].Scopes == nil) || !slices.Equal(comp.seen[0].Roles, []rbac.RoleName{"admin"}) {
				t.Errorf("compiled %+v, want the user's roles, scopes %v and Interactive=%v", comp.seen, wantScopes, c.interactive)
			}
		})
	}
}

// The roles are the user's current ones, read on every call, and an actor
// that is not an active person fails closed.
func TestDiagnosticsViewerFor_LiveRolesAndRefusals(t *testing.T) {
	users := dwUsers{
		"alfred":  dwUser("alfred", auth.StatusActive, "viewer"),
		"harvey":  dwUser("harvey", auth.StatusDisabled, "admin"),
		"selina":  dwUser("selina", auth.StatusPending, "admin"),
		"_system": dwUser("_system", auth.StatusSystem, "admin"),
	}
	vf := diagnosticsViewerFor(users, &dwKeys{}, newDWCompiler(t))
	session := func(id string) tasks.Actor { return tasks.Actor{UserID: id, AuthMethod: "session"} }

	v, err := vf(t.Context(), session("alfred"))
	if err != nil || v.Can("core:system.manage") || !v.Can("core:metrics.read") {
		t.Fatalf("viewer alfred: err %v, manage %v, metrics %v", err, err == nil && v.Can("core:system.manage"), err == nil && v.Can("core:metrics.read"))
	}
	users["alfred"] = dwUser("alfred", auth.StatusActive, "admin")
	if v, err := vf(t.Context(), session("alfred")); err != nil || !v.Can("core:system.manage") {
		t.Fatalf("promoted alfred: err %v, want core:system.manage now", err)
	}

	for name, actor := range map[string]tasks.Actor{
		"system actor":   {AuthMethod: "system", System: "core"},
		"unknown user":   session("joker"),
		"disabled user":  session("harvey"),
		"pending user":   session("selina"),
		"attribution id": session("_system"),
	} {
		if _, err := vf(t.Context(), actor); !errors.Is(err, errNoDiagnosticsViewer) {
			t.Errorf("%s: err = %v, want errNoDiagnosticsViewer", name, err)
		}
	}
	if diagnosticsViewerFor(nil, &dwKeys{}, newDWCompiler(t)) != nil || diagnosticsViewerFor(users, nil, newDWCompiler(t)) != nil ||
		diagnosticsViewerFor(users, &dwKeys{}, nil) != nil {
		t.Error("a nil collaborator did not yield a nil ViewerFor (diagnostics.New must refuse it)")
	}
}

// dwStarter records the one start.
type dwStarter struct {
	calls int
	kind  tasks.Kind
	req   tasks.StartRequest
	who   *auth.Principal
	err   error
}

func (s *dwStarter) Start(ctx context.Context, kind tasks.Kind, req tasks.StartRequest) (tasks.RunRef, error) {
	s.calls++
	s.kind, s.req = kind, req
	s.who, _ = auth.PrincipalFrom(ctx)
	if s.err != nil {
		return tasks.RunRef{}, s.err
	}
	return tasks.RunRef{ID: "run-ra-s-al-ghul"}, nil
}

// emptyUserCtx is a principal whose user has the ID "", so a params ActorID of
// "" and a requestedBy of "" both equal it: the one thing left to refuse is the
// empty requester itself.
func emptyUserCtx() context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{Method: auth.MethodSession, User: &auth.User{}})
}

func principalCtx(id string) context.Context {
	p := &auth.Principal{Method: auth.MethodSession}
	if id != "" {
		p.User = &auth.User{ID: auth.UserID(id)}
	}
	return auth.WithPrincipal(context.Background(), p)
}

func TestDiagnosticsEnqueuer_StartsTheKindForTheRequester(t *testing.T) {
	st := &dwStarter{}
	e := diagnosticsEnqueuer{tasks: st}
	p := diagnostics.ExportParams{ActorID: "bruce", Perms: []string{"core:system.manage"}, Sections: []string{"core.health"}}
	id, err := e.EnqueueExport(principalCtx("bruce"), p, "bruce")
	if err != nil || id != "run-ra-s-al-ghul" {
		t.Fatalf("EnqueueExport = %q, %v", id, err)
	}
	if st.calls != 1 || st.kind != diagnostics.ExportKind || st.req.Mode != tasks.ModeRequestLive || st.req.Trigger != tasks.TriggerManual {
		t.Fatalf("start = %d calls, kind %q, mode %v, trigger %q", st.calls, st.kind, st.req.Mode, st.req.Trigger)
	}
	if st.who == nil || st.who.User == nil || st.who.User.ID != "bruce" {
		t.Fatalf("the executor saw principal %+v, want bruce's (it stamps the run from it)", st.who)
	}
	var back diagnostics.ExportParams
	if err := json.Unmarshal(st.req.Params, &back); err != nil || back.ActorID != "bruce" ||
		!slices.Equal(back.Perms, p.Perms) || !slices.Equal(back.Sections, p.Sections) {
		t.Fatalf("params %s (%v), want %+v", st.req.Params, err, p)
	}
}

func TestDiagnosticsEnqueuer_RefusesAMismatchedPerson(t *testing.T) {
	for name, c := range map[string]struct {
		ctx context.Context
		p   diagnostics.ExportParams
		by  string
	}{
		"params name another":    {principalCtx("bruce"), diagnostics.ExportParams{ActorID: "jason"}, "bruce"},
		"principal is another":   {principalCtx("jason"), diagnostics.ExportParams{ActorID: "bruce"}, "bruce"},
		"principal has no user":  {principalCtx(""), diagnostics.ExportParams{ActorID: "bruce"}, "bruce"},
		"no principal":           {context.Background(), diagnostics.ExportParams{ActorID: "bruce"}, "bruce"},
		"no requester":           {principalCtx(""), diagnostics.ExportParams{}, ""},
		"empty requester alone":  {emptyUserCtx(), diagnostics.ExportParams{}, ""},
		"requester is not owner": {principalCtx("bruce"), diagnostics.ExportParams{ActorID: "bruce"}, "jason"},
	} {
		st := &dwStarter{}
		if _, err := (diagnosticsEnqueuer{tasks: st}).EnqueueExport(c.ctx, c.p, c.by); !errors.Is(err, errExportActor) || st.calls != 0 {
			t.Errorf("%s: err %v, %d starts; want errExportActor and none", name, err, st.calls)
		}
	}
	boom := errors.New("arkham is full")
	if _, err := (diagnosticsEnqueuer{tasks: &dwStarter{err: boom}}).EnqueueExport(principalCtx("bruce"), diagnostics.ExportParams{ActorID: "bruce"}, "bruce"); !errors.Is(err, boom) {
		t.Errorf("a start failure: err %v, want it returned", err)
	}
	if _, err := (diagnosticsEnqueuer{}).EnqueueExport(principalCtx("bruce"), diagnostics.ExportParams{ActorID: "bruce"}, "bruce"); err == nil {
		t.Error("an enqueuer with no executor started nothing and reported nothing")
	}
}

// dwStore is a run table.
type dwStore struct {
	runs map[tasks.RunID]tasks.RunRecord
	err  error
}

func (s dwStore) Get(_ context.Context, id tasks.RunID) (tasks.RunRecord, error) {
	if s.err != nil {
		return tasks.RunRecord{}, s.err
	}
	r, ok := s.runs[id]
	if !ok {
		return tasks.RunRecord{}, tasks.ErrRunNotFound
	}
	return r, nil
}

func exportRun(id string, state tasks.RunState, actor, file string) tasks.RunRecord {
	rec := tasks.RunRecord{ID: tasks.RunID(id), Kind: diagnostics.ExportKind, State: state,
		Actor: tasks.Actor{UserID: actor, AuthMethod: "session"}}
	// The params name someone else: the adapter must read the stamped actor.
	rec.Params, _ = json.Marshal(diagnostics.ExportParams{ActorID: "joker"})
	if file != "" {
		rec.Result, _ = json.Marshal(diagnostics.ExportResult{File: file})
	}
	return rec
}

func TestDiagnosticsRuns_ReadsTheStoredRun(t *testing.T) {
	other := exportRun("run-scan", tasks.StateSucceeded, "bruce", "run-scan.zip")
	other.Kind = "core.library.scan"
	malformed := exportRun("run-bad", tasks.StateSucceeded, "bruce", "")
	malformed.Result = json.RawMessage(`{"file":`)
	store := dwStore{runs: map[tasks.RunID]tasks.RunRecord{
		"run-done":    exportRun("run-done", tasks.StateSucceeded, "bruce", "run-done.zip"),
		"run-busy":    exportRun("run-busy", tasks.StateRunning, "bruce", ""),
		"run-failed":  exportRun("run-failed", tasks.StateFailed, "bruce", ""),
		"run-forged":  exportRun("run-forged", tasks.StateSucceeded, "bruce", "run-done.zip"),
		"run-scan":    other,
		"run-bad":     malformed,
		"run-aliased": exportRun("run-done", tasks.StateSucceeded, "bruce", "run-done.zip"),
	}}
	r := diagnosticsRuns{store: store}

	if run, err := r.Run(t.Context(), "run-done"); err != nil || run != (healthhttp.Run{ActorID: "bruce", File: "run-done.zip"}) {
		t.Errorf("succeeded run = %+v, %v", run, err)
	}
	for _, id := range []string{"run-busy", "run-failed"} {
		if run, err := r.Run(t.Context(), id); err != nil || run != (healthhttp.Run{ActorID: "bruce"}) {
			t.Errorf("%s = %+v, %v; want the actor and no file", id, run, err)
		}
	}
	for _, id := range []string{"run-joker", "run-scan", "run-aliased"} {
		if _, err := r.Run(t.Context(), id); !errors.Is(err, healthhttp.ErrRunNotFound) {
			t.Errorf("%s: err %v, want healthhttp.ErrRunNotFound", id, err)
		}
	}
	for _, id := range []string{"run-forged", "run-bad"} {
		if run, err := r.Run(t.Context(), id); err == nil || errors.Is(err, healthhttp.ErrRunNotFound) || run.File != "" {
			t.Errorf("%s = %+v, %v; want an error and no file", id, run, err)
		}
	}
	boom := errors.New("pebble is closed")
	if _, err := (diagnosticsRuns{store: dwStore{err: boom}}).Run(t.Context(), "run-done"); !errors.Is(err, boom) || errors.Is(err, healthhttp.ErrRunNotFound) {
		t.Errorf("a store failure: err %v, want it returned and not read as not-found", err)
	}
	if _, err := (diagnosticsRuns{}).Run(t.Context(), "run-done"); err == nil {
		t.Error("runs with no store reported nothing")
	}
}

// dwRoutes is a route list that can change between calls.
type dwRoutes struct{ infos []httpapi.RouteInfo }

func (r *dwRoutes) Routes() iter.Seq[httpapi.RouteInfo] { return slices.Values(r.infos) }

func TestDiagnosticsRoutePatterns(t *testing.T) {
	lr := &dwRoutes{infos: []httpapi.RouteInfo{
		{Method: "GET", Pattern: "/livez"},
		{Pattern: "/arkham"},
		{Method: "GET", Pattern: "/api/core/v1/health"},
		{Method: "POST", Pattern: "/api/core/v1/health"},
		{Method: "GET"},
		{Pattern: "/arkham"},
	}}
	f := diagnosticsRoutePatterns(lr)
	want := []string{"GET /livez", "/livez", "/arkham", "GET /api/core/v1/health", "/api/core/v1/health", "POST /api/core/v1/health"}
	if got := f(); !slices.Equal(got, want) {
		t.Fatalf("patterns = %q, want %q", got, want)
	}
	lr.infos = append(lr.infos, httpapi.RouteInfo{Method: "GET", Pattern: "/readyz"})
	if got := f(); !slices.Contains(got, "GET /readyz") || !slices.Contains(got, "/readyz") {
		t.Fatalf("a route added after the function was built is missing: %q", got)
	}
	if diagnosticsRoutePatterns(nil) != nil {
		t.Error("a nil router did not yield nil")
	}

	// Over the real router: an Infra route registered with its method.
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{})
	r.Infra("GET /livez", rbac.Public, http.NotFoundHandler())
	if got := diagnosticsRoutePatterns(r)(); !slices.Equal(got, []string{"GET /livez", "/livez"}) {
		t.Fatalf("router patterns = %q", got)
	}
}
