// file: core/kernel/identity_mount_test.go
// version: 0.1.0
// guid: 3e8b1f64-92a7-4c05-b6d1-7a0c4e95f283
// last-edited: 2026-11-21

package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/internal/modulehttp"
	"github.com/falkcorp/mediacore/internal/taskhttp"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// Batman names only. B4: stages 12 and 13 and the identity routes in a real
// boot, behind the identity switch.

// mountOut is the fixtures' response body.
type mountOut struct {
	OK bool `json:"ok"`
}

// callbackBase is the OIDC callback base the fixtures declare, as ABO's is.
const callbackBase = "/api/v1/auth/oauth"

// mountFx is a booted kernel whose one module, "alfred", registers two routes:
// GET /relay (alfred:signal) and POST /relay-lock (alfred:signal, interactive
// only).
type mountFx struct {
	k     *Kernel
	h     http.Handler
	hits  *atomic.Int64
	relay string
	lock  string
}

func mountConfig(t *testing.T, switchOn bool) (*swapHost, Config, *atomic.Int64) {
	t.Helper()
	h := newSwapHost(t)
	c := h.envConfig(nil)
	c.IdentityBoot = switchOn
	c.AuthOptions.OIDCCallbackBase = callbackBase
	hits := new(atomic.Int64)
	handler := func(ctx context.Context, in *httpapi.In[struct{}]) (mountOut, error) {
		hits.Add(1)
		return mountOut{OK: true}, nil
	}
	c.Modules = []module.Module{storageMod("alfred", func(r *module.Registrar) error {
		httpapi.Handle(r.HTTP(), httpapi.Route{Method: http.MethodGet, Path: "/relay", Name: "getRelay", Perm: rbac.Need("alfred:signal")}, handler)
		httpapi.Handle(r.HTTP(), httpapi.Route{Method: http.MethodPost, Path: "/relay-lock", Name: "lockRelay",
			Perm: rbac.Need("alfred:signal"), Guards: httpapi.Guards{Interactive: true}}, handler)
		return nil
	}, seedStep(1, "cave")).with(func(m *module.Manifest) {
		m.Permissions = []rbac.PermissionSpec{{Name: "alfred:signal", Title: "Signal"}}
		m.Roles = []rbac.RoleSeed{{Name: "relay-worker", Title: "Relay worker", Permissions: []rbac.Permission{"alfred:signal"}}}
		m.ImportedRoleIDs = []rbac.RoleName{"relay-worker"}
	})}
	return h, c, hits
}

func newMountFx(t *testing.T) *mountFx {
	t.Helper()
	h, c, hits := mountConfig(t, true)
	k := h.mustBoot(c)
	fx := &mountFx{k: k, h: k.Handler(), hits: hits}
	for ri := range k.Routes() {
		switch ri.Method + " " + ri.Pattern {
		case "GET /api/alfred/v1/relay":
			fx.relay = ri.Pattern
		case "POST /api/alfred/v1/relay-lock":
			fx.lock = ri.Pattern
		}
	}
	if fx.relay == "" || fx.lock == "" {
		t.Fatalf("the module's routes are not mounted: %v", routeInfos(k))
	}
	return fx
}

func routeInfos(k *Kernel) []string {
	var out []string
	for ri := range k.Routes() {
		out = append(out, ri.Method+" "+ri.Pattern)
	}
	slices.Sort(out)
	return out
}

// user stores an active user holding roles and returns it with a session
// cookie for it, built the way stage 12 reads it.
func (fx *mountFx) user(t *testing.T, id string, roles ...rbac.RoleName) (*auth.User, *http.Cookie) {
	t.Helper()
	now := time.Now().UTC()
	u := &auth.User{ID: auth.UserID(id), Username: id, Status: auth.StatusActive, Roles: roles, CreatedAt: now, UpdatedAt: now, Version: 1}
	b := fx.k.ident.users.NewBatch()
	if err := fx.k.ident.users.PutUser(b, u); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(t.Context(), storage.Sync); err != nil {
		t.Fatal(err)
	}
	_ = b.Close()
	// The user is written past auth.Service, as an importer does: tell it, or
	// stage 12 binds nothing ("no user yet", 05-authn §4.2 item 1).
	fx.k.ident.svc.Auth.NoteUserCreated()
	issued, err := fx.k.ident.svc.Auth.IssueSession(t.Context(), u, auth.OriginPassword, false, auth.ClientInfo{})
	if err != nil {
		t.Fatal(err)
	}
	probe := httptest.NewRequest(http.MethodGet, "/", nil)
	return u, fx.k.ident.svc.Auth.SessionCookie(httpapi.ClientInfoFrom(probe), issued.Token, issued.ExpiresAt)
}

func (fx *mountFx) do(method, path string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(""))
	req.RemoteAddr = "192.0.2.10:4000"
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	fx.h.ServeHTTP(rec, req)
	return rec
}

func withCookie(c *http.Cookie) func(*http.Request) { return func(r *http.Request) { r.AddCookie(c) } }

// coreRoutes is the identity routes a switch-on boot mounts, one per package,
// by "METHOD pattern" (the authhttp entry, a worker of each of the six, and the
// OIDC pair on the fixed group).
var coreRoutes = []string{
	"GET /api/core/v1/modules",                  // modulehttp
	"POST /api/core/v1/ui/diagnostics",          // uihttp
	"GET /api/core/v1/auth/status",              // authhttp
	"GET /api/core/v1/roles",                    // rbachttp
	"GET /api/core/v1/audit",                    // audithttp
	"GET /api/core/v1/settings",                 // confighttp
	"POST /api/core/v1/system/factory-reset",    // storagehttp
	"GET " + callbackBase + "/{provider}/start", // authhttp OIDC (fixed group)
}

// TestIdentityMount_SwitchOnMountsTheRoutes: a switch-on boot has the seven
// packages' routes and the module's, each at its path.
func TestIdentityMount_SwitchOnMountsTheRoutes(t *testing.T) {
	fx := newMountFx(t)
	got := routeInfos(fx.k)
	for _, want := range coreRoutes {
		if !slices.Contains(got, want) {
			t.Errorf("route %q is not mounted; routes: %v", want, got)
		}
	}
}

// TestIdentityMount_SwitchOffMountsNothingAndStillRefuses: with the switch off
// no identity route exists, and a module's non-Public native route still
// refuses the boot with httpapi.ErrNoAuthService: relaxing the refusal is
// SetAuth's, and nothing calls it.
func TestIdentityMount_SwitchOffMountsNothingAndStillRefuses(t *testing.T) {
	h, c, _ := mountConfig(t, false)
	_, err := h.boot(c)
	if !errors.Is(err, httpapi.ErrNoAuthService) {
		t.Fatalf("boot with a non-Public native route and the switch off = %v, want httpapi.ErrNoAuthService", err)
	}

	h = newSwapHost(t)
	c = h.envConfig(nil)
	c.Modules = []module.Module{storageMod("alfred", func(r *module.Registrar) error {
		httpapi.Handle(r.HTTP(), httpapi.Route{Method: http.MethodGet, Path: "/relay", Name: "getRelay", Perm: rbac.Public},
			func(context.Context, *httpapi.In[struct{}]) (mountOut, error) { return mountOut{OK: true}, nil })
		return nil
	}, seedStep(1, "cave")).with(func(m *module.Manifest) { m.PublicRoutes = []string{"getRelay"} })}
	k := h.mustBoot(c)
	for _, r := range routeInfos(k) {
		if strings.HasPrefix(r, "GET /api/core/v1/auth") || strings.Contains(r, "/api/core/v1/roles") ||
			strings.Contains(r, "/api/core/v1/settings") || strings.Contains(r, "/factory-reset") || strings.Contains(r, callbackBase) {
			t.Errorf("the switch is off and %q is mounted", r)
		}
	}
}

// TestIdentityMount_Stage13Refuses: stage 13 sits behind stage 12 on the
// module's route and on core's, with each of its refusals over HTTP.
func TestIdentityMount_Stage13Refuses(t *testing.T) {
	fx := newMountFx(t)
	_, holder := fx.user(t, "u-lucius", "relay-worker")
	_, nobody := fx.user(t, "u-selina")

	t.Run("no credential: 401", func(t *testing.T) {
		before := fx.hits.Load()
		rec := fx.do(http.MethodGet, fx.relay, nil)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "authentication_required") {
			t.Fatalf("anonymous = %d %s", rec.Code, rec.Body)
		}
		if fx.hits.Load() != before {
			t.Error("a refused request reached the handler")
		}
	})
	t.Run("a session without the scope: 403 permission_denied", func(t *testing.T) {
		before := fx.hits.Load()
		rec := fx.do(http.MethodGet, fx.relay, withCookie(nobody))
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "permission_denied") {
			t.Fatalf("a session holding no role = %d %s", rec.Code, rec.Body)
		}
		if fx.hits.Load() != before {
			t.Error("a refused request reached the handler")
		}
	})
	t.Run("a session with the scope: served", func(t *testing.T) {
		before := fx.hits.Load()
		if rec := fx.do(http.MethodGet, fx.relay, withCookie(holder)); rec.Code != http.StatusOK {
			t.Fatalf("the scope holder = %d %s", rec.Code, rec.Body)
		}
		if fx.hits.Load() != before+1 {
			t.Error("a served request did not reach the handler")
		}
	})
	t.Run("an API key on an interactive route: 403 interactive_required", func(t *testing.T) {
		u, _ := fx.user(t, "u-barbara", "relay-worker")
		actor, err := fx.k.ident.svc.Auth.PrincipalFor(t.Context(), u, auth.MethodSession, nil, u.CredentialEpoch)
		if err != nil {
			t.Fatal(err)
		}
		raw, _, err := fx.k.ident.svc.Auth.CreateAPIKey(t.Context(), actor, auth.KeySpec{Owner: u.ID, Name: "oracle", Scopes: []rbac.Permission{"alfred:signal"}})
		if err != nil {
			t.Fatal(err)
		}
		bearer := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+raw) }
		before := fx.hits.Load()
		rec := fx.do(http.MethodPost, fx.lock, bearer)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "interactive_required") {
			t.Fatalf("a key on an interactive route = %d %s", rec.Code, rec.Body)
		}
		if fx.hits.Load() != before {
			t.Error("a refused request reached the handler")
		}
		// The same key is fine where the route is not interactive-only.
		if rec := fx.do(http.MethodGet, fx.relay, bearer); rec.Code != http.StatusOK {
			t.Fatalf("a key naming the scope on a plain route = %d %s", rec.Code, rec.Body)
		}
	})
	t.Run("core's own routes take the stages too", func(t *testing.T) {
		if rec := fx.do(http.MethodGet, "/api/core/v1/roles", nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("GET /api/core/v1/roles anonymous = %d %s", rec.Code, rec.Body)
		}
		if rec := fx.do(http.MethodGet, "/api/core/v1/roles", withCookie(nobody)); rec.Code != http.StatusForbidden {
			t.Fatalf("GET /api/core/v1/roles with no role = %d %s", rec.Code, rec.Body)
		}
	})
}

// TestIdentityMount_DescribeMountsTheSameRoutes: Describe, which opens no
// store, mounts the identity routes a real boot does, so the OpenAPI document
// it renders is the served one.
func TestIdentityMount_DescribeMountsTheSameRoutes(t *testing.T) {
	h, c, _ := mountConfig(t, true)
	live := routeInfos(h.mustBoot(c))

	_, dc, _ := mountConfig(t, true)
	dk, err := boot(t.Context(), dc, true)
	if err != nil {
		t.Fatalf("Describe boot: %v", err)
	}
	t.Cleanup(func() { _ = dk.closeCore(context.Background(), time.Now().Add(defaultShutdownBudget)) })
	if got := routeInfos(dk); !slices.Equal(got, live) {
		t.Errorf("Describe routes differ from the boot's:\n  describe %v\n  boot     %v", got, live)
	}
}

// reviewKind is the task kind of TestIdentityMount_VerifiedMethodIsInstalled.
const reviewKind tasks.Kind = "alfred.review"

// TestIdentityMount_VerifiedMethodIsInstalled: what constructTasks handed
// taskhttp is the kernel's rule when the app declared identity, observed
// through the approve route of the task server the kernel built. A key naming
// the approval scope is admitted, which taskhttp's own default never does, and
// a delegated session and a key that does not name it are refused (SEC-24).
func TestIdentityMount_VerifiedMethodIsInstalled(t *testing.T) {
	h := newSwapHost(t)
	c := h.envConfig(nil)
	c.IdentityBoot = true
	c.Modules = []module.Module{storageMod("alfred", func(r *module.Registrar) error {
		r.Tasks(tasks.Task(reviewKind, tasks.TaskSpec[struct{}, struct{}]{
			Title:   "Review the signal log",
			Effects: tasks.ReadOnly(),
			Perms:   tasks.Permissions{Start: rbac.Need("alfred:signal"), Approve: rbac.Need("alfred:approve")},
			Resume:  tasks.ResumeFromZero,
			Run:     func(*tasks.RC, struct{}) (struct{}, error) { return struct{}{}, nil },
		}))
		return r.Err()
	}, seedStep(1, "cave")).with(func(m *module.Manifest) {
		m.TaskLedger = []tasks.Kind{reviewKind}
		m.Permissions = []rbac.PermissionSpec{{Name: "alfred:signal", Title: "Signal"}, {Name: "alfred:approve", Title: "Approve"}}
		m.Roles = []rbac.RoleSeed{{Name: "reviewer", Title: "Reviewer", Permissions: []rbac.Permission{"alfred:signal", "alfred:approve"}}}
		m.ImportedRoleIDs = []rbac.RoleName{"reviewer"}
	})}
	k := h.mustBoot(c)
	fx := &mountFx{k: k}
	u, _ := fx.user(t, "u-lucius", "reviewer")
	session, err := k.ident.svc.Auth.PrincipalFor(t.Context(), u, auth.MethodSession, nil, u.CredentialEpoch)
	if err != nil {
		t.Fatal(err)
	}
	mint := func(name string, scopes ...rbac.Permission) *auth.Principal {
		_, rec, err := k.ident.svc.Auth.CreateAPIKey(t.Context(), session, auth.KeySpec{Owner: u.ID, Name: name, Scopes: scopes})
		if err != nil {
			t.Fatal(err)
		}
		p := *session
		p.Method, p.CredentialID, p.Perms = auth.MethodAPIKey, rec.ID, holdsAll{}
		return &p
	}
	// Every principal holds every scope, so the permission rule of the gate
	// and the run's visibility never decide: only the verified-method rule does.
	asMethod := func(m auth.Method) *auth.Principal { p := *session; p.Method, p.Perms = m, holdsAll{}; return &p }

	cases := []struct {
		name     string
		p        *auth.Principal
		verified bool
	}{
		{"own interactive session", asMethod(auth.MethodSession), true},
		{"key naming the approval scope", mint("approver", "alfred:approve"), true},
		{"key naming another scope", mint("watcher", "alfred:signal"), false},
		{"delegated session", asMethod(auth.MethodSessionDelegated), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := k.TaskClient().Start(asAdmin(t.Context()), reviewKind, tasks.StartRequest{Mode: tasks.ModeRequestLive, Trigger: tasks.TriggerManual})
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, taskhttp.BasePath+"/runs/"+string(ref.ID)+"/approve", strings.NewReader(`{}`))
			req = req.WithContext(auth.WithPrincipal(req.Context(), tc.p))
			rec := httptest.NewRecorder()
			k.tasks.server.Handler().ServeHTTP(rec, req)
			refused := rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), "tasks.verified_method_required")
			if refused == tc.verified {
				t.Fatalf("verified = %v, want %v: %d %s", !refused, tc.verified, rec.Code, rec.Body)
			}
			// A refusal is the gate's, not a run that could not be read: every
			// case reached the gate, so none is a 401 or a 404.
			if rec.Code == http.StatusNotFound || rec.Code == http.StatusUnauthorized {
				t.Fatalf("the request never reached the gate: %d %s", rec.Code, rec.Body)
			}
		})
	}
}

// TestIdentityMount_ModulesViewAndDocuments: the catalog the modules route
// serves is the kernel's (app identity, enablement, UI), RestartRequired is
// stored-versus-running and not "anything is stored", and the documents the
// OpenAPI routes serve are core's and each enabled module's, filled at Build.
func TestIdentityMount_ModulesViewAndDocuments(t *testing.T) {
	h, c, _ := mountConfig(t, true)
	c.DisplayName, c.Version = "Batcave", "1.2.3"
	k := h.mustBoot(c)
	view := modulesView{k: k}

	snap, err := view.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snap.App.ID != c.AppID || snap.App.DisplayName != "Batcave" || snap.App.Version != "1.2.3" {
		t.Errorf("App = %+v, want the Config's identity", snap.App)
	}
	var alfred *modulehttp.Module
	for i := range snap.Modules {
		if snap.Modules[i].ID == "alfred" {
			alfred = &snap.Modules[i]
		}
	}
	if alfred == nil || !alfred.Enabled {
		t.Fatalf("modules = %+v, want alfred enabled", snap.Modules)
	}
	if snap.RestartRequired {
		t.Error("RestartRequired on a boot with nothing stored")
	}

	// A stored value equal to the running one is not a difference; one that
	// differs is, and it is Pending, not running (core.modules is a boot key).
	apply := func(enabled string) {
		t.Helper()
		_, err := k.cfgBoot.store.Apply(t.Context(),
			config.Patch{Set: map[config.Path]json.RawMessage{"core.modules.alfred.enabled": json.RawMessage(enabled)}},
			config.Actor{Subject: "u-lucius", Interactive: true, Can: func(string) bool { return true }})
		if err != nil {
			t.Fatalf("Apply enabled=%s: %v", enabled, err)
		}
	}
	apply("true")
	if s, _ := view.Snapshot(t.Context()); s.RestartRequired {
		t.Error("RestartRequired when the stored enablement equals the running one")
	}
	apply("false")
	s, _ := view.Snapshot(t.Context())
	if !s.RestartRequired {
		t.Error("RestartRequired = false after storing alfred disabled while it runs")
	}
	if !s.Modules[slices.IndexFunc(s.Modules, func(m modulehttp.Module) bool { return m.ID == "alfred" })].Enabled {
		t.Error("the running state changed before the restart")
	}
	apply("true")
	if s, _ := view.Snapshot(t.Context()); s.RestartRequired {
		t.Error("RestartRequired after the stored value was put back")
	}

	for _, owner := range []string{"core", "alfred"} {
		doc, ok := k.docs.OpenAPI(owner)
		if !ok || !bytes.Contains(doc, []byte(`"openapi"`)) {
			t.Errorf("no OpenAPI document for %q (ok=%v)", owner, ok)
		}
	}
	if _, ok := k.docs.OpenAPI("joker"); ok {
		t.Error("a document for an owner that is not in the app")
	}
}
