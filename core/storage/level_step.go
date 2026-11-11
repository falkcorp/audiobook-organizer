// file: core/storage/level_step.go
// version: 1.0.0
// guid: 22c6d5dc-c225-47db-bbf9-4a10643ca966
// last-edited: 2026-11-10

package storage

import (
	"context"
	"errors"
	"fmt"
	"strconv"
)

// The level step (05-storage §5S.3.7 "The level step procedure"; 03 §3.8's
// flip mechanism). M7 is made of level steps: Breaking versioned steps with
// no writes of their own whose names encode the target level, books'
// books.level-<n> and core's core.level-<n> (names coined by F3-04; the spec
// names neither). Each flip that adds a cutover step adds one level step per
// owner whose level it raises, at that owner's next contiguous version
// immediately before the flip's own step, and never removes an earlier one.
// Nothing here registers one: F3-07 registers core's next to books' (F3-06).
//
// A level step runs through the ordinary versioned-step path: runStep stages
// its (empty) Up, the version and the applied record, and commitWithRaise
// writes .storage-levels (and, for an owner with a LegacyGuardProjection, the
// projection sidecar and the marker through LegacyGuardProjection.Stage)
// with fsync before the one stamp batch commits, alone, before any chunk of
// the cutover step that runs After it.

// ErrLevelStepRefused: RaiseLevel was called outside a writable boot's
// Migrate, for an owner the store does not serve, or above the build's
// supported level. It writes nothing.
var ErrLevelStepRefused = errors.New("storage: level step refused")

// LevelStepName is the level step's name for levelOwner's target level:
// "<levelOwner>.level-<level>", e.g. "core.level-3" or "books.level-2".
// levelOwner is a level owner ("core" for every core subsystem, a module ID
// otherwise), not a keyspace owner.
func LevelStepName(levelOwner Owner, level int) string {
	return string(levelOwner) + ".level-" + strconv.Itoa(level)
}

// LevelStep is one level step of levelOwner's half of M7: a Breaking
// versioned step at version, raising the level to level, whose Up writes
// nothing (the stamp batch is its only commit). after holds the steps it
// must follow; the cutover step it guards names this step in its own After.
// version and level are the flip's: version is the owner's next contiguous
// version, level the owner's new Spec.Level, which the same PR declares.
func LevelStep(levelOwner Owner, version, level int, after ...StepRef) Migration {
	return Migration{
		Version:     version,
		Name:        LevelStepName(levelOwner, level),
		Compat:      Breaking,
		RaisesLevel: level,
		After:       after,
		Up:          func(context.Context, *MigrationTx) error { return nil },
		levelStep:   true,
	}
}

// CoreLevelStep is core's own half of M7, the step core.level-<level> (name
// coined by F3-04): F3-07 registers it on core/storage's Spec.Migrations
// next to books' half, at core/storage's next contiguous version, and raises
// that Spec's Level to level in the same PR. Every core subsystem shares the
// one "core" level, so one core.level-<n> raises core's stamp for all of
// them.
func CoreLevelStep(version, level int, after ...StepRef) Migration {
	return LevelStep(coreLevelOwner, version, level, after...)
}

// RaiseLevel runs the level step procedure for owner outside a versioned
// step: the stamp batch holds only the stamp (and the owner's legacy
// projection and marker), committed after the sidecars, rewritten from the
// committed stamps when the commit fails. It is the data-migration runner's
// level-step seam (internal/storagemigrate Options.LevelStep): a data
// migration whose RaisesLevel is above the stored stamp raises it before its
// first chunk. A level at or below the stored stamp stamps nothing (a level
// is never lowered).
//
// It runs only inside a writable boot's Migrate, after Prepare took the
// boot's checkpoint, and refuses (ErrLevelStepRefused, nothing written) an
// owner the store does not serve or a level above the build's supported
// level for it: a stamp the build itself would refuse at its next boot.
func (db *DB) RaiseLevel(ctx context.Context, owner Owner, level int) (err error) {
	st := db.boot
	switch {
	case db.readOnly || st == nil:
		return fmt.Errorf("%w: %s to %d on a read-only store", ErrLevelStepRefused, owner, level)
	case !st.prepared || !st.migrated || st.finished:
		return fmt.Errorf("%w: %s to %d outside Migrate", ErrLevelStepRefused, owner, level)
	}
	o, ok := db.owners[owner]
	if !ok {
		return fmt.Errorf("%w: owner %s is not served by this store", ErrLevelStepRefused, owner)
	}
	lo := o.levelOwner()
	if level > st.supported[lo] {
		return fmt.Errorf("%w: %s to %d is above the build's supported level %d", ErrLevelStepRefused, lo, level, st.supported[lo])
	}
	if level <= st.stamps[lo] {
		return nil
	}
	ks := db.storageKS()
	b := newBatch(ks, false)
	defer func() { err = errors.Join(err, b.Close()) }()
	return db.commitWithRaise(ctx, b, o, level)
}
