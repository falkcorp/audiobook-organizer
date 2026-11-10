// file: core/rbac/helpers_test.go
// version: 1.0.0
// guid: 7e2a9c41-6b3d-4f85-a0e7-5c1d8b2f9a36
// last-edited: 2026-11-10

package rbac_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/internal/rbacstore"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
)

// usersOwner holds the stand-in for auth's user records that the Holders
// fake reads through the role write's batch.
const usersOwner storage.Owner = "core/auth"

// usersPrefix is the Serialized prefix of the user rows.
func usersPrefix() []byte { return keys.Prefix(keys.Str("user")) }

func userKey(id string) []byte { return keys.Join(keys.Str("user"), keys.Str(id)) }

// env is one booted store holding the role keyspace and the user keyspace.
// It survives Service instances, so a test can boot the service again over
// the same data the way a restart would.
type env struct {
	db    *storage.DB
	roles storage.Keyspace
	users storage.Keyspace
}

func newEnv(t testing.TB) *env {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: filepath.Join(t.TempDir(), "main.pebble"),
		Owners: []storage.OwnerSpec{
			{Owner: rbacstore.Owner, Spec: rbacstore.StorageSpec(), Enabled: true},
			{Owner: usersOwner, Spec: storage.Spec{Keyspace: true, Serialized: []string{string(usersPrefix())}}, Enabled: true},
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
	roles, ok := db.Keyspace(rbacstore.Owner)
	if !ok {
		t.Fatal("no role keyspace")
	}
	users, ok := db.Keyspace(usersOwner)
	if !ok {
		t.Fatal("no user keyspace")
	}
	return &env{db: db, roles: roles, users: users}
}

// store is the native role store over e, checking imports against cat.
func (e *env) store(t testing.TB, cat *rbac.Catalog) *rbacstore.Store {
	t.Helper()
	st, err := rbacstore.New(e.roles, rbacstore.Options{Catalog: cat})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// ---------------------------------------------------------------------------
// Fakes of the service's dependencies.

// auditLog records what the service audits.
type auditLog struct {
	mu        sync.Mutex
	recorded  []audit.Event
	emitted   []audit.Event
	recordErr error
}

func (a *auditLog) Record(_ context.Context, e audit.Event) error {
	return errors.New("the service records role changes with RecordIn, never Record")
}

func (a *auditLog) RecordIn(_ context.Context, b *storage.Batch, e audit.Event) error {
	if b == nil {
		return errors.New("RecordIn without the change's batch")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.recordErr != nil {
		return a.recordErr
	}
	a.recorded = append(a.recorded, e)
	return nil
}

func (a *auditLog) Emit(_ context.Context, e audit.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.emitted = append(a.emitted, e)
}

func (a *auditLog) actions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, len(a.recorded))
	for i, e := range a.recorded {
		out[i] = e.Action + " " + e.Target.ID
	}
	return out
}

func (a *auditLog) denials() []audit.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.emitted)
}

// revJournal records revocation lines and gaps.
type revJournal struct {
	mu        sync.Mutex
	lines     []journal.Revocation
	gaps      int
	appendErr error
	gapErr    error
}

func (j *revJournal) Append(_ context.Context, b *storage.Batch, r journal.Revocation) error {
	if b == nil {
		return errors.New("Append without the change's batch")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.appendErr != nil {
		return j.appendErr
	}
	j.lines = append(j.lines, r)
	return nil
}

func (j *revJournal) Gap(b *storage.Batch, _ time.Time) error {
	if b == nil {
		return errors.New("Gap without the change's batch")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.gaps++
	return j.gapErr
}

func (j *revJournal) kinds() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]string, len(j.lines))
	for i, r := range j.lines {
		out[i] = string(r.Kind) + " " + r.Subject.ID
	}
	return out
}

// compiler is a recording Deps.Compile: it counts calls, keeps the inputs and
// returns a checker over the role patterns and key scopes (role ∩ key is
// authz's; the stand-in takes the role patterns when Scopes is nil and the
// key scopes otherwise, which is enough for the service's tests).
type compiler struct {
	mu     sync.Mutex
	inputs []rbac.CompileInput
	nilOut bool
}

func (c *compiler) compile(in rbac.CompileInput, cat *rbac.Catalog) rbac.Checker {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inputs = append(c.inputs, in)
	if c.nilOut {
		return nil
	}
	held := in.RolePatterns
	if in.Scopes != nil {
		held = in.Scopes
	}
	return grant(cat, in.Interactive, held...)
}

func (c *compiler) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.inputs)
}

func (c *compiler) last(t testing.TB) rbac.CompileInput {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.inputs) == 0 {
		t.Fatal("Deps.Compile was never called")
	}
	return c.inputs[len(c.inputs)-1]
}

// checker is a test stand-in for authz's compiled checker, implementing the
// parts of 05-rbac §4.4 the service's rules lean on: a held pattern covers
// what it expands to, core:admin covers every concrete scope, implication is
// followed, a Sensitive scope is never held by a non-interactive principal
// (core:admin included), and Has of a pattern holds only for that pattern or
// a wider one held literally.
type checker struct {
	cat         *rbac.Catalog
	interactive bool
	held        []rbac.Permission
}

func grant(cat *rbac.Catalog, interactive bool, held ...rbac.Permission) *checker {
	return &checker{cat: cat, interactive: interactive, held: slices.Clone(held)}
}

func (c *checker) Has(p rbac.Permission) bool {
	pat, err := rbac.ParsePattern(string(p))
	if err != nil {
		return false
	}
	if pat.IsWildcard() {
		for _, h := range c.held {
			if h == pat || (rbac.Permission(h.Namespace()+":*") == h && h.Namespace() == pat.Namespace()) {
				return true
			}
		}
		return false
	}
	spec, ok := c.cat.Lookup(pat)
	if !ok || (spec.Sensitive && !c.interactive) {
		return false
	}
	return slices.Contains(c.Concrete(), pat)
}

func (c *checker) Satisfies(r rbac.Requirement) bool {
	switch r.Kind() {
	case rbac.KindPublic:
		return true
	case rbac.KindAllOf:
		for _, p := range r.Permissions() {
			if !c.Has(p) {
				return false
			}
		}
		return len(r.Permissions()) > 0
	case rbac.KindAnyOf:
		if slices.ContainsFunc(r.Permissions(), c.Has) {
			return true
		}
	}
	return false
}

func (c *checker) Confined() bool {
	return rbac.Confined(c.cat, c.held)
}

func (c *checker) Concrete() []rbac.Permission {
	var out []rbac.Permission
	add := func(p rbac.Permission) {
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	if rbac.GrantsAdmin(c.held) {
		for spec := range c.cat.All() {
			add(spec.Name)
		}
	}
	for _, h := range c.held {
		for _, p := range c.cat.Expand(h) {
			add(p)
		}
	}
	for i := 0; i < len(out); i++ {
		spec, _ := c.cat.Lookup(out[i])
		for _, imp := range spec.Implies {
			add(imp)
		}
	}
	slices.Sort(out)
	return out
}

// userRec is the stand-in for one auth user record.
type userRec struct {
	Roles  []rbac.RoleName `json:"roles"`
	Active bool            `json:"active"`
}

// users is the Holders fake: it reads every known user row through the role
// write's batch with GetForUpdate, so a concurrent change to a user row
// fails that batch's Commit, as auth's implementation must (05-rbac §9).
type users struct {
	e   *env
	mu  sync.Mutex
	ids []string
	// wait, when set, runs inside every HoldersOf call (the concurrency test's
	// barrier).
	wait func()
	err  error
}

func (u *users) HoldersOf(ctx context.Context, b *storage.Batch, roles []rbac.RoleName) ([]rbac.Holder, error) {
	u.mu.Lock()
	ids, wait, ferr := slices.Clone(u.ids), u.wait, u.err
	u.mu.Unlock()
	if wait != nil {
		wait()
	}
	if ferr != nil {
		return nil, ferr
	}
	b.Bind(u.e.users)
	var out []rbac.Holder
	for _, id := range ids {
		raw, err := b.GetForUpdate(ctx, u.e.users, userKey(id))
		if errors.Is(err, storage.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var rec userRec
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, err
		}
		for _, r := range rec.Roles {
			if slices.Contains(roles, r) {
				out = append(out, rbac.Holder{UserID: id, Active: rec.Active})
				break
			}
		}
	}
	return out, nil
}

// put writes user id's record, as auth would on an assignment or demotion.
func (u *users) put(t testing.TB, id string, rec userRec) {
	t.Helper()
	ctx := context.Background()
	u.mu.Lock()
	if !slices.Contains(u.ids, id) {
		u.ids = append(u.ids, id)
	}
	u.mu.Unlock()
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	b := u.e.users.NewBatch()
	defer func() {
		if err := b.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	if _, err := b.GetForUpdate(ctx, u.e.users, userKey(id)); err != nil && !errors.Is(err, storage.ErrNotFound) {
		t.Fatal(err)
	}
	if err := b.In(u.e.users).Set(userKey(id), raw); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatalf("write user %s: %v", id, err)
	}
}

// ---------------------------------------------------------------------------
// The fixture: a service over env with every fake wired.

type fixture struct {
	t       testing.TB
	e       *env
	svc     *rbac.Service
	store   *rbacstore.Store
	audit   *auditLog
	journal *revJournal
	comp    *compiler
	users   *users
	gens    *[]uint64
}

// module is one module's declarations.
type module struct {
	id       string
	imported []rbac.RoleName
	specs    []rbac.PermissionSpec
	seeds    []rbac.RoleSeed
}

type setup struct {
	modules   []module
	noHolders bool
	metrics   *metrics.Scope
	cacheSize int
}

// boot builds a service over e, declares mods, freezes and commits the seed
// roles: a whole P5..P7 boot.
func boot(t testing.TB, e *env, s setup) *fixture {
	t.Helper()
	f := build(t, e, s)
	f.declare(s.modules...)
	if err := f.svc.Freeze(context.Background()); err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	if err := f.svc.CommitSeeds(context.Background()); err != nil {
		t.Fatalf("CommitSeeds: %v", err)
	}
	return f
}

// build builds the service only.
func build(t testing.TB, e *env, s setup) *fixture {
	t.Helper()
	f := &fixture{t: t, e: e, audit: &auditLog{}, journal: &revJournal{}, comp: &compiler{}, gens: new([]uint64)}
	f.store = e.store(t, nil)
	f.users = &users{e: e}
	deps := rbac.Deps{
		Store:       f.store,
		Compile:     f.comp.compile,
		Audit:       f.audit,
		Revocations: f.journal,
		NewBatch:    f.store.NewBatch,
		Holders:     f.users,
		Metrics:     s.metrics,
		OnChange:    func(g uint64) { *f.gens = append(*f.gens, g) },
	}
	if s.noHolders {
		deps.Holders = nil
	}
	svc, err := rbac.New(context.Background(), rbac.Options{CacheSize: s.cacheSize, Source: "gotham-test"}, deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f.svc = svc
	return f
}

func (f *fixture) declare(mods ...module) {
	f.t.Helper()
	for _, m := range mods {
		d := f.svc.Declarer(m.id, m.imported...)
		if err := d.Permissions(m.specs...); err != nil {
			f.t.Fatalf("declare %s permissions: %v", m.id, err)
		}
		if err := d.Roles(m.seeds...); err != nil {
			f.t.Fatalf("declare %s roles: %v", m.id, err)
		}
	}
}

func (f *fixture) cat() *rbac.Catalog { return f.svc.Catalog() }

// role reads a stored role, fatal when absent.
func (f *fixture) role(name rbac.RoleName) *rbac.Role {
	f.t.Helper()
	r, err := f.store.GetRole(context.Background(), name)
	if err != nil {
		f.t.Fatalf("GetRole(%s): %v", name, err)
	}
	return r
}

// admin is an interactive core:admin principal.
func (f *fixture) admin() *checker { return grant(f.cat(), true, rbac.CoreAdmin) }

// as is a principal holding held, interactive.
func (f *fixture) as(held ...rbac.Permission) *checker { return grant(f.cat(), true, held...) }

// gotham is the synthetic module most tests declare: a plain scope granted to
// both core roles, an implied pair, a Sensitive scope, a settings scope, a
// Confining worker scope, a module role and an imported worker role.
func gotham() module {
	return module{
		id:       "gotham",
		imported: []rbac.RoleName{"relay-worker"},
		specs: []rbac.PermissionSpec{
			{Name: "gotham:patrol", Title: "Patrol", Class: rbac.ClassView, DefaultGrants: []rbac.RoleName{rbac.RoleEditor, rbac.RoleViewer}},
			{Name: "gotham:patrol.night", Title: "Night patrol", Implies: []rbac.Permission{"gotham:patrol"}, DefaultGrants: []rbac.RoleName{rbac.RoleEditor}},
			{Name: "gotham:signal", Title: "Bat-Signal", DefaultGrants: []rbac.RoleName{rbac.RoleEditor}},
			{Name: "gotham:cave.manage", Title: "Batcave", Sensitive: true},
			{Name: "gotham:settings.manage", Title: "Gotham settings"},
			{Name: "gotham:relay.worker", Title: "Relay worker", Confining: true},
		},
		seeds: []rbac.RoleSeed{
			{Name: "gotham.detective", Title: "Detective", Permissions: []rbac.Permission{"gotham:patrol", "gotham:patrol.night"}},
			{Name: "relay-worker", Title: "Relay worker", Permissions: []rbac.Permission{"gotham:relay.worker"}},
		},
	}
}

// perms is a shorthand for a permission list.
func perms(ps ...string) []rbac.Permission {
	out := make([]rbac.Permission, len(ps))
	for i, p := range ps {
		out[i] = rbac.Permission(p)
	}
	return out
}

// wantPerms fails unless got equals want (both taken as sets in order).
func wantPerms(t testing.TB, what string, got []rbac.Permission, want ...string) {
	t.Helper()
	w := perms(want...)
	if len(w) == 0 {
		w = nil
	}
	g := got
	if len(g) == 0 {
		g = nil
	}
	if !slices.Equal(g, w) {
		t.Errorf("%s = %v, want %v", what, g, w)
	}
}

// mustCreate creates a custom role as an interactive admin.
func (f *fixture) mustCreate(name string, ps ...string) *rbac.Role {
	f.t.Helper()
	r, err := f.svc.CreateRole(context.Background(), f.admin(), rbac.RoleInput{Name: rbac.RoleName(name), Title: name, Permissions: perms(ps...)})
	if err != nil {
		f.t.Fatalf("CreateRole(%s): %v", name, err)
	}
	return r
}

// errIs fails unless err matches want.
func errIs(t testing.TB, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Errorf("%s: err = %v, want %v", what, err, want)
	}
}

// unchanged fails unless the stored role still has version v.
func (f *fixture) unchanged(name rbac.RoleName, v int64) {
	f.t.Helper()
	if got := f.role(name).Version; got != v {
		f.t.Errorf("%s moved to version %d, want %d (a refused write must stage nothing)", name, got, v)
	}
}

func (f *fixture) String() string { return fmt.Sprintf("fixture(gen %d)", f.svc.Generation()) }
