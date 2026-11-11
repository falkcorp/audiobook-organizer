// file: core/storage/rewrite.go
// version: 1.0.1
// guid: 7959a601-c65e-4e8c-bdfe-6fedd766d21e
// last-edited: 2026-11-11

package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// RewriteCopied is M8's record state between phases 2 and 4: the copy is
// complete at RewriteDir. The DAG treats M8 as pending until
// DataMigrationDone.
const RewriteCopied = "copied"

// The crash-matrix labels of M8's storage half (06 §6.5.1's boot-writer
// table, M8's phases).
const (
	LabelRewriteState   = "storage.rewrite.state"
	LabelRewriteCompact = "storage.rewrite.compact"
	LabelRewriteCopy    = "storage.rewrite.copy"
	LabelRewriteClose   = "storage.rewrite.close"
	LabelRewriteReopen  = "storage.rewrite.reopen"
)

// RewriteLabels are every label of M8's storage half.
func RewriteLabels() []string {
	return []string{LabelRewriteState, LabelRewriteCompact, LabelRewriteCopy, LabelRewriteClose, LabelRewriteReopen}
}

// RewriteDir is the finished copy's directory beside the store at dbPath:
// <db parent>/<db name>.rewrite. Its temporary form adds ".tmp".
func RewriteDir(dbPath string) string { return filepath.Clean(dbPath) + ".rewrite" }

// rewriteSSTBytes is the size at which the copy starts a new SST.
const rewriteSSTBytes = 64 << 20

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

// RewriteCompact is M8's phase 1: flush, then compact the whole range,
// parallelized.
func (db *DB) RewriteCompact(ctx context.Context) error {
	return db.labelled(LabelRewriteCompact, func() error {
		release, err := db.lease()
		if err != nil {
			return err
		}
		defer release()
		if err := db.eng.DB().Flush(); err != nil {
			return fmt.Errorf("storage: rewrite flush: %w", err)
		}
		start, end := fullRange()
		if err := db.eng.DB().Compact(ctx, start, end, true); err != nil {
			return fmt.Errorf("storage: rewrite compact: %w", err)
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
// fsynced. The live store is read under one snapshot and never written. The
// dry run copies into its own scratch directory the same way.
func (db *DB) RewriteCopy(ctx context.Context, final string) (stats RewriteCopyStats, err error) {
	st := db.boot
	if st == nil || db.readOnly {
		return stats, errors.New("storage: the store rewrite needs a writable boot")
	}
	err = db.labelled(LabelRewriteCopy, func() error {
		var err error
		stats, err = db.rewriteCopy(ctx, final)
		return err
	})
	return stats, err
}

func (db *DB) rewriteCopy(ctx context.Context, final string) (stats RewriteCopyStats, err error) {
	st := db.boot
	fs := st.opts.fs
	tmp, ingest := final+".tmp", final+".ingest"
	for _, p := range []string{tmp, final, ingest} {
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
	if f := db.eng.FormatMajorVersion(); f > dst.FormatMajorVersion() {
		if err := dst.DB().RatchetFormatMajorVersion(f); err != nil {
			return stats, fmt.Errorf("storage: rewrite: the copy's format: %w", err)
		}
	}
	paths, err := db.writeTables(ctx, dst, ingest, &stats)
	if err != nil {
		return stats, err
	}
	if len(paths) > 0 {
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
	if err := dst.DB().Flush(); err != nil {
		return stats, fmt.Errorf("storage: rewrite: flush the copy: %w", err)
	}
	closed = true
	if err := dst.Close(st.opts.CloseTimeout); err != nil {
		return stats, fmt.Errorf("storage: rewrite: close the copy: %w", err)
	}
	if err := fs.RemoveAll(ingest); err != nil {
		return stats, fmt.Errorf("storage: rewrite: remove %s: %w", ingest, err)
	}
	if err := db.privateTree(tmp); err != nil {
		return stats, err
	}
	if err := ipebble.SyncDir(fs, tmp); err != nil {
		return stats, err
	}
	if err := fs.Rename(tmp, final); err != nil {
		return stats, fmt.Errorf("storage: rewrite: rename %s: %w", tmp, err)
	}
	return stats, ipebble.SyncDir(fs, filepath.Dir(final))
}

// writeTables writes every live key of the store into SSTs under dir, in
// key order, each up to rewriteSSTBytes, at dst's table format.
func (db *DB) writeTables(ctx context.Context, dst *ipebble.Engine, dir string, stats *RewriteCopyStats) (paths []string, err error) {
	release, err := db.lease()
	if err != nil {
		return nil, err
	}
	defer release()
	snap := db.eng.DB().NewSnapshot()
	defer func() { err = errors.Join(err, snap.Close()) }()
	it, err := snap.NewIter(nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, it.Close()) }()
	fs := db.boot.opts.fs
	format := dst.DB().TableFormat()
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
		if w == nil || size >= rewriteSSTBytes {
			if err := finish(); err != nil {
				return nil, err
			}
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
	}
	if err := it.Error(); err != nil {
		return nil, errors.Join(err, finish())
	}
	stats.Tables = len(paths)
	return paths, finish()
}

// RewriteSwap is M8's phase 3 around the swap itself: it closes the engine,
// runs swap (restoreswap's in-process KindRewrite: set aside, move into
// place, sidecars), and reopens the engine at the store's path whatever swap
// returned, so the DB serves the copy after a swap and the original after a
// swap-back. After a swap the boot's generation keeps its epoch with the new
// directory's identity, which the copy's c/storage/epoch already holds. The
// DB refuses every call while the engine is closed.
func (db *DB) RewriteSwap(ctx context.Context, swap func(ctx context.Context) error) error {
	st := db.boot
	if st == nil || db.readOnly || !st.migrated || st.finished {
		return errors.New("storage: the store rewrite's swap runs inside a writable boot's Migrate")
	}
	db.closed.Store(true)
	if err := db.labelled(LabelRewriteClose, func() error { return db.eng.Close(db.set.closeTimeout) }); err != nil {
		db.closed.Store(false)
		return fmt.Errorf("storage: rewrite: close the store: %w", err)
	}
	swapErr := swap(ctx)
	var eng *ipebble.Engine
	err := db.labelled(LabelRewriteReopen, func() error {
		var err error
		eng, err = ipebble.Open(st.opts.Path, ipebble.Options{
			FS: st.opts.fs, CacheBytes: st.opts.CacheBytes, MemtableBytes: st.opts.MemtableBytes, Logger: st.opts.Logger,
			ErrorIfNotExists: true,
		})
		return err
	})
	if err != nil {
		return errors.Join(swapErr, fmt.Errorf("storage: rewrite: reopen %s: %w", st.opts.Path, err))
	}
	db.eng = eng
	db.closed.Store(false)
	if swapErr != nil {
		return swapErr
	}
	fsid, ino, err := st.opts.dirIdentity(st.opts.Path)
	if err != nil {
		return fmt.Errorf("storage: rewrite: the new directory's identity: %w", err)
	}
	// The copy wrote this generation with the new identity: nothing is owed
	// at step 9.
	st.epoch.DirFSID, st.epoch.DirIno = fsid, ino
	st.epochNew = false
	return nil
}

// RemoveRewriteLeftovers removes an earlier attempt's copy beside the store
// (RewriteDir and its temporary and ingest forms), for a phase engine that
// restarts M8 from phase 1. Removing what is gone is success.
func (db *DB) RemoveRewriteLeftovers() error {
	fs := db.boot.opts.fs
	final := RewriteDir(db.boot.opts.Path)
	for _, p := range []string{final + ".tmp", final + ".ingest", final} {
		if err := fs.RemoveAll(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("storage: rewrite: remove %s: %w", p, err)
		}
	}
	return ipebble.SyncDir(fs, filepath.Dir(final))
}

// planHoldsRewrite reports whether the boot's plan holds M8 pending.
func (db *DB) planHoldsRewrite() bool {
	for _, n := range db.boot.plan {
		if n.data != nil && n.data.Rewrite {
			return true
		}
	}
	return false
}

// Path is the store's directory, core.storage.path.
func (db *DB) Path() string { return db.eng.Path() }
