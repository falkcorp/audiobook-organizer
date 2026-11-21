// file: core/storage/upgrade_floor_test.go
// version: 0.1.0
// guid: 5d83a2c6-0e14-4b97-a6f1-7c29e8b0d435
// last-edited: 2026-11-21

package storage

import (
	"context"
	"errors"
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

// floorBuild is a build of one keyspace owner, "wayne", with steps 1..n. Each
// step counts its runs in *runs and writes a key of its own, so a boot that
// migrates a refused store is visible twice over.
func floorBuild(n int, floor *UpgradeFloor, runs *atomic.Int32) []OwnerSpec {
	var steps []Migration
	for v := 1; v <= n; v++ {
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
			e := newEnv(t, floorBuild(1, nil, &oldRuns)...)
			if err := e.open().Close(); err != nil {
				t.Fatal(err)
			}
			dumpBefore := floorDump(t, e.fs, e.path)
			namesBefore := parentNames(t, e.fs, e.path)
			if len(dumpBefore) == 0 {
				t.Fatal("the fixture store is empty, so the floor would be exempt")
			}

			// The newer build declares the floor and has steps 2 and 3 pending.
			e.opts.Owners = floorBuild(3, c.floor, &newRuns)
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
		e := newEnv(t, floorBuild(1, nil, &oldRuns)...)
		if err := e.open().Close(); err != nil {
			t.Fatal(err)
		}
		e.opts.Owners = floorBuild(3, &UpgradeFloor{Owner: "wayne", Version: 1, InstallFirst: tag}, &newRuns)
		db := e.open()
		if got := db.AppliedVersion("wayne"); got != 3 || newRuns.Load() != 2 {
			t.Errorf("wayne at %d after %d steps, want 3 after 2", got, newRuns.Load())
		}
	})

	t.Run("control: an empty store is exempt and boots twice", func(t *testing.T) {
		var runs atomic.Int32
		floor := &UpgradeFloor{Owner: "wayne", Version: 3, Requires: []StepRef{wayneM2}, InstallFirst: tag}
		e := newEnv(t, floorBuild(3, floor, &runs)...)
		if err := e.open().Close(); err != nil {
			t.Fatalf("fresh install under a floor: %v", err)
		}
		db := e.open() // now a non-empty store: the floor is checked and passes
		if got := db.AppliedVersion("wayne"); got != 3 {
			t.Errorf("wayne at %d on the second boot, want 3", got)
		}
	})
}
