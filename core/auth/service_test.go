// file: core/auth/service_test.go
// version: 1.1.0
// guid: 298429ba-81c3-4473-ac04-17d50cc5f897
// last-edited: 2026-11-10

package auth_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/archtest"
	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/auth/password"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/internal/authstore"
	"github.com/falkcorp/mediacore/internal/authz"
	"github.com/falkcorp/mediacore/internal/rbacstore"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// fx is one booted auth service over a fresh in-memory store: the native
// identity store, a frozen rbac.Service compiling through authz.Compile (as
// the kernel wires it), and recording audit and journal fakes.
type fx struct {
	t     testing.TB
	st    *authstore.Store
	rb    *rbac.Service
	svc   *auth.Service
	audit *auditFake
	jrnl  *journalFake
	// hasher is a lowered-cost hasher (test binaries only) whose derivations
	// the sign-in tests count.
	hasher *password.Hasher
	// loginSleep, when set, replaces the sign-in delays of every service
	// rebuild builds.
	loginSleep func(time.Duration)

	mu       sync.Mutex
	now      time.Time
	settings auth.Settings
}

// liveSettings is the Deps.Settings the fixture's service reads.
func (f *fx) liveSettings() auth.Settings {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.settings
}

// setSettings replaces the settings the service reads from its next call on.
func (f *fx) setSettings(st auth.Settings) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settings = st
}

func (f *fx) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fx) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// roleCommissioner manages users without being an admin.
const roleCommissioner rbac.RoleName = "custom.commissioner"

// gothamPerms are the fixture module's scopes.
var gothamPerms = []rbac.PermissionSpec{
	{Name: "gotham:patrol", Title: "Patrol", Class: rbac.ClassView, DefaultGrants: []rbac.RoleName{rbac.RoleEditor, rbac.RoleViewer}},
	{Name: "gotham:signal", Title: "Bat-Signal", DefaultGrants: []rbac.RoleName{rbac.RoleEditor}},
	{Name: "gotham:cave.manage", Title: "Batcave", Sensitive: true},
	{Name: "gotham:cases.read", Title: "Case files", Class: rbac.ClassView},
	{Name: "gotham:relay.worker", Title: "Relay worker", Confining: true},
}

func newFx(t testing.TB) *fx {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: filepath.Join(t.TempDir(), "main.pebble"),
		Owners: []storage.OwnerSpec{
			{Owner: authstore.Owner, Spec: authstore.StorageSpec(), Enabled: true},
			{Owner: rbacstore.Owner, Spec: rbacstore.StorageSpec(), Enabled: true},
		},
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
	f := &fx{t: t, audit: &auditFake{}, jrnl: &journalFake{}, now: time.Date(2026, 11, 10, 21, 0, 0, 0, time.UTC),
		hasher: password.New(password.Options{Time: 1, MemoryKiB: 64})}
	aks, _ := db.Keyspace(authstore.Owner)
	rks, _ := db.Keyspace(rbacstore.Owner)
	if f.st, err = authstore.New(aks, authstore.Options{Now: f.clock}); err != nil {
		t.Fatal(err)
	}
	rst, err := rbacstore.New(rks, rbacstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if f.rb, err = rbac.New(ctx, rbac.Options{Source: "gotham-test"}, rbac.Deps{
		Store: rst, Compile: authz.Compile, Audit: &auditFake{}, Revocations: &journalFake{},
		NewBatch: rst.NewBatch, Holders: holders{f.st},
	}); err != nil {
		t.Fatal(err)
	}
	d := f.rb.Declarer("gotham", "relay-worker")
	if err := d.Permissions(gothamPerms...); err != nil {
		t.Fatal(err)
	}
	if err := d.Roles(
		rbac.RoleSeed{Name: "gotham.detective", Title: "Detective", Permissions: []rbac.Permission{"gotham:patrol", "gotham:cases.read"}},
		rbac.RoleSeed{Name: "relay-worker", Title: "Relay worker", Permissions: []rbac.Permission{"gotham:relay.worker"}},
	); err != nil {
		t.Fatal(err)
	}
	if err := f.rb.Freeze(ctx); err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	if err := f.rb.CommitSeeds(ctx); err != nil {
		t.Fatalf("CommitSeeds: %v", err)
	}
	// A user manager below admin: a custom role, since module seeds may not
	// grant core scopes, created by a signed-in admin's checker.
	admin := f.rb.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{rbac.RoleAdmin}, Interactive: true})
	if _, err := f.rb.CreateRole(ctx, admin, rbac.RoleInput{
		Name: roleCommissioner, Title: "Commissioner",
		Permissions: []rbac.Permission{"gotham:patrol", "gotham:signal", "gotham:cases.read", "core:users.manage", "core:users.view"},
	}); err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	f.rebuild()
	return f
}

// rebuild builds f.svc anew over the same store, as a restart would: the
// fixture stores users behind the service, and HasUsers is a flag New sets.
func (f *fx) rebuild() {
	f.t.Helper()
	svc, err := auth.New(context.Background(), auth.Options{ImportedKeyPrefixes: []string{"abk_"}}, auth.Deps{
		Store: f.st, NewBatch: f.st.NewBatch, RBAC: f.rb, Audit: f.audit, Revocations: f.jrnl, Clock: f.clock,
		Hasher: f.hasher, Settings: f.liveSettings,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if f.loginSleep != nil {
		svc.SetLoginSleep(f.loginSleep)
	}
	f.svc = svc
}

// user stores an active user named name holding roles.
func (f *fx) user(name string, roles ...rbac.RoleName) *auth.User {
	f.t.Helper()
	u := &auth.User{
		ID: auth.UserID("u-" + name), Username: name, Roles: roles, Status: auth.StatusActive,
		CreatedAt: f.clock(), UpdatedAt: f.clock(), Version: 1,
	}
	f.commit(func(b *storage.Batch) error { return f.st.PutUser(b, u) })
	if !f.svc.HasUsers() {
		f.rebuild()
	}
	return f.reload(u.ID)
}

// setRoles replaces id's stored roles (a demotion or promotion behind the
// service's back, as rbac or an admin would).
func (f *fx) setRoles(id auth.UserID, roles ...rbac.RoleName) *auth.User {
	f.t.Helper()
	u := f.reload(id)
	u.Roles, u.Version = roles, u.Version+1
	f.commit(func(b *storage.Batch) error { return f.st.PutUser(b, u) })
	return f.reload(id)
}

func (f *fx) reload(id auth.UserID) *auth.User {
	f.t.Helper()
	u, err := f.st.GetUser(context.Background(), id)
	if err != nil {
		f.t.Fatalf("GetUser %s: %v", id, err)
	}
	return u
}

func (f *fx) commit(stage func(b *storage.Batch) error) {
	f.t.Helper()
	b := f.st.NewBatch()
	defer func() { _ = b.Close() }()
	if err := stage(b); err != nil {
		f.t.Fatal(err)
	}
	if err := b.Commit(context.Background(), storage.Sync); err != nil {
		f.t.Fatal(err)
	}
}

// session is u's interactive session principal.
func (f *fx) session(u *auth.User) *auth.Principal {
	f.t.Helper()
	p, err := f.svc.PrincipalFor(context.Background(), u, auth.MethodSession, nil, u.CredentialEpoch)
	if err != nil {
		f.t.Fatalf("PrincipalFor session %s: %v", u.ID, err)
	}
	return p
}

// keyPrincipal is the principal a request presenting k gets.
func (f *fx) keyPrincipal(u *auth.User, k *auth.APIKey) *auth.Principal {
	f.t.Helper()
	p, err := f.svc.PrincipalFor(context.Background(), u, auth.MethodAPIKey, k.Scopes, u.CredentialEpoch)
	if err != nil {
		f.t.Fatalf("PrincipalFor key %s: %v", k.ID, err)
	}
	p.CredentialID = k.ID
	return p
}

// mint creates a key for owner through the actor, failing the test on error.
func (f *fx) mint(actor *auth.Principal, owner auth.UserID, scopes ...rbac.Permission) (string, *auth.APIKey) {
	f.t.Helper()
	raw, k, err := f.svc.CreateAPIKey(context.Background(), actor, auth.KeySpec{Owner: owner, Name: "utility belt", Scopes: scopes})
	if err != nil {
		f.t.Fatalf("CreateAPIKey %v: %v", scopes, err)
	}
	return raw, k
}

func (f *fx) key(id string) *auth.APIKey {
	f.t.Helper()
	k, err := f.st.APIKeyByID(context.Background(), id)
	if err != nil {
		f.t.Fatalf("APIKeyByID %s: %v", id, err)
	}
	return k
}

// holders is rbac's role-assignment view over the identity store, as the
// kernel builds it.
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

// auditFake records what is audited; auth stages state changes with RecordIn.
type auditFake struct {
	mu       sync.Mutex
	recorded []audit.Event
	emitted  []audit.Event
	// hold, when set, runs at the start of every RecordIn: the change's
	// batch is staged but not committed, so a test can line writes up.
	hold func(audit.Event)
}

func (a *auditFake) Record(context.Context, audit.Event) error {
	return errors.New("state changes are audited in their batch with RecordIn")
}

func (a *auditFake) RecordIn(_ context.Context, b *storage.Batch, e audit.Event) error {
	if b == nil {
		return errors.New("RecordIn without the change's batch")
	}
	a.mu.Lock()
	hold := a.hold
	a.mu.Unlock()
	if hold != nil {
		hold(e)
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

// journalFake records revocation lines; appendErr makes Append fail.
type journalFake struct {
	mu        sync.Mutex
	lines     []journal.Revocation
	gaps      int
	appendErr error
}

func (j *journalFake) Append(_ context.Context, b *storage.Batch, r journal.Revocation) error {
	if b == nil {
		return errors.New("Append without the revoking batch")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.appendErr != nil {
		return j.appendErr
	}
	j.lines = append(j.lines, r)
	return nil
}

func (j *journalFake) Gap(b *storage.Batch, _ time.Time) error {
	if b == nil {
		return errors.New("Gap without the revoking batch")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.gaps++
	return nil
}

// recordedActions lists the actions recorded in a batch, in order.
func (a *auditFake) recordedActions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, len(a.recorded))
	for i, e := range a.recorded {
		out[i] = e.Action
	}
	return out
}

func (j *journalFake) count(k journal.Kind) int {
	j.mu.Lock()
	defer j.mu.Unlock()
	n := 0
	for _, l := range j.lines {
		if l.Kind == k {
			n++
		}
	}
	return n
}

// TestNoGlobals (03 R5): auth, its password hasher and the native store hold
// no package-level mutable state and no init function.
func TestNoGlobals(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := archtest.ModuleRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	archtest.NoInit(t, root, "./auth/...")
	archtest.NoInit(t, root, "./internal/authstore/...")
}

// The service refuses to build without the collaborators every method needs,
// and New writes nothing.
func TestNew_RequiresDepsAndWritesNothing(t *testing.T) {
	f := newFx(t)
	if _, err := auth.New(context.Background(), auth.Options{}, auth.Deps{}); err == nil {
		t.Fatal("New built a service with no store, rbac, audit or journal")
	}
	if f.svc.HasUsers() {
		t.Fatal("HasUsers on an empty store")
	}
	f.user("bruce", rbac.RoleAdmin)
	svc, err := auth.New(context.Background(), auth.Options{}, auth.Deps{
		Store: f.st, NewBatch: f.st.NewBatch, RBAC: f.rb, Audit: f.audit, Revocations: f.jrnl,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !svc.HasUsers() {
		t.Fatal("HasUsers false with a stored user")
	}
	if len(f.audit.recorded) != 0 || len(f.jrnl.lines) != 0 {
		t.Fatalf("New wrote: audit %d, journal %d", len(f.audit.recorded), len(f.jrnl.lines))
	}
}
