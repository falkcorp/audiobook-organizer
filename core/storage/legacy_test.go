// file: core/storage/legacy_test.go
// version: 1.0.0
// guid: 8e3b1d74-5a9c-4f20-b6e8-2d7c0a4f9e15
// last-edited: 2026-11-08

package storage

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// TestLegacyClaimsNeverRefuseBlindWrites (05-storage §5S.10, A-3): a blind
// Set, Delete and DeleteRange in a Serialized legacy claim commit and leave
// no record (no claim-violation series); a Serialized legacy write takes its
// key's stripe, and a DeleteRange there takes every stripe; an Expect on a
// key of a non-Serialized claim fails with ErrNotSerialized, and on a
// Serialized one is admitted.
func TestLegacyClaimsNeverRefuseBlindWrites(t *testing.T) {
	ctx := context.Background()
	db := newEnv(t, serializedOwners()...).open()
	lr, ok := db.Legacy("gotham")
	if !ok {
		t.Fatal("gotham has no legacy range")
	}
	var took []int
	db.hooks.stripes = func(n int) { took = append(took, n) }

	if err := lr.Set(ctx, []byte("joker_counter"), []byte("7")); err != nil {
		t.Fatalf("blind Set in a Serialized legacy claim: %v", err)
	}
	if err := lr.Delete(ctx, []byte("joker_counter")); err != nil {
		t.Fatalf("blind Delete in a Serialized legacy claim: %v", err)
	}
	if err := lr.DeleteRange(ctx, []byte("joker_counter"), []byte("joker_counter\x00")); err != nil {
		t.Fatalf("blind DeleteRange in a Serialized legacy claim: %v", err)
	}
	if want := []int{1, 1, stripeCount}; len(took) != len(want) || took[0] != want[0] || took[1] != want[1] || took[2] != want[2] {
		t.Fatalf("stripes taken = %v, want %v (one per Serialized write, every stripe for the DeleteRange)", took, want)
	}
	if v := db.ClaimViolations(); len(v) != 0 {
		t.Fatalf("blind legacy writes left violation records %v", v)
	}

	mustErrIs(t, lr.NewBatch().Expect(lr, []byte("joker:1"), nil), ErrNotSerialized)
	b := lr.NewBatch()
	if err := b.Expect(lr, []byte("joker_counter"), nil); err != nil {
		t.Fatalf("Expect in a Serialized legacy claim: %v", err)
	}
	if err := b.Set([]byte("joker_counter"), []byte("8")); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx, Sync); err != nil {
		t.Fatalf("an Expect-checked legacy write: %v", err)
	}
	got, err := lr.Get(ctx, []byte("joker_counter"))
	if err != nil || string(got) != "8" {
		t.Fatalf("joker_counter = %q, %v; want 8", got, err)
	}
}

// TestOrphanedClaimNeedsTakeover (05-storage §5S.10, §5S.3.4 rule 5, DATA-28):
// a removed module's claims stay on the claim ledger, and no other owner
// re-owns them in silence. A build that drops the owner boots and no handle
// reaches its rows; a build whose claim overlaps the orphan without naming
// the former owner in TakesOver is refused at boot; with TakesOver it boots
// and reads the rows the former owner left.
func TestOrphanedClaimNeedsTakeover(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, OwnerSpec{Owner: "gotham", Enabled: true, Spec: Spec{Claims: gothamClaims()}})
	db := e.open()
	lr, _ := db.Legacy("gotham")
	if err := lr.Set(ctx, []byte("joker:1"), []byte("ha")); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	riddler := func(c Claim) []OwnerSpec {
		return []OwnerSpec{{Owner: "riddler", Enabled: true, Spec: Spec{Claims: []Claim{c}}}}
	}

	// Removed, nothing claims its range: the boot succeeds and the orphan's
	// rows are reachable by no handle.
	e.opts.Owners = riddler(Claim{Prefix: "enigma:", Mode: ClaimReadWrite})
	db = e.open()
	other, _ := db.Legacy("riddler")
	if _, err := other.Get(ctx, []byte("joker:1")); err == nil {
		t.Fatal("another owner read the removed owner's row")
	}
	mustErrIs(t, other.Set(ctx, []byte("joker:2"), []byte("x")), ErrOutsideKeyspace)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Claimed without TakesOver: refused, naming the former owner.
	e.opts.Owners = riddler(Claim{Prefix: "joker:", Mode: ClaimReadWrite})
	_, err := e.tryOpen()
	mustErrIs(t, err, ErrClaimConflict)
	if !strings.Contains(err.Error(), "orphaned claim") || !strings.Contains(err.Error(), `TakesOver: "gotham"`) {
		t.Fatalf("the refusal does not name the orphan and its takeover: %v", err)
	}

	// Taken over explicitly: boots and reads the former owner's row.
	e.opts.Owners = riddler(Claim{Prefix: "joker:", Mode: ClaimReadWrite, TakesOver: "gotham"})
	db = e.open()
	took, _ := db.Legacy("riddler")
	got, err := took.Get(ctx, []byte("joker:1"))
	if err != nil || string(got) != "ha" {
		t.Fatalf("joker:1 after the takeover = %q, %v; want ha", got, err)
	}
}

// TestLegacyStepsRunBeforeCoreSteps (05-storage §5S.10, §5S.3.5 "Order",
// R2-4, DATA2-2): on a store below an owner's last legacy step, every
// pending legacy step runs, on the owner's own claims, before any core
// owner's step; a module's own chain is not held behind them. ABO's
// instance over books' steps 59..LegacyCap is
// internal/database's TestLegacySteps59to66RunFirst.
func TestLegacyStepsRunBeforeCoreSteps(t *testing.T) {
	var ran []string
	record := func(ref string) { ran = append(ran, ref) }
	legacy := func(v int) LegacyMigration {
		return LegacyMigration{Version: v, Name: fmt.Sprintf("l%d", v), Up: func(ctx context.Context, r LegacyRange) error {
			record(fmt.Sprintf("gotham#%d", v))
			return r.Set(ctx, fmt.Appendf(nil, "joker:legacy-%d", v), []byte("ha"))
		}}
	}
	step := func(o Owner, v int) Migration {
		return Migration{Version: v, Name: fmt.Sprintf("s%d", v), Compat: Additive, Up: func(context.Context, *MigrationTx) error {
			record(StepRef{Owner: o, Version: v}.String())
			return nil
		}}
	}
	owners := []OwnerSpec{
		{Owner: "core/tasks", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{step("core/tasks", 1)}}},
		{Owner: "core/rbac", Enabled: true, Spec: Spec{Keyspace: true, Migrations: []Migration{step("core/rbac", 1)}}},
		{Owner: "gotham", Enabled: true, Spec: Spec{Claims: gothamVSClaims(), VersionSource: &fakeVersionSource{},
			Legacy: []LegacyMigration{legacy(1), legacy(2), legacy(3)}}},
	}
	e := newEnv(t, bare(owners)...)
	if err := e.open().Close(); err != nil {
		t.Fatal(err)
	}
	e.opts.Owners = owners
	db := e.open()

	firstCore := slices.IndexFunc(ran, func(s string) bool { return strings.HasPrefix(s, "core/") })
	lastLegacy := -1
	for i, s := range ran {
		if strings.HasPrefix(s, "gotham#") {
			lastLegacy = i
		}
	}
	if lastLegacy != 2 || firstCore != 3 || len(ran) != 5 {
		t.Fatalf("order = %v, want gotham#1..3 then the core steps", ran)
	}
	lr, _ := db.Legacy("gotham")
	for v := 1; v <= 3; v++ {
		if _, err := lr.Get(context.Background(), fmt.Appendf(nil, "joker:legacy-%d", v)); err != nil {
			t.Fatalf("legacy step %d's write: %v", v, err)
		}
	}
}
