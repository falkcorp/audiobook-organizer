// file: core/storage/swap.go
// version: 1.0.0
// guid: 965b855f-5738-408a-b043-7d62a94a43d8
// last-edited: 2026-11-07

package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
	ipebble "github.com/falkcorp/mediacore/internal/pebble"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// ErrRenameRefused: a swap target fails the rename pre-check (another
// filesystem, read-only, a mount point or a single-file bind mount; DATA-23,
// 05-storage §5S.3.15). Defined in the L0 leaf restorejournal, whose Validate
// returns it, and re-exported here as ErrSwapPending is.
var ErrRenameRefused = restorejournal.ErrRenameRefused //archtest:allow-var the spec's re-export of restorejournal's sentinel, compared with errors.Is

// RewriteSidecars rewrites the sidecars beside the CLOSED store at dbPath
// from that store's own committed stamps: the level sidecar (every owner of
// the build, 0 when it has no stamp, and every dormant owner's stamp) and
// each owner's legacy projection sidecar. The journaled swap calls it at
// step e (from the staged store, now in place) and at step x (from the
// original store, moved back), before any storage.Open of the boot
// (05-backup §5B.3.4; 05-storage §5S.3.16 machine 2's "restore step e"
// edge), so no reader of a sidecar ever sees the other store's levels.
//
// It opens the store read-only (no WAL flush, no OPTIONS file) and closes it
// before it writes. fs nil is vfs.Default. A directory that holds no store
// is an error.
func RewriteSidecars(ctx context.Context, fs vfs.FS, dbPath string, owners []OwnerSpec) error {
	if fs == nil {
		fs = vfs.Default
	}
	regs, claims, _, err := register(owners)
	if err != nil {
		return err
	}
	eng, err := ipebble.Open(dbPath, ipebble.Options{FS: fs, ReadOnly: true, ErrorIfNotExists: true})
	if err != nil {
		return fmt.Errorf("storage: sidecar rewrite of %s: %w", dbPath, err)
	}
	opts := OpenOptions{Path: dbPath, FS: FS{fs: fs}}
	opts.defaults()
	db := newDB(eng, opts, regs, claims, true)
	db.boot = &bootState{opts: opts}
	stamps, err := db.readStamps(ctx)
	if cerr := db.Close(); err != nil || cerr != nil {
		return errors.Join(err, cerr)
	}
	db.boot.stamps, db.boot.supported = stamps, db.supportedLevels()
	return db.writeSidecarsFor(stamps)
}

// adoptSeams takes the crash-matrix kit's per-boot seams from ctx: its
// filesystem (when the caller set no FS; defaults copies FS into fs) and its
// labeller. Production boots carry none.
func (o *OpenOptions) adoptSeams(ctx context.Context) {
	s := bootseam.From(ctx)
	if s == nil {
		return
	}
	if o.FS.fs == nil && s.FS != nil {
		o.FS = FS{fs: s.FS}
	}
	o.seams = s
}

// labelled runs fn inside label's crash-matrix region (06 §6.5.1's
// boot-writer table): every durable write, rename and fsync fn makes
// carries that label. A no-op wrapper outside the kit.
func (db *DB) labelled(label string, fn func() error) error {
	var s *bootseam.Seams
	if db.boot != nil {
		s = db.boot.opts.seams
	}
	return bootseam.Labelled(s, label, fn)
}

// The storage rows of 06 §6.5.1's boot-writer table, as the crash-matrix
// kit's labels (coretest/crashmatrix registers the same names).
const (
	labelFreshBatch       = "storage.fresh.batch"
	labelFreshSidecars    = "storage.fresh.sidecars"
	labelSweep            = "storage.checkpoint.sweep"
	labelCheckpointReuse  = "storage.checkpoint.reuse"
	labelCheckpointMkdir  = "storage.checkpoint.mkdir"
	labelCheckpointPebble = "storage.checkpoint.pebble"
	labelCheckpointState  = "storage.checkpoint.statefiles"
	labelCheckpointRename = "storage.checkpoint.rename"
	labelCheckpointRecord = "storage.checkpoint.record"
	labelCheckpointPtr    = "storage.checkpoint.pointer"
	labelMarkerBatch      = "storage.marker.batch"
	labelRatchet          = "storage.ratchet"
	labelStepBatch        = "storage.step.batch"
	labelLevelSidecars    = "storage.levelstep.sidecars"
	labelLevelRollback    = "storage.levelstep.rollback"
	labelFinishBatch      = "storage.finish.batch"
	labelFinishSidecars   = "storage.finish.sidecars"
	labelCheckpointDone   = "storage.checkpoint.complete"
	labelStranded         = "storage.checkpoint.stranded"
	labelAttemptClear     = "storage.attempt.clear"
	labelCheckpointReady  = "storage.checkpoint.ready"
)
