// file: core/kernel/offline_test.go
// version: 1.1.0
// guid: dc0dbdfc-2776-4dc9-98cb-2d552f85078a
// last-edited: 2026-11-09

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/coretest/logcapture"
	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// brokenStep is an upgrade step that always fails.
var brokenStep = storage.Migration{Version: 2, Name: "broken", Compat: storage.Additive,
	Up: func(context.Context, *storage.MigrationTx) error { return errors.New("bane broke it") }}

// caveStep is an upgrade step that sets cave to value.
func caveStep(v int, value string) storage.Migration {
	return storage.Migration{Version: v, Name: "cave-" + value, Compat: storage.Additive,
		Up: func(_ context.Context, tx *storage.MigrationTx) error { return tx.Set([]byte("cave"), []byte(value)) }}
}

// keyFile is the state file the offline tests' checkpoints copy.
func (h *swapHost) keyFile() string { return filepath.Join(h.state, "alfred.key") }

// keyed is c with the host's state-file table.
func (h *swapHost) keyed(c Config) Config {
	c.StorageStateFiles = []storage.StateFile{{Label: "alfred_key", Path: h.keyFile()}}
	return c
}

// readCave boots the store with alfred and migs, reads cave and closes it.
func (h *swapHost) readCave(migs ...storage.Migration) string {
	h.t.Helper()
	var deps module.Deps
	k := h.mustBoot(h.config(&deps, migs...))
	defer h.close(k)
	v, err := deps.Storage.Keyspace.Get(h.t.Context(), []byte("cave"))
	if err != nil {
		h.t.Fatal(err)
	}
	return string(v)
}

// pointer is the checkpoint the store's pointer names.
func (h *swapHost) pointer() string {
	h.t.Helper()
	raw, err := os.ReadFile(h.db + ".migration-checkpoint")
	if err != nil {
		h.t.Fatal(err)
	}
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.Name == "" {
		h.t.Fatalf("pointer %s: %v", raw, err)
	}
	return p.Name
}

// completedUpgrade boots alfred's seed (cave "1", the key file "wayne"),
// then an upgrade that sets cave to "bane" and rewrites the key file, and
// returns its checkpoint, which completed.
func (h *swapHost) completedUpgrade() string {
	h.t.Helper()
	if err := os.MkdirAll(h.state, 0o700); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(h.keyFile(), []byte("wayne"), 0o600); err != nil {
		h.t.Fatal(err)
	}
	h.close(h.mustBoot(h.keyed(h.config(nil))))
	h.close(h.mustBoot(h.keyed(h.config(nil, caveStep(2, "bane")))))
	if err := os.WriteFile(h.keyFile(), []byte("bane"), 0o600); err != nil {
		h.t.Fatal(err)
	}
	return h.pointer()
}

// failedUpgrade boots alfred's seed, then an upgrade whose step fails, and
// returns its checkpoint, which is open.
func (h *swapHost) failedUpgrade() string {
	h.t.Helper()
	h.close(h.mustBoot(h.config(nil)))
	if _, err := h.boot(h.config(nil, brokenStep)); err == nil {
		h.t.Fatal("the failing upgrade booted")
	}
	return h.pointer()
}

// slot is the host's swap slot.
func (h *swapHost) slot() restorejournal.SlotState {
	h.t.Helper()
	st, err := restorejournal.ReadSlot(vfs.Default, h.state)
	if err != nil {
		h.t.Fatal(err)
	}
	return st
}

// assertNothingStaged fails when the slot holds a swap or a staging
// directory was left beside the store.
func (h *swapHost) assertNothingStaged() {
	h.t.Helper()
	if st := h.slot(); st.Busy() {
		h.t.Fatalf("slot = %+v, want empty", st)
	}
	if names, _ := os.ReadDir(filepath.Join(filepath.Dir(h.db), ".mediacore-restore")); len(names) > 0 {
		h.t.Fatalf(".mediacore-restore holds %v, want nothing staged", names)
	}
}

// TestRestoreOffline (05-storage §5S.3.12 "Sources", "Loss", "In place"):
// the latest attempt's open checkpoint restores without --accept-loss and
// the flag changes nothing; a completed checkpoint and an archive are
// refused without it, with nothing staged, and restored with it, the state
// file put back; the record turns consumed. An older checkpoint, a held
// store, a tampered state-file copy and a failing BeforeSwap are refused
// with nothing staged; a pending intent is superseded.
func TestRestoreOffline(t *testing.T) {
	for _, accept := range []bool{false, true} {
		t.Run("open checkpoint, accept-loss "+map[bool]string{false: "unset", true: "set"}[accept], func(t *testing.T) {
			h := newSwapHost(t)
			name := h.failedUpgrade()
			res, err := RestoreOffline(h.ctx, h.config(nil), RestoreOfflineRequest{Checkpoint: name, AcceptLoss: accept})
			if err != nil {
				t.Fatalf("restore of the open checkpoint = %v", err)
			}
			if got := stepsOf(h.journal(res.ID)); got[len(got)-1] != restoreswap.StepAppliedOffline {
				t.Fatalf("journal %v, want applied_offline", got)
			}
			if st, _, err := checkpointStatus(h.db, name); err != nil || st != "consumed" {
				t.Fatalf("record status %q (%v), want consumed", st, err)
			}
			if h.slot().Busy() {
				t.Fatalf("slot after the restore = %+v", h.slot())
			}
			if got := h.readCave(); got != "1" {
				t.Fatalf("cave after the restore = %q, want the seed's 1", got)
			}
		})
	}
	t.Run("completed checkpoint", func(t *testing.T) {
		h := newSwapHost(t)
		name := h.completedUpgrade()
		var swaps []string
		c := h.keyed(h.config(nil))
		c.BeforeSwap = func(p string) error { swaps = append(swaps, p); return nil }
		if _, err := RestoreOffline(h.ctx, c, RestoreOfflineRequest{Checkpoint: name}); !errors.Is(err, ErrAcceptLossRequired) {
			t.Fatalf("without --accept-loss = %v, want ErrAcceptLossRequired", err)
		}
		h.assertNothingStaged()
		if len(swaps) != 0 {
			t.Fatalf("BeforeSwap ran for a refused restore: %v", swaps)
		}
		res, err := RestoreOffline(h.ctx, c, RestoreOfflineRequest{Checkpoint: name, AcceptLoss: true})
		if err != nil {
			t.Fatalf("with --accept-loss = %v", err)
		}
		if !slices.Equal(swaps, []string{h.db}) {
			t.Fatalf("BeforeSwap calls %v, want one with the store path", swaps)
		}
		if raw, err := os.ReadFile(h.keyFile()); err != nil || string(raw) != "wayne" {
			t.Fatalf("key file after the restore = %q (%v), want the checkpoint's", raw, err)
		}
		if st, _, err := checkpointStatus(h.db, name); err != nil || st != "consumed" {
			t.Fatalf("record status %q (%v), want consumed", st, err)
		}
		if !strings.Contains(res.Source, name) {
			t.Fatalf("source %q does not name the checkpoint", res.Source)
		}
		if got := h.readCave(); got != "1" {
			t.Fatalf("cave after the restore = %q, want 1", got)
		}
	})
	t.Run("archive", func(t *testing.T) {
		h := newSwapHost(t)
		h.close(h.mustBoot(h.config(nil)))
		saved := filepath.Join(t.TempDir(), "archived.pebble")
		copyTree(t, h.db, saved)
		h.close(h.mustBoot(h.config(nil, caveStep(2, "bane"))))
		c := h.config(nil)
		c.ArchiveExtract = func(_ context.Context, archive, dir string) (string, error) {
			copyTree(t, saved, dir)
			return "archive-" + filepath.Base(archive), nil
		}
		req := RestoreOfflineRequest{Archive: filepath.Join(t.TempDir(), "joker.tar.gz")}
		if _, err := RestoreOffline(h.ctx, c, req); !errors.Is(err, ErrAcceptLossRequired) {
			t.Fatalf("archive without --accept-loss = %v, want ErrAcceptLossRequired", err)
		}
		h.assertNothingStaged()
		req.AcceptLoss = true
		res, err := RestoreOffline(h.ctx, c, req)
		if err != nil {
			t.Fatalf("archive with --accept-loss = %v", err)
		}
		if got := stepsOf(h.journal(res.ID)); got[len(got)-1] != restoreswap.StepAppliedOffline {
			t.Fatalf("journal %v, want applied_offline", got)
		}
		if got := h.readCave(); got != "1" {
			t.Fatalf("cave after the archive restore = %q, want 1", got)
		}
	})
	t.Run("not the latest", func(t *testing.T) {
		h := newSwapHost(t)
		h.completedUpgrade()
		_, err := RestoreOffline(h.ctx, h.config(nil), RestoreOfflineRequest{Checkpoint: "20200101T000000Z-0badc0de", AcceptLoss: true})
		if !errors.Is(err, ErrNotLatestCheckpoint) {
			t.Fatalf("an older checkpoint = %v, want ErrNotLatestCheckpoint", err)
		}
		h.assertNothingStaged()
	})
	t.Run("released checkpoint", func(t *testing.T) {
		h := newSwapHost(t)
		name := h.failedUpgrade()
		cps, err := storage.CheckpointsOf(storage.FSOf(vfs.Default), h.db, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := cps.Release(name, time.Now()); err != nil {
			t.Fatal(err)
		}
		_, err = RestoreOffline(h.ctx, h.config(nil), RestoreOfflineRequest{Checkpoint: name, AcceptLoss: true})
		if err == nil || !strings.Contains(err.Error(), "released") {
			t.Fatalf("restore of a released checkpoint whose copy remains = %v, want a refusal", err)
		}
		h.assertNothingStaged()
	})
	t.Run("sweep in progress", func(t *testing.T) {
		h := newSwapHost(t)
		name := h.completedUpgrade()
		cps, err := storage.CheckpointsOf(storage.FSOf(vfs.Default), h.db, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		unlock, err := cps.Lock(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(h.ctx, 300*time.Millisecond)
		defer cancel()
		_, err = RestoreOffline(ctx, h.keyed(h.config(nil)), RestoreOfflineRequest{Checkpoint: name, AcceptLoss: true})
		unlock()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("restore while a sweep holds the checkpoint directory = %v, want it to wait", err)
		}
		h.assertNothingStaged()
		if _, err := RestoreOffline(h.ctx, h.keyed(h.config(nil)), RestoreOfflineRequest{Checkpoint: name, AcceptLoss: true}); err != nil {
			t.Fatalf("restore once the sweep let go = %v", err)
		}
	})
	// R1-24's seam: storage prune-checkpoints takes the same sweep lock and
	// probes the swap slot under it, so a prune that starts while a restore
	// stages and swaps waits for it and deletes nothing; once the restore
	// let go, the prune runs and the consumed checkpoint is deleted.
	t.Run("prune waits for a restore in progress", func(t *testing.T) {
		h := newSwapHost(t)
		name := h.completedUpgrade()
		cpDir := filepath.Join(filepath.Dir(h.db), ".mediacore-checkpoints", name)
		var pruneErr error
		var pruned storage.PruneResult
		c := h.keyed(h.config(nil))
		c.BeforeSwap = func(string) error {
			ctx, cancel := context.WithTimeout(h.ctx, 300*time.Millisecond)
			defer cancel()
			pruned, pruneErr = storage.PruneCheckpoints(ctx, storage.PruneOptions{Path: h.db, StateDir: h.state, IncludeOpen: true})
			return nil
		}
		if _, err := RestoreOffline(h.ctx, c, RestoreOfflineRequest{Checkpoint: name, AcceptLoss: true}); err != nil {
			t.Fatalf("restore = %v", err)
		}
		if !errors.Is(pruneErr, context.DeadlineExceeded) || len(pruned.Deleted) != 0 {
			t.Fatalf("prune during the restore = %+v, %v; want it to wait on the sweep lock and delete nothing", pruned, pruneErr)
		}
		if _, err := os.Stat(cpDir); err != nil {
			t.Fatalf("the restored checkpoint during the restore: %v", err)
		}
		res, err := storage.PruneCheckpoints(h.ctx, storage.PruneOptions{Path: h.db, StateDir: h.state})
		if err != nil || !slices.Contains(res.Deleted, name) {
			t.Fatalf("prune after the restore = %+v, %v; want the consumed checkpoint %s deleted", res, err, name)
		}
	})
	t.Run("held store", func(t *testing.T) {
		h := newSwapHost(t)
		name := h.completedUpgrade()
		k := h.mustBoot(h.config(nil, caveStep(2, "bane")))
		_, err := RestoreOffline(h.ctx, h.keyed(h.config(nil)), RestoreOfflineRequest{Checkpoint: name, AcceptLoss: true})
		if !errors.Is(err, ErrStoreHeld) {
			t.Fatalf("restore under a running service = %v, want ErrStoreHeld", err)
		}
		h.close(k)
		h.assertNothingStaged()
		if got := h.readCave(caveStep(2, "bane")); got != "bane" {
			t.Fatalf("cave = %q, want the live store untouched", got)
		}
	})
	t.Run("tampered state file", func(t *testing.T) {
		h := newSwapHost(t)
		name := h.completedUpgrade()
		cp := filepath.Join(filepath.Dir(h.db), ".mediacore-checkpoints", name, "state", "alfred_key")
		if err := os.WriteFile(cp, []byte("riddler"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := RestoreOffline(h.ctx, h.keyed(h.config(nil)), RestoreOfflineRequest{Checkpoint: name, AcceptLoss: true})
		if err == nil || !strings.Contains(err.Error(), "SHA-256") {
			t.Fatalf("restore with a tampered state file = %v, want a SHA-256 refusal", err)
		}
		h.assertNothingStaged()
		if raw, _ := os.ReadFile(h.keyFile()); string(raw) != "bane" {
			t.Fatalf("key file = %q, want the live one untouched", raw)
		}
	})
	t.Run("BeforeSwap fails", func(t *testing.T) {
		h := newSwapHost(t)
		name := h.completedUpgrade()
		c := h.keyed(h.config(nil))
		c.BeforeSwap = func(string) error { return errors.New("the vector graph is busy") }
		if _, err := RestoreOffline(h.ctx, c, RestoreOfflineRequest{Checkpoint: name, AcceptLoss: true}); err == nil {
			t.Fatal("restore with a failing BeforeSwap succeeded")
		}
		h.assertNothingStaged()
		if got := h.readCave(caveStep(2, "bane")); got != "bane" {
			t.Fatalf("cave = %q, want the live store untouched", got)
		}
	})
	t.Run("pending intent superseded", func(t *testing.T) {
		h := newSwapHost(t)
		name := h.completedUpgrade()
		staged, err := StageResetOffline(h.ctx, h.config(nil, caveStep(2, "bane")), "alfred",
			storage.ResetRequest{Kind: restorejournal.ResetStore, Confirm: storage.ResetConfirm})
		if err != nil {
			t.Fatal(err)
		}
		res, err := RestoreOffline(h.ctx, h.keyed(h.config(nil)), RestoreOfflineRequest{Checkpoint: name, AcceptLoss: true})
		if err != nil {
			t.Fatalf("restore over a pending reset = %v", err)
		}
		if res.Superseded != staged.ID {
			t.Fatalf("superseded %q, want the pending reset %s", res.Superseded, staged.ID)
		}
		recs := h.journal(staged.ID)
		if last := recs[len(recs)-1]; last.Step != restoreswap.StepRejected || !strings.Contains(last.Note, "superseded") {
			t.Fatalf("pending reset journal ends %+v, want rejected as superseded", last)
		}
	})
}

// TestRestoreOfflineIntoEmptyPath (05-storage §5S.3.12 "Into an empty
// path"): --into copies the checkpoint into a new directory inside the
// scratch area, the state files into <dir>.state/, modes 0700 and 0600,
// without --accept-loss, without the store's lock and without BeforeSwap;
// the live store, its state file and the slot are untouched. A directory
// outside the scratch area, or one that exists, is refused and nothing is
// created.
func TestRestoreOfflineIntoEmptyPath(t *testing.T) {
	h := newSwapHost(t)
	name := h.completedUpgrade()
	scratch := filepath.Join(filepath.Dir(h.db), ".mediacore-checkpoints", "scratch")
	into := filepath.Join(scratch, "rehearsal")
	c := h.keyed(h.config(nil))
	c.BeforeSwap = func(string) error { t.Error("BeforeSwap ran for an --into copy"); return nil }
	held := h.mustBoot(h.config(nil, caveStep(2, "bane"))) // --into needs no stopped service
	res, err := RestoreOffline(h.ctx, c, RestoreOfflineRequest{Checkpoint: name, Into: into})
	if err != nil {
		t.Fatalf("--into = %v", err)
	}
	h.close(held)
	if res.Into != into || res.IntoState != into+".state" || len(res.Warnings) == 0 {
		t.Fatalf("result %+v, want the copy, its state directory and the secrets warning", res)
	}
	for _, d := range []string{into, into + ".state", scratch} {
		if fi, err := os.Stat(d); err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v, mode %v; want 0700", d, err, fi.Mode().Perm())
		}
	}
	if raw, err := os.ReadFile(filepath.Join(into+".state", "alfred_key")); err != nil || string(raw) != "wayne" {
		t.Fatalf("state copy = %q (%v), want the checkpoint's", raw, err)
	}
	err = filepath.WalkDir(into, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if fi, err := d.Info(); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v, want 0600", p, fi.Mode().Perm())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(into + restorejournal.LevelsSuffix); err != nil {
		t.Fatalf("the --into copy has no level sidecar: %v", err)
	}
	h.assertNothingStaged()
	if raw, _ := os.ReadFile(h.keyFile()); string(raw) != "bane" {
		t.Fatalf("live key file = %q, want it untouched", raw)
	}
	if got := h.readCave(caveStep(2, "bane")); got != "bane" {
		t.Fatalf("live cave = %q, want it untouched", got)
	}
	if st, _, err := checkpointStatus(h.db, name); err != nil || st == "consumed" {
		t.Fatalf("record status %q (%v), want it unchanged by a copy", st, err)
	}
	var deps module.Deps
	cc := h.config(&deps, caveStep(2, "bane"))
	cc.StoragePath, cc.StateDir = into, ""
	k := h.mustBoot(cc)
	if v, err := deps.Storage.Keyspace.Get(t.Context(), []byte("cave")); err != nil || string(v) != "bane" {
		t.Fatalf("the copy upgraded again reads cave %q (%v), want bane: the copy holds the checkpoint", v, err)
	}
	h.close(k)

	for _, bad := range []string{filepath.Join(t.TempDir(), "outside"), scratch, into} {
		if _, err := RestoreOffline(h.ctx, c, RestoreOfflineRequest{Checkpoint: name, Into: bad}); err == nil {
			t.Errorf("--into %s succeeded, want a refusal", bad)
		}
	}
	if _, err := os.Stat(filepath.Join(t.TempDir(), "outside")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused --into created its directory: %v", err)
	}
}

// TestRestoreOfflineIntoWritesTheCopysLevels (05-storage §5S.3.12 row
// make rollback-restore; R1-27): an --into copy gets its own level sidecar,
// rewritten from the copy's stamps, so make rollback-restore reads an
// archive's levels from the archived store itself before it touches the live
// store. A copy at a level this build does not serve still reports it, the
// live store's sidecar is untouched, nothing is staged, and a copy that does
// not open leaves neither the copy nor a sidecar behind.
func TestRestoreOfflineIntoWritesTheCopysLevels(t *testing.T) {
	h := newSwapHost(t)
	h.close(h.mustBoot(h.config(nil)))
	saved := filepath.Join(t.TempDir(), "archived.pebble")
	h.tooNewStore(saved)
	live := h.db + restorejournal.LevelsSuffix
	before, err := os.ReadFile(live)
	if err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(filepath.Dir(h.db), ".mediacore-checkpoints", "scratch")
	into := filepath.Join(scratch, "rollback-restore-levels")
	c := h.config(nil)
	c.ArchiveExtract = func(_ context.Context, _, dir string) (string, error) {
		copyTree(t, saved, dir)
		return "archive-joker", nil
	}
	archive := filepath.Join(t.TempDir(), "joker.tar.gz")
	if _, err := RestoreOffline(h.ctx, c, RestoreOfflineRequest{Archive: archive, Into: into}); err != nil {
		t.Fatalf("--into = %v", err)
	}
	raw, err := os.ReadFile(into + restorejournal.LevelsSuffix)
	if err != nil {
		t.Fatalf("the copy has no level sidecar: %v", err)
	}
	if !slices.Contains(strings.Split(strings.TrimSpace(string(raw)), "\n"), "alfred 2") {
		t.Fatalf("copy sidecar = %q, want alfred at the archived store's level 2", raw)
	}
	if after, _ := os.ReadFile(live); string(after) != string(before) {
		t.Fatalf("live sidecar changed from %q to %q", before, after)
	}
	h.assertNothingStaged()

	bad := filepath.Join(scratch, "unopenable")
	c.ArchiveExtract = func(_ context.Context, _, dir string) (string, error) {
		return "archive-mud", os.WriteFile(filepath.Join(dir, "clayface"), []byte("not a store"), 0o600)
	}
	if _, err := RestoreOffline(h.ctx, c, RestoreOfflineRequest{Archive: archive, Into: bad}); err == nil {
		t.Fatal("--into of a copy that does not open succeeded")
	}
	for _, p := range []string{bad, bad + ".state", bad + restorejournal.LevelsSuffix} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind after a failed --into: %v", p, err)
		}
	}
}

// TestCheckpointEscapeKeepsOwedHooks (05-storage §5S.3.12 "In place",
// §5S.10; DATA-7, DATA8-6): a checkpoint restore boot whose migrations
// failed leaves a KindCheckpoint journal at failed; the binary that failed
// it is refused, and the previous binary's restore-offline --checkpoint
// escapes it: the failed store set aside to failed/, the checkpoint placed,
// the journal finished at applied_offline with HooksOwed, the in-flight
// file gone and the record consumed; the previous build then boots.
func TestCheckpointEscapeKeepsOwedHooks(t *testing.T) {
	h := newSwapHost(t)
	h.close(h.mustBoot(h.config(nil)))
	const id = "r-checkpoint"
	h.stage(id, restorejournal.KindCheckpoint, h.stagedCopy(id, "robin"))
	if _, err := h.boot(h.config(nil, brokenStep)); !errors.Is(err, ErrCheckpointRestoreFailedHere) {
		t.Fatalf("restore boot with a failing step = %v", err)
	}
	name := h.pointer()
	if _, err := RestoreOffline(h.ctx, h.config(nil), RestoreOfflineRequest{Checkpoint: name}); !errors.Is(err, ErrCheckpointRestoreFailedHere) {
		t.Fatalf("the failing binary's escape = %v, want ErrCheckpointRestoreFailedHere", err)
	}
	prev := bootseam.With(t.Context(), &bootseam.Seams{BuildID: "batcomputer-r0"})
	res, err := RestoreOffline(prev, h.config(nil), RestoreOfflineRequest{Checkpoint: name})
	if err != nil {
		t.Fatalf("the previous binary's escape = %v", err)
	}
	if !res.Escaped || res.ID != id {
		t.Fatalf("result %+v, want the escape of %s", res, id)
	}
	recs := h.journal(id)
	last := recs[len(recs)-1]
	if last.Step != restoreswap.StepAppliedOffline || !last.HooksOwed {
		t.Fatalf("journal ends %+v, want applied_offline with HooksOwed", last)
	}
	if h.slot().Busy() {
		t.Fatalf("slot after the escape = %+v, want empty", h.slot())
	}
	if st, _, err := checkpointStatus(h.db, name); err != nil || st != "consumed" {
		t.Fatalf("record status %q (%v), want consumed", st, err)
	}
	h.ctx = prev
	if got := h.readCave(); got != "robin" {
		t.Fatalf("cave after the escape = %q, want the restored checkpoint's robin", got)
	}
}

// resetProbe is a reset hook that records what the kernel hands it.
type resetProbe struct {
	mu          sync.Mutex
	plan        storage.ResetPlan
	planErr     error
	reqs        []storage.ResetRequest
	withStorage []bool
	kept        []func(string) bool
	runs        []storage.ResetRun
	run         func(ctx context.Context, rr storage.ResetRun) error
}

func (p *resetProbe) hook() *storage.ResetHook {
	return &storage.ResetHook{
		Plan: func(ctx context.Context, req storage.ResetRequest) (storage.ResetPlan, error) {
			p.mu.Lock()
			defer p.mu.Unlock()
			_, ok := module.ResetPlanStorage(ctx)
			kept, _ := module.ResetPlanKept(ctx)
			p.reqs, p.withStorage, p.kept = append(p.reqs, req), append(p.withStorage, ok), append(p.kept, kept)
			return p.plan, p.planErr
		},
		Run: func(ctx context.Context, rr storage.ResetRun) error {
			p.mu.Lock()
			p.runs = append(p.runs, rr)
			run := p.run
			p.mu.Unlock()
			if run != nil {
				return run(ctx, rr)
			}
			return rr.Done(ctx, nil)
		},
	}
}

func (p *resetProbe) runCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.runs)
}

// hookConfig is the host's config with alfred declaring p's reset hook.
func (h *swapHost) hookConfig(p *resetProbe, migs ...storage.Migration) Config {
	c := h.config(nil, migs...)
	c.Modules = []module.Module{c.Modules[0].(testModule).with(func(m *module.Manifest) { m.ResetHook = p.hook() })}
	return c
}

// library is a library root with three top-level entries and an Open
// Library dump directory, both beside the state directory.
func (h *swapHost) library() (root, dump string) {
	h.t.Helper()
	base := filepath.Dir(h.state)
	root, dump = filepath.Join(base, "library"), filepath.Join(base, "openlibrary")
	for _, d := range []string{filepath.Join(root, "Year One"), filepath.Join(root, "The Long Halloween"), dump} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			h.t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "Hush.m4b"), []byte("hush"), 0o600); err != nil {
		h.t.Fatal(err)
	}
	return root, dump
}

// TestOfflineResetStagesIntent (05-storage §5S.3.15 "Stage"; 06 §6.4.6):
// with the service stopped, storage reset --kind factory stages a
// ResetFactory in the one slot carrying the hook's arguments and paths and
// the live install ID, BeforeSwap runs once, and the live store is
// untouched until the next boot, which completes the reset as a fresh
// install. Each refusal stages nothing: a dangerous root with the wipe
// requested (the hook's error), an unconfirmed or mismatched wipe, a hook
// path holding the library root without the wipe, a running service, a
// busy slot, and a failing BeforeSwap.
func TestOfflineResetStagesIntent(t *testing.T) {
	setup := func(t *testing.T) (*swapHost, *resetProbe, string, string) {
		h := newSwapHost(t)
		h.close(h.mustBoot(h.config(nil)))
		root, dump := h.library()
		p := &resetProbe{plan: storage.ResetPlan{Hook: []byte(`{"dump":"` + dump + `"}`), Paths: []string{dump},
			LibraryRoot: root, LibraryFiles: 3}}
		return h, p, root, dump
	}
	factory := storage.ResetRequest{Kind: restorejournal.ResetFactory, Confirm: storage.ResetConfirm}
	t.Run("staged", func(t *testing.T) {
		h, p, root, dump := setup(t)
		var swaps []string
		c := h.hookConfig(p)
		c.BeforeSwap = func(path string) error { swaps = append(swaps, path); return nil }
		staged, err := StageResetOffline(h.ctx, c, "alfred", factory)
		if err != nil {
			t.Fatalf("stage = %v", err)
		}
		st := h.slot()
		if st.Pending == nil || st.Pending.ID != staged.ID || st.Pending.Kind != restorejournal.KindReset || st.Pending.Reset != restorejournal.ResetFactory {
			t.Fatalf("slot = %+v, want the factory reset %s pending", st, staged.ID)
		}
		raw, err := os.ReadFile(restorejournal.PendingPath(h.state))
		if err != nil {
			t.Fatal(err)
		}
		var in restorejournal.Intent
		if err := json.Unmarshal(raw, &in); err != nil {
			t.Fatal(err)
		}
		if string(in.HookArgs) != string(p.plan.Hook) || !slices.Equal(in.HookPaths, []string{dump}) || in.InstallID == "" || in.DeleteLibraryFiles {
			t.Fatalf("intent %+v, want the plan's arguments and paths, the live install ID and no wipe", in)
		}
		if !slices.Equal(swaps, []string{h.db}) {
			t.Fatalf("BeforeSwap calls %v, want one with the store path", swaps)
		}
		// Plan had the confinement's rule (4): the store, its parent, a
		// sidecar, the state directory and the swap's restore/ are kept; the
		// library, the dump and a file inside the state directory are not.
		if len(p.kept) != 1 || p.kept[0] == nil {
			t.Fatalf("Plan saw kept %v, want the reset's rule (4)", p.kept)
		}
		for _, k := range []string{h.db, filepath.Dir(h.db), h.db + restorejournal.CoreSidecarSuffixes()[0], h.state,
			restorejournal.Dir(h.state)} {
			if !p.kept[0](k) {
				t.Errorf("Plan's kept(%s) = false, want true", k)
			}
		}
		for _, k := range []string{root, filepath.Join(root, "Hush.m4b"), dump, filepath.Join(h.state, "riddle")} {
			if p.kept[0](k) {
				t.Errorf("Plan's kept(%s) = true, want false", k)
			}
		}
		if p.withStorage[0] != true {
			t.Fatal("Plan ran without its read-only storage")
		}
		if _, err := os.Stat(filepath.Join(root, "Hush.m4b")); err != nil {
			t.Fatalf("staging touched the library: %v", err)
		}
		if _, err := os.Stat(restorejournal.SetAsideDest(h.state, h.db, staged.ID, h.db)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the store was set aside at staging: %v", err)
		}
		k := h.mustBoot(h.hookConfig(p))
		if !k.store.opened.Fresh() || k.store.opened.InstallID() == in.InstallID {
			t.Fatal("the next boot did not apply the reset as a fresh install with a new install ID")
		}
		k.runResetHook(h.ctx)
		h.close(k)
		if got := stepsOf(h.journal(staged.ID)); got[len(got)-1] != restoreswap.StepHookDone {
			t.Fatalf("journal %v, want hook_done after the reset boot", got)
		}
	})
	refusals := []struct {
		name  string
		req   func(root string) storage.ResetRequest
		probe func(p *resetProbe)
		hold  bool
		busy  bool
		swap  error
		want  error
	}{
		{name: "dangerous root", req: func(root string) storage.ResetRequest {
			r := factory
			r.DeleteLibraryFiles, r.LibraryRoot, r.LibraryFileCount = true, root, 3
			return r
		}, probe: func(p *resetProbe) {
			p.planErr = errors.Join(storage.ErrResetRefused, errors.New("the library root is /"))
		},
			want: storage.ErrResetRefused},
		{name: "wipe with no library root", req: func(string) storage.ResetRequest {
			r := factory
			r.DeleteLibraryFiles = true
			return r
		}, probe: func(p *resetProbe) { p.plan.LibraryRoot, p.plan.LibraryFiles = "", 0 },
			want: storage.ErrResetRequestInvalid},
		{name: "wipe unconfirmed", req: func(string) storage.ResetRequest {
			r := factory
			r.DeleteLibraryFiles = true
			return r
		}, want: storage.ErrLibraryConfirmRequired},
		{name: "wipe count differs", req: func(root string) storage.ResetRequest {
			r := factory
			r.DeleteLibraryFiles, r.LibraryRoot, r.LibraryFileCount = true, root, 2
			return r
		}, want: storage.ErrLibraryConfirmRequired},
		{name: "wipe root differs", req: func(root string) storage.ResetRequest {
			r := factory
			r.DeleteLibraryFiles, r.LibraryRoot, r.LibraryFileCount = true, filepath.Join(filepath.Dir(root), "blackgate"), 3
			return r
		}, want: storage.ErrLibraryConfirmRequired},
		{name: "hook path holds the root without the wipe", req: func(string) storage.ResetRequest { return factory },
			probe: func(p *resetProbe) { p.plan.Paths = append(p.plan.Paths, filepath.Dir(p.plan.LibraryRoot)) },
			want:  storage.ErrResetRequestInvalid},
		{name: "running service", req: func(string) storage.ResetRequest { return factory }, hold: true, want: ErrStoreHeld},
		{name: "busy slot", req: func(string) storage.ResetRequest { return factory }, busy: true, want: restorejournal.ErrSwapPending},
		{name: "BeforeSwap fails", req: func(string) storage.ResetRequest { return factory }, swap: errors.New("graph busy")},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			h, p, root, _ := setup(t)
			if tc.probe != nil {
				tc.probe(p)
			}
			c := h.hookConfig(p)
			if tc.swap != nil {
				c.BeforeSwap = func(string) error { return tc.swap }
			}
			if tc.hold {
				k := h.mustBoot(h.config(nil))
				defer h.close(k)
			}
			if tc.busy {
				h.stage("r-busy", restorejournal.KindCheckpoint, h.stagedCopy("r-busy", "robin"))
			}
			_, err := StageResetOffline(h.ctx, c, "alfred", tc.req(root))
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) || (tc.swap != nil && !errors.Is(err, tc.swap)) {
				t.Fatalf("stage = %v, want %v", err, errors.Join(tc.want, tc.swap))
			}
			if st := h.slot(); tc.busy {
				if st.Pending == nil || st.Pending.ID != "r-busy" {
					t.Fatalf("slot = %+v, want only the busy restore", st)
				}
				if len(p.reqs) != 0 {
					t.Fatal("Plan read the store while a swap was staged")
				}
			} else if st.Busy() {
				t.Fatalf("slot = %+v, want nothing staged", st)
			}
		})
	}
	t.Run("confirm mismatch names the fresh values", func(t *testing.T) {
		h, p, root, _ := setup(t)
		r := factory
		r.DeleteLibraryFiles = true
		_, err := StageResetOffline(h.ctx, h.hookConfig(p), "alfred", r)
		lce, ok := errors.AsType[*storage.LibraryConfirmError](err)
		if !ok || lce.Root != root || lce.Files != 3 {
			t.Fatalf("error %v, want a LibraryConfirmError naming %s and 3", err, root)
		}
	})
}

// TestResetRequestFlowsUnchanged (05-storage §5S.3.15, the offline half;
// R9-6): the one storage.ResetRequest reaches Plan unchanged from the
// offline reset.
func TestResetRequestFlowsUnchanged(t *testing.T) {
	h := newSwapHost(t)
	h.close(h.mustBoot(h.config(nil)))
	root, _ := h.library()
	p := &resetProbe{plan: storage.ResetPlan{LibraryRoot: root, LibraryFiles: 3, Paths: []string{root}}}
	req := storage.ResetRequest{Kind: restorejournal.ResetFactory, Confirm: storage.ResetConfirm,
		DeleteLibraryFiles: true, LibraryRoot: root, LibraryFileCount: 3}
	if _, err := StageResetOffline(h.ctx, h.hookConfig(p), "alfred", req); err != nil {
		t.Fatal(err)
	}
	if len(p.reqs) != 1 || !reflect.DeepEqual(p.reqs[0], req) {
		t.Fatalf("Plan received %+v, want exactly %+v", p.reqs, req)
	}
}

// TestFactoryResetNativeAppCompletes (05-storage §5S.3.15; COR8-4, COR10-9
// (a)): an app with no reset hook stages a factory reset offline and the
// next boot finishes it itself, with no restore_inflight left; a request
// with delete_library_files is refused with ErrResetRequestInvalid and
// nothing staged.
func TestFactoryResetNativeAppCompletes(t *testing.T) {
	h := newSwapHost(t)
	h.close(h.mustBoot(h.config(nil)))
	req := storage.ResetRequest{Kind: restorejournal.ResetFactory, Confirm: storage.ResetConfirm, DeleteLibraryFiles: true,
		LibraryRoot: "/", LibraryFileCount: 1}
	if _, err := StageResetOffline(h.ctx, h.config(nil), "alfred", req); !errors.Is(err, storage.ErrResetRequestInvalid) ||
		!strings.Contains(err.Error(), "declares no reset hook") {
		t.Fatalf("wipe without a hook = %v, want ErrResetRequestInvalid naming the missing hook", err)
	}
	h.assertNothingStaged()
	req.DeleteLibraryFiles, req.LibraryRoot, req.LibraryFileCount = false, "", 0
	staged, err := StageResetOffline(h.ctx, h.config(nil), "alfred", req)
	if err != nil {
		t.Fatal(err)
	}
	h.close(h.mustBoot(h.config(nil)))
	if got := stepsOf(h.journal(staged.ID)); got[len(got)-1] != restoreswap.StepHookDone {
		t.Fatalf("journal %v, want hook_done after the reset boot", got)
	}
	if h.slot().Busy() {
		t.Fatalf("slot after the reset boot = %+v, want no restore_inflight", h.slot())
	}
}

// TestFactoryResetUnderClaims (05-storage §5S.3.15 "Tests"): under strict
// claims (the default) a staged factory reset boots as a fresh install
// with a new install ID, the old store set aside, and no out-of-claim
// access refuses the boot.
func TestFactoryResetUnderClaims(t *testing.T) {
	h := newSwapHost(t)
	k := h.mustBoot(h.config(nil))
	before := k.store.opened.InstallID()
	h.close(k)
	p := &resetProbe{}
	staged, err := StageResetOffline(h.ctx, h.hookConfig(p), "alfred",
		storage.ResetRequest{Kind: restorejournal.ResetFactory, Confirm: storage.ResetConfirm})
	if err != nil {
		t.Fatal(err)
	}
	c := h.hookConfig(p)
	if c.StorageClaimsShadow {
		t.Fatal("the test needs strict claims")
	}
	k = h.mustBoot(c)
	after := k.store.opened.InstallID()
	if !k.store.opened.Fresh() || after == "" || after == before {
		t.Fatalf("reset boot fresh=%v install %q (was %q), want a fresh install with a new install ID", k.store.opened.Fresh(), after, before)
	}
	k.runResetHook(h.ctx)
	h.close(k)
	if _, err := os.Stat(restorejournal.SetAsideDest(h.state, h.db, staged.ID, h.db)); err != nil {
		t.Fatalf("the old store was not set aside: %v", err)
	}
}

// TestPostResetHookRerunsUntilDone (05-storage §5S.3.15; COR8-4): a
// factory reset of an app with a hook journals applied at P4a and leaves
// hook_done to the hook at Run step (2); a crash before the hook's Done
// (here: the boot ends before step (2)) leaves the journal at applied with
// the in-flight file, and every later boot reruns the hook until Done
// journals hook_done; the hook is idempotent over what an earlier run
// already removed. The hook gets the intent's arguments and a confined
// filesystem.
func TestPostResetHookRerunsUntilDone(t *testing.T) {
	h := newSwapHost(t)
	h.close(h.mustBoot(h.config(nil)))
	_, dump := h.library()
	if err := os.WriteFile(filepath.Join(dump, "authors.jsonl"), []byte("penguin"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &resetProbe{plan: storage.ResetPlan{Hook: []byte("dump"), Paths: []string{dump}}}
	var outside error
	p.run = func(ctx context.Context, rr storage.ResetRun) error {
		outside = rr.Files.RemoveAll(filepath.Join(filepath.Dir(h.state), "library"))
		if err := rr.Files.RemoveAll(dump); err != nil {
			return err
		}
		return rr.Done(ctx, nil)
	}
	staged, err := StageResetOffline(h.ctx, h.hookConfig(p), "alfred",
		storage.ResetRequest{Kind: restorejournal.ResetFactory, Confirm: storage.ResetConfirm})
	if err != nil {
		t.Fatal(err)
	}
	h.close(h.mustBoot(h.hookConfig(p))) // the crash: no Run step (2)
	if got := stepsOf(h.journal(staged.ID)); got[len(got)-1] != restoreswap.StepApplied {
		t.Fatalf("journal %v, want applied: hook_done is the hook's", got)
	}
	if st := h.slot(); len(st.InFlight) != 1 {
		t.Fatalf("slot = %+v, want the reset in flight until hook_done", st)
	}
	if err := os.RemoveAll(filepath.Join(dump, "authors.jsonl")); err != nil { // half of a first run
		t.Fatal(err)
	}
	k := h.mustBoot(h.hookConfig(p))
	if p.runCount() != 0 {
		t.Fatal("the hook ran in New, before Run step (2)")
	}
	k.runResetHook(h.ctx)
	h.close(k)
	if p.runCount() != 1 {
		t.Fatalf("hook runs %d, want 1", p.runCount())
	}
	if rr := p.runs[0]; string(rr.Args) != "dump" || rr.ID != staged.ID {
		t.Fatalf("run %+v, want the intent's id and arguments", rr)
	}
	if !errors.Is(outside, storage.ErrConfined) {
		t.Fatalf("removing the library without the wipe = %v, want ErrConfined", outside)
	}
	if _, err := os.Stat(dump); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dump directory after the hook: %v, want removed", err)
	}
	if got := stepsOf(h.journal(staged.ID)); got[len(got)-1] != restoreswap.StepHookDone {
		t.Fatalf("journal %v, want hook_done", got)
	}
	if h.slot().Busy() {
		t.Fatalf("slot = %+v, want empty after hook_done", h.slot())
	}
	h.close(h.mustBoot(h.hookConfig(p)))
	if p.runCount() != 1 {
		t.Fatalf("hook runs %d after a later boot, want it not rerun once done", p.runCount())
	}
}

// TestResetHookNeverFailsBoot (05-storage §5S.3.15 rule (1); OPS8-5): a
// reset hook that returns an error without Done, panics, or outlives the
// reset hook timeout does not stop the boot: the kernel journals hook_done
// with the failure, deletes the in-flight file and logs
// factory_reset_cleanup_incomplete.
func TestResetHookNeverFailsBoot(t *testing.T) {
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	for name, run := range map[string]func(context.Context, storage.ResetRun) error{
		"error":   func(context.Context, storage.ResetRun) error { return errors.New("the joker took the dump") },
		"panic":   func(context.Context, storage.ResetRun) error { panic("two-face flipped") },
		"timeout": func(context.Context, storage.ResetRun) error { <-stuck; return nil },
	} {
		t.Run(name, func(t *testing.T) {
			h := newSwapHost(t)
			h.close(h.mustBoot(h.config(nil)))
			p := &resetProbe{run: run}
			staged, err := StageResetOffline(h.ctx, h.hookConfig(p), "alfred",
				storage.ResetRequest{Kind: restorejournal.ResetFactory, Confirm: storage.ResetConfirm})
			if err != nil {
				t.Fatal(err)
			}
			c := h.hookConfig(p)
			capture := logcapture.New(t)
			c.Logger, c.ResetHookTimeout = capture.Logger(), 50*time.Millisecond
			k := h.mustBoot(c)
			k.runResetHook(h.ctx)
			h.close(k)
			recs := h.journal(staged.ID)
			last := recs[len(recs)-1]
			if last.Step != restoreswap.StepHookDone || len(last.Failed) == 0 {
				t.Fatalf("journal ends %+v, want hook_done recording the failure", last)
			}
			if h.slot().Busy() {
				t.Fatalf("slot = %+v, want the in-flight file deleted", h.slot())
			}
			found := false
			for _, r := range capture.Records() {
				r.Attrs(func(a slog.Attr) bool {
					if a.Value.String() == cleanupIncomplete {
						found = true
					}
					return true
				})
			}
			if !found {
				t.Fatalf("no %s in the log: %v", cleanupIncomplete, capture.Messages())
			}
		})
	}
}

// TestPostResetHookSkipsKeptPathsWithHook is the declared-hook half of the
// spec's TestPostResetHookSkipsKeptPaths (05-storage §5S.3.15 rule (4),
// "Hook confinement"); the no-hook half, where the kernel's own completion
// skips them, is R1-21's function of that name in swap_test.go, and
// `-run TestPostResetHookSkipsKeptPaths` runs both. A kept path the hook
// tries to remove (<db parent>, the state directory, the store's set-aside
// destination) is refused with ErrKeptPath, stays, and is listed in
// hook_done's kept; Done called again, and from several goroutines at
// once, journals hook_done exactly once with what the first call reported.
func TestPostResetHookSkipsKeptPathsWithHook(t *testing.T) {
	h := newSwapHost(t)
	h.close(h.mustBoot(h.config(nil)))
	parent := filepath.Dir(h.db)
	var keptErrs []error
	var want []string
	p := &resetProbe{run: func(ctx context.Context, rr storage.ResetRun) error {
		want = []string{parent, h.state, restorejournal.SetAsideDest(h.state, h.db, rr.ID, h.db)}
		for _, k := range want {
			keptErrs = append(keptErrs, rr.Files.RemoveAll(k))
		}
		if err := rr.Done(ctx, []string{"scarecrow"}); err != nil {
			return err
		}
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for range 8 {
			wg.Go(func() { errs <- rr.Done(ctx, []string{"riddler"}) })
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				return err
			}
		}
		return nil
	}}
	staged, err := StageResetOffline(h.ctx, h.hookConfig(p), "alfred",
		storage.ResetRequest{Kind: restorejournal.ResetFactory, Confirm: storage.ResetConfirm})
	if err != nil {
		t.Fatal(err)
	}
	k := h.mustBoot(h.hookConfig(p))
	k.runResetHook(h.ctx)
	h.close(k)
	for i, err := range keptErrs {
		if !errors.Is(err, storage.ErrKeptPath) {
			t.Errorf("removing %s = %v, want ErrKeptPath", want[i], err)
		}
	}
	for _, k := range want {
		if _, err := os.Stat(k); err != nil {
			t.Errorf("%s after the hook: %v", k, err)
		}
	}
	var done []restorejournal.Record
	for _, r := range h.journal(staged.ID) {
		if r.Step == restoreswap.StepHookDone {
			done = append(done, r)
		}
	}
	if len(done) != 1 {
		t.Fatalf("hook_done records %+v, want exactly one", done)
	}
	if !slices.Equal(done[0].Failed, []string{"scarecrow"}) || !slices.Equal(done[0].Kept, want) {
		t.Fatalf("hook_done %+v, want failed [scarecrow] and kept %v", done[0], want)
	}
	if h.slot().Busy() {
		t.Fatalf("slot = %+v, want empty after hook_done", h.slot())
	}
}

// TestResetHookRunsBeforeModuleHooks (06 §6.5.2 start step (2); COR8-8):
// Run runs the reset hook after the served write and before the first
// module start hook and the listener.
func TestResetHookRunsBeforeModuleHooks(t *testing.T) {
	h := newSwapHost(t)
	h.close(h.mustBoot(h.config(nil)))
	rec := &recorder{}
	p := &resetProbe{run: func(ctx context.Context, rr storage.ResetRun) error { rec.add("reset:run"); return rr.Done(ctx, nil) }}
	if _, err := StageResetOffline(h.ctx, h.hookConfig(p), "alfred",
		storage.ResetRequest{Kind: restorejournal.ResetFactory, Confirm: storage.ResetConfirm}); err != nil {
		t.Fatal(err)
	}
	hc := h.hookConfig(p)
	c := supervised(t, rec, hc.Modules[0], hooked("bruce", rec))
	c.StoragePath, c.StateDir = h.db, h.state
	k := h.mustBoot(c)
	ctx, cancel := context.WithCancel(h.ctx)
	done := make(chan error, 1)
	go func() { done <- k.Run(ctx) }()
	for deadline := time.Now().Add(10 * time.Second); rec.index("ready") < 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no readiness: %v", rec.list())
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	reset, start, listen := rec.index("reset:run"), rec.index("start:bruce.cowl"), slices.IndexFunc(rec.list(), func(e string) bool { return strings.HasPrefix(e, "listen:") })
	if reset < 0 || start < 0 || listen < 0 || reset > start || reset > listen {
		t.Fatalf("events %v, want reset:run before the first start hook and the listener", rec.list())
	}
}
