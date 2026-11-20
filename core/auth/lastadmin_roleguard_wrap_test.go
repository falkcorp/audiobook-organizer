// file: core/auth/lastadmin_roleguard_wrap_test.go
// version: 1.5.0
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
	// genErr, when set, is what RoleSetGeneration answers: the role store
	// being unreadable at the one read every guarded write starts with.
	genErr error
	// guardErr, when set, is what GuardRoleSet answers: the role store
	// refusing to stage the guard after the generation was read fine.
	guardErr error
	// failGenAt, when above zero, makes the failGenAt-th RoleSetGeneration call
	// answer failGenErr and every other one succeed: the role store breaking at
	// one chosen read of a write that reads the generation more than once
	// (SetRoles reads it in keepAnAdmin, then again in claimRoles).
	failGenAt  int
	failGenErr error
	genCalls   int
	// getFaultRole, when set, makes GetRole of that role answer getFault: the
	// role store breaking on the one role a write is about to give, after
	// everything the write read before it was fine.
	getFaultRole rbac.RoleName
	getFault     error
}

func newGuardingRoleStore(s rbac.Store) *guardingRoleStore {
	return &guardingRoleStore{Store: s, guard: s.(rbac.RoleGuarder)}
}

func (g *guardingRoleStore) RoleSetGeneration(ctx context.Context) (uint64, error) {
	g.mu.Lock()
	err := g.genErr
	g.genCalls++
	if g.failGenAt > 0 && g.genCalls == g.failGenAt {
		err = g.failGenErr
	}
	g.mu.Unlock()
	if err != nil {
		return 0, err
	}
	return g.guard.RoleSetGeneration(ctx)
}

func (g *guardingRoleStore) GuardRoleSet(b *storage.Batch, generation uint64) error {
	g.mu.Lock()
	err := g.guardErr
	g.mu.Unlock()
	if err != nil {
		return err
	}
	return g.guard.GuardRoleSet(b, generation)
}

func (g *guardingRoleStore) ClaimRoleSet(b *storage.Batch, generation uint64) error {
	return g.guard.ClaimRoleSet(b, generation)
}

func (g *guardingRoleStore) GuardRoleClaims(b *storage.Batch) error {
	return g.guard.GuardRoleClaims(b)
}

// armAfterRead runs fn once, right after the next GetRole of name returns.
func (g *guardingRoleStore) armAfterRead(name rbac.RoleName, fn func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.name, g.afterRead = name, fn
}

func (g *guardingRoleStore) GetRole(ctx context.Context, name rbac.RoleName) (*rbac.Role, error) {
	g.mu.Lock()
	if g.getFault != nil && name == g.getFaultRole {
		err := g.getFault
		g.mu.Unlock()
		return nil, err
	}
	g.mu.Unlock()
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

// TestLastAdmin_GuardThatCannotBeStagedRefusesTheWrite (round 2): the role
// store read the generation and then could not stage the guard on it (a read
// through the batch failed, or the generation moved on the spot). The write is
// refused with that error and changes nothing: an unstaged guard is no guard,
// and the rule must not go on to decide on role definitions nothing watches.
// Disabling a detective, who holds no admin, is the case that shows it, since
// the guard is staged before the rule looks at anyone's roles.
func TestLastAdmin_GuardThatCannotBeStagedRefusesTheWrite(t *testing.T) {
	var gs *guardingRoleStore
	w := newWayneFx(t, func(s rbac.Store) rbac.Store { gs = newGuardingRoleStore(s); return gs })
	bruce := w.f.user("bruce-wayne", rbac.RoleAdmin)
	jason := w.f.user("jason-todd", "gotham.detective")
	boom := errors.New("the role keyspace refused the guard")
	gs.mu.Lock()
	gs.guardErr = boom
	gs.mu.Unlock()

	ctx := context.Background()
	if err := w.f.svc.SetStatus(ctx, w.f.session(bruce), jason.ID, auth.StatusDisabled); !errors.Is(err, boom) {
		t.Fatalf("SetStatus over a role store that cannot stage the guard: err = %v, want %v", err, boom)
	}
	got, err := w.f.st.GetUser(ctx, jason.ID)
	if err != nil || got.Version != jason.Version || got.Status != auth.StatusActive {
		t.Fatalf("a refused write changed the user: %+v, %v", got, err)
	}
}
