// file: core/auth/lastadmin_roleguard_test.go
// version: 1.0.1
// guid: 5b8e2f61-7a3d-4c90-b1e4-d6a92c0f3e75
// last-edited: 2026-11-19

package auth_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// roleWayne is a custom role that grants core:admin, held by one of the two
// admins of every scenario below; the other holds the built-in admin role.
const roleWayne rbac.RoleName = "custom.wayne-enterprises"

// adminRace is two active core:admin holders: bruce through roleWayne (a
// role definition rbac can edit) and dick through the locked admin role.
// An rbac role edit that strips core:admin from roleWayne and an auth user
// write that takes core:admin from dick would, committed one after the
// other with each deciding before the other landed, leave no active admin.
type adminRace struct {
	f         *fx
	bruce     *auth.User
	dick      *auth.User
	wayne     *rbac.Role
	actor     *auth.Principal // bruce's session: users.manage, dominates dick
	rbacActor rbac.Checker    // a signed-in admin, as the HTTP layer hands rbac
}

func newAdminRace(t testing.TB) *adminRace {
	t.Helper()
	f := newFx(t)
	r := &adminRace{f: f}
	r.rbacActor = f.rb.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{rbac.RoleAdmin}, Interactive: true})
	var err error
	r.wayne, err = f.rb.CreateRole(context.Background(), r.rbacActor, rbac.RoleInput{
		Name: roleWayne, Title: "Wayne Enterprises board", Permissions: []rbac.Permission{"core:admin"},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	r.bruce = f.user("bruce-wayne", roleWayne)
	r.dick = f.user("dick-grayson", rbac.RoleAdmin)
	r.actor = f.session(r.bruce)
	return r
}

// stripAdmin is the rbac role edit: roleWayne stops granting core:admin.
func (r *adminRace) stripAdmin() error {
	_, err := r.f.rb.UpdateRole(context.Background(), r.rbacActor, roleWayne,
		rbac.RoleInput{Title: "Wayne Enterprises board", Permissions: []rbac.Permission{"core:self"}}, r.wayne.Version)
	return err
}

// userWrites are the auth writes that take core:admin from dick.
func (r *adminRace) userWrites() map[string]func() error {
	ctx := context.Background()
	return map[string]func() error{
		"demote": func() error {
			return r.f.svc.SetRoles(ctx, r.actor, r.dick.ID, []rbac.RoleName{rbac.RoleViewer})
		},
		"deactivate": func() error {
			return r.f.svc.SetStatus(ctx, r.actor, r.dick.ID, auth.StatusDisabled)
		},
		"delete": func() error {
			return r.f.svc.DeleteUser(ctx, r.actor, r.dick.ID)
		},
	}
}

// activeAdmins counts the active users, as stored now, holding a role whose
// stored definition grants core:admin.
func (r *adminRace) activeAdmins(t testing.TB) int {
	t.Helper()
	ctx := context.Background()
	n := 0
	for u, err := range r.f.st.ScanUsers(ctx) {
		if err != nil {
			t.Fatalf("ScanUsers: %v", err)
		}
		if u.Status != auth.StatusActive {
			continue
		}
		for _, name := range u.Roles {
			role, err := r.f.rb.Role(ctx, name)
			if err != nil {
				continue
			}
			if rbac.GrantsAdmin(role.Permissions()) {
				n++
				break
			}
		}
	}
	return n
}

// TestLastAdmin_RoleEditAgainstUserWrite (F4-02-ROLE-GUARD): the role
// definition is part of the last-admin decision, so an rbac role edit and an
// auth user write cannot both land when together they leave no active admin.
// Each subtest lines the two up deterministically, one way each, with the
// batch of the first writer staged and its decision made before the second
// writer commits. At least one active admin must remain, and the writer that
// committed second is the one refused with ErrLastAdmin.
func TestLastAdmin_RoleEditAgainstUserWrite(t *testing.T) {
	for _, name := range []string{"demote", "deactivate", "delete"} {
		t.Run("role edit commits inside the user write "+name, func(t *testing.T) {
			r := newAdminRace(t)
			write := r.userWrites()[name]
			// The user write has read bruce's role, guarded bruce's record
			// and not yet committed; the role edit lands now. The user
			// write's own retry passes straight through.
			var editErr error
			var fired atomic.Bool
			r.f.audit.hold = func(audit.Event) {
				if fired.CompareAndSwap(false, true) {
					editErr = r.stripAdmin()
				}
			}
			userErr := write()
			if editErr != nil {
				t.Fatalf("the role edit, with dick still an active admin: %v", editErr)
			}
			if !errors.Is(userErr, auth.ErrLastAdmin) {
				t.Fatalf("user write %s after the role edit took bruce's admin: err = %v, want ErrLastAdmin", name, userErr)
			}
			if n := r.activeAdmins(t); n != 1 {
				t.Fatalf("%d active admins, want 1", n)
			}
			if got := r.f.reload(r.dick.ID); got.Version != r.dick.Version || got.Status != auth.StatusActive {
				t.Fatalf("a refused user write changed dick: %+v", got)
			}
		})

		t.Run("user write "+name+" commits inside the role edit", func(t *testing.T) {
			r := newAdminRace(t)
			write := r.userWrites()[name]
			// The role edit has read the holders before and after the edit
			// (dick among them: bruce's role is the one it strips), guarded
			// every user it read, and not yet committed; the user write
			// lands now, in the window between its last holder read (the
			// second call) and its commit. Its batch's Commit must fail on
			// the guarded user, and the retry, which reads dick gone, must
			// refuse: a needed second attempt is what shows the guard held.
			var userErr error
			r.f.holders.setAfterRead(func(call int) {
				if call == 2 {
					userErr = write()
				}
			})
			editErr := r.stripAdmin()
			if userErr != nil {
				t.Fatalf("user write %s, with bruce still an active admin: %v", name, userErr)
			}
			if !errors.Is(editErr, rbac.ErrLastAdmin) {
				t.Fatalf("role edit after the user write took dick's admin: err = %v, want ErrLastAdmin", editErr)
			}
			if n := r.f.holders.holderReads(); n < 4 {
				t.Fatalf("HoldersOf ran %d times, want 4 or more: the first attempt must have failed at its commit and run again", n)
			}
			if n := r.activeAdmins(t); n != 1 {
				t.Fatalf("%d active admins, want 1", n)
			}
			got, err := r.f.rb.Role(context.Background(), roleWayne)
			if err != nil || got.Version != r.wayne.Version {
				t.Fatalf("a refused role edit changed the role: %+v, %v", got, err)
			}
		})
	}
}

// TestLastAdmin_RoleEditAgainstUserWrite_Free runs the same two writers with
// no hook, released together, many times over (it is the -race run's
// target). Whatever the interleaving, an active admin remains and a writer
// that did not land was refused for the right reason.
func TestLastAdmin_RoleEditAgainstUserWrite_Free(t *testing.T) {
	const rounds = 12
	for _, name := range []string{"demote", "deactivate", "delete"} {
		t.Run(name, func(t *testing.T) {
			var landedBoth int
			for i := range rounds {
				r := newAdminRace(t)
				write := r.userWrites()[name]
				var start sync.WaitGroup
				start.Add(1)
				var userErr, editErr error
				var wg sync.WaitGroup
				wg.Go(func() { start.Wait(); userErr = write() })
				wg.Go(func() { start.Wait(); editErr = r.stripAdmin() })
				start.Done()
				wg.Wait()
				for who, err := range map[string]error{"user write": userErr, "role edit": editErr} {
					if err != nil && !errors.Is(err, auth.ErrLastAdmin) && !errors.Is(err, rbac.ErrLastAdmin) && !errors.Is(err, rbac.ErrVersionMismatch) {
						t.Fatalf("round %d: %s: unexpected error %v", i, who, err)
					}
				}
				if n := r.activeAdmins(t); n < 1 {
					t.Fatalf("round %d: no active admin left (user write: %v, role edit: %v)", i, userErr, editErr)
				}
				if userErr == nil && editErr == nil {
					landedBoth++
				}
			}
			if landedBoth != 0 {
				t.Fatalf("both writers landed in %d of %d rounds", landedBoth, rounds)
			}
		})
	}
}

// TestLastAdmin_RoleGuardOtherRoleStaysHeld: a role edit that leaves the
// other holder an admin, or touches a role no admin holds, is not blocked by
// the guard (a guard that refused every concurrent role edit would be safe
// and useless).
func TestLastAdmin_RoleGuardOtherRoleStaysHeld(t *testing.T) {
	r := newAdminRace(t)
	ctx := context.Background()
	// An unrelated role edit between the user write's read and its commit
	// does not refuse the user write.
	if _, err := r.f.rb.CreateRole(ctx, r.rbacActor, rbac.RoleInput{
		Name: "custom.gordon", Title: "Commissioner", Permissions: []rbac.Permission{"core:self"},
	}); err != nil {
		t.Fatal(err)
	}
	gordon := slices.Clone([]rbac.Permission{"core:self", "core:users.view"})
	var fired atomic.Bool
	r.f.audit.hold = func(audit.Event) {
		if fired.CompareAndSwap(false, true) {
			cur, err := r.f.rb.Role(ctx, "custom.gordon")
			if err != nil {
				t.Errorf("Role: %v", err)
				return
			}
			if _, err := r.f.rb.UpdateRole(ctx, r.rbacActor, "custom.gordon",
				rbac.RoleInput{Title: "Commissioner", Permissions: gordon}, cur.Version); err != nil {
				t.Errorf("unrelated role edit: %v", err)
			}
		}
	}
	if err := r.userWrites()["demote"](); err != nil {
		t.Fatalf("demoting dick while bruce keeps core:admin, with an unrelated role edited meanwhile: %v", err)
	}
	if n := r.activeAdmins(t); n != 1 {
		t.Fatalf("%d active admins, want 1", n)
	}
}

// bareRoleStore is the native role store seen only as rbac.Store, with no
// RoleGuarder.
type bareRoleStore struct{ rbac.Store }

// TestLastAdmin_RoleStoreWithoutGuardRefuses: when rbac's role store cannot
// guard a role, a write that needs the guard is refused (rbac.ErrNoRoleGuard)
// and writes nothing, rather than deciding on a definition a role edit may
// change before the commit. A write that needs no guard is unaffected.
func TestLastAdmin_RoleStoreWithoutGuardRefuses(t *testing.T) {
	f := newFxRoleStore(t, func(s rbac.Store) rbac.Store { return bareRoleStore{s} })
	ctx := context.Background()
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	dick := f.user("dick-grayson", rbac.RoleAdmin)
	me := f.session(bruce)

	err := f.svc.SetRoles(ctx, me, dick.ID, []rbac.RoleName{rbac.RoleViewer})
	if !errors.Is(err, rbac.ErrNoRoleGuard) {
		t.Fatalf("demoting an admin with another admin active, over a store with no guard: err = %v, want rbac.ErrNoRoleGuard", err)
	}
	if got := f.reload(dick.ID); got.Version != dick.Version {
		t.Fatalf("a refused demotion wrote version %d", got.Version)
	}
	// Taking nothing of core:admin needs no other holder, so no guard.
	jason := f.user("jason-todd", "gotham.detective")
	if err := f.svc.SetRoles(ctx, me, jason.ID, []rbac.RoleName{rbac.RoleViewer}); err != nil {
		t.Fatalf("a role change that takes no admin: %v", err)
	}
}
