// file: core/internal/authhttp/env_test.go
// version: 1.0.0
// guid: 91c4e2a7-5d38-4b06-8f1a-e7b3029d6c45
// last-edited: 2026-11-10

package authhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/auth/password"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/internal/authstore"
	"github.com/falkcorp/mediacore/internal/authz"
	"github.com/falkcorp/mediacore/internal/rbacstore"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// env is one booted auth.Service over an in-memory store (the kernel graph's
// own wiring: storage.Open over MemFS, the native stores, a frozen rbac
// compiling through authz), with recording audit and journal fakes. The
// routes are tested against the real Service: the rules they map live there.
type env struct {
	t     testing.TB
	st    *authstore.Store
	rb    *rbac.Service
	svc   *auth.Service
	audit *auditFake
	jrnl  *journalFake

	mu  sync.Mutex
	now time.Time
	// setupNeedsToken is core.auth.setup_requires_token; the default is true.
	setupNeedsToken bool
}

func (e *env) settings() auth.Settings {
	e.mu.Lock()
	defer e.mu.Unlock()
	v := e.setupNeedsToken
	return auth.Settings{SetupRequiresToken: &v}
}

// sessionIDs lists user's open sessions' public IDs. It drains the iterator
// before returning, so a test that fails afterwards leaves no engine access
// open.
func (e *env) sessionIDs(user auth.UserID) []string {
	e.t.Helper()
	var ids []string
	var failure error
	for v, err := range e.svc.Sessions(context.Background(), user) {
		if err != nil {
			failure = err
			continue
		}
		ids = append(ids, v.PublicID)
	}
	if failure != nil {
		e.t.Fatalf("Sessions %s: %v", user, failure)
	}
	return ids
}

func (e *env) clock() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

// roleCommissioner manages users without being an admin.
const roleCommissioner rbac.RoleName = "custom.commissioner"

// roleDetective is a module role a commissioner holds every scope of.
const roleDetective rbac.RoleName = "gotham.detective"

var gothamPerms = []rbac.PermissionSpec{
	{Name: "gotham:patrol", Title: "Patrol", Class: rbac.ClassView, DefaultGrants: []rbac.RoleName{rbac.RoleEditor, rbac.RoleViewer}},
	{Name: "gotham:signal", Title: "Bat-Signal", DefaultGrants: []rbac.RoleName{rbac.RoleEditor}},
	{Name: "gotham:cave.manage", Title: "Batcave", Sensitive: true},
	{Name: "gotham:cases.read", Title: "Case files", Class: rbac.ClassView},
}

func newEnv(t testing.TB) *env {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: filepath.Join(t.TempDir(), "main.pebble"),
		Owners: []storage.OwnerSpec{
			{Owner: authstore.Owner, Spec: authstore.StorageSpec(), Enabled: true},
			{Owner: rbacstore.Owner, Spec: rbacstore.StorageSpec(), Enabled: true},
		},
		Build:           "authhttp+fixture",
		FreeSpaceMargin: 1,
		FS:              storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		if cerr := o.Close(); cerr != nil {
			t.Logf("close after a failed boot: %v", cerr)
		}
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	e := &env{t: t, audit: &auditFake{}, jrnl: &journalFake{}, now: time.Date(2026, 11, 10, 21, 0, 0, 0, time.UTC), setupNeedsToken: true}
	aks, _ := db.Keyspace(authstore.Owner)
	rks, _ := db.Keyspace(rbacstore.Owner)
	if e.st, err = authstore.New(aks, authstore.Options{Now: e.clock}); err != nil {
		t.Fatal(err)
	}
	rst, err := rbacstore.New(rks, rbacstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if e.rb, err = rbac.New(ctx, rbac.Options{Source: "gotham-test"}, rbac.Deps{
		Store: rst, Compile: authz.Compile, Audit: &auditFake{}, Revocations: &journalFake{},
		NewBatch: rst.NewBatch, Holders: holders{e.st},
	}); err != nil {
		t.Fatal(err)
	}
	d := e.rb.Declarer("gotham")
	if err := d.Permissions(gothamPerms...); err != nil {
		t.Fatal(err)
	}
	if err := d.Roles(rbac.RoleSeed{Name: roleDetective, Title: "Detective", Permissions: []rbac.Permission{"gotham:patrol", "gotham:cases.read"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.rb.Freeze(ctx); err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	if err := e.rb.CommitSeeds(ctx); err != nil {
		t.Fatalf("CommitSeeds: %v", err)
	}
	admin := e.rb.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{rbac.RoleAdmin}, Interactive: true})
	if _, err := e.rb.CreateRole(ctx, admin, rbac.RoleInput{
		Name: roleCommissioner, Title: "Commissioner",
		Permissions: []rbac.Permission{"core:self", "core:metrics.read", "core:system.status", "core:tasks.view", "gotham:patrol", "gotham:signal", "gotham:cases.read", "core:users.manage", "core:users.view"},
	}); err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	e.rebuild()
	return e
}

// rebuild builds e.svc anew over the same store, as a restart would: HasUsers
// is a flag New sets.
func (e *env) rebuild() {
	e.t.Helper()
	svc, err := auth.New(context.Background(), auth.Options{}, auth.Deps{
		Store: e.st, NewBatch: e.st.NewBatch, RBAC: e.rb, Audit: e.audit, Revocations: e.jrnl, Clock: e.clock,
		Hasher: password.New(password.Options{Time: 1, MemoryKiB: 64}), Settings: e.settings,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.svc = svc
}

// user stores an active user named name holding roles, with password pw when
// pw is not empty.
func (e *env) user(name, pw string, roles ...rbac.RoleName) *auth.User {
	e.t.Helper()
	u := &auth.User{
		ID: auth.UserID("u-" + name), Username: name, Roles: roles, Status: auth.StatusActive,
		CreatedAt: e.clock(), UpdatedAt: e.clock(), Version: 1,
	}
	e.commit(func(b *storage.Batch) error { return e.st.PutUser(b, u) })
	if !e.svc.HasUsers() {
		e.rebuild()
	}
	if pw != "" {
		_, h, err := password.New(password.Options{Time: 1, MemoryKiB: 64}).Hash(pw)
		if err != nil {
			e.t.Fatal(err)
		}
		cur := e.reload(u.ID)
		cur.Version++
		e.commit(func(b *storage.Batch) error { return e.st.SetPassword(b, cur, h) })
	}
	return e.reload(u.ID)
}

func (e *env) reload(id auth.UserID) *auth.User {
	e.t.Helper()
	u, err := e.st.GetUser(context.Background(), id)
	if err != nil {
		e.t.Fatalf("GetUser %s: %v", id, err)
	}
	return u
}

func (e *env) commit(stage func(b *storage.Batch) error) {
	e.t.Helper()
	b := e.st.NewBatch()
	defer func() { _ = b.Close() }()
	if err := stage(b); err != nil {
		e.t.Fatal(err)
	}
	if err := b.Commit(context.Background(), storage.Sync); err != nil {
		e.t.Fatal(err)
	}
}

// principal is u's principal as method m builds it.
func (e *env) principal(u *auth.User, m auth.Method, scopes ...rbac.Permission) *auth.Principal {
	e.t.Helper()
	var sc []rbac.Permission
	if len(scopes) > 0 {
		sc = scopes
	}
	p, err := e.svc.PrincipalFor(context.Background(), u, m, sc, u.CredentialEpoch)
	if err != nil {
		e.t.Fatalf("PrincipalFor %s %s: %v", u.ID, m, err)
	}
	return p
}

// session is u's interactive session principal.
func (e *env) session(u *auth.User) *auth.Principal { return e.principal(u, auth.MethodSession) }

// withSession opens a real session for u and returns it with its principal
// (CredentialID the session's public ID), as stage 12 would build it.
func (e *env) withSession(u *auth.User) (*auth.Principal, *auth.IssuedSession) {
	e.t.Helper()
	p := e.session(u)
	is, err := e.svc.Login(context.Background(), auth.LoginInput{Username: u.Username, Password: pwOf(u), Client: peer("192.0.2.9")})
	if err != nil {
		e.t.Fatalf("Login %s: %v", u.Username, err)
	}
	p.CredentialID = is.PublicID
	return p, is
}

// pwOf is the password env.user gives a user it creates with one: the name,
// twice.
func pwOf(u *auth.User) string { return u.Username + u.Username }

// api builds the handlers over the real Service.
func (e *env) api(mod ...func(*Deps)) *api {
	e.t.Helper()
	d := FromService(e.svc, e.rb)
	d.Audit = e.audit
	d.Log = slog.New(slog.DiscardHandler)
	for _, m := range mod {
		m(&d)
	}
	return &api{Deps: d, log: d.Log}
}

// holders is rbac's role-assignment view over the identity store.
type holders struct{ st *authstore.Store }

func (h holders) HoldersOf(ctx context.Context, _ *storage.Batch, roles []rbac.RoleName) ([]rbac.Holder, error) {
	var out []rbac.Holder
	for u, err := range h.st.ScanUsers(ctx) {
		if err != nil {
			return nil, err
		}
		for _, r := range u.Roles {
			if slices.Contains(roles, r) {
				out = append(out, rbac.Holder{UserID: string(u.ID), Active: u.Status == auth.StatusActive})
				break
			}
		}
	}
	return out, nil
}

// auditFake records what is audited.
type auditFake struct {
	mu       sync.Mutex
	recorded []audit.Event
	emitted  []audit.Event
}

func (a *auditFake) Record(context.Context, audit.Event) error {
	return errors.New("state changes are audited in their batch with RecordIn")
}

func (a *auditFake) RecordIn(_ context.Context, b *storage.Batch, e audit.Event) error {
	if b == nil {
		return errors.New("RecordIn without the change's batch")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recorded = append(a.recorded, e)
	return nil
}

func (a *auditFake) Emit(_ context.Context, e audit.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.emitted = append(a.emitted, e)
}

// emittedActions lists the actions emitted outside a batch, in order.
func (a *auditFake) emittedActions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, e := range a.emitted {
		out = append(out, e.Action)
	}
	return out
}

func (a *auditFake) recordedActions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, e := range a.recorded {
		out = append(out, e.Action)
	}
	return out
}

type journalFake struct {
	mu    sync.Mutex
	lines []journal.Revocation
}

func (j *journalFake) Append(_ context.Context, b *storage.Batch, r journal.Revocation) error {
	if b == nil {
		return errors.New("Append without the revoking batch")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.lines = append(j.lines, r)
	return nil
}

func (j *journalFake) Gap(b *storage.Batch, _ time.Time) error {
	if b == nil {
		return errors.New("Gap without the revoking batch")
	}
	return nil
}

// peer is a client at ip.
func peer(ip string) auth.ClientInfo {
	a := netip.MustParseAddr(ip)
	return auth.ClientInfo{Addr: a, Peer: a, UserAgent: "batcomputer"}
}

// in builds the decoded request a handler gets for body (a JSON value or nil)
// and p, with the path values pv ("id", "publicID").
func in[Req any](p *auth.Principal, req Req) *httpapi.In[Req] {
	return &httpapi.In[Req]{Req: req, Principal: p, Request: httptest.NewRequest(http.MethodGet, "/", nil).WithContext(context.Background())}
}

// serve sends one request through h and returns the recorded response.
func serve(h http.Handler, method, target string, body any, mod ...func(*http.Request)) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			panic(err)
		}
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, target, rd)
	r.RemoteAddr = "192.0.2.50:4000"
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	for _, m := range mod {
		m(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// asPrincipal runs the request as p: stage 12's job, which the router does
// not have yet.
func asPrincipal(p *auth.Principal) func(*http.Request) {
	return func(r *http.Request) { *r = *r.WithContext(auth.WithPrincipal(r.Context(), p)) }
}

// problem decodes an application/problem+json body.
type problemBody struct {
	Status int    `json:"status"`
	Code   string `json:"code"`
}

func problemOf(t testing.TB, w *httptest.ResponseRecorder) problemBody {
	t.Helper()
	var p problemBody
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("response is not a problem body: %v: %s", err, w.Body.String())
	}
	return p
}

// wantProblem asserts w is a problem with status and code.
func wantProblem(t testing.TB, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body.String())
	}
	if got := problemOf(t, w).Code; got != code {
		t.Fatalf("code %q, want %q: %s", got, code, w.Body.String())
	}
}

// wantErr asserts err is an *httpapi.Error with status and code.
func wantErr(t testing.TB, err error, status int, code string) {
	t.Helper()
	var he *httpapi.Error
	if !errors.As(err, &he) {
		t.Fatalf("error %v is not an *httpapi.Error, want %d %s", err, status, code)
	}
	if he.Status != status || he.Code != code {
		t.Fatalf("error %d %s, want %d %s", he.Status, he.Code, status, code)
	}
}

func cookieNamed(w *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// publicHandler builds a router holding only a's Public routes, which Build
// accepts before stage 13 exists, and returns its handler: the real chain,
// the real mux.
func publicHandler(t testing.TB, a *api, lim httpapi.LimitOptions) http.Handler {
	t.Helper()
	r := httpapi.NewRouter(httpapi.Options{Limits: lim}, httpapi.Deps{})
	var pub []route
	for _, rt := range a.routes() {
		if rt.s.public() {
			pub = append(pub, rt)
		}
	}
	a.registerAll(r, pub)
	h, err := r.Build()
	if err != nil {
		t.Fatalf("Build of the Public routes: %v", err)
	}
	return h
}

// storageBatch is storage.Batch, for the signatures of test helpers.
type storageBatch = storage.Batch

// recordedAndEmitted is every event the fake saw, in-batch and out.
func (a *auditFake) recordedAndEmitted() []audit.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append(slices.Clone(a.recorded), a.emitted...)
}

// readerBody is s as a request body.
func readerBody(s string) io.ReadCloser { return io.NopCloser(strings.NewReader(s)) }
