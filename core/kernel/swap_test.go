// file: core/kernel/swap_test.go
// version: 1.0.7
// guid: e61f321a-b5df-40c7-9f12-074b36c11a97
// last-edited: 2026-11-08

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/cockroachdb/pebble/v2/vfs/errorfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/bootseam/bootseamtest"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/storage/keys"
	"github.com/falkcorp/mediacore/storage/restorejournal"
	"github.com/falkcorp/mediacore/storage/restorejournal/restorejournaltest"
)

// swapBuild is the binary ID the swap tests hand the kernel through
// bootseam, so a stager and a boot agree without hashing the test binary.
const swapBuild = "batcomputer-r1"

// swapHost is a state directory and a store under it on the real
// filesystem (the store generation reads the directory's device and inode,
// which no in-memory filesystem has).
type swapHost struct {
	t     *testing.T
	state string
	db    string
	ctx   context.Context
}

func newSwapHost(t *testing.T) *swapHost {
	state := filepath.Join(t.TempDir(), "state")
	return &swapHost{t: t, state: state, db: filepath.Join(state, "storage", "main.pebble"),
		ctx: bootseam.With(t.Context(), &bootseam.Seams{BuildID: swapBuild})}
}

// config is a Config over the host's store and state directory; deps
// receives the module's Deps.
func (h *swapHost) config(deps *module.Deps, migs ...storage.Migration) Config {
	reg := func(r *module.Registrar) error {
		if deps != nil {
			*deps = r.Deps()
		}
		return nil
	}
	c := testConfig(h.t, storageMod("alfred", reg, append([]storage.Migration{seedStep(1, "cave")}, migs...)...))
	c.StoragePath, c.StateDir = h.db, h.state
	return c
}

// boot boots c and closes it when the test ends.
func (h *swapHost) boot(c Config) (*Kernel, error) {
	k, err := New(h.ctx, c)
	if err != nil {
		return nil, err
	}
	h.t.Cleanup(func() { _ = k.closeCore(context.Background(), time.Now().Add(defaultShutdownBudget)) })
	return k, nil
}

func (h *swapHost) mustBoot(c Config) *Kernel {
	h.t.Helper()
	k, err := h.boot(c)
	if err != nil {
		h.t.Fatalf("boot: %v", err)
	}
	return k
}

func (h *swapHost) close(k *Kernel) {
	h.t.Helper()
	if err := k.closeCore(context.Background(), time.Now().Add(defaultShutdownBudget)); err != nil {
		h.t.Fatal(err)
	}
}

// generation is the store generation c/storage/epoch (05-storage §5S.3.7).
func generation(t *testing.T, k *Kernel) uint64 {
	t.Helper()
	ks, ok := k.store.db.Keyspace("core/storage")
	if !ok {
		t.Fatal("no core/storage keyspace")
	}
	raw, err := ks.Get(t.Context(), keys.Join(keys.Str("epoch")))
	if err != nil {
		t.Fatal(err)
	}
	rec, err := codec.JSON[struct {
		Epoch uint64 `json:"epoch"`
	}](1).Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return rec.Epoch
}

// copyTree copies the closed store directory src to dst.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		to := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(to, 0o700)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		return errors.Join(err, out.Close())
	})
	if err != nil {
		t.Fatal(err)
	}
}

// stage stages a restore of kind placing the store at staged over the live
// one, as a stager would.
func (h *swapHost) stage(id string, kind restorejournal.Kind, staged string) {
	h.t.Helper()
	s, err := restoreswap.New(restoreswap.Options{StateDir: h.state, BuildID: swapBuild})
	if err != nil {
		h.t.Fatal(err)
	}
	in := restorejournal.Intent{ID: id, Kind: kind, DBPath: h.db, Targets: []restorejournal.Target{{From: staged, To: h.db}},
		StagedByBuild: swapBuild, ExpiresAt: time.Now().Add(time.Hour), RequestedBy: "alfred", RequestedAt: time.Now()}
	if err := s.Stage(h.ctx, in); err != nil {
		h.t.Fatal(err)
	}
}

// resetStager makes the host's later boots, and the swapper it returns for
// staging, read every path in dirs as on a second filesystem
// (restorejournaltest.OtherDevice through bootseam.Seams.Probe). A reset's delete
// entry on the DB's filesystem is refused at Stage and at P0a's step a
// (05-storage §5S.3.15), and the host has one filesystem.
func (h *swapHost) resetStager(dirs ...string) *restoreswap.Swapper {
	h.t.Helper()
	probe := restorejournaltest.OtherDevice{Probe: restorejournal.OSProbe(), Dirs: dirs}
	h.ctx = bootseam.With(h.t.Context(), &bootseam.Seams{BuildID: swapBuild, Probe: probe})
	s, err := restoreswap.New(restoreswap.Options{StateDir: h.state, BuildID: swapBuild, Probe: probe})
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}

// resetIntent is a reset of the host's store and its levels sidecar, each
// set aside under restore/<id>/previous/, deleting del.
func (h *swapHost) resetIntent(id string, reset restorejournal.ResetKind, del ...string) restorejournal.Intent {
	in := restorejournal.Intent{ID: id, Kind: restorejournal.KindReset, Reset: reset, DBPath: h.db, Delete: del,
		StagedByBuild: swapBuild, ExpiresAt: time.Now().Add(time.Hour), RequestedBy: "alfred", RequestedAt: time.Now()}
	for _, p := range []string{h.db, h.db + restorejournal.LevelsSuffix} {
		in.Targets = append(in.Targets, restorejournal.Target{From: p, To: restorejournal.SetAsideDest(h.state, h.db, id, p)})
	}
	return in
}

// stagedCopy is a copy of the live store under the swap's staging directory,
// rebooted on its own with "cave" set to value, so the restored store is
// recognisable.
func (h *swapHost) stagedCopy(id, value string) string {
	h.t.Helper()
	staged := filepath.Join(restorejournal.StagingDir(h.db, id), "main.pebble")
	copyTree(h.t, h.db, staged)
	var deps module.Deps
	c := h.config(&deps)
	c.StoragePath, c.StateDir = staged, ""
	k := h.mustBoot(c)
	if err := deps.Storage.Keyspace.Set(h.t.Context(), []byte("cave"), []byte(value)); err != nil {
		h.t.Fatal(err)
	}
	h.close(k)
	return staged
}

func (h *swapHost) journal(id string) []restorejournal.Record {
	h.t.Helper()
	recs, err := restoreswap.ReadJournal(vfs.Default, h.state, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return recs
}

func stepsOf(recs []restorejournal.Record) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.Step
	}
	return out
}

// TestRestoreBootWithoutHooksReachesApplied (05-storage §5S.10, R1): a
// restore boot of a build without P4d steps 2-3 journals machine 1's one
// edge opened → marker_written{HooksOwed} → applied, deletes the in-flight
// file, and serves the restored store.
// TestStateDirWithoutStoragePathRefused (R1-21 review m10): a direct
// kernel.Config with a state directory and no store is refused at New,
// before P0a reads the state directory. A swap applies to a store: run with
// none, a staged restore starts and waits at swapped, in flight, on every
// boot. Here the staged intent stays in the slot and the live store where
// it was.
func TestStateDirWithoutStoragePathRefused(t *testing.T) {
	h := newSwapHost(t)
	h.close(h.mustBoot(h.config(nil)))
	staged := h.stagedCopy("r-archive", "robin")
	h.stage("r-archive", restorejournal.KindArchive, staged)
	c := h.config(nil)
	c.StoragePath = ""
	if k, err := New(h.ctx, c); err == nil {
		h.close(k)
		t.Fatal("New booted a state directory without a store")
	} else if !strings.Contains(err.Error(), "StoragePath") {
		t.Fatalf("New = %v, want the refusal to name Config.StoragePath", err)
	}
	if _, err := os.Stat(restorejournal.PendingPath(h.state)); err != nil {
		t.Fatalf("the staged intent left the slot: %v", err)
	}
	if _, ok, err := restoreswap.InFlight(vfs.Default, h.state); err != nil || ok {
		t.Fatalf("in flight = %v, %v; want nothing started", ok, err)
	}
	if _, err := os.Stat(restorejournal.SetAsideDest(h.state, h.db, "r-archive", h.db)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the live store was set aside (%v)", err)
	}
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("the staged store left staging: %v", err)
	}
}

func TestRestoreBootWithoutHooksReachesApplied(t *testing.T) {
	h := newSwapHost(t)
	h.close(h.mustBoot(h.config(nil)))
	staged := h.stagedCopy("r-archive", "robin")
	h.stage("r-archive", restorejournal.KindArchive, staged)
	var deps module.Deps
	h.mustBoot(h.config(&deps))
	recs := h.journal("r-archive")
	want := []string{"staged", "rechecked", "previous_moved", "staged_moved", "sidecars", "swapped", "opened", "marker_written", "applied"}
	if got := stepsOf(recs); !slices.Equal(got, want) {
		t.Fatalf("journal %v, want %v", got, want)
	}
	if !recs[7].HooksOwed {
		t.Fatal("marker_written does not record HooksOwed")
	}
	if _, ok, err := restoreswap.InFlight(vfs.Default, h.state); err != nil || ok {
		t.Fatalf("in flight after applied: %v %v", ok, err)
	}
	if v, err := deps.Storage.Keyspace.Get(t.Context(), []byte("cave")); err != nil || string(v) != "robin" {
		t.Fatalf("cave = %q, %v; want the restored store's robin", v, err)
	}
}

// TestStoreGenerationBumpsOnSwap (05-storage §5S.10, the swap mechanism's
// part): a restore that swaps a copy of the store into place changes the
// store generation; a restore whose store P2 refuses is swapped back, and
// the original directory returns with its own generation.
func TestStoreGenerationBumpsOnSwap(t *testing.T) {
	h := newSwapHost(t)
	k := h.mustBoot(h.config(nil))
	gen := generation(t, k)
	h.close(k)

	h.stage("r-copy", restorejournal.KindArchive, h.stagedCopy("r-copy", "robin"))
	k = h.mustBoot(h.config(nil))
	if got := generation(t, k); got != gen+1 {
		t.Fatalf("generation after the swap = %d, want %d", got, gen+1)
	}
	h.close(k)

	// A store at a level this build does not serve: P2 refuses it.
	tooNew := filepath.Join(restorejournal.StagingDir(h.db, "r-too-new"), "main.pebble")
	h.tooNewStore(tooNew)
	h.stage("r-too-new", restorejournal.KindArchive, tooNew)
	if _, err := h.boot(h.config(nil)); !errors.Is(err, ErrSwappedBack) || !errors.Is(err, storage.ErrKeyspaceTooNew) {
		t.Fatalf("restore boot of a too-new store = %v, want ErrSwappedBack wrapping ErrKeyspaceTooNew", err)
	}
	if got := stepsOf(h.journal("r-too-new")); got[len(got)-1] != "swapped_back" {
		t.Fatalf("journal %v, want swapped_back", got)
	}
	k = h.mustBoot(h.config(nil))
	if got := generation(t, k); got != gen+1 {
		t.Fatalf("generation after the swap-back = %d, want the original directory's %d", got, gen+1)
	}
}

// tooNewStore writes a store at path at a level this build does not serve,
// so P2 refuses it.
func (h *swapHost) tooNewStore(path string) {
	h.t.Helper()
	c := h.config(nil)
	c.StoragePath, c.StateDir = path, ""
	c.Modules = []module.Module{storageMod("alfred", nil, seedStep(1, "cave"),
		storage.Migration{Version: 2, Name: "raise", Compat: storage.Breaking, RaisesLevel: 2,
			Up: func(context.Context, *storage.MigrationTx) error { return nil }}).
		with(func(m *module.Manifest) { m.Storage.Level = 2 })}
	h.close(h.mustBoot(c))
}

// TestSwapBackOverUnopenableOriginal (05-backup §5B.3.4 step x; R1-21 review
// M2, probe 2 on the real filesystem): a restore whose store P2 refuses,
// over an original that cannot be opened, swaps back in that boot and
// frees the slot. Before the fix x5's sidecar rewrite failed on the
// original and every later boot halted at P0a in step x. After it the next
// boot meets the original as it was: an empty directory becomes a fresh
// install, and a store with a truncated MANIFEST fails P2 on its own, with
// nothing in flight, so another restore can be staged.
func TestSwapBackOverUnopenableOriginal(t *testing.T) {
	for _, original := range []string{"empty", "corrupt"} {
		t.Run(original, func(t *testing.T) {
			h := newSwapHost(t)
			switch original {
			case "empty":
				if err := os.MkdirAll(h.db, 0o700); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				h.close(h.mustBoot(h.config(nil)))
				ms, _ := filepath.Glob(filepath.Join(h.db, "MANIFEST-*"))
				for _, m := range ms {
					fi, err := os.Stat(m)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.Truncate(m, fi.Size()/2); err != nil {
						t.Fatal(err)
					}
				}
			}
			const id = "r-too-new"
			staged := filepath.Join(restorejournal.StagingDir(h.db, id), "main.pebble")
			h.tooNewStore(staged)
			h.stage(id, restorejournal.KindArchive, staged)
			if _, err := h.boot(h.config(nil)); !errors.Is(err, ErrSwappedBack) {
				t.Fatalf("restore boot = %v, want ErrSwappedBack", err)
			}
			if got := stepsOf(h.journal(id)); got[len(got)-1] != "swapped_back" {
				t.Fatalf("journal %v, want swapped_back", got)
			}
			if _, ok, err := restoreswap.InFlight(vfs.Default, h.state); err != nil || ok {
				t.Fatalf("in flight after the swap-back = %v, %v; want the slot free", ok, err)
			}
			_, err := h.boot(h.config(nil))
			switch {
			case original == "empty" && err != nil:
				t.Fatalf("the next boot over the empty original = %v, want a fresh install", err)
			case original == "corrupt" && (err == nil || errors.Is(err, ErrSwapInFlight) || errors.Is(err, ErrSwappedBack)):
				t.Fatalf("the next boot over the corrupt original = %v, want P2's own refusal of it", err)
			}
		})
	}
}

// TestCheckpointRestoreFailedKeepsInFlight (DATA7-12, DATA8-6): a
// KindCheckpoint restore whose migrations fail in P4b journals failed with
// this binary's ID instead of swapping back; this binary then refuses every
// boot, and the reader check refuses every opener but the failed-checkpoint
// escape.
func TestCheckpointRestoreFailedKeepsInFlight(t *testing.T) {
	h := newSwapHost(t)
	h.close(h.mustBoot(h.config(nil)))
	h.stage("r-checkpoint", restorejournal.KindCheckpoint, h.stagedCopy("r-checkpoint", "robin"))
	broken := storage.Migration{Version: 2, Name: "broken", Compat: storage.Additive,
		Up: func(context.Context, *storage.MigrationTx) error { return errors.New("bane broke it") }}
	if _, err := h.boot(h.config(nil, broken)); !errors.Is(err, ErrCheckpointRestoreFailedHere) {
		t.Fatalf("restore boot with a failing step = %v, want ErrCheckpointRestoreFailedHere", err)
	}
	if got := stepsOf(h.journal("r-checkpoint")); got[len(got)-1] != "failed" {
		t.Fatalf("journal %v, want failed", got)
	}
	if _, err := h.boot(h.config(nil, broken)); !errors.Is(err, ErrCheckpointRestoreFailedHere) {
		t.Fatalf("the same binary's next boot = %v, want ErrCheckpointRestoreFailedHere", err)
	}
	// What the previous binary's offline escape will read (05-storage
	// §5S.3.12): the swap is still in flight, a KindCheckpoint at failed.
	if id, kind, last, ok, err := restoreswap.InFlightJournal(vfs.Default, h.state); err != nil || !ok ||
		id != "r-checkpoint" || kind != restorejournal.KindCheckpoint || last != restoreswap.StepFailed {
		t.Fatalf("in flight = %q %q %q %v (%v), want r-checkpoint, a checkpoint restore at failed", id, kind, last, ok, err)
	}
}

// TestCheckpointRestoreBookkeepingFailureRetries (DATA7-12 as R1-21's
// review (f) reads it): only a migration step's own failure (a
// *storage.StepError) journals a KindCheckpoint restore failed. A P4b
// bookkeeping failure, here an ENOSPC on the finish sidecars, closes the
// store and halts the boot with nothing journaled: the journal stays at
// opened with its in-flight file, and the next boot of the same binary
// resumes the restore boot to applied.
func TestCheckpointRestoreBookkeepingFailureRetries(t *testing.T) {
	mem := vfs.NewCrashableMem()
	const state = "/gotham/state"
	db := state + "/storage/main.pebble"
	boot := func(fail func(bootseamtest.Event) error) error {
		rec := bootseamtest.New(mem, bootseamtest.Options{Fail: fail})
		seams := rec.Seams()
		seams.BuildID = swapBuild
		c := testConfig(t, storageMod("alfred", nil, seedStep(1, "cave")))
		c.StoragePath, c.StateDir = db, state
		k, err := New(bootseam.With(t.Context(), seams), c)
		if err != nil {
			return err
		}
		return k.closeCore(context.Background(), time.Now().Add(defaultShutdownBudget))
	}
	if err := boot(nil); err != nil {
		t.Fatal(err)
	}
	const id = "r-checkpoint"
	staged := filepath.Join(restorejournal.StagingDir(db, id), "main.pebble")
	if _, err := vfs.Clone(mem, mem, db, staged); err != nil {
		t.Fatal(err)
	}
	s, err := restoreswap.New(restoreswap.Options{FS: mem, StateDir: state, BuildID: swapBuild})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Stage(t.Context(), restorejournal.Intent{ID: id, Kind: restorejournal.KindCheckpoint, DBPath: db,
		Targets:       []restorejournal.Target{{From: staged, To: db}},
		StagedByBuild: swapBuild, ExpiresAt: time.Now().Add(time.Hour), RequestedBy: "alfred", RequestedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	hit := false
	err = boot(func(ev bootseamtest.Event) error {
		if !hit && ev.Label == "storage.finish.sidecars" && ev.Kind == errorfs.OpCreate {
			hit = true
			return syscall.ENOSPC
		}
		return nil
	})
	if !hit || !errors.Is(err, syscall.ENOSPC) || errors.Is(err, ErrCheckpointRestoreFailedHere) || errors.Is(err, ErrSwappedBack) {
		t.Fatalf("restore boot with a failing finish sidecar = %v (injected %v), want the ENOSPC alone", err, hit)
	}
	recs, err := restoreswap.ReadJournal(mem, state, id)
	if err != nil {
		t.Fatal(err)
	}
	if got := stepsOf(recs); got[len(got)-1] != "opened" {
		t.Fatalf("journal %v, want it left at opened", got)
	}
	if _, ok, err := restoreswap.InFlight(mem, state); err != nil || !ok {
		t.Fatalf("in flight = %v, %v; want the in-flight file kept", ok, err)
	}
	if err := boot(nil); err != nil {
		t.Fatalf("the same binary's next boot = %v, want it to finish the restore", err)
	}
	if recs, err = restoreswap.ReadJournal(mem, state, id); err != nil {
		t.Fatal(err)
	}
	if got := stepsOf(recs); got[len(got)-1] != "applied" {
		t.Fatalf("journal %v, want applied", got)
	}
}

// TestResetHookDoneAfterMigrationList (06 §6.5.1 P4d step 1, COR8-4): a
// reset boot with no reset hook journals applied after P4a's fresh install,
// and removes the delete entries and journals hook_done only after the
// migration list. A reset boot whose P4b fails leaves the journal at
// applied, the in-flight file and the delete entries in place, and the next
// boot finishes the reset after its own P4b.
func TestResetHookDoneAfterMigrationList(t *testing.T) {
	h := newSwapHost(t)
	h.close(h.mustBoot(h.config(nil)))
	aux := filepath.Join(h.state, "batcave-index")
	if err := os.MkdirAll(aux, 0o700); err != nil {
		t.Fatal(err)
	}
	const id = "z-reset"
	if err := h.resetStager(aux).Stage(h.ctx, h.resetIntent(id, restorejournal.ResetStore, aux)); err != nil {
		t.Fatal(err)
	}
	broken := storage.Migration{Version: 2, Name: "broken", Compat: storage.Additive,
		Up: func(context.Context, *storage.MigrationTx) error { return errors.New("bane broke it") }}
	if _, err := h.boot(h.config(nil, broken)); err == nil {
		t.Fatal("reset boot with a failing step succeeded")
	}
	if got := stepsOf(h.journal(id)); got[len(got)-1] != "applied" {
		t.Fatalf("after a failed P4b the journal is %v, want it at applied", got)
	}
	if _, ok, err := restoreswap.InFlight(vfs.Default, h.state); err != nil || !ok {
		t.Fatalf("in flight after a failed P4b = %v, %v; want the in-flight file kept", ok, err)
	}
	if _, err := os.Stat(aux); err != nil {
		t.Fatalf("a delete entry was removed before the migration list finished: %v", err)
	}
	h.mustBoot(h.config(nil))
	if got := stepsOf(h.journal(id)); got[len(got)-1] != "hook_done" {
		t.Fatalf("after the next boot the journal is %v, want it at hook_done", got)
	}
	if _, ok, err := restoreswap.InFlight(vfs.Default, h.state); err != nil || ok {
		t.Fatalf("in flight after hook_done = %v, %v", ok, err)
	}
	if _, err := os.Stat(aux); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the delete entry after hook_done: %v, want it removed", err)
	}
}

// TestP0aRejectsKeptDeleteEntries (05-storage §5S.3.15 rule (4); R1-21
// review M1, probe 1; verification review): a reset intent written past
// Stage that lists the DB's parent and the state directory among its delete
// entries is rejected at P0a's step a, as Stage refuses it. The journal
// says rejected, the store boots untouched with nothing set aside, and the
// auxiliary entry (on a second filesystem, so only the kept rule refuses
// the intent) stays.
func TestP0aRejectsKeptDeleteEntries(t *testing.T) {
	h := newSwapHost(t)
	h.close(h.mustBoot(h.config(nil)))
	aux := filepath.Join(filepath.Dir(h.state), "batcave-index")
	if err := os.MkdirAll(aux, 0o700); err != nil {
		t.Fatal(err)
	}
	const id = "z-kept"
	in := h.resetIntent(id, restorejournal.ResetFactory, filepath.Dir(h.db), h.state, aux)
	if err := h.resetStager(aux).Stage(h.ctx, in); !errors.Is(err, restorejournal.ErrDeleteRefused) {
		t.Fatalf("Stage = %v, want ErrDeleteRefused", err)
	}
	if err := os.MkdirAll(restorejournal.JournalDir(h.state, id), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := restorejournal.CreateIntent(h.state, in); err != nil {
		t.Fatal(err)
	}
	h.mustBoot(h.config(nil))
	if got := stepsOf(h.journal(id)); got[len(got)-1] != "rejected" {
		t.Fatalf("journal = %v, want the intent rejected at step a", got)
	}
	if _, err := os.Stat(filepath.Join(restorejournal.JournalDir(h.state, id), restorejournal.RejectedName)); err != nil {
		t.Fatalf("rejected.json: %v", err)
	}
	if _, err := os.Stat(restorejournal.SetAsideDest(h.state, h.db, id, h.db)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a rejected reset set the store aside: %v", err)
	}
	if ms, _ := filepath.Glob(filepath.Join(h.db, "MANIFEST-*")); len(ms) == 0 {
		t.Fatal("the live store is gone")
	}
	if _, err := os.Stat(aux); err != nil {
		t.Fatalf("a rejected reset removed its auxiliary entry: %v", err)
	}
}

// TestP0aRejectsADeleteEntryOnTheDBFilesystem (05-storage §5S.3.15 "Stage";
// R1-21 review M1; verification review): the kernel's P0a reads the
// operating system's probe, so step a refuses a reset written past Stage
// whose delete entry is on the DB's filesystem, which Stage's probe would
// have refused, and the entry stays.
func TestP0aRejectsADeleteEntryOnTheDBFilesystem(t *testing.T) {
	h := newSwapHost(t)
	h.close(h.mustBoot(h.config(nil)))
	aux := filepath.Join(filepath.Dir(h.state), "batcave-index")
	if err := os.MkdirAll(aux, 0o700); err != nil {
		t.Fatal(err)
	}
	const id = "z-same-fs"
	in := h.resetIntent(id, restorejournal.ResetFactory, aux)
	if err := os.MkdirAll(restorejournal.JournalDir(h.state, id), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := restorejournal.CreateIntent(h.state, in); err != nil {
		t.Fatal(err)
	}
	h.mustBoot(h.config(nil))
	recs := h.journal(id)
	if last := recs[len(recs)-1]; last.Step != "rejected" || !strings.Contains(last.Note, "on the DB's filesystem") {
		t.Fatalf("journal ends %+v, want step a's rejection of an entry on the DB's filesystem", last)
	}
	if _, err := os.Stat(aux); err != nil {
		t.Fatalf("a rejected reset removed its delete entry: %v", err)
	}
}

// TestPostResetHookSkipsKeptPaths (05-storage §5S.3.15 rule (4), the kernel's
// no-hook case; R1-21 review M1, probe 1): once the in-flight file exists
// nothing but the kernel's completion reads the delete entries again, so
// its own skip still guards them. A reset staged with one auxiliary entry
// fails its P4b and stays at applied, and its in-flight intent is then
// rewritten to list the DB's parent and the state directory as well. The
// next boot completes with both recorded as kept and neither removed: the
// set-aside original and the fresh store survive, and the auxiliary entry
// is removed.
func TestPostResetHookSkipsKeptPaths(t *testing.T) {
	h := newSwapHost(t)
	h.close(h.mustBoot(h.config(nil)))
	aux := filepath.Join(filepath.Dir(h.state), "batcave-index")
	if err := os.MkdirAll(aux, 0o700); err != nil {
		t.Fatal(err)
	}
	const id = "z-kept"
	if err := h.resetStager(aux).Stage(h.ctx, h.resetIntent(id, restorejournal.ResetFactory, aux)); err != nil {
		t.Fatal(err)
	}
	broken := storage.Migration{Version: 2, Name: "broken", Compat: storage.Additive,
		Up: func(context.Context, *storage.MigrationTx) error { return errors.New("bane broke it") }}
	if _, err := h.boot(h.config(nil, broken)); err == nil {
		t.Fatal("reset boot with a failing step succeeded")
	}
	if got := stepsOf(h.journal(id)); got[len(got)-1] != "applied" {
		t.Fatalf("after a failed P4b the journal is %v, want it at applied", got)
	}
	inflight := filepath.Join(restorejournal.JournalDir(h.state, id), restorejournal.InflightName)
	b, err := os.ReadFile(inflight)
	if err != nil {
		t.Fatal(err)
	}
	var in restorejournal.Intent
	if err := json.Unmarshal(b, &in); err != nil {
		t.Fatal(err)
	}
	in.Delete = []string{filepath.Dir(h.db), h.state, aux}
	if b, err = json.Marshal(in); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inflight, b, 0o600); err != nil {
		t.Fatal(err)
	}
	h.mustBoot(h.config(nil))
	recs := h.journal(id)
	last := recs[len(recs)-1]
	if want := []string{filepath.Dir(h.db), h.state}; last.Step != "hook_done" || !slices.Equal(last.Kept, want) || len(last.Failed) != 0 {
		t.Fatalf("hook_done = %+v, want kept %v and nothing failed", last, want)
	}
	prev := restorejournal.SetAsideDest(h.state, h.db, id, h.db)
	if ms, _ := filepath.Glob(filepath.Join(prev, "MANIFEST-*")); len(ms) == 0 {
		t.Fatalf("the set-aside original store %s is gone: the reset's undo was deleted", prev)
	}
	if ms, _ := filepath.Glob(filepath.Join(h.db, "MANIFEST-*")); len(ms) == 0 {
		t.Fatal("the fresh store is gone")
	}
	if _, err := os.Stat(aux); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the auxiliary delete entry: %v, want it removed", err)
	}
}

// TestDescribeAppliesNoSwap (COR2-13): Describe performs no I/O, so a staged
// intent is neither applied nor rejected: pending.json and the journal are
// as the stager left them.
func TestDescribeAppliesNoSwap(t *testing.T) {
	h := newSwapHost(t)
	h.close(h.mustBoot(h.config(nil)))
	h.stage("r-describe", restorejournal.KindArchive, h.stagedCopy("r-describe", "robin"))
	if _, err := Describe(h.ctx, h.config(nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(restorejournal.PendingPath(h.state)); err != nil {
		t.Fatalf("pending.json after Describe: %v", err)
	}
	if got := stepsOf(h.journal("r-describe")); !slices.Equal(got, []string{"staged"}) {
		t.Fatalf("journal after Describe %v, want [staged]", got)
	}
}
