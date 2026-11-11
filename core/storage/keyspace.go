// file: core/storage/keyspace.go
// version: 1.0.1
// guid: 19622090-dcb6-4a27-9fdc-56c0a74d93af
// last-edited: 2026-11-11

package storage

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"

	"github.com/falkcorp/mediacore/storage/keys"
)

// Keyspace is a capability: every key passed to its methods is relative and
// is stored as Prefix()+key, so a holder cannot address anything else.
// The zero Keyspace is invalid; every method on it returns ErrInvalidKeyspace.
type Keyspace struct {
	db     *DB
	owner  Owner
	prefix []byte
	tx     *MigrationTx // reads go through a migration's batch
	ro     bool         // a read-only open's handle
}

func (Keyspace) handle() {}

// errTxKeyspaceWrite: a write through a migration step's handles that would
// commit outside the step's one batch: a direct write on its Keyspace, or a
// batch or sequence taken from its Keyspace (or a Sub) or its LegacyRange.
var errTxKeyspaceWrite = errors.New("storage: a migration step writes only through its one batch: MigrationTx.Set or Delete, or its LegacyRange's own Set, Delete and DeleteRange; a batch or sequence of the step's handles would commit outside it")

// errTxSnapshot: a snapshot of a migration step's handle, which reads the
// engine and so would not see the step's own writes.
var errTxSnapshot = errors.New("storage: a snapshot inside a migration step would not see the step's own writes; read through the step's handles")

// Prefix is the keyspace's absolute prefix: "m/tv/" or "c/tasks/".
func (k Keyspace) Prefix() string { return string(k.prefix) }

// Owner is the keyspace's owner.
func (k Keyspace) Owner() Owner { return k.owner }

// Sub is the sub-keyspace "<prefix><name>/": "m/tv/" + "series" ->
// "m/tv/series/". name is checked by keys.ValidName; an invalid name yields
// the zero Keyspace, whose every method returns ErrInvalidKeyspace.
func (k Keyspace) Sub(name string) Keyspace {
	if k.db == nil || keys.ValidName(name) != nil {
		return Keyspace{}
	}
	p := append(slices.Clone(k.prefix), name...)
	p = append(p, '/')
	return Keyspace{db: k.db, owner: k.owner, prefix: p, tx: k.tx, ro: k.ro}
}

// Get returns a copy owned by the caller; ErrNotFound when absent.
func (k Keyspace) Get(ctx context.Context, key []byte) ([]byte, error) {
	return k.scope().get(ctx, key)
}

// Has reports whether key exists.
func (k Keyspace) Has(ctx context.Context, key []byte) (bool, error) {
	_, err := k.Get(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Set is a one-entry batch committed with Sync.
func (k Keyspace) Set(ctx context.Context, key, value []byte) error {
	return k.scope().single(ctx, opSet, key, value, nil)
}

// Delete is a one-entry batch committed with Sync.
func (k Keyspace) Delete(ctx context.Context, key []byte) error {
	return k.scope().single(ctx, opDelete, key, nil, nil)
}

// DeleteRange deletes [start, end) inside the keyspace; nil end = to the end
// of the keyspace.
func (k Keyspace) DeleteRange(ctx context.Context, start, end []byte) error {
	return k.scope().single(ctx, opDelete, start, nil, &rangeEnd{end: end})
}

// Scan yields keys in [opts.Start, opts.End) (relative), forward or reverse.
// Key and Value are valid only until the next iteration step; use KV.Clone to
// retain. A non-nil error is yielded at most once, as the last pair.
func (k Keyspace) Scan(ctx context.Context, opts ScanOptions) iter.Seq2[KV, error] {
	return k.scope().scan(ctx, opts)
}

// ScanPrefix scans the relative keys that start with prefix.
func (k Keyspace) ScanPrefix(ctx context.Context, prefix []byte) iter.Seq2[KV, error] {
	return k.Scan(ctx, ScanOptions{Start: prefix, End: keys.PrefixEnd(prefix)})
}

// NewIterator is the cursor form for callers that seek, step backwards or
// interleave scans (the books repository layer needs SeekGE/Prev/Last).
func (k Keyspace) NewIterator(ctx context.Context, opts ScanOptions) (*Iterator, error) {
	return k.scope().newIterator(ctx, opts, nil)
}

// NewBatch is a write-only batch with k bound. On a migration step's
// Keyspace (MigrationTx.Keyspace, or a Sub of it) the batch refuses every
// write and its Commit (errTxKeyspaceWrite).
func (k Keyspace) NewBatch() *Batch { return newBatch(k, false) }

// NewIndexedBatch is a batch whose reads see its own writes; refused on a
// migration step's Keyspace as NewBatch is.
func (k Keyspace) NewIndexedBatch() *Batch { return newBatch(k, true) }

// Snapshot is a point-in-time, read-only view scoped to k. A migration
// step's Keyspace refuses it (errTxSnapshot): it would read the engine, not
// the step's batch.
func (k Keyspace) Snapshot() (*Snapshot, error) { return newSnapshot(k.scope()) }

// Sequence returns a named monotonic uint64 allocator stored in the keyspace
// (§5S.3.2), at the relative key keys.Join("_seq", name), so the segment
// "_seq" is reserved in every keyspace. The same name on the same keyspace
// always returns the same *Sequence, whose lease is shared.
func (k Keyspace) Sequence(name string) *Sequence {
	if k.db == nil {
		return &Sequence{err: ErrInvalidKeyspace}
	}
	if err := keys.ValidName(name); err != nil {
		return &Sequence{err: fmt.Errorf("storage: sequence name: %w", err)}
	}
	if k.tx != nil {
		// Its lease commits on its own, outside the step's one batch.
		return &Sequence{err: errTxKeyspaceWrite}
	}
	rel := keys.Join(keys.Str("_seq"), keys.Str(name))
	abs := string(k.prefix) + string(rel)
	k.db.seqMu.Lock()
	defer k.db.seqMu.Unlock()
	if s, ok := k.db.seqs[abs]; ok {
		return s
	}
	s := newSequence(Keyspace{db: k.db, owner: k.owner, prefix: k.prefix, ro: k.ro}, rel, k.db.set.seqLease)
	if k.db.seqs == nil {
		k.db.seqs = make(map[string]*Sequence)
	}
	k.db.seqs[abs] = s
	return s
}

// EstimateBytes is Pebble's on-disk estimate for the keyspace (cheap).
func (k Keyspace) EstimateBytes(ctx context.Context) (uint64, error) {
	s := k.scope()
	if err := s.check(); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	release, err := k.db.lease()
	if err != nil {
		return 0, err
	}
	defer release()
	n, err := k.db.engine().DB().EstimateDiskUsage(k.prefix, keys.PrefixEnd(k.prefix))
	if err != nil {
		return 0, fmt.Errorf("storage: estimate %s: %w", k.prefix, err)
	}
	return n, nil
}

// rangeEnd marks a single-op call as a DeleteRange with this end.
type rangeEnd struct{ end []byte }

// get reads one key through the handle: the migration batch, else the
// engine.
func (s handleOf) get(ctx context.Context, key []byte) ([]byte, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	abs, _, err := s.absolute(key, opRead)
	if err != nil {
		return nil, err
	}
	var v []byte
	var ok bool
	if s.tx != nil {
		v, ok, err = s.tx.b.getView(abs)
	} else {
		v, ok, err = s.db.getCommitted(abs)
	}
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	return v, nil
}

// single runs one write as a one-entry batch committed with Sync; on a
// migration's legacy range it stages into the step's batch instead.
func (s handleOf) single(ctx context.Context, op opKind, key, value []byte, dr *rangeEnd) (err error) {
	if err := s.check(); err != nil {
		return err
	}
	if s.ro {
		return ErrReadOnly
	}
	h := s.handle()
	if s.tx != nil {
		if !s.legacy {
			return errTxKeyspaceWrite
		}
		if dr != nil {
			err = s.tx.b.stageRange(h, key, dr.end)
		} else {
			err = s.tx.b.stage(h, op, key, value)
		}
		if err != nil {
			return err
		}
		return s.tx.checkSize()
	}
	b := newBatch(h, false)
	defer func() { err = errors.Join(err, b.Close()) }()
	if dr != nil {
		err = b.stageRange(h, key, dr.end)
	} else {
		err = b.stage(h, op, key, value)
	}
	if err != nil {
		return err
	}
	return b.Commit(ctx, Sync)
}

// handle rebuilds the Handle value of a scope.
func (s handleOf) handle() Handle {
	if s.legacy {
		return LegacyRange{db: s.db, owner: s.owner, tx: s.tx, ro: s.ro}
	}
	return Keyspace{db: s.db, owner: s.owner, prefix: s.prefix, tx: s.tx, ro: s.ro}
}

// scan is the iter.Seq2 form of newIterator.
func (s handleOf) scan(ctx context.Context, opts ScanOptions) iter.Seq2[KV, error] {
	return func(yield func(KV, error) bool) {
		it, err := s.newIterator(ctx, opts, nil)
		if err != nil {
			yield(KV{}, err)
			return
		}
		yieldScan(it, opts, s.db.log, yield)
	}
}
