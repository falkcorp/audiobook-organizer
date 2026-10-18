// file: internal/database/review_claim.go
// version: 1.0.0
// guid: 793a733c-f28c-4b2d-ba41-4170b8a43a07
// last-edited: 2026-10-18

package database

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// ReviewItemApplyClaimer is the compare-and-set that keeps two concurrent approves
// of one review item from both running its apply handler. It is a capability, not
// part of ReviewStore or Store: the review handler resolves it with AsCapability
// (store_capability.go) and a store that lacks it simply gets no claim. Implemented
// on *PebbleStore (review_claim.go).
//
// The claim moves the item to ReviewStatusApplying under the same reviewMu as every
// other review status write, through the same record + status-index batch, so the
// review_item:status:applying:<id> index row always matches the record.
type ReviewItemApplyClaimer interface {
	// ClaimReviewItemForApply atomically moves a pending, approved or rejected
	// item to applying, remembering the status it came from. Returns (nil, nil)
	// when the item does not exist and a *ReviewClaimConflictError when it is
	// already applied or already applying under a claim younger than staleAfter. A claim
	// older than staleAfter is taken over (see ReviewApplyClaimStale).
	ClaimReviewItemForApply(id string, staleAfter time.Duration) (*ReviewItem, error)

	// ReleaseReviewItemClaim puts a claimed item back to the status it was claimed
	// from. It is a no-op when the item is no longer applying (another writer
	// finished or took it over). Used when the apply handler failed.
	ReleaseReviewItemClaim(id string) error
}

// ReviewApplyClaimStale is how long an applying claim holds before another approve
// may take it over. 15 minutes: far above any single review apply (one merge), far
// below "a human notices". A crash between claim and finish therefore blocks only
// that item, only for this long, with no startup hook and no migration; the cost is
// that a takeover after a crash that landed mid-apply re-runs the handler, the same
// ambiguity as any retry after a crash.
const ReviewApplyClaimStale = 15 * time.Minute

// ReviewClaimConflictError says why a claim was refused.
type ReviewClaimConflictError struct {
	ID     string
	Status string
}

func (e *ReviewClaimConflictError) Error() string {
	return fmt.Sprintf("review item %s is %s and cannot be claimed for apply", e.ID, e.Status)
}

func (p *PebbleStore) ClaimReviewItemForApply(id string, staleAfter time.Duration) (*ReviewItem, error) {
	p.reviewMu.Lock()
	defer p.reviewMu.Unlock()

	item, err := p.getReviewItem(id)
	if err != nil || item == nil {
		return nil, err
	}
	from := item.Status
	switch from {
	case ReviewStatusPending, ReviewStatusApproved, ReviewStatusRejected:
	// Rejected stays claimable: approving a rejected item has always been allowed
	// (a reviewer changing their mind), and the claim must not change that.
	case ReviewStatusApplying:
		if staleAfter <= 0 || time.Since(item.UpdatedAt) < staleAfter {
			return nil, &ReviewClaimConflictError{ID: id, Status: from}
		}
		// Stale: keep the original prior status, not "applying".
		from = item.ApplyClaimedFrom
		if from == "" {
			from = ReviewStatusApproved
		}
	default:
		return nil, &ReviewClaimConflictError{ID: id, Status: from}
	}
	return p.writeClaimLocked(item, ReviewStatusApplying, from)
}

func (p *PebbleStore) ReleaseReviewItemClaim(id string) error {
	p.reviewMu.Lock()
	defer p.reviewMu.Unlock()

	item, err := p.getReviewItem(id)
	if err != nil || item == nil || item.Status != ReviewStatusApplying {
		return err
	}
	back := item.ApplyClaimedFrom
	if back == "" {
		back = ReviewStatusApproved
	}
	_, err = p.writeClaimLocked(item, back, "")
	return err
}

// writeClaimLocked writes item with a new status and claimedFrom in one synced
// batch, moving the status-index row. Caller holds reviewMu.
func (p *PebbleStore) writeClaimLocked(item *ReviewItem, status, claimedFrom string) (*ReviewItem, error) {
	oldStatus := item.Status
	item.Status = status
	item.ApplyClaimedFrom = claimedFrom
	item.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(item)
	if err != nil {
		return nil, fmt.Errorf("marshal review item: %w", err)
	}
	b := p.db.NewBatch()
	defer b.Close()
	if err := b.Set(reviewItemRecKey(item.ID), data, nil); err != nil {
		return nil, err
	}
	if oldStatus != "" && oldStatus != status {
		if err := b.Delete(reviewItemStatusKey(oldStatus, item.ID), nil); err != nil {
			return nil, err
		}
	}
	if err := b.Set(reviewItemStatusKey(status, item.ID), nil, nil); err != nil {
		return nil, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return item, nil
}
