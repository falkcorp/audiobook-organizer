// file: core/storage/storage.go
// version: 1.0.0
// guid: ef01cafc-1a04-4006-984c-af64e9ce7c8b
// last-edited: 2026-11-07

// Package storage is the install's single system-of-record store (05-storage
// §5S.3): keyspaces and legacy claims cut from one Pebble DB, atomic batches
// across the handles bound to them, commit-time preconditions on Serialized
// ranges, sequences, versioned migrations over one DAG, the per-owner level
// guard with its sidecar, the install marker, and the pre-migration
// checkpoint.
//
// The engine lives in core/internal/pebble. The only constructor of a *DB is
// the boot path (Open, Opened.Prepare), which only the kernel and coretest
// call; apps never open a store, they receive Keyspaces and LegacyRanges
// through module.StorageHandles. storage imports neither storage/index nor
// tasks (COR-2, SIMP-17).
//
// It lands dark in release R1 (05-storage §5S.9.4 step 1): ABO keeps opening
// its own store until R1-23.
package storage

import (
	"context"
	"errors"
	"iter"
	"slices"

	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// Owner identifies who holds a keyspace or a claim: a module ID ("books"), or
// "core/<subsystem>" ("core/tasks"). Levels (§5S.3.7) use "core" for all of core.
type Owner string

// Handle is implemented by Keyspace and LegacyRange only (sealed).
type Handle interface{ handle() }

// Reader is the read surface shared by Keyspace, LegacyRange and Snapshot.
type Reader interface {
	Get(ctx context.Context, key []byte) ([]byte, error)
	Scan(ctx context.Context, opts ScanOptions) iter.Seq2[KV, error]
	NewIterator(ctx context.Context, opts ScanOptions) (*Iterator, error)
}

// ScanOptions bounds a scan or an iterator. For a Keyspace the bounds are
// relative; for a LegacyRange they are absolute.
type ScanOptions struct {
	Start, End []byte // nil = the handle's bound
	Reverse    bool
	KeysOnly   bool // skips value fetch for value-separated blobs
	Limit      int  // 0 = unlimited
}

// Range is an absolute key range; End is exclusive.
type Range struct{ Start, End []byte }

// KV is one scanned pair; Key is relative to the handle.
type KV struct{ Key, Value []byte }

// Clone returns a copy that outlives the iteration step.
func (kv KV) Clone() KV { return KV{Key: slices.Clone(kv.Key), Value: slices.Clone(kv.Value)} }

// Durability is a commit's WAL durability.
type Durability uint8

const (
	DurabilityInvalid Durability = iota // zero value is rejected
	Sync                                // fsync the WAL before Commit returns (default for every keyspace)
	NoSync                              // caches, census results, metrics snapshots: may be lost on crash
)

var (
	ErrNotFound        = errors.New("storage: not found")
	ErrOutsideKeyspace = errors.New("storage: key outside the handle's keyspace or claims")
	ErrInvalidKeyspace = errors.New("storage: zero or closed keyspace")
	ErrClosed          = errors.New("storage: db closed")
	ErrBatchTooLarge   = errors.New("storage: batch exceeds the configured limit")
	ErrClaimMode       = errors.New("storage: the claim's mode forbids this operation")
	ErrNotSerialized   = errors.New("storage: preconditions are allowed only on Serialized ranges")
	ErrConflict        = errors.New("storage: a precondition failed at commit; nothing was applied")
	ErrBlindWrite      = errors.New("storage: a write to a Serialized range of a core or module keyspace carries no Expect on its key, or is a DeleteRange there")
	ErrReadOnly        = errors.New("storage: handle of a read-only open; every write is refused") // §5S.3.10, COR7-8
)

// ErrSwapPending: the one intent slot of the journaled swap is taken (an
// intent of any kind is staged) or a swap is in flight. Defined in the L0
// leaf restorejournal, whose CreateIntent returns it, and re-exported here
// for every caller (R8-1, COR8-6, COR9-13).
var ErrSwapPending = restorejournal.ErrSwapPending
