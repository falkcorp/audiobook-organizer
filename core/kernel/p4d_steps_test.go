// file: core/kernel/p4d_steps_test.go
// version: 0.1.0
// guid: 8e1d5b02-6a4f-49c7-b3e8-0f27c9a1d654
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/backup/journal"
)

// P4d steps 2-4 as one run (F4-08 A10): the re-apply, the restore hooks and
// auth's boot writes, in that order, any error refusing to serve.

// p4dRun is a run's inputs over fx with the recording hook builder and boot
// writes; events receives "hooks" when the hook list is built and
// "bootwrites" when step 4 runs.
func p4dRun(fx *rxFx, rb *restoreBoot, events *[]string, writes func(context.Context) error) p4dStepsInput {
	in := fx.input(nil)
	in.Restore = rb
	return p4dStepsInput{
		Reapply:      in,
		OpenPrevious: fx.openPrevious,
		Hooks: func(env hookEnv) ([]journal.RestoreHook, error) {
			*events = append(*events, "hooks")
			return restoreHookList(fx.stores())(env)
		},
		BootWrites: func(ctx context.Context) error {
			*events = append(*events, "bootwrites")
			if writes != nil {
				return writes(ctx)
			}
			return nil
		},
	}
}

// TestRunP4dSteps_RestoreBootRunsTheStepsInOrder: on a restore boot the hooks
// get step 2's result and run, and the boot writes run after the hooks have
// completed (their restore_hooked record exists), never before.
func TestRunP4dSteps_RestoreBootRunsTheStepsInOrder(t *testing.T) {
	s := newRestoreScene(t, nil)
	var events []string
	hookedAtWrites := false
	in := p4dRun(s.rxFx, archiveBoot("restore-1", "arch-1", s.previous, &rxSteps{}), &events, func(context.Context) error {
		hookedAtWrites = s.hooked(s.restoreLineSeq("restore-1"))
		return nil
	})
	res, err := runP4dSteps(s.ctx, in)
	if err != nil {
		t.Fatalf("runP4dSteps: %v", err)
	}
	if !slices.Equal(events, []string{"hooks", "bootwrites"}) {
		t.Fatalf("events %v, want hooks then bootwrites", events)
	}
	if !hookedAtWrites {
		t.Error("the boot writes ran before the hooks' restore_hooked record existed")
	}
	if !res.Hooks.Ran || res.Reapply.RestoreLine == 0 || res.Reapply.RestoreLine != s.restoreLineSeq("restore-1") {
		t.Errorf("result: hooks ran %v, restore line %d", res.Hooks.Ran, res.Reapply.RestoreLine)
	}
}

// TestRunP4dSteps_PlainBootStillRunsTheBootWrites: a plain boot of the same
// store runs no hooks, and the boot writes run anyway (they are every-boot
// writes, DATA3-18).
func TestRunP4dSteps_PlainBootStillRunsTheBootWrites(t *testing.T) {
	fx := newRxFx(t, nil)
	fx.boot(nil, nil)
	fx.restart()
	var events []string
	res, err := runP4dSteps(fx.ctx, p4dRun(fx, nil, &events, nil))
	if err != nil {
		t.Fatalf("runP4dSteps: %v", err)
	}
	if !slices.Equal(events, []string{"bootwrites"}) || res.Hooks.Ran || res.Reapply.Class != classSame {
		t.Fatalf("events %v, hooks ran %v, class %q; want only the boot writes on a same-store boot", events, res.Hooks.Ran, res.Reapply.Class)
	}
}

// TestRunP4dSteps_EveryStepErrorRefusesToServe: an error in any step is
// returned wrapping errP4dRefuse and its own cause, and no later step runs.
func TestRunP4dSteps_EveryStepErrorRefusesToServe(t *testing.T) {
	boom := errors.New("the batcomputer is down")

	t.Run("step 2", func(t *testing.T) {
		fx := newRxFx(t, nil)
		var events []string
		in := p4dRun(fx, nil, &events, nil)
		in.Reapply.Keys = nil // the keyring P4d step 1 opens is missing
		_, err := runP4dSteps(fx.ctx, in)
		if !errors.Is(err, errP4dRefuse) || !errors.Is(err, errReapplyNotReady) {
			t.Fatalf("err = %v, want errP4dRefuse wrapping errReapplyNotReady", err)
		}
		if len(events) != 0 {
			t.Errorf("later steps ran after a step 2 error: %v", events)
		}
	})

	t.Run("step 3", func(t *testing.T) {
		s := newRestoreScene(t, nil)
		var events []string
		in := p4dRun(s.rxFx, archiveBoot("restore-1", "arch-1", s.previous, &rxSteps{}), &events, nil)
		in.Hooks = func(env hookEnv) ([]journal.RestoreHook, error) {
			events = append(events, "hooks")
			real, err := restoreHookList(s.stores())(env)
			return append(real, func(context.Context, journal.RestoreContext) (map[string]int, error) { return nil, boom }), err
		}
		_, err := runP4dSteps(s.ctx, in)
		if !errors.Is(err, errP4dRefuse) || !errors.Is(err, boom) {
			t.Fatalf("err = %v, want errP4dRefuse wrapping the hook's error", err)
		}
		if !slices.Equal(events, []string{"hooks"}) {
			t.Errorf("events %v: the boot writes ran after a failed hook", events)
		}
	})

	t.Run("step 4", func(t *testing.T) {
		fx := newRxFx(t, nil)
		var events []string
		_, err := runP4dSteps(fx.ctx, p4dRun(fx, nil, &events, func(context.Context) error { return boom }))
		if !errors.Is(err, errP4dRefuse) || !errors.Is(err, boom) {
			t.Fatalf("err = %v, want errP4dRefuse wrapping the boot writes' error", err)
		}
	})

	t.Run("unwired", func(t *testing.T) {
		fx := newRxFx(t, nil)
		var events []string
		for name, mut := range map[string]func(*p4dStepsInput){
			"no hooks":       func(in *p4dStepsInput) { in.Hooks = nil },
			"no boot writes": func(in *p4dStepsInput) { in.BootWrites = nil },
		} {
			in := p4dRun(fx, nil, &events, nil)
			mut(&in)
			before := fx.pair()
			if _, err := runP4dSteps(fx.ctx, in); !errors.Is(err, errP4dRefuse) {
				t.Errorf("%s: err = %v, want errP4dRefuse", name, err)
			}
			if fx.pair() != before {
				t.Errorf("%s: step 2 ran with a step missing", name)
			}
		}
	})
}
