// file: core/coretest/crashmatrix/rewrite.go
// version: 1.0.0
// guid: 4d069b7e-b39b-4e8b-94d2-d991b032da3e
// last-edited: 2026-11-10

package crashmatrix

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/cockroachdb/pebble/v2/vfs/errorfs"

	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/storage/keys"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// SyntheticRewrite is M8's cell (05-storage §5S.3.13 "M8: the rewrite";
// 05-backup §5B.3.4 KindRewrite): a store under the state directory (the
// compiled default layout, whose set-aside is <state>/restore/<id>/previous)
// holding Rows rows in Owner's keyspace, booted the way F3-07's boot will
// run M8: restoreswap's P0a first, with the Swapper serving M8
// (Options.RewriteStep), then storage's DAG with M8 as the kernel's last
// data step (DataMigrationsConfig.Rewrite and Swapper). Its Check holds
// after a kill anywhere in M8: every row there, M8 done, the generation
// unchanged, no copy, no set-aside, no in-flight or staged intent left.
// Name coined by F3-04.
type SyntheticRewrite struct {
	Owner storage.Owner
	Spec  storage.Spec
	// Rows is the fixture's size; 0 = 400.
	Rows int
	// SwappedBack seeds a store whose first M8 swap failed and swapped back
	// (the copy's placement refused once), so the cell's boot is M8's one
	// restart from the compaction.
	SwappedBack bool
}

// The rewrite cell's paths on its in-memory filesystem.
const (
	RewriteStateDir = "/batcave/state"
	RewritePath     = RewriteStateDir + "/storage/main.pebble"
)

// RewriteID is the cell's M8 step ID for owner. Name coined by F3-04.
func RewriteID(owner storage.Owner) string { return string(owner) + ".rewrite" }

// RewriteCellLabels are the writers an M8 run reaches: storage's record,
// compaction, copy and reopen, restoreswap's staging and phase 4's
// deletion, and the shared swap steps of a KindRewrite swap. The engine's
// close before the swap (storage.LabelRewriteClose) writes only a WAL sync
// still owed, which M8's first run, after Sync commits only, never owes:
// withClose adds it for the run after a swap-back, whose close does write.
// Name coined by F3-04.
func RewriteCellLabels(withClose bool) []string {
	labels := storage.RewriteLabels()
	if !withClose {
		labels = slices.DeleteFunc(labels, func(l string) bool { return l == storage.LabelRewriteClose })
	}
	return append(labels, restoreswap.LabelRewriteStage, restoreswap.LabelRewriteDelete,
		"swap.journal.staged", "swap.slot.inflight", "swap.setaside.mkdir", "swap.setaside.mv", "swap.journal.previous_moved",
		"swap.place.mv", "swap.journal.staged_moved", "swap.sidecars", "swap.journal.sidecars", "swap.journal.swapped",
		"swap.journal.opened", "swap.journal.applied", "swap.slot.remove")
}

func (c SyntheticRewrite) defaults() SyntheticRewrite {
	if c.Rows == 0 {
		c.Rows = 400
	}
	return c
}

func (c SyntheticRewrite) owners() []storage.OwnerSpec {
	spec := c.Spec
	spec.Keyspace = true
	return []storage.OwnerSpec{{Owner: c.Owner, Spec: spec, Enabled: true}}
}

// rewriteRow is row n's key and value.
func rewriteRow(n int) (key, value []byte) {
	return fmt.Appendf(nil, "story/%05d", n), fmt.Appendf(nil, "Hush, chapter %d", n)
}

// Scenario is the cell: Seed writes the rows, Boot runs P0a and the DAG
// with M8, Check asserts the invariants above.
func (c SyntheticRewrite) Scenario() ChunkScenario {
	c = c.defaults()
	var gen uint64
	return ChunkScenario{
		Path:   RewritePath,
		Labels: RewriteCellLabels(c.SwappedBack),
		Seed: func(t testing.TB, fs vfs.FS) {
			gen = c.seed(t, fs)
			if c.SwappedBack {
				c.swapBackOnce(t, fs)
			}
		},
		Boot:          c.boot,
		Check:         func(t testing.TB, fs vfs.FS) { c.check(t, fs, gen) },
		NoChunkStarts: true,
	}
}

// seed writes the pre-M8 store and returns its generation.
func (c SyntheticRewrite) seed(t testing.TB, fs vfs.FS) uint64 {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{Path: RewritePath, FS: storage.FSOf(fs), Owners: c.owners(),
		Build: "batcomputer-1", FreeSpaceMargin: 1, Clock: Now})
	if err != nil {
		t.Fatalf("crashmatrix: rewrite seed open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		t.Fatalf("crashmatrix: rewrite seed boot: %v", errors.Join(err, o.Close()))
	}
	ks, _ := db.Keyspace(c.Owner)
	b := ks.NewBatch()
	for n := 1; n <= c.Rows; n++ {
		k, v := rewriteRow(n)
		if err := b.Set(k, v); err != nil {
			t.Fatalf("crashmatrix: rewrite seed row %d: %v", n, err)
		}
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatalf("crashmatrix: rewrite seed commit: %v", err)
	}
	gen := generationOf(t, db)
	if err := db.Close(); err != nil {
		t.Fatalf("crashmatrix: rewrite seed close: %v", err)
	}
	return gen
}

// swapBackOnce boots the M8 build over fs with the copy's placement into
// the store's path refused once, so its swap swaps back.
func (c SyntheticRewrite) swapBackOnce(t testing.TB, fs vfs.FS) {
	t.Helper()
	var hit atomic.Bool
	failing := errorfs.Wrap(fs, errorfs.InjectorFunc(func(op errorfs.Op) error {
		if op.Kind == errorfs.OpRename && filepath.Clean(op.Path) == storage.RewriteDir(RewritePath) && hit.CompareAndSwap(false, true) {
			return syscall.EIO
		}
		return nil
	}))
	if err := c.boot(context.Background(), failing, nil); !hit.Load() || !errors.Is(err, restoreswap.ErrSwappedBack) {
		t.Fatalf("crashmatrix: the seeded swap-back = %v (injected %t), want restoreswap.ErrSwappedBack", err, hit.Load())
	}
}

// boot is one boot of the M8 build over fs.
func (c SyntheticRewrite) boot(ctx context.Context, fs vfs.FS, migs []datamigrate.DataMigration) error {
	c = c.defaults()
	owners, id := c.owners(), RewriteID(c.Owner)
	s, err := restoreswap.New(restoreswap.Options{FS: fs, StateDir: RewriteStateDir, BuildID: "batcomputer-2", Clock: Now,
		Owners: owners, RewriteStep: id, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		return err
	}
	if _, err := s.RunP0a(ctx); err != nil {
		return fmt.Errorf("P0a: %w", err)
	}
	rw := storage.NewRewriteStep(c.Owner, id)
	dms, err := kernel.NewDataMigrations(kernel.DataMigrationsConfig{Build: "batcomputer-2", Logger: slog.New(slog.DiscardHandler),
		Rewrite: &rw, Swapper: s}, owners, migs...)
	if err != nil {
		return err
	}
	o, err := storage.Open(ctx, storage.OpenOptions{Path: RewritePath, FS: storage.FSOf(fs), Owners: owners,
		Build: "batcomputer-2", FreeSpaceMargin: 1, DataSteps: dms.Steps(), Clock: Now})
	if err != nil {
		return err
	}
	db, err := o.Prepare(ctx)
	if err != nil {
		return errors.Join(err, o.Close())
	}
	return errors.Join(db.Migrate(ctx), db.Close())
}

// check asserts M8's invariants on fs.
func (c SyntheticRewrite) check(t testing.TB, fs vfs.FS, gen uint64) {
	t.Helper()
	c = c.defaults()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{Path: RewritePath, FS: storage.FSOf(fs), Owners: c.owners(),
		Build: "batcomputer-2", ReadOnly: true, MustExist: true, Clock: Now})
	if err != nil {
		t.Fatalf("crashmatrix: rewrite check open: %v", err)
	}
	defer func() {
		if err := o.Close(); err != nil {
			t.Errorf("crashmatrix: rewrite check close: %v", err)
		}
	}()
	db, err := o.Offline()
	if err != nil {
		t.Fatalf("crashmatrix: rewrite check offline: %v", err)
	}
	ks, _ := db.Keyspace(c.Owner)
	for n := 1; n <= c.Rows; n++ {
		k, v := rewriteRow(n)
		if got, err := ks.Get(ctx, k); err != nil || !bytes.Equal(got, v) {
			t.Errorf("row %d after M8 = %q, %v; want %q", n, got, err, v)
		}
	}
	if state, err := db.RewriteRecord(ctx, RewriteID(c.Owner)); err != nil || state != storage.DataMigrationDone {
		t.Errorf("M8's record = %q, %v; want done", state, err)
	}
	if g := generationOf(t, db); g != gen {
		t.Errorf("generation after M8 = %d, want %d (the rewrite keeps it)", g, gen)
	}
	for _, p := range []string{storage.RewriteDir(RewritePath), storage.RewriteDir(RewritePath) + ".tmp", restorejournal.PendingPath(RewriteStateDir)} {
		if _, err := fs.Stat(p); err == nil {
			t.Errorf("%s left after M8", p)
		}
	}
	// No old store in any set-aside: an applied swap's previous/ is deleted
	// by phase 4, a swapped-back one's is empty (the original moved back;
	// its failed/ keeps the refused copy for the journal's retention).
	dir := restorejournal.Dir(RewriteStateDir)
	ids, err := fs.List(dir)
	if err != nil {
		t.Fatalf("crashmatrix: list %s: %v", dir, err)
	}
	for _, id := range ids {
		if _, err := fs.Stat(filepath.Join(dir, id, restorejournal.InflightName)); err == nil {
			t.Errorf("restore/%s/%s left after M8", id, restorejournal.InflightName)
		}
		if f := anyFile(fs, filepath.Join(dir, id, "previous")); f != "" {
			t.Errorf("%s: an old store's file left in a set-aside after M8", f)
		}
	}
}

// anyFile is the first regular file under p, "" when there is none; a
// directory it cannot list counts as one, so the check fails closed.
func anyFile(fs vfs.FS, p string) string {
	fi, err := fs.Stat(p)
	if err != nil {
		return ""
	}
	if !fi.IsDir() {
		return p
	}
	names, err := fs.List(p)
	if err != nil {
		return p + " (unlisted: " + err.Error() + ")"
	}
	for _, n := range names {
		if f := anyFile(fs, filepath.Join(p, n)); f != "" {
			return f
		}
	}
	return ""
}

// generationOf is the store generation in c/storage/epoch.
func generationOf(t testing.TB, db *storage.DB) uint64 {
	t.Helper()
	ks, _ := db.Keyspace("core/storage")
	raw, err := ks.Get(context.Background(), keys.Join(keys.Str("epoch")))
	if err != nil {
		t.Fatalf("crashmatrix: epoch record: %v", err)
	}
	var rec struct {
		Epoch uint64 `json:"epoch"`
	}
	if i := bytes.IndexByte(raw, '{'); i < 0 || json.Unmarshal(raw[i:], &rec) != nil {
		t.Fatalf("crashmatrix: epoch record %q does not decode", raw)
	}
	return rec.Epoch
}
