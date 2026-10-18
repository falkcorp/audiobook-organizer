// file: internal/database/iface_review.go
// version: 1.3.0
// guid: 7a1e4d63-2c9f-48b5-b0a7-6e3d51f8c294
// last-edited: 2026-10-18

package database

// ReviewStore is the universal review-queue surface (PR-A1). It backs a generic,
// producer-agnostic queue of items flagged for a human decision. Producers
// UPSERT by DedupKey (idempotent, decision-preserving); the HTTP layer lists and
// counts items. Implemented on *PebbleStore (review_store.go).
//
// It deliberately has no status writer and no delete. Every write that can race
// an apply goes through ReviewItemApplyClaimer (review_claim.go) and the only
// delete is ReviewItemPendingDeleter's pending-only one; both are capabilities
// resolved with AsCapability. A generic SetStatus/Delete here was how a reject
// could land over a running apply (01-P81j).
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
}

// ReviewStatusIndexRepairer rebuilds the review-item status index from the
// records. It is deliberately NOT part of ReviewStore (and so not part of Store):
// it is a maintenance concern with exactly one caller, the
// maintenance.review-status-index-repair op, and widening the 398-method Store
// for it would also mean regenerating two mocks. Like FileProvenanceStore, the
// server resolves it with AsCapability (store_capability.go) -- never a bare
// type assertion, which the production store decorator defeats -- and a store
// that lacks it at every layer yields nil. Implemented on *PebbleStore
// (review_store.go).
type ReviewStatusIndexRepairer interface {
	// RebuildReviewStatusIndex reconciles review_item:status:* against the
	// records. apply=false only counts; apply=true deletes stale rows and adds
	// missing ones in one synced batch. See the implementation for what counts
	// as stale or missing.
	RebuildReviewStatusIndex(apply bool) (ReviewStatusIndexRepair, error)
}
