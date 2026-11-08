// file: core/kernel/swap_test.go
// version: 1.0.0
// guid: e61f321a-b5df-40c7-9f12-074b36c11a97
// last-edited: 2026-11-07

package kernel

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/storage/keys"
	"github.com/falkcorp/mediacore/storage/restorejournal"
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
	c := h.config(nil)
	c.StoragePath, c.StateDir = tooNew, ""
	c.Modules = []module.Module{storageMod("alfred", nil, seedStep(1, "cave"),
		storage.Migration{Version: 2, Name: "raise", Compat: storage.Breaking, RaisesLevel: 2,
			Up: func(context.Context, *storage.MigrationTx) error { return nil }}).
		with(func(m *module.Manifest) { m.Storage.Level = 2 })}
	h.close(h.mustBoot(c))
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
	if err := ReaderCheck(h.state, ReaderCheckAllow{}); !errors.Is(err, ErrSwapInFlight) {
		t.Fatalf("reader check = %v, want ErrSwapInFlight", err)
	}
	if err := ReaderCheck(h.state, ReaderCheckAllow{FailedCheckpointEscape: true}); err != nil {
		t.Fatalf("reader check with the failed-checkpoint escape = %v, want nil", err)
	}
}

// TestReaderCheck (05-storage §5S.3.7): a pending intent passes; an
// in-flight one refuses with ErrSwapInFlight, the escape admitting only a
// KindCheckpoint journal at failed.
func TestReaderCheck(t *testing.T) {
	h := newSwapHost(t)
	if err := ReaderCheck(h.state, ReaderCheckAllow{}); err != nil {
		t.Fatalf("no state directory: %v", err)
	}
	h.close(h.mustBoot(h.config(nil)))
	h.stage("r-pending", restorejournal.KindArchive, h.stagedCopy("r-pending", "robin"))
	if err := ReaderCheck(h.state, ReaderCheckAllow{}); err != nil {
		t.Fatalf("a pending intent: %v, want nil", err)
	}
	// In flight: the swap took the slot, and a kill fell before step c.
	dir := restorejournal.JournalDir(h.state, "r-pending")
	if err := os.Rename(restorejournal.PendingPath(h.state), filepath.Join(dir, restorejournal.InflightName)); err != nil {
		t.Fatal(err)
	}
	for _, allow := range []ReaderCheckAllow{{}, {FailedCheckpointEscape: true}} {
		if err := ReaderCheck(h.state, allow); !errors.Is(err, ErrSwapInFlight) {
			t.Fatalf("an in-flight archive restore with %+v: %v, want ErrSwapInFlight", allow, err)
		}
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
