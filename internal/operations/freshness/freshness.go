// file: internal/operations/freshness/freshness.go
// version: 1.0.0
// guid: f1e2d3c4-b5a6-7890-abcd-ef1234567890
// last-edited: 2026-06-24

// Package freshness provides per-item freshness stamps for background operations.
//
// Use OpFreshness when an operation needs to skip recently-processed items
// across independent scheduled runs — for example, "skip this book if it was
// fingerprinted within the last 7 days". This is distinct from reporter.Checkpoint
// (which resumes a single crashed run) and from struct fields like DurationVerifiedAt
// (which belong on the entity and are queryable from the library list).
//
// # When to use OpFreshness vs alternatives
//
//   - Use a struct field (DurationVerifiedAt, FingerprintFailedAt) when the stamp
//     is meaningful to the library list, needs to be queryable from API filters,
//     or semantically belongs to the entity model.
//   - Use reporter.Checkpoint when resuming a single crashed run from its last
//     committed position.
//   - Use OpFreshness.Stamp when the stamp is an internal implementation detail
//     of a background op that doesn't belong on the entity.
package freshness

import (
	"encoding/binary"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// OpFreshness stores per-item timestamps for background operations.
// All methods are safe for concurrent use.
type OpFreshness interface {
	// ShouldProcess returns true when the item needs processing:
	//   - always true if force=true
	//   - always true if no stamp exists for (op, itemID)
	//   - true if the stamp is older than maxAge
	//   - false otherwise (item is fresh, skip it)
	ShouldProcess(op, itemID string, maxAge time.Duration, force bool) bool

	// Stamp records that (op, itemID) was processed at now.
	Stamp(op, itemID string) error

	// StampBatch records that all itemIDs were processed at now in a single
	// atomic Pebble batch. More efficient than calling Stamp N times.
	StampBatch(op string, itemIDs []string) error

	// ClearStamps removes all per-item stamps for op using a range delete,
	// causing ShouldProcess to return true for every item on the next run.
	// O(1) regardless of item count.
	ClearStamps(op string) error
}

// PebbleFreshness implements OpFreshness backed by PebbleDB.
//
// Key layout: opck:{op}:item:{itemID} → 8-byte little-endian Unix nanoseconds.
// The opck: prefix groups all freshness keys for easy range iteration/deletion.
type PebbleFreshness struct {
	db *pebble.DB
}

// NewPebbleFreshness creates a PebbleFreshness using the provided PebbleDB handle.
// The DB must remain open for the lifetime of the returned value.
func NewPebbleFreshness(db *pebble.DB) *PebbleFreshness {
	return &PebbleFreshness{db: db}
}

func itemKey(op, itemID string) []byte {
	return []byte("opck:" + op + ":item:" + itemID)
}

func itemPrefix(op string) []byte {
	return []byte("opck:" + op + ":item:")
}

// ShouldProcess implements OpFreshness.
func (f *PebbleFreshness) ShouldProcess(op, itemID string, maxAge time.Duration, force bool) bool {
	if force {
		return true
	}
	val, closer, err := f.db.Get(itemKey(op, itemID))
	if err != nil {
		// Not found or read error — process the item.
		return true
	}
	defer closer.Close()
	if len(val) < 8 {
		return true
	}
	ns := int64(binary.LittleEndian.Uint64(val))
	stamped := time.Unix(0, ns)
	return time.Since(stamped) > maxAge
}

// Stamp implements OpFreshness.
func (f *PebbleFreshness) Stamp(op, itemID string) error {
	return f.db.Set(itemKey(op, itemID), encodeNow(), pebble.Sync)
}

// StampBatch implements OpFreshness.
func (f *PebbleFreshness) StampBatch(op string, itemIDs []string) error {
	if len(itemIDs) == 0 {
		return nil
	}
	now := encodeNow()
	b := f.db.NewBatch()
	defer b.Close()
	for _, id := range itemIDs {
		if err := b.Set(itemKey(op, id), now, nil); err != nil {
			return err
		}
	}
	return b.Commit(pebble.Sync)
}

// ClearStamps implements OpFreshness using a range delete — O(1) regardless
// of item count.
func (f *PebbleFreshness) ClearStamps(op string) error {
	prefix := itemPrefix(op)
	end := prefixSuccessor(prefix)
	return f.db.DeleteRange(prefix, end, pebble.Sync)
}

// encodeNow returns the current time as 8-byte little-endian Unix nanoseconds.
func encodeNow() []byte {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, uint64(time.Now().UnixNano()))
	return buf
}

// prefixSuccessor returns the lexicographic successor of prefix for use as an
// exclusive range-delete end key. Increments the last byte; if it overflows,
// the prefix itself is the successor (no keys exist past it).
func prefixSuccessor(prefix []byte) []byte {
	end := make([]byte, len(prefix))
	copy(end, prefix)
	for i := len(end) - 1; i >= 0; i-- {
		end[i]++
		if end[i] != 0 {
			return end
		}
	}
	return end
}
