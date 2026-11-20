// file: core/rbac/roleguard_test.go
// version: 1.2.0
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
			// Map order is random: the delete makes the role it deletes, not
			// the one the create case makes.
			cur := f.mustCreate("custom.lucius", "core:self")
			if err := f.svc.DeleteRole(ctx, f.admin(), "custom.lucius", cur.Version); err != nil {
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
		// A guard that is already stale is refused on the spot, as the commit
		// would refuse it; either way the caller's retry loop decides again.
		if err := f.svc.GuardRoleSet(b, g); !errors.Is(err, storage.ErrConflict) {
			t.Fatalf("GuardRoleSet over a stale generation: %v, want storage.ErrConflict", err)
		}
	})
}

// TestGuardRoleSet_TheFirstGuardOfABatchWins (F4-02-ROLE-GUARD r2, n6): a
// caller that decides twice in one batch reads the generation twice, and the
// second read can be newer when a role write landed between them. The second
// guard must not replace the first: the decisions made at the first
// generation lose their guard if it does.
func TestGuardRoleSet_TheFirstGuardOfABatchWins(t *testing.T) {
	ctx := context.Background()
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
	g1, err := f.svc.RoleSetGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b := f.store.NewBatch()
	defer func() { _ = b.Close() }()
	if err := f.svc.GuardRoleSet(b, g1); err != nil {
		t.Fatal(err)
	}
	f.mustCreate("custom.wayne", "core:admin")
	g2, err := f.svc.RoleSetGeneration(ctx)
	if err != nil || g2 == g1 {
		t.Fatalf("generation after a role write: %d then %d, %v", g1, g2, err)
	}
	if err := f.svc.GuardRoleSet(b, g2); err != nil {
		t.Fatalf("the second guard, at the generation now: %v", err)
	}
	if err := b.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("Commit with the first guard stale and a second at the new generation: %v, want storage.ErrConflict", err)
	}
}

// TestClaimRoleSet_GuardsAndBumps: a claim over the current generation
// commits and raises it; a second claim, or a guard, read before it fails.
func TestClaimRoleSet_GuardsAndBumps(t *testing.T) {
	ctx := context.Background()
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
	g, err := f.svc.RoleSetGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reader := f.store.NewBatch()
	defer func() { _ = reader.Close() }()
	if err := f.svc.GuardRoleSet(reader, g); err != nil {
		t.Fatal(err)
	}
	claim := f.store.NewBatch()
	defer func() { _ = claim.Close() }()
	if err := f.svc.ClaimRoleSet(claim, g); err != nil {
		t.Fatalf("ClaimRoleSet at the current generation: %v", err)
	}
	if err := claim.Commit(ctx, storage.Sync); err != nil {
		t.Fatalf("Commit of a claim: %v", err)
	}
	if after, err := f.svc.RoleSetGeneration(ctx); err != nil || after != g+1 {
		t.Fatalf("generation after a claim: %d, %v, want %d", after, err, g+1)
	}
	if err := reader.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("Commit of a guard read before the claim: %v, want storage.ErrConflict", err)
	}
	late := f.store.NewBatch()
	defer func() { _ = late.Close() }()
	if err := f.svc.ClaimRoleSet(late, g); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("ClaimRoleSet over a generation read before the first claim: %v, want storage.ErrConflict", err)
	}
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
	if err := svc.ClaimRoleSet(b, 0); !errors.Is(err, rbac.ErrNoRoleGuard) {
		t.Fatalf("ClaimRoleSet over a store with no guard: %v, want ErrNoRoleGuard", err)
	}
}

// TestDeleteRole_StoreWithoutGuardFailsClosed: DeleteRole guards the role set
// before it reads anything; a store that cannot guard cannot show the delete
// safe against a user write that gives the role, so it refuses.
func TestDeleteRole_StoreWithoutGuardFailsClosed(t *testing.T) {
	ctx := context.Background()
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
	f.mustCreate("custom.wayne", "core:self")
	svc, err := rbac.New(ctx, rbac.Options{Source: "gotham-test"}, rbac.Deps{
		Store: unguardable{f.store}, Compile: f.comp.compile, Audit: f.audit,
		Revocations: f.journal, NewBatch: f.store.NewBatch,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Freeze(ctx); err != nil {
		t.Fatal(err)
	}
	cur, err := f.store.GetRole(ctx, "custom.wayne")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteRole(ctx, f.admin(), "custom.wayne", cur.Version); !errors.Is(err, rbac.ErrNoRoleGuard) {
		t.Fatalf("DeleteRole over a store with no guard: %v, want ErrNoRoleGuard", err)
	}
	if _, err := f.store.GetRole(ctx, "custom.wayne"); err != nil {
		t.Fatalf("a refused delete removed the role: %v", err)
	}
}
