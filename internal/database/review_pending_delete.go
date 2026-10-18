// file: internal/database/review_pending_delete.go
// version: 1.1.0
// guid: a067e38f-ed88-4e38-87ce-5f53cb6a8fdf
// last-edited: 2026-10-18

package database

// ReviewItemPendingDeleter deletes a review item only while it is still pending. It
// is a capability, resolved with AsCapability (store_capability.go), not part of
// ReviewStore or Store.
//
// It exists for the regroup reconcile (maintenance.reconcileStaleHolds), which lists
// pending holds and deletes the ones its scan no longer emits. The unconditional
// delete it used to make after listing (DeleteReviewItem, removed in 01-P81j) raced
// a reviewer: an item approved, rejected or claimed for apply between the list and
// the delete lost its decision, and deleting the dedup index as well let the next
// scan offer the folder again as if nobody had decided. This is now the store's
// only review delete. Here the status is re-read under reviewMu, the
// mutex every review writer holds, so a decided item is never deleted.
type ReviewItemPendingDeleter interface {
	// DeleteReviewItemIfPending deletes the item and all its index rows when its
	// status is pending, and reports whether it did. A missing item or one in any
	// other status is left alone and reports false.
	DeleteReviewItemIfPending(id string) (bool, error)
}

var _ ReviewItemPendingDeleter = (*PebbleStore)(nil)

func (p *PebbleStore) DeleteReviewItemIfPending(id string) (bool, error) {
	p.reviewMu.Lock()
	defer p.reviewMu.Unlock()

	item, err := p.getReviewItem(id)
	if err != nil || item == nil || item.Status != ReviewStatusPending {
		return false, err
	}
	if err := p.deleteReviewItemLocked(item); err != nil {
		return false, err
	}
	return true, nil
}
