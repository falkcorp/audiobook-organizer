// file: core/rbac/roleguard_test.go
// version: 1.0.0
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

// TestGuardRole_FailsTheGuardingBatchWhenTheRoleChanges (F4-02-ROLE-GUARD):
// the guard is an Expect on the role as read, in the caller's batch. A role
// edit or delete that commits before the caller's Commit fails that Commit
// with storage.ErrConflict; with nothing in between it commits and writes
// nothing.
func TestGuardRole_FailsTheGuardingBatchWhenTheRoleChanges(t *testing.T) {
	ctx := context.Background()
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
	wayne := f.mustCreate("custom.wayne", "core:admin")

	read, err := f.svc.Role(ctx, "custom.wayne")
	if err != nil {
		t.Fatal(err)
	}
	b := f.store.NewBatch()
	if err := f.svc.GuardRole(b, read); err != nil {
		t.Fatalf("GuardRole of the stored role: %v", err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatalf("Commit of an untouched guard: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	f.unchanged("custom.wayne", wayne.Version)

	t.Run("an edit commits first", func(t *testing.T) {
		b := f.store.NewBatch()
		defer func() { _ = b.Close() }()
		if err := f.svc.GuardRole(b, read); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.UpdateRole(ctx, f.admin(), "custom.wayne", rbac.RoleInput{Permissions: perms("core:self")}, read.Version); err != nil {
			t.Fatal(err)
		}
		if err := b.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
			t.Fatalf("Commit after the guarded role was edited: %v, want storage.ErrConflict", err)
		}
	})

	t.Run("a role read before the edit is stale", func(t *testing.T) {
		b := f.store.NewBatch()
		defer func() { _ = b.Close() }()
		if err := f.svc.GuardRole(b, read); !errors.Is(err, storage.ErrConflict) {
			t.Fatalf("GuardRole of a role read before an edit: %v, want storage.ErrConflict", err)
		}
	})

	t.Run("a delete commits first", func(t *testing.T) {
		cur, err := f.svc.Role(ctx, "custom.wayne")
		if err != nil {
			t.Fatal(err)
		}
		b := f.store.NewBatch()
		defer func() { _ = b.Close() }()
		if err := f.svc.GuardRole(b, cur); err != nil {
			t.Fatal(err)
		}
		if err := f.svc.DeleteRole(ctx, f.admin(), "custom.wayne", cur.Version); err != nil {
			t.Fatal(err)
		}
		if err := b.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
			t.Fatalf("Commit after the guarded role was deleted: %v, want storage.ErrConflict", err)
		}
		if err := f.svc.GuardRole(f.store.NewBatch(), cur); !errors.Is(err, storage.ErrConflict) {
			t.Fatalf("GuardRole of a deleted role: %v, want storage.ErrConflict", err)
		}
	})
}

// unguardable is a role store that is only rbac.Store: no RoleGuarder.
type unguardable struct{ rbac.Store }

// TestGuardRole_StoreWithoutGuardFailsClosed: a Deps.Store that cannot guard a
// role answers ErrNoRoleGuard, so auth refuses a last-admin decision it cannot
// make atomic instead of making it without the guard.
func TestGuardRole_StoreWithoutGuardFailsClosed(t *testing.T) {
	f := build(t, newEnv(t), setup{})
	svc, err := rbac.New(context.Background(), rbac.Options{Source: "gotham-test"}, rbac.Deps{
		Store: unguardable{f.store}, Compile: f.comp.compile, Audit: f.audit,
		Revocations: f.journal, NewBatch: f.store.NewBatch,
	})
	if err != nil {
		t.Fatal(err)
	}
	b := f.store.NewBatch()
	defer func() { _ = b.Close() }()
	if err := svc.GuardRole(b, &rbac.Role{Name: "custom.wayne", Version: 1}); !errors.Is(err, rbac.ErrNoRoleGuard) {
		t.Fatalf("GuardRole over a store with no guard: %v, want ErrNoRoleGuard", err)
	}
}
