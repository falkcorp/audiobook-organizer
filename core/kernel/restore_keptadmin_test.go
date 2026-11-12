// file: core/kernel/restore_keptadmin_test.go
// version: 1.2.0
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

// cannotApplyRemoval is the Known of a build that cannot apply the kind of the
// scene's removal line (role.unassigned for one admin, role.narrowed for two).
func (s *kaScene) cannotApplyRemoval() func(*reapplyInput) {
	return func(in *reapplyInput) {
		in.Known = func(k journal.Kind) bool { return k != s.removal.Kind && journal.KnownKind(k) }
	}
}

// crashAtRestriction ends the boot at the restriction of the kept last admins:
// before it runs, or right after it committed, as a process death would. Nothing
// the boot would have done later happens. n is the 1-based call; the re-apply
// makes one call for every admin it kept, so only n = 1 is ever reached (a larger
// n lets the boot finish, which a test that expects a crash then reports).
func crashAtRestriction(n int, after bool) func(*reapplyInput) {
	return func(in *reapplyInput) {
		inner, calls := in.Apply.RestrictLastAdmins, 0
		in.Apply.RestrictLastAdmins = func(ctx context.Context, ids []string) error {
			calls++
			if calls < n {
				return inner(ctx, ids)
			}
			if !after {
				return errors.New("crash before the restriction")
			}
			if err := inner(ctx, ids); err != nil {
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

// kaScene is a trustee role holding core:admin, its holders each with a key (bruce
// alone, or bruce and alfred), and selina, an ordinary user. The removal line is
// the one that would leave no active core:admin holder: bruce unassigned from the
// role when he is the only holder, the role narrowed (core:admin taken off it)
// when there are two, which rbac keeps by reporting both holders at once.
type kaScene struct {
	fx              *rxFx
	trustee         rbac.RoleName
	bruce, selina   string
	alfred          string // empty unless the scene has two admins
	admins          []string
	keys            map[string]string // admin ID -> the ID of its key
	removal, freeze journal.Revocation
	disable         journal.Revocation
}

// kaKinds are the scenes every crash test runs over.
var kaKinds = []struct {
	name string
	pair bool
}{{"one admin", false}, {"two admins", true}}

func newKaScene(t *testing.T) *kaScene { return newKaSceneOf(t, false) }

func newKaSceneOf(t *testing.T, pair bool) *kaScene {
	t.Helper()
	fx := newRxFx(t, nil)
	fx.boot(nil, nil)
	s := &kaScene{fx: fx, trustee: fx.uqTrustee(), keys: map[string]string{}}
	s.bruce = fx.user("bruce-wayne", s.trustee)
	fx.key("key-batsignal", s.bruce, auth.KeyActive)
	s.admins, s.keys[s.bruce] = []string{s.bruce}, "key-batsignal"
	s.removal = journal.Revocation{Kind: journal.RoleUnassigned, Subject: rxUserRef(s.bruce), Object: rxRoleRef(string(s.trustee))}
	if pair {
		s.alfred = fx.user("alfred-pennyworth", s.trustee)
		fx.key("key-teapot", s.alfred, auth.KeyActive)
		s.admins, s.keys[s.alfred] = append(s.admins, s.alfred), "key-teapot"
		s.removal = journal.Revocation{Kind: journal.RoleNarrowed, Subject: rxRoleRef(string(s.trustee)), Detail: []string{"core:admin"}}
	}
	s.selina = fx.user("selina-kyle")
	s.freeze = uqFrozen(s.selina)
	s.disable = journal.Revocation{Kind: journal.UserDisabled, Subject: rxUserRef(s.selina), Epoch: 1}
	return s
}

func (s *kaScene) bruceRestricted() bool { return s.fx.userRow(s.bruce).SignInRestricted }

// allRestricted is true when every admin of the scene is restricted.
func (s *kaScene) allRestricted() bool {
	for _, id := range s.admins {
		if !s.fx.userRow(id).SignInRestricted {
			return false
		}
	}
	return true
}

// gone is true when the store has no user id.
func (s *kaScene) gone(id string) bool {
	_, err := s.fx.auth.GetUser(s.fx.ctx, auth.UserID(id))
	return errors.Is(err, auth.ErrNotFound)
}

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
	if passed && !s.allRestricted() {
		t.Errorf("the line at seq %d is past (head %d, queue %v) but an admin it kept is not restricted: it would never be", removalSeq, fx.head().Seq, uqSeqs(fx.uqQueue()))
	}
}

// settled fails unless the boot after the crash left the store whole.
func (s *kaScene) settled(t *testing.T, queue []uint64, top uint64) {
	t.Helper()
	fx := s.fx
	if !s.selinaDisabled() {
		t.Errorf("selina is %q after the next boot, want disabled", fx.userRow(s.selina).Status)
	}
	for _, id := range s.admins {
		if u := fx.userRow(id); !slices.Equal(u.Roles, []rbac.RoleName{s.trustee}) || !u.SignInRestricted {
			t.Errorf("%s: roles %v, sign-in restricted %v, want the role kept and the admin restricted", id, u.Roles, u.SignInRestricted)
		}
		if got := fx.keyStatus(s.keys[id]); got != auth.KeyNeedsConfirmation {
			t.Errorf("%s = %q, want needs_confirmation (a restricted last admin's keys)", s.keys[id], got)
		}
	}
	if got := uqSeqs(fx.uqQueue()); !slices.Equal(got, queue) {
		t.Errorf("queue = %v, want %v", got, queue)
	}
	if got := fx.head().Seq; got < top {
		t.Errorf("head = %d, want at least %d", got, top)
	}
}

// TestKeptAdminRestriction_AQueuedRemovalDrainedByALaterBuildWaitsForTheWholeTail:
// a build that cannot apply the removal's kind queues a removal that would leave
// no core:admin; the next build drains it and keeps the admin (or, for two
// holders, both). The journal has two lines after the head by then, one only a
// newer build knows and one every build does (a user disabled). The drain's
// restriction used to be the first append of the boot, and with it the head passed
// both before the first was queued and the second applied: a crash there lost a
// revocation.
func TestKeptAdminRestriction_AQueuedRemovalDrainedByALaterBuildWaitsForTheWholeTail(t *testing.T) {
	for _, kind := range kaKinds {
		for _, pt := range kaCrashPoints() {
			t.Run(kind.name+"/"+pt.name, func(t *testing.T) {
				s := newKaSceneOf(t, kind.pair)
				fx := s.fx
				removal := fx.journalOnly(s.removal)
				fx.restart()
				if res, _ := fx.boot(nil, s.cannotApplyRemoval()); res.Queued != 1 || !slices.Equal(uqSeqs(fx.uqQueue()), []uint64{removal}) {
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
}

// TestKeptAdminRestriction_ATailChunkThatKeepsTheAdminWaitsForTheChunksAfterIt:
// the same hazard inside one tail: the removal is in the first chunk, a user
// disabled in the second and a line only a newer build knows in the third. The
// chunk that keeps the admin cannot let the head pass it, and the restriction
// cannot run, before the later chunks are applied and queued. With two holders
// one line keeps both, and both must be restricted before the head can be left
// above the line: restricting them one commit at a time left the second without a
// restriction after a crash between the two, with the head already at the top.
func TestKeptAdminRestriction_ATailChunkThatKeepsTheAdminWaitsForTheChunksAfterIt(t *testing.T) {
	for _, kind := range kaKinds {
		for _, pt := range kaCrashPoints() {
			t.Run(kind.name+"/"+pt.name, func(t *testing.T) {
				s := newKaSceneOf(t, kind.pair)
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
}

// TestKeptAdminRestriction_EveryKeptAdminIsRestrictedInOneCall: one line that
// leaves no admin keeps every holder of the role, and the boot restricts all of
// them in ONE call (one batch), in the order rbac reports them, never a loop of
// calls with a commit each. The call count is what a loop would change; what a
// crash between two commits would leave is pinned by the crash tests above.
func TestKeptAdminRestriction_EveryKeptAdminIsRestrictedInOneCall(t *testing.T) {
	s := newKaSceneOf(t, true)
	fx := s.fx
	fx.journalOnly(s.removal) // 1
	fx.restart()

	var calls [][]string
	spy := func(in *reapplyInput) {
		inner := in.Apply.RestrictLastAdmins
		in.Apply.RestrictLastAdmins = func(ctx context.Context, ids []string) error {
			calls = append(calls, slices.Clone(ids))
			return inner(ctx, ids)
		}
	}
	if _, _, err := fx.tryBoot(nil, uqWith(uqTail(1), spy), nil); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{s.alfred, s.bruce}}; !slices.EqualFunc(calls, want, slices.Equal[[]string]) {
		t.Errorf("restriction calls = %v, want one call with %v", calls, want)
	}
	if !s.allRestricted() {
		t.Errorf("alfred restricted %v, bruce restricted %v, want both", fx.userRow(s.alfred).SignInRestricted, s.bruceRestricted())
	}
	for _, id := range s.admins {
		if got := fx.keyStatus(s.keys[id]); got != auth.KeyNeedsConfirmation {
			t.Errorf("%s = %q, want needs_confirmation", s.keys[id], got)
		}
	}
}

// TestKeptAdminRestriction_AKeptAdminDeletedByALaterLineNeedsNoRestriction: the
// first chunk keeps the admin (the line would leave no active admin) and a later
// chunk deletes that user. Deleting is stricter than restricting, so the boot
// finishes: it used to fail on the restriction's ErrNotFound after the whole tail
// had been applied, and pass only the next time, when the line no longer kept
// anything. A second kept admin that was not deleted is still restricted.
func TestKeptAdminRestriction_AKeptAdminDeletedByALaterLineNeedsNoRestriction(t *testing.T) {
	t.Run("the only admin", func(t *testing.T) {
		s := newKaSceneOf(t, false)
		fx := s.fx
		fx.journalOnly(s.removal)                                                                         // 1: keeps bruce
		top := fx.journalOnly(journal.Revocation{Kind: journal.UserDeleted, Subject: rxUserRef(s.bruce)}) // 2
		fx.restart()

		if _, _, err := fx.tryBoot(nil, uqTail(1), nil); err != nil {
			t.Fatalf("the boot failed on a kept admin a later line deleted: %v", err)
		}
		if !s.gone(s.bruce) {
			t.Error("bruce is still in the store, want the deletion applied")
		}
		if got := fx.head().Seq; got < top {
			t.Errorf("head = %d, want at least %d: the whole tail is handled", got, top)
		}
		fx.restart()
		if _, _, err := fx.tryBoot(nil, uqTail(1), nil); err != nil {
			t.Fatalf("the next boot: %v", err)
		}
	})
	t.Run("one of two admins", func(t *testing.T) {
		s := newKaSceneOf(t, true)
		fx := s.fx
		fx.journalOnly(s.removal)                                                                          // 1: keeps both
		top := fx.journalOnly(journal.Revocation{Kind: journal.UserDeleted, Subject: rxUserRef(s.alfred)}) // 2
		fx.restart()

		if _, _, err := fx.tryBoot(nil, uqTail(1), nil); err != nil {
			t.Fatalf("the boot failed on a kept admin a later line deleted: %v", err)
		}
		if !s.gone(s.alfred) {
			t.Error("alfred is still in the store, want the deletion applied")
		}
		if !s.bruceRestricted() {
			t.Error("bruce is not restricted: the admin that was not deleted must still be")
		}
		if got := fx.keyStatus(s.keys[s.bruce]); got != auth.KeyNeedsConfirmation {
			t.Errorf("%s = %q, want needs_confirmation", s.keys[s.bruce], got)
		}
		if got := fx.head().Seq; got < top {
			t.Errorf("head = %d, want at least %d", got, top)
		}
	})
}

// TestKeptAdminRestriction_AnAdminKeptTwiceIsRestrictedOnce: the removal is in the
// queue and again in the tail, so the drain keeps the admin and so does a tail
// chunk. The restriction is journaled once, after both.
func TestKeptAdminRestriction_AnAdminKeptTwiceIsRestrictedOnce(t *testing.T) {
	s := newKaScene(t)
	fx := s.fx
	fx.journalOnly(s.removal) // 1
	fx.restart()
	fx.boot(nil, s.cannotApplyRemoval()) // queues 1
	fx.journalOnly(s.removal)            // 2, the journal is still bound
	fx.restart()

	var calls [][]string
	count := func(in *reapplyInput) {
		inner := in.Apply.RestrictLastAdmins
		in.Apply.RestrictLastAdmins = func(ctx context.Context, ids []string) error {
			calls = append(calls, slices.Clone(ids))
			return inner(ctx, ids)
		}
	}
	res, _, err := fx.tryBoot(nil, uqWith(uqTail(1), count), nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{s.bruce}}; !slices.EqualFunc(calls, want, slices.Equal[[]string]) || res.Drained != 1 {
		t.Errorf("restricted %v, drained %d, want one call for %v and 1", calls, res.Drained, want)
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
