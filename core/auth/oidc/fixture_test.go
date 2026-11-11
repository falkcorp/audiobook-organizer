// file: core/auth/oidc/fixture_test.go
// version: 1.0.0
// guid: b1d75824-9a46-4e31-a57d-84cb5a5a7945
// last-edited: 2026-11-11

package oidc

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"sync"
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
)

// fx is one booted identity store and auth service over an in-memory store,
// with recording audit and journal fakes, and a fake provider.
type fx struct {
	t     testing.TB
	st    *authstore.Store
	rb    *rbac.Service
	svc   *auth.Service
	audit *auditFake

	mu  sync.Mutex
	now time.Time
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

// roleSignal is a custom role that is not an admin.
const roleSignal rbac.RoleName = "custom.signal-operator"

func newFx(t testing.TB) *fx {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: filepath.Join(t.TempDir(), "main.pebble"),
		Owners: []storage.OwnerSpec{
			{Owner: authstore.Owner, Spec: authstore.StorageSpec(), Enabled: true},
			{Owner: rbacstore.Owner, Spec: rbacstore.StorageSpec(), Enabled: true},
		},
		Build: "coretest+fixture", FreeSpaceMargin: 1, FS: storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		_ = o.Close()
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	f := &fx{t: t, audit: &auditFake{}, now: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)}
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
	if err := f.rb.Freeze(ctx); err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	if err := f.rb.CommitSeeds(ctx); err != nil {
		t.Fatalf("CommitSeeds: %v", err)
	}
	admin := f.rb.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{rbac.RoleAdmin}, Interactive: true})
	if _, err := f.rb.CreateRole(ctx, admin, rbac.RoleInput{
		Name: roleSignal, Title: "Signal operator", Permissions: []rbac.Permission{"core:users.view"},
	}); err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	f.svc, err = auth.New(ctx, auth.Options{}, auth.Deps{
		Store: f.st, NewBatch: f.st.NewBatch, RBAC: f.rb, Audit: f.audit, Revocations: &journalFake{},
		Clock: f.clock, Hasher: password.New(password.Options{Time: 1, MemoryKiB: 64}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// resolver is a Resolver over the fixture.
func (f *fx) resolver(extra func(context.Context, string) (bool, error)) *Resolver {
	f.t.Helper()
	r, err := NewResolver(ResolverDeps{
		Store: f.st, NewBatch: f.st.NewBatch, Audit: f.audit, Roles: f.rb, Accounts: f.svc,
		ExtraAllowed: extra, Clock: f.clock,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

// put stores a user.
func (f *fx) put(u *auth.User) *auth.User {
	f.t.Helper()
	if u.Version == 0 {
		u.Version = 1
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt, u.UpdatedAt = f.clock(), f.clock()
	}
	if u.Status == "" {
		u.Status = auth.StatusActive
	}
	b := f.st.NewBatch()
	defer func() { _ = b.Close() }()
	if err := f.st.PutUser(b, u); err != nil {
		f.t.Fatal(err)
	}
	if err := b.Commit(context.Background(), storage.Sync); err != nil {
		f.t.Fatal(err)
	}
	f.svc.NoteUserCreated()
	return u
}

// withPassword stores u with a password hash.
func (f *fx) withPassword(u *auth.User) *auth.User {
	f.t.Helper()
	u.HasPassword = true
	if u.Status == "" {
		u.Status = auth.StatusActive
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt, u.UpdatedAt = f.clock(), f.clock()
	}
	b := f.st.NewBatch()
	defer func() { _ = b.Close() }()
	if u.Version == 0 {
		u.Version = 1
	}
	if err := f.st.SetPassword(b, u, "$argon2id$v=19$m=64,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaGhhc2g"); err != nil {
		f.t.Fatal(err)
	}
	if err := b.Commit(context.Background(), storage.Sync); err != nil {
		f.t.Fatal(err)
	}
	return u
}

func (f *fx) user(id string) *auth.User {
	f.t.Helper()
	u, err := f.st.GetUser(context.Background(), auth.UserID(id))
	if err != nil {
		f.t.Fatalf("GetUser %s: %v", id, err)
	}
	return u
}

func (f *fx) identity(provider, subject string) (*auth.ExternalIdentity, error) {
	return f.st.IdentityBySubject(context.Background(), provider, subject)
}

func (f *fx) countUsers() int {
	n := 0
	for _, err := range f.st.ScanUsers(context.Background()) {
		if err != nil {
			f.t.Fatal(err)
		}
		n++
	}
	return n
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

func (a *auditFake) actions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, len(a.recorded))
	for i, e := range a.recorded {
		out[i] = e.Action
	}
	return out
}

func (a *auditFake) emittedWith(action, reason string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, e := range a.emitted {
		if e.Action == action && e.Reason == reason {
			n++
		}
	}
	return n
}

type journalFake struct{}

func (journalFake) Append(_ context.Context, b *storage.Batch, _ journal.Revocation) error {
	if b == nil {
		return errors.New("Append without the revoking batch")
	}
	return nil
}

func (journalFake) Gap(b *storage.Batch, _ time.Time) error {
	if b == nil {
		return errors.New("Gap without the revoking batch")
	}
	return nil
}

// queryParam is the value of key in the query of raw.
func queryParam(t testing.TB, raw, key string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get(key)
}

// roundTrip is an http.RoundTripper made of a func.
type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
