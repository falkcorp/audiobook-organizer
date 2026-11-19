// file: core/auth/lastadmin_roleguard_test.go
// version: 1.1.0
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

// wayneFx is a fixture with the custom role roleWayne (core:admin) created
// and the rbac actor that edits it; each scenario adds its own users.
type wayneFx struct {
	f         *fx
	wayne     *rbac.Role
	rbacActor rbac.Checker // a signed-in admin, as the HTTP layer hands rbac
}

// newWayneFx is wayneFx over the role store wrap makes of the native one
// (nil: as is).
func newWayneFx(t testing.TB, wrap func(rbac.Store) rbac.Store) *wayneFx {
	t.Helper()
	f := newFxRoleStore(t, wrap)
	w := &wayneFx{f: f}
	w.rbacActor = f.rb.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{rbac.RoleAdmin}, Interactive: true})
	var err error
	w.wayne, err = f.rb.CreateRole(context.Background(), w.rbacActor, rbac.RoleInput{
		Name: roleWayne, Title: "Wayne Enterprises board", Permissions: []rbac.Permission{"core:admin"},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	return w
}

// stripAdmin is the rbac role edit: roleWayne stops granting core:admin.
func (w *wayneFx) stripAdmin() error {
	_, err := w.f.rb.UpdateRole(context.Background(), w.rbacActor, roleWayne,
		rbac.RoleInput{Title: "Wayne Enterprises board", Permissions: []rbac.Permission{"core:self"}}, w.wayne.Version)
	return err
}

// activeAdmins counts the active users, as stored now, holding a role whose
// stored definition grants core:admin.
func (w *wayneFx) activeAdmins(t testing.TB) int {
	t.Helper()
	ctx := context.Background()
	n := 0
	for u, err := range w.f.st.ScanUsers(ctx) {
		if err != nil {
			t.Fatalf("ScanUsers: %v", err)
		}
		if u.Status != auth.StatusActive {
			continue
		}
		for _, name := range u.Roles {
			role, err := w.f.rb.Role(ctx, name)
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

// holdOnce installs the audit hook that runs fn once, at the first audit
// record of a user write: inside the window between the write's decision
// and its commit.
func (w *wayneFx) holdOnce(fn func()) *atomic.Bool {
	var fired atomic.Bool
	w.f.audit.hold = func(audit.Event) {
		if fired.CompareAndSwap(false, true) {
			fn()
		}
	}
	return &fired
}

// adminRace is two active core:admin holders: bruce through roleWayne (a
// role definition rbac can edit) and dick through the locked admin role.
// An rbac role edit that strips core:admin from roleWayne and an auth user
// write that takes core:admin from dick would, committed one after the
// other with each deciding before the other landed, leave no active admin.
type adminRace struct {
	*wayneFx
	bruce *auth.User
	dick  *auth.User
	actor *auth.Principal // bruce's session: users.manage, dominates dick
}

func newAdminRace(t testing.TB) *adminRace {
	t.Helper()
	r := &adminRace{wayneFx: newWayneFx(t, nil)}
	r.bruce = r.f.user("bruce-wayne", roleWayne)
	r.dick = r.f.user("dick-grayson", rbac.RoleAdmin)
	r.actor = r.f.session(r.bruce)
	return r
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
// guard the role set, every user write that runs the last-admin rule is
// refused (rbac.ErrNoRoleGuard) and writes nothing, rather than deciding on
// role definitions a role edit may change before the commit. The refusal does
// not depend on the user holding core:admin: the guard is staged before the
// first role read, so there is no path that reads a role unguarded.
func TestLastAdmin_RoleStoreWithoutGuardRefuses(t *testing.T) {
	f := newFxRoleStore(t, func(s rbac.Store) rbac.Store { return bareRoleStore{s} })
	ctx := context.Background()
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	dick := f.user("dick-grayson", rbac.RoleAdmin)
	jason := f.user("jason-todd", "gotham.detective")
	me := f.session(bruce)

	writes := map[string]struct {
		u  *auth.User
		fn func() error
	}{
		"demote an admin": {dick, func() error { return f.svc.SetRoles(ctx, me, dick.ID, []rbac.RoleName{rbac.RoleViewer}) }},
		"change a role that takes no admin": {jason, func() error {
			return f.svc.SetRoles(ctx, me, jason.ID, []rbac.RoleName{rbac.RoleViewer})
		}},
		"disable": {jason, func() error { return f.svc.SetStatus(ctx, me, jason.ID, auth.StatusDisabled) }},
		"delete":  {jason, func() error { return f.svc.DeleteUser(ctx, me, jason.ID) }},
	}
	for name, w := range writes {
		if err := w.fn(); !errors.Is(err, rbac.ErrNoRoleGuard) {
			t.Errorf("%s over a store with no guard: err = %v, want rbac.ErrNoRoleGuard", name, err)
		}
		got, err := f.st.GetUser(ctx, w.u.ID)
		if err != nil || got.Version != w.u.Version || got.Status != auth.StatusActive {
			t.Errorf("%s: a refused write changed the user: %+v, %v", name, got, err)
		}
	}
}

// TestLastAdmin_StillHoldsExit (F4-02-ROLE-GUARD round 2, B1): a user write
// that returns at "the user still holds core:admin" decided on a role
// definition, so a role edit committed in its window must fail its commit.
// bruce holds the built-in admin role and roleWayne and drops admin, keeping
// roleWayne; the edit that strips core:admin from roleWayne lands inside the
// write. Each shape ends with the write refused and one admin left.
func TestLastAdmin_StillHoldsExit(t *testing.T) {
	for _, shape := range []struct {
		name  string
		other bool // dick, a second user, holds roleWayne too
	}{
		{"sole admin racing himself", false},
		{"two actors", true},
	} {
		t.Run(shape.name, func(t *testing.T) {
			w := newWayneFx(t, nil)
			bruce := w.f.user("bruce-wayne", rbac.RoleAdmin, roleWayne)
			if shape.other {
				w.f.user("dick-grayson", roleWayne)
			}
			me := w.f.session(bruce)
			var editErr error
			fired := w.holdOnce(func() { editErr = w.stripAdmin() })

			err := w.f.svc.SetRoles(context.Background(), me, bruce.ID, []rbac.RoleName{roleWayne})
			if !fired.Load() {
				t.Fatal("the role edit never ran inside the user write")
			}
			if editErr != nil {
				t.Fatalf("the role edit, with bruce still holding the admin role: %v", editErr)
			}
			if !errors.Is(err, auth.ErrLastAdmin) {
				t.Fatalf("SetRoles dropping admin while roleWayne lost core:admin meanwhile: err = %v, want ErrLastAdmin", err)
			}
			if n := w.activeAdmins(t); n != 1 {
				t.Fatalf("%d active admins, want 1", n)
			}
			if got := w.f.reload(bruce.ID); got.Version != bruce.Version {
				t.Fatalf("a refused SetRoles changed bruce: %+v", got)
			}
		})
	}
}

// TestLastAdmin_NobodyHeldAdminExit (round 2, B1's was == nil variant): a
// write that takes nothing of core:admin from its user read that user's roles
// as granting nothing, so a role edit that makes one of them grant it, and a
// second edit that takes core:admin from the one admin role left (which
// counts the user as a holder, whose unchanged record it guards) may not both
// land inside it. dick holds custom.gordon; bruce is the only admin, through
// roleWayne.
func TestLastAdmin_NobodyHeldAdminExit(t *testing.T) {
	const roleGordon rbac.RoleName = "custom.gordon"
	writes := map[string]func(w *wayneFx, me *auth.Principal, dick *auth.User) error{
		"disable": func(w *wayneFx, me *auth.Principal, dick *auth.User) error {
			return w.f.svc.SetStatus(context.Background(), me, dick.ID, auth.StatusDisabled)
		},
		"delete": func(w *wayneFx, me *auth.Principal, dick *auth.User) error {
			return w.f.svc.DeleteUser(context.Background(), me, dick.ID)
		},
		"remove the role": func(w *wayneFx, me *auth.Principal, dick *auth.User) error {
			return w.f.svc.SetRoles(context.Background(), me, dick.ID, []rbac.RoleName{rbac.RoleViewer})
		},
	}
	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			w := newWayneFx(t, nil)
			ctx := context.Background()
			gordon, err := w.f.rb.CreateRole(ctx, w.rbacActor, rbac.RoleInput{
				Name: roleGordon, Title: "Commissioner", Permissions: []rbac.Permission{"core:self"},
			})
			if err != nil {
				t.Fatal(err)
			}
			bruce := w.f.user("bruce-wayne", roleWayne)
			dick := w.f.user("dick-grayson", roleGordon)
			me := w.f.session(bruce)
			var errs [2]error
			fired := w.holdOnce(func() {
				_, errs[0] = w.f.rb.UpdateRole(ctx, w.rbacActor, roleGordon,
					rbac.RoleInput{Title: "Commissioner", Permissions: []rbac.Permission{"core:admin"}}, gordon.Version)
				errs[1] = w.stripAdmin()
			})

			err = write(w, me, dick)
			if !fired.Load() {
				t.Fatal("the role edits never ran inside the user write")
			}
			if errs[0] != nil || errs[1] != nil {
				t.Fatalf("the role edits: grant %v, strip %v", errs[0], errs[1])
			}
			if !errors.Is(err, auth.ErrLastAdmin) {
				t.Fatalf("%s of dick, who holds the only admin role left: err = %v, want ErrLastAdmin", name, err)
			}
			if n := w.activeAdmins(t); n != 1 {
				t.Fatalf("%d active admins, want 1", n)
			}
			if got := w.f.reload(dick.ID); got.Version != dick.Version || got.Status != auth.StatusActive {
				t.Fatalf("a refused write changed dick: %+v", got)
			}
		})
	}
}

// TestLastAdmin_RoleDeletedOrRecreatedInTheWindow (round 2, B1's ABA): bruce,
// the only admin, takes roleWayne, which nobody holds. A role edit cannot be
// told apart from its absence by a version check: roleWayne is deleted (no
// holder, so rbac allows it) and, in the second shape, created again at
// version 1 without core:admin. Both shapes must fail the user write's
// commit, and its retry refuses.
func TestLastAdmin_RoleDeletedOrRecreatedInTheWindow(t *testing.T) {
	for _, recreate := range []bool{false, true} {
		name := "deleted"
		if recreate {
			name = "deleted and created again at version 1"
		}
		t.Run(name, func(t *testing.T) {
			w := newWayneFx(t, nil)
			ctx := context.Background()
			bruce := w.f.user("bruce-wayne", rbac.RoleAdmin)
			me := w.f.session(bruce)
			var errs [2]error
			fired := w.holdOnce(func() {
				errs[0] = w.f.rb.DeleteRole(ctx, w.rbacActor, roleWayne, w.wayne.Version)
				if recreate {
					_, errs[1] = w.f.rb.CreateRole(ctx, w.rbacActor, rbac.RoleInput{
						Name: roleWayne, Title: "Wayne Enterprises board", Permissions: []rbac.Permission{"core:self"},
					})
				}
			})

			err := w.f.svc.SetRoles(ctx, me, bruce.ID, []rbac.RoleName{roleWayne})
			if !fired.Load() {
				t.Fatal("the role writes never ran inside the user write")
			}
			if errs[0] != nil || errs[1] != nil {
				t.Fatalf("the role writes: delete %v, create %v", errs[0], errs[1])
			}
			if !errors.Is(err, auth.ErrLastAdmin) {
				t.Fatalf("SetRoles to a role that was deleted meanwhile: err = %v, want ErrLastAdmin", err)
			}
			if n := w.activeAdmins(t); n != 1 {
				t.Fatalf("%d active admins, want 1", n)
			}
		})
	}
}

// TestRoleDelete_UserWriteCommitsInsideTheDelete (round 2, M1): DeleteRole's
// role-in-use check reads who holds the role, which a user write that adds it
// afterwards is invisible to. bruce, the only admin, takes roleWayne (core:admin,
// held by nobody) in the window between DeleteRole's last holder read and its
// commit. The delete must not land: its commit fails on bruce's record, which
// the last-admin rule guarded, and the retry finds roleWayne held and answers
// ErrRoleInUse.
func TestRoleDelete_UserWriteCommitsInsideTheDelete(t *testing.T) {
	w := newWayneFx(t, nil)
	ctx := context.Background()
	bruce := w.f.user("bruce-wayne", rbac.RoleAdmin)
	me := w.f.session(bruce)
	// A delete reads the holders of the role (role-in-use), then of every role
	// that grants core:admin before the delete, then of those after it: the
	// third call is its last read of an attempt.
	const lastRead = 3
	var userErr error
	var fired bool
	w.f.holders.setAfterRead(func(call int) {
		if call == lastRead {
			fired = true
			userErr = w.f.svc.SetRoles(ctx, me, bruce.ID, []rbac.RoleName{roleWayne})
		}
	})
	err := w.f.rb.DeleteRole(ctx, w.rbacActor, roleWayne, w.wayne.Version)
	if !fired {
		t.Fatalf("the user write never ran: DeleteRole made %d holder reads, the hook is on the %dth", w.f.holders.holderReads(), lastRead)
	}
	if userErr != nil {
		t.Fatalf("the user write inside the delete: %v", userErr)
	}
	if !errors.Is(err, rbac.ErrRoleInUse) {
		t.Fatalf("DeleteRole of a role a user took meanwhile: err = %v, want ErrRoleInUse", err)
	}
	if n := w.activeAdmins(t); n != 1 {
		t.Fatalf("%d active admins, want 1", n)
	}
	if _, err := w.f.rb.Role(ctx, roleWayne); err != nil {
		t.Fatalf("a refused delete removed roleWayne: %v", err)
	}
	if n := w.f.holders.holderReads(); n < lastRead+1 {
		t.Fatalf("HoldersOf ran %d times, want %d or more: the first attempt must have failed at its commit and run again", n, lastRead+1)
	}
}
