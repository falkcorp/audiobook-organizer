// file: internal/database/raw_batch.go
// version: 1.0.0
// guid: 3c8e5a17-9d24-4b60-a1f3-7e0b92d4c586
// last-edited: 2026-10-21

package database

// RawBatchApplier is implemented by stores that can apply a mixed set/delete
// batch of raw keys as one atomic, durably-synced commit: a reader sees all of
// the writes or none, and a crash leaves all of them or none. Writes apply in
// order, so a later write to the same key wins. Like SetRaw it refuses
// opv2:op: keys, and it refuses the whole batch before staging anything. An
// empty batch is a no-op. internal/decisions needs this: a decision record,
// its verdict and its veto index rows must never be half-written.
//
// It is deliberately NOT part of RawKVStore. RawKVStore is embedded in Store,
// and TestStoreFlattenedWidth allows Store only to narrow; *PebbleStore
// implements this directly, and consumers that need it declare it in their own
// small interface (decisions.KV), the same way RawSnapshotter is kept off
// RawKVStore. That also leaves every RawKVStore mock and fake untouched.
type RawBatchApplier interface {
	ApplyRawBatch(writes []RawWrite) error
}

var _ RawBatchApplier = (*PebbleStore)(nil)
