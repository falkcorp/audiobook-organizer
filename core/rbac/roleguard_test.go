// file: core/rbac/roleguard_test.go
// version: 1.4.0
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

// TestClaimRoleSet_GuardsTheGenerationAndRaisesTheClaimCounter: a claim over
// the current generation commits and changes neither the generation nor a role;
// it fails a GuardRoleClaims staged before it (DeleteRole's guard) and leaves a
// GuardRoleSet staged before it alone (a last-admin decision); a claim over a
// generation a role write has since moved is refused.
func TestClaimRoleSet_GuardsTheGenerationAndRaisesTheClaimCounter(t *testing.T) {
	ctx := context.Background()
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
	g, err := f.svc.RoleSetGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	deleter := f.store.NewBatch()
	defer func() { _ = deleter.Close() }()
	if err := f.svc.GuardRoleClaims(deleter); err != nil {
		t.Fatal(err)
	}
	decider := f.store.NewBatch()
	defer func() { _ = decider.Close() }()
	if err := f.svc.GuardRoleSet(decider, g); err != nil {
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
	if after, err := f.svc.RoleSetGeneration(ctx); err != nil || after != g {
		t.Fatalf("generation after a claim: %d, %v, want it unchanged at %d", after, err, g)
	}
	if err := deleter.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("Commit of a claims guard staged before the claim: %v, want storage.ErrConflict", err)
	}
	if err := decider.Commit(ctx, storage.Sync); err != nil {
		t.Fatalf("Commit of a role-set guard staged before a claim: %v, want nil: a claim is not a role write", err)
	}
	f.mustCreate("custom.wayne", "core:self")
	late := f.store.NewBatch()
	defer func() { _ = late.Close() }()
	if err := f.svc.ClaimRoleSet(late, g); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("ClaimRoleSet over a generation a role write has moved: %v, want storage.ErrConflict", err)
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
	if err := svc.GuardRoleClaims(b); !errors.Is(err, rbac.ErrNoRoleGuard) {
		t.Fatalf("GuardRoleClaims over a store with no guard: %v, want ErrNoRoleGuard", err)
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

// claimOnce commits one ClaimRoleSet in its own batch, over the generation read
// just before, the way a user write that gives a role does; a generation that
// moved between the read and the commit is a conflict, and it reads again.
func claimOnce(ctx context.Context, f *fixture) error {
	return storage.RetryOnConflict(ctx, 64, func(ctx context.Context) error {
		g, err := f.svc.RoleSetGeneration(ctx)
		if err != nil {
			return err
		}
		b := f.store.NewBatch()
		defer func() { _ = b.Close() }()
		if err := f.svc.ClaimRoleSet(b, g); err != nil {
			return err
		}
		return b.Commit(ctx, storage.Sync)
	})
}

// TestDeleteRole_LosingEveryTryIsContendedNotAMismatch: a DeleteRole whose
// Commit conflicts on every one of its tries (a claim lands between its reads
// and its Commit each time) answers ErrContended, which wraps
// storage.ErrConflict and is not ErrVersionMismatch: the caller's version was
// current, the role is untouched, and the same request is expected to land
// later. rbachttp maps it 503 busy with a Retry-After, never a 500 and never the
// 412 that tells a client its read was stale.
func TestDeleteRole_LosingEveryTryIsContendedNotAMismatch(t *testing.T) {
	ctx := context.Background()
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
	lucius := f.mustCreate("custom.lucius", "core:self")
	attempts := 0
	rbac.SetBeforeCommit(f.svc, func() {
		attempts++
		if err := claimOnce(ctx, f); err != nil {
			t.Errorf("the competing claim: %v", err)
		}
	})
	err := f.svc.DeleteRole(ctx, f.admin(), "custom.lucius", lucius.Version)
	if !errors.Is(err, rbac.ErrContended) || !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("DeleteRole that lost every try = %v, want ErrContended wrapping storage.ErrConflict", err)
	}
	if errors.Is(err, rbac.ErrVersionMismatch) {
		t.Fatalf("DeleteRole that lost every try = %v: it must not read as a stale version", err)
	}
	if attempts != rbac.MaxWriteAttempts {
		t.Fatalf("commit attempts = %d, want %d", attempts, rbac.MaxWriteAttempts)
	}
	f.unchanged("custom.lucius", lucius.Version)

	rbac.SetBeforeCommit(f.svc, nil)
	if err := f.svc.DeleteRole(ctx, f.admin(), "custom.lucius", lucius.Version); err != nil {
		t.Fatalf("the same DeleteRole once the claims stop: %v", err)
	}
}

// TestDeleteRole_LandsAgainstAFullBurstOfClaimers: fifteen claims, each landing
// between a try's reads and its Commit (the first fifteen tries), with a
// DeleteRole of a role none of them names. Each claim fails the delete's Commit
// once, so the sixteenth try lands it; the retry is a bounded loop inside the
// service, and the caller never sees a conflict. (Fifteen racing goroutines do
// not exercise this: a claim almost never commits inside the window, so the
// hook places it there.)
func TestDeleteRole_LandsAgainstAFullBurstOfClaimers(t *testing.T) {
	const claimers = 15
	if rbac.MaxWriteAttempts < claimers+1 {
		t.Fatalf("maxWriteAttempts = %d: fewer than the %d tries a burst of %d claimers can cost", rbac.MaxWriteAttempts, claimers+1, claimers)
	}
	ctx := context.Background()
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
	lucius := f.mustCreate("custom.lucius", "core:self")
	attempts := 0
	rbac.SetBeforeCommit(f.svc, func() {
		attempts++
		if attempts <= claimers {
			if err := claimOnce(ctx, f); err != nil {
				t.Errorf("claim %d: %v", attempts, err)
			}
		}
	})
	if err := f.svc.DeleteRole(ctx, f.admin(), "custom.lucius", lucius.Version); err != nil {
		t.Fatalf("DeleteRole against %d claimers: %v", claimers, err)
	}
	if attempts != claimers+1 {
		t.Fatalf("commit attempts = %d, want %d", attempts, claimers+1)
	}
	if _, err := f.svc.Role(ctx, "custom.lucius"); !errors.Is(err, rbac.ErrUnknownRole) {
		t.Fatalf("the role is still there after its delete returned nil: %v", err)
	}
}
