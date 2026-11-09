// file: core/kernel/offline_kill_test.go
// version: 1.0.0
// guid: a0f2d104-e67e-4d33-b0ea-9d4393e9642a
// last-edited: 2026-11-09

package kernel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
	"github.com/falkcorp/mediacore/storage/restorejournal/restorejournaltest"
)

// killer panics at the nth Enter of any label and records every label it
// saw; nth -1 never panics, which counts a clean run's labelled operations.
type killer struct {
	nth    int
	seen   int
	labels []string
}

type killed struct{ label string }

func (k *killer) Enter(label string) func() {
	k.labels = append(k.labels, label)
	k.seen++
	if k.seen-1 == k.nth {
		panic(killed{label})
	}
	return func() {}
}

// killAt runs fn under a killer at kill point nth and returns the label it
// was killed at, or "" with fn's error when it ran to the end. Every defer
// of the offline command runs as the panic unwinds, as the process's exit
// releases its locks.
func killAt(fn func(ctx context.Context) error, build string, nth int) (label string, err error) {
	defer func() {
		if r := recover(); r != nil {
			k, ok := r.(killed)
			if !ok {
				panic(r)
			}
			label = k.label
		}
	}()
	ctx := bootseam.With(context.Background(), &bootseam.Seams{BuildID: build, Labels: &killer{nth: nth}})
	return "", fn(ctx)
}

// cleanLabels is every labelled operation of one clean run of fn, in order.
func cleanLabels(t *testing.T, fn func(ctx context.Context) error, build string) []string {
	t.Helper()
	probe := &killer{nth: -1}
	if err := fn(bootseam.With(context.Background(), &bootseam.Seams{BuildID: build, Labels: probe})); err != nil {
		t.Fatalf("the clean run: %v", err)
	}
	return probe.labels
}

// skippedKill is a label an in-process panic cannot model: storage.close is
// the close of a store the offline command opened read-only (the live
// store's install ID, a source's verify open), and a panic there leaves
// Pebble's in-process lock table entry behind, which a real kill releases.
func skippedKill(label string) bool { return label == "storage.close" }

var upgradedStep = storage.Migration{Version: 2, Name: "upgraded", Compat: storage.Additive,
	Up: func(_ context.Context, tx *storage.MigrationTx) error {
		return tx.Set([]byte("upgraded"), []byte("yes"))
	}}

// TestOfflineRestoreKillMatrix (R1-25 review SF1, probe 5): storage
// restore-offline --checkpoint killed before each of its labelled
// operations, then booted: the store and the state file are both the
// original's or both the checkpoint's, and the slot ends empty. It kills
// the in-flight rename moved after the renames (a kill mid-swap with no
// in-flight file boots a half-swapped store) and applied_offline dropped
// from the terminal steps (a kill before the in-flight file's delete halts
// every boot).
func TestOfflineRestoreKillMatrix(t *testing.T) {
	setup := func(t *testing.T) (*swapHost, string) {
		h := newSwapHost(t)
		if err := os.MkdirAll(h.state, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(h.keyFile(), []byte("wayne"), 0o600); err != nil {
			t.Fatal(err)
		}
		h.close(h.mustBoot(h.keyed(h.config(nil))))               // cave "1"
		h.close(h.mustBoot(h.keyed(h.config(nil, upgradedStep)))) // the checkpoint, completed
		var deps module.Deps
		k := h.mustBoot(h.keyed(h.config(&deps, upgradedStep)))
		if err := deps.Storage.Keyspace.Set(t.Context(), []byte("cave"), []byte("bane")); err != nil {
			t.Fatal(err)
		}
		h.close(k)
		if err := os.WriteFile(h.keyFile(), []byte("bane"), 0o600); err != nil {
			t.Fatal(err)
		}
		return h, h.pointer()
	}
	restore := func(h *swapHost, name string) func(ctx context.Context) error {
		return func(ctx context.Context) error {
			_, err := RestoreOffline(ctx, h.keyed(h.config(nil)), RestoreOfflineRequest{Checkpoint: name, AcceptLoss: true})
			return err
		}
	}
	h, name := setup(t)
	labels := cleanLabels(t, restore(h, name), swapBuild)
	if len(labels) < 10 {
		t.Fatalf("a clean restore made %d labelled operations (%v); the matrix would prove nothing", len(labels), labels)
	}
	for i, want := range labels {
		if skippedKill(want) {
			continue
		}
		h, name := setup(t)
		label, err := killAt(restore(h, name), swapBuild, i)
		if label == "" {
			t.Fatalf("kill %d (%s): the restore ran to the end (%v); the clean run's labels changed", i, want, err)
		}
		var cave string
		var bootErrs []string
		for range 3 {
			var deps module.Deps
			k, err := New(h.ctx, h.keyed(h.config(&deps, upgradedStep)))
			if err != nil {
				bootErrs = append(bootErrs, err.Error())
				continue
			}
			v, _ := deps.Storage.Keyspace.Get(t.Context(), []byte("cave"))
			cave = string(v)
			h.close(k)
			break
		}
		key, _ := os.ReadFile(h.keyFile())
		st := h.slot()
		if st.Busy() || !((cave == "1" && string(key) == "wayne") || (cave == "bane" && string(key) == "bane")) {
			t.Errorf("kill %d at %s: cave %q, key %q, slot %+v, boot errors %v; want both the original's or both the checkpoint's, slot empty",
				i, label, cave, key, st, bootErrs)
		}
	}
}

// TestEscapeKillMatrix (R1-25 review SF1): the escape of a failed checkpoint
// restore killed before each of its labelled operations. A boot that finds
// the escape started and unfinished halts with ErrEscapeInterrupted, never
// booting the half-swapped store, and rerunning the same restore-offline
// --checkpoint finishes it; a kill before the escape started leaves the
// failed restore for the previous build to resume. Either way the store
// ends the checkpoint's, the state file the checkpoint's once the escape
// moved anything, and the slot empty.
func TestEscapeKillMatrix(t *testing.T) {
	const id, prevBuild = "r-checkpoint", "batcomputer-r0"
	setup := func(t *testing.T) (*swapHost, string) {
		h := newSwapHost(t)
		if err := os.MkdirAll(h.state, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(h.keyFile(), []byte("wayne"), 0o600); err != nil {
			t.Fatal(err)
		}
		h.close(h.mustBoot(h.keyed(h.config(nil))))
		h.stage(id, restorejournal.KindCheckpoint, h.stagedCopy(id, "robin"))
		if _, err := h.boot(h.keyed(h.config(nil, brokenStep))); !errors.Is(err, ErrCheckpointRestoreFailedHere) {
			t.Fatalf("restore boot with a failing step = %v", err)
		}
		// What the failing build left in the state file.
		if err := os.WriteFile(h.keyFile(), []byte("joker"), 0o600); err != nil {
			t.Fatal(err)
		}
		h.ctx = bootseam.With(t.Context(), &bootseam.Seams{BuildID: prevBuild})
		return h, h.pointer()
	}
	escape := func(h *swapHost, name string) func(ctx context.Context) error {
		return func(ctx context.Context) error {
			_, err := RestoreOffline(ctx, h.keyed(h.config(nil)), RestoreOfflineRequest{Checkpoint: name})
			return err
		}
	}
	h, name := setup(t)
	labels := cleanLabels(t, escape(h, name), prevBuild)
	if !strings.Contains(strings.Join(labels, " "), "swap.escape.place") {
		t.Fatalf("a clean escape made no placement (%v)", labels)
	}
	for i, want := range labels {
		if skippedKill(want) {
			continue
		}
		h, name := setup(t)
		label, err := killAt(escape(h, name), prevBuild, i)
		if label == "" {
			t.Fatalf("kill %d (%s): the escape ran to the end (%v); the clean run's labels changed", i, want, err)
		}
		_, serr := os.Stat(filepath.Join(restorejournal.JournalDir(h.state, id), "escape.started"))
		started := serr == nil
		recs := h.journal(id)
		last := recs[len(recs)-1].Step
		var deps module.Deps
		k, bootErr := New(h.ctx, h.keyed(h.config(&deps)))
		if started && last == restoreswap.StepFailed && !errors.Is(bootErr, restoreswap.ErrEscapeInterrupted) {
			if k != nil {
				h.close(k)
			}
			t.Errorf("kill %d at %s: boot after an interrupted escape = %v, want ErrEscapeInterrupted", i, label, bootErr)
			continue
		}
		if k != nil {
			h.close(k)
		}
		if bootErr != nil {
			if err := escape(h, name)(h.ctx); err != nil {
				t.Errorf("kill %d at %s: boot = %v; rerunning the escape = %v, want it to finish", i, label, bootErr, err)
				continue
			}
		}
		cave := h.readCave()
		key, _ := os.ReadFile(h.keyFile())
		wantKey := "joker"
		if started {
			wantKey = "wayne"
		}
		if st := h.slot(); st.Busy() || cave != "robin" || string(key) != wantKey {
			t.Errorf("kill %d at %s (escape started %v): cave %q, key %q, slot %+v; want the checkpoint's robin, key %q, slot empty",
				i, label, started, cave, key, st, wantKey)
		}
	}
}

// TestOfflineRestoreRefusesUnopenableSources (R1-25 review SF2, SF4): an
// archive whose extracted store this build does not open (here a level
// this build does not serve) is refused before anything is staged, and so
// is a checkpoint whose store copy this build does not open; the live store
// stays as it was. Without the verify open the restore swaps either in and
// the next boot refuses the store.
func TestOfflineRestoreRefusesUnopenableSources(t *testing.T) {
	t.Run("archive", func(t *testing.T) {
		h := newSwapHost(t)
		h.close(h.mustBoot(h.config(nil)))
		tooNew := filepath.Join(t.TempDir(), "too-new.pebble")
		h.tooNewStore(tooNew)
		c := h.config(nil)
		c.ArchiveExtract = func(_ context.Context, archive, dir string) (string, error) {
			copyTree(t, tooNew, dir)
			return "archive-too-new", nil
		}
		_, err := RestoreOffline(h.ctx, c, RestoreOfflineRequest{Archive: filepath.Join(t.TempDir(), "joker.tar.gz"), AcceptLoss: true})
		if err == nil || !strings.Contains(err.Error(), "does not open") {
			t.Fatalf("restore of a too-new archive = %v, want a refusal naming the open", err)
		}
		h.assertNothingStaged()
		if got := h.readCave(); got != "1" {
			t.Fatalf("cave = %q, want the live store untouched", got)
		}
	})
	t.Run("checkpoint", func(t *testing.T) {
		h := newSwapHost(t)
		name := h.completedUpgrade()
		store := filepath.Join(filepath.Dir(h.db), ".mediacore-checkpoints", name, "db")
		if err := os.RemoveAll(store); err != nil {
			t.Fatal(err)
		}
		h.tooNewStore(store)
		_, err := RestoreOffline(h.ctx, h.keyed(h.config(nil)), RestoreOfflineRequest{Checkpoint: name, AcceptLoss: true})
		if err == nil || !strings.Contains(err.Error(), "does not open") {
			t.Fatalf("restore of a too-new checkpoint = %v, want a refusal naming the open", err)
		}
		h.assertNothingStaged()
		if got := h.readCave(caveStep(2, "bane")); got != "bane" {
			t.Fatalf("cave = %q, want the live store untouched", got)
		}
	})
}

// TestRefusedRestoreLeavesNoStaging (R1-25 review SF3, probe 1): a restore
// refused because a swap is in flight that it cannot escape reads the slot
// before it extracts anything and names the in-flight swap; a restore whose
// Stage refuses (here a rename across filesystems) removes what it staged.
// Before the fix each refused attempt left a full store copy beside the
// live one.
func TestRefusedRestoreLeavesNoStaging(t *testing.T) {
	t.Run("swap in flight", func(t *testing.T) {
		h := newSwapHost(t)
		h.close(h.mustBoot(h.config(nil)))
		staged, err := StageResetOffline(h.ctx, h.config(nil), "alfred",
			storage.ResetRequest{Kind: restorejournal.ResetStore, Confirm: storage.ResetConfirm})
		if err != nil {
			t.Fatal(err)
		}
		// A boot killed right after P0a's in-flight rename.
		if err := os.Rename(restorejournal.PendingPath(h.state),
			filepath.Join(restorejournal.JournalDir(h.state, staged.ID), restorejournal.InflightName)); err != nil {
			t.Fatal(err)
		}
		c := h.config(nil)
		extracted := 0
		c.ArchiveExtract = func(context.Context, string, string) (string, error) {
			extracted++
			return "", errors.New("unreachable")
		}
		for range 2 {
			_, err := RestoreOffline(h.ctx, c, RestoreOfflineRequest{Archive: filepath.Join(t.TempDir(), "joker.tar.gz"), AcceptLoss: true})
			if !errors.Is(err, restorejournal.ErrSwapPending) || !strings.Contains(err.Error(), restorejournal.TokenRestoreInflight) ||
				!strings.Contains(err.Error(), staged.ID) {
				t.Fatalf("restore while a reset is in flight = %v, want restore_inflight naming %s", err, staged.ID)
			}
		}
		if extracted != 0 {
			t.Fatalf("the archive was extracted %d times for a refused restore", extracted)
		}
		if names, _ := os.ReadDir(filepath.Join(filepath.Dir(h.db), ".mediacore-restore")); len(names) != 0 {
			var left []string
			for _, n := range names {
				left = append(left, n.Name())
			}
			t.Fatalf(".mediacore-restore holds %v after the refused restores, want only nothing", left)
		}
	})
	t.Run("stage refused", func(t *testing.T) {
		h := newSwapHost(t)
		h.close(h.mustBoot(h.config(nil)))
		saved := filepath.Join(t.TempDir(), "archived.pebble")
		copyTree(t, h.db, saved)
		// The staging directory reads as another filesystem than the live
		// store's, so Stage's rename pre-check refuses the swap.
		probe := restorejournaltest.OtherDevice{Probe: restorejournal.OSProbe(),
			Dirs: []string{filepath.Join(filepath.Dir(h.db), restorejournal.AsideDirName)}}
		ctx := bootseam.With(t.Context(), &bootseam.Seams{BuildID: swapBuild, Probe: probe})
		c := h.config(nil)
		c.ArchiveExtract = func(_ context.Context, archive, dir string) (string, error) {
			copyTree(t, saved, dir)
			return "archive-" + filepath.Base(archive), nil
		}
		_, err := RestoreOffline(ctx, c, RestoreOfflineRequest{Archive: filepath.Join(t.TempDir(), "joker.tar.gz"), AcceptLoss: true})
		if !errors.Is(err, storage.ErrRenameRefused) {
			t.Fatalf("restore across filesystems = %v, want ErrRenameRefused", err)
		}
		h.assertNothingStaged()
		if got := h.readCave(); got != "1" {
			t.Fatalf("cave = %q, want the live store untouched", got)
		}
	})
}
