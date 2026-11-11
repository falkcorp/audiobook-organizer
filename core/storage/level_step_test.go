// file: core/storage/level_step_test.go
// version: 1.0.0
// guid: e49c757e-89d8-465f-87ef-b718e9906f8a
// last-edited: 2026-11-10

package storage

import (
	"context"
	"errors"
	"testing"

	ipebble "github.com/falkcorp/mediacore/internal/pebble"
)

// TestRaiseCommitErrorRewritesSidecar (05-storage §5S.10, OPS-26b): a level
// step whose stamp batch fails to commit rewrites the sidecars from the
// committed stamps before it returns, so they describe the store again: the
// level sidecar back at the old level and the legacy projection back at
// LegacyMaxFormat. The raise wrote the new level first.
func TestRaiseCommitErrorRewritesSidecar(t *testing.T) {
	g := projectionGuard()
	stage := g.Stage
	// The stamp batch carries a precondition the store does not meet, so
	// its Commit fails with ErrConflict after the sidecars were written.
	g.Stage = func(ctx context.Context, r LegacyRange, b *Batch, value int, marker bool) error {
		if err := stage(ctx, r, b, value, marker); err != nil {
			return err
		}
		return b.Expect(r, []byte("joker_counter"), []byte("two-face"))
	}
	e := newEnv(t, OwnerSpec{Owner: "gotham", Enabled: true, Spec: Spec{Claims: projectionClaims(), LegacyGuard: g}})
	upgrade(t, e, OwnerSpec{Owner: "gotham", Enabled: true, Spec: Spec{Claims: projectionClaims(), LegacyGuard: g, Level: 1,
		Migrations: []Migration{LevelStep("gotham", 1, 1)}}})
	var during map[string]int
	var projDuring int
	e.opts.hooks.beforeStampCommit = func() error {
		side, _, err := ipebble.ReadLevels(e.fs, e.path)
		if err != nil {
			return err
		}
		during = side
		projDuring, _, err = ipebble.ReadInt(e.fs, e.path+g.SidecarSuffix)
		return err
	}
	_, err := e.tryOpen()
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("boot = %v, want the stamp batch's ErrConflict", err)
	}
	if during["gotham"] != 1 || projDuring != g.LegacyMaxFormat+1 {
		t.Fatalf("sidecars before the commit = %v / projection %d, want gotham 1 / %d", during, projDuring, g.LegacyMaxFormat+1)
	}
	side, _, err := ipebble.ReadLevels(e.fs, e.path)
	if err != nil || side["gotham"] != 0 {
		t.Fatalf("level sidecar after the failed commit = %v, %v; want gotham 0", side, err)
	}
	proj, _, err := ipebble.ReadInt(e.fs, e.path+g.SidecarSuffix)
	if err != nil || proj != g.LegacyMaxFormat {
		t.Fatalf("projection sidecar after the failed commit = %d, %v; want %d", proj, err, g.LegacyMaxFormat)
	}
	// The store still opens under a level-0 build: nothing was stamped.
	e.opts.Owners = []OwnerSpec{{Owner: "gotham", Enabled: true, Spec: Spec{Claims: projectionClaims(), LegacyGuard: projectionGuard()}}}
	e.opts.hooks = testHooks{}
	if err := e.open().Close(); err != nil {
		t.Fatalf("a level-0 build after the failed raise: %v", err)
	}
}

// TestLevelStepConstructors: the level step is a Breaking step named for its
// owner's target level whose Up writes nothing; core's half raises the one
// "core" level and runs After the steps it names.
func TestLevelStepConstructors(t *testing.T) {
	m := LevelStep("wayne", 4, 2, StepRef{Owner: "wayne", Version: 3})
	if m.Name != "wayne.level-2" || m.Version != 4 || m.Compat != Breaking || m.RaisesLevel != 2 || len(m.After) != 1 {
		t.Fatalf("LevelStep = %+v", m)
	}
	if err := m.Up(context.Background(), nil); err != nil {
		t.Fatalf("a level step's Up = %v, want no writes and no error", err)
	}
	c := CoreLevelStep(2, 3)
	if c.Name != "core.level-3" || c.Version != 2 || c.RaisesLevel != 3 {
		t.Fatalf("CoreLevelStep = %+v", c)
	}
	// Over a core subsystem: core's stamp is raised, the sidecar first.
	e := newEnv(t, OwnerSpec{Owner: "core/tasks", Enabled: true, Spec: Spec{Keyspace: true}})
	upgrade(t, e, OwnerSpec{Owner: "core/tasks", Enabled: true, Spec: Spec{Keyspace: true, Level: 1,
		Migrations: []Migration{additive(1), CoreLevelStep(2, 1, StepRef{Owner: "core/tasks", Version: 1})}}})
	db := e.open()
	if l, _ := stamp(t, db, "core"); l != 1 {
		t.Fatalf("core stamp = %d, want 1", l)
	}
	side, _, err := ipebble.ReadLevels(e.fs, e.path)
	if err != nil || side["core"] != 1 {
		t.Fatalf("level sidecar = %v, %v", side, err)
	}
}

// additive is a no-op Additive step at version v.
func additive(v int) Migration {
	return Migration{Version: v, Name: "additive", Compat: Additive, Up: func(context.Context, *MigrationTx) error { return nil }}
}

// TestRaiseLevelSeam: the data-migration runner's level-step seam raises the
// stamp alone (sidecar first) from inside Migrate, never lowers it, and
// refuses an unknown owner, a level above the build's and any call outside a
// writable Migrate, writing nothing.
func TestRaiseLevelSeam(t *testing.T) {
	ctx := context.Background()
	wayne := OwnerSpec{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true}}
	e := newEnv(t, wayne)
	upgrade(t, e, OwnerSpec{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Level: 2}})
	var errs []error
	var sideBefore int
	e.opts.hooks.beforeStampCommit = func() error {
		side, _, err := ipebble.ReadLevels(e.fs, e.path)
		sideBefore = side["wayne"]
		return err
	}
	e.opts.DataSteps = []DataStep{{Owner: "wayne", ID: "wayne.riddles", Run: func(ctx context.Context, db *DB) error {
		errs = append(errs,
			db.RaiseLevel(ctx, "wayne", 3),   // above the build's level 2
			db.RaiseLevel(ctx, "penguin", 1), // not served
			db.RaiseLevel(ctx, "wayne", 2),   // the raise
			db.RaiseLevel(ctx, "wayne", 1))   // below: stamps nothing
		return markDone(ctx, db, "wayne.riddles")
	}}}
	db := e.open()
	if !errors.Is(errs[0], ErrLevelStepRefused) || !errors.Is(errs[1], ErrLevelStepRefused) || errs[2] != nil || errs[3] != nil {
		t.Fatalf("RaiseLevel results = %v", errs)
	}
	if sideBefore != 2 {
		t.Fatalf("sidecar before the stamp commit = %d, want 2", sideBefore)
	}
	if l, _ := stamp(t, db, "wayne"); l != 2 {
		t.Fatalf("wayne stamp = %d, want 2", l)
	}
	if err := db.RaiseLevel(ctx, "wayne", 2); !errors.Is(err, ErrLevelStepRefused) {
		t.Fatalf("RaiseLevel after Migrate = %v, want ErrLevelStepRefused", err)
	}
}

// markDone records data migration id done, as a runner's last state commit
// does.
func markDone(ctx context.Context, db *DB, id string) error {
	raw, err := DataMigrationCodec().Encode(DataMigrationRecord{State: DataMigrationDone, Build: "batcomputer-1"})
	if err != nil {
		return err
	}
	return db.storageKS().Set(ctx, DataMigrationKey(id), raw)
}
