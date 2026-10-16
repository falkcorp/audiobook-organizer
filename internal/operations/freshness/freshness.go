// file: internal/operations/freshness/freshness.go
// version: 1.1.0
// guid: f1e2d3c4-b5a6-7890-abcd-ef1234567890
// last-edited: 2026-10-16

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
	"time"
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
