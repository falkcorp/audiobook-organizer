// file: core/storage/dryrun.go
// version: 1.0.1
// guid: 40d09635-1676-462e-9a88-3dd03428e5a5
// last-edited: 2026-11-11

package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"iter"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	ipebble "github.com/falkcorp/mediacore/internal/pebble"
)

// The dry run (05-storage §5S.3.6 "Dry run", §5S.3.12 "Scratch copies";
// `storage migrate --dry-run`): the live store, opened ReadOnly (no WAL
// flush, no ratchet, no write), is copied into a new directory of the
// checkpoint area's scratch/ (SSTs hard-linked; WAL, MANIFEST and OPTIONS
// copied), the copy is opened as a full store and every pending step runs
// on it; the report says what a live run would do, and the scratch is
// deleted whatever the outcome. The kernel drives it (kernel.DryRun); this
// file is storage's half: the scratch directory, the copy and the report.

// ErrDryRunFreeSpace: the scratch area's filesystem has less free space
// than the dry run needs (the store's size for what the steps write, again
// for M8's copy when the list holds one, plus core.storage.free_space_margin).
var ErrDryRunFreeSpace = errors.New("storage: not enough free space for the dry run's scratch copy")

// dryRunPrefix names a dry run's directory in the scratch area.
const dryRunPrefix = "dryrun-"

// NewDryRunScratch creates a new directory for one dry run in the scratch
// area of o's checkpoint directory (<checkpoint dir>/.mediacore-checkpoints/
// scratch/dryrun-<UTC time>-<id>/), the area and the directory 0700, and
// returns it. The checkpoint sweeper deletes a scratch entry 7 days after
// its last change, so a run a crash interrupted does not stay.
func (o *Opened) NewDryRunScratch() (string, error) {
	st := o.db.boot
	area := filepath.Join(st.opts.cpDir, checkpointScratch)
	dir := filepath.Join(area, dryRunPrefix+st.opts.Clock().UTC().Format(checkpointStamp)+"-"+newInstallID()[:8])
	for _, d := range []string{st.opts.cpDir, area, dir} {
		if err := st.opts.fs.MkdirAll(d, 0o700); err != nil {
			return "", fmt.Errorf("storage: dry run scratch %s: %w", d, err)
		}
		if err := st.opts.chmod(d, true); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// CopyForDryRun copies the store o opened ReadOnly into dst, a directory
// that must not exist yet (copyStoreDir: SSTs hard-linked, WAL, MANIFEST,
// OPTIONS and markers copied, no WAL flush), so nothing is written to the
// live directory and no format is ratcheted; the copy's files are made 0600
// and its directories 0700. The directory is quiescent: the caller refused
// a store another process holds, and o's read-only engine writes nothing. Before it, the
// scratch filesystem's free space is checked against the store's size, once
// more when the list holds M8 (rewrite), plus the margin.
func (o *Opened) CopyForDryRun(ctx context.Context, dst string, rewrite bool) error {
	db := o.db
	st := db.boot
	if !db.readOnly {
		return errors.New("storage: the dry run copies a store opened ReadOnly")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	release, err := db.lease()
	if err != nil {
		return err
	}
	defer release()
	storeBytes, err := db.engine().DB().EstimateDiskUsage([]byte{0}, bytes.Repeat([]byte{0xff}, 8))
	if err != nil {
		return fmt.Errorf("storage: estimate the store's size: %w", err)
	}
	need := storeBytes + st.opts.FreeSpaceMargin
	if rewrite {
		need += storeBytes
	}
	avail, err := st.opts.freeSpace(existingAncestor(st.opts.fs, filepath.Dir(dst)))
	if err != nil {
		return fmt.Errorf("storage: free space for the dry run: %w", err)
	}
	if avail < need {
		return fmt.Errorf("%w: need %d bytes (store %d, margin %d, M8 copy %t), %d available",
			ErrDryRunFreeSpace, need, storeBytes, st.opts.FreeSpaceMargin, rewrite, avail)
	}
	if err := copyStoreDir(st.opts.fs, st.opts.Path, dst); err != nil {
		return fmt.Errorf("storage: dry run copy into %s: %w", dst, err)
	}
	if err := db.privateTree(dst); err != nil {
		return err
	}
	return ipebble.SyncDir(st.opts.fs, filepath.Dir(dst))
}

// copyStoreDir copies the quiescent Pebble directory src into dst, a new
// directory: each table hard-linked (vfs.LinkOrCopy; a table is immutable,
// and the copy's compactions unlink only the copy's link), every other file
// copied (the WAL, MANIFEST, OPTIONS and marker files, which the copy's
// engine appends to or replaces), LOCK skipped. Pebble's own Checkpoint
// cannot copy a read-only engine: it copies OPTIONS-<n> by the number the
// engine would have written, and a read-only open writes none.
func copyStoreDir(fs vfs.FS, src, dst string) error {
	if err := fs.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	names, err := fs.List(src)
	if err != nil {
		return err
	}
	slices.Sort(names)
	for _, n := range names {
		if n == "LOCK" {
			continue
		}
		from, to := filepath.Join(src, n), filepath.Join(dst, n)
		fi, err := fs.Stat(from)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			continue
		}
		if strings.HasSuffix(n, ".sst") {
			err = vfs.LinkOrCopy(fs, from, to)
		} else {
			err = vfs.Copy(fs, from, to)
		}
		if err != nil {
			return fmt.Errorf("copy %s: %w", n, err)
		}
	}
	return ipebble.SyncDir(fs, dst)
}

// NewDryRunScratchAt is NewDryRunScratch for a run with no live store open
// (--from-archive): the scratch area of the store at dbPath under
// checkpointDir ("" = the DB's parent), with Open's checks of the
// checkpoint directory.
func NewDryRunScratchAt(fs FS, dbPath, checkpointDir string, now time.Time) (string, error) {
	o := OpenOptions{Path: dbPath, CheckpointDir: checkpointDir, FS: fs, Clock: func() time.Time { return now }}
	o.defaults()
	if err := o.checkCheckpointDir(); err != nil {
		return "", err
	}
	db := &DB{boot: &bootState{opts: o}}
	return (&Opened{db: db}).NewDryRunScratch()
}

// EstimateSource yields the rows of a data migration's Source over o, read
// only, for the dry run's --estimate: each key relative to the migration's
// source handle (owner's keyspace when the range lies in it, else its
// legacy range, absolute), as the runner hands them to Step.
func (o *Opened) EstimateSource(ctx context.Context, owner Owner, src []Range) iter.Seq2[KV, error] {
	return func(yield func(KV, error) bool) {
		info := o.db.owners[owner]
		for _, r := range src {
			if info != nil && info.prefix != nil && bytes.HasPrefix(r.Start, info.prefix) {
				ks, _ := o.db.Keyspace(owner)
				opts := ScanOptions{Start: r.Start[len(info.prefix):]}
				if bytes.HasPrefix(r.End, info.prefix) {
					opts.End = r.End[len(info.prefix):]
				}
				for kv, err := range ks.Scan(ctx, opts) {
					if !yield(kv, err) || err != nil {
						return
					}
				}
				continue
			}
			lr, ok := o.db.Legacy(owner)
			if !ok {
				yield(KV{}, fmt.Errorf("storage: owner %s has no keyspace or legacy range holding %q", owner, r.Start))
				return
			}
			for kv, err := range lr.Scan(ctx, ScanOptions{Start: r.Start, End: r.End}) {
				if !yield(kv, err) || err != nil {
					return
				}
			}
		}
	}
}

// DryRunReport is what `storage migrate --dry-run` prints: the blockers
// first, then per step the counts per outcome, the held items, the rows it
// would quarantine with their reasons, the bytes it wrote and the first keys
// per outcome; M8's copy; the run's duration. An --estimate report counts
// over the live store with every write discarded and says so.
type DryRunReport struct {
	Estimate bool
	Source   string // the live store's path, or the archive
	Blockers []CutoverBlocker
	Steps    []DryRunStep
	Rewrite  *RewriteCopyStats
	Took     time.Duration
	Err      string // the error that stopped the run, "" when it completed
}

// DryRunStep is one data migration's part of a dry run.
type DryRunStep struct {
	ID         string
	State      string
	Items      map[string]int64    // per outcome
	First      map[string][]string // the first keys per outcome, printable
	Held       []DryRunItem
	Quarantine []DryRunItem
	Bytes      int64 // bytes the step staged through its StepWriter
	Took       time.Duration
}

// DryRunItem is a held or would-quarantine row: its key and reason, never
// its value.
type DryRunItem struct {
	Key       string
	Reason    string
	Sensitive bool
}

// Lines renders the report, blockers first, and never a value.
func (r *DryRunReport) Lines() []string {
	var out []string
	kind := "dry run"
	if r.Estimate {
		kind = "ESTIMATE (in-batch discard over the live store; no step wrote anything, importer writes not run)"
	}
	out = append(out, fmt.Sprintf("storage migrate --dry-run: %s of %s", kind, r.Source))
	out = append(out, CutoverBlockerReport(r.Blockers)...)
	for _, s := range r.Steps {
		out = append(out, fmt.Sprintf("step %s: state=%s bytes_written=%d took=%s", s.ID, s.State, s.Bytes, s.Took.Round(time.Millisecond)))
		for _, o := range slices.Sorted(maps.Keys(s.Items)) {
			line := fmt.Sprintf("  %s: %d", o, s.Items[o])
			if first := s.First[o]; len(first) > 0 {
				line += " (first: " + strings.Join(first, ", ") + ")"
			}
			out = append(out, line)
		}
		for _, h := range s.Held {
			out = append(out, fmt.Sprintf("  held: key=%s reason=%q", h.Key, h.Reason))
		}
		for _, q := range s.Quarantine {
			out = append(out, fmt.Sprintf("  would quarantine: key=%s reason=%q sensitive=%t", q.Key, q.Reason, q.Sensitive))
		}
	}
	if r.Rewrite != nil {
		out = append(out, fmt.Sprintf("M8 store rewrite: %d keys, %d bytes, %d tables copied into scratch; nothing swapped", r.Rewrite.Keys, r.Rewrite.Bytes, r.Rewrite.Tables))
	}
	if r.Err != "" {
		out = append(out, "stopped: "+r.Err)
	}
	out = append(out, fmt.Sprintf("took %s; the scratch copy is deleted", r.Took.Round(time.Millisecond)))
	return out
}

// PrintableKey renders a key for an operator's report (a tuple key by its
// segments, else quoted bytes).
func PrintableKey(k []byte) string { return printableKey(k) }
