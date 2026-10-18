// file: internal/database/review_claim.go
// version: 2.0.0
// guid: 793a733c-f28c-4b2d-ba41-4170b8a43a07
// last-edited: 2026-10-18

package database

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// ReviewItemApplyClaimer owns every review-item write that can race an apply. It is
// a capability, not part of ReviewStore or Store: the review handler resolves it with
// AsCapability (store_capability.go). Implemented on *PebbleStore.
//
// 🔴 THE INVARIANT. An item in ReviewStatusApplying is held by exactly one claim, and
// only that claim leaves the state: FinishReviewItemApply (applying → applied) or
// ReleaseReviewItemClaim (applying → the status it was claimed from), each checked
// against the claim's token. No decision write may replace it — DecideReviewItem
// refuses, and so does the generic SetReviewItemDecision — because a reject or a
// "separate" landing over a live claim un-claims the item while its apply is still
// running, and the next approve then claims it again and applies a second time.
// Every method here runs under reviewMu, the mutex every other review writer takes,
// and moves the status-index row in the same synced batch as the record.
//
// 🔴 LIVENESS IS THE CLAIMING PROCESS, NOT A CLOCK. A claim records the instance id
// of the store handle that took it (reviewClaimInstance: random, made on first use,
// never persisted on its own). A claim carrying this handle's id belongs to an apply
// running in this process and is NEVER taken over, however long it runs — CombineBooks
// moves files and the regroup handlers finish their numbering under
// context.WithoutCancel, so no fixed timeout is safe. A claim carrying any other id was
// left by a process that has since exited (a crash, or a restart while an apply was
// unrecorded), so nothing can still be running it and the next approve of the SAME
// action takes it over at once. Production opens the store once per process
// (InitializeStore), so "this handle" and "this process" are the same thing. If the
// store is ever reopened inside a running server, drain in-flight review applies first:
// the new handle would treat the old handle's claims as dead.
type ReviewItemApplyClaimer interface {
	// ClaimReviewItemForApply atomically moves an item to applying under a fresh
	// token, recording the action being applied. Returns (nil, nil) when the item
	// does not exist and a *ReviewClaimConflictError when it may not be claimed:
	//   - applied: already done;
	//   - applying under a claim from this process: an apply is running;
	//   - applying under a claim from a previous process, for a DIFFERENT action:
	//     the interrupted apply must be finished as the action it started as;
	//   - the record's UpdatedAt differs from ifUpdatedAt (when non-zero): the item
	//     changed after the caller read it, so the action the caller resolved from
	//     that read may no longer be what the item means.
	// Pending, approved and rejected items are claimable; so is an applying item
	// whose claim a previous process left, for the same action (Claim.TookOver).
	ClaimReviewItemForApply(id, action string, ifUpdatedAt time.Time) (*ReviewApplyClaim, error)

	// FinishReviewItemApply moves the claimed item to applied and records the
	// claim's action as the item's ChosenAction. A token that does not hold the
	// claim is a *ReviewClaimConflictError with ReviewClaimNotHeld. Returns
	// (nil, nil) when the item does not exist.
	FinishReviewItemApply(id, token string) (*ReviewItem, error)

	// ReleaseReviewItemClaim puts a claimed item back to the status it was claimed
	// from. It reports false, and changes nothing, when token does not hold the
	// claim. Used when the apply handler failed.
	ReleaseReviewItemClaim(id, token string) (bool, error)

	// DecideReviewItem records a human decision (status plus, when non-empty, the
	// chosen action) on an item no apply has claimed or finished. It refuses with a
	// *ReviewClaimConflictError when the item is applied or applying — under a live
	// claim or a dead one, because a dead claim's apply may already have changed the
	// library, and only re-running it (an approve, which takes the claim over) leaves
	// a record that matches what happened. status may not be applying or applied;
	// those belong to the claim. Returns (nil, nil) when the item does not exist.
	DecideReviewItem(id, status, chosenAction string) (*ReviewItem, error)
}

var _ ReviewItemApplyClaimer = (*PebbleStore)(nil)

// ReviewApplyClaim is a claim the caller now holds.
type ReviewApplyClaim struct {
	// Item is the record as claimed, read under the lock: status applying, payload
	// as it stands now. Apply this, not a copy read before the claim.
	Item *ReviewItem
	// Token is what FinishReviewItemApply and ReleaseReviewItemClaim check.
	Token string
	// TookOver is true when the claim replaced one a previous process left behind:
	// the apply about to run is a re-run of one that was interrupted (or ran but was
	// never recorded), so it must tolerate work already done.
	TookOver bool
}

// ReviewClaimConflictReason says why a claim or decision was refused.
type ReviewClaimConflictReason string

const (
	// ReviewClaimApplied: the item is already applied.
	ReviewClaimApplied ReviewClaimConflictReason = "applied"
	// ReviewClaimInProgress: an apply in this process holds the claim.
	ReviewClaimInProgress ReviewClaimConflictReason = "in_progress"
	// ReviewClaimInterrupted: a previous process left the claim. Only an approve of
	// the claim's own action (ReviewClaimConflictError.ClaimAction) may take it over.
	ReviewClaimInterrupted ReviewClaimConflictReason = "interrupted"
	// ReviewClaimChanged: the record changed after the caller read it.
	ReviewClaimChanged ReviewClaimConflictReason = "changed"
	// ReviewClaimNotHeld: a finish named a token that does not hold the claim.
	ReviewClaimNotHeld ReviewClaimConflictReason = "not_held"
)

// ReviewClaimConflictError says why a claim, finish or decision was refused.
type ReviewClaimConflictError struct {
	ID     string
	Status string // the item's status when refused
	Reason ReviewClaimConflictReason
	// ClaimAction is the action an applying item's claim is applying ("" otherwise).
	ClaimAction string
}

func (e *ReviewClaimConflictError) Error() string {
	switch e.Reason {
	case ReviewClaimInProgress:
		return fmt.Sprintf("review item %s: an apply of %q is running in this process", e.ID, e.ClaimAction)
	case ReviewClaimInterrupted:
		return fmt.Sprintf("review item %s: an apply of %q was interrupted by a restart; only an approve of %q can finish it",
			e.ID, e.ClaimAction, e.ClaimAction)
	case ReviewClaimChanged:
		return fmt.Sprintf("review item %s changed after it was read", e.ID)
	case ReviewClaimNotHeld:
		return fmt.Sprintf("review item %s is %s and the claim is not held by this token", e.ID, e.Status)
	}
	return fmt.Sprintf("review item %s is %s", e.ID, e.Status)
}

// reviewClaimInstance is a store handle's claim instance id, made on first use.
type reviewClaimInstance struct {
	once sync.Once
	id   string
	err  error
}

// claimInstanceID returns this handle's instance id. Random (crypto/rand), so no two
// processes share one; never written anywhere except into the claims it takes.
func (p *PebbleStore) claimInstanceID() (string, error) {
	p.reviewClaim.once.Do(func() {
		p.reviewClaim.id, p.reviewClaim.err = newUnlinkedULID()
	})
	return p.reviewClaim.id, p.reviewClaim.err
}

// claimConflictLocked describes why an applying or applied item refuses a write.
// Caller holds reviewMu. Returns nil for any other status.
func (p *PebbleStore) claimConflictLocked(item *ReviewItem) (*ReviewClaimConflictError, error) {
	switch item.Status {
	case ReviewStatusApplied:
		return &ReviewClaimConflictError{ID: item.ID, Status: item.Status, Reason: ReviewClaimApplied}, nil
	case ReviewStatusApplying:
		self, err := p.claimInstanceID()
		if err != nil {
			return nil, err
		}
		reason := ReviewClaimInterrupted
		if item.ApplyClaimInstance == self {
			reason = ReviewClaimInProgress
		}
		return &ReviewClaimConflictError{ID: item.ID, Status: item.Status, Reason: reason, ClaimAction: item.ApplyClaimAction}, nil
	}
	return nil, nil
}

func (p *PebbleStore) ClaimReviewItemForApply(id, action string, ifUpdatedAt time.Time) (*ReviewApplyClaim, error) {
	if action == "" {
		return nil, fmt.Errorf("review item %s: claim requires the action being applied", id)
	}
	self, err := p.claimInstanceID()
	if err != nil {
		return nil, fmt.Errorf("review claim instance id: %w", err)
	}
	p.reviewMu.Lock()
	defer p.reviewMu.Unlock()

	item, err := p.getReviewItem(id)
	if err != nil || item == nil {
		return nil, err
	}
	from := item.Status
	tookOver := false
	switch from {
	case ReviewStatusPending, ReviewStatusApproved, ReviewStatusRejected:
	// Rejected stays claimable: approving a rejected item has always been allowed
	// (a reviewer changing their mind), and the claim must not change that.
	case ReviewStatusApplying:
		conflict, cerr := p.claimConflictLocked(item)
		if cerr != nil {
			return nil, cerr
		}
		if conflict.Reason == ReviewClaimInProgress {
			return nil, conflict
		}
		// Dead claim. Its apply may have run partly or wholly, so the takeover must
		// re-run the SAME action; any other action over a half-applied item is a
		// decision nobody can make safely.
		if item.ApplyClaimAction != "" && item.ApplyClaimAction != action {
			return nil, conflict
		}
		// Keep the status it held before the interrupted claim, not "applying".
		from = item.ApplyClaimedFrom
		if from == "" {
			from = ReviewStatusApproved
		}
		tookOver = true
	default:
		conflict, cerr := p.claimConflictLocked(item)
		if cerr != nil {
			return nil, cerr
		}
		if conflict == nil {
			conflict = &ReviewClaimConflictError{ID: id, Status: from}
		}
		return nil, conflict
	}
	if !ifUpdatedAt.IsZero() && !item.UpdatedAt.Equal(ifUpdatedAt) {
		return nil, &ReviewClaimConflictError{ID: id, Status: item.Status, Reason: ReviewClaimChanged, ClaimAction: item.ApplyClaimAction}
	}
	token, err := newUnlinkedULID()
	if err != nil {
		return nil, fmt.Errorf("review claim token: %w", err)
	}
	oldStatus := item.Status
	item.Status = ReviewStatusApplying
	item.ApplyClaimedFrom = from
	item.ApplyClaimAction = action
	item.ApplyClaimToken = token
	item.ApplyClaimInstance = self
	if err := p.commitReviewItemLocked(item, oldStatus); err != nil {
		return nil, err
	}
	return &ReviewApplyClaim{Item: item, Token: token, TookOver: tookOver}, nil
}

func (p *PebbleStore) FinishReviewItemApply(id, token string) (*ReviewItem, error) {
	p.reviewMu.Lock()
	defer p.reviewMu.Unlock()

	item, err := p.getReviewItem(id)
	if err != nil || item == nil {
		return nil, err
	}
	if item.Status != ReviewStatusApplying || token == "" || item.ApplyClaimToken != token {
		return nil, &ReviewClaimConflictError{ID: id, Status: item.Status, Reason: ReviewClaimNotHeld, ClaimAction: item.ApplyClaimAction}
	}
	if item.ApplyClaimAction != "" {
		item.ChosenAction = item.ApplyClaimAction
	}
	item.Status = ReviewStatusApplied
	clearReviewClaim(item)
	if err := p.commitReviewItemLocked(item, ReviewStatusApplying); err != nil {
		return nil, err
	}
	return item, nil
}

func (p *PebbleStore) ReleaseReviewItemClaim(id, token string) (bool, error) {
	p.reviewMu.Lock()
	defer p.reviewMu.Unlock()

	item, err := p.getReviewItem(id)
	if err != nil || item == nil {
		return false, err
	}
	if item.Status != ReviewStatusApplying || token == "" || item.ApplyClaimToken != token {
		return false, nil
	}
	back := item.ApplyClaimedFrom
	if back == "" {
		back = ReviewStatusApproved
	}
	item.Status = back
	clearReviewClaim(item)
	if err := p.commitReviewItemLocked(item, ReviewStatusApplying); err != nil {
		return false, err
	}
	return true, nil
}

func (p *PebbleStore) DecideReviewItem(id, status, chosenAction string) (*ReviewItem, error) {
	switch status {
	case "":
		return nil, fmt.Errorf("review item: status is required")
	case ReviewStatusApplying, ReviewStatusApplied:
		return nil, fmt.Errorf("review item %s: status %q is written only by the apply claim", id, status)
	}
	p.reviewMu.Lock()
	defer p.reviewMu.Unlock()

	item, err := p.getReviewItem(id)
	if err != nil || item == nil {
		return nil, err
	}
	conflict, err := p.claimConflictLocked(item)
	if err != nil {
		return nil, err
	}
	if conflict != nil {
		return nil, conflict
	}
	oldStatus := item.Status
	item.Status = status
	if chosenAction != "" {
		item.ChosenAction = chosenAction
	}
	if err := p.commitReviewItemLocked(item, oldStatus); err != nil {
		return nil, err
	}
	return item, nil
}

// clearReviewClaim drops every claim field: a finished or released claim leaves
// nothing behind.
func clearReviewClaim(item *ReviewItem) {
	item.ApplyClaimedFrom = ""
	item.ApplyClaimAction = ""
	item.ApplyClaimToken = ""
	item.ApplyClaimInstance = ""
}

// commitReviewItemLocked writes item (stamping UpdatedAt) and moves its status-index
// row from oldStatus in one synced batch. Caller holds reviewMu and has just read the
// record, so oldStatus is the status the index row is under.
func (p *PebbleStore) commitReviewItemLocked(item *ReviewItem, oldStatus string) error {
	item.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("marshal review item: %w", err)
	}
	b := p.db.NewBatch()
	defer b.Close()
	if err := b.Set(reviewItemRecKey(item.ID), data, nil); err != nil {
		return err
	}
	if oldStatus != "" && oldStatus != item.Status {
		if err := b.Delete(reviewItemStatusKey(oldStatus, item.ID), nil); err != nil {
			return err
		}
	}
	if err := b.Set(reviewItemStatusKey(item.Status, item.ID), nil, nil); err != nil {
		return err
	}
	return b.Commit(pebble.Sync)
}
