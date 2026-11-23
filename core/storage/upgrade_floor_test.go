// file: core/storage/upgrade_floor_test.go
// version: 0.1.0
// guid: 5d83a2c6-0e14-4b97-a6f1-7c29e8b0d435
// last-edited: 2026-11-21

package storage

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	cpebble "github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
)

// floorDump is every key and value of a closed store, read through a
// read-only Pebble open: the byte-identical check of a refused boot. (The
// engine's files are not compared: any Pebble open rotates its WAL and
// manifest, which is not a write of the store's contents.)
func floorDump(t *testing.T, fs vfs.FS, path string) map[string]string {
	t.Helper()
	db, err := cpebble.Open(path, &cpebble.Options{FS: fs, ReadOnly: true, Logger: quiet{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	it, err := db.NewIter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = it.Close() }()
	out := map[string]string{}
	for ok := it.First(); ok; ok = it.Next() {
		out[string(it.Key())] = string(it.Value())
	}
	if err := it.Error(); err != nil {
		t.Fatal(err)
	}
	return out
}

// parentNames lists the entries beside the store (its checkpoint directory
// and its migration-checkpoint pointer would appear here).
func parentNames(t *testing.T, fs vfs.FS, path string) []string {
	t.Helper()
	names, err := fs.List(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	return names
}

// floorBuild is a build of one keyspace owner, "wayne", registering steps
// from..to: a build with a floor registers none at or below the floor's
// Version, so its first step is the floor's Version+1. Each step counts its
// runs in *runs and writes a key of its own, so a boot that migrates a refused
// store is visible twice over.
func floorBuild(from, to int, floor *UpgradeFloor, runs *atomic.Int32) []OwnerSpec {
	var steps []Migration
	for v := from; v <= to; v++ {
		key := "cave/" + string(rune('a'+v-1))
		steps = append(steps, Migration{Version: v, Name: key, Compat: Additive, Up: func(ctx context.Context, tx *MigrationTx) error {
			runs.Add(1)
			return tx.Set([]byte(key), []byte("bat"))
		}})
	}
	return []OwnerSpec{
		{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Migrations: steps, UpgradeFloor: floor}},
	}
}

// TestUpgradeFloorRefusesBeforeAnyWrite (R3-02c; 05-storage §5S.4 step 3,
// R2-4): a non-empty store below a declared UpgradeFloor, by version or by a
// Requires step that is not done, is refused with *ErrUpgradeFloor before
// anything is written. The pending steps never run, no checkpoint is taken,
// and every key and value is as the older build left it. The controls prove
// the refusal is the floor's: the same store boots and migrates under a floor
// it satisfies, and an empty store is exempt and boots twice.
func TestUpgradeFloorRefusesBeforeAnyWrite(t *testing.T) {
	const tag = "r2"
	wayneM2 := StepRef{Owner: "wayne", DataMigration: "wayne.m2"}
	for name, c := range map[string]struct {
		floor       *UpgradeFloor
		wantHave    int
		wantMissing []StepRef
	}{
		"version below the floor": {
			floor:    &UpgradeFloor{Owner: "wayne", Version: 3, InstallFirst: tag},
			wantHave: 1,
		},
		"required data migration not done": {
			floor:       &UpgradeFloor{Owner: "wayne", Version: 1, Requires: []StepRef{wayneM2}, InstallFirst: tag},
			wantHave:    1,
			wantMissing: []StepRef{wayneM2},
		},
		"version and required step both short": {
			floor:       &UpgradeFloor{Owner: "wayne", Version: 3, Requires: []StepRef{wayneM2}, InstallFirst: tag},
			wantHave:    1,
			wantMissing: []StepRef{wayneM2},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var oldRuns, newRuns atomic.Int32
			// The older build: step 1 applied (recorded by its fresh install),
			// a non-empty store.
			e := newEnv(t, floorBuild(1, 1, nil, &oldRuns)...)
			if err := e.open().Close(); err != nil {
				t.Fatal(err)
			}
			dumpBefore := floorDump(t, e.fs, e.path)
			namesBefore := parentNames(t, e.fs, e.path)
			if len(dumpBefore) == 0 {
				t.Fatal("the fixture store is empty, so the floor would be exempt")
			}

			// The newer build declares the floor and has two steps above it pending.
			e.opts.Owners = floorBuild(c.floor.Version+1, c.floor.Version+2, c.floor, &newRuns)
			db, err := e.tryOpen()
			if err == nil {
				_ = db.Close()
				t.Fatal("boot of a store below the floor succeeded")
			}
			var floor *ErrUpgradeFloor
			if !errors.As(err, &floor) {
				t.Fatalf("boot = %v, want *ErrUpgradeFloor", err)
			}
			if floor.Owner != "wayne" || floor.Have != c.wantHave || floor.Floor != c.floor.Version ||
				floor.InstallFirst != tag || !slices.Equal(floor.Missing, c.wantMissing) {
				t.Errorf("refusal = %+v, want owner wayne, have %d, floor %d, missing %v, install %q",
					*floor, c.wantHave, c.floor.Version, c.wantMissing, tag)
			}
			if !strings.HasSuffix(err.Error(), "install "+tag+" first") {
				t.Errorf("refusal text %q does not end with the instruction", err.Error())
			}

			if n := newRuns.Load(); n != 0 {
				t.Errorf("%d pending steps ran on a refused store", n)
			}
			dumpAfter := floorDump(t, e.fs, e.path)
			if len(dumpAfter) != len(dumpBefore) {
				t.Errorf("the store changed: %d keys before, %d after", len(dumpBefore), len(dumpAfter))
			}
			for k, v := range dumpBefore {
				if got, ok := dumpAfter[k]; !ok || got != v {
					t.Errorf("the store changed at %q", k)
				}
			}
			for k := range dumpAfter {
				if _, ok := dumpBefore[k]; !ok {
					t.Errorf("the refused boot wrote %q", k)
				}
			}
			if after := parentNames(t, e.fs, e.path); !slices.Equal(namesBefore, after) {
				t.Errorf("entries beside the store: before %v, after %v (a checkpoint was taken?)", namesBefore, after)
			}
		})
	}

	t.Run("control: a floor the store satisfies boots and migrates", func(t *testing.T) {
		var oldRuns, newRuns atomic.Int32
		e := newEnv(t, floorBuild(1, 1, nil, &oldRuns)...)
		if err := e.open().Close(); err != nil {
			t.Fatal(err)
		}
		e.opts.Owners = floorBuild(2, 3, &UpgradeFloor{Owner: "wayne", Version: 1, InstallFirst: tag}, &newRuns)
		db := e.open()
		if got := db.AppliedVersion("wayne"); got != 3 || newRuns.Load() != 2 {
			t.Errorf("wayne at %d after %d steps, want 3 after 2", got, newRuns.Load())
		}
	})

	t.Run("control: an empty store is exempt and boots twice", func(t *testing.T) {
		var runs atomic.Int32
		floor := &UpgradeFloor{Owner: "wayne", Version: 3, Requires: []StepRef{wayneM2}, InstallFirst: tag}
		e := newEnv(t, floorBuild(4, 5, floor, &runs)...)
		if err := e.open().Close(); err != nil {
			t.Fatalf("fresh install under a floor: %v", err)
		}
		db := e.open() // now a non-empty store: the floor is checked and passes
		if got := db.AppliedVersion("wayne"); got != 5 {
			t.Errorf("wayne at %d on the second boot, want 5", got)
		}
	})
}

// TestFloorOwnersStepsStartAfterTheFloor (R3-02e; 05-storage §5S.3.5): an
// owner that declares an UpgradeFloor registers its steps from the floor's
// Version+1, and no earlier; without a floor they start at 1. A step numbered
// at the floor (its code deleted, the version already applied by every store
// that passes it) and a gap above it are registration errors, and a floor
// owner that registers no step is valid and boots twice.
func TestFloorOwnersStepsStartAfterTheFloor(t *testing.T) {
	var runs atomic.Int32
	floor := func(v int) *UpgradeFloor { return &UpgradeFloor{Owner: "wayne", Version: v, InstallFirst: "r2"} }
	for name, c := range map[string]struct {
		from, to int
		floor    *UpgradeFloor
		want     string // a substring of the registration error; "" = accepted
	}{
		"first step above the floor":  {4, 5, floor(3), ""},
		"first step at the floor":     {3, 5, floor(3), "versions are contiguous; want 4"},
		"first step below the floor":  {1, 5, floor(3), "versions are contiguous; want 4"},
		"a gap above the floor":       {5, 6, floor(3), "versions are contiguous; want 4"},
		"without a floor: from 1":     {1, 2, nil, ""},
		"without a floor: not from 1": {2, 3, nil, "versions are contiguous; want 1"},
		"a floor and no step":         {0, -1, floor(3), ""},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, floorBuild(c.from, c.to, c.floor, &runs)...)
			db, err := e.tryOpen()
			if c.want == "" {
				if err != nil {
					t.Fatalf("a valid declaration refused: %v", err)
				}
				_ = db.Close()
				return
			}
			if err == nil {
				_ = db.Close()
				t.Fatal("an invalid step numbering was accepted")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

// TestFreshInstallRecordsEveryVersionUpToTheFloor: on an empty store a floor
// owner's one batch records versions 1..Version as applied (their steps are
// not registered) and the registered steps above them, on the owner's native
// bookkeeping as well as through a VersionSource; the second boot is a
// non-empty store that passes the floor and runs nothing.
func TestFreshInstallRecordsEveryVersionUpToTheFloor(t *testing.T) {
	var runs atomic.Int32
	floor := &UpgradeFloor{Owner: "wayne", Version: 3, InstallFirst: "r2"}
	e := newEnv(t, floorBuild(4, 5, floor, &runs)...)
	db := e.open()
	if got := db.AppliedVersion("wayne"); got != 5 {
		t.Fatalf("wayne at %d after a fresh install, want 5", got)
	}
	applied := db.boot.applied["wayne"]
	for v := 1; v <= 5; v++ {
		if _, ok := applied[v]; !ok {
			t.Errorf("no applied record for version %d after a fresh install (have %v)", v, slices.Sorted(maps.Keys(applied)))
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if n := runs.Load(); n != 0 {
		t.Errorf("a fresh install ran %d steps; it records them applied", n)
	}
	db = e.open()
	if got := db.AppliedVersion("wayne"); got != 5 || runs.Load() != 0 {
		t.Errorf("second boot: wayne at %d, %d steps run; want 5, 0", got, runs.Load())
	}
}

// TestFloorOwnerWithoutStepsBootsTwice: an owner whose build registers no step
// above its floor (the version is the floor's alone) records the floor on a
// fresh install and passes it on the next boot.
func TestFloorOwnerWithoutStepsBootsTwice(t *testing.T) {
	var runs atomic.Int32
	floor := &UpgradeFloor{Owner: "wayne", Version: 2, InstallFirst: "r2"}
	e := newEnv(t, floorBuild(0, -1, floor, &runs)...)
	e.opts.Owners[0].Spec.Migrations = nil
	db := e.open()
	if got := db.AppliedVersion("wayne"); got != 2 {
		t.Fatalf("wayne at %d after a fresh install, want 2", got)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if got := e.open().AppliedVersion("wayne"); got != 2 {
		t.Errorf("wayne at %d on the second boot, want 2", got)
	}
}
