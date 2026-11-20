// file: core/auth/oidc/roleclaim_test.go
// version: 1.2.0
// guid: 7e4b1a93-c5d2-4f68-9a07-3b8d6e1f4c25
// last-edited: 2026-11-19

package oidc

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// deleteSignal deletes roleSignal as an admin would, at its current version.
func (f *fx) deleteSignal() error {
	ctx := context.Background()
	admin := f.rb.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{rbac.RoleAdmin}, Interactive: true})
	cur, err := f.rb.Role(ctx, roleSignal)
	if err != nil {
		return err
	}
	return f.rb.DeleteRole(ctx, admin, roleSignal, cur.Version)
}

func (f *fx) signalHolders() int {
	f.t.Helper()
	n := 0
	for u, err := range f.st.ScanUsers(context.Background()) {
		if err != nil {
			f.t.Fatalf("ScanUsers: %v", err)
		}
		if slices.Contains(u.Roles, roleSignal) {
			n++
		}
	}
	return n
}

// TestOIDC_CreateRoleDeletedInsideTheCommit (F4-02-ROLE-GUARD r2, n6): the
// first sign-in of a new subject gives the policy's default role, which it has
// read and not yet committed when DeleteRole removes it. The commit must
// conflict on the role set, and the resolver's retry (RetryOnConflict around
// resolveLocked), which finds the role gone, must refuse: no account is created
// holding a role that no longer exists.
func TestOIDC_CreateRoleDeletedInsideTheCommit(t *testing.T) {
	f := newFx(t)
	pol := untrusting()
	pol.DefaultRole = roleSignal
	var delErr error
	var fired bool
	f.audit.hold = func(audit.Event) {
		if !fired {
			fired = true
			delErr = f.deleteSignal()
		}
	}
	_, err := resolve(t, f, pol, claims("sub-robin", "dick@wayne.example.test"))
	if !fired {
		t.Fatal("the hook never ran: the creation recorded no audit event")
	}
	if delErr != nil {
		t.Fatalf("DeleteRole, with no holder committed: %v", delErr)
	}
	if !errors.Is(err, auth.ErrBadInput) {
		t.Fatalf("a first sign-in over a default role deleted before its commit: err = %v, want auth.ErrBadInput (the default role cannot be read)", err)
	}
	if n := f.countUsers(); n != 0 {
		t.Fatalf("%d account(s) created over a deleted role", n)
	}
}

// TestOIDC_CreateInsideRoleDelete is the other order: the delete has read the
// role's holders (none) and the first sign-in commits now. The delete must
// conflict and its retry, which finds the new account, refuse with
// ErrRoleInUse.
func TestOIDC_CreateInsideRoleDelete(t *testing.T) {
	f := newFx(t)
	pol := untrusting()
	pol.DefaultRole = roleSignal
	var created *auth.User
	var createErr error
	var fired bool
	f.holders.setAfterRead(func(call int) {
		if call == 1 {
			fired = true
			created, createErr = resolve(t, f, pol, claims("sub-robin", "dick@wayne.example.test"))
		}
	})
	delErr := f.deleteSignal()
	if !fired {
		t.Fatal("the hook never ran: DeleteRole read no holders")
	}
	if createErr != nil || created == nil {
		t.Fatalf("the first sign-in, with the role still stored: %v", createErr)
	}
	if !errors.Is(delErr, rbac.ErrRoleInUse) {
		t.Fatalf("DeleteRole after the sign-in gave the role a holder: err = %v, want ErrRoleInUse", delErr)
	}
	if _, err := f.rb.Role(context.Background(), roleSignal); err != nil {
		t.Fatalf("the refused delete removed the role: %v", err)
	}
	if n := f.signalHolders(); n != 1 {
		t.Fatalf("%d holder(s) of the role, want 1", n)
	}
}

// TestOIDC_CreateLosingEveryTryIsContended: a first sign-in whose commit
// conflicts on every try (a new role lands inside each one, moving the
// role-set generation it claims at) answers auth.ErrContended, wrapping
// storage.ErrConflict, and creates no account; authhttp's callback does not
// treat it as a failure of the provider.
func TestOIDC_CreateLosingEveryTryIsContended(t *testing.T) {
	f := newFx(t)
	pol := untrusting()
	pol.DefaultRole = roleSignal
	admin := f.rb.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{rbac.RoleAdmin}, Interactive: true})
	tries := 0
	f.audit.hold = func(audit.Event) {
		tries++
		name := rbac.RoleName(fmt.Sprintf("custom.nightwing-%d", tries))
		if _, err := f.rb.CreateRole(context.Background(), admin, rbac.RoleInput{Name: name, Title: "Nightwing", Permissions: []rbac.Permission{"core:users.view"}}); err != nil {
			t.Errorf("CreateRole %s: %v", name, err)
		}
	}
	_, err := resolve(t, f, pol, claims("sub-robin", "dick@wayne.example.test"))
	if !errors.Is(err, auth.ErrContended) || !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("a first sign-in that lost every try: err = %v, want auth.ErrContended wrapping storage.ErrConflict", err)
	}
	if n := f.countUsers(); n != 0 {
		t.Fatalf("%d account(s) created by a sign-in that never committed", n)
	}
}

// afterRoleRead is a RoleReader that runs fn once, right after the at-th read
// of role name returns: a role write landing after the resolver read the role
// its decision rests on, and before anything else of the decision is left.
type afterRoleRead struct {
	RoleReader
	name  rbac.RoleName
	at    int
	mu    sync.Mutex
	calls int
	fn    func()
}

func (r *afterRoleRead) Role(ctx context.Context, name rbac.RoleName) (*rbac.Role, error) {
	role, err := r.RoleReader.Role(ctx, name)
	r.mu.Lock()
	var fn func()
	if name == r.name {
		r.calls++
		if r.calls == r.at {
			fn, r.fn = r.fn, nil
		}
	}
	r.mu.Unlock()
	if fn != nil {
		fn()
	}
	return role, err
}

// TestOIDC_CreateReadsTheGenerationBeforeAnyRole (F4-02-ROLE-GUARD r3, n7):
// the first sign-in reads the role-set generation before it reads the default
// role, so a role write that lands right after its last read of the role, and
// before the commit, is seen at the commit. A generation read after the role
// reads would take the generation the write produced and let the stale decision
// stand: the account would be created holding a deleted role. The reader here
// deletes the role after the create's own read of it (the second: holdsAdmin
// reads it first), which is the last role read of the decision.
func TestOIDC_CreateReadsTheGenerationBeforeAnyRole(t *testing.T) {
	f := newFx(t)
	pol := untrusting()
	pol.DefaultRole = roleSignal
	var delErr error
	var fired bool
	roles := &afterRoleRead{RoleReader: f.rb, name: roleSignal, at: 2, fn: func() {
		fired = true
		delErr = f.deleteSignal()
	}}
	_, err := f.resolverOver(roles, nil).Resolve(context.Background(), pol, claims("sub-robin", "dick@wayne.example.test"))
	if !fired {
		t.Fatal("the role never changed: the resolver read the default role fewer than twice")
	}
	if delErr != nil {
		t.Fatalf("DeleteRole, with no holder committed: %v", delErr)
	}
	if !errors.Is(err, auth.ErrBadInput) {
		t.Fatalf("a first sign-in over a default role deleted right after its last read: err = %v, want auth.ErrBadInput (the retry cannot read the role)", err)
	}
	if n := f.countUsers(); n != 0 {
		t.Fatalf("%d account(s) created over a deleted role", n)
	}
	if n := f.signalHolders(); n != 0 {
		t.Fatalf("%d holder(s) of the deleted role", n)
	}
}
