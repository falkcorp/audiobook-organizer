// file: core/rbac/roleguard_test.go
// version: 1.1.0
// guid: c3a71d5e-2b84-4f09-9e6a-81d4b0f7a352
// last-edited: 2026-11-19

package rbac_test

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// TestGuardRoleSet_FailsTheGuardingBatchWhenAnyRoleIsWritten
// (F4-02-ROLE-GUARD round 2): the guard is an Expect on the role-set
// generation read before the caller's role reads, in the caller's batch. A
// role create, edit or delete that commits before the caller's Commit fails
// that Commit with storage.ErrConflict, a role the caller never read included;
// with nothing in between it commits and writes nothing.
func TestGuardRoleSet_FailsTheGuardingBatchWhenAnyRoleIsWritten(t *testing.T) {
	ctx := context.Background()
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
	wayne := f.mustCreate("custom.wayne", "core:admin")

	guard := func(t *testing.T) *storage.Batch {
		t.Helper()
		g, err := f.svc.RoleSetGeneration(ctx)
		if err != nil {
			t.Fatalf("RoleSetGeneration: %v", err)
		}
		b := f.store.NewBatch()
		if err := f.svc.GuardRoleSet(b, g); err != nil {
			t.Fatalf("GuardRoleSet(%d): %v", g, err)
		}
		return b
	}

	b := guard(t)
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatalf("Commit of an untouched guard: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	f.unchanged("custom.wayne", wayne.Version)

	for name, write := range map[string]func(t *testing.T){
		"an edit": func(t *testing.T) {
			cur, _ := f.svc.Role(ctx, "custom.wayne")
			if _, err := f.svc.UpdateRole(ctx, f.admin(), "custom.wayne", rbac.RoleInput{Permissions: perms("core:self")}, cur.Version); err != nil {
				t.Fatal(err)
			}
		},
		"a create of a role the caller never read": func(t *testing.T) { f.mustCreate("custom.gordon", "core:self") },
		"a delete": func(t *testing.T) {
			cur, _ := f.svc.Role(ctx, "custom.gordon")
			if err := f.svc.DeleteRole(ctx, f.admin(), "custom.gordon", cur.Version); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name+" commits first", func(t *testing.T) {
			b := guard(t)
			defer func() { _ = b.Close() }()
			write(t)
			if err := b.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
				t.Fatalf("Commit after %s: %v, want storage.ErrConflict", name, err)
			}
		})
	}

	t.Run("a generation read before the writes is stale", func(t *testing.T) {
		g, err := f.svc.RoleSetGeneration(ctx)
		if err != nil {
			t.Fatal(err)
		}
		f.mustCreate("custom.alfred", "core:self")
		b := f.store.NewBatch()
		defer func() { _ = b.Close() }()
		if err := f.svc.GuardRoleSet(b, g); err != nil {
			t.Fatalf("GuardRoleSet stages the Expect, and the commit reports the conflict: %v", err)
		}
		if err := b.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
			t.Fatalf("Commit of a guard over a stale generation: %v, want storage.ErrConflict", err)
		}
	})
}

// unguardable is a role store that is only rbac.Store: no RoleGuarder.
type unguardable struct{ rbac.Store }

// TestGuardRoleSet_StoreWithoutGuardFailsClosed: a Deps.Store that cannot guard a
// role answers ErrNoRoleGuard, so auth refuses a last-admin decision it cannot
// make atomic instead of making it without the guard.
func TestGuardRoleSet_StoreWithoutGuardFailsClosed(t *testing.T) {
	f := build(t, newEnv(t), setup{})
	svc, err := rbac.New(context.Background(), rbac.Options{Source: "gotham-test"}, rbac.Deps{
		Store: unguardable{f.store}, Compile: f.comp.compile, Audit: f.audit,
		Revocations: f.journal, NewBatch: f.store.NewBatch,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RoleSetGeneration(context.Background()); !errors.Is(err, rbac.ErrNoRoleGuard) {
		t.Fatalf("RoleSetGeneration over a store with no guard: %v, want ErrNoRoleGuard", err)
	}
	b := f.store.NewBatch()
	defer func() { _ = b.Close() }()
	if err := svc.GuardRoleSet(b, 0); !errors.Is(err, rbac.ErrNoRoleGuard) {
		t.Fatalf("GuardRoleSet over a store with no guard: %v, want ErrNoRoleGuard", err)
	}
}
