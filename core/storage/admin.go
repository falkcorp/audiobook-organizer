// file: core/storage/admin.go
// version: 1.2.1
// guid: e760c156-0276-4ad3-9f53-76551e9860b5
// last-edited: 2026-11-11

package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/cockroachdb/pebble/v2/vfs"

	ipebble "github.com/falkcorp/mediacore/internal/pebble"
	"github.com/falkcorp/mediacore/storage/keys"
)

// Admin is the kernel-only store administration surface: checkpoint,
// compaction, compaction counters, size estimates, the census and the
// read-only open of another store directory. It has NO staging or swap
// method: restoreswap imports storage, never the reverse (COR8-5, 04 §8.1
// R1). The census and backup reach the store only through it.
type Admin struct{ db *DB }

// Admin returns the store's admin surface, for the kernel.
func (db *DB) Admin() *Admin { return &Admin{db: db} }

// CheckpointBytes sums the bytes of every recorded pre-migration checkpoint
// by the two states of <namespace>_storage_checkpoint_bytes (§5S.8):
// completed, a record whose attempt completed (completed_at set), and open,
// every other record (an attempt's rollback point, served, or left by a
// restore). A record whose directory is gone counts nothing.
func (a *Admin) CheckpointBytes() (open, completed uint64, err error) {
	db := a.db
	recs, err := db.checkpointRecords()
	if err != nil {
		return 0, 0, err
	}
	for _, r := range recs {
		n, err := dirBytes(db.boot.opts.fs, filepath.Join(db.boot.opts.cpDir, r.name))
		if err != nil {
			return 0, 0, fmt.Errorf("storage: size of checkpoint %s: %w", r.name, err)
		}
		if r.rec.CompletedAt != nil {
			completed += n
		} else {
			open += n
		}
	}
	return open, completed, nil
}

// dirBytes is the total size of the regular files under dir; 0 when dir is
// missing.
func dirBytes(fs vfs.FS, dir string) (uint64, error) {
	names, err := fs.List(dir)
	if err != nil {
		if oserrorNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var total uint64
	for _, n := range names {
		p := filepath.Join(dir, n)
		fi, err := fs.Stat(p)
		if err != nil {
			if oserrorNotExist(err) {
				continue
			}
			return 0, err
		}
		if fi.IsDir() {
			sub, err := dirBytes(fs, p)
			if err != nil {
				return 0, err
			}
			total += sub
			continue
		}
		total += uint64(fi.Size())
	}
	return total, nil
}

// fullRange spans every key the store can hold.
func fullRange() (start, end []byte) { return []byte{0}, bytes.Repeat([]byte{0xff}, 8) }

// Checkpoint writes a consistent copy of the store into dir (which must not
// exist) with a flushed memtable and WAL, checks that the copy opens
// read-only, then sets its directories to 0700 and its files to 0600
// (§5S.3.12 "Where" and "How"; 05-backup's archive and the dry run's scratch
// copy use it). A copy that does not open, or will not close after the
// check, is removed and the error returned, as Pebble removes dir when its own
// Checkpoint fails.
func (a *Admin) Checkpoint(ctx context.Context, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.db.checkpointInto(dir); err != nil {
		if errors.Is(err, errCopyRefused) {
			// Only a copy checkpointInto made and refused: any other failure
			// left dir as Pebble left it, which may be a directory the caller
			// already had.
			if rerr := a.db.engine().FS().RemoveAll(dir); rerr != nil {
				err = errors.Join(err, fmt.Errorf("storage: remove the refused checkpoint copy %s: %w", dir, rerr))
			}
		}
		return err
	}
	if a.db.boot == nil {
		return nil
	}
	return a.db.privateTree(dir)
}

// Compact compacts [start, end) (nil, nil = the whole key range) with
// parallel compactions allowed (HEAD's db.Compact(nil, 0xff), db.go:65-80).
func (a *Admin) Compact(ctx context.Context, start, end []byte) error {
	if start == nil && end == nil {
		start, end = fullRange()
	}
	release, err := a.db.lease()
	if err != nil {
		return err
	}
	defer release()
	if err := a.db.engine().DB().Compact(ctx, start, end, true); err != nil {
		return fmt.Errorf("storage: compact: %w", err)
	}
	return nil
}

// ClaimViolations is DB.ClaimViolations for an app that holds only the admin
// surface (ABO's composed test harness reads it after the app stopped, to
// fail a test that made an out-of-claim access it did not expect). It reads
// counters, so it works on a closed store.
func (a *Admin) ClaimViolations() map[ClaimViolation]uint64 { return a.db.ClaimViolations() }

// ClaimViolationKeys is DB.ClaimViolationKeys through the admin surface.
func (a *Admin) ClaimViolationKeys() map[ClaimViolation][]string { return a.db.ClaimViolationKeys() }

// InstallID is the open store's install marker, the value Opened.InstallID
// reported when it was opened. An app's restore stager records it in the
// intent (Intent.InstallID, 05-backup §5B.3.4 step 8; R1-21 decision D8):
// P2 gives it to a restored store that carries no marker.
func (a *Admin) InstallID() string { return a.db.boot.installID }

// CompactionStats samples the engine's compaction counters; a closed store
// reports Absent.
func (a *Admin) CompactionStats() CompactionStats {
	release, err := a.db.lease()
	if err != nil {
		return CompactionStats{Absent: true}
	}
	defer release()
	return compactionStatsFromMetrics(a.db.engine().DB().Metrics())
}

// EngineSample reads the engine's counters for an app's Pebble collector
// (engine_sample.go). Cheap: it reads in-memory counters under a lease, so
// it cannot race Close; a closed store returns ErrClosed and the collector
// emits nothing for it rather than zeros.
func (a *Admin) EngineSample() (EngineSample, error) {
	release, err := a.db.lease()
	if err != nil {
		return EngineSample{}, err
	}
	defer release()
	return engineSampleFromMetrics(a.db.engine().DB().Metrics()), nil
}

// Flush flushes the memtables to sstables, so that table properties and size
// estimates cover recent writes (ABO's census flushes before it reads them).
func (a *Admin) Flush(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.db.readOnly {
		return ErrReadOnly
	}
	release, err := a.db.lease()
	if err != nil {
		return err
	}
	defer release()
	if err := a.db.engine().DB().Flush(); err != nil {
		return fmt.Errorf("storage: flush: %w", err)
	}
	return nil
}

// EstimateDiskUsage is Pebble's on-disk estimate for [start, end) (nil, nil
// = the whole store).
func (a *Admin) EstimateDiskUsage(start, end []byte) (uint64, error) {
	if start == nil && end == nil {
		start, end = fullRange()
	}
	release, err := a.db.lease()
	if err != nil {
		return 0, err
	}
	defer release()
	n, err := a.db.engine().DB().EstimateDiskUsage(start, end)
	if err != nil {
		return 0, fmt.Errorf("storage: estimate: %w", err)
	}
	return n, nil
}

// CensusEntry is one partition of the store: a keyspace, a claim interval of
// one owner, or the keys no owner holds.
type CensusEntry struct {
	Owner Owner  // "" for unowned keys
	Start []byte // the partition's absolute range
	End   []byte
	Mode  ClaimMode // ClaimInvalid for a keyspace or unowned keys
	Keys  uint64
	Bytes uint64 // key + value bytes
}

// Census is the exact per-partition count of the store, partitioned by
// claims and keyspaces (§5S.9.1; the mechanism holds no key name). Every key
// is counted once.
func (a *Admin) Census(ctx context.Context) ([]CensusEntry, error) {
	db := a.db
	release, err := db.lease()
	if err != nil {
		return nil, err
	}
	defer release()
	it, err := db.engine().DB().NewIter(nil)
	if err != nil {
		return nil, fmt.Errorf("storage: census: %w", err)
	}
	type part struct{ start, end string }
	counts := map[part]*CensusEntry{}
	var order []part
	n := 0
	for ok := it.First(); ok; ok = it.Next() {
		if n++; n%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, errors.Join(err, it.Close())
			}
		}
		k := it.Key()
		e := db.partitionOf(k)
		p := part{string(e.Start), string(e.End)}
		c, ok := counts[p]
		if !ok {
			c = &e
			counts[p] = c
			order = append(order, p)
		}
		lv := it.LazyValue()
		c.Keys++
		c.Bytes += uint64(len(k)) + uint64(lv.Len())
	}
	if err := errors.Join(it.Error(), it.Close()); err != nil {
		return nil, fmt.Errorf("storage: census: %w", err)
	}
	out := make([]CensusEntry, 0, len(order))
	for _, p := range order {
		out = append(out, *counts[p])
	}
	slices.SortFunc(out, func(x, y CensusEntry) int { return bytes.Compare(x.Start, y.Start) })
	return out, nil
}

// partitionOf names the partition of an absolute key.
func (db *DB) partitionOf(k []byte) CensusEntry {
	for _, o := range sortedInfos(db.owners) {
		if o.prefix != nil && bytes.HasPrefix(k, o.prefix) {
			return CensusEntry{Owner: o.owner, Start: o.prefix, End: keys.PrefixEnd(o.prefix)}
		}
	}
	if iv, ok := db.claims.resolve(k); ok {
		return CensusEntry{Owner: iv.owner, Start: iv.start, End: iv.end, Mode: iv.mode}
	}
	// Unowned: one partition per reserved root, one for the rest.
	switch {
	case bytes.HasPrefix(k, []byte("c/")):
		return CensusEntry{Start: []byte("c/"), End: []byte("c0")}
	case bytes.HasPrefix(k, []byte("m/")):
		return CensusEntry{Start: []byte("m/"), End: []byte("m0")}
	}
	return CensusEntry{Start: []byte{}, End: nil}
}

// OpenReadOnly opens the store directory dir read-only: Pebble ReadOnly (no
// WAL flush, no ratchet, no OPTIONS-<n>), no guard writes, no sidecar
// rewrite. Its handles resolve the build's claims and keyspaces, and every
// write through them fails with ErrReadOnly (§5S.3.10, COR7-8).
func (a *Admin) OpenReadOnly(ctx context.Context, dir string) (*ReadOnlyDB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("storage: read-only open of %q: not absolute", dir)
	}
	src := a.db
	fs := src.engine().FS()
	eng, err := ipebble.Open(dir, ipebble.Options{FS: fs, ReadOnly: true, ErrorIfNotExists: true, Logger: src.log})
	if err != nil {
		return nil, fmt.Errorf("storage: read-only open: %w", err)
	}
	db := &DB{
		log: src.log, clock: src.clock, readOnly: true, set: src.set,
		owners: src.owners, claims: src.claims,
	}
	db.eng.Store(eng)
	return &ReadOnlyDB{db: db}, nil
}

// ReadOnlyDB issues handles whose every write (Set, Delete, DeleteRange,
// Commit of a batch with writes, Sequence refill) fails with ErrReadOnly.
type ReadOnlyDB struct{ db *DB }

// Legacy is owner's claims as the build declares them.
func (r *ReadOnlyDB) Legacy(owner Owner) (LegacyRange, error) {
	lr, ok := r.db.Legacy(owner)
	if !ok {
		return LegacyRange{}, fmt.Errorf("storage: %s declares no claims: %w", owner, ErrInvalidKeyspace)
	}
	return lr, nil
}

// Keyspace is m/<owner>/ or c/<subsystem>/.
func (r *ReadOnlyDB) Keyspace(owner Owner) (Keyspace, error) {
	ks, ok := r.db.Keyspace(owner)
	if !ok {
		return Keyspace{}, fmt.Errorf("storage: %s declares no keyspace: %w", owner, ErrInvalidKeyspace)
	}
	return ks, nil
}

// Close closes the read-only store.
func (r *ReadOnlyDB) Close() error { return r.db.Close() }
