// file: core/kernel/restore_labels_test.go
// version: 0.1.0
// guid: 45afe359-828c-4b92-a5ea-aeb944994737
// last-edited: 2026-11-20

package kernel

import (
	"context"
	"os"
	"slices"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/internal/bootseam"
)

// labelLog is a bootseam.Labeler that records the regions a boot enters, in
// order (the fixture's tryBoot puts one in every boot's context).
type labelLog struct {
	mu      sync.Mutex
	entered []string
}

func (l *labelLog) Enter(label string) func() {
	l.mu.Lock()
	l.entered = append(l.entered, label)
	l.mu.Unlock()
	return func() {}
}

// take returns the labels entered since the last take and forgets them.
func (l *labelLog) take() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.entered
	l.entered = nil
	return out
}

// TestReapplyRegions_EveryCommitSiteCarriesItsLabel: each store commit of P4d
// steps 2 and 3 that F4-08 A5 labels runs inside its bootseam region, so the
// crash matrix can kill and fail it by name once F4-08 wires the boot. Each
// scenario drives the boot to the sites it reaches; together they reach every
// label of ReapplyLabels and HookRunLabels.
func TestReapplyRegions_EveryCommitSiteCarriesItsLabel(t *testing.T) {
	reached := map[string]bool{}
	expect := func(t *testing.T, got []string, want ...string) {
		t.Helper()
		for _, l := range got {
			reached[l] = true
		}
		for _, w := range want {
			if !slices.Contains(got, w) {
				t.Errorf("the boot entered %v, want %s among them", got, w)
			}
		}
	}

	t.Run("a tail to re-apply, then served_boot", func(t *testing.T) {
		fx := newRxFx(t, nil)
		fx.boot(nil, nil)
		fx.key("key-oracle", fx.user("barbara"), auth.KeyActive)
		fx.journalOnly(journal.Revocation{Kind: journal.APIKeyRevoked, Subject: rxKeyRef("key-oracle")})
		fx.restart()
		fx.labels.take()
		fx.boot(nil, nil)
		expect(t, fx.labels.take(), labelReapplyTailAuth, labelReapplyTailRBAC, labelReapplyServedBoot)
	})
	t.Run("a line this build cannot apply is queued, closed, then drained", func(t *testing.T) {
		fx := newRxFx(t, nil)
		fx.boot(nil, nil)
		fx.journalOnly(journal.Revocation{Kind: journal.UserDisabled, Subject: rxUserRef(fx.user("selina")), Epoch: 1})
		fx.restart()
		fx.labels.take()
		fx.boot(nil, uqOldBuild)
		expect(t, fx.labels.take(), labelReapplyQueue, labelReapplyUnappliedClosed)
		fx.restart()
		fx.boot(nil, nil)
		expect(t, fx.labels.take(), labelReapplyDrainAuth, labelReapplyDrainRBAC)
	})
	t.Run("a tail chunk keeps the last admin", func(t *testing.T) {
		s := newKaSceneOf(t, false)
		s.fx.journalOnly(s.removal)
		s.fx.restart()
		s.fx.labels.take()
		s.fx.boot(nil, nil)
		expect(t, s.fx.labels.take(), labelReapplyRestrictKept, labelReapplyWithheldHead)
	})
	t.Run("a drained line keeps the last admin", func(t *testing.T) {
		s := newKaSceneOf(t, false)
		s.fx.journalOnly(s.removal)
		s.fx.restart()
		s.fx.boot(nil, s.cannotApplyRemoval())
		s.fx.restart()
		s.fx.labels.take()
		s.fx.boot(nil, nil)
		expect(t, s.fx.labels.take(), labelReapplyDrainRBAC, labelReapplyRestrictKept, labelReapplyDequeue)
	})
	t.Run("another install's journal: the gap line's head with no tail", func(t *testing.T) {
		g := newBatcave(t)
		g.restart()
		other := newRxFx(t, g.keys)
		other.boot(nil, nil)
		other.restart()
		raw, err := os.ReadFile(other.journalPath())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(g.journalPath(), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		g.labels.take()
		if res, _ := g.boot(nil, nil); !res.Reset {
			t.Fatalf("reset %v: the scenario needs the set-aside", res.Reset)
		}
		expect(t, g.labels.take(), labelReapplyPendingHead)
	})
	t.Run("a replaced store whose restore line cannot be written runs the hooks under a run ID", func(t *testing.T) {
		fx := replacedByCopy(t, "anchor")
		brokenKeyring(fx)
		fx.labels.take()
		if res, hres := fx.boot(nil, nil); res.RestoreLine != 0 || !hres.Ran {
			t.Fatalf("restore line %d, hooks ran %v: the scenario needs a run with no line", res.RestoreLine, hres.Ran)
		}
		expect(t, fx.labels.take(), labelHooksRunEnsure, labelHooksRunClear, labelHooksHeldHead)
	})
	t.Run("a restore boot's restore line", func(t *testing.T) {
		s := newRestoreScene(t, nil)
		s.labels.take()
		s.boot(archiveBoot("restore-1", "arch-1", s.previous, &rxSteps{}), nil)
		expect(t, s.labels.take(), labelReapplyRestoreLine, labelReapplyServedBoot, labelHooksMarkHooked, labelHooksReport)
	})

	for _, l := range append(ReapplyLabels(), HookRunLabels()...) {
		if !reached[l] {
			t.Errorf("no scenario reached %s: a label no test enters is a region nothing proves is there", l)
		}
	}
}

// TestReapplyRegions_NoOpWithoutSeams: production boots carry no Seams, so a
// region is the no-op exit (outside test files, bootseam.With is called only by
// core/coretest, archtest.BootseamHookCallers), and the re-apply runs exactly as
// before.
func TestReapplyRegions_NoOpWithoutSeams(t *testing.T) {
	if bootseam.From(context.Background()) != nil {
		t.Fatal("a plain context carries Seams")
	}
	exit := bootseam.Region(context.Background(), labelReapplyTailAuth)
	exit()
	exit()
	fx := newRxFx(t, nil)
	fx.boot(nil, nil)
	fx.key("key-oracle", fx.user("barbara"), auth.KeyActive)
	fx.journalOnly(journal.Revocation{Kind: journal.APIKeyRevoked, Subject: rxKeyRef("key-oracle")})
	fx.restart()
	in := fx.input(nil)
	res, err := reapplyRevocations(context.Background(), in)
	if err != nil || res.Reapplied != 1 || fx.keyStatus("key-oracle") != auth.KeyRevoked {
		t.Fatalf("a boot with no Seams: err %v, re-applied %d, key %q", err, res.Reapplied, fx.keyStatus("key-oracle"))
	}
}
