// file: core/storage/checkpoint_sweep_test.go
// version: 1.0.2
// guid: 9c1e6f47-2b80-4d3a-a5f1-7e04b9d2c816
// last-edited: 2026-11-08

package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	ipebble "github.com/falkcorp/mediacore/internal/pebble"
)

// The sweeper and prune tests run on a fake clock: the store's
// OpenOptions.Clock, which every timestamp of a record and every sweep
// reads. The spec marks some of them synctest; a clock injected where the
// store reads it proves the same 7-day boundaries without a bubble around
// Pebble's goroutines (seams.d/R1-24.md).

// fakeClock is a settable clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, time.April, 1, 9, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

// sweep runs one sweep over db with no state directory.
func sweep(t *testing.T, db *DB) SweepReport {
	t.Helper()
	rep, err := db.SweepCheckpoints(context.Background(), "")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	return rep
}

// cpNames lists the checkpoint directory's names.
func cpNames(t *testing.T, fs vfs.FS, dir string) []string {
	t.Helper()
	names, err := fs.List(dir)
	if err != nil && !oserrorNotExist(err) {
		t.Fatal(err)
	}
	slices.Sort(names)
	return slices.DeleteFunc(names, func(n string) bool { return n == sweepLockName })
}

// infoOf is the health entry of name.
func infoOf(h CheckpointHealth, name string) (CheckpointInfo, bool) {
	for _, c := range h.Checkpoints {
		if c.Name == name {
			return c, true
		}
	}
	return CheckpointInfo{}, false
}

// opLog is a filesystem that records the paths of Remove and RemoveAll in
// order, and can fail the RemoveAll of one path once.
type opLog struct {
	vfs.FS
	mu       sync.Mutex
	ops      []string
	failOnce string
}

func (o *opLog) Remove(name string) error {
	o.mu.Lock()
	o.ops = append(o.ops, "remove "+filepath.Base(name))
	o.mu.Unlock()
	return o.FS.Remove(name)
}

func (o *opLog) RemoveAll(name string) error {
	o.mu.Lock()
	o.ops = append(o.ops, "removeall "+filepath.Base(name))
	fail := o.failOnce != "" && name == o.failOnce
	if fail {
		o.failOnce = ""
	}
	o.mu.Unlock()
	if fail {
		return errors.New("bane cut the power")
	}
	return o.FS.RemoveAll(name)
}

func (o *opLog) take() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := o.ops
	o.ops = nil
	return out
}

// TestCheckpointAutoDeletedAfterSevenDays (05-storage §5S.10, 09 §9.4 R1
// Verification; D12 (d), CONFORM-6): R1's first-boot checkpoint of a HEAD
// store (the install marker on a non-empty store, no step to run) completes
// with delete_after = completed + 7 days. Every sweep before that keeps it
// and health reports it retained; the first sweep past it deletes it,
// record first, then the copy, then the pointer that names it. A boot past
// the 7 days deletes it at its first sweep too.
func TestCheckpointAutoDeletedAfterSevenDays(t *testing.T) {
	t.Run("while serving", func(t *testing.T) {
		clock := newFakeClock()
		log := &opLog{FS: vfs.NewMem()}
		e := headStore(t, log, OwnerSpec{Owner: "gotham", Enabled: true, Spec: Spec{Claims: gothamClaims()}})
		e.opts.Clock = clock.now
		db := e.open()
		name := db.CheckpointName()
		if name == "" {
			t.Fatal("the first boot of a HEAD store took no checkpoint")
		}
		rec := readRecord(t, e, name)
		if rec.CompletedAt == nil || rec.DeleteAfter == nil || !rec.DeleteAfter.Equal(clock.now().Add(7*24*time.Hour)) {
			t.Fatalf("first-boot record = %+v, want completed with delete_after = completed + 7 days", rec)
		}
		due := *rec.DeleteAfter
		log.take()
		for _, at := range []time.Time{clock.now(), due.Add(-time.Second)} {
			clock.set(at)
			rep := sweep(t, db)
			if len(rep.Deleted) != 0 {
				t.Fatalf("sweep at %s deleted %v before delete_after %s", at, rep.Deleted, due)
			}
			c, ok := infoOf(rep.CheckpointHealth, name)
			if !ok || c.DeleteAfter == nil || !c.DeleteAfter.Equal(due) || c.Rollback {
				t.Fatalf("health at %s = %+v, want %s retained until %s", at, rep.Checkpoints, name, due)
			}
		}
		if ops := log.take(); len(ops) != 0 {
			t.Fatalf("sweeps before delete_after removed %v", ops)
		}
		clock.set(due.Add(time.Second))
		rep := sweep(t, db)
		if !slices.Equal(rep.Deleted, []string{name}) {
			t.Fatalf("sweep past delete_after deleted %v, want [%s]", rep.Deleted, name)
		}
		want := []string{"remove " + name + ".json", "removeall " + name, "remove main.pebble" + ipebble.CheckpointSuffix}
		if ops := log.take(); !slices.Equal(ops, want) {
			t.Fatalf("delete order = %v, want %v (record first)", ops, want)
		}
		if names := cpNames(t, e.fs, cpDir(e)); len(names) != 0 {
			t.Fatalf("checkpoint directory after the deletion = %v, want empty", names)
		}
		if _, err := e.fs.Stat(e.path + ipebble.CheckpointSuffix); !oserrorNotExist(err) {
			t.Fatalf("the pointer naming the deleted checkpoint is still there: %v", err)
		}
		if len(rep.Checkpoints) != 0 {
			t.Fatalf("health after the deletion = %+v, want no checkpoint", rep.Checkpoints)
		}
	})
	t.Run("at a boot", func(t *testing.T) {
		clock := newFakeClock()
		e := headStore(t, vfs.NewMem(), OwnerSpec{Owner: "gotham", Enabled: true, Spec: Spec{Claims: gothamClaims()}})
		e.opts.Clock = clock.now
		db := e.open()
		name := db.CheckpointName()
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		clock.set(clock.now().Add(8 * 24 * time.Hour))
		db = e.open()
		if db.CheckpointName() != "" {
			t.Fatalf("a boot with nothing to run took checkpoint %q", db.CheckpointName())
		}
		if rep := sweep(t, db); !slices.Equal(rep.Deleted, []string{name}) {
			t.Fatalf("the boot's first sweep deleted %v, want [%s]", rep.Deleted, name)
		}
	})
	t.Run("killed between the record and the copy", func(t *testing.T) {
		clock := newFakeClock()
		log := &opLog{FS: vfs.NewMem()}
		e := headStore(t, log, OwnerSpec{Owner: "gotham", Enabled: true, Spec: Spec{Claims: gothamClaims()}})
		e.opts.Clock = clock.now
		db := e.open()
		name := db.CheckpointName()
		clock.set(clock.now().Add(8 * 24 * time.Hour))
		log.failOnce = filepath.Join(cpDir(e), name)
		if _, err := db.SweepCheckpoints(context.Background(), ""); err == nil {
			t.Fatal("the sweep whose copy deletion failed reported success")
		}
		// The record went first, so what is left is a directory without a
		// record: garbage, never a record naming a partial copy.
		if names := cpNames(t, e.fs, cpDir(e)); !slices.Equal(names, []string{name}) {
			t.Fatalf("after the cut = %v, want only the record-less directory %s", names, name)
		}
		rep := sweep(t, db)
		if !slices.Contains(rep.Garbage, filepath.Join(cpDir(e), name)) {
			t.Fatalf("the next sweep's garbage = %v, want the record-less %s", rep.Garbage, name)
		}
		if !slices.Contains(rep.Garbage, e.path+ipebble.CheckpointSuffix) {
			t.Fatalf("the next sweep left the pointer naming the deleted checkpoint: garbage %v", rep.Garbage)
		}
		if names := cpNames(t, e.fs, cpDir(e)); len(names) != 0 {
			t.Fatalf("after the next sweep = %v, want empty", names)
		}
	})
}

// servedOpenCheckpoint is a store whose upgrade failed (failingOwners), then
// booted and served by the previous build (cpOwners): its checkpoint is
// open and served, the live store's rollback. It returns the env, the open
// DB and the checkpoint's name.
func servedOpenCheckpoint(t *testing.T, fs vfs.FS, clock *fakeClock) (*env, *DB, string) {
	t.Helper()
	e := headStore(t, fs, failingOwners()...)
	e.opts.Clock = clock.now
	if _, err := e.tryOpen(); err == nil {
		t.Fatal("the failing upgrade succeeded")
	}
	name := readPointer(t, e).Name
	e.opts.Owners = cpOwners()
	db := e.open()
	if err := db.MarkServed(clock.now()); err != nil {
		t.Fatal(err)
	}
	return e, db, name
}

// TestOpenCheckpointKeptWhileRollback (05-storage §5S.10): an open or served
// checkpoint of the live store that has neither completed a migration nor
// been restored since survives every sweep, with health raising
// storage_checkpoint_open (Rollback) and no delete_after; prune leaves it
// without --include-open and deletes it with it.
func TestOpenCheckpointKeptWhileRollback(t *testing.T) {
	clock := newFakeClock()
	e, db, name := servedOpenCheckpoint(t, vfs.NewMem(), clock)
	for _, days := range []int{0, 7, 30, 365} {
		clock.set(clock.now().Add(time.Duration(days) * 24 * time.Hour))
		rep := sweep(t, db)
		if len(rep.Deleted)+len(rep.Released) != 0 {
			t.Fatalf("day %d: the sweep released %v and deleted %v, want the rollback kept", days, rep.Released, rep.Deleted)
		}
		c, ok := infoOf(rep.CheckpointHealth, name)
		if !ok || !c.Rollback || c.DeleteAfter != nil || c.Status != "served" {
			t.Fatalf("day %d: health = %+v, want %s kept as the rollback (served, no delete_after)", days, rep.Checkpoints, name)
		}
	}
	h, err := db.CheckpointHealth("")
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := infoOf(h, name); !ok || !c.Rollback {
		t.Fatalf("CheckpointHealth = %+v, want %s as the rollback", h.Checkpoints, name)
	}
	prune := PruneOptions{Path: e.path, FS: FS{fs: e.fs}, Clock: clock.now}
	res, err := PruneCheckpoints(context.Background(), prune)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deleted) != 0 || !slices.Equal(res.KeptOpen, []string{name}) {
		t.Fatalf("prune without --include-open = %+v, want %s kept", res, name)
	}
	prune.IncludeOpen = true
	if res, err = PruneCheckpoints(context.Background(), prune); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Deleted, []string{name}) {
		t.Fatalf("prune --include-open deleted %v, want [%s]", res.Deleted, name)
	}
	if names := cpNames(t, e.fs, cpDir(e)); len(names) != 0 {
		t.Fatalf("checkpoint directory after prune --include-open = %v", names)
	}
}

// writeForeignCheckpoint writes a checkpoint record and directory under dir
// as another store's (install ID "joker-install"), with status and
// delete_after as given, taken at taken.
func writeForeignCheckpoint(t *testing.T, fs vfs.FS, dir, name, status string, taken time.Time, deleteAfter *time.Time) {
	t.Helper()
	if err := fs.MkdirAll(filepath.Join(dir, name, "db"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := &cpArea{fs: fs, chmod: func(string, bool) error { return nil }}
	rec := checkpointRecord{InstallID: "joker-install", Epoch: 1, TakenAt: taken, Status: status, DeleteAfter: deleteAfter}
	if err := a.writeRecordAt(dir, name, rec); err != nil {
		t.Fatal(err)
	}
}

// TestOrphanedOpenCheckpointDeletedAfterSevenDays (05-storage §5S.10,
// 05-backup §5B.9; CONFORM-2, DATA-8): an open checkpoint is deleted 7 days
// after it stopped being the rollback, and health raises nothing open for
// it meanwhile: (a) the live store is no longer the one it copies (restored
// from an archive: another install ID); (b) a later attempt on the same
// store completed; (c) a copy in a restore's failed/ directory, and one
// marked restore_left, whose clocks start at the swap-back (here the first
// sweep that finds them without a delete_after).
func TestOrphanedOpenCheckpointDeletedAfterSevenDays(t *testing.T) {
	week := 7 * 24 * time.Hour
	expectReleasedThenDeleted := func(t *testing.T, clock *fakeClock, db *DB, stateDir string, names ...string) {
		t.Helper()
		rep, err := db.SweepCheckpoints(context.Background(), stateDir)
		if err != nil {
			t.Fatal(err)
		}
		released := slices.Clone(rep.Released)
		slices.Sort(released)
		want := slices.Clone(names)
		slices.Sort(want)
		if !slices.Equal(released, want) || len(rep.Deleted) != 0 {
			t.Fatalf("first sweep released %v and deleted %v, want %v released", rep.Released, rep.Deleted, want)
		}
		for _, n := range names {
			c, ok := infoOf(rep.CheckpointHealth, n)
			if !ok || c.Rollback || c.DeleteAfter == nil || !c.DeleteAfter.Equal(clock.now().Add(week)) {
				t.Fatalf("health of %s = %+v, want retained until now + 7 days and no open token", n, c)
			}
		}
		clock.set(clock.now().Add(week - time.Second))
		if rep, err = db.SweepCheckpoints(context.Background(), stateDir); err != nil || len(rep.Deleted) != 0 || len(rep.Released) != 0 {
			t.Fatalf("sweep before the 7 days: %+v, %v; want nothing deleted or re-released", rep, err)
		}
		clock.set(clock.now().Add(2 * time.Second))
		if rep, err = db.SweepCheckpoints(context.Background(), stateDir); err != nil {
			t.Fatal(err)
		}
		for _, n := range want {
			if !slices.Contains(rep.Deleted, n) {
				t.Fatalf("sweep after the 7 days deleted %v, want %s among them", rep.Deleted, n)
			}
		}
	}
	t.Run("restored from an archive", func(t *testing.T) {
		clock := newFakeClock()
		e := newEnv(t, cpOwners()...)
		e.opts.Clock = clock.now
		db := e.open()
		const riddler = "20260301T000000Z-0dd1e500"
		writeForeignCheckpoint(t, e.fs, cpDir(e), riddler, "served", clock.now().Add(-30*24*time.Hour), nil)
		expectReleasedThenDeleted(t, clock, db, "", riddler)
	})
	t.Run("a later attempt completed", func(t *testing.T) {
		clock := newFakeClock()
		e, db, first := servedOpenCheckpoint(t, vfs.NewMem(), clock)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		clock.set(clock.now().Add(time.Hour))
		e.opts.Owners = fixedOwners()
		db = e.open()
		if db.CheckpointName() == first {
			t.Fatal("the fixed build reused the served checkpoint")
		}
		if c, ok := infoOf(mustHealth(t, db), db.CheckpointName()); !ok || c.DeleteAfter == nil || c.Rollback {
			t.Fatalf("the completed attempt's checkpoint = %+v, want it retained with its delete_after", c)
		}
		// Completed an hour later, so the two expire together.
		expectReleasedThenDeleted(t, clock, db, "", first)
	})
	t.Run("failed and restore_left", func(t *testing.T) {
		clock := newFakeClock()
		e := newEnv(t, cpOwners()...)
		e.opts.Clock = clock.now
		db := e.open()
		stateDir := filepath.Join(filepath.Dir(e.path), "state")
		const (
			penguin = "20260302T000000Z-9e1a0001"
			bane    = "20260303T000000Z-ba1e0002"
			scarecr = "20260304T000000Z-5ca7e003"
		)
		// step x's rule (restorejournal.FailedDest): a checkpoint beside the
		// store goes to <db parent>/.mediacore-restore/<id>/failed/.
		dbSide := filepath.Join(filepath.Dir(e.path), ".mediacore-restore", "r-arkham", "failed", ".mediacore-checkpoints")
		writeForeignCheckpoint(t, e.fs, dbSide, penguin, "open", clock.now().Add(-time.Hour), nil)
		// One left in place by a swap-back that could not move it.
		writeForeignCheckpoint(t, e.fs, cpDir(e), bane, "restore_left", clock.now().Add(-time.Hour), nil)
		// The state directory's restore/<id>/failed/ is scanned too.
		stateSide := filepath.Join(stateDir, "restore", "r-blackgate", "failed", ".mediacore-checkpoints")
		if err := e.fs.MkdirAll(filepath.Join(stateDir, "restore", "r-blackgate"), 0o700); err != nil {
			t.Fatal(err)
		}
		writeForeignCheckpoint(t, e.fs, stateSide, scarecr, "open", clock.now().Add(-time.Hour), nil)
		// A failed/ checkpoint whose own delete_after is already set (by
		// step x at the swap-back) keeps it.
		swapBack := clock.now().Add(-6 * 24 * time.Hour).Add(week)
		const twoface = "20260305T000000Z-2face004"
		writeForeignCheckpoint(t, e.fs, dbSide, twoface, "open", clock.now().Add(-6*24*time.Hour), &swapBack)
		rep, err := db.SweepCheckpoints(context.Background(), stateDir)
		if err != nil {
			t.Fatal(err)
		}
		if c, ok := infoOf(rep.CheckpointHealth, twoface); !ok || !c.DeleteAfter.Equal(swapBack) {
			t.Fatalf("the swap-back's delete_after was rewritten: %+v", c)
		}
		clock.set(swapBack.Add(time.Second))
		if rep, err = db.SweepCheckpoints(context.Background(), stateDir); err != nil || !slices.Contains(rep.Deleted, twoface) {
			t.Fatalf("the swap-back's checkpoint at its delete_after: %+v, %v", rep.Deleted, err)
		}
		// The others were released by the first sweep, a day earlier.
		rep, err = db.SweepCheckpoints(context.Background(), stateDir)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range []string{penguin, bane, scarecr} {
			c, ok := infoOf(rep.CheckpointHealth, n)
			if !ok || c.DeleteAfter == nil || c.Rollback {
				t.Fatalf("%s = %+v, want released", n, c)
			}
		}
		clock.set(clock.now().Add(week))
		if rep, err = db.SweepCheckpoints(context.Background(), stateDir); err != nil {
			t.Fatal(err)
		}
		deleted := slices.Clone(rep.Deleted)
		slices.Sort(deleted)
		if want := []string{penguin, bane, scarecr}; !slices.Equal(deleted, want) {
			t.Fatalf("after the 7 days the sweep deleted %v, want %v", deleted, want)
		}
	})
}

// TestPruneCheckpoints (05-storage §5S.10): storage prune-checkpoints
// deletes completed and released checkpoints now, while the service runs
// (the store open), leaves open ones without --include-open, refuses during
// an offline restore (a swap in flight) and while another sweep holds the
// lock, and touches no garbage, scratch entry or name outside the grammar.
func TestPruneCheckpoints(t *testing.T) {
	clock := newFakeClock()
	e, db, open := servedOpenCheckpoint(t, vfs.NewMem(), clock)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	clock.set(clock.now().Add(time.Hour))
	e.opts.Owners = fixedOwners()
	db = e.open() // serving: the store stays open while prune runs
	completed := db.CheckpointName()
	const riddler = "20260301T000000Z-0dd1e500"
	released := clock.now().Add(7 * 24 * time.Hour)
	writeForeignCheckpoint(t, e.fs, cpDir(e), riddler, "released", clock.now(), &released)
	const partial = "20260306T000000Z-ca7a0001"
	for _, d := range []string{partial, "scratch/r-rehearsal", "batcave"} {
		if err := e.fs.MkdirAll(filepath.Join(cpDir(e), d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	stateDir := filepath.Join(filepath.Dir(e.path), "state")
	opts := PruneOptions{Path: e.path, StateDir: stateDir, FS: FS{fs: e.fs}, Clock: clock.now, lockWait: 50 * time.Millisecond}

	t.Run("refused while a swap is in flight", func(t *testing.T) {
		inflight := filepath.Join(stateDir, "restore", "r-gcpd", "inflight.json")
		if err := e.fs.MkdirAll(filepath.Dir(inflight), 0o700); err != nil {
			t.Fatal(err)
		}
		f, err := e.fs.Create(inflight, vfs.WriteCategoryUnspecified)
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
		defer func() { _ = e.fs.RemoveAll(filepath.Join(stateDir, "restore")) }()
		before := cpNames(t, e.fs, cpDir(e))
		_, err = PruneCheckpoints(context.Background(), opts)
		mustErrIs(t, err, ErrPruneSwapInFlight)
		if after := cpNames(t, e.fs, cpDir(e)); !slices.Equal(before, after) {
			t.Fatalf("a refused prune changed %v to %v", before, after)
		}
	})
	t.Run("refused while a restore is staged", func(t *testing.T) {
		// A staged intent (a KindCheckpoint restore among them) is swapped
		// in by the next boot and may name the checkpoint prune would
		// delete.
		pending := filepath.Join(stateDir, "restore", "pending.json")
		if err := e.fs.MkdirAll(filepath.Dir(pending), 0o700); err != nil {
			t.Fatal(err)
		}
		f, err := e.fs.Create(pending, vfs.WriteCategoryUnspecified)
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
		defer func() { _ = e.fs.RemoveAll(filepath.Join(stateDir, "restore")) }()
		before := cpNames(t, e.fs, cpDir(e))
		_, err = PruneCheckpoints(context.Background(), opts)
		mustErrIs(t, err, ErrPruneSwapInFlight)
		if after := cpNames(t, e.fs, cpDir(e)); !slices.Equal(before, after) {
			t.Fatalf("a refused prune changed %v to %v", before, after)
		}
	})
	t.Run("refused while a sweep holds the lock", func(t *testing.T) {
		// The lock is the OS's (cpArea.lock): on the real filesystem, a
		// prune waits out its lockWait and gives up while a sweep holds it.
		dir := t.TempDir()
		path := filepath.Join(dir, "main.pebble")
		cps := filepath.Join(dir, checkpointLeaf)
		if err := os.MkdirAll(cps, 0o700); err != nil {
			t.Fatal(err)
		}
		l, err := vfs.Default.Lock(filepath.Join(cps, sweepLockName))
		if err != nil {
			t.Fatal(err)
		}
		_, err = PruneCheckpoints(context.Background(), PruneOptions{Path: path, lockWait: 50 * time.Millisecond})
		_ = l.Close()
		mustErrIs(t, err, ErrCheckpointSweepBusy)
		if _, err := PruneCheckpoints(context.Background(), PruneOptions{Path: path}); err != nil {
			t.Fatalf("a prune after the lock was released = %v", err)
		}
	})
	t.Run("deletes completed and released, keeps open", func(t *testing.T) {
		res, err := PruneCheckpoints(context.Background(), opts)
		if err != nil {
			t.Fatal(err)
		}
		deleted := slices.Clone(res.Deleted)
		slices.Sort(deleted)
		want := []string{completed, riddler}
		slices.Sort(want)
		if !slices.Equal(deleted, want) || !slices.Equal(res.KeptOpen, []string{open}) {
			t.Fatalf("prune = %+v, want %v deleted and %s kept", res, want, open)
		}
		names := cpNames(t, e.fs, cpDir(e))
		if want := []string{open, partial, open + ".json", "batcave", "scratch"}; !slices.Equal(names, sortedCopy(want)) {
			t.Fatalf("after prune = %v, want %v (garbage, scratch and other names are the sweeper's or nobody's)", names, sortedCopy(want))
		}
		// The fixed build's completion pointed .migration-checkpoint at
		// the checkpoint prune deleted, so prune removed the pointer too.
		if _, err := e.fs.Stat(e.path + ipebble.CheckpointSuffix); !oserrorNotExist(err) {
			t.Fatalf("the pointer naming the pruned %s is still there: %v", completed, err)
		}
	})
}

// TestPruneCheckpointsProbesTheSwapUnderTheLock: prune reads the swap slot
// after it holds the sweep lock, so a restore staged or started while it
// waited for the lock is refused, not raced: a storage restore-offline
// that holds the same lock while it swaps never overlaps a prune. Real
// filesystem: the lock is the OS's.
func TestPruneCheckpointsProbesTheSwapUnderTheLock(t *testing.T) {
	for _, slot := range []string{"pending.json", filepath.Join("r-arkham", "inflight.json")} {
		t.Run(slot, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "main.pebble")
			stateDir := filepath.Join(dir, "state")
			cps := filepath.Join(dir, checkpointLeaf)
			const penguin = "20260302T000000Z-0b1e5e00"
			released := time.Now().Add(-time.Hour)
			writeForeignCheckpoint(t, vfs.Default, cps, penguin, "released", time.Now().Add(-8*24*time.Hour), &released)
			l, err := vfs.Default.Lock(filepath.Join(cps, sweepLockName))
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := PruneCheckpoints(context.Background(), PruneOptions{Path: path, StateDir: stateDir, lockWait: time.Minute})
				done <- err
			}()
			// The restore stages while prune waits for the lock, then the
			// lock is released.
			time.Sleep(150 * time.Millisecond)
			p := filepath.Join(stateDir, "restore", slot)
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
			_ = l.Close()
			select {
			case err := <-done:
				mustErrIs(t, err, ErrPruneSwapInFlight)
			case <-time.After(30 * time.Second):
				t.Fatal("prune did not return after the lock was released")
			}
			if _, err := os.Stat(filepath.Join(cps, penguin+".json")); err != nil {
				t.Fatalf("a refused prune removed %s's record: %v", penguin, err)
			}
		})
	}
}

func mustHealth(t *testing.T, db *DB) CheckpointHealth {
	t.Helper()
	h, err := db.CheckpointHealth("")
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func sortedCopy(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

// TestPartialCheckpointSweptAfterReadiness (05-storage §5S.10
// TestPartialCheckpointSwept, the sweeper's half; DATA3-22): a checkpoint
// directory without a record, a <name>.tmp and a record without its
// directory found by a sweep while serving are removed, and nothing outside
// the checkpoint grammar is.
func TestPartialCheckpointSweptAfterReadiness(t *testing.T) {
	e := newEnv(t, cpOwners()...)
	db := e.open()
	dir := cpDir(e)
	const catwoman, poison = "20260101T000000Z-ca7a0001", "20260102T000000Z-901500aa.tmp"
	for _, d := range []string{catwoman, poison, "scratch", "batcave"} {
		if err := e.fs.MkdirAll(filepath.Join(dir, d, "db"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"20260103T000000Z-2face002.json", "joker.json"} {
		f, err := e.fs.Create(filepath.Join(dir, n), vfs.WriteCategoryUnspecified)
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	rep := sweep(t, db)
	if len(rep.Garbage) != 3 {
		t.Fatalf("garbage = %v, want the record-less directory, the .tmp and the dangling record", rep.Garbage)
	}
	if names := cpNames(t, e.fs, dir); !slices.Equal(names, []string{"batcave", "joker.json", "scratch"}) {
		t.Fatalf("after the sweep = %v, want only the names outside the grammar", names)
	}
}

// TestDryRunScratchModeAndSweep (05-storage §5S.10, the sweeper's half;
// the dry run itself is F3's): an entry left in the scratch area (a killed
// dry run's directory, an --into copy nobody deleted) is reported open with
// its mtime and deleted by the sweeper 7 days after it.
func TestDryRunScratchModeAndSweep(t *testing.T) {
	clock := newFakeClock()
	e := newEnv(t, cpOwners()...)
	e.opts.Clock = clock.now
	db := e.open()
	killed := filepath.Join(cpDir(e), "scratch", "dryrun-joker")
	if err := e.fs.MkdirAll(filepath.Join(killed, "db"), 0o700); err != nil {
		t.Fatal(err)
	}
	fi, err := e.fs.Stat(killed)
	if err != nil {
		t.Fatal(err)
	}
	mtime := fi.ModTime()
	clock.set(mtime.Add(6 * 24 * time.Hour))
	rep := sweep(t, db)
	if len(rep.ScratchDeleted) != 0 || len(rep.Scratch) != 1 || rep.Scratch[0].Path != killed || !rep.Scratch[0].ModTime.Equal(mtime) {
		t.Fatalf("sweep at 6 days = %+v, want %s kept and reported with its mtime", rep, killed)
	}
	clock.set(mtime.Add(7*24*time.Hour + time.Second))
	rep = sweep(t, db)
	if !slices.Equal(rep.ScratchDeleted, []string{killed}) || len(rep.Scratch) != 0 {
		t.Fatalf("sweep past 7 days = %+v, want %s deleted", rep, killed)
	}
	if _, err := e.fs.Stat(filepath.Join(cpDir(e), "scratch")); err != nil {
		t.Fatalf("the scratch area itself was removed: %v", err)
	}
}

// TestSweepRefusedWhileTheLockIsHeld: a sweep takes the checkpoint
// directory's lock, so while a prune (or another sweep) holds it the sweep
// returns ErrCheckpointSweepBusy and changes nothing, and the next one,
// after the lock is released, does the work. Real filesystem: the lock is
// the OS's, and a non-OS filesystem takes none (cpArea.lock).
func TestSweepRefusedWhileTheLockIsHeld(t *testing.T) {
	e := headStore(t, vfs.Default, cpOwners()...)
	db := e.open()
	dir := cpDir(e)
	const scarecrow = "20260304T000000Z-5ca7e001"
	released := time.Now().Add(-time.Hour)
	writeForeignCheckpoint(t, vfs.Default, dir, scarecrow, "released", time.Now().Add(-8*24*time.Hour), &released)
	before := cpNames(t, vfs.Default, dir)
	l, err := vfs.Default.Lock(filepath.Join(dir, sweepLockName))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.SweepCheckpoints(context.Background(), "")
	_ = l.Close()
	mustErrIs(t, err, ErrCheckpointSweepBusy)
	if after := cpNames(t, vfs.Default, dir); !slices.Equal(before, after) {
		t.Fatalf("a sweep refused by the lock changed %v to %v", before, after)
	}
	rep := sweep(t, db)
	if !slices.Contains(rep.Deleted, scarecrow) {
		t.Fatalf("the sweep after the lock was released = %+v, want %s deleted", rep, scarecrow)
	}
}

// TestSweepNeedsAFinishedBoot: the sweep and the health read refuse a store
// whose boot did not finish Migrate, as MarkServed does.
func TestSweepNeedsAFinishedBoot(t *testing.T) {
	e := newEnv(t, cpOwners()...)
	op, err := Open(context.Background(), e.opts)
	if err != nil {
		t.Fatal(err)
	}
	db, err := op.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.SweepCheckpoints(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "Migrate") {
		t.Fatalf("SweepCheckpoints before Migrate = %v, want refused", err)
	}
	if _, err := db.CheckpointHealth(""); err == nil {
		t.Fatal("CheckpointHealth before Migrate succeeded")
	}
}

// TestPruneRefusesARelativePath: prune names files directly, so a relative
// store path or checkpoint directory is refused before anything is read.
func TestPruneRefusesARelativePath(t *testing.T) {
	for _, o := range []PruneOptions{{Path: "main.pebble"}, {Path: "/gotham/main.pebble", CheckpointDir: "cave"}} {
		if _, err := PruneCheckpoints(context.Background(), o); err == nil {
			t.Errorf("PruneCheckpoints(%+v) succeeded", o)
		}
	}
}

// TestPruneWithNoCheckpointDirectory: a store that never took a checkpoint
// has nothing to prune, and prune creates nothing.
func TestPruneWithNoCheckpointDirectory(t *testing.T) {
	fs := vfs.NewMem()
	dir := filepath.Join(string(os.PathSeparator)+"gotham", "db")
	res, err := PruneCheckpoints(context.Background(), PruneOptions{Path: filepath.Join(dir, "main.pebble"), FS: FS{fs: fs}})
	if err != nil || len(res.Deleted)+len(res.KeptOpen) != 0 {
		t.Fatalf("prune with no checkpoints = %+v, %v", res, err)
	}
	if _, err := fs.Stat(filepath.Join(dir, checkpointLeaf)); !oserrorNotExist(err) {
		t.Fatalf("prune created the checkpoint directory: %v", err)
	}
}
