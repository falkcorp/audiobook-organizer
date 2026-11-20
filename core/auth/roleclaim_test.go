// file: core/auth/roleclaim_test.go
// version: 1.0.0
// guid: 0c5d7e21-93b4-4a68-8f1e-2d6a4b9c7e53
// last-edited: 2026-11-19

package auth_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
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
