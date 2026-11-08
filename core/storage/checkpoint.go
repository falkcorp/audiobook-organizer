// file: core/storage/checkpoint.go
// version: 1.0.1
// guid: 32e3978a-4fec-4d22-bab1-48d0d64226d4
// last-edited: 2026-11-08

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
	"maps"
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

// ErrCheckpointDirOverlap: core.storage.checkpoint_dir puts the checkpoint
// directory at, inside or around the store's own directory, where the
// garbage sweep could delete the live store. Open refuses it before any
// write.
var ErrCheckpointDirOverlap = errors.New("storage: the checkpoint directory overlaps the store")

// checkpointLeaf is the checkpoint directory's name under
// core.storage.checkpoint_dir (§5S.3.12 "Where").
const checkpointLeaf = ".mediacore-checkpoints"

// checkpointStamp is the layout of a checkpoint name's UTC time.
const checkpointStamp = "20060102T150405Z"

// isCheckpointName reports a name in the checkpoint grammar,
// <YYYYMMDD>T<HHMMSS>Z-<8 lowercase hex>, the only names takeCheckpoint
// writes and so the only ones the sweep and the reuse rule may touch.
func isCheckpointName(s string) bool {
	if len(s) != len(checkpointStamp)+1+8 {
		return false
	}
	for i, c := range s {
		switch {
		case i == 8:
			if c != 'T' {
				return false
			}
		case i == 15:
			if c != 'Z' {
				return false
			}
		case i == 16:
			if c != '-' {
				return false
			}
		case i < 16:
			if c < '0' || c > '9' {
				return false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return false
			}
		}
	}
	return true
}

// checkCheckpointDir refuses a relative core.storage.checkpoint_dir and a
// checkpoint directory that equals the store's directory, holds it or lies
// inside it.
func (o *OpenOptions) checkCheckpointDir() error {
	if o.CheckpointDir != "" && !filepath.IsAbs(o.CheckpointDir) {
		return fmt.Errorf("storage: core.storage.checkpoint_dir %q is not absolute", o.CheckpointDir)
	}
	leaf, store := filepath.Clean(o.cpDir), filepath.Clean(o.Path)
	if leaf == store || within(leaf, store) || within(store, leaf) {
		return fmt.Errorf("%w: core.storage.checkpoint_dir %q puts the checkpoints at %s, and the store is at %s",
			ErrCheckpointDirOverlap, o.CheckpointDir, leaf, store)
	}
	return nil
}

// within reports whether path lies strictly inside dir (both clean and
// absolute).
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

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

// checkpointRecord is <name>.json (§5S.3.12 "How"). DirFSID and DirIno are
// the DB directory's identity at the boot that took it (osDirIdentity: the
// filesystem ID and the inode), the identity its epoch was read against: with
// Epoch they name the store the copy came from.
type checkpointRecord struct {
	InstallID   string            `json:"install_id"`
	Epoch       uint64            `json:"epoch"`
	DirFSID     uint64            `json:"dir_fsid"`
	DirIno      uint64            `json:"dir_ino"`
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

// pending reports a record whose attempt is unfinished: open or served, not
// completed, released or left by a failed restore.
func (r checkpointRecord) pending() bool {
	return r.CompletedAt == nil && r.DeleteAfter == nil && r.Status != "restore_left"
}

// open reports a pending record no boot has served past: the one kind a
// crash loop reuses.
func (r checkpointRecord) open() bool { return r.pending() && r.ReadyAt == nil }

// ofStore reports a record taken of the store in e's generation: the same
// epoch and the same DB directory identity. A different marker-less store
// swapped in at the path lands on the same epoch number (neither store has
// an epoch row yet) but not on the directory's identity, so it never adopts
// the record's install ID or reuses its checkpoint. Off unix the identity is
// 0/0 and the match falls back to the epoch alone.
func (r checkpointRecord) ofStore(e epochRecord) bool {
	return r.Epoch == e.Epoch && r.DirFSID == e.DirFSID && r.DirIno == e.DirIno
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

// checkpointRecords reads every <name>.json in the checkpoint directory. A
// record that cannot be read fails the call; one removed since the listing
// is skipped, and one that reads but does not parse is logged and skipped.
func (db *DB) checkpointRecords() ([]namedRecord, error) {
	st := db.boot
	fs := st.opts.fs
	names, err := fs.List(st.opts.cpDir)
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
		if !ok || !isCheckpointName(base) {
			continue
		}
		data, ok, err := ipebble.ReadFile(fs, filepath.Join(st.opts.cpDir, n))
		if err != nil {
			// An EIO or a permission error: skipping the record would take
			// a second full checkpoint beside it, or orphan it under a new
			// install ID, so the caller refuses (review MINOR-1).
			return nil, fmt.Errorf("storage: checkpoint record %s: %w", n, err)
		}
		if !ok {
			continue // removed since the listing
		}
		var rec checkpointRecord
		if json.Unmarshal(data, &rec) != nil {
			// Records are written atomically, so this is a hand edit or
			// corruption; it is never reused or completed.
			db.log.Error("storage: checkpoint record does not parse; never reused", "name", base)
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
	maps.Copy(v.Versions, st.versions)
	for lo := range st.supported {
		v.Levels[lo] = st.stamps[lo]
	}
	return v
}

// sweepGarbage removes checkpoint directories without a record and
// <name>.tmp directories (a crash before the record), and drops records
// whose directory is gone (§5S.3.12 "How"). It touches only names in the
// checkpoint grammar (isCheckpointName), so the scratch area and anything
// else in the directory stay.
func (db *DB) sweepGarbage() error {
	st := db.boot
	fs := st.opts.fs
	dir := st.opts.cpDir
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
		base, isRecord := strings.CutSuffix(n, ".json")
		base, isTmp := strings.CutSuffix(base, ".tmp")
		if !isCheckpointName(base) || (isRecord && isTmp) {
			// The scratch area, or a name takeCheckpoint never writes: not
			// the sweep's to delete, whatever the directory holds.
			continue
		}
		switch {
		case isRecord:
			if !has[base] {
				db.log.Warn("storage: checkpoint record without its directory dropped", "name", n)
				if err := fs.Remove(p); err != nil {
					return fmt.Errorf("storage: drop record %s: %w", n, err)
				}
			}
		case isTmp || !has[base+".json"]:
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
	for _, r := range slices.Backward(recs) {
		if r.rec.InstallID == st.installID && r.rec.ofStore(st.epoch) && r.rec.open() && r.rec.FromVector.ancestorOf(cur) {
			st.cpName = r.name
			db.log.Info("storage: reusing the upgrade attempt's checkpoint", "name", r.name)
			return db.writePointer(checkpointPointer{Name: r.name, State: "open"})
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fs := st.opts.fs
	dir := st.opts.cpDir
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
	name := now.Format(checkpointStamp) + "-" + newInstallID()[:8]
	tmp := filepath.Join(dir, name+".tmp")
	if err := db.mkdirPrivate(dir); err != nil {
		return err
	}
	if err := db.mkdirPrivate(tmp); err != nil {
		return err
	}
	// A copy that fails checkpointInto's read-only check stays behind as
	// <name>.tmp, which the next boot's sweep removes.
	if err := db.checkpointInto(filepath.Join(tmp, "db")); err != nil {
		return err
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
		InstallID: st.installID, Epoch: st.epoch.Epoch, DirFSID: st.epoch.DirFSID, DirIno: st.epoch.DirIno,
		FromVector: cur, FromRelease: st.opts.FromRelease, StateFiles: files, TakenAt: now, Status: "open",
	}
	if err := db.writeRecord(name, rec); err != nil {
		return err
	}
	st.cpName = name
	db.log.Info("storage: pre-migration checkpoint taken", "name", name, "bytes", storeBytes)
	return db.writePointer(checkpointPointer{Name: name, State: "open"})
}

// errCopyRefused marks a checkpoint copy checkpointInto made and refused: it
// did not open read-only, or it would not close after the check. The copy is
// the caller's to leave for the sweep or to remove.
var errCopyRefused = errors.New("is refused")

// checkpointInto writes a Pebble checkpoint of the store into dir, which must
// not exist, and refuses a copy that does not open read-only (F2; §5S.3.12
// "How"). The boot's pre-migration checkpoint and Admin.Checkpoint (05-backup's
// archive, the dry run's scratch copy) both take their copy here.
//
// Pebble v2.1.7's DB.Checkpoint copies as many bytes of the MANIFEST as this
// process's MANIFEST writer has written (checkpoint.go), and Pebble makes that
// writer at the process's first version edit. Open writes one whenever it
// replays a WAL, even an empty one, but a store with no WAL left to replay (a
// kill inside Open after the MANIFEST marker moved and before the new WAL's
// directory entry was durable) opens without one: its copy's MANIFEST would
// be empty and the copy unopenable. A Flush applies a version edit even with
// an empty memtable, so the copy taken after it holds a full MANIFEST
// (TestEmptyFlushRewritesTheManifest pins both halves of that reading of
// Pebble). The read-only open is the guard that does not depend on it: a copy
// that fails it, or will not close after it, is an error wrapping
// errCopyRefused, and the caller leaves it for the sweep or removes it.
func (db *DB) checkpointInto(dir string) error {
	release, err := db.lease()
	if err != nil {
		return err
	}
	if err = db.eng.DB().Flush(); err != nil {
		err = fmt.Errorf("flush before it: %w", err)
	} else {
		err = db.eng.DB().Checkpoint(dir, cpebble.WithFlushedWAL())
	}
	release()
	if err != nil {
		return fmt.Errorf("storage: checkpoint: %w", err)
	}
	eng, err := ipebble.Open(dir, ipebble.Options{FS: db.eng.FS(), ReadOnly: true, ErrorIfNotExists: true, Logger: db.log})
	if err != nil {
		return fmt.Errorf("storage: checkpoint copy %s does not open read-only, so it %w. The live store is untouched. "+
			"Check the filesystem that holds the copy for I/O errors and free space; if every attempt is refused, "+
			"report it with Pebble's error: %w", dir, errCopyRefused, err)
	}
	if err := eng.Close(db.set.closeTimeout); err != nil {
		return fmt.Errorf("storage: checkpoint copy %s %w: it opened read-only but did not close: %w", dir, errCopyRefused, err)
	}
	return nil
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
	p := filepath.Join(st.opts.cpDir, name+".json")
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
	found, err := db.completeRecord(st.cpName, now, true)
	if err == nil && !found {
		err = fmt.Errorf("storage: the attempt's checkpoint record %s is gone", st.cpName)
	}
	return err
}

// completeStranded completes the checkpoint c/storage/attempt names: an
// attempt whose step 9 batch committed, so every step it ran is applied, but
// whose completion a kill cut off. It writes checkpoint files only. A record
// already completed, or gone (pruned by hand), needs nothing; the caller
// deletes the key either way.
func (db *DB) completeStranded() error {
	st := db.boot
	if st.attempt == "" {
		return nil
	}
	found, err := db.completeRecord(st.attempt, st.opts.Clock().UTC(), false)
	if err != nil {
		return fmt.Errorf("storage: completing the attempt a killed boot left open: %w", err)
	}
	if !found {
		db.log.Warn("storage: the checkpoint record of a stranded upgrade attempt is gone; clearing the attempt", "name", st.attempt)
		return nil
	}
	db.log.Info("storage: completed the upgrade attempt a killed boot left open", "name", st.attempt)
	return nil
}

// completeRecord completes the named record, unless it is no longer pending
// (already completed, released or restore_left), then the pointer: always
// when latest (the attempt this boot ran), otherwise only when the pointer
// still names that checkpoint and says open, so a stranded attempt's
// resolution never repoints a newer attempt's pointer. It reports whether
// the record exists. Each step is idempotent, so a kill anywhere in it is
// finished by the next call.
func (db *DB) completeRecord(name string, now time.Time, latest bool) (found bool, err error) {
	recs, err := db.checkpointRecords()
	if err != nil {
		return false, err
	}
	i := slices.IndexFunc(recs, func(r namedRecord) bool { return r.name == name })
	if i < 0 {
		return false, nil
	}
	rec := recs[i].rec
	if rec.pending() {
		del := now.Add(checkpointRetention)
		rec.CompletedAt, rec.DeleteAfter, rec.Status = &now, &del, "completed"
		if err := db.writeRecord(name, rec); err != nil {
			return true, err
		}
	}
	if rec.CompletedAt == nil {
		return true, nil // released or restore_left: not this attempt's to complete
	}
	if !latest {
		p, ok, err := db.pointer()
		if err != nil || !ok || p.Name != name || p.State == "completed" {
			return true, err
		}
	}
	return true, db.writePointer(checkpointPointer{Name: name, State: "completed", DeleteAfter: rec.DeleteAfter})
}

// pointer reads <db path>.migration-checkpoint; ok is false when it is absent.
func (db *DB) pointer() (p checkpointPointer, ok bool, err error) {
	st := db.boot
	path := st.opts.Path + ipebble.CheckpointSuffix
	data, ok, err := ipebble.ReadFile(st.opts.fs, path)
	if err != nil || !ok {
		return p, false, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, false, fmt.Errorf("storage: %s unreadable: %w", path, err)
	}
	return p, true, nil
}

// CheckpointName is the checkpoint this boot took or reused; "" when none.
func (db *DB) CheckpointName() string { return db.boot.cpName }

// MarkServed is §5S.3.12's served write ("Upgrade attempt and reuse"): the
// first boot that hands the store to module code after a checkpoint was taken
// writes ready_at = now and status served onto every open record of the live
// store (this boot's install_id, epoch and DB directory identity), each
// through WriteFileAtomic. From then on the checkpoint predates writes this
// boot's modules make, so no later boot reuses it: the next upgrade takes its
// own, and restoring this one needs --accept-loss. The pointer is left alone
// (served is a state of the record only). A store with no open record writes
// nothing, and a second call changes nothing. The kernel calls it at the
// start of Run, before any module hook or worker starts, so a boot that dies
// after a module wrote has still marked the record; a DB whose Migrate did
// not finish is refused.
func (db *DB) MarkServed(now time.Time) error {
	st := db.boot
	if st == nil || !st.finished {
		return errors.New("storage: MarkServed on a store whose boot did not finish Migrate")
	}
	recs, err := db.checkpointRecords()
	if err != nil {
		return err
	}
	now = now.UTC()
	for _, r := range recs {
		if r.rec.InstallID != st.installID || !r.rec.ofStore(st.epoch) || !r.rec.open() {
			continue
		}
		r.rec.ReadyAt, r.rec.Status = &now, "served"
		if err := db.writeRecord(r.name, r.rec); err != nil {
			return err
		}
		db.log.Info("storage: an open upgrade checkpoint was served past; it will not be reused", "name", r.name)
	}
	return nil
}
