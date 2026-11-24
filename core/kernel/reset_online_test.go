// file: core/kernel/reset_online_test.go
// version: 0.1.0
// guid: 6a1d3c85-e247-4f90-8b3a-2c5f7e019d64
// last-edited: 2026-11-21

package kernel

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// Batman names only. The running service's reset staging (F4-08 B4):
// storagehttp.Stager over the open store, sharing the offline command's plan
// check and commit.

// restartRequested reports whether the lifecycle was asked to restart.
func restartRequested(k *Kernel) bool {
	select {
	case <-k.lifecycle.requested:
		return true
	default:
		return false
	}
}

// onlineFixture boots the host with alfred declaring p's reset hook, marks the
// app ready (a restart request before readiness is ignored by design), and
// returns the online stager over it.
func onlineFixture(t *testing.T, p *resetProbe) (*swapHost, *Kernel, *resetStager, string) {
	t.Helper()
	h := newSwapHost(t)
	root, _ := h.library()
	k := h.mustBoot(h.hookConfig(p))
	k.lifecycle.ready.Store(true)
	st, err := k.newResetStager(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	return h, k, st, root
}

var factoryRequest = storage.ResetRequest{Kind: restorejournal.ResetFactory, Confirm: storage.ResetConfirm}

// TestOnlineReset_StagesAndRequestsARestart: the running service stages the
// factory reset the offline command would, with the live install ID and the
// reset's rule (4) on Plan's context but no read-only storage (the module is
// running and reads its own), BeforeSwap once, and asks for the restart that
// applies it, only after the intent exists.
func TestOnlineReset_StagesAndRequestsARestart(t *testing.T) {
	p := &resetProbe{plan: storage.ResetPlan{Hook: []byte(`{"dump":"x"}`)}}
	h, k, st, _ := onlineFixture(t, p)
	var swaps []string
	k.cfg.BeforeSwap = func(path string) error {
		if restartRequested(k) {
			t.Error("the restart was requested before the intent was staged")
		}
		swaps = append(swaps, path)
		return nil
	}
	staged, err := st.StageReset(t.Context(), "u-lucius", factoryRequest)
	if err != nil {
		t.Fatalf("StageReset: %v", err)
	}
	slot := h.slot()
	if slot.Pending == nil || slot.Pending.ID != staged.ID || slot.Pending.Kind != restorejournal.KindReset {
		t.Fatalf("slot = %+v, want the reset %s pending", slot, staged.ID)
	}
	raw, err := os.ReadFile(restorejournal.PendingPath(h.state))
	if err != nil {
		t.Fatal(err)
	}
	if want := k.store.db.Admin().InstallID(); want == "" || !bytes.Contains(raw, []byte(want)) {
		t.Errorf("the intent does not carry the open store's install ID %q: %s", want, raw)
	}
	if !slices.Equal(swaps, []string{h.db}) {
		t.Errorf("BeforeSwap calls = %v, want one with the store path", swaps)
	}
	if len(p.kept) != 1 || p.kept[0] == nil {
		t.Fatalf("Plan saw kept %v, want the reset's rule (4)", p.kept)
	}
	if p.withStorage[0] {
		t.Error("Plan was handed read-only storage by the running service")
	}
	if !restartRequested(k) {
		t.Error("a staged reset did not request the restart that applies it")
	}
}

// TestOnlineReset_PlanNeedsTheKeptPaths: a hook whose Plan refuses to run
// without module.ResetPlanKept (as ABO's, which must leave the store out of
// the count it confirms) plans under the online stager.
func TestOnlineReset_PlanNeedsTheKeptPaths(t *testing.T) {
	h := newSwapHost(t)
	root, _ := h.library()
	plan := storage.ResetPlan{LibraryRoot: root, LibraryFiles: 3}
	c := h.config(nil)
	c.Modules = []module.Module{c.Modules[0].(testModule).with(func(m *module.Manifest) {
		m.ResetHook = &storage.ResetHook{
			Plan: func(ctx context.Context, _ storage.ResetRequest) (storage.ResetPlan, error) {
				if _, ok := module.ResetPlanKept(ctx); !ok {
					return storage.ResetPlan{}, errors.New("batcomputer: no kept paths, so the count would include the store")
				}
				return plan, nil
			},
			Run: func(context.Context, storage.ResetRun) error { return nil },
		}
	})}
	k := h.mustBoot(c)
	k.lifecycle.ready.Store(true)
	st, err := k.newResetStager(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	req := factoryRequest
	req.DeleteLibraryFiles, req.LibraryRoot, req.LibraryFileCount = true, root, 3
	if _, err := st.StageReset(t.Context(), "u-lucius", req); err != nil {
		t.Fatalf("StageReset: %v", err)
	}
}

// TestOnlineReset_RefusalsStageNothingAndKeepTheService: each refusal leaves
// the slot as it was and asks for no restart.
func TestOnlineReset_RefusalsStageNothingAndKeepTheService(t *testing.T) {
	setup := onlineFixture
	t.Run("the library wipe is not confirmed", func(t *testing.T) {
		h, k, st, _ := setup(t, &resetProbe{plan: storage.ResetPlan{LibraryRoot: "/library", LibraryFiles: 3}})
		req := factoryRequest
		req.DeleteLibraryFiles = true // no root, no count
		_, err := st.StageReset(t.Context(), "u-lucius", req)
		if _, ok := errors.AsType[*storage.LibraryConfirmError](err); !ok {
			t.Fatalf("StageReset = %v, want a *storage.LibraryConfirmError", err)
		}
		if h.slot().Busy() || restartRequested(k) {
			t.Error("a refused reset staged or asked for a restart")
		}
	})
	t.Run("the confirmation names another root", func(t *testing.T) {
		h, k, st, _ := setup(t, &resetProbe{plan: storage.ResetPlan{LibraryRoot: "/library", LibraryFiles: 3}})
		req := factoryRequest
		req.DeleteLibraryFiles, req.LibraryRoot, req.LibraryFileCount = true, "/elsewhere", 3
		_, err := st.StageReset(t.Context(), "u-lucius", req)
		if _, ok := errors.AsType[*storage.LibraryConfirmError](err); !ok {
			t.Fatalf("StageReset = %v, want a *storage.LibraryConfirmError", err)
		}
		if h.slot().Busy() || restartRequested(k) {
			t.Error("a refused reset staged or asked for a restart")
		}
	})
	t.Run("the hook refuses", func(t *testing.T) {
		boom := errors.New("the vault is sealed")
		h, k, st, _ := setup(t, &resetProbe{planErr: boom})
		if _, err := st.StageReset(t.Context(), "u-lucius", factoryRequest); !errors.Is(err, boom) {
			t.Fatalf("StageReset = %v, want the hook's error", err)
		}
		if h.slot().Busy() || restartRequested(k) {
			t.Error("a refused reset staged or asked for a restart")
		}
	})
	t.Run("BeforeSwap refuses", func(t *testing.T) {
		boom := errors.New("the batmobile is out")
		h, k, st, _ := setup(t, &resetProbe{})
		k.cfg.BeforeSwap = func(string) error { return boom }
		if _, err := st.StageReset(t.Context(), "u-lucius", factoryRequest); !errors.Is(err, boom) {
			t.Fatalf("StageReset = %v, want BeforeSwap's error", err)
		}
		if h.slot().Busy() || restartRequested(k) {
			t.Error("a refused reset staged or asked for a restart")
		}
	})
	t.Run("a second reset while one is staged", func(t *testing.T) {
		h, _, st, _ := setup(t, &resetProbe{})
		first, err := st.StageReset(t.Context(), "u-lucius", factoryRequest)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.StageReset(t.Context(), "u-selina", factoryRequest); err == nil {
			t.Fatal("a second reset staged over the first")
		}
		if s := h.slot(); s.Pending == nil || s.Pending.ID != first.ID {
			t.Errorf("slot = %+v, want only the first reset %s", s, first.ID)
		}
	})
	t.Run("an invalid request", func(t *testing.T) {
		h, k, st, _ := setup(t, &resetProbe{})
		if _, err := st.StageReset(t.Context(), "u-lucius", storage.ResetRequest{Kind: restorejournal.ResetFactory}); !errors.Is(err, storage.ErrResetConfirmRequired) {
			t.Fatalf("StageReset without the confirmation = %v, want ErrResetConfirmRequired", err)
		}
		if h.slot().Busy() || restartRequested(k) {
			t.Error("a refused reset staged or asked for a restart")
		}
	})
}
