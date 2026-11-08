// file: core/storage/admin.go
// version: 1.0.0
// guid: e760c156-0276-4ad3-9f53-76551e9860b5
// last-edited: 2026-11-07

package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	cpebble "github.com/cockroachdb/pebble/v2"

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

// fullRange spans every key the store can hold.
func fullRange() (start, end []byte) { return []byte{0}, bytes.Repeat([]byte{0xff}, 8) }

// Checkpoint writes a consistent copy of the store into dir (which must not
// exist) with a flushed WAL, then sets its directories to 0700 and its files
// to 0600 (§5S.3.12 "Where"; 05-backup's archive and the dry run's scratch
// copy use it).
func (a *Admin) Checkpoint(ctx context.Context, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	release, err := a.db.lease()
	if err != nil {
		return err
	}
	err = a.db.eng.DB().Checkpoint(dir, cpebble.WithFlushedWAL())
	release()
	if err != nil {
		return fmt.Errorf("storage: checkpoint: %w", err)
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
	if err := a.db.eng.DB().Compact(ctx, start, end, true); err != nil {
		return fmt.Errorf("storage: compact: %w", err)
	}
	return nil
}

// CompactionStats samples the engine's compaction counters; a closed store
// reports Absent.
func (a *Admin) CompactionStats() CompactionStats {
	release, err := a.db.lease()
	if err != nil {
		return CompactionStats{Absent: true}
	}
	defer release()
	return compactionStatsFromMetrics(a.db.eng.DB().Metrics())
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
	n, err := a.db.eng.DB().EstimateDiskUsage(start, end)
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
	it, err := db.eng.DB().NewIter(nil)
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
	fs := src.eng.FS()
	eng, err := ipebble.Open(dir, ipebble.Options{FS: fs, ReadOnly: true, ErrorIfNotExists: true, Logger: src.log})
	if err != nil {
		return nil, fmt.Errorf("storage: read-only open: %w", err)
	}
	db := &DB{
		eng: eng, log: src.log, clock: src.clock, readOnly: true, set: src.set,
		owners: src.owners, claims: src.claims,
	}
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
