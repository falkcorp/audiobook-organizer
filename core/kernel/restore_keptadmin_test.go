// file: core/kernel/restore_keptadmin_test.go
// version: 1.1.0
// guid: 409dcde0-998e-4c55-8b60-bda571d2d6ab
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
)

// A kept last admin's restriction is a journal append, and an append stages its
// own seq as the head: seq above every line of the journal. So the restriction
// may only run once every line after the store's head has been applied, queued or
// stepped over, or a crash right after it leaves those lines below the head, never
// to be re-applied; and the line that kept the admin may only pass the head after
// the restriction ran, or a crash between the two leaves an admin the journal
// proved was demoted with the role and no restriction, with nothing to replay it.
// These tests crash the re-apply at every step that can end a boot (each batch,
// the queue's batches, and either side of the restriction) and read what the store
// holds before the next boot does anything.

// kaNoRoleRemoval is the Known of a build that cannot apply role.unassigned.
func kaNoRoleRemoval(in *reapplyInput) {
	in.Known = func(k journal.Kind) bool { return k != journal.RoleUnassigned && journal.KnownKind(k) }
}

// crashAtRestriction ends the boot at the n-th restriction of a kept last admin
// (1-based): before it runs, or right after it committed, as a process death
// would. Nothing the boot would have done later happens.
func crashAtRestriction(n int, after bool) func(*reapplyInput) {
	return func(in *reapplyInput) {
		inner, calls := in.Apply.RestrictLastAdmin, 0
		in.Apply.RestrictLastAdmin = func(ctx context.Context, id string) error {
			calls++
			if calls < n {
				return inner(ctx, id)
			}
			if !after {
				return errors.New("crash before the restriction")
			}
			if err := inner(ctx, id); err != nil {
				return err
			}
			return errors.New("crash after the restriction")
		}
	}
}

// kaCrash is one place a boot is ended. fires says the boot must actually fail
// there in the scenes that reach it (the others may finish before the n-th batch).
type kaCrash struct {
	name      string
	mut       func(*reapplyInput)
	fires     bool
	drainOnly bool // the step exists only in the scene that drains a queued line
}

// kaCrashPoints are the places a boot is ended: none, before each batch the
// re-apply opens, before each of the unapplied queue's own batches, either side of
// the first restriction.
func kaCrashPoints() []kaCrash {
	pts := []kaCrash{{name: "no crash", mut: uqTail(1)}}
	for n := 1; n <= 8; n++ {
		pts = append(pts, kaCrash{name: fmt.Sprintf("before batch %d", n), mut: uqWith(uqTail(1), crashAtBatch(n)), fires: n == 1})
	}
	return append(pts,
		kaCrash{name: "before queue batch 1", mut: uqWith(uqTail(1), crashAtQueueBatch(1)), fires: true},
		kaCrash{name: "before queue batch 2", mut: uqWith(uqTail(1), crashAtQueueBatch(2)), fires: true, drainOnly: true},
		kaCrash{name: "before the restriction", mut: uqWith(uqTail(1), crashAtRestriction(1, false)), fires: true},
		kaCrash{name: "after the restriction", mut: uqWith(uqTail(1), crashAtRestriction(1, true)), fires: true},
	)
}

// kaScene is a trustee role holding core:admin, bruce its only holder with a key,
// and selina, an ordinary user.
type kaScene struct {
	fx              *rxFx
	trustee         rbac.RoleName
	bruce, selina   string
	removal, freeze journal.Revocation
	disable         journal.Revocation
}

func newKaScene(t *testing.T) *kaScene {
	t.Helper()
	fx := newRxFx(t, nil)
	fx.boot(nil, nil)
	s := &kaScene{fx: fx, trustee: fx.uqTrustee()}
	s.bruce = fx.user("bruce-wayne", s.trustee)
	fx.key("key-batsignal", s.bruce, auth.KeyActive)
	s.selina = fx.user("selina-kyle")
	s.removal = journal.Revocation{Kind: journal.RoleUnassigned, Subject: rxUserRef(s.bruce), Object: rxRoleRef(string(s.trustee))}
	s.freeze = uqFrozen(s.selina)
	s.disable = journal.Revocation{Kind: journal.UserDisabled, Subject: rxUserRef(s.selina), Epoch: 1}
	return s
}

func (s *kaScene) bruceRestricted() bool { return s.fx.userRow(s.bruce).SignInRestricted }

func (s *kaScene) selinaDisabled() bool {
	return s.fx.userRow(s.selina).Status == auth.StatusDisabled
}

// afterCrash fails on what a store must never be left holding, whichever step
// the boot ended at: a head past a line that is neither applied nor queued, and a
// line that kept the last admin passed before the admin was restricted.
func (s *kaScene) afterCrash(t *testing.T, unknownSeq, disableSeq, removalSeq uint64, removalQueued bool) {
	t.Helper()
	fx := s.fx
	fx.uqNeverPassed(unknownSeq)
	if fx.head().Seq >= disableSeq && !s.selinaDisabled() {
		t.Errorf("the head is at %d, past the line at seq %d, but selina is %q: that revocation would never be applied", fx.head().Seq, disableSeq, fx.userRow(s.selina).Status)
	}
	passed := fx.head().Seq >= removalSeq
	if removalQueued {
		passed = !slices.Contains(uqSeqs(fx.uqQueue()), removalSeq)
	}
	if passed && !s.bruceRestricted() {
		t.Errorf("the line at seq %d is past (head %d, queue %v) but bruce is not restricted: the kept admin would never be", removalSeq, fx.head().Seq, uqSeqs(fx.uqQueue()))
	}
}

// settled fails unless the boot after the crash left the store whole.
func (s *kaScene) settled(t *testing.T, queue []uint64, top uint64) {
	t.Helper()
	fx := s.fx
	if !s.selinaDisabled() {
		t.Errorf("selina is %q after the next boot, want disabled", fx.userRow(s.selina).Status)
	}
	if u := fx.userRow(s.bruce); !slices.Equal(u.Roles, []rbac.RoleName{s.trustee}) || !u.SignInRestricted {
		t.Errorf("bruce: roles %v, sign-in restricted %v, want the role kept and the admin restricted", u.Roles, u.SignInRestricted)
	}
	if got := fx.keyStatus("key-batsignal"); got != auth.KeyNeedsConfirmation {
		t.Errorf("key-batsignal = %q, want needs_confirmation (a restricted last admin's keys)", got)
	}
	if got := uqSeqs(fx.uqQueue()); !slices.Equal(got, queue) {
		t.Errorf("queue = %v, want %v", got, queue)
	}
	if got := fx.head().Seq; got < top {
		t.Errorf("head = %d, want at least %d", got, top)
	}
}

// TestKeptAdminRestriction_AQueuedRemovalDrainedByALaterBuildWaitsForTheWholeTail:
// a build that cannot apply role.unassigned queues a removal that would leave no
// core:admin; the next build drains it and keeps the admin. The journal has two
// lines after the head by then, one only a newer build knows and one every build
// does (a user disabled). The drain's restriction used to be the first append of
// the boot, and with it the head passed both before the first was queued and the
// second applied: a crash there lost a revocation.
func TestKeptAdminRestriction_AQueuedRemovalDrainedByALaterBuildWaitsForTheWholeTail(t *testing.T) {
	for _, pt := range kaCrashPoints() {
		t.Run(pt.name, func(t *testing.T) {
			s := newKaScene(t)
			fx := s.fx
			removal := fx.journalOnly(s.removal)
			fx.restart()
			if res, _ := fx.boot(nil, kaNoRoleRemoval); res.Queued != 1 || !slices.Equal(uqSeqs(fx.uqQueue()), []uint64{removal}) {
				t.Fatalf("queued %d, queue %v, want the removal at %d queued", res.Queued, uqSeqs(fx.uqQueue()), removal)
			}
			// The journal is still bound: the old build's boot may have journaled
			// lines of its own, so the seqs are read, not assumed.
			frozen := fx.journalOnly(s.freeze)
			disabled := fx.journalOnly(s.disable)
			fx.restart()

			_, _, err := fx.tryBoot(nil, pt.mut, nil)
			t.Logf("boot: %v; head %d, queue %v", err, fx.head().Seq, uqSeqs(fx.uqQueue()))
			if pt.fires && err == nil {
				t.Fatalf("the boot finished: %q is a step it takes, so the crash test would prove nothing", pt.name)
			}
			s.afterCrash(t, frozen, disabled, removal, true)

			fx.restart()
			if _, _, err := fx.tryBoot(nil, uqTail(1), nil); err != nil {
				t.Fatalf("the next boot: %v", err)
			}
			s.settled(t, []uint64{frozen}, disabled)
		})
	}
}

// TestKeptAdminRestriction_ATailChunkThatKeepsTheAdminWaitsForTheChunksAfterIt:
// the same hazard inside one tail: the removal is in the first chunk, a user
// disabled in the second and a line only a newer build knows in the third. The
// chunk that keeps the admin cannot let the head pass it, and the restriction
// cannot run, before the later chunks are applied and queued.
func TestKeptAdminRestriction_ATailChunkThatKeepsTheAdminWaitsForTheChunksAfterIt(t *testing.T) {
	for _, pt := range kaCrashPoints() {
		t.Run(pt.name, func(t *testing.T) {
			s := newKaScene(t)
			fx := s.fx
			fx.journalOnly(s.removal) // 1
			fx.journalOnly(s.disable) // 2
			fx.journalOnly(s.freeze)  // 3
			fx.restart()

			if pt.drainOnly {
				t.Skip("the scene has no drained line")
			}
			_, _, err := fx.tryBoot(nil, pt.mut, nil)
			t.Logf("boot: %v; head %d, queue %v", err, fx.head().Seq, uqSeqs(fx.uqQueue()))
			if pt.fires && err == nil {
				t.Fatalf("the boot finished: %q is a step it takes, so the crash test would prove nothing", pt.name)
			}
			s.afterCrash(t, 3, 2, 1, false)

			fx.restart()
			if _, _, err := fx.tryBoot(nil, uqTail(1), nil); err != nil {
				t.Fatalf("the next boot: %v", err)
			}
			s.settled(t, []uint64{3}, 3)
		})
	}
}

// TestKeptAdminRestriction_AnAdminKeptTwiceIsRestrictedOnce: the removal is in the
// queue and again in the tail, so the drain keeps the admin and so does a tail
// chunk. The restriction is journaled once, after both.
func TestKeptAdminRestriction_AnAdminKeptTwiceIsRestrictedOnce(t *testing.T) {
	s := newKaScene(t)
	fx := s.fx
	fx.journalOnly(s.removal) // 1
	fx.restart()
	fx.boot(nil, kaNoRoleRemoval) // queues 1
	fx.journalOnly(s.removal)     // 2, the journal is still bound
	fx.restart()

	calls := 0
	count := func(in *reapplyInput) {
		inner := in.Apply.RestrictLastAdmin
		in.Apply.RestrictLastAdmin = func(ctx context.Context, id string) error {
			calls++
			return inner(ctx, id)
		}
	}
	res, _, err := fx.tryBoot(nil, uqWith(uqTail(1), count), nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || res.Drained != 1 {
		t.Errorf("restricted %d times, drained %d, want once and 1", calls, res.Drained)
	}
	if !s.bruceRestricted() || len(fx.uqQueue()) != 0 || fx.head().Seq < 2 {
		t.Errorf("restricted %v, queue %v, head %d, want true, empty and at least 2", s.bruceRestricted(), uqSeqs(fx.uqQueue()), fx.head().Seq)
	}
}

// TestKeptAdminRestriction_AJournalThatTakesNoLineStillGetsItsHead: the
// restriction's own append is what usually carries the head to the top of the
// journal. When the journal cannot take it (the component is held, a gap is
// recorded instead) the restriction still commits and the head withheld from the
// chunks is committed by restrictKept at the last line's seq, not left at 0.
func TestKeptAdminRestriction_AJournalThatTakesNoLineStillGetsItsHead(t *testing.T) {
	s := newKaScene(t)
	fx := s.fx
	fx.journalOnly(s.removal) // 1
	disabled := fx.journalOnly(s.disable)
	fx.restart()
	fx.comp.Hold(errors.New("the journal takes no line in this test"))

	if _, _, err := fx.tryBoot(nil, uqTail(1), nil); err != nil {
		t.Fatal(err)
	}
	if !s.bruceRestricted() || !s.selinaDisabled() {
		t.Errorf("bruce restricted %v, selina disabled %v, want both: the restriction commits without its journal line", s.bruceRestricted(), s.selinaDisabled())
	}
	if got := fx.head().Seq; got != disabled {
		t.Errorf("head = %d, want %d, the last line: the head withheld from the chunks is committed after the restriction", got, disabled)
	}
}
