// file: core/auth/lastadmin_roleguard_wrap_test.go
// version: 1.1.0
// guid: 8e2c4a17-3b95-4d60-a1f8-5c70d9e3b246
// last-edited: 2026-11-19

package auth_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// guardingRoleStore is a role store wrapped the way a deployment may wrap
// it (a metrics or tracing layer): reads and writes pass through, and the
// rbac.RoleGuarder methods are forwarded explicitly. A wrapper that embeds
// only rbac.Store hides them, and every user write then fails closed with
// rbac.ErrNoRoleGuard (bareRoleStore above).
//
// afterRead, when set, runs once, after GetRole returned role name: it is a
// role edit landing between auth's read of that role and its commit, with
// nothing else of auth's decision left to read.
type guardingRoleStore struct {
	rbac.Store
	guard rbac.RoleGuarder

	mu        sync.Mutex
	name      rbac.RoleName
	afterRead func()
}

func newGuardingRoleStore(s rbac.Store) *guardingRoleStore {
	return &guardingRoleStore{Store: s, guard: s.(rbac.RoleGuarder)}
}

func (g *guardingRoleStore) RoleSetGeneration(ctx context.Context) (uint64, error) {
	return g.guard.RoleSetGeneration(ctx)
}

func (g *guardingRoleStore) GuardRoleSet(b *storage.Batch, generation uint64) error {
	return g.guard.GuardRoleSet(b, generation)
}

func (g *guardingRoleStore) ClaimRoleSet(b *storage.Batch, generation uint64) error {
	return g.guard.ClaimRoleSet(b, generation)
}

// armAfterRead runs fn once, right after the next GetRole of name returns.
func (g *guardingRoleStore) armAfterRead(name rbac.RoleName, fn func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.name, g.afterRead = name, fn
}

func (g *guardingRoleStore) GetRole(ctx context.Context, name rbac.RoleName) (*rbac.Role, error) {
	r, err := g.Store.GetRole(ctx, name)
	g.mu.Lock()
	fn := g.afterRead
	if fn != nil && name == g.name {
		g.afterRead = nil
	} else {
		fn = nil
	}
	g.mu.Unlock()
	if fn != nil {
		fn()
	}
	return r, err
}

// TestLastAdmin_RoleReadBeforeGenerationIsAConflict (round 2): the role-set
// generation is read before the first role is, so a role edit that lands right
// after auth read the one role its decision rests on, and before its commit,
// is seen at commit. A guard staged after the role reads would take the
// generation the edit produced and let the stale decision stand. The wrapped
// role store also shows that a wrapper which forwards the guard keeps user
// writes working.
func TestLastAdmin_RoleReadBeforeGenerationIsAConflict(t *testing.T) {
	var gs *guardingRoleStore
	w := newWayneFx(t, func(s rbac.Store) rbac.Store { gs = newGuardingRoleStore(s); return gs })
	bruce := w.f.user("bruce-wayne", rbac.RoleAdmin, roleWayne)
	me := w.f.session(bruce)
	var editErr error
	var edited atomic.Bool
	gs.armAfterRead(roleWayne, func() { editErr = w.stripAdmin(); edited.Store(true) })

	err := w.f.svc.SetRoles(context.Background(), me, bruce.ID, []rbac.RoleName{roleWayne})
	if !edited.Load() {
		t.Fatal("the role edit never ran after the read of roleWayne")
	}
	if editErr != nil {
		t.Fatalf("the role edit: %v", editErr)
	}
	if !errors.Is(err, auth.ErrLastAdmin) {
		t.Fatalf("SetRoles on a decision whose last role read went stale: err = %v, want ErrLastAdmin", err)
	}
	if n := w.activeAdmins(t); n != 1 {
		t.Fatalf("%d active admins, want 1", n)
	}
	// And a forwarding wrapper does not refuse an ordinary write.
	jason := w.f.user("jason-todd", "gotham.detective")
	if err := w.f.svc.SetStatus(context.Background(), me, jason.ID, auth.StatusDisabled); err != nil {
		t.Fatalf("disabling a detective over a wrapper that forwards the guard: %v", err)
	}
}
