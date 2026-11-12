// file: core/kernel/restore_unapplied_test.go
// version: 1.1.0
// guid: f7a08fa0-5661-48d2-8e67-284ca3838859
// last-edited: 2026-11-12

package kernel

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// The queue of revocation lines this build cannot apply (restore_reapply.go,
// journal/unapplied.go). A line of a kind only a newer build knows is recorded in
// c/backup/journal_unapplied/<seq>, the head passes it, and the first build that
// knows the kind applies it. These tests play both builds over one store: uqOldBuild
// is a build that predates user.disabled (the default Known is this build, which
// knows it), and "user.frozen" is a kind no build knows yet.

// uqOldBuild is the Known of a build that cannot apply user.disabled.
func uqOldBuild(in *reapplyInput) {
	in.Known = func(k journal.Kind) bool { return k != journal.UserDisabled && journal.KnownKind(k) }
}

// uqWith runs every mutation in order.
func uqWith(muts ...func(*reapplyInput)) func(*reapplyInput) {
	return func(in *reapplyInput) {
		for _, m := range muts {
			m(in)
		}
	}
}

// uqTail makes every line its own re-apply batch.
func uqTail(n int) func(*reapplyInput) { return func(in *reapplyInput) { in.TailBatch = n } }

// uqQueue is the unapplied queue as the next boot reads it.
func (fx *rxFx) uqQueue() []journal.Unapplied {
	fx.t.Helper()
	got, err := journal.ReadUnapplied(fx.ctx, fx.bks())
	if err != nil {
		fx.t.Fatal(err)
	}
	return got
}

func uqSeqs(us []journal.Unapplied) []uint64 {
	var out []uint64
	for _, u := range us {
		out = append(out, u.Seq)
	}
	return out
}

// uqNeverPassed fails the test when the store's head is at or past the line at
// seq and the queue holds no entry for it: the invariant every crash must leave,
// that the head never passes a line that is neither applied nor queued.
func (fx *rxFx) uqNeverPassed(seqs ...uint64) {
	fx.t.Helper()
	queued := uqSeqs(fx.uqQueue())
	for _, seq := range seqs {
		if fx.head().Seq >= seq && !slices.Contains(queued, seq) {
			fx.t.Fatalf("the head is at %d, past the line at seq %d, which is not queued (queue %v): it would never be applied", fx.head().Seq, seq, queued)
		}
	}
}

// uqTrustee puts a custom role granting core:admin in the role store.
func (fx *rxFx) uqTrustee() rbac.RoleName {
	fx.t.Helper()
	const trustee rbac.RoleName = "custom.trustee"
	b := fx.roles.NewBatch()
	defer func() { _ = b.Close() }()
	if err := fx.roles.PutRole(b, &rbac.Role{Name: trustee, Title: "Trustee", Origin: rbac.OriginCustom, Added: []rbac.Permission{"core:admin"}, Version: 1}); err != nil {
		fx.t.Fatal(err)
	}
	if err := b.Commit(fx.ctx, storage.Sync); err != nil {
		fx.t.Fatal(err)
	}
	return trustee
}

// uqFrozen is a line of a kind no build knows yet.
func uqFrozen(user string) journal.Revocation {
	return journal.Revocation{Kind: "user.frozen", Subject: rxUserRef(user)}
}

// TestUnappliedQueue_TheHeadPassesALineThisBuildCannotApply: a newer build's
// revocation kind is queued, not skipped and not a wall. The lines either side of
// it are applied with the head, which ends past it; the journal is not broken and
// takes lines; the boot fails closed for it once, not at every boot.
func TestUnappliedQueue_TheHeadPassesALineThisBuildCannotApply(t *testing.T) {
	fx := newRxFx(t, nil)
	fx.boot(nil, nil)
	owner := fx.user("barbara")
	for _, k := range []string{"key-one", "key-two"} {
		fx.key(k, owner, auth.KeyActive)
	}
	fx.journalOnly(journal.Revocation{Kind: journal.APIKeyRevoked, Subject: rxKeyRef("key-one")})
	fx.journalOnly(uqFrozen(owner))
	fx.journalOnly(journal.Revocation{Kind: journal.APIKeyRevoked, Subject: rxKeyRef("key-two")})
	fx.restart()

	res, _ := fx.boot(nil, nil)
	if res.Reapplied != 2 || res.Queued != 1 || res.Drained != 0 || res.Unapplied != 1 || fx.head().Seq != 3 {
		t.Errorf("re-applied %d, queued %d, drained %d, unapplied %d, head %d, want 2, 1, 0, 1 and 3: the head passes the line it queued", res.Reapplied, res.Queued, res.Drained, res.Unapplied, fx.head().Seq)
	}
	if res.Broken != nil || fx.hasHealth(journal.HealthBroken) || fx.hasHealth(journal.HealthUnwritable) {
		t.Errorf("broken %v, health %v: a queued line is not a broken or refused journal", res.Broken, fx.health())
	}
	if !res.FailedClosed {
		t.Error("the boot that queued the line did not fail closed")
	}
	if q := fx.uqQueue(); len(q) != 1 || q[0].Seq != 2 || q[0].Kind != "user.frozen" || !q[0].Closed {
		t.Errorf("queue = %+v, want one closed entry for seq 2", q)
	}
	for _, k := range []string{"key-one", "key-two"} {
		if got := fx.keyStatus(k); got != auth.KeyRevoked {
			t.Errorf("%s = %q, want revoked", k, got)
		}
	}
	if _, err := fx.comp.AppendLine(fx.ctx, journal.GapLine(fx.clock.now(), journal.GapAppendFailed)); err != nil {
		t.Errorf("the journal refused a line after a line was queued: %v", err)
	}
	epoch := fx.userRow(owner).CredentialEpoch

	fx.restart()
	res, _ = fx.boot(nil, nil)
	if res.FailedClosed || res.Reapplied != 0 || res.Queued != 0 || res.Unapplied != 1 {
		t.Errorf("second boot: failed closed %v, re-applied %d, queued %d, unapplied %d, want false, 0, 0 and 1", res.FailedClosed, res.Reapplied, res.Queued, res.Unapplied)
	}
	if got := fx.userRow(owner).CredentialEpoch; got != epoch {
		t.Errorf("epoch %d -> %d: the second boot failed closed again", epoch, got)
	}
}

// TestUnappliedQueue_AWideningSurvivesEveryRestart: with the head held below the
// unknown line the lines after it were applied again at every boot, so an admin's
// re-enable of a user or a key, or a user's new password, was undone at the next
// restart. The head passes the line now, and nothing is applied twice.
func TestUnappliedQueue_AWideningSurvivesEveryRestart(t *testing.T) {
	fx := newRxFx(t, nil)
	fx.boot(nil, nil)
	sel := fx.user("selina")
	pam := fx.passwordUser("pamela", "operator")
	fx.key("k-sel", sel, auth.KeyActive)
	fx.journalOnly(uqFrozen(sel))
	fx.journalOnly(journal.Revocation{Kind: journal.UserDisabled, Subject: rxUserRef(sel), Epoch: 1})
	fx.journalOnly(journal.Revocation{Kind: journal.PasswordChanged, Subject: rxUserRef(pam), Epoch: 1})
	fx.journalOnly(journal.Revocation{Kind: journal.APIKeyDisabled, Subject: rxKeyRef("k-sel")})
	fx.restart()
	fx.boot(nil, nil)
	if fx.userRow(sel).Status != auth.StatusDisabled || fx.userRow(pam).HasPassword || fx.keyStatus("k-sel") == auth.KeyActive {
		t.Fatalf("the first boot did not apply the lines after the queued one: selina %q, pamela has a password %v, k-sel %q",
			fx.userRow(sel).Status, fx.userRow(pam).HasPassword, fx.keyStatus("k-sel"))
	}

	// The admin re-enables selina and her key; pamela sets a new password.
	fx.commit(func(b *storage.Batch) error {
		u := *fx.userRow(sel)
		u.Status, u.Version = auth.StatusActive, u.Version+1
		return fx.auth.PutUser(b, &u)
	})
	fx.commit(func(b *storage.Batch) error {
		k, err := fx.auth.APIKeyByID(fx.ctx, "k-sel")
		if err != nil {
			return err
		}
		k.Status, k.Version = auth.KeyActive, k.Version+1
		return fx.auth.PutAPIKey(b, k)
	})
	p := *fx.userRow(pam)
	p.Version, p.HasPassword, p.PasswordResetRequired = p.Version+1, true, false
	fx.commit(func(b *storage.Batch) error { return fx.auth.SetPassword(b, &p, rxPasswordHash) })

	for boot := 2; boot <= 3; boot++ {
		fx.restart()
		fx.boot(nil, nil)
		if got := fx.userRow(sel).Status; got != auth.StatusActive {
			t.Errorf("boot %d: selina = %q, want the admin's re-enable to survive", boot, got)
		}
		if got := fx.keyStatus("k-sel"); got != auth.KeyActive {
			t.Errorf("boot %d: k-sel = %q, want the admin's re-enable to survive", boot, got)
		}
		if u := fx.userRow(pam); !u.HasPassword || u.PasswordResetRequired {
			t.Errorf("boot %d: pamela has a password %v, reset required %v: her new password did not survive", boot, u.HasPassword, u.PasswordResetRequired)
		}
	}
}

// TestUnappliedQueue_ABuildThatKnowsTheKindAppliesTheLineOnce: an older build
// queues a user.disabled line it cannot apply; the first build that knows the
// kind applies it and removes the entry in the same step, without failing closed
// again; later boots do nothing; and a rollback to the older build neither
// queues the line again nor undoes it.
func TestUnappliedQueue_ABuildThatKnowsTheKindAppliesTheLineOnce(t *testing.T) {
	fx := newRxFx(t, nil)
	fx.boot(nil, nil)
	owner, sel := fx.user("barbara"), fx.user("selina")
	fx.key("key-one", owner, auth.KeyActive)
	fx.journalOnly(journal.Revocation{Kind: journal.APIKeyRevoked, Subject: rxKeyRef("key-one")})
	fx.journalOnly(journal.Revocation{Kind: journal.UserDisabled, Subject: rxUserRef(sel), Epoch: 1})
	fx.restart()

	res, _ := fx.boot(nil, uqOldBuild) // the older build
	if res.Queued != 1 || res.Reapplied != 1 || fx.head().Seq != 2 || !res.FailedClosed {
		t.Fatalf("older build: queued %d, re-applied %d, head %d, failed closed %v, want 1, 1, 2 and true", res.Queued, res.Reapplied, fx.head().Seq, res.FailedClosed)
	}
	if got := fx.userRow(sel).Status; got != auth.StatusActive {
		t.Fatalf("selina = %q: the older build applied a kind it does not know", got)
	}
	epoch := fx.userRow(owner).CredentialEpoch

	fx.restart()
	res, _ = fx.boot(nil, nil) // this build
	if res.Drained != 1 || res.Unapplied != 0 || res.Queued != 0 || res.Reapplied != 0 || res.FailedClosed {
		t.Errorf("upgraded boot: drained %d, unapplied %d, queued %d, re-applied %d, failed closed %v, want 1, 0, 0, 0 and false", res.Drained, res.Unapplied, res.Queued, res.Reapplied, res.FailedClosed)
	}
	if got := fx.userRow(sel).Status; got != auth.StatusDisabled {
		t.Errorf("selina = %q after the upgrade, want disabled: the queued line was never applied", got)
	}
	if q := fx.uqQueue(); len(q) != 0 {
		t.Errorf("queue = %+v, want it empty once the line is applied", q)
	}
	if got := fx.userRow(owner).CredentialEpoch; got != epoch {
		t.Errorf("epoch %d -> %d: applying a queued line failed closed again", epoch, got)
	}

	fx.restart()
	if res, _ = fx.boot(nil, nil); res.Drained != 0 || res.Unapplied != 0 || res.FailedClosed {
		t.Errorf("third boot: drained %d, unapplied %d, failed closed %v, want nothing to do", res.Drained, res.Unapplied, res.FailedClosed)
	}

	fx.restart()
	res, _ = fx.boot(nil, uqOldBuild) // rolled back
	if res.Queued != 0 || res.Unapplied != 0 || res.FailedClosed || res.Drained != 0 {
		t.Errorf("rollback boot: queued %d, unapplied %d, failed closed %v, drained %d, want none: the line is below the head and applied", res.Queued, res.Unapplied, res.FailedClosed, res.Drained)
	}
	if got := fx.userRow(sel).Status; got != auth.StatusDisabled {
		t.Errorf("selina = %q after the rollback, want still disabled", got)
	}
	if got := fx.userRow(owner).CredentialEpoch; got != epoch {
		t.Errorf("epoch %d -> %d across the upgrade and the rollback", epoch, got)
	}
}

// TestUnappliedQueue_AKindStillUnknownStaysWhileOthersAreApplied: a queue holds
// lines of several kinds. A build that knows some of them applies those and
// removes their entries; the entries of the kinds it still does not know stay,
// and it does not fail closed for them again.
func TestUnappliedQueue_AKindStillUnknownStaysWhileOthersAreApplied(t *testing.T) {
	fx := newRxFx(t, nil)
	fx.boot(nil, nil)
	sel, pam := fx.user("selina"), fx.passwordUser("pamela", "operator")
	fx.journalOnly(journal.Revocation{Kind: journal.UserDisabled, Subject: rxUserRef(sel), Epoch: 1})    // 1
	fx.journalOnly(uqFrozen(sel))                                                                        // 2
	fx.journalOnly(journal.Revocation{Kind: journal.PasswordChanged, Subject: rxUserRef(pam), Epoch: 1}) // 3
	fx.restart()
	noPasswords := func(in *reapplyInput) {
		in.Known = func(k journal.Kind) bool {
			return k != journal.UserDisabled && k != journal.PasswordChanged && journal.KnownKind(k)
		}
	}
	noDisable := func(in *reapplyInput) {
		in.Known = func(k journal.Kind) bool { return k != journal.UserDisabled && journal.KnownKind(k) }
	}

	if res, _ := fx.boot(nil, noPasswords); res.Queued != 3 || res.Unapplied != 3 {
		t.Fatalf("oldest build: queued %d, unapplied %d, want 3 and 3", res.Queued, res.Unapplied)
	}
	fx.restart()
	res, _ := fx.boot(nil, noDisable) // knows password.changed, not user.disabled
	if res.Drained != 1 || res.Unapplied != 2 || res.FailedClosed {
		t.Errorf("middle build: drained %d, unapplied %d, failed closed %v, want 1, 2 and false", res.Drained, res.Unapplied, res.FailedClosed)
	}
	if u := fx.userRow(pam); u.HasPassword || !u.PasswordResetRequired {
		t.Errorf("pamela has a password %v, reset required %v: the password line was not applied", u.HasPassword, u.PasswordResetRequired)
	}
	if got := fx.userRow(sel).Status; got != auth.StatusActive {
		t.Errorf("selina = %q: the middle build applied user.disabled", got)
	}
	if got := uqSeqs(fx.uqQueue()); !slices.Equal(got, []uint64{1, 2}) {
		t.Errorf("queue = %v, want [1 2]", got)
	}
	fx.restart()
	res, _ = fx.boot(nil, nil) // knows user.disabled; user.frozen is still no one's
	if res.Drained != 1 || res.Unapplied != 1 || res.FailedClosed {
		t.Errorf("this build: drained %d, unapplied %d, failed closed %v, want 1, 1 and false", res.Drained, res.Unapplied, res.FailedClosed)
	}
	if got := fx.userRow(sel).Status; got != auth.StatusDisabled {
		t.Errorf("selina = %q, want disabled", got)
	}
	if got := uqSeqs(fx.uqQueue()); !slices.Equal(got, []uint64{2}) {
		t.Errorf("queue = %v, want [2]: the kind nobody knows yet", got)
	}
}

// TestUnappliedQueue_FailsClosedOncePerEntry: a boot that finds a new line it
// cannot apply fails closed for it; one that finds only entries an earlier boot
// answered does not. Two lines queued by two boots cost two passes, never three.
func TestUnappliedQueue_FailsClosedOncePerEntry(t *testing.T) {
	fx := newRxFx(t, nil)
	fx.boot(nil, nil)
	owner := fx.user("barbara")
	closes := 0
	boot := func(want bool) {
		t.Helper()
		fx.restart()
		res, _ := fx.boot(nil, nil)
		if res.FailedClosed != want {
			t.Errorf("failed closed = %v, want %v (queue %v)", res.FailedClosed, want, uqSeqs(fx.uqQueue()))
		}
		if res.FailedClosed {
			closes++
		}
	}
	fx.journalOnly(uqFrozen(owner))
	boot(true)
	boot(false)
	fx.journalOnly(journal.Revocation{Kind: "user.thawed", Subject: rxUserRef(owner)})
	boot(true) // the new line, not the old one
	boot(false)
	boot(false)
	if closes != 2 {
		t.Errorf("%d fail-closed passes for 2 queued lines, want 2", closes)
	}
	for _, q := range fx.uqQueue() {
		if !q.Closed {
			t.Errorf("entry %d is still open after the boots that answered it", q.Seq)
		}
	}
}

// TestUnappliedQueue_ACrashAtEveryBatchLosesNoLine: unknown lines between known
// ones, one line to a batch, a crash before each batch in turn and then a normal
// boot. The head is never past an unknown line that is not queued, every known
// line takes effect, both unknown lines end up queued, and the answer for them
// (fail closed) is given once.
func TestUnappliedQueue_ACrashAtEveryBatchLosesNoLine(t *testing.T) {
	for crash := 1; crash <= 17; crash++ { // 16 batches: 17 never fires
		t.Run(fmt.Sprintf("crash before batch %d", crash), func(t *testing.T) {
			fx := newRxFx(t, nil)
			fx.boot(nil, nil)
			u1, u2, u3 := fx.user("barbara"), fx.user("selina", "operator", "viewer"), fx.user("harley")
			u4 := fx.passwordUser("pamela", "operator", "viewer")
			fx.key("k1", u1, auth.KeyActive)
			fx.key("k2", u1, auth.KeyActive)
			for _, l := range []journal.Revocation{
				{Kind: journal.APIKeyRevoked, Subject: rxKeyRef("k1")}, // 1
				uqFrozen(u1), // 2 unknown
				{Kind: journal.UserDisabled, Subject: rxUserRef(u1), Epoch: 1},                        // 3
				{Kind: journal.APIKeyRevoked, Subject: rxKeyRef("k2")},                                // 4
				{Kind: journal.RoleUnassigned, Subject: rxUserRef(u2), Object: rxRoleRef("operator")}, // 5
				{Kind: journal.UserDeleted, Subject: rxUserRef(u3)},                                   // 6
				{Kind: "user.thawed", Subject: rxUserRef(u1)},                                         // 7 unknown
				{Kind: journal.PasswordChanged, Subject: rxUserRef(u4), Epoch: 1},                     // 8
				{Kind: journal.RoleUnassigned, Subject: rxUserRef(u2), Object: rxRoleRef("viewer")},   // 9
			} {
				fx.journalOnly(l)
			}
			fx.restart()

			closes := 0
			first, _, err := fx.tryBoot(nil, uqWith(uqTail(1), crashAtBatch(crash)), nil)
			if first.FailedClosed {
				closes++
			}
			t.Logf("first boot: err %v, head %d, queue %v", err, fx.head().Seq, uqSeqs(fx.uqQueue()))
			fx.uqNeverPassed(2, 7)

			fx.restart()
			res, _, err := fx.tryBoot(nil, uqTail(1), nil)
			if err != nil {
				t.Fatalf("second boot: %v", err)
			}
			if res.FailedClosed {
				closes++
			}
			if fx.head().Seq != 9 {
				t.Errorf("head %d, want 9", fx.head().Seq)
			}
			if fx.keyStatus("k1") != auth.KeyRevoked || fx.keyStatus("k2") != auth.KeyRevoked || fx.userRow(u1).Status != auth.StatusDisabled {
				t.Errorf("k1 %q, k2 %q, u1 %q: a known line was lost", fx.keyStatus("k1"), fx.keyStatus("k2"), fx.userRow(u1).Status)
			}
			if r := fx.userRow(u2).Roles; len(r) != 0 {
				t.Errorf("selina's roles = %v, want none", r)
			}
			if _, err := fx.auth.GetUser(fx.ctx, auth.UserID(u3)); err == nil {
				t.Error("harley was not deleted")
			}
			if usr := fx.userRow(u4); usr.HasPassword || !usr.PasswordResetRequired {
				t.Errorf("pamela: has a password %v, reset required %v: her password was not blanked", usr.HasPassword, usr.PasswordResetRequired)
			}
			if q := fx.uqQueue(); !slices.Equal(uqSeqs(q), []uint64{2, 7}) || !q[0].Closed || !q[1].Closed {
				t.Errorf("queue = %+v, want both unknown lines, answered", q)
			}
			fx.restart()
			if res, _, err = fx.tryBoot(nil, uqTail(1), nil); err != nil || res.FailedClosed {
				t.Errorf("third boot: err %v, failed closed %v, want a quiet boot", err, res.FailedClosed)
			}
			if closes != 1 {
				t.Errorf("%d fail-closed passes across the first two boots, want exactly 1", closes)
			}
		})
	}
}

// TestUnappliedQueue_ACrashDuringTheDrainAppliesAndDequeuesTogether: three
// queued user.disabled lines and a build that knows the kind, one line to a chunk,
// a crash before each batch in turn. An entry that is gone has its line applied
// (the dequeue is staged in the batch that applies it), and the next boot
// finishes the rest without failing closed.
func TestUnappliedQueue_ACrashDuringTheDrainAppliesAndDequeuesTogether(t *testing.T) {
	for crash := 1; crash <= 7; crash++ {
		t.Run(fmt.Sprintf("crash before batch %d", crash), func(t *testing.T) {
			fx := newRxFx(t, nil)
			fx.boot(nil, nil)
			users := []string{fx.user("barbara"), fx.user("selina"), fx.user("harley")}
			for _, u := range users {
				fx.journalOnly(journal.Revocation{Kind: journal.UserDisabled, Subject: rxUserRef(u), Epoch: 1})
			}
			fx.restart()
			if res, _ := fx.boot(nil, uqOldBuild); res.Queued != 3 {
				t.Fatalf("queued %d, want 3", res.Queued)
			}
			fx.restart()

			_, _, err := fx.tryBoot(nil, uqWith(uqTail(1), crashAtBatch(crash)), nil)
			t.Logf("crashing boot: err %v, queue %v", err, uqSeqs(fx.uqQueue()))
			queued := uqSeqs(fx.uqQueue())
			for i, u := range users {
				seq := uint64(i + 1)
				if !slices.Contains(queued, seq) && fx.userRow(u).Status != auth.StatusDisabled {
					t.Errorf("entry %d was removed but %s is %q: the dequeue went in without the apply", seq, u, fx.userRow(u).Status)
				}
			}

			fx.restart()
			res, _, err := fx.tryBoot(nil, uqTail(1), nil)
			if err != nil {
				t.Fatalf("second boot: %v", err)
			}
			if res.FailedClosed || res.Unapplied != 0 {
				t.Errorf("second boot: failed closed %v, unapplied %d, want false and 0", res.FailedClosed, res.Unapplied)
			}
			for _, u := range users {
				if got := fx.userRow(u).Status; got != auth.StatusDisabled {
					t.Errorf("%s = %q after the drain finished, want disabled", u, got)
				}
			}
		})
	}
}

// TestUnappliedQueue_AKeptAdminsRestrictionNeverCarriesTheHeadPastAnUnqueuedLine:
// a role removal that would leave no active core:admin holder is skipped by rbac
// and the kernel restricts the holder after that batch (SEC3-15, SEC4-12), and the
// restriction is journaled by the re-apply itself, and an append stages its own
// seq as the head. The unknown line is queued before any of that, so the head may
// pass it whichever side of it the removal is, and whichever batch a crash lands
// in.
func TestUnappliedQueue_AKeptAdminsRestrictionNeverCarriesTheHeadPastAnUnqueuedLine(t *testing.T) {
	for _, unknownFirst := range []bool{true, false} {
		for crash := 0; crash <= 5; crash++ { // 0: no crash
			t.Run(fmt.Sprintf("unknown line first %v, crash before batch %d", unknownFirst, crash), func(t *testing.T) {
				fx := newRxFx(t, nil)
				fx.boot(nil, nil)
				trustee := fx.uqTrustee()
				bruce := fx.user("bruce-wayne", trustee)
				fx.key("key-batsignal", bruce, auth.KeyActive)
				unknown := uqFrozen(bruce)
				removal := journal.Revocation{Kind: journal.RoleUnassigned, Subject: rxUserRef(bruce), Object: rxRoleRef(string(trustee))}
				unknownSeq := uint64(2)
				if unknownFirst {
					fx.journalOnly(unknown)
					fx.journalOnly(removal)
					unknownSeq = 1
				} else {
					fx.journalOnly(removal)
					fx.journalOnly(unknown)
				}
				ends := fx.comp.Tail().Seq
				fx.restart()

				mut := uqTail(1)
				if crash > 0 {
					mut = uqWith(uqTail(1), crashAtBatch(crash))
				}
				res, _, err := fx.tryBoot(nil, mut, nil)
				fx.uqNeverPassed(unknownSeq)
				if crash > 0 && err != nil {
					fx.restart()
					res, _, err = fx.tryBoot(nil, uqTail(1), nil)
				}
				if err != nil {
					t.Fatalf("boot: %v", err)
				}
				if res.Queued != 1 && crash == 0 {
					t.Errorf("queued %d, want 1", res.Queued)
				}
				if got := uqSeqs(fx.uqQueue()); !slices.Equal(got, []uint64{unknownSeq}) {
					t.Errorf("queue = %v, want [%d]", got, unknownSeq)
				}
				if got := fx.comp.Tail().Seq; got <= ends {
					t.Errorf("journal ends at seq %d, want a line past %d: the journal is not refused, so the restriction is journaled", got, ends)
				}
				if fx.head().Seq < unknownSeq {
					t.Errorf("head %d is still below the queued line at %d", fx.head().Seq, unknownSeq)
				}
				u := fx.userRow(bruce)
				if !slices.Equal(u.Roles, []rbac.RoleName{trustee}) || !u.SignInRestricted {
					t.Errorf("roles %v, sign-in restricted %v: the last admin keeps the role and is restricted", u.Roles, u.SignInRestricted)
				}
				if got := fx.keyStatus("key-batsignal"); got != auth.KeyNeedsConfirmation {
					t.Errorf("key-batsignal = %q, want needs_confirmation (a restricted last admin's keys)", got)
				}
			})
		}
	}
}

// TestUnappliedQueue_AbortedHooksOfAnUnknownKindAreNeitherAppliedNorQueued: a
// hook line of an aborted restore describes a store that was swapped back from.
// It is verified and skipped, applied by no one and queued for no one, whatever
// its kind; a live revocation after it still is applied.
func TestUnappliedQueue_AbortedHooksOfAnUnknownKindAreNeitherAppliedNorQueued(t *testing.T) {
	fx := newRxFx(t, nil)
	fx.boot(nil, nil)
	barbara, selina := fx.user("barbara"), fx.user("selina")
	fx.key("key-batarang", barbara, auth.KeyActive)
	fx.key("key-cowl", selina, auth.KeyActive)
	// WriteLine, not Append: the lines reach the journal and nothing else, so the
	// store's head stays at 0 as it would after a crash.
	write := func(r journal.Revocation) uint64 {
		t.Helper()
		r.At = fx.clock.now()
		if _, err := fx.comp.WriteLine(fx.ctx, r); err != nil {
			t.Fatal(err)
		}
		return fx.comp.Tail().Seq
	}
	write(uqFrozen(barbara)) // 1: queued
	restore := write(journal.RestoreLineOf(journal.RestoreInfo{Kind: journal.RestoreArchive, Source: "arch-1", RestoreID: "restore-1"}, fx.clock.now()))
	write(journal.Revocation{Kind: journal.APIKeyRevoked, Subject: rxKeyRef("key-cowl"), RestoreSeq: restore}) // its hook line
	write(journal.Revocation{Kind: "user.thawed", Subject: rxUserRef(selina), RestoreSeq: restore})            // and one of a kind no one knows
	write(journal.AbortedLine(restore, fx.clock.now()))
	write(journal.Revocation{Kind: journal.APIKeyRevoked, Subject: rxKeyRef("key-batarang")})
	fx.restart()

	res, _ := fx.boot(nil, nil)
	// The pass for the queued line journals the restriction it derives (key-cowl
	// needing confirmation) as a line of its own, past line 6; the head follows it.
	if tail := fx.comp.Tail().Seq; res.Reapplied != 1 || res.Queued != 1 || fx.head().Seq != tail || tail != 7 {
		t.Errorf("re-applied %d, queued %d, head %d, journal ends at %d, want 1, 1 and both at 7", res.Reapplied, res.Queued, fx.head().Seq, tail)
	}
	if got := uqSeqs(fx.uqQueue()); !slices.Equal(got, []uint64{1}) {
		t.Errorf("queue = %v, want [1]: the aborted restore's hook line of an unknown kind is not owed", got)
	}
	if got := fx.keyStatus("key-batarang"); got != auth.KeyRevoked {
		t.Errorf("key-batarang = %q, want revoked: a live revocation after the queued line is applied", got)
	}
	// The pass for the queued line leaves an active key needing confirmation; the
	// aborted restore's hook line would have revoked it.
	if got := fx.keyStatus("key-cowl"); got != auth.KeyNeedsConfirmation {
		t.Errorf("key-cowl = %q, want needs_confirmation: a hook line of an aborted restore was applied", got)
	}
}

// TestUnappliedQueue_AnUntrustedChainQueuesAndAppliesWhatItVerifies: a journal
// cut below the anchor is untrusted and still applies its verified lines after
// the store's head. A line of a kind this build cannot apply among them is
// queued like any other, and the restriction of a kept last admin that follows
// cannot take the head past it unqueued. Nothing already queued is drained from
// such a chain.
func TestUnappliedQueue_AnUntrustedChainQueuesAndAppliesWhatItVerifies(t *testing.T) {
	fx := newRxFx(t, nil)
	fx.boot(nil, nil)
	trustee := fx.uqTrustee()
	bruce := fx.user("bruce-wayne", trustee)
	fx.key("key-batsignal", bruce, auth.KeyActive)
	fx.key("key-cowl", bruce, auth.KeyActive)
	archive := fx.snapshot("archive") // head 0, bruce the only admin

	// Lines 1 and 2 reach the journal with their heads; line 3 is the one the cut
	// removes, and the anchor (the committed head) is 3.
	for _, r := range []journal.Revocation{uqFrozen(bruce), {Kind: journal.RoleUnassigned, Subject: rxUserRef(bruce), Object: rxRoleRef(string(trustee))}} {
		r.At = fx.clock.now()
		bb := fx.auth.NewBatch()
		if err := fx.comp.Append(fx.ctx, bb, r); err != nil {
			t.Fatal(err)
		}
		if err := bb.Commit(fx.ctx, storage.Sync); err != nil {
			t.Fatal(err)
		}
		_ = bb.Close()
	}
	fx.revokeKey("key-cowl") // line 3, head 3
	fx.restart()
	truncateJournal(t, fx.journalPath(), 2)
	fx.swapIn(archive)

	res, _ := fx.boot(nil, nil)
	if res.Broken == nil || !res.FailedClosed {
		t.Fatalf("broken %v, failed closed %v: the cut is below the anchor", res.Broken, res.FailedClosed)
	}
	if res.Reapplied != 1 || res.Queued != 1 {
		t.Errorf("re-applied %d, queued %d, want 1 and 1", res.Reapplied, res.Queued)
	}
	if got := uqSeqs(fx.uqQueue()); !slices.Equal(got, []uint64{1}) {
		t.Errorf("queue = %v, want [1]", got)
	}
	fx.uqNeverPassed(1)
	if u := fx.userRow(bruce); !slices.Equal(u.Roles, []rbac.RoleName{trustee}) || !u.SignInRestricted {
		t.Errorf("roles %v, sign-in restricted %v: the last admin keeps the role and is restricted", u.Roles, u.SignInRestricted)
	}
}

// TestUnappliedQueue_AnEntryIsAppliedOnlyForTheLineTheVerifiedChainHolds: an
// entry names a line by seq and mac, and the line applied is the verified chain's.
// An entry with another mac, one past the chain's end, and every entry when the
// chain is not whole, stay: nothing is applied from them, and none of it fails
// closed again.
func TestUnappliedQueue_AnEntryIsAppliedOnlyForTheLineTheVerifiedChainHolds(t *testing.T) {
	setup := func() (*rxFx, string, journal.Line) {
		fx := newRxFx(t, nil)
		fx.boot(nil, nil)
		sel := fx.user("selina")
		fx.journalOnly(journal.Revocation{Kind: journal.UserDisabled, Subject: rxUserRef(sel), Epoch: 1})
		fx.restart()
		fx.boot(nil, uqOldBuild) // queues seq 1
		fx.restart()
		return fx, sel, fx.chain().Lines[0]
	}
	replaceEntry := func(fx *rxFx, u journal.Unapplied) {
		t.Helper()
		b := fx.bks().NewBatch()
		defer func() { _ = b.Close() }()
		if err := b.In(fx.bks()).Delete([]byte(fmt.Sprintf("journal_unapplied/%020d", 1))); err != nil {
			t.Fatal(err)
		}
		if err := journal.StageUnapplied(fx.ctx, b, fx.bks(), u); err != nil {
			t.Fatal(err)
		}
		if err := b.Commit(fx.ctx, storage.Sync); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("another mac", func(t *testing.T) {
		fx, sel, line := setup()
		forged := journal.UnappliedOf(line, true)
		forged.MAC = "00" + forged.MAC[2:]
		replaceEntry(fx, forged)
		res, _ := fx.boot(nil, nil)
		if res.Drained != 0 || res.Unapplied != 1 || res.FailedClosed || fx.userRow(sel).Status != auth.StatusActive {
			t.Errorf("drained %d, unapplied %d, failed closed %v, selina %q: an entry that is not the chain's line applied nothing", res.Drained, res.Unapplied, res.FailedClosed, fx.userRow(sel).Status)
		}
	})
	t.Run("past the chain's end", func(t *testing.T) {
		fx, sel, line := setup()
		far := journal.UnappliedOf(line, true)
		far.Seq = 99
		b := fx.bks().NewBatch()
		if err := journal.StageUnapplied(fx.ctx, b, fx.bks(), far); err != nil {
			t.Fatal(err)
		}
		if err := b.Commit(fx.ctx, storage.Sync); err != nil {
			t.Fatal(err)
		}
		_ = b.Close()
		res, _ := fx.boot(nil, nil)
		if res.Drained != 1 || res.Unapplied != 1 || res.FailedClosed || fx.userRow(sel).Status != auth.StatusDisabled {
			t.Errorf("drained %d, unapplied %d, failed closed %v, selina %q: the real entry drains and the one past the end stays", res.Drained, res.Unapplied, res.FailedClosed, fx.userRow(sel).Status)
		}
		if got := uqSeqs(fx.uqQueue()); !slices.Equal(got, []uint64{99}) {
			t.Errorf("queue = %v, want [99]", got)
		}
	})
	t.Run("a chain that does not verify at the line", func(t *testing.T) {
		fx, sel, _ := setup()
		editJournalLine(t, fx.journalPath(), 1, "u-selina", "u-barbara") // a hand edit the MAC catches
		res, _ := fx.boot(nil, nil)
		if res.Broken == nil || res.Drained != 0 || res.Unapplied != 1 || fx.userRow(sel).Status != auth.StatusActive {
			t.Errorf("broken %v, drained %d, unapplied %d, selina %q: the chain does not hold the line the entry names", res.Broken, res.Drained, res.Unapplied, fx.userRow(sel).Status)
		}
	})
	// The approved design: nothing is drained from a journal that is not whole,
	// even a line the verified part of it holds. The entries were answered when
	// they were queued, the boot reports the break and fails closed for it on its
	// own, and the entries wait for a journal that verifies.
	t.Run("a chain that breaks after the line", func(t *testing.T) {
		fx := newRxFx(t, nil)
		fx.boot(nil, nil)
		sel, har := fx.user("selina"), fx.user("harley")
		fx.journalOnly(journal.Revocation{Kind: journal.UserDisabled, Subject: rxUserRef(sel), Epoch: 1})
		fx.journalOnly(journal.Revocation{Kind: journal.UserDisabled, Subject: rxUserRef(har), Epoch: 1})
		fx.restart()
		if res, _ := fx.boot(nil, uqOldBuild); res.Queued != 2 { // queues 1 and 2
			t.Fatalf("queued %d, want 2", res.Queued)
		}
		fx.restart()
		editJournalLine(t, fx.journalPath(), 2, "u-harley", "u-barbara") // line 2 no longer verifies; line 1 still does
		res, _ := fx.boot(nil, nil)
		if res.Broken == nil || res.Drained != 0 || res.Unapplied != 2 || fx.userRow(sel).Status != auth.StatusActive || fx.userRow(har).Status != auth.StatusActive {
			t.Errorf("broken %v, drained %d, unapplied %d, selina %q, harley %q: a chain that is not whole drains nothing, not even the verified line", res.Broken, res.Drained, res.Unapplied, fx.userRow(sel).Status, fx.userRow(har).Status)
		}
	})
}

// TestUnappliedQueue_ARestoreBootQueuesAndStillWritesItsLine: a restore boot whose
// journal holds a line this build cannot apply writes its restore line under it
// (the journal used to be refused for good, so the line could not be written, the
// restore journal recorded seq 0, and a later restore_aborted could not name it),
// queues the line, and leaves the fail-closed pass to the next plain boot, as the
// hooks do their own on a restore.
func TestUnappliedQueue_ARestoreBootQueuesAndStillWritesItsLine(t *testing.T) {
	s := newRestoreScene(t, func(g *batcave) {
		g.journalOnly(journal.Revocation{Kind: journal.APIKeyRevoked, Subject: rxKeyRef("key-redrobin")})
		g.journalOnly(uqFrozen(g.user("alfred")))
		g.journalOnly(journal.Revocation{Kind: journal.APIKeyRevoked, Subject: rxKeyRef("key-batmobile")})
	})
	steps := &rxSteps{}
	res, hres := s.boot(archiveBoot("restore-1", "arch-1", s.previous, steps), nil)
	if !hres.Ran || res.RestoreLine == 0 || res.Queued != 1 || res.FailedClosed || res.Broken != nil {
		t.Fatalf("hooks ran %v, restore line %d, queued %d, failed closed %v, broken %v: want a restore line, one queued line and no pass on a restore boot",
			hres.Ran, res.RestoreLine, res.Queued, res.FailedClosed, res.Broken)
	}
	if got := steps.log(); got != "restore_line:"+u64(res.RestoreLine)+",hooks_done,marker_written,applied" {
		t.Errorf("restore journal edges = %q, want the restore line named and every edge walked", got)
	}
	if s.keyStatus("key-redrobin") != auth.KeyRevoked || s.keyStatus("key-batmobile") != auth.KeyRevoked {
		t.Errorf("redrobin %q, batmobile %q: the lines either side of the queued one were not applied", s.keyStatus("key-redrobin"), s.keyStatus("key-batmobile"))
	}
	if q := s.uqQueue(); len(q) != 1 || q[0].Closed {
		t.Errorf("queue = %+v, want one entry, still open", q)
	}

	s.restart()
	next, _ := s.boot(nil, nil)
	if !next.FailedClosed || next.Unapplied != 1 {
		t.Errorf("next plain boot: failed closed %v, unapplied %d, want the pass the restore boot left, and one entry", next.FailedClosed, next.Unapplied)
	}
	s.restart()
	if again, _ := s.boot(nil, nil); again.FailedClosed {
		t.Error("the boot after that failed closed again")
	}
}

// TestUnappliedQueue_RestoreAbortedStillWorksAfterABootQueuedALine: the swap-back
// of a restore appends a restore_aborted line naming the restore line; with the
// journal refused for good after an unknown line, that append failed. It succeeds,
// and names the line.
func TestUnappliedQueue_RestoreAbortedStillWorksAfterABootQueuedALine(t *testing.T) {
	s := newRestoreScene(t, func(g *batcave) {
		g.journalOnly(uqFrozen(g.user("alfred")))
	})
	res, _ := s.boot(archiveBoot("restore-1", "arch-1", s.previous, &rxSteps{}), nil)
	if res.Queued != 1 || res.RestoreLine == 0 {
		t.Fatalf("queued %d, restore line %d, want 1 and a line", res.Queued, res.RestoreLine)
	}
	if err := restoreAborted(s.ctx, s.comp, s.keys, "restore-1", s.clock.now()); err != nil {
		t.Fatalf("restoreAborted after a boot that queued a line: %v", err)
	}
	lines := s.chain().Lines
	last := lines[len(lines)-1]
	if seq, ok := last.AbortedSeq(); last.Kind != journal.RestoreAborted || !ok || seq != res.RestoreLine {
		t.Errorf("last journal line = %+v (aborted seq %d, %v), want a restore_aborted for line %d", last.Revocation, seq, ok, res.RestoreLine)
	}
}

// TestUnappliedQueue_AHeldBootQueuesWithoutMovingTheHead: a replaced store whose
// restore line cannot be written holds the head until its hooks have run, and the
// journal takes no line meanwhile. A line this build cannot apply is queued all
// the same, before the head moves; the held head commits after the hooks and the
// journal takes lines again. When the hooks fail the head stays, the entry stays
// answered, and the retry queues it again without a second pass.
func TestUnappliedQueue_AHeldBootQueuesWithoutMovingTheHead(t *testing.T) {
	prep := func() *rxFx {
		fx := newRxFx(t, nil)
		fx.boot(nil, nil)
		u := fx.user("barbara")
		fx.key("key-a", u, auth.KeyActive)
		archive := fx.snapshot("archive")
		fx.revokeKey("key-a")       // 1
		fx.journalOnly(uqFrozen(u)) // 2
		if err := journal.CommitHead(fx.ctx, fx.bks(), fx.comp.Tail()); err != nil {
			t.Fatal(err)
		}
		fx.restart() // anchor 2
		fx.swapIn(archive)
		brokenKeyring(fx) // the restore line cannot be sealed: the head is held
		return fx
	}

	t.Run("the hooks complete", func(t *testing.T) {
		fx := prep()
		res, hres := fx.boot(nil, nil)
		if res.Class != classReplaced || !hres.Ran || res.RestoreLine != 0 || res.Held.Seq != 2 || res.Queued != 1 {
			t.Fatalf("class %q, hooks ran %v, restore line %d, held %d, queued %d, want a replaced store, no line, head 2 held, one queued", res.Class, hres.Ran, res.RestoreLine, res.Held.Seq, res.Queued)
		}
		if got := fx.head().Seq; got != 2 {
			t.Errorf("head = %d, want 2: the hooks completed, so the held head is committed", got)
		}
		if got := uqSeqs(fx.uqQueue()); !slices.Equal(got, []uint64{2}) {
			t.Errorf("queue = %v, want [2]", got)
		}
		fx.keys.(*rxKeys).active = "d1" // the keyring is back
		if _, err := fx.comp.AppendLine(fx.ctx, journal.GapLine(fx.clock.now(), journal.GapAppendFailed)); err != nil {
			t.Errorf("the journal refused a line after the hooks completed: %v", err)
		}
	})

	t.Run("the hooks fail", func(t *testing.T) {
		fx := prep()
		boom := errors.New("the hook failed")
		res, _, err := fx.tryBoot(nil, nil, func(hookEnv) ([]journal.RestoreHook, error) {
			return []journal.RestoreHook{func(context.Context, journal.RestoreContext) (map[string]int, error) { return nil, boom }}, nil
		})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the hook's", err)
		}
		if got := fx.head().Seq; got != 0 || res.Held.Seq != 2 {
			t.Fatalf("head %d, held %d, want 0 and 2: the verdict is not durable until the hooks ran", got, res.Held.Seq)
		}
		if q := fx.uqQueue(); len(q) != 1 || q[0].Seq != 2 || !q[0].Closed {
			t.Errorf("queue = %+v, want seq 2, answered by the first boot", q)
		}
		if _, err := fx.comp.AppendLine(fx.ctx, journal.GapLine(fx.clock.now(), journal.GapAppendFailed)); err == nil {
			t.Error("the journal took a line while the hooks had not completed")
		}

		fx.restart()
		brokenKeyring(fx) // still no restore line
		res, hres := fx.boot(nil, nil)
		if res.Class != classReplaced || !hres.Ran || res.Queued != 1 || res.FailedClosed && len(fx.uqQueue()) == 1 && !fx.uqQueue()[0].Closed {
			t.Errorf("retry: class %q, hooks ran %v, queued %d", res.Class, hres.Ran, res.Queued)
		}
		if q := fx.uqQueue(); len(q) != 1 || !q[0].Closed {
			t.Errorf("queue after the retry = %+v, want the one entry, still answered (queued again, not reset)", q)
		}
	})
}

// TestReplacedStoreWithAnUnknownKind_FailsClosedForTheLineOnce: the boot that
// finds a replaced store appends its restore line after the unknown line and
// queues that line; the answer for it is given by the boot that queued it and not
// again, so the credential epochs are bumped once across the boots that follow.
func TestReplacedStoreWithAnUnknownKind_FailsClosedForTheLineOnce(t *testing.T) {
	fx := newRxFx(t, nil)
	fx.boot(nil, nil)
	owner := fx.user("alfred")
	fx.key("key-a", owner, auth.KeyActive)
	archive := fx.snapshot("archive")
	fx.revokeKey("key-a")
	fx.journalOnly(uqFrozen(owner))
	fx.restart()
	fx.swapIn(archive)
	var epochs []int64
	var closed []bool
	for range 3 {
		res, hres := fx.boot(nil, nil)
		closed = append(closed, res.FailedClosed)
		if res.Class == classReplaced && !hres.Ran {
			t.Error("a replaced store ran no hooks")
		}
		fx.restart()
		epochs = append(epochs, fx.userRow(owner).CredentialEpoch)
	}
	// The replaced store's own hooks bump every epoch once on the first boot too;
	// the pass for the queued line is a second bump on that boot, never one on the
	// boots after it.
	if !slices.Equal(closed, []bool{true, false, false}) || epochs[1] != epochs[0] || epochs[2] != epochs[0] {
		t.Errorf("failed closed per boot %v, credential epochs %v: want one pass for the queued line and no bump after the first boot", closed, epochs)
	}
}

// TestUnappliedQueue_AReplacedStoreCrashedAtEveryBatchStillQueuesAndApplies: a
// store replaced by an older copy, a line this build cannot apply and a known one
// after it in the journal, a crash before each batch in turn. The restore line is
// written once, the head is never past the unknown line unqueued, and the next
// boots finish the job: the disable and the revocation apply, every other key is
// asked to confirm, and the line stays queued and answered.
func TestUnappliedQueue_AReplacedStoreCrashedAtEveryBatchStillQueuesAndApplies(t *testing.T) {
	for crash := 1; crash <= 6; crash++ { // 5 batches: 6 never fires
		t.Run(fmt.Sprintf("crash before batch %d", crash), func(t *testing.T) {
			fx := newRxFx(t, nil)
			fx.boot(nil, nil)
			u := fx.user("barbara")
			fx.key("key-a", u, auth.KeyActive)
			fx.key("key-b", u, auth.KeyActive)
			archive := fx.snapshot("archive")
			fx.revokeKey("key-a")                                                                           // 1, committed
			fx.journalOnly(uqFrozen(u))                                                                     // 2
			fx.journalOnly(journal.Revocation{Kind: journal.UserDisabled, Subject: rxUserRef(u), Epoch: 1}) // 3
			if err := journal.CommitHead(fx.ctx, fx.bks(), fx.comp.Tail()); err != nil {
				t.Fatal(err)
			}
			fx.restart() // anchor 3
			fx.swapIn(archive)

			_, first, err := fx.tryBoot(nil, uqWith(uqTail(1), crashAtBatch(crash)), nil)
			crashed := err != nil
			t.Logf("first boot: err %v, head %d, restore lines %d, queue %v", err, fx.head().Seq, len(fx.restoreLines()), uqSeqs(fx.uqQueue()))
			fx.uqNeverPassed(2)
			if !crashed && !first.Ran {
				t.Error("the boot the crash never reached did not run the replaced store's hooks")
			}

			fx.restart()
			res, hres, err := fx.tryBoot(nil, nil, nil)
			if err != nil {
				t.Fatalf("second boot: %v", err)
			}
			if crashed && (res.Class != classReplaced || !hres.Ran) {
				t.Errorf("second boot: class %q (%s), hooks ran %v, want the replaced store's hooks to run after the crash", res.Class, res.Why, hres.Ran)
			}
			if fx.userRow(u).Status != auth.StatusDisabled || fx.keyStatus("key-a") != auth.KeyRevoked || fx.keyStatus("key-b") == auth.KeyActive {
				t.Errorf("user %q, key-a %q, key-b %q", fx.userRow(u).Status, fx.keyStatus("key-a"), fx.keyStatus("key-b"))
			}
			if n := len(fx.restoreLines()); n != 1 {
				t.Errorf("%d restore lines, want exactly one: a retried boot never duplicates it", n)
			}
			if q := fx.uqQueue(); len(q) != 1 || q[0].Seq != 2 || !q[0].Closed {
				t.Errorf("queue = %+v, want seq 2, answered", q)
			}
			fx.restart()
			if res, hres, _ = fx.tryBoot(nil, nil, nil); res.FailedClosed || hres.Ran {
				t.Errorf("third boot: failed closed %v, hooks ran %v, want a quiet boot of the same store", res.FailedClosed, hres.Ran)
			}
		})
	}
}

// crashAtQueueBatch makes the n-th batch the queueing step opens (1-based) fail
// the way a process death would end it, as crashAtBatch does for the re-apply's.
func crashAtQueueBatch(n int) func(*reapplyInput) {
	return func(in *reapplyInput) {
		inner, calls := in.KS.NewBatch, 0
		in.QueueBatch = func() *storage.Batch {
			calls++
			b := inner()
			if calls >= n {
				_ = b.Close()
			}
			return b
		}
	}
}

// TestUnappliedQueue_ACrashWhileQueueingMovesNothing: three lines this build
// cannot apply, one to a chunk, among known ones, and a crash before each queue
// batch in turn. Nothing has been applied and the head has not moved (the queue
// is the step before the first re-apply batch), what was queued stays, and the
// next boot queues the rest, applies the tail and answers once.
func TestUnappliedQueue_ACrashWhileQueueingMovesNothing(t *testing.T) {
	for crash := 1; crash <= 4; crash++ { // 3 batches: 4 never fires
		t.Run(fmt.Sprintf("crash before queue batch %d", crash), func(t *testing.T) {
			fx := newRxFx(t, nil)
			fx.boot(nil, nil)
			owner := fx.user("barbara")
			fx.key("key-one", owner, auth.KeyActive)
			fx.journalOnly(journal.Revocation{Kind: journal.APIKeyRevoked, Subject: rxKeyRef("key-one")}) // 1
			fx.journalOnly(uqFrozen(owner))                                                               // 2
			fx.journalOnly(journal.Revocation{Kind: "user.thawed", Subject: rxUserRef(owner)})            // 3
			fx.journalOnly(journal.Revocation{Kind: "user.melted", Subject: rxUserRef(owner)})            // 4
			fx.restart()

			closes := 0
			first, _, err := fx.tryBoot(nil, uqWith(uqTail(1), crashAtQueueBatch(crash)), nil)
			crashed := err != nil
			if first.FailedClosed {
				closes++
			}
			if crashed && (fx.head().Seq != 0 || fx.keyStatus("key-one") != auth.KeyActive) {
				t.Errorf("head %d, key-one %q after a crash while queueing: nothing is applied and the head has not moved before the queue is whole", fx.head().Seq, fx.keyStatus("key-one"))
			}
			if want := min(crash-1, 3); crashed && len(fx.uqQueue()) != want {
				t.Errorf("queue = %v after crashing before batch %d, want the %d earlier batches' entries", uqSeqs(fx.uqQueue()), crash, want)
			}
			fx.uqNeverPassed(2, 3, 4)

			fx.restart()
			res, _, err := fx.tryBoot(nil, uqTail(1), nil)
			if err != nil {
				t.Fatalf("second boot: %v", err)
			}
			if res.FailedClosed {
				closes++
			}
			if q := fx.uqQueue(); !slices.Equal(uqSeqs(q), []uint64{2, 3, 4}) || !q[0].Closed || !q[1].Closed || !q[2].Closed {
				t.Errorf("queue = %+v, want the three lines, answered", q)
			}
			if fx.head().Seq != 4 || fx.keyStatus("key-one") != auth.KeyRevoked || closes != 1 {
				t.Errorf("head %d, key-one %q, %d fail-closed passes, want 4, revoked and 1", fx.head().Seq, fx.keyStatus("key-one"), closes)
			}
		})
	}
}
