// file: core/auth/roleclaim_test.go
// version: 1.2.0
// guid: 0c5d7e21-93b4-4a68-8f1e-2d6a4b9c7e53
// last-edited: 2026-11-19

package auth_test

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

// roleRobin is a custom role no one holds yet, the role every scenario below
// assigns while an rbac write deletes (or deletes and recreates) it.
const roleRobin rbac.RoleName = "custom.robin"

// claimFx is a fixture with roleRobin created, a non-admin manager (gordon,
// users.manage plus the gotham scopes, so CanAssign covers roleRobin when it
// grants only gotham:patrol) and an admin rbac actor that edits roles.
type claimFx struct {
	f         *fx
	rbacActor rbac.Checker
	robin     *rbac.Role
	gordon    *auth.User
	manager   *auth.Principal
	jason     *auth.User // a detective the SetRoles scenario hands roleRobin to
}

func newClaimFx(t testing.TB) *claimFx {
	t.Helper()
	c := &claimFx{f: newFxRoleStore(t, nil)}
	c.rbacActor = c.f.rb.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{rbac.RoleAdmin}, Interactive: true})
	c.robin = c.createRobin([]rbac.Permission{"gotham:patrol"})
	c.gordon = c.f.user("jim-gordon", roleCommissioner)
	c.manager = c.f.session(c.gordon)
	c.jason = c.f.user("jason-todd", "gotham.detective")
	return c
}

func (c *claimFx) createRobin(perms []rbac.Permission) *rbac.Role {
	c.f.t.Helper()
	r, err := c.f.rb.CreateRole(context.Background(), c.rbacActor, rbac.RoleInput{Name: roleRobin, Title: "Robin", Permissions: perms})
	if err != nil {
		c.f.t.Fatalf("CreateRole: %v", err)
	}
	return r
}

func (c *claimFx) deleteRobin() error {
	cur, err := c.f.rb.Role(context.Background(), roleRobin)
	if err != nil {
		return err
	}
	return c.f.rb.DeleteRole(context.Background(), c.rbacActor, roleRobin, cur.Version)
}

// holdersOfRobin is how many stored users hold roleRobin.
func (c *claimFx) holdersOfRobin() int {
	c.f.t.Helper()
	n := 0
	for u, err := range c.f.st.ScanUsers(context.Background()) {
		if err != nil {
			c.f.t.Fatalf("ScanUsers: %v", err)
		}
		if slices.Contains(u.Roles, roleRobin) {
			n++
		}
	}
	return n
}

// assign is the auth write by which a user comes to hold roleRobin, one per
// way a user gets a role: CreateUser, SetRoles and an invite's accept. The
// invite is minted here, before the scenario sets its hook, so the write itself
// is only the accept.
func (c *claimFx) assign(way string) func() error {
	ctx := context.Background()
	switch way {
	case "create user":
		return func() error {
			_, err := c.f.svc.CreateUser(ctx, c.manager, auth.NewUser{Username: "tim-drake", Roles: []rbac.RoleName{roleRobin}})
			return err
		}
	case "set roles":
		return func() error {
			return c.f.svc.SetRoles(ctx, c.manager, c.jason.ID, []rbac.RoleName{"gotham.detective", roleRobin})
		}
	case "accept invite":
		tok, _ := c.f.invite(c.manager, auth.InviteSpec{Username: "stephanie-brown", Role: roleRobin})
		return func() error {
			_, _, err := c.f.accept(tok, "gotham-city-1939")
			return err
		}
	}
	c.f.t.Fatalf("no such way %q", way)
	return nil
}

var claimWays = []string{"create user", "set roles", "accept invite"}

// TestRoleClaim_RoleDeletedInsideTheAssignment (F4-02-ROLE-GUARD r2, n6): a
// user write that gives a role has read the role and not yet committed when
// DeleteRole removes it (it is held by no one the delete can see). The write's
// commit must conflict on the role set, and its retry, which finds the role
// gone, must refuse: no user ends up holding a role that no longer exists,
// for a later CreateRole under the same name to grant.
func TestRoleClaim_RoleDeletedInsideTheAssignment(t *testing.T) {
	for _, way := range claimWays {
		t.Run(way, func(t *testing.T) {
			c := newClaimFx(t)
			write := c.assign(way)
			var delErr error
			var fired bool
			c.f.audit.hold = func(audit.Event) {
				if !fired {
					fired = true
					delErr = c.deleteRobin()
				}
			}
			err := write()
			if !fired {
				t.Fatal("the hook never ran: the write recorded no audit event")
			}
			if delErr != nil {
				t.Fatalf("DeleteRole, with no holder committed: %v", delErr)
			}
			if !errors.Is(err, rbac.ErrUnknownRole) {
				t.Fatalf("%s after the role was deleted: err = %v, want a refusal naming the unknown role", way, err)
			}
			if n := c.holdersOfRobin(); n != 0 {
				t.Fatalf("%d user(s) hold the deleted role", n)
			}
		})
	}
}

// TestRoleClaim_AssignmentInsideRoleDelete is the other commit order: the
// delete has read the role's holders (none) and staged its batch, and the
// assignment commits now. The delete's commit must conflict, and its retry,
// which finds the holder, must refuse with ErrRoleInUse.
func TestRoleClaim_AssignmentInsideRoleDelete(t *testing.T) {
	for _, way := range claimWays {
		t.Run(way, func(t *testing.T) {
			c := newClaimFx(t)
			write := c.assign(way)
			var writeErr error
			var fired bool
			c.f.holders.setAfterRead(func(call int) {
				if call == 1 {
					fired = true
					writeErr = write()
				}
			})
			delErr := c.deleteRobin()
			if !fired {
				t.Fatal("the hook never ran: DeleteRole read no holders")
			}
			if writeErr != nil {
				t.Fatalf("%s, with the role still stored: %v", way, writeErr)
			}
			if !errors.Is(delErr, rbac.ErrRoleInUse) {
				t.Fatalf("DeleteRole after %s gave the role a holder: err = %v, want ErrRoleInUse", way, delErr)
			}
			if n := c.f.holders.holderReads(); n < 2 {
				t.Fatalf("HoldersOf ran %d time(s), want 2 or more: the first attempt must have failed at its commit and run again", n)
			}
			if _, err := c.f.rb.Role(context.Background(), roleRobin); err != nil {
				t.Fatalf("the refused delete removed the role: %v", err)
			}
			if n := c.holdersOfRobin(); n != 1 {
				t.Fatalf("%d user(s) hold the role, want 1", n)
			}
		})
	}
}

// TestRoleClaim_RoleRecreatedInsideTheAssignment: the role is deleted and
// created again under the same name, now granting a scope the manager does
// not hold, between the write's read and its commit. A version check on the
// role cannot tell it from the untouched one; the role-set generation can, and
// the retry must run CanAssign over the new definition and refuse.
func TestRoleClaim_RoleRecreatedInsideTheAssignment(t *testing.T) {
	for _, way := range claimWays {
		t.Run(way, func(t *testing.T) {
			c := newClaimFx(t)
			write := c.assign(way)
			var swapErr error
			var fired bool
			c.f.audit.hold = func(audit.Event) {
				if fired {
					return
				}
				fired = true
				if swapErr = c.deleteRobin(); swapErr != nil {
					return
				}
				_, swapErr = c.f.rb.CreateRole(context.Background(), c.rbacActor, rbac.RoleInput{
					Name: roleRobin, Title: "Robin", Permissions: []rbac.Permission{"gotham:patrol", "gotham:cave.manage"},
				})
			}
			err := write()
			if !fired {
				t.Fatal("the hook never ran: the write recorded no audit event")
			}
			if swapErr != nil {
				t.Fatalf("delete and recreate: %v", swapErr)
			}
			if !errors.Is(err, rbac.ErrEscalation) {
				t.Fatalf("%s over a role recreated with a scope the manager lacks: err = %v, want ErrEscalation", way, err)
			}
			if n := c.holdersOfRobin(); n != 0 {
				t.Fatalf("%d user(s) hold the recreated role", n)
			}
		})
	}
}

// TestRoleClaim_ConcurrentUnrelatedAssignmentsAllSucceed: every claim bumps
// one counter, so claims that touch different users must still not fail one
// another. Five CreateUsers, five SetRoleses on five different users and five
// accepted invites, all for roleRobin and released together, all succeed (the
// -race run's target): a conflict means another claim committed, so a writer
// loses at most once to each of the other fourteen, and its sixteen attempts
// (claimRetries) let all fifteen land.
func TestRoleClaim_ConcurrentUnrelatedAssignmentsAllSucceed(t *testing.T) {
	const n = 5
	c := newClaimFx(t)
	ctx := context.Background()
	var writes []func() error
	for i := range n {
		name := fmt.Sprintf("gotham-recruit-%02d", i)
		writes = append(writes, func() error {
			_, err := c.f.svc.CreateUser(ctx, c.manager, auth.NewUser{Username: name, Roles: []rbac.RoleName{roleRobin}})
			return err
		})
		target := c.f.user(fmt.Sprintf("gotham-patrol-%02d", i), "gotham.detective")
		writes = append(writes, func() error {
			return c.f.svc.SetRoles(ctx, c.manager, target.ID, []rbac.RoleName{"gotham.detective", roleRobin})
		})
		tok, _ := c.f.invite(c.manager, auth.InviteSpec{Username: fmt.Sprintf("gotham-invitee-%02d", i), Role: roleRobin})
		writes = append(writes, func() error {
			_, _, err := c.f.accept(tok, "gotham-city-1939")
			return err
		})
	}
	errs := make([]error, len(writes))
	var start sync.WaitGroup
	start.Add(1)
	var wg sync.WaitGroup
	for i, w := range writes {
		wg.Go(func() { start.Wait(); errs[i] = w() })
	}
	start.Done()
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("assignment %d of %d: %v", i, len(writes), err)
		}
	}
	if got := c.holdersOfRobin(); got != len(writes) {
		t.Fatalf("%d holders of the role, want %d", got, len(writes))
	}
}

// TestRoleClaim_AssignRacesDelete_Free releases an assignment and DeleteRole
// of the role together, many times over. Whatever the interleaving, the end
// state has no dangling role: either the role exists (the delete was refused
// ErrRoleInUse or never ran first) and holds whatever was assigned, or it is
// gone and nobody holds it (the assignment was refused as unknown).
func TestRoleClaim_AssignRacesDelete_Free(t *testing.T) {
	const rounds = 15
	for _, way := range claimWays {
		t.Run(way, func(t *testing.T) {
			var assigned, deleted int
			for i := range rounds {
				c := newClaimFx(t)
				write := c.assign(way)
				var start sync.WaitGroup
				start.Add(1)
				var writeErr, delErr error
				var wg sync.WaitGroup
				wg.Go(func() { start.Wait(); writeErr = write() })
				wg.Go(func() { start.Wait(); delErr = c.deleteRobin() })
				start.Done()
				wg.Wait()
				_, roleErr := c.f.rb.Role(context.Background(), roleRobin)
				held := c.holdersOfRobin()
				switch {
				case roleErr == nil && delErr != nil && !errors.Is(delErr, rbac.ErrRoleInUse) && !errors.Is(delErr, rbac.ErrVersionMismatch):
					t.Fatalf("round %d: DeleteRole: unexpected error %v", i, delErr)
				case roleErr != nil && held != 0:
					t.Fatalf("round %d: %d user(s) hold a role that was deleted (write: %v, delete: %v)", i, held, writeErr, delErr)
				case writeErr == nil && held != 1:
					t.Fatalf("round %d: the assignment succeeded and %d user(s) hold the role", i, held)
				case writeErr != nil && !errors.Is(writeErr, rbac.ErrUnknownRole) && !errors.Is(writeErr, storage.ErrConflict):
					t.Fatalf("round %d: the assignment failed for the wrong reason: %v", i, writeErr)
				}
				if writeErr == nil {
					assigned++
				}
				if roleErr != nil {
					deleted++
				}
			}
			t.Logf("%d rounds: assigned %d, role deleted %d", rounds, assigned, deleted)
		})
	}
}

// TestRoleClaim_LosingEveryTryIsContended: a write that gives a role whose
// Commit conflicts on every try (a new role lands inside each one, moving the
// role-set generation it guards) answers ErrContended, wrapping
// storage.ErrConflict, and writes nothing: authhttp maps it 503 busy with a
// Retry-After, where a bare storage.ErrConflict was a 500.
func TestRoleClaim_LosingEveryTryIsContended(t *testing.T) {
	for _, way := range claimWays {
		t.Run(way, func(t *testing.T) {
			c := newClaimFx(t)
			write := c.assign(way)
			tries := 0
			c.f.audit.hold = func(audit.Event) {
				tries++
				name := rbac.RoleName(fmt.Sprintf("custom.nightwing-%d", tries))
				if _, err := c.f.rb.CreateRole(context.Background(), c.rbacActor, rbac.RoleInput{Name: name, Title: "Nightwing", Permissions: []rbac.Permission{"gotham:patrol"}}); err != nil {
					t.Errorf("CreateRole %s: %v", name, err)
				}
			}
			err := write()
			if !errors.Is(err, auth.ErrContended) || !errors.Is(err, storage.ErrConflict) {
				t.Fatalf("%s that lost every try: err = %v, want ErrContended wrapping storage.ErrConflict", way, err)
			}
			if n := c.holdersOfRobin(); n != 0 {
				t.Fatalf("%d user(s) hold the role after a write that never committed", n)
			}
		})
	}
}
