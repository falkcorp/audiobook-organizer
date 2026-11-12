// file: core/internal/taskhttp/realauth_test.go
// version: 1.0.0
// guid: 1c23dc07-9b8c-4a77-8e20-f236f992bf02
// last-edited: 2026-11-11

package taskhttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/auth/password"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/internal/authstore"
	"github.com/falkcorp/mediacore/internal/authz"
	"github.com/falkcorp/mediacore/internal/rbacstore"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// This file runs the approval and permission cells over core auth itself
// (F4-05, finishing F2-05): principals are built by auth.Service.PrincipalFor
// and CreateAPIKey over a real rbac compile, never by the fake grants of
// fakes_test.go. The kernel's verified-method rule (05-rbac 4.10, F4-08) is
// modelled by kernelRule below from the same pieces the kernel has: the
// registered extensions' specs and the key records auth keeps.

const (
	extCF    = auth.Method("ext:cfaccess") // an interactive identity extension (Cloudflare Access shaped)
	extBadge = auth.Method("ext:badge")    // an extension that is not interactive
	gateWay  = auth.Method("arkham.gate")  // a module's compat method; never interactive
)

type nameAuthenticator string

func (n nameAuthenticator) Name() string                                      { return string(n) }
func (nameAuthenticator) Authenticate(*http.Request) (*auth.Assertion, error) { return nil, nil }

// realAuth is one booted auth.Service over an in-memory store.
type realAuth struct {
	t   testing.TB
	st  *authstore.Store
	rb  *rbac.Service
	svc *auth.Service
	now time.Time
}

func newRealAuth(t testing.TB) *realAuth {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: filepath.Join(t.TempDir(), "main.pebble"),
		Owners: []storage.OwnerSpec{
			{Owner: authstore.Owner, Spec: authstore.StorageSpec(), Enabled: true},
			{Owner: rbacstore.Owner, Spec: rbacstore.StorageSpec(), Enabled: true},
		},
		Build:           "taskhttp+fixture",
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
	r := &realAuth{t: t, now: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)}
	aks, _ := db.Keyspace(authstore.Owner)
	rks, _ := db.Keyspace(rbacstore.Owner)
	if r.st, err = authstore.New(aks, authstore.Options{Now: func() time.Time { return r.now }}); err != nil {
		t.Fatal(err)
	}
	rst, err := rbacstore.New(rks, rbacstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.rb, err = rbac.New(ctx, rbac.Options{Source: "taskhttp-test"}, rbac.Deps{
		Store: rst, Compile: authz.Compile, Audit: &sinkAudit{}, Revocations: &sinkJournal{},
		NewBatch: rst.NewBatch, Holders: noHolders{},
	}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []struct {
		ns    string
		perms []rbac.PermissionSpec
		roles []rbac.RoleSeed
	}{
		{"batcave", []rbac.PermissionSpec{
			{Name: "batcave:run", Title: "Run the Batcave"},
			{Name: "batcave:view", Title: "See the Batcave", Class: rbac.ClassView},
		}, []rbac.RoleSeed{
			{Name: "batcave.keeper", Title: "Keeper", Permissions: []rbac.Permission{"batcave:run", "batcave:view"}},
			{Name: "batcave.watcher", Title: "Watcher", Permissions: []rbac.Permission{"batcave:view"}},
		}},
		{"arkham", []rbac.PermissionSpec{{Name: "arkham:approve", Title: "Approve transfers"}},
			[]rbac.RoleSeed{{Name: "arkham.warden", Title: "Warden", Permissions: []rbac.Permission{"arkham:approve"}}}},
	} {
		dc := r.rb.Declarer(d.ns)
		if err := dc.Permissions(d.perms...); err != nil {
			t.Fatal(err)
		}
		if err := dc.Roles(d.roles...); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.rb.Freeze(ctx); err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	if err := r.rb.CommitSeeds(ctx); err != nil {
		t.Fatalf("CommitSeeds: %v", err)
	}
	r.svc, err = auth.New(ctx, auth.Options{}, auth.Deps{
		Store: r.st, NewBatch: r.st.NewBatch, RBAC: r.rb, Audit: &sinkAudit{}, Revocations: &sinkJournal{},
		Clock:  func() time.Time { return r.now },
		Hasher: password.New(password.Options{Time: 1, MemoryKiB: 64}),
		Extensions: []auth.Extension{
			{Authenticator: nameAuthenticator("cfaccess"), Spec: auth.MethodSpec{Method: extCF, Interactive: true}, Order: 150},
			{Authenticator: nameAuthenticator("badge"), Spec: auth.MethodSpec{Method: extBadge}, Order: 160},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// user stores an active user holding roles. The first user rebuilds nothing:
// the service reads users through the store.
func (r *realAuth) user(name string, roles ...rbac.RoleName) *auth.User {
	r.t.Helper()
	u := &auth.User{
		ID: auth.UserID("u-" + name), Username: name, Roles: roles, Status: auth.StatusActive,
		CreatedAt: r.now, UpdatedAt: r.now, Version: 1,
	}
	b := r.st.NewBatch()
	defer func() { _ = b.Close() }()
	if err := r.st.PutUser(b, u); err != nil {
		r.t.Fatal(err)
	}
	if err := b.Commit(context.Background(), storage.Sync); err != nil {
		r.t.Fatal(err)
	}
	got, err := r.st.GetUser(context.Background(), u.ID)
	if err != nil {
		r.t.Fatal(err)
	}
	return got
}

// as is u's principal as method m builds it, over scopes (nil: the roles).
func (r *realAuth) as(u *auth.User, m auth.Method, scopes ...rbac.Permission) *auth.Principal {
	r.t.Helper()
	var sc []rbac.Permission
	if len(scopes) > 0 {
		sc = scopes
	}
	p, err := r.svc.PrincipalFor(context.Background(), u, m, sc, u.CredentialEpoch)
	if err != nil {
		r.t.Fatalf("PrincipalFor %s %s: %v", u.ID, m, err)
	}
	return p
}

// key mints an API key for u naming scopes and returns u's principal for it,
// as the key authenticator builds one (CredentialID the key's ID).
func (r *realAuth) key(u *auth.User, scopes ...rbac.Permission) *auth.Principal {
	r.t.Helper()
	_, k, err := r.svc.CreateAPIKey(context.Background(), r.as(u, auth.MethodSession), auth.KeySpec{Name: "batphone", Scopes: scopes})
	if err != nil {
		r.t.Fatalf("CreateAPIKey %s %v: %v", u.ID, scopes, err)
	}
	p := r.as(u, auth.MethodAPIKey, k.Scopes...)
	p.CredentialID = k.ID
	return p
}

// kernelRule is the verified-method rule F4-08 hands Deps.VerifiedMethod
// (05-rbac 4.10, SEC-24), written from what the kernel holds: an interactive
// session verifies; so does a method an app-level extension registered as
// interactive; an API key verifies only when the key's own record names one
// of the scopes the route needs, never through core:admin or an implication.
// A delegated session, a module's compat method and every unknown method do
// not.
func (r *realAuth) kernelRule() func(*auth.Principal, rbac.Requirement) bool {
	interactive := map[auth.Method]bool{extCF: true, extBadge: false}
	return func(p *auth.Principal, need rbac.Requirement) bool {
		switch {
		case p == nil || p.User == nil:
			return false
		case p.Method.Interactive(), interactive[p.Method]:
			return true
		case p.Method == auth.MethodAPIKey:
			for k, err := range r.svc.APIKeys(context.Background(), p.User.ID) {
				if err == nil && k.ID == p.CredentialID {
					return slices.ContainsFunc(need.Permissions(), func(s rbac.Permission) bool { return slices.Contains(k.Scopes, s) })
				}
			}
		}
		return false
	}
}

type noHolders struct{}

func (noHolders) HoldersOf(context.Context, *storage.Batch, []rbac.RoleName) ([]rbac.Holder, error) {
	return nil, nil
}

type sinkAudit struct{}

func (*sinkAudit) Record(context.Context, audit.Event) error {
	return errors.New("audited with RecordIn")
}
func (*sinkAudit) RecordIn(_ context.Context, b *storage.Batch, _ audit.Event) error {
	if b == nil {
		return errors.New("RecordIn without the change's batch")
	}
	return nil
}
func (*sinkAudit) Emit(context.Context, audit.Event) {}

type sinkJournal struct{}

func (*sinkJournal) Append(_ context.Context, b *storage.Batch, _ journal.Revocation) error {
	if b == nil {
		return errors.New("Append without the revoking batch")
	}
	return nil
}
func (*sinkJournal) Gap(b *storage.Batch, _ time.Time) error {
	if b == nil {
		return errors.New("Gap without the revoking batch")
	}
	return nil
}

// cast is the cast of the approval cells: a warden who approves, a keeper
// who runs but may not approve, a site admin whose core:admin implies the
// approval scope without naming it.
type cast struct {
	*realAuth
	lucius, edward, selina *auth.User
}

func newCast(t testing.TB) *cast {
	t.Helper()
	r := newRealAuth(t)
	admin := r.rb.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{rbac.RoleAdmin}, Interactive: true})
	if _, err := r.rb.CreateRole(context.Background(), admin, rbac.RoleInput{
		Name: "custom.everything", Title: "Everything", Permissions: []rbac.Permission{"core:self", "batcave:run", "batcave:view", "arkham:approve"},
	}); err != nil {
		t.Fatal(err)
	}
	return &cast{
		realAuth: r,
		lucius:   r.user("lucius", "arkham.warden", "batcave.keeper"),
		edward:   r.user("edward", "batcave.watcher"),
		selina:   r.user("selina", rbac.RoleAdmin),
	}
}

// TestApprove_OverCoreAuth is the approval row over real principals: the
// session, the interactive extension, the key naming the scope, and every
// refusal 05-tasks 5T.3.7 lists (a key implying it through core:admin, a
// delegated session, a non-interactive extension, a module's method, and a
// forwarded header that is never identity).
func TestApprove_OverCoreAuth(t *testing.T) {
	c := newCast(t)
	cells := []struct {
		name     string
		as       *auth.Principal
		headers  map[string]string
		status   int
		code     string
		approved bool
	}{
		{name: "interactive session holding the scope", as: c.as(c.lucius, auth.MethodSession), status: 200, approved: true},
		{name: "interactive extension holding the scope", as: c.as(c.lucius, extCF), status: 200, approved: true},
		{name: "key naming the approval scope", as: c.key(c.lucius, "arkham:approve", "batcave:view"), status: 200, approved: true},
		{name: "session without the scope", as: c.as(c.edward, auth.MethodSession), status: 403, code: "permission_denied"},
		{name: "extension without the scope", as: c.as(c.edward, extCF), status: 403, code: "permission_denied"},
		{name: "key that does not name the scope", as: c.key(c.edward, "batcave:view"), status: 403, code: "permission_denied"},
		{name: "key implying the scope through core:admin", as: c.key(c.selina, "core:admin"), status: 403, code: "tasks.verified_method_required"},
		{name: "key naming core:admin and the scope is still a name", as: c.key(c.selina, "core:admin", "arkham:approve"), status: 200, approved: true},
		{name: "delegated session", as: c.as(c.lucius, auth.MethodSessionDelegated), status: 403, code: "tasks.verified_method_required"},
		{name: "non-interactive extension", as: c.as(c.lucius, extBadge), status: 403, code: "tasks.verified_method_required"},
		{name: "forwarded header with no principal", headers: map[string]string{"X-Forwarded-User": "lucius"}, status: 401, code: "unauthenticated"},
	}
	// A module's compat method is registered once; its principal never verifies.
	if _, err := c.svc.RegisterCompat("arkham", auth.MethodSpec{Method: gateWay}); err != nil {
		t.Fatal(err)
	}
	cells = append(cells, struct {
		name     string
		as       *auth.Principal
		headers  map[string]string
		status   int
		code     string
		approved bool
	}{name: "module compat method", as: c.as(c.lucius, gateWay), status: 403, code: "tasks.verified_method_required"})

	for _, cell := range cells {
		t.Run(cell.name, func(t *testing.T) {
			g := newRig(t, func(d *Deps) { d.VerifiedMethod = c.kernelRule() })
			planRun(g, "run-plan")
			q := approveReq("run-plan", `{"rows": ["cell-7"]}`, cell.as)
			q.headers = cell.headers
			rec := g.do(q)
			if cell.code != "" {
				wantProblem(t, rec, cell.status, cell.code)
			} else if rec.Code != cell.status {
				t.Fatalf("= %d, want %d\n%s", rec.Code, cell.status, rec.Body.String())
			}
			if got := len(g.appr.calls) == 1; got != cell.approved {
				t.Fatalf("Approve called = %v, want %v", got, cell.approved)
			}
		})
	}
}

// TestApprove_CoreDefaultRefusesWhatOnlyTheKernelWidens: with no
// VerifiedMethod the core default verifies an interactive session and
// nothing else, real extension and key principals included.
func TestApprove_CoreDefaultRefusesWhatOnlyTheKernelWidens(t *testing.T) {
	c := newCast(t)
	for name, as := range map[string]*auth.Principal{
		"interactive extension": c.as(c.lucius, extCF),
		"key naming the scope":  c.key(c.lucius, "arkham:approve"),
	} {
		t.Run(name, func(t *testing.T) {
			g := newRig(t)
			planRun(g, "run-plan")
			wantProblem(t, g.do(approveReq("run-plan", `{}`, as)), 403, "tasks.verified_method_required")
			if len(g.appr.calls) != 0 {
				t.Fatalf("Approve called %d times", len(g.appr.calls))
			}
		})
	}
}

// TestRoutes_PermissionTableOverCoreAuth: every route against real
// principals. A session and a key holding exactly the route's floor pass the
// front door; a keeper holding other scopes, and a key naming those, are
// refused with the floor named; no principal is 401.
func TestRoutes_PermissionTableOverCoreAuth(t *testing.T) {
	c := newCast(t)
	g := newRig(t)
	g.client.add(view("run-1", kindSweep, tasks.StateSucceeded, "bruce"))
	admin := c.rb.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{rbac.RoleAdmin}, Interactive: true})
	watcher, watcherKey := c.as(c.edward, auth.MethodSession), c.key(c.edward, "batcave:view")
	for i, r := range g.srv.Routes() {
		t.Run(r.Method+" "+r.Path, func(t *testing.T) {
			rq := req{method: r.Method, path: concretePath(r.Path)}
			wantProblem(t, g.do(rq), 401, "unauthenticated")

			// A route open to any authenticated caller checks the
			// definition's own scopes in its handler, so its holder gets
			// them all; any other route's holder gets exactly its floor.
			floor, held := rbac.Permission("core:self"), []rbac.Permission{"core:self", "batcave:run", "batcave:view", "arkham:approve", scopeView, scopeManage}
			if r.Perm.Kind() != rbac.KindAuthenticated {
				floor = r.Perm.Permissions()[0]
				held = []rbac.Permission{floor}
			}
			role := rbac.RoleName(fmt.Sprintf("custom.floor-%d", i))
			if _, err := c.rb.CreateRole(context.Background(), admin, rbac.RoleInput{Name: role, Title: "Floor", Permissions: held}); err != nil {
				t.Fatalf("CreateRole %v: %v", held, err)
			}
			holder := c.user(fmt.Sprintf("holder-%d", i), role)
			for name, as := range map[string]*auth.Principal{
				"session at the floor": c.as(holder, auth.MethodSession),
				"key at the floor":     c.key(holder, held...),
			} {
				if rec := g.do(withPrincipal(rq, as)); rec.Code == 401 || (rec.Code == 403 && code(t, rec) == "permission_denied") {
					t.Fatalf("%s was refused: %d %s", name, rec.Code, rec.Body.String())
				}
			}
			if r.Perm.Kind() == rbac.KindAuthenticated {
				return
			}
			if slices.Contains(watcher.Perms.Concrete(), floor) {
				return // the floor is one the watcher holds: nothing to refuse
			}
			for name, as := range map[string]*auth.Principal{
				"session holding only other scopes": watcher,
				"key naming only other scopes":      watcherKey,
			} {
				rec := g.do(withPrincipal(rq, as))
				wantProblem(t, rec, 403, "permission_denied")
				if !strings.Contains(rec.Body.String(), r.Perm.String()) {
					t.Fatalf("%s: the refusal does not name %s: %s", name, r.Perm, rec.Body.String())
				}
			}
		})
	}
}

func withPrincipal(r req, p *auth.Principal) req {
	r.as = p
	return r
}
