// file: core/storage/checkpoints.go
// version: 1.0.0
// guid: 7b3f2c18-9e4a-4d61-8a57-2f6c0d9e4b13
// last-edited: 2026-11-08

package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"time"
)

// statusConsumed is machine 3's consumed (§5S.3.16): storage
// restore-offline swapped the checkpoint in, so its db/ became the live
// store and the record names no copy.
const statusConsumed = "consumed"

// ErrCheckpointGone: the pointer names a checkpoint whose record is gone
// (pruned by hand, or swept), so there is nothing to restore.
var ErrCheckpointGone = errors.New("storage: the checkpoint the pointer names has no record")

// Checkpoint is one pre-migration checkpoint record as storage
// restore-offline reads it (§5S.3.12 "How", "Sources", "Loss"), with the
// store closed.
type Checkpoint struct {
	Name        string
	Dir         string // <checkpoints>/<name>: the store copy in db/, the state files in state/<label>
	InstallID   string
	Epoch       uint64
	Status      string // open, served, completed, restore_left or consumed
	TakenAt     time.Time
	ReadyAt     *time.Time
	CompletedAt *time.Time
	DeleteAfter *time.Time
	HooksOwed   bool // the record carries hooks_owed (DATA-7)
	StateFiles  []CheckpointStateFile
}

// CheckpointStateFile is one state file a checkpoint copied (05-config
// §2.4's table): the copy is Dir/state/<Label>, Path is the resolved
// original path, SHA256 the copy's hex digest.
type CheckpointStateFile struct {
	Label  string
	Path   string
	SHA256 string
}

// Store is the checkpoint's store copy.
func (c Checkpoint) Store() string { return filepath.Join(c.Dir, "db") }

// StateCopy is the copy of the state file labelled label.
func (c Checkpoint) StateCopy(label string) string { return filepath.Join(c.Dir, "state", label) }

// Restorable reports a checkpoint whose store copy is still the one the
// record describes: open, served or completed. A consumed one became a
// live store, a released or restore_left one is waiting for the sweeper.
func (c Checkpoint) Restorable() bool {
	switch c.Status {
	case "open", "served":
		return c.DeleteAfter == nil // released: the store moved on or was restored
	case "completed":
		return true
	}
	return false
}

// NeedsAcceptLoss is §5S.3.12 "Loss": restoring a served or completed
// checkpoint discards every write made since it was taken, so it needs
// --accept-loss; only an open one (ready_at empty, nothing served past it)
// does not.
func (c Checkpoint) NeedsAcceptLoss() bool {
	return c.Status != "open" || c.ReadyAt != nil || c.CompletedAt != nil
}

func checkpointOf(dir, name string, r checkpointRecord) Checkpoint {
	c := Checkpoint{
		Name: name, Dir: filepath.Join(dir, name), InstallID: r.InstallID, Epoch: r.Epoch, Status: r.Status,
		TakenAt: r.TakenAt, ReadyAt: r.ReadyAt, CompletedAt: r.CompletedAt, DeleteAfter: r.DeleteAfter,
		HooksOwed: len(r.HooksOwed) > 0 && string(r.HooksOwed) != "null",
	}
	for _, f := range r.StateFiles {
		c.StateFiles = append(c.StateFiles, CheckpointStateFile(f))
	}
	return c
}

// Checkpoints is the checkpoint directory and the pointer of the store at
// one DB path, read and written without opening the store: storage
// restore-offline runs with the service stopped, and on a store too broken
// to open (§5S.3.12). It is the sweeper's checkpoint area (cpArea), so it
// reads and writes records as the sweeper and prune-checkpoints do and
// shares their lock (Lock).
type Checkpoints struct{ a *cpArea }

// CheckpointsOf is the checkpoints of the store at dbPath (absolute), in
// <checkpointDir or the DB's parent>/.mediacore-checkpoints, with Open's
// refusal of a checkpoint directory that overlaps the store
// (ErrCheckpointDirOverlap). log nil discards.
func CheckpointsOf(fs FS, dbPath, checkpointDir string, log *slog.Logger) (*Checkpoints, error) {
	if !filepath.IsAbs(dbPath) {
		return nil, fmt.Errorf("storage: core.storage.path %q is not absolute", dbPath)
	}
	o := OpenOptions{Path: dbPath, CheckpointDir: checkpointDir, FS: fs, Logger: log}
	o.defaults()
	if err := o.checkCheckpointDir(); err != nil {
		return nil, err
	}
	return &Checkpoints{a: &cpArea{fs: o.fs, dbPath: o.Path, cpDir: o.cpDir, log: o.Logger, chmod: o.chmod, walk: osModeWalk(o.fs)}}, nil
}

// Lock takes the checkpoint directory's sweep lock, which the hourly
// sweeper and storage prune-checkpoints take (§5S.3.12 "Retention"),
// waiting for one in progress as prune does, so neither deletes a
// checkpoint storage restore-offline is reading, copying or swapping in,
// nor races its record update. A directory that does not exist takes no
// lock. The caller calls unlock once it no longer reads the checkpoint.
func (c *Checkpoints) Lock(ctx context.Context) (unlock func(), err error) {
	unlock, _, err = c.a.lockWithin(ctx, pruneLockWait)
	return unlock, err
}

// Dir is the checkpoint directory, .mediacore-checkpoints.
func (c *Checkpoints) Dir() string { return c.a.cpDir }

// Scratch is the scratch area <Dir>/scratch (§5S.3.12 "Scratch copies"),
// where an --into copy goes.
func (c *Checkpoints) Scratch() string { return filepath.Join(c.a.cpDir, checkpointScratch) }

// Latest is the checkpoint of the store's latest upgrade attempt
// (§5S.3.12 "Sources"): the one <db path>.migration-checkpoint names.
// Every attempt writes the pointer when it takes or reuses its checkpoint,
// and every swap (a restore, a reset) sets the pointer aside with the store
// it describes, so the pointer is that store's latest attempt and an older
// checkpoint is never it. ok is false when there is no pointer (no attempt
// since the store was made or swapped in); a pointer whose record is gone
// is ErrCheckpointGone.
func (c *Checkpoints) Latest() (cp Checkpoint, ok bool, err error) {
	p, ok, err := readPointerFile(c.a.fs, c.a.pointerPath())
	if err != nil || !ok {
		return Checkpoint{}, false, err
	}
	recs, err := readRecords(c.a.fs, c.a.cpDir, c.a.log)
	if err != nil {
		return Checkpoint{}, false, err
	}
	i := slices.IndexFunc(recs, func(r namedRecord) bool { return r.name == p.Name })
	if i < 0 {
		return Checkpoint{}, false, fmt.Errorf("%w: %s (pointer %s)", ErrCheckpointGone, p.Name, c.a.pointerPath())
	}
	return checkpointOf(c.a.cpDir, p.Name, recs[i].rec), true, nil
}

// Consume is machine 3's open, served or completed --> consumed (§5S.3.16):
// storage restore-offline calls it once its journal reached swapped or
// applied_offline (DATA-6). delete_after is now, so the sweeper's first
// pass removes the record and the state copies left in its directory. A
// record already consumed is left as it is.
func (c *Checkpoints) Consume(name string, now time.Time) error {
	return c.update(name, func(r *checkpointRecord) bool {
		if r.Status == statusConsumed {
			return false
		}
		now = now.UTC()
		r.Status = statusConsumed
		if r.DeleteAfter == nil {
			r.DeleteAfter = &now
		}
		return true
	})
}

// Release is machine 3's open --> released at a swap-back (§5S.3.16): step
// x moved the checkpoint's store copy into restore/<id>/failed/, so the
// record must never be restored or reused again; delete_after = now + 7
// days, written once (CONFORM-2), and status released, as the sweeper
// writes a release.
func (c *Checkpoints) Release(name string, now time.Time) error {
	return c.update(name, func(r *checkpointRecord) bool {
		if r.DeleteAfter != nil {
			return false
		}
		del := now.UTC().Add(checkpointRetention)
		r.DeleteAfter = &del
		if r.CompletedAt == nil && r.Status != "restore_left" {
			r.Status = "released"
		}
		return true
	})
}

func (c *Checkpoints) update(name string, fn func(*checkpointRecord) bool) error {
	recs, err := readRecords(c.a.fs, c.a.cpDir, c.a.log)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(recs, func(r namedRecord) bool { return r.name == name })
	if i < 0 {
		return fmt.Errorf("%w: %s", ErrCheckpointGone, name)
	}
	rec := recs[i].rec
	if !fn(&rec) {
		return nil
	}
	return c.a.writeRecordAt(c.a.cpDir, name, rec)
}
