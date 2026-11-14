// file: core/internal/healthhttp/routes_test.go
// version: 1.0.2
// guid: e6dcf7f8-396a-47a8-b1bd-154ed75f5597
// last-edited: 2026-11-14

package healthhttp

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/health/diagnostics"
	"github.com/falkcorp/mediacore/health/sysinfo"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// ---- doubles ---------------------------------------------------------------

const (
	hhBase     = "/api/core/v1"
	hhBruce    = "01GOTHAMBRUCEWAYNE0000000"
	hhAlfred   = "01GOTHAMALFREDPENNYWORTH"
	hhSecretPw = "batsignal-over-gotham-city"
)

// hhChecker holds exactly its permissions; core:admin holds every one, as the
// real checker's does for an interactive principal.
type hhChecker []rbac.Permission

func (c hhChecker) Has(p rbac.Permission) bool {
	return slices.Contains(c, p) || slices.Contains(c, rbac.CoreAdmin)
}

func (c hhChecker) Satisfies(r rbac.Requirement) bool {
	ps := r.Permissions()
	switch r.Kind() {
	case rbac.KindPublic, rbac.KindAuthenticated:
		return true
	case rbac.KindAllOf:
		return len(ps) > 0 && !slices.ContainsFunc(ps, func(p rbac.Permission) bool { return !c.Has(p) })
	case rbac.KindAnyOf:
		return slices.ContainsFunc(ps, c.Has)
	}
	return false
}
func (hhChecker) Confined() bool                { return false }
func (c hhChecker) Concrete() []rbac.Permission { return slices.Clone(c) }

// hhAs is user id authenticated by m holding perms.
func hhAs(id string, m auth.Method, perms ...rbac.Permission) *auth.Principal {
	p := &auth.Principal{Method: m, Perms: hhChecker(perms), CredentialID: "cred-gotham", ClientAddr: netip.MustParseAddr("192.0.2.7")}
	if m != auth.MethodAuthDisabled {
		p.User = &auth.User{ID: auth.UserID(id), Username: "user." + id[len(id)-4:]}
	}
	return p
}

type hhRegistry struct {
	snap   health.Snapshot
	status health.StatusPayload
}

func (r hhRegistry) Snapshot() health.Snapshot    { return r.snap }
func (r hhRegistry) Status() health.StatusPayload { return r.status }

type hhRuns map[string]Run

func (r hhRuns) Run(_ context.Context, id string) (Run, error) {
	if run, ok := r[id]; ok {
		return run, nil
	}
	return Run{}, ErrRunNotFound
}

type hhEnq struct {
	mu     sync.Mutex
	params diagnostics.ExportParams
	by     string
	calls  int
	err    error
}

func (e *hhEnq) EnqueueExport(_ context.Context, p diagnostics.ExportParams, by string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	e.params, e.by = p, by
	return "run-the-long-halloween", e.err
}

type hhRec struct {
	mu      sync.Mutex
	records []audit.Event
	fail    bool
}

func (r *hhRec) Record(_ context.Context, e audit.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return errors.New("audit disk is full")
	}
	r.records = append(r.records, e)
	return nil
}

func (r *hhRec) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.records)
}

type contributor []diagnostics.Section

func (c contributor) Sections() []diagnostics.Section { return c }

// hhRig is the routes over the doubles, mounted from the real route table.
type hhRig struct {
	t    *testing.T
	mux  *http.ServeMux
	rec  *hhRec
	enq  *hhEnq
	root *os.Root
	dir  string
	runs hhRuns
	logs *bytes.Buffer
}

func newRig(t *testing.T) *hhRig {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	r := &hhRig{t: t, rec: &hhRec{}, enq: &hhEnq{}, root: root, dir: dir, runs: hhRuns{}, logs: &bytes.Buffer{}}
	r.mount(nil)
	return r
}

func (r *hhRig) deps() Deps {
	snap := health.Snapshot{Ready: true, Live: true, Components: []health.ComponentState{
		{ID: "core.storage", Result: health.Result{Status: health.StatusUp, At: time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)}, Critical: true, Duration: 1500 * time.Microsecond},
		{ID: "core.outbox", Result: health.Result{Status: health.StatusDegraded, Reason: "dead_letters",
			Detail: "relay to https://oswald:" + hhSecretPw + "@penguin.example failed"}, Duration: 2 * time.Millisecond},
	}, Startup: []string{"search_index"}}
	return Deps{
		Health: hhRegistry{snap: snap, status: health.StatusPayload{Ready: false, NotReady: []string{"core.storage:warming"}}},
		Info: func(context.Context) (sysinfo.Info, error) {
			return sysinfo.Info{Version: "9.9.9", NumCPU: 8, Roots: []sysinfo.RootSpace{{Name: "arkham"}}}, nil
		},
		Runs:     r.runs,
		Enqueuer: r.enq,
		Bundles:  r.root,
		Audit:    r.rec,
		App:      "gotham-audio", Version: "9.9.9",
		Log: slog.New(slog.NewTextHandler(r.logs, nil)),
	}
}

// mount replaces the routes with ones over the rig's deps after tweak.
func (r *hhRig) mount(tweak func(*Deps)) {
	d := r.deps()
	if tweak != nil {
		tweak(&d)
	}
	r.mux = http.NewServeMux()
	for _, def := range newAPI(d).routeTable() {
		r.mux.Handle(def.method+" "+hhBase+def.path, def.handler)
	}
}

type hhReply struct {
	status int
	header http.Header
	body   []byte
}

func (r *hhRig) do(p *auth.Principal, method, target, contentType, body string) hhReply {
	r.t.Helper()
	req := httptest.NewRequest(method, hhBase+target, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if p != nil {
		req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	}
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		r.t.Fatal(err)
	}
	return hhReply{status: res.StatusCode, header: res.Header, body: b}
}

func problemCode(t *testing.T, rep hhReply) string {
	t.Helper()
	var pb httpapi.Problem
	if err := json.Unmarshal(rep.body, &pb); err != nil {
		t.Fatalf("body is not a problem: %v: %s", err, rep.body)
	}
	return pb.Code
}

var (
	manage    = rbac.Permission("core:system.manage")
	adminPerm = rbac.CoreAdmin
)

// bundleFor builds a real bundle for actor as run id, with one section that
// needs core:admin and one that needs core:system.manage, and registers the run.
func (r *hhRig) bundleFor(id, actor string) {
	r.t.Helper()
	sec := func(name string, perm rbac.Requirement) diagnostics.Contributor {
		return contributor{{Name: name, Perm: perm, Write: func(_ context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
			return w.JSON("d.json", map[string]string{"section": name})
		}}}
	}
	e, err := diagnostics.New(diagnostics.Config{
		Root: r.root,
		ViewerFor: func(_ context.Context, a tasks.Actor) (diagnostics.Viewer, error) {
			return diagnostics.Viewer{ActorID: a.UserID, Can: func(p rbac.Permission) bool { return p == manage || p == adminPerm }}, nil
		},
		Contributors: []diagnostics.Contributor{sec("batcave.vault", rbac.Need("core:admin")), sec("batcave.garage", rbac.Need("core:system.manage"))},
	})
	if err != nil {
		r.t.Fatal(err)
	}
	res, err := e.Build(context.Background(), id, tasks.Actor{UserID: actor, AuthMethod: "session"}, diagnostics.ExportParams{ActorID: actor}, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	r.runs[id] = Run{ActorID: actor, File: res.File}
}

func zipNames(t *testing.T, b []byte) []string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	var out []string
	for _, f := range zr.File {
		out = append(out, f.Name)
	}
	slices.Sort(out)
	return out
}

// ---- permissions ------------------------------------------------------------

type hhCase struct {
	name        string
	method      string
	target      string
	ctype, body string
	perm        rbac.Permission
	interactive bool
	anon        bool
	ok          int
}

func cases() []hhCase {
	return []hhCase{
		{name: "health", method: "GET", target: "/health", perm: manage, ok: 200},
		{name: "info", method: "GET", target: "/system/info", perm: manage, ok: 200},
		{name: "status", method: "GET", target: "/system/status", perm: "core:system.status", anon: true, ok: 200},
		{name: "start", method: "POST", target: "/diagnostics", perm: manage, ok: 202},
		{name: "download", method: "GET", target: "/diagnostics/run-bruce", perm: manage, interactive: true, ok: 200},
	}
}

func TestHealthRoutesPermissions(t *testing.T) {
	for _, c := range cases() {
		t.Run(c.name, func(t *testing.T) {
			grant := func() *auth.Principal {
				if c.interactive {
					return hhAs(hhBruce, auth.MethodSession, c.perm)
				}
				return hhAs(hhBruce, auth.MethodAPIKey, c.perm)
			}
			setup := func() *hhRig {
				r := newRig(t)
				r.bundleFor("run-bruce", hhBruce)
				return r
			}
			t.Run("no principal", func(t *testing.T) {
				if rep := setup().do(nil, c.method, c.target, c.ctype, c.body); rep.status != 401 {
					t.Errorf("status = %d, want 401: %s", rep.status, rep.body)
				}
			})
			t.Run("lacking the permission", func(t *testing.T) {
				r := setup()
				rep := r.do(hhAs(hhBruce, auth.MethodSession, "core:users.view"), c.method, c.target, c.ctype, c.body)
				if rep.status != 403 || problemCode(t, rep) != "permission_denied" {
					t.Errorf("= %d %s, want 403 permission_denied", rep.status, rep.body)
				}
				if r.rec.count() != 0 || r.enq.calls != 0 {
					t.Error("a collaborator was reached before authorisation")
				}
			})
			if c.interactive {
				t.Run("non-interactive credential", func(t *testing.T) {
					rep := setup().do(hhAs(hhBruce, auth.MethodAPIKey, c.perm), c.method, c.target, c.ctype, c.body)
					if rep.status != 403 || problemCode(t, rep) != "interactive_required" {
						t.Errorf("= %d %s, want 403 interactive_required", rep.status, rep.body)
					}
				})
			}
			t.Run("granted", func(t *testing.T) {
				if rep := setup().do(grant(), c.method, c.target, c.ctype, c.body); rep.status != c.ok {
					t.Errorf("status = %d, want %d: %s", rep.status, c.ok, rep.body)
				}
			})
			if c.anon {
				t.Run("auth disabled", func(t *testing.T) {
					p := hhAs(hhBruce, auth.MethodAuthDisabled, c.perm)
					if rep := setup().do(p, c.method, c.target, c.ctype, c.body); rep.status != c.ok {
						t.Errorf("status = %d, want %d (the status route reaches an auth-off caller)", rep.status, c.ok)
					}
				})
			} else {
				t.Run("auth disabled", func(t *testing.T) {
					p := hhAs(hhBruce, auth.MethodAuthDisabled, c.perm)
					if rep := setup().do(p, c.method, c.target, c.ctype, c.body); rep.status != 401 {
						t.Errorf("status = %d, want 401 (a route that names an actor needs a user)", rep.status)
					}
				})
			}
		})
	}
}

// ---- reads --------------------------------------------------------------------

func TestHealthRoute_DetailRedactedAndShaped(t *testing.T) {
	r := newRig(t)
	rep := r.do(hhAs(hhBruce, auth.MethodAPIKey, manage), "GET", "/health", "", "")
	if rep.status != 200 {
		t.Fatalf("status %d: %s", rep.status, rep.body)
	}
	if bytes.Contains(rep.body, []byte(hhSecretPw)) {
		t.Errorf("a credential reached the health body: %s", rep.body)
	}
	var got healthResponse
	if err := json.Unmarshal(rep.body, &got); err != nil {
		t.Fatal(err)
	}
	if got.App != "gotham-audio" || got.Version != "9.9.9" || !got.Ready || !got.Live || len(got.Components) != 2 || !slices.Equal(got.Startup, []string{"search_index"}) {
		t.Errorf("body = %+v", got)
	}
	st := got.Components[0]
	if st.ID != "core.storage" || st.Status != "up" || !st.Critical || st.DurationMs != 1.5 || st.At != "2026-11-13T09:00:00Z" {
		t.Errorf("core.storage = %+v", st)
	}
	if ob := got.Components[1]; ob.Reason != "dead_letters" || ob.Status != "degraded" || ob.Detail == "" {
		t.Errorf("core.outbox = %+v", ob)
	}
	if rep.header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rep.header.Get("Cache-Control"))
	}
}

func TestSystemInfoRoute(t *testing.T) {
	r := newRig(t)
	rep := r.do(hhAs(hhBruce, auth.MethodAPIKey, manage), "GET", "/system/info", "", "")
	var got sysinfo.Info
	if err := json.Unmarshal(rep.body, &got); err != nil || rep.status != 200 {
		t.Fatalf("%d %v %s", rep.status, err, rep.body)
	}
	if got.App != "gotham-audio" || got.Version != "9.9.9" || got.NumCPU != 8 || len(got.Roots) != 1 || got.Roots[0].Name != "arkham" {
		t.Errorf("info = %+v", got)
	}
	r.mount(func(d *Deps) {
		d.Info = func(context.Context) (sysinfo.Info, error) {
			return sysinfo.Info{}, errors.New("statfs on /mnt/arkham failed")
		}
	})
	rep = r.do(hhAs(hhBruce, auth.MethodAPIKey, manage), "GET", "/system/info", "", "")
	if rep.status != 500 || bytes.Contains(rep.body, []byte("arkham")) {
		t.Errorf("a failing reader = %d %s, want a 500 that withholds the cause", rep.status, rep.body)
	}
}

func TestSystemStatusRoute_TokensOnly(t *testing.T) {
	r := newRig(t)
	rep := r.do(hhAs(hhAlfred, auth.MethodAPIKey, "core:system.status"), "GET", "/system/status", "", "")
	if rep.status != 200 {
		t.Fatalf("status %d", rep.status)
	}
	var generic map[string]any
	if err := json.Unmarshal(rep.body, &generic); err != nil {
		t.Fatal(err)
	}
	if len(generic) != 2 || generic["ready"] != false || generic["not_ready"] == nil {
		t.Errorf("body = %s, want exactly {ready, not_ready}", rep.body)
	}
}

func TestHealthRoutesUnavailableOnlyAfterAuthorisation(t *testing.T) {
	r := newRig(t)
	r.mount(func(d *Deps) { d.Health, d.Info, d.Runs, d.Enqueuer, d.Bundles, d.Audit = nil, nil, nil, nil, nil, nil })
	for _, c := range cases() {
		if rep := r.do(nil, c.method, c.target, c.ctype, c.body); rep.status != 401 {
			t.Errorf("%s with no principal and no collaborators = %d, want 401", c.name, rep.status)
		}
		var p *auth.Principal
		if c.interactive {
			p = hhAs(hhBruce, auth.MethodSession, c.perm)
		} else {
			p = hhAs(hhBruce, auth.MethodAPIKey, c.perm)
		}
		if rep := r.do(p, c.method, c.target, c.ctype, c.body); rep.status != 503 {
			t.Errorf("%s with no collaborators = %d, want 503", c.name, rep.status)
		}
	}
}

// ---- diagnostics: start ---------------------------------------------------------

func TestDiagnosticsStart(t *testing.T) {
	r := newRig(t)
	p := hhAs(hhBruce, auth.MethodAPIKey, manage)
	rep := r.do(p, "POST", "/diagnostics", "application/json", `{"sections":["core.health","batcave.garage"]}`)
	if rep.status != 202 || !strings.Contains(string(rep.body), `"run_id":"run-the-long-halloween"`) {
		t.Fatalf("= %d %s", rep.status, rep.body)
	}
	if r.enq.params.ActorID != hhBruce || r.enq.by != hhBruce || !slices.Equal(r.enq.params.Sections, []string{"core.health", "batcave.garage"}) {
		t.Errorf("enqueued %+v by %q", r.enq.params, r.enq.by)
	}
	// An empty body means every section.
	if rep := r.do(p, "POST", "/diagnostics", "", ""); rep.status != 202 || len(r.enq.params.Sections) != 0 {
		t.Errorf("empty body = %d, sections %v", rep.status, r.enq.params.Sections)
	}
	for name, body := range map[string]string{
		"bad name":       `{"sections":["Joker"]}`,
		"unknown member": `{"sections":[],"actor_id":"01GOTHAMALFREDPENNYWORTH"}`,
		"trailing":       `{} {}`,
		"not json":       `nope`,
	} {
		before := r.enq.calls
		if rep := r.do(p, "POST", "/diagnostics", "application/json", body); rep.status < 400 || rep.status >= 500 || r.enq.calls != before {
			t.Errorf("%s = %d (enqueued %v)", name, rep.status, r.enq.calls != before)
		}
	}
	if rep := r.do(p, "POST", "/diagnostics", "text/plain", `{}`); rep.status != 415 {
		t.Errorf("text/plain = %d", rep.status)
	}
	r.enq.err = errors.New("queue is closed")
	if rep := r.do(p, "POST", "/diagnostics", "", ""); rep.status != 500 || bytes.Contains(rep.body, []byte("queue")) {
		t.Errorf("an enqueue failure = %d %s", rep.status, rep.body)
	}
}

// ---- diagnostics: download ------------------------------------------------------

func TestDiagnostics_DownloadOtherActor404(t *testing.T) {
	r := newRig(t)
	r.bundleFor("run-bruce", hhBruce)
	alfred := hhAs(hhAlfred, auth.MethodSession, manage)
	rep := r.do(alfred, "GET", "/diagnostics/run-bruce", "", "")
	if rep.status != 404 || problemCode(t, rep) != "run_not_found" {
		t.Fatalf("another actor's run = %d %s, want 404 run_not_found", rep.status, rep.body)
	}
	missing := r.do(alfred, "GET", "/diagnostics/run-nobody", "", "")
	if missing.status != 404 || problemCode(t, missing) != "run_not_found" {
		t.Errorf("an unknown run answers differently from someone else's: %d %s", missing.status, missing.body)
	}
	if r.rec.count() != 0 {
		t.Error("a refused download was audited as a download")
	}
	// The actor and core:admin may fetch it.
	if rep := r.do(hhAs(hhBruce, auth.MethodSession, manage), "GET", "/diagnostics/run-bruce", "", ""); rep.status != 200 {
		t.Errorf("the run's actor = %d", rep.status)
	}
	if rep := r.do(hhAs(hhAlfred, auth.MethodSession, manage, adminPerm), "GET", "/diagnostics/run-bruce", "", ""); rep.status != 200 {
		t.Errorf("core:admin = %d", rep.status)
	}
	if got := r.rec.count(); got != 2 {
		t.Errorf("%d downloads audited, want 2", got)
	}
}

func TestDiagnostics_NarrowKeyOfActor403(t *testing.T) {
	r := newRig(t)
	r.bundleFor("run-bruce", hhBruce)
	// The actor's own scrape key, scoped to metrics only.
	rep := r.do(hhAs(hhBruce, auth.MethodAPIKey, "core:metrics.read"), "GET", "/diagnostics/run-bruce", "", "")
	if rep.status != 403 || problemCode(t, rep) != "permission_denied" {
		t.Errorf("narrow key = %d %s", rep.status, rep.body)
	}
	// The actor's full-scope but non-interactive key, and a delegated session.
	for _, m := range []auth.Method{auth.MethodAPIKey, auth.MethodSessionDelegated} {
		rep := r.do(hhAs(hhBruce, m, manage, adminPerm), "GET", "/diagnostics/run-bruce", "", "")
		if rep.status != 403 || problemCode(t, rep) != "interactive_required" {
			t.Errorf("%s = %d %s, want 403 interactive_required", m, rep.status, rep.body)
		}
	}
	if r.rec.count() != 0 {
		t.Error("a refused credential was audited as a download")
	}
}

func TestDiagnostics_DemotedActor403(t *testing.T) {
	r := newRig(t)
	r.bundleFor("run-bruce", hhBruce) // started while Bruce held core:admin
	// Demoted below core:system.manage: the route refuses.
	rep := r.do(hhAs(hhBruce, auth.MethodSession, "core:self"), "GET", "/diagnostics/run-bruce", "", "")
	if rep.status != 403 {
		t.Errorf("demoted below manage = %d %s, want 403", rep.status, rep.body)
	}
	// Demoted from core:admin to core:system.manage: the download works, minus
	// the sections that need core:admin.
	rep = r.do(hhAs(hhBruce, auth.MethodSession, manage), "GET", "/diagnostics/run-bruce", "", "")
	if rep.status != 200 {
		t.Fatalf("demoted to manage = %d %s", rep.status, rep.body)
	}
	names := zipNames(t, rep.body)
	if slices.Contains(names, "batcave.vault/d.json") {
		t.Errorf("a section the demoted actor can no longer read was streamed: %v", names)
	}
	if !slices.Contains(names, "batcave.garage/d.json") || !slices.Contains(names, diagnostics.OmittedAtDownloadFile) {
		t.Errorf("entries = %v", names)
	}
	// Still admin: everything.
	rep = r.do(hhAs(hhBruce, auth.MethodSession, manage, adminPerm), "GET", "/diagnostics/run-bruce", "", "")
	if !slices.Contains(zipNames(t, rep.body), "batcave.vault/d.json") {
		t.Error("an admin's download lacks the admin section")
	}
}

func TestDiagnosticsDownload_StatesAndHeaders(t *testing.T) {
	r := newRig(t)
	r.bundleFor("run-bruce", hhBruce)
	p := hhAs(hhBruce, auth.MethodSession, manage)
	rep := r.do(p, "GET", "/diagnostics/run-bruce", "", "")
	if rep.status != 200 || rep.header.Get("Content-Type") != "application/zip" ||
		rep.header.Get("Content-Disposition") != `attachment; filename="run-bruce.zip"` ||
		rep.header.Get("X-Content-Type-Options") != "nosniff" || rep.header.Get("Cache-Control") != "no-store" {
		t.Errorf("download = %d %v", rep.status, rep.header)
	}
	r.rec.mu.Lock()
	ev := r.rec.records[0]
	r.rec.mu.Unlock()
	if ev.Action != "core.diagnostics.download" || ev.Actor.UserID != hhBruce || ev.Details["run_id"] != "run-bruce" {
		t.Errorf("audit event = %+v", ev)
	}

	// A run that has produced nothing yet.
	r.runs["run-pending"] = Run{ActorID: hhBruce}
	if rep := r.do(p, "GET", "/diagnostics/run-pending", "", ""); rep.status != 409 || problemCode(t, rep) != "bundle_not_ready" {
		t.Errorf("pending run = %d %s", rep.status, rep.body)
	}
	// A run whose file was pruned.
	r.runs["run-old"] = Run{ActorID: hhBruce, File: "run-old.zip"}
	if rep := r.do(p, "GET", "/diagnostics/run-old", "", ""); rep.status != 410 || problemCode(t, rep) != "bundle_expired" {
		t.Errorf("expired run = %d %s", rep.status, rep.body)
	}
	// A run ID that cannot be a file name never reaches the lookup.
	for _, id := range []string{"..%2f..%2fetc", "a.b", "run%20bruce"} {
		if rep := r.do(p, "GET", "/diagnostics/"+id, "", ""); rep.status != 404 {
			t.Errorf("run id %q = %d, want 404", id, rep.status)
		}
	}
	// A run whose recorded file escapes the root is not a bundle name.
	r.runs["run-evil"] = Run{ActorID: hhBruce, File: "../../etc/passwd"}
	if rep := r.do(p, "GET", "/diagnostics/run-evil", "", ""); rep.status != 410 {
		t.Errorf("escaping file = %d, want 410", rep.status)
	}
}

func TestDiagnosticsDownload_AuditFailureStopsTheStream(t *testing.T) {
	r := newRig(t)
	r.bundleFor("run-bruce", hhBruce)
	r.rec.fail = true
	rep := r.do(hhAs(hhBruce, auth.MethodSession, manage), "GET", "/diagnostics/run-bruce", "", "")
	if rep.status != 503 {
		t.Errorf("= %d, want 503", rep.status)
	}
	// Anywhere in the body, not only at its start: an error body followed by
	// the zip is still the bundle leaving.
	if bytes.Contains(rep.body, []byte("PK\x03\x04")) {
		t.Error("bundle bytes were sent although the audit write failed")
	}
}

// ---- registration --------------------------------------------------------------

func registered(t testing.TB) *httpapi.Router {
	t.Helper()
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: slog.New(slog.DiscardHandler)})
	Register(r.CoreGroup(), Deps{})
	return r
}

func TestHealthRegisterRouteTable(t *testing.T) {
	want := map[string]struct {
		method, pattern, perm string
		interactive           bool
	}{
		"getHealth":           {"GET", hhBase + "/health", "core:system.manage", false},
		"getSystemInfo":       {"GET", hhBase + "/system/info", "core:system.manage", false},
		"getSystemStatus":     {"GET", hhBase + "/system/status", "core:system.status", false},
		"startDiagnostics":    {"POST", hhBase + "/diagnostics", "core:system.manage", false},
		"downloadDiagnostics": {"GET", hhBase + "/diagnostics/{run}", "core:system.manage", true},
	}
	seen := 0
	for ri := range registered(t).Routes() {
		w, ok := want[ri.Name]
		if !ok {
			t.Errorf("unexpected route %s %s", ri.Method, ri.Pattern)
			continue
		}
		seen++
		if ri.Method != w.method || ri.Pattern != w.pattern || ri.Perm.String() != w.perm || ri.Guards != (httpapi.Guards{Interactive: w.interactive}) {
			t.Errorf("%s = %s %s %s %+v", ri.Name, ri.Method, ri.Pattern, ri.Perm, ri.Guards)
		}
	}
	if seen != len(want) {
		t.Errorf("%d routes registered, want %d", seen, len(want))
	}
	// The handler's table and Register's are the one table.
	for _, def := range newAPI(Deps{}).routeTable() {
		if _, ok := want[def.name]; !ok {
			t.Errorf("handler table has %s, which Register tests do not expect", def.name)
		}
	}
}

// spaHandler stands in for the shell: it answers 200 text/html and counts.
type spaHandler struct{ hits int }

func (s *spaHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	s.hits++
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, "<html>the shell</html>")
}

// TestProbes_VariantsAreNotSPA is the real-router half of 05-health 5H.3.3: the
// exact probe paths answer, and a variant of a probe path (or /health, or
// /metrics) gets a problem 404, never 200 text/html that a monitor would read
// as healthy.
func TestProbes_VariantsAreNotSPA(t *testing.T) {
	reg := health.NewRegistry()
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: slog.New(slog.DiscardHandler)})
	RegisterProbes(r, reg)
	spa := &spaHandler{}
	if err := r.SPA(spa, httpapi.SPAOptions{}); err != nil {
		t.Fatal(err)
	}
	h, err := r.Build()
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	if rec := get("/livez"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"live":true`) {
		t.Errorf("/livez = %d %s", rec.Code, rec.Body.String())
	}
	if rec := get("/readyz"); rec.Code != 200 && rec.Code != 503 || !strings.Contains(rec.Body.String(), `"ready"`) {
		t.Errorf("/readyz = %d %s", rec.Code, rec.Body.String())
	}
	for _, p := range []string{"/livez/", "/READYZ", "/Livez", "/readyz/x", "/health/", "/HEALTH", "/metrics/", "/health"} {
		before := spa.hits
		rec := get(p)
		if spa.hits != before || rec.Header().Get("Content-Type") == "text/html; charset=utf-8" {
			t.Errorf("GET %s reached the SPA (%d %q)", p, rec.Code, rec.Header().Get("Content-Type"))
		}
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
	}
	if rec := get("/library"); rec.Code != 200 || spa.hits == 0 {
		t.Errorf("GET /library = %d, want the SPA", rec.Code)
	}
	segs := strings.Join(r.NonSPASegments(), ",")
	for _, s := range []string{"livez", "readyz", "health", "metrics"} {
		if !strings.Contains(","+segs+",", ","+s+",") {
			t.Errorf("NonSPASegments %s lacks %s", segs, s)
		}
	}
}
