// file: core/storage/restorejournal/createfs.go
// version: 1.0.2
// guid: 69d7e9b2-f1ed-433f-bf6d-12935c2dd31e
// last-edited: 2026-11-08

package restorejournal

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// The files of one swap under <stateDir>/restore/<id>/ (05-backup §5B.3.4,
// 05-storage §5S.3.15). Every stager and the swapper name them through
// these, so the in-flight probe of the rollback guard, of the kernel and of
// restoreswap reads one layout.
const (
	PendingName  = pendingName  // restore/pending.json: the one intent slot
	InflightName = inflightName // restore/<id>/inflight.json: the swap has started
	RejectedName = "rejected.json"
	JournalName  = "journal.json" // JSON Lines, one Record per line
)

// Dir is <stateDir>/restore, the directory of the slot and every journal.
func Dir(stateDir string) string { return filepath.Join(stateDir, restoreDir) }

// PendingPath is the one intent slot, <stateDir>/restore/pending.json.
func PendingPath(stateDir string) string { return filepath.Join(Dir(stateDir), pendingName) }

// JournalDir is <stateDir>/restore/<id>, which holds the swap's journal, its
// in-flight or rejected file and the state-directory set-aside.
func JournalDir(stateDir, id string) string { return filepath.Join(Dir(stateDir), id) }

// CreateIntentFS is CreateIntent through fs: the one create of the slot.
// CreateIntent is CreateIntentFS over vfs.Default; tests and the crash
// matrix stage through an in-memory fs with the same code.
//
// Order: restore/ created and synced into the state directory, the
// in-flight probe (ErrSwapPending while any swap is in flight), a temp file
// written and fsynced, link(2) to pending.json (EEXIST → ErrSwapPending, the
// temp removed), the temp removed, the directory fsynced. link(2) is the
// exclusion between writers, across processes, with no mutex: two creates
// race on one name and exactly one wins. The probe and the link are two
// steps, so a P0a that renames pending.json to inflight.json between them is
// not seen: stage only after P0a or with the service stopped (CreateIntent).
//
// Once the link succeeded the result says what P0a will find (R1-21 review
// m1): a directory fsync that fails then is undone (pending.json taken back
// and the directory synced) and reported; when the take-back cannot be made
// durable either, the intent is put back and the result wraps
// ErrIntentUnsynced: it is staged, and a crash may still lose it.
func CreateIntentFS(fs vfs.FS, stateDir string, in Intent) error {
	if stateDir == "" || !filepath.IsAbs(stateDir) {
		return fmt.Errorf("restorejournal: state directory %q is not an absolute path", stateDir)
	}
	if !ValidID(in.ID) {
		return fmt.Errorf("restorejournal: intent id %q is not one path segment", in.ID)
	}
	dir := Dir(stateDir)
	if err := mkdirSyncedFS(fs, dir); err != nil {
		return err
	}
	if ids, err := InFlightIDs(fs, stateDir); err != nil {
		return err
	} else if len(ids) > 0 {
		return fmt.Errorf("%w (in flight: %s)", ErrSwapPending, ids[0])
	}
	b, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("restorejournal: encode intent: %w", err)
	}
	tmp, err := writeTempFS(fs, dir, b)
	if err != nil {
		return err
	}
	pending := filepath.Join(dir, pendingName)
	linkErr := fs.Link(tmp, pending)
	rmErr := fs.Remove(tmp)
	switch {
	case errors.Is(linkErr, os.ErrExist):
		return errors.Join(ErrSwapPending, tempLeft(tmp, rmErr))
	case linkErr != nil:
		return errors.Join(fmt.Errorf("restorejournal: link %s: %w", pending, linkErr), tempLeft(tmp, rmErr))
	}
	// The intent is linked and durable once the directory is synced. A temp
	// name that would not go away is a stray dot-file beside it, not a failed
	// create, so it does not turn a created intent into an error the caller
	// would retry.
	if err := syncDirFS(fs, dir); err != nil {
		return takeBack(fs, dir, pending, err)
	}
	return nil
}

// ErrIntentUnsynced: CreateIntentFS linked the intent into the slot and
// could neither make the link durable nor take it back durably, so the
// intent stands, staged, and a crash may lose it (R1-21 review m1). A caller
// reports the create as done, with a warning, never as failed: P0a applies
// what the slot holds.
var ErrIntentUnsynced = errors.New("restorejournal: the intent is staged but its directory was not synced")

// takeBack undoes a linked intent whose directory fsync (cause) failed:
// pending.json is renamed to a dot-name that no reader opens and the
// directory is synced, so its absence is durable and the create fails with
// cause; the dot-name is then removed. When that sync fails too the intent
// is renamed back, so the slot holds what the result says (ErrIntentUnsynced).
// A rename back that fails leaves the slot free with an error result; a
// crash before any later sync may still bring the intent back, which three
// failed operations in one directory can only report.
func takeBack(fs vfs.FS, dir, pending string, cause error) error {
	aside, err := tempName(dir)
	if err != nil {
		return errors.Join(ErrIntentUnsynced, cause, err)
	}
	if err := fs.Rename(pending, aside); err != nil {
		return errors.Join(ErrIntentUnsynced, cause, fmt.Errorf("restorejournal: take back %s: %w", pending, err))
	}
	if err := syncDirFS(fs, dir); err != nil {
		if rerr := fs.Rename(aside, pending); rerr != nil {
			return errors.Join(cause, err, fmt.Errorf("restorejournal: put back %s (the slot is free; a crash may restore it): %w", pending, rerr))
		}
		return errors.Join(ErrIntentUnsynced, cause, err)
	}
	return errors.Join(cause, tempLeft(aside, fs.Remove(aside)))
}

// mkdirSyncedFS creates dir and its missing parents (0700) through fs and
// fsyncs the parent of every directory it created, so restore/ is in the
// state directory before an intent is linked into it (R1-21 review m2).
func mkdirSyncedFS(fs vfs.FS, dir string) error {
	var created []string
	for d := filepath.Clean(dir); filepath.Dir(d) != d; d = filepath.Dir(d) {
		if _, err := fs.Stat(d); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("restorejournal: stat %s: %w", d, err)
		}
		created = append(created, d)
	}
	if len(created) == 0 {
		return nil
	}
	if err := fs.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("restorejournal: create %s: %w", dir, err)
	}
	for _, d := range created {
		if err := syncDirFS(fs, filepath.Dir(d)); err != nil {
			return err
		}
	}
	return nil
}

// ValidID reports an intent id that names a directory of its own under
// restore/: one path segment, not empty, "." or "..". Every reader and
// writer of an intent checks it, since restore/.. is the state directory.
func ValidID(id string) bool {
	return id != "" && id != "." && id != ".." && filepath.Base(id) == id
}

// InFlightIDs lists the ids of every restore/<id>/inflight.json under
// stateDir, sorted: the one in-flight probe (05-storage §5S.3.7 "reader
// check", the sudoers find of the rollback guard). At most one exists under
// the one-slot rule; a caller that finds more halts. A missing restore
// directory is no swap, and a file in it (restore/rejected-<time>.json, a
// temp) holds none: only a directory is probed, so the probe does not lean
// on each filesystem's error for a path through a file.
func InFlightIDs(fs vfs.FS, stateDir string) ([]string, error) {
	dir := Dir(stateDir)
	names, err := fs.List(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("restorejournal: probe %s: %w", dir, err)
	}
	var ids []string
	for _, n := range names {
		if n == pendingName || n[0] == '.' {
			continue
		}
		if di, err := fs.Stat(filepath.Join(dir, n)); errors.Is(err, os.ErrNotExist) || (err == nil && !di.IsDir()) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("restorejournal: probe %s: %w", filepath.Join(dir, n), err)
		}
		fi, err := fs.Stat(filepath.Join(dir, n, inflightName))
		switch {
		case err == nil && !fi.IsDir():
			ids = append(ids, n)
		case err == nil, errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		default:
			return nil, fmt.Errorf("restorejournal: probe %s: %w", filepath.Join(dir, n), err)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// writeTempFS writes b to a new temp file in dir through fs, fsynced and
// closed; mode 0600 on the real filesystem (vfs.FS has no mode).
func writeTempFS(fs vfs.FS, dir string, b []byte) (string, error) {
	name, err := tempName(dir)
	if err != nil {
		return "", err
	}
	f, err := fs.Create(name, vfs.WriteCategoryUnspecified)
	if err != nil {
		return "", fmt.Errorf("restorejournal: create temp intent: %w", err)
	}
	var cherr error
	if vfs.Root(fs) == vfs.Default {
		cherr = os.Chmod(name, 0o600)
	}
	_, werr := f.Write(b)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(cherr, werr, serr, cerr); err != nil {
		return "", errors.Join(fmt.Errorf("restorejournal: write temp intent: %w", err), fs.Remove(name))
	}
	return name, nil
}

// tempName is a new dot-name for an intent file in dir: InFlightIDs and P0a
// skip a name starting with a dot.
func tempName(dir string) (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("restorejournal: temp name: %w", err)
	}
	return filepath.Join(dir, "."+pendingName+".tmp-"+hex.EncodeToString(suffix[:])), nil
}

// syncDirFS fsyncs dir through fs so a create, link or remove in it is
// durable.
func syncDirFS(fs vfs.FS, dir string) error {
	d, err := fs.OpenDir(dir)
	if err != nil {
		return fmt.Errorf("restorejournal: open %s: %w", dir, err)
	}
	serr := d.Sync()
	cerr := d.Close()
	if err := errors.Join(serr, cerr); err != nil {
		return fmt.Errorf("restorejournal: fsync %s: %w", dir, err)
	}
	return nil
}
