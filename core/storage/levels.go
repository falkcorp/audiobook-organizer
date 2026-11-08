// file: core/storage/levels.go
// version: 1.0.1
// guid: b872a575-c708-488a-9a3c-5f0dd05d0e70
// last-edited: 2026-11-07

package storage

import (
	"context"
	"errors"
	"fmt"

	ipebble "github.com/falkcorp/mediacore/internal/pebble"
	"github.com/falkcorp/mediacore/storage/codec"
)

func codecEpoch() codec.Codec[epochRecord] { return codec.JSON[epochRecord](1) }

// commitWithRaise is the level step procedure of §5S.3.7 for a versioned
// step whose writes and bookkeeping b already holds:
//
//  1. the boot's checkpoint exists (Prepare took it before any step ran);
//  2. new = max(stored, raises); when it equals stored, b commits as an
//     ordinary step and nothing is stamped. Otherwise the stamp (and, for an
//     owner with a LegacyGuardProjection, the projection and the marker
//     through its Stage) are staged into b, and the sidecars are written with
//     the new level and fsynced;
//  3. b commits;
//  4. when that commit fails, the sidecars are rewritten from the committed
//     stamps before returning, so a failed raise leaves them describing the
//     store (OPS-26b).
//
// A crash between 2 and 3 leaves a sidecar ahead of its stamp: an older
// build refuses (the guard reads max(stamp, sidecar)), and this build's next
// boot re-runs the step. A level is never lowered (DATA2-22).
func (db *DB) commitWithRaise(ctx context.Context, b *Batch, o *ownerInfo, raises int) error {
	st := db.boot
	lo := o.levelOwner()
	stored := st.stamps[lo]
	level := max(stored, raises)
	if level == stored {
		return db.labelled(labelStepBatch, func() error { return b.Commit(ctx, Sync) })
	}
	ks := db.storageKS()
	b.Bind(ks)
	raw, err := codec.JSON[stampRecord](1).Encode(stampRecord{Level: level, ByBuild: st.opts.Build, At: db.clock().UTC()})
	if err != nil {
		return err
	}
	if err := b.In(ks).Set(levelKey(lo), raw); err != nil {
		return err
	}
	if g := o.spec.LegacyGuard; g != nil {
		lr, _ := db.Legacy(o.owner)
		b.Bind(lr)
		if err := g.Stage(ctx, lr, b, g.projection(level), true); err != nil {
			return fmt.Errorf("storage: %s's legacy projection: %w", o.owner, err)
		}
	}
	next := maps0(st.stamps)
	next[lo] = level
	if err := db.labelled(labelLevelSidecars, func() error { return db.writeSidecarsFor(next) }); err != nil {
		return errors.Join(err, db.labelled(labelLevelRollback, db.writeSidecars))
	}
	if h := db.hooks.beforeStampCommit; h != nil {
		if err := h(); err != nil {
			return err // the crash tests stop here: sidecar written, stamp not
		}
	}
	if err := db.labelled(labelStepBatch, func() error { return b.Commit(ctx, Sync) }); err != nil {
		return errors.Join(fmt.Errorf("storage: level step for %s: %w", lo, err), db.labelled(labelLevelRollback, db.writeSidecars))
	}
	st.stamps[lo] = level
	return nil
}

// writeSidecars rewrites the sidecars from the committed stamps.
func (db *DB) writeSidecars() error { return db.writeSidecarsFor(db.boot.stamps) }

// writeSidecarsFor writes .storage-levels (every build owner, 0 when it has
// no stamp, plus every dormant owner's stamp) and, for each owner with a
// LegacyGuardProjection, its projection sidecar.
func (db *DB) writeSidecarsFor(stamps Levels) error {
	st := db.boot
	levels := map[string]int{}
	for lo := range st.supported {
		levels[string(lo)] = stamps[lo]
	}
	for lo, v := range stamps {
		levels[string(lo)] = v
	}
	fs := st.opts.fs
	if err := ipebble.WriteLevels(fs, st.opts.Path, levels); err != nil {
		return fmt.Errorf("storage: level sidecar: %w", err)
	}
	for _, o := range sortedInfos(db.owners) {
		if g := o.spec.LegacyGuard; g != nil {
			if err := ipebble.WriteInt(fs, st.opts.Path+g.SidecarSuffix, g.projection(stamps[o.levelOwner()])); err != nil {
				return fmt.Errorf("storage: %s's legacy projection sidecar: %w", o.owner, err)
			}
		}
	}
	return nil
}

// Levels reports, for every level owner the build serves, its committed
// level stamp (store) and the build's supported level (supported): the two
// kinds of <namespace>_storage_level (§5S.8, OPS2-24). After Migrate the
// store side holds every raise the boot committed.
func (db *DB) Levels() (store, supported Levels) {
	st := db.boot
	store = make(Levels, len(st.supported))
	for lo := range st.supported {
		store[lo] = st.stamps[lo]
	}
	return store, maps0(st.supported)
}

// Supported is the build's supported level vector, the --print-storage-levels
// output (§5S.3.7 "Fast-exit flags"): computed from the specs alone.
func Supported(owners []OwnerSpec) Levels {
	out := Levels{coreLevelOwner: 0}
	for _, os := range owners {
		lo := os.Owner
		if _, core, err := keyspacePrefix(os.Owner); err == nil && core {
			lo = coreLevelOwner
		}
		out[lo] = max(out[lo], os.Spec.Level)
	}
	return out
}
