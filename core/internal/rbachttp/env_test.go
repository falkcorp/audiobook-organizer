// file: core/internal/rbachttp/env_test.go
// version: 1.0.0
// guid: 4145811e-837f-44de-a19d-8a62d5929e1c
// last-edited: 2026-11-10

package rbachttp

import (
	"context"
	"errors"
	"iter"
	"log/slog"
	"net/netip"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/internal/authz"
	"github.com/falkcorp/mediacore/internal/rbacstore"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// The tests open their store with storage.Open over MemFS (never
// coretest.NewStore, which the kernel's import graph must not reach) and
// build a real *rbac.Service over it, the way rbac's own tests do.

// openRoleStore boots a store holding the role keyspace.
func openRoleStore(t testing.TB) storage.Keyspace {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:            filepath.Join(t.TempDir(), "main.pebble"),
		Owners:          []storage.OwnerSpec{{Owner: rbacstore.Owner, Spec: rbacstore.StorageSpec(), Enabled: true}},
		Build:           "coretest+fixture",
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
	ks, ok := db.Keyspace(rbacstore.Owner)
	if !ok {
		t.Fatal("no role keyspace")
	}
	return ks
}

// auditLog is the service's audit.Logger: it keeps what the service records
// and what it emits, so a test can tell which side wrote a refusal.
type auditLog struct {
	mu       sync.Mutex
	recorded []audit.Event
	emitted  []audit.Event
}

func (a *auditLog) Record(context.Context, audit.Event) error {
	return errors.New("role changes are recorded with RecordIn")
}

func (a *auditLog) RecordIn(_ context.Context, _ *storage.Batch, e audit.Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recorded = append(a.recorded, e)
	return nil
}

func (a *auditLog) Emit(_ context.Context, e audit.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.emitted = append(a.emitted, e)
}

func (a *auditLog) denials() []audit.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.emitted)
}

// noHolders is a role-holder source with no users: every custom role is
// unassigned, so a delete can succeed.
type noHolders struct{}

func (noHolders) HoldersOf(context.Context, *storage.Batch, []rbac.RoleName) ([]rbac.Holder, error) {
	return nil, nil
}

// revJournal accepts revocation lines.
type revJournal struct{}

func (revJournal) Append(context.Context, *storage.Batch, journal.Revocation) error { return nil }
func (revJournal) Gap(*storage.Batch, time.Time) error                              { return nil }

// module is one module's declarations.
type module struct {
	id    string
	specs []rbac.PermissionSpec
	seeds []rbac.RoleSeed
}

// gotham is the module the tests declare: a plain scope both core roles get, a
// Sensitive scope, a settings scope, and one module role.
func gotham() module {
	return module{
		id: "gotham",
		specs: []rbac.PermissionSpec{
			{Name: "gotham:patrol", Title: "Patrol", Class: rbac.ClassView, DefaultGrants: []rbac.RoleName{rbac.RoleEditor, rbac.RoleViewer}},
			{Name: "gotham:signal", Title: "Bat-Signal", DefaultGrants: []rbac.RoleName{rbac.RoleEditor}},
			{Name: "gotham:cave.manage", Title: "Batcave", Sensitive: true},
			{Name: "gotham:settings.manage", Title: "Gotham settings"},
		},
		seeds: []rbac.RoleSeed{
			{Name: "gotham.detective", Title: "Detective", Permissions: []rbac.Permission{"gotham:patrol"}},
		},
	}
}

// service is a real rbac.Service over a store, with its fakes.
type service struct {
	*rbac.Service
	audit *auditLog
}

// boot builds a service over ks, declares mods, freezes and commits the seed
// roles. A second boot over the same ks is a restart with a new release.
func boot(t testing.TB, ks storage.Keyspace, mods ...module) *service {
	t.Helper()
	ctx := context.Background()
	st, err := rbacstore.New(ks, rbacstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	al := &auditLog{}
	svc, err := rbac.New(ctx, rbac.Options{Source: "gotham-test"}, rbac.Deps{
		Store: st, Compile: authz.Compile, Audit: al, Revocations: revJournal{}, NewBatch: st.NewBatch, Holders: noHolders{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, m := range mods {
		d := svc.Declarer(m.id)
		if err := d.Permissions(m.specs...); err != nil {
			t.Fatalf("declare %s permissions: %v", m.id, err)
		}
		if err := d.Roles(m.seeds...); err != nil {
			t.Fatalf("declare %s roles: %v", m.id, err)
		}
	}
	if err := svc.Freeze(ctx); err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	if err := svc.CommitSeeds(ctx); err != nil {
		t.Fatalf("CommitSeeds: %v", err)
	}
	return &service{Service: svc, audit: al}
}

// realAPI is the handlers over a service booted with gotham and a seed
// release that leaves gotham.detective with pending permissions.
func realAPI(t testing.TB) (*api, *service) {
	t.Helper()
	ks := openRoleStore(t)
	boot(t, ks, gotham())
	next := gotham()
	next.seeds[0].Permissions = append(next.seeds[0].Permissions, "gotham:cave.manage", "gotham:settings.manage", "gotham:signal")
	svc := boot(t, ks, next)
	return &api{roles: svc, audit: svc.audit, log: slog.New(slog.DiscardHandler), base: "/api/core/v1"}, svc
}

// session is a principal on a session of method m holding roles; its checker
// is the service's, so Sensitive scopes follow §4.4.
func (s *service) principal(m auth.Method, roles ...rbac.RoleName) *auth.Principal {
	return &auth.Principal{
		User:   &auth.User{ID: "u-bruce", Username: "bruce"},
		Method: m,
		Perms:  s.Compile(rbac.CheckerInput{Roles: roles, Interactive: m.Interactive()}),
	}
}

// admin is bruce on an interactive session holding admin.
func (s *service) admin() *auth.Principal { return s.principal(auth.MethodSession, rbac.RoleAdmin) }

// in wraps req as the router would hand it to a typed handler.
func in[Req any](p *auth.Principal, req Req) *httpapi.In[Req] {
	return &httpapi.In[Req]{Req: req, Principal: p}
}

// fakeRoles is a RoleService whose answers a test sets. It records the actor
// and arguments the handlers pass.
type fakeRoles struct {
	mu    sync.Mutex
	roles []*rbac.Role
	err   error // returned by every call that has no row-specific answer
	rows  func(yield func(*rbac.Role, error) bool)

	actor   rbac.Checker
	created rbac.RoleInput
	updated rbac.RoleInput
	version int64
	calls   []string
}

func (f *fakeRoles) note(call string) {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
}

func (f *fakeRoles) Roles(context.Context) iter.Seq2[*rbac.Role, error] {
	f.note("Roles")
	if f.rows != nil {
		return f.rows
	}
	return func(yield func(*rbac.Role, error) bool) {
		for _, r := range f.roles {
			if !yield(r, nil) {
				return
			}
		}
	}
}

func (f *fakeRoles) Role(_ context.Context, id rbac.RoleName) (*rbac.Role, error) {
	f.note("Role")
	for _, r := range f.roles {
		if r.Name == id {
			return r, nil
		}
	}
	return nil, rbac.ErrUnknownRole
}

func (f *fakeRoles) CreateRole(_ context.Context, actor rbac.Checker, in rbac.RoleInput) (*rbac.Role, error) {
	f.note("CreateRole")
	f.actor, f.created = actor, in
	if f.err != nil {
		return nil, f.err
	}
	return &rbac.Role{Name: in.Name, Title: in.Title, Description: in.Description, Origin: rbac.OriginCustom, Added: in.Permissions, Version: 1}, nil
}

func (f *fakeRoles) UpdateRole(_ context.Context, actor rbac.Checker, id rbac.RoleName, in rbac.RoleInput, ifVersion int64) (*rbac.Role, error) {
	f.note("UpdateRole")
	f.actor, f.updated, f.version = actor, in, ifVersion
	if f.err != nil {
		return nil, f.err
	}
	return &rbac.Role{Name: id, Title: in.Title, Description: in.Description, Origin: rbac.OriginCustom, Added: in.Permissions, Version: ifVersion + 1}, nil
}

func (f *fakeRoles) DeleteRole(_ context.Context, actor rbac.Checker, _ rbac.RoleName, ifVersion int64) error {
	f.note("DeleteRole")
	f.actor, f.version = actor, ifVersion
	return f.err
}

func (f *fakeRoles) AcceptSeed(_ context.Context, actor rbac.Checker, id rbac.RoleName, ifVersion int64) (*rbac.Role, error) {
	f.note("AcceptSeed")
	f.actor, f.version = actor, ifVersion
	if f.err != nil {
		return nil, f.err
	}
	return &rbac.Role{Name: id, Origin: rbac.OriginSeed, Version: ifVersion + 1}, nil
}

// emitted records Emit calls.
type emitted struct {
	mu sync.Mutex
	ev []audit.Event
}

func (e *emitted) Emit(_ context.Context, ev audit.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ev = append(e.ev, ev)
}

func (e *emitted) all() []audit.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.ev)
}

// mustAddr parses a documentation-range address.
func mustAddr(t testing.TB, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
