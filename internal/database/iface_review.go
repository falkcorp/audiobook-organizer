// file: internal/database/iface_review.go
// version: 1.1.0
// guid: 7a1e4d63-2c9f-48b5-b0a7-6e3d51f8c294
// last-edited: 2026-08-06

package database

// ReviewStore is the universal review-queue surface (PR-A1). It backs a generic,
// producer-agnostic queue of items flagged for a human decision. Producers
// UPSERT by DedupKey (idempotent, decision-preserving); the HTTP layer lists,
// counts, and transitions items. Implemented on *PebbleStore (review_store.go).
type ReviewStore interface {
	// UpsertReviewItem inserts or idempotently updates a review item keyed by
	// its stable DedupKey. A re-scan never duplicates a row and never resurfaces
	// an already-decided (non-pending) item. See the implementation for the full
	// idempotency contract.
	UpsertReviewItem(item ReviewItem) (ReviewItem, error)

	// GetReviewItem returns a single item by ID, or (nil, nil) when absent.
	GetReviewItem(id string) (*ReviewItem, error)

	// ListReviewItems returns a paginated, newest-first slice of items matching
	// the filter plus the total match count (before pagination).
	ListReviewItems(filter ReviewFilter) ([]ReviewItem, int, error)

	// CountReviewItems returns the number of items with the given status; a blank
	// status counts every item.
	CountReviewItems(status string) (int, error)

	// ReviewStatsByKind returns a per-(Kind, Status) count breakdown.
	ReviewStatsByKind() ([]ReviewKindStat, error)

	// SetReviewItemStatus transitions an item to a new status, moving its
	// status-index row. Returns (nil, nil) when the item does not exist. It does
	// NOT touch ChosenAction — a status-only transition (reject, or replay's
	// approved→applied) must never erase a recorded human decision.
	SetReviewItemStatus(id, status string) (*ReviewItem, error)

	// SetReviewItemDecision transitions an item AND records the action a human
	// chose, atomically. A blank chosenAction leaves the stored one alone; there is
	// deliberately no way to clear it. This is what makes an override durable: the
	// replay path prefers ChosenAction over the payload's recommendation, so a
	// `combine` hold approved as `separate` is never later replayed as a merge.
	SetReviewItemDecision(id, status, chosenAction string) (*ReviewItem, error)

	// DeleteReviewItem removes an item and all its index rows (record, status,
	// dedup). Idempotent — deleting a missing id is a no-op. Deleting the dedup
	// index forgets a remembered decision, so producers must only delete PENDING
	// items (the regroup reconcile relies on this to purge superseded holds).
	DeleteReviewItem(id string) error
}
