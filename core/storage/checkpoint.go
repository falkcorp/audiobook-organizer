// file: core/storage/checkpoint.go
// version: 1.0.0
// guid: 32e3978a-4fec-4d22-bab1-48d0d64226d4
// last-edited: 2026-11-07

package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	cpebble "github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	ipebble "github.com/falkcorp/mediacore/internal/pebble"
)

// ErrCheckpointFreeSpace: the DB's filesystem lacks room for the
// pre-migration checkpoint (§5S.3.12 "Free space"); boot halts before any
// write, and the message carries the numbers.
var ErrCheckpointFreeSpace = errors.New("storage: not enough free space for the pre-migration checkpoint")

// ErrCheckpointDevice: the checkpoint directory is on another filesystem than
// the DB, so its hard links cannot be made (§5S.3.12 "Where").
var ErrCheckpointDevice = errors.New("storage: the checkpoint directory is not on the DB's filesystem; set core.storage.checkpoint_dir")

// scratchDir is the scratch area under the checkpoint directory; the
// record-less-directory rule does not apply to it (§5S.3.12 "Scratch copies").
const scratchDir = "scratch"

// vector is a checkpoint's from-vector: every owner's applied version, every
// level and the Pebble format (§5S.3.12 "Upgrade attempt and reuse").
type vector struct {
	Versions map[Owner]int `json:"versions"`
	Levels   map[Owner]int `json:"levels"`
	Format   uint64        `json:"format"`
}

// ancestorOf reports whether v is at or below cur in every component.
func (v vector) ancestorOf(cur vector) bool {
	for o, n := range v.Versions {
		if cur.Versions[o] < n {
			return false
		}
	}
	for o, n := range v.Levels {
		if cur.Levels[o] < n {
			return false
		}
	}
	return v.Format <= cur.Format
}

type stateFileRecord struct {
	Label  string `json:"label"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// checkpointRecord is <name>.json (§5S.3.12 "How").
type checkpointRecord struct {
	InstallID   string            `json:"install_id"`
	Epoch       uint64            `json:"epoch"`
	FromVector  vector            `json:"from_vector"`
	FromRelease string            `json:"from_release"`
	JournalSeq  uint64            `json:"journal_seq"`
	HooksOwed   json.RawMessage   `json:"hooks_owed,omitempty"`
	StateFiles  []stateFileRecord `json:"state_files"`
	TakenAt     time.Time         `json:"taken_at"`
	ReadyAt     *time.Time        `json:"ready_at,omitempty"`
	CompletedAt *time.Time        `json:"completed_at,omitempty"`
	DeleteAfter *time.Time        `json:"delete_after,omitempty"`
	Status      string            `json:"status"` // open, served, completed, restore_left
}

// open reports a record that is still an upgrade attempt's rollback point.
func (r checkpointRecord) open() bool {
	return r.CompletedAt == nil && r.DeleteAfter == nil && r.Status != "restore_left" && r.ReadyAt == nil
}

// checkpointPointer is <db path>.migration-checkpoint.
type checkpointPointer struct {
	Name        string     `json:"name"`
	State       string     `json:"state"` // open or completed
	DeleteAfter *time.Time `json:"delete_after,omitempty"`
}

type namedRecord struct {
	name string
	rec  checkpointRecord
}

// checkpointRecords reads every <name>.json in the checkpoint directory.
func (db *DB) checkpointRecords() ([]namedRecord, error) {
	st := db.boot
	fs := st.opts.fs
	names, err := fs.List(st.opts.CheckpointDir)
	if err != nil {
		if oserrorNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("storage: list checkpoints: %w", err)
	}
	slices.Sort(names)
	var out []namedRecord
	for _, n := range names {
		base, ok := strings.CutSuffix(n, ".json")
		if !ok {
			continue
		}
		data, ok, err := ipebble.ReadFile(fs, filepath.Join(st.opts.CheckpointDir, n))
		if err != nil || !ok {
			continue
		}
		var rec checkpointRecord
		if json.Unmarshal(data, &rec) != nil {
			db.log.Error("storage: checkpoint record unreadable; never reused", "name", base)
			continue
		}
		out = append(out, namedRecord{name: base, rec: rec})
	}
	return out, nil
}

// oserrorNotExist matches a missing file on the real and the in-memory
// filesystems (pebble's MemFS wraps os.ErrNotExist).
func oserrorNotExist(err error) bool { return errors.Is(err, os.ErrNotExist) }

// currentVector is the live store's vector.
func (db *DB) currentVector() vector {
	st := db.boot
	v := vector{Versions: map[Owner]int{}, Levels: map[Owner]int{}, Format: uint64(db.eng.FormatMajorVersion())}
	for o, n := range st.versions {
		v.Versions[o] = n
	}
	for lo := range st.supported {
		v.Levels[lo] = st.stamps[lo]
	}
	return v
}

// sweepGarbage removes checkpoint directories without a record and
// <name>.tmp directories (a crash before the record), and drops records
// whose directory is gone (§5S.3.12 "How"; the scratch area excepted).
func (db *DB) sweepGarbage() error {
	st := db.boot
	fs := st.opts.fs
	dir := st.opts.CheckpointDir
	names, err := fs.List(dir)
	if err != nil {
		if oserrorNotExist(err) {
			return nil
		}
		return fmt.Errorf("storage: list checkpoints: %w", err)
	}
	has := map[string]bool{}
	for _, n := range names {
		has[n] = true
	}
	for _, n := range names {
		p := filepath.Join(dir, n)
		switch {
		case n == scratchDir:
		case strings.HasSuffix(n, ".json"):
			if !has[strings.TrimSuffix(n, ".json")] {
				db.log.Warn("storage: checkpoint record without its directory dropped", "name", n)
				if err := fs.Remove(p); err != nil {
					return fmt.Errorf("storage: drop record %s: %w", n, err)
				}
			}
		case strings.HasSuffix(n, ".tmp") || !has[n+".json"]:
			if fi, err := fs.Stat(p); err != nil || !fi.IsDir() {
				continue
			}
			db.log.Warn("storage: partial checkpoint swept", "name", n)
			if err := fs.RemoveAll(p); err != nil {
				return fmt.Errorf("storage: sweep %s: %w", n, err)
			}
		}
	}
	return ipebble.SyncDir(fs, dir)
}

// takeCheckpoint is §5S.4 step 6's checkpoint: sweep the garbage, reuse the
// attempt's checkpoint when the reuse rule admits it, else check the headroom
// and the device and take one: Checkpoint with WithFlushedWAL into
// <name>.tmp/db, the state files into <name>.tmp/state, modes 0700/0600,
// rename, directory fsync, the record, then .migration-checkpoint.
func (db *DB) takeCheckpoint(ctx context.Context) error {
	st := db.boot
	st.opts.ExtendStart("storage.checkpoint")
	if err := db.sweepGarbage(); err != nil {
		return err
	}
	recs, err := db.checkpointRecords()
	if err != nil {
		return err
	}
	cur := db.currentVector()
	for i := len(recs) - 1; i >= 0; i-- {
		r := recs[i]
		if r.rec.InstallID == st.installID && r.rec.Epoch == st.epoch.Epoch && r.rec.open() && r.rec.FromVector.ancestorOf(cur) {
			st.cpName = r.name
			db.log.Info("storage: reusing the upgrade attempt's checkpoint", "name", r.name)
			return db.writePointer(checkpointPointer{Name: r.name, State: "open"})
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fs := st.opts.fs
	dir := st.opts.CheckpointDir
	// Headroom: the live store + the state files + the margin.
	release, err := db.lease()
	if err != nil {
		return err
	}
	storeBytes, err := db.eng.DB().EstimateDiskUsage([]byte{0}, bytes.Repeat([]byte{0xff}, 8))
	release()
	if err != nil {
		return fmt.Errorf("storage: estimate the store's size: %w", err)
	}
	var stateBytes uint64
	for _, f := range st.opts.StateFiles {
		fi, err := fs.Stat(f.Path)
		if err != nil {
			if oserrorNotExist(err) {
				continue
			}
			return fmt.Errorf("storage: state file %s (%s): %w", f.Label, f.Path, err)
		}
		stateBytes += uint64(fi.Size())
	}
	need := storeBytes + stateBytes + st.opts.FreeSpaceMargin
	avail, err := st.opts.freeSpace(filepath.Dir(st.opts.Path))
	if err != nil {
		return fmt.Errorf("storage: free space on the DB's filesystem: %w", err)
	}
	if avail < need {
		return fmt.Errorf("%w: need %d bytes (store %d + state files %d + margin %d), %d available",
			ErrCheckpointFreeSpace, need, storeBytes, stateBytes, st.opts.FreeSpaceMargin, avail)
	}
	same, err := st.opts.sameDevice(st.opts.Path, existingAncestor(fs, dir))
	if err != nil {
		return fmt.Errorf("storage: checkpoint device check: %w", err)
	}
	if !same {
		return fmt.Errorf("%w (%s)", ErrCheckpointDevice, dir)
	}
	now := st.opts.Clock().UTC()
	name := now.Format("20060102T150405Z") + "-" + newInstallID()[:8]
	tmp := filepath.Join(dir, name+".tmp")
	if err := db.mkdirPrivate(dir); err != nil {
		return err
	}
	if err := db.mkdirPrivate(tmp); err != nil {
		return err
	}
	release, err = db.lease()
	if err != nil {
		return err
	}
	err = db.eng.DB().Checkpoint(filepath.Join(tmp, "db"), cpebble.WithFlushedWAL())
	release()
	if err != nil {
		return fmt.Errorf("storage: checkpoint: %w", err)
	}
	files, err := db.copyStateFiles(filepath.Join(tmp, "state"))
	if err != nil {
		return err
	}
	if err := db.privateTree(tmp); err != nil {
		return err
	}
	final := filepath.Join(dir, name)
	if err := fs.Rename(tmp, final); err != nil {
		return fmt.Errorf("storage: checkpoint rename: %w", err)
	}
	if err := ipebble.SyncDir(fs, dir); err != nil {
		return err
	}
	rec := checkpointRecord{
		InstallID: st.installID, Epoch: st.epoch.Epoch, FromVector: cur, FromRelease: st.opts.FromRelease,
		StateFiles: files, TakenAt: now, Status: "open",
	}
	if err := db.writeRecord(name, rec); err != nil {
		return err
	}
	st.cpName = name
	db.log.Info("storage: pre-migration checkpoint taken", "name", name, "bytes", storeBytes)
	return db.writePointer(checkpointPointer{Name: name, State: "open"})
}

// existingAncestor is dir or its nearest existing parent.
func existingAncestor(fs vfs.FS, dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := fs.Stat(d); err == nil || filepath.Dir(d) == d {
			return d
		}
	}
}

// mkdirPrivate creates dir (and missing parents) and sets it to 0700.
func (db *DB) mkdirPrivate(dir string) error {
	if err := db.boot.opts.fs.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("storage: mkdir %s: %w", dir, err)
	}
	return db.boot.opts.chmod(dir, true)
}

// privateTree sets every directory under root to 0700 and every file to 0600.
func (db *DB) privateTree(root string) error {
	fs := db.boot.opts.fs
	fi, err := fs.Stat(root)
	if err != nil {
		return fmt.Errorf("storage: stat %s: %w", root, err)
	}
	if !fi.IsDir() {
		return db.boot.opts.chmod(root, false)
	}
	if err := db.boot.opts.chmod(root, true); err != nil {
		return err
	}
	names, err := fs.List(root)
	if err != nil {
		return fmt.Errorf("storage: list %s: %w", root, err)
	}
	for _, n := range names {
		if err := db.privateTree(filepath.Join(root, n)); err != nil {
			return err
		}
	}
	return nil
}

// copyStateFiles copies each existing state file into dir/<label>, fsynced,
// and returns its record: label, resolved original path, SHA-256.
func (db *DB) copyStateFiles(dir string) ([]stateFileRecord, error) {
	st := db.boot
	fs := st.opts.fs
	if err := db.mkdirPrivate(dir); err != nil {
		return nil, err
	}
	var out []stateFileRecord
	for _, f := range st.opts.StateFiles {
		src, err := fs.Open(f.Path)
		if err != nil {
			if oserrorNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("storage: state file %s (%s): %w", f.Label, f.Path, err)
		}
		dst, err := fs.Create(filepath.Join(dir, f.Label), vfs.WriteCategoryUnspecified)
		if err != nil {
			_ = src.Close()
			return nil, fmt.Errorf("storage: checkpoint state file %s: %w", f.Label, err)
		}
		// The hash sees each chunk before the file does: a vfs.File may take
		// ownership of a written buffer (pebble's MemFS scribbles over it
		// under its invariants build), so hashing after the write would
		// record bytes that were never copied.
		h := sha256.New()
		_, cerr := io.Copy(io.MultiWriter(h, dst), src)
		err = errors.Join(cerr, dst.Sync(), dst.Close(), src.Close())
		if err != nil {
			return nil, fmt.Errorf("storage: copy state file %s (%s): %w", f.Label, f.Path, err)
		}
		out = append(out, stateFileRecord{Label: f.Label, Path: f.Path, SHA256: hex.EncodeToString(h.Sum(nil))})
	}
	return out, ipebble.SyncDir(fs, dir)
}

func (db *DB) writeRecord(name string, rec checkpointRecord) error {
	st := db.boot
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(st.opts.CheckpointDir, name+".json")
	if err := ipebble.WriteFileAtomic(st.opts.fs, p, append(data, '\n')); err != nil {
		return fmt.Errorf("storage: checkpoint record: %w", err)
	}
	return st.opts.chmod(p, false)
}

func (db *DB) writePointer(p checkpointPointer) error {
	st := db.boot
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	path := st.opts.Path + ipebble.CheckpointSuffix
	if err := ipebble.WriteFileAtomic(st.opts.fs, path, append(data, '\n')); err != nil {
		return fmt.Errorf("storage: %s: %w", ipebble.CheckpointSuffix, err)
	}
	return st.opts.chmod(path, false)
}

// completeCheckpoint writes completed_at and delete_after = completed_at + 7
// days on the attempt's checkpoint once every step that ran finished
// (§5S.3.12 "Retention").
func (db *DB) completeCheckpoint(now time.Time) error {
	st := db.boot
	if st.cpName == "" {
		return nil
	}
	recs, err := db.checkpointRecords()
	if err != nil {
		return err
	}
	for _, r := range recs {
		if r.name != st.cpName {
			continue
		}
		del := now.Add(checkpointRetention)
		r.rec.CompletedAt, r.rec.DeleteAfter, r.rec.Status = &now, &del, "completed"
		if err := db.writeRecord(r.name, r.rec); err != nil {
			return err
		}
		return db.writePointer(checkpointPointer{Name: r.name, State: "completed", DeleteAfter: &del})
	}
	return fmt.Errorf("storage: the attempt's checkpoint record %s is gone", st.cpName)
}

// CheckpointName is the checkpoint this boot took or reused; "" when none.
func (db *DB) CheckpointName() string { return db.boot.cpName }
