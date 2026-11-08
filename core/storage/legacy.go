// file: core/storage/legacy.go
// version: 1.0.0
// guid: 3e00c9cb-7614-4b45-814c-a6dac105888e
// last-edited: 2026-11-07

package storage

import (
	"context"
	"fmt"
	"iter"

	cpebble "github.com/cockroachdb/pebble/v2"
)

// LegacyRange is the handle for an owner's claims. Keys are ABSOLUTE (legacy
// keys have no m/ or c/ prefix). Every read and every write is checked against the
// handle's claim set with longest-match resolution; a key that resolves to
// another owner, or to no claim of this handle, is refused with ErrOutsideKeyspace,
// and an operation the mode forbids with ErrClaimMode. Scans and iterators
// are clamped: keys outside the handle are skipped (the iterator seeks past
// carve-outs), and c/ and m/ are never reachable.
type LegacyRange struct {
	db    *DB
	owner Owner
	tx    *MigrationTx // reads and writes go through a migration's batch
	ro    bool
}

func (LegacyRange) handle() {}

// Get reads one absolute key of the owner's claims.
func (r LegacyRange) Get(ctx context.Context, key []byte) ([]byte, error) {
	return r.scope().get(ctx, key)
}

// Scan yields the owner's keys in [opts.Start, opts.End) (absolute), clamped
// to its claims.
func (r LegacyRange) Scan(ctx context.Context, opts ScanOptions) iter.Seq2[KV, error] {
	return r.scope().scan(ctx, opts)
}

// NewIterator is the clamped cursor over the owner's claims.
func (r LegacyRange) NewIterator(ctx context.Context, opts ScanOptions) (*Iterator, error) {
	return r.scope().newIterator(ctx, opts, nil)
}

// NewBatch is a write-only batch with r bound. On a migration step's range
// (MigrationTx.Legacy) the batch refuses every write and its Commit
// (errTxKeyspaceWrite): the step's writes go through r's own methods.
func (r LegacyRange) NewBatch() *Batch { return newBatch(r, false) }

// NewIndexedBatch is a batch whose reads see its own writes; refused on a
// migration step's range as NewBatch is.
func (r LegacyRange) NewIndexedBatch() *Batch { return newBatch(r, true) }

// Snapshot is a point-in-time, read-only view scoped to r; refused on a
// migration step's range (errTxSnapshot).
func (r LegacyRange) Snapshot() (*Snapshot, error) { return newSnapshot(r.scope()) }

// Set is a one-entry batch committed with Sync (inside a migration, a write
// staged into the step's batch).
func (r LegacyRange) Set(ctx context.Context, key, value []byte) error {
	return r.scope().single(ctx, opSet, key, value, nil)
}

// Delete is a one-entry batch committed with Sync.
func (r LegacyRange) Delete(ctx context.Context, key []byte) error {
	return r.scope().single(ctx, opDelete, key, nil, nil)
}

// DeleteRange deletes [start, end); the whole range must resolve to this
// handle.
func (r LegacyRange) DeleteRange(ctx context.Context, start, end []byte) error {
	if end == nil {
		return fmt.Errorf("storage: a legacy DeleteRange needs an end key: %w", ErrOutsideKeyspace)
	}
	return r.scope().single(ctx, opDelete, start, nil, &rangeEnd{end: end})
}

// Barrier is a WAL sync barrier; replaces LogData(nil, Sync) (ident_state.go:1360).
func (r LegacyRange) Barrier(ctx context.Context) error {
	s := r.scope()
	if err := s.check(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.ro {
		return ErrReadOnly
	}
	release, err := r.db.lease()
	if err != nil {
		return err
	}
	defer release()
	if err := r.db.eng.DB().LogData(nil, cpebble.Sync); err != nil {
		return fmt.Errorf("storage: WAL barrier: %w", err)
	}
	return nil
}
