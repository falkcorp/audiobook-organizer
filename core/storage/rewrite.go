// file: core/storage/rewrite.go
// version: 1.0.4
// guid: 7959a601-c65e-4e8c-bdfe-6fedd766d21e
// last-edited: 2026-11-11

package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"

	cpebble "github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/objstorage/objstorageprovider"
	"github.com/cockroachdb/pebble/v2/sstable"

	ipebble "github.com/falkcorp/mediacore/internal/pebble"
	"github.com/falkcorp/mediacore/storage/codec"
)

// M8, the store rewrite (05-storage §5S.3.13 "M8: the rewrite"; 05-backup
// §5B.3.4 "M8's store rewrite is KindRewrite of this swap"). It runs last in
// the R2 cutover's list, once, over everything the final R2 build migrated:
//
//  1. compact: flush, then Compact over the whole range, parallelized;
//  2. copy: every live key into <db parent>/<db name>.rewrite.tmp/ by SST
//     ingest, at the store's own format, with c/storage/epoch rewritten to
//     the same generation and the new directory's identity; fsync; rename to
//     <db name>.rewrite/; fsync the parent; M8's record "copied";
//  3. swap: restorejournal.KindRewrite in process (core/internal/restoreswap):
//     the engine closed, the old directory set aside, the copy moved into
//     place, the sidecars rewritten, the engine reopened;
//  4. delete: the set-aside directory at once, M8 done, the journal applied.
//
// The phases' order and their derivation from M8's own journals are the
// phase engine's (restoreswap/rewrite.go); this file is storage's half: the
// declaration, the record, the compaction, the copy and the in-process
// reopen.

// RewriteStep declares M8 (name coined by F3-04): its owner, its one data
// migration ID for all of R2 and the steps it runs after. books' M8
// definition (F3-06) is NewRewriteStep("books", "books.rewrite"); F3-07
// registers it last (kernel DataMigrationsConfig.Rewrite), and a later flip
// inserts its own steps before it and never renames, replaces or removes it
// (D15).
type RewriteStep struct {
	Owner Owner
	ID    string
	After []StepRef
}

// NewRewriteStep is M8's invocation API (name coined here, by F3-04): the
// declaration books' M8 definition builds, with the one fixed ID.
func NewRewriteStep(owner Owner, id string, after ...StepRef) RewriteStep {
	return RewriteStep{Owner: owner, ID: id, After: after}
}

// RewriteCopied is the M8 record state the copy carries (§5S.3.13 phase 2:
// "The copy carries M8's state at phase copied"): the live store reads it
// only once the copy serves, between the swap and phase 4. The original's
// record stays pending until the swap, so a boot that did not swap always
// copies again, from the store as it is then. The DAG treats M8 as pending
// until DataMigrationDone.
const RewriteCopied = "copied"

// The crash-matrix labels of M8's storage half (06 §6.5.1's boot-writer
// table, M8's phases).
const (
	LabelRewriteState   = "storage.rewrite.state"
	LabelRewriteCompact = "storage.rewrite.compact"
	LabelRewriteCopy    = "storage.rewrite.copy"
	LabelRewriteClose   = "storage.rewrite.close"
	LabelRewriteReopen  = "storage.rewrite.reopen"
	// LabelRewriteCheckpoint: the attempt's checkpoint record, given the
	// copy's identity before the swap and promoted to it after.
	LabelRewriteCheckpoint = "storage.rewrite.checkpoint"
)

// RewriteLabels are every label of M8's storage half.
func RewriteLabels() []string {
	return []string{LabelRewriteState, LabelRewriteCompact, LabelRewriteCopy, LabelRewriteClose, LabelRewriteReopen, LabelRewriteCheckpoint}
}

// AttemptCheckpoint is the name of this boot's upgrade-attempt checkpoint
// (taken or reused at P4a; "" when Prepare took none). M8 names it in its
// KindRewrite intent and counts only that attempt's swap-backs, so a store
// restored from the checkpoint is a new attempt. Name coined by F3-04.
func (db *DB) AttemptCheckpoint() string {
	if db.boot == nil {
		return ""
	}
	return db.boot.cpName
}

// rewriteCopyOwed reports whether P4a's headroom owes M8's copy
// (§5S.3.12 "Free space"): the plan holds M8 and its record on the live
// store is still pending. A store that reads copied is the copy, swapped in
// by a boot a kill stopped before phase 4 (its KindRewrite journal at
// swapped or opened): the copy's bytes are spent already.
func (db *DB) rewriteCopyOwed(ctx context.Context) (bool, error) {
	for _, n := range db.boot.plan {
		if n.data == nil || !n.data.Rewrite {
			continue
		}
		state, err := db.RewriteRecord(ctx, n.data.ID)
		if err != nil {
			return false, err
		}
		return state == "", nil
	}
	return false, nil
}

// RewriteDir is the finished copy's directory beside the store at dbPath:
// <db parent>/<db name>.rewrite. Its temporary form adds ".tmp".
func RewriteDir(dbPath string) string { return filepath.Clean(dbPath) + ".rewrite" }

// rewriteSSTBytes is the size at which the copy starts a new SST.
const rewriteSSTBytes = 64 << 20

// How often M8 asks the service manager for more start time while it works.
// The unit is real progress, never a clock (05-health §5H.3.4: a wedged step
// must still be killed once its own sdnotify.StartupStepBudget, 10 min, runs
// out), so each constant is a measure of work, and each is far finer than the
// budget. Prod's store at the first flip held 74.2 GB in 173.5 M keys and
// 1106 tables, and the prod-copy rehearsal's dry run copied it in 46 to 69
// min, 18 to 27 MB/s. At that rate a table of rewriteSSTBytes takes 2.5 to
// 3.7 s and rewriteExtendBytes under 1 s, and at a hundredth of the slowest
// pass (0.18 MB/s) rewriteExtendBytes still takes 93 s, inside the budget.
// rewriteExtendKeys covers a store of tiny values, where the byte count moves
// slowly while every key still costs a read; it is 100,000 keys, 1.6 to 2.4 s
// at the measured 42,000 to 63,000 keys/s. A call is one datagram write:
// about 5,500 of them over that whole copy.
const (
	rewriteExtendBytes = 16 << 20
	rewriteExtendKeys  = 100_000
)

// The ExtendStart step names of M8's storage half: fixed tokens, never a key,
// a path or a value.
const (
	stepRewriteCompact = "storage.rewrite.compact"
	stepRewriteCopy    = "storage.rewrite.copy"
	stepRewriteSwap    = "storage.rewrite.swap"
)

// extendStart asks for start time before a step of M8 that makes progress. A
// DB without a boot (the offline open's) has no start timeout to extend.
// Callers are one goroutine at a time: ExtendStart is not safe to call
// concurrently (the kernel's start window is not), so a worker that learns of
// progress hands it to the goroutine running the step.
func (db *DB) extendStart(step string) {
	if db.boot != nil {
		db.boot.opts.ExtendStart(step)
	}
}

// RewriteRecord is M8's record state ("" when it has none).
func (db *DB) RewriteRecord(ctx context.Context, id string) (string, error) {
	raw, err := db.storageKS().Get(ctx, DataMigrationKey(id))
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	rec, err := DataMigrationCodec().Decode(raw)
	if err != nil {
		return "", fmt.Errorf("storage: M8 record %s: %w", id, err)
	}
	return rec.State, nil
}

// SetRewriteRecord commits M8's record state, Sync.
func (db *DB) SetRewriteRecord(ctx context.Context, id, state string) error {
	raw, err := DataMigrationCodec().Encode(DataMigrationRecord{State: state, At: db.clock().UTC(), Build: db.buildID()})
	if err != nil {
		return err
	}
	return db.labelled(LabelRewriteState, func() error { return db.storageKS().Set(ctx, DataMigrationKey(id), raw) })
}

// rewriteCompactBytes is the table data one compaction chunk covers (the sum
// of the sizes of the tables whose first key falls in the chunk, over every
// level, so a key range that has versions at several levels counts each).
// Prod's store held 1106 tables over 74.2 GB, about 67 MB each, so a chunk is
// about 8 tables and the store about 150 chunks. The prod-copy rehearsal's
// copy ran at 18 to 27 MB/s, which puts a chunk at 20 to 30 s, a tenth of
// the 5 min the rehearsal gate allows between two extensions; a chunk that
// takes longer than sdnotify.StartupStepBudget (under 0.9 MB/s, twenty times
// slower than the slowest measured) is wedged for the budget's purpose and is
// killed, not extended.
const rewriteCompactBytes = 512 << 20

// compactRanges splits the key space [start, end] into consecutive closed
// ranges at table boundaries, each covering about chunkBytes of table data. A
// store with no table, or one smaller than a chunk, is one range. The ranges
// overlap by at most the boundary key, so a table that starts exactly on a
// boundary can be compacted by both neighbours, which costs one table per
// boundary and changes no result. They are computed once, after the flush, so
// every key is in the tables listed.
func compactRanges(db *cpebble.DB, start, end []byte, chunkBytes int64) ([][2][]byte, error) {
	levels, err := db.SSTables()
	if err != nil {
		return nil, err
	}
	type table struct {
		first []byte
		size  int64
	}
	var tables []table
	for _, lvl := range levels {
		for _, t := range lvl {
			tables = append(tables, table{first: t.Smallest.UserKey, size: int64(t.Size)})
		}
	}
	slices.SortFunc(tables, func(a, b table) int { return bytes.Compare(a.first, b.first) })
	var ranges [][2][]byte
	from, acc := start, int64(0)
	for _, t := range tables {
		// A cut at a table's first key, once the chunk holds enough; a key
		// at or before the chunk's own start, or at or after the end, would
		// make an empty or inverted range (Pebble refuses start >= end).
		if acc >= chunkBytes && bytes.Compare(t.first, from) > 0 && bytes.Compare(t.first, end) < 0 {
			ranges = append(ranges, [2][]byte{from, bytes.Clone(t.first)})
			from, acc = bytes.Clone(t.first), 0
		}
		acc += t.size
	}
	return append(ranges, [2][]byte{from, end}), nil
}

// RewriteCompact is M8's phase 1: flush, then compact the whole key range
// parallelized, in chunks. Pebble's Compact reports no progress and one call
// over a store of tens of GB outlasts sdnotify.StartupStepBudget, so the
// compaction is cut into compactRanges and the step asks for start time
// before the flush and before each chunk, on this goroutine, as each chunk's
// Compact returns: the request follows the step's own work, never a clock and
// never an engine event.
//
// Why not an engine event (the table-created event of #4989's first cut): the
// event listener sees every flush and compaction of the engine, the
// background ones included, and Pebble (v2.1.7) gives no way to tell the
// manual compaction's from them, so a wedged Compact beside any background
// work would be kept alive for good. The sources, all in
// github.com/cockroachdb/pebble/v2@v2.1.7:
//   - compaction.go:494-503, makeInfo: CompactionInfo.Reason is
//     c.kind.String(), and a manual compaction has no kind of its own
//     (compaction.go:139-157, compactionKindDefault to
//     compactionKindBlobFileRewrite): it is "default", or "move" when it
//     only relinks tables (compaction_picker.go:2005-2008 picks it as an
//     ordinary pickedTableCompaction with manualID set), the same as an
//     automatic one.
//   - compaction_picker.go:231, ManualID, is the one marker, and nothing
//     exported carries it: CompactionInfo (event.go:203-231) has JobID,
//     Reason, Input, Output, Duration, Annotations and no manual field.
//   - event.go:482-489, TableCreateInfo is JobID, Reason ("compacting",
//     "flushing" or "ingesting"), Path, FileNum; its JobID joins to a
//     CompactionBegin's, which is no help when the begin cannot be told
//     apart; and compaction.go:3568 emits it from every flush and compaction
//     alike.
//   - a key-range test is vacuous: the range is the whole key space, so every
//     background compaction lies inside it (db.go:1825, Compact; db.go:1925-1932, manualCompact).
//   - a move compaction (a manual one over tables that do not overlap) writes
//     no table at all (compaction.go:3215-3216, runCompaction calls
//     runMoveCompaction, which relinks; compact1, compaction.go:2687-2742,
//     emits only CompactionBegin and CompactionEnd around it), so a table
//     event could not show progress there even for a manual compaction.
//
// A chunk's Compact still parallelizes inside its range (parallelize=true,
// db.go:1825 splitManualCompaction), and the chunks' union is the whole range,
// so the result is the same as one call.
func (db *DB) RewriteCompact(ctx context.Context) error {
	return db.labelled(LabelRewriteCompact, func() error {
		release, err := db.lease()
		if err != nil {
			return err
		}
		defer release()
		pdb := db.engine().DB()
		db.extendStart(stepRewriteCompact)
		if err := pdb.Flush(); err != nil {
			return fmt.Errorf("storage: rewrite flush: %w", err)
		}
		start, end := fullRange()
		chunkBytes := int64(rewriteCompactBytes)
		if db.boot != nil && db.boot.opts.rewriteCompactBytes > 0 {
			chunkBytes = db.boot.opts.rewriteCompactBytes
		}
		ranges, err := compactRanges(pdb, start, end, chunkBytes)
		if err != nil {
			return fmt.Errorf("storage: rewrite compact: list the tables: %w", err)
		}
		compact := func(ctx context.Context, pdb *cpebble.DB, from, to []byte) error {
			return pdb.Compact(ctx, from, to, true)
		}
		if db.boot != nil && db.boot.opts.compactRange != nil {
			compact = db.boot.opts.compactRange
		}
		for _, r := range ranges {
			db.extendStart(stepRewriteCompact)
			if err := compact(ctx, pdb, r[0], r[1]); err != nil {
				return fmt.Errorf("storage: rewrite compact: %w", err)
			}
		}
		return nil
	})
}

// RewriteCopyStats is what a copy wrote.
type RewriteCopyStats struct {
	Keys, Bytes int64
	Tables      int
}

// RewriteCopy is M8's phase 2 into final: every live key of the store, by
// SST ingest, into final+".tmp" (removed first with any earlier attempt's
// final), a fresh store at the store's own format with c/storage/epoch
// rewritten to this boot's generation and the new directory's identity
// (which the renames keep), fsynced, then renamed to final and the parent
// fsynced. The copy also carries M8's record id at RewriteCopied. The live
// store is read under one snapshot and never written. The dry run copies
// into its own scratch directory the same way.
func (db *DB) RewriteCopy(ctx context.Context, final, id string) (stats RewriteCopyStats, err error) {
	st := db.boot
	if st == nil || db.readOnly {
		return stats, errors.New("storage: the store rewrite needs a writable boot")
	}
	err = db.labelled(LabelRewriteCopy, func() error {
		var err error
		stats, err = db.rewriteCopy(ctx, final, id)
		return err
	})
	return stats, err
}

func (db *DB) rewriteCopy(ctx context.Context, final, id string) (stats RewriteCopyStats, err error) {
	st := db.boot
	fs := st.opts.fs
	tmp, ingest := final+".tmp", final+".ingest"
	for _, p := range []string{tmp, final, ingest} {
		// An earlier attempt's partial or finished copy can be tens of GB:
		// each removal asks for its own budget.
		db.extendStart(stepRewriteCopy)
		if err := fs.RemoveAll(p); err != nil {
			return stats, fmt.Errorf("storage: rewrite: remove %s: %w", p, err)
		}
	}
	if err := db.mkdirPrivate(tmp); err != nil {
		return stats, err
	}
	if err := db.mkdirPrivate(ingest); err != nil {
		return stats, err
	}
	fsid, ino, err := st.opts.dirIdentity(tmp)
	if err != nil {
		return stats, fmt.Errorf("storage: rewrite: %s's identity: %w", tmp, err)
	}
	dst, err := ipebble.Open(tmp, ipebble.Options{FS: fs, CacheBytes: st.opts.CacheBytes, MemtableBytes: st.opts.MemtableBytes, Logger: st.opts.Logger})
	if err != nil {
		return stats, fmt.Errorf("storage: rewrite: open the copy: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, dst.Close(st.opts.CloseTimeout))
		}
	}()
	if f := db.engine().FormatMajorVersion(); f > dst.FormatMajorVersion() {
		if err := dst.DB().RatchetFormatMajorVersion(f); err != nil {
			return stats, fmt.Errorf("storage: rewrite: the copy's format: %w", err)
		}
	}
	paths, err := db.writeTables(ctx, dst, ingest, &stats)
	if err != nil {
		return stats, err
	}
	if len(paths) > 0 {
		db.extendStart(stepRewriteCopy)
		if err := dst.DB().Ingest(ctx, paths); err != nil {
			return stats, fmt.Errorf("storage: rewrite: ingest: %w", err)
		}
	}
	ep := st.epoch
	ep.DirFSID, ep.DirIno = fsid, ino
	raw, err := codec.JSON[epochRecord](1).Encode(ep)
	if err != nil {
		return stats, err
	}
	abs := append(append([]byte(nil), db.storageKS().prefix...), epochKey()...)
	if err := dst.DB().Set(abs, raw, cpebble.Sync); err != nil {
		return stats, fmt.Errorf("storage: rewrite: the copy's generation: %w", err)
	}
	m8, err := DataMigrationCodec().Encode(DataMigrationRecord{State: RewriteCopied, At: db.clock().UTC(), Build: db.buildID()})
	if err != nil {
		return stats, err
	}
	abs = append(append([]byte(nil), db.storageKS().prefix...), DataMigrationKey(id)...)
	if err := dst.DB().Set(abs, m8, cpebble.Sync); err != nil {
		return stats, fmt.Errorf("storage: rewrite: the copy's M8 record: %w", err)
	}
	db.extendStart(stepRewriteCopy)
	if err := dst.DB().Flush(); err != nil {
		return stats, fmt.Errorf("storage: rewrite: flush the copy: %w", err)
	}
	closed = true
	if err := dst.Close(st.opts.CloseTimeout); err != nil {
		return stats, fmt.Errorf("storage: rewrite: close the copy: %w", err)
	}
	db.extendStart(stepRewriteCopy)
	if err := fs.RemoveAll(ingest); err != nil {
		return stats, fmt.Errorf("storage: rewrite: remove %s: %w", ingest, err)
	}
	db.extendStart(stepRewriteCopy)
	if err := db.privateTree(tmp); err != nil {
		return stats, err
	}
	db.extendStart(stepRewriteCopy)
	if err := ipebble.SyncDir(fs, tmp); err != nil {
		return stats, err
	}
	if err := fs.Rename(tmp, final); err != nil {
		return stats, fmt.Errorf("storage: rewrite: rename %s: %w", tmp, err)
	}
	return stats, ipebble.SyncDir(fs, filepath.Dir(final))
}

// writeTables writes every live key of the store into SSTs under dir, in
// key order, each up to the table size (rewriteSSTBytes), at dst's table
// format. It asks for start time at every new table and again each time
// rewriteExtendKeys keys or rewriteExtendBytes bytes have been copied since
// the last request, so the deadline moves only while keys are being read and
// written: an iterator or a write that stalls stops the requests.
func (db *DB) writeTables(ctx context.Context, dst *ipebble.Engine, dir string, stats *RewriteCopyStats) (paths []string, err error) {
	release, err := db.lease()
	if err != nil {
		return nil, err
	}
	defer release()
	snap := db.engine().DB().NewSnapshot()
	defer func() { err = errors.Join(err, snap.Close()) }()
	it, err := snap.NewIter(nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, it.Close()) }()
	fs := db.boot.opts.fs
	format := dst.DB().TableFormat()
	tableBytes, everyKeys, everyBytes := db.boot.opts.rewriteTableBytes, db.boot.opts.rewriteExtendKeys, db.boot.opts.rewriteExtendBytes
	var sinceKeys, sinceBytes int64 // copied since the last extension
	extend := func() {
		db.extendStart(stepRewriteCopy)
		sinceKeys, sinceBytes = 0, 0
	}
	extend()
	var w *sstable.Writer
	var size int64
	finish := func() error {
		if w == nil {
			return nil
		}
		werr := w.Close()
		w = nil
		return werr
	}
	for ok := it.First(); ok; ok = it.Next() {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(err, finish())
		}
		if w == nil || size >= tableBytes {
			if err := finish(); err != nil {
				return nil, err
			}
			extend()
			p := filepath.Join(dir, "rewrite-"+strconv.Itoa(len(paths))+".sst")
			f, err := fs.Create(p, "")
			if err != nil {
				return nil, fmt.Errorf("storage: rewrite: create %s: %w", p, err)
			}
			w = sstable.NewWriter(objstorageprovider.NewFileWritable(f), sstable.WriterOptions{TableFormat: format})
			paths = append(paths, p)
			size = 0
		}
		v, err := it.ValueAndErr()
		if err != nil {
			return nil, errors.Join(err, finish())
		}
		if err := w.Set(it.Key(), v); err != nil {
			return nil, errors.Join(err, finish())
		}
		n := int64(len(it.Key()) + len(v))
		size += n
		stats.Keys++
		stats.Bytes += n
		sinceKeys++
		sinceBytes += n
		if sinceKeys >= everyKeys || sinceBytes >= everyBytes {
			extend()
		}
	}
	if err := it.Error(); err != nil {
		return nil, errors.Join(err, finish())
	}
	stats.Tables = len(paths)
	return paths, finish()
}

// RewriteSwap is M8's phase 3 around the swap itself. Before anything
// moves it stages the copy's directory identity (which the renames keep) as
// the attempt checkpoint record's next identity; then it closes the engine,
// runs swap (restoreswap's in-process KindRewrite: set aside, move into
// place, sidecars) and reopens the engine at the store's path, so the DB
// serves the copy after a swap and the original after a swap-back. A copy
// that is in place but does not open writable is swapped back at once
// (swapBack, with the reopen's error as the cause) and the original
// reopened: the call then returns swapBack's error, which wraps
// restoreswap's ErrSwappedBack. After a swap the boot's generation keeps
// its epoch with the new directory's identity, which the copy's
// c/storage/epoch already holds, and the checkpoint record is promoted to
// it. The DB refuses every call while the engine is closed.
func (db *DB) RewriteSwap(ctx context.Context, swap func(ctx context.Context) error, swapBack func(ctx context.Context, cause error) error) error {
	st := db.boot
	if st == nil || db.readOnly || !st.migrated || st.finished {
		return errors.New("storage: the store rewrite's swap runs inside a writable boot's Migrate")
	}
	fsid, ino, err := st.opts.dirIdentity(RewriteDir(st.opts.Path))
	if err != nil {
		return fmt.Errorf("storage: rewrite: the copy's identity: %w", err)
	}
	db.extendStart(stepRewriteSwap)
	if err := db.labelled(LabelRewriteCheckpoint, func() error { return db.attemptIdentity(fsid, ino, false) }); err != nil {
		return err
	}
	db.closed.Store(true)
	db.extendStart(stepRewriteSwap)
	if err := db.labelled(LabelRewriteClose, func() error { return db.engine().Close(db.set.closeTimeout) }); err != nil {
		db.closed.Store(false)
		return fmt.Errorf("storage: rewrite: close the store: %w", err)
	}
	db.extendStart(stepRewriteSwap)
	swapErr := swap(ctx)
	eng, err := db.reopenEngine()
	if err != nil && swapErr == nil {
		// The copy is in place and does not open writable: put the original
		// back and serve it, so the boot fails with the store as it was.
		cause := fmt.Errorf("storage: rewrite: reopen the copy at %s: %w", st.opts.Path, err)
		backErr := swapBack(ctx, cause)
		orig, err := db.reopenEngine()
		if err != nil {
			return errors.Join(cause, backErr, fmt.Errorf("storage: rewrite: reopen the original at %s: %w", st.opts.Path, err))
		}
		db.eng.Store(orig)
		db.closed.Store(false)
		return errors.Join(cause, backErr)
	}
	if err != nil {
		return errors.Join(swapErr, fmt.Errorf("storage: rewrite: reopen %s: %w", st.opts.Path, err))
	}
	db.eng.Store(eng)
	db.closed.Store(false)
	if swapErr != nil {
		return swapErr
	}
	if fsid, ino, err = st.opts.dirIdentity(st.opts.Path); err != nil {
		return fmt.Errorf("storage: rewrite: the new directory's identity: %w", err)
	}
	// The copy wrote this generation with the new identity: nothing is owed
	// at step 9.
	st.epoch.DirFSID, st.epoch.DirIno = fsid, ino
	st.epochNew = false
	return db.labelled(LabelRewriteCheckpoint, func() error { return db.attemptIdentity(fsid, ino, true) })
}

// reopenEngine opens the engine at the store's path, as RewriteSwap does
// after the swap.
func (db *DB) reopenEngine() (*ipebble.Engine, error) {
	st := db.boot
	db.extendStart(stepRewriteSwap)
	var eng *ipebble.Engine
	err := db.labelled(LabelRewriteReopen, func() error {
		var err error
		eng, err = ipebble.Open(st.opts.Path, ipebble.Options{
			FS: st.opts.fs, CacheBytes: st.opts.CacheBytes, MemtableBytes: st.opts.MemtableBytes, Logger: st.opts.Logger,
			TargetFileBytes:  st.opts.engineTargetFile,
			ErrorIfNotExists: true,
		})
		return err
	})
	return eng, err
}

// attemptIdentity rewrites the attempt checkpoint record (this boot's
// cpName; nothing without one) for M8's swap: before it (promote false) the
// copy's identity becomes the record's next identity, so a kill after the
// copy moves into place still finds the record of this store (ofStore);
// after it (promote true) the identity is the record's own and the next one
// is cleared. The epoch is unchanged: a KindRewrite swap keeps the
// generation (§5S.3.13 phase 3, §5S.3.12 "Sources").
func (db *DB) attemptIdentity(fsid, ino uint64, promote bool) error {
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
		if promote {
			r.rec.DirFSID, r.rec.DirIno = fsid, ino
			r.rec.NextDirFSID, r.rec.NextDirIno = 0, 0
		} else {
			r.rec.NextDirFSID, r.rec.NextDirIno = fsid, ino
		}
		return db.writeRecord(r.name, r.rec)
	}
	return fmt.Errorf("storage: rewrite: the attempt's checkpoint record %s is gone", st.cpName)
}

// Path is the store's directory, core.storage.path.
func (db *DB) Path() string { return db.engine().Path() }
