// file: internal/database/review_claim.go
// version: 3.3.0
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
// ReleaseReviewItemClaim (applying → the status it was claimed from) or
// HoldReviewItemClaim (applying, claim held by no process), each checked
// against the claim's token — or, for a claim no running process holds, an
// operator's AbandonReviewItemClaim (applying → needs_attention). No decision write
// may replace it — DecideReviewItem refuses, and the store has no other status
// writer — because a reject or a "separate" landing over a live claim un-claims the
// item while its apply is still running, and the next approve then claims it again
// and applies a second time. Every method here runs under reviewMu, the mutex every
// other review writer takes, and moves the status-index row in the same synced batch
// as the record.
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
	// Pending, approved, rejected and needs_attention items are claimable; so is an
	// applying item whose claim no running process holds, for the same action
	// (Claim.TookOver). A takeover's claim is marked ApplyClaimRerun.
	ClaimReviewItemForApply(id, action string, ifUpdatedAt time.Time) (*ReviewApplyClaim, error)

	// FinishReviewItemApply moves the claimed item to applied and records the
	// claim's action as the item's ChosenAction. A token that does not hold the
	// claim is a *ReviewClaimConflictError with ReviewClaimNotHeld. Returns
	// (nil, nil) when the item does not exist.
	FinishReviewItemApply(id, token string) (*ReviewItem, error)

	// ReleaseReviewItemClaim ends a claim whose apply failed having written nothing
	// (NothingWrittenError). A first-run claim puts the item back to the status it was
	// claimed from. A RE-RUN claim (one that took over an interrupted apply) does not:
	// the first run may already have changed the library, so the item is held exactly as
	// HoldReviewItemClaim holds it. Returns the item as left, or (nil, nil) when the
	// item is missing or token does not hold the claim (nothing is changed).
	ReleaseReviewItemClaim(id, token string) (*ReviewItem, error)

	// HoldReviewItemClaim ends a claim whose apply failed and may have written
	// something: a first run that failed after its first write, or one that could not
	// say (a plain error, a panic). The item stays applying, the claim marked as held
	// by no process, with its claimed-from status and action kept: it keeps answering
	// "interrupted" until an approve of the same action takes it over and re-runs the
	// apply (Claim.TookOver) or an operator abandons it. This is the state a failed
	// re-run leaves; ReleaseReviewItemClaim of a re-run claim is this call. Returns the
	// item as left, or (nil, nil) when the item is missing or token does not hold the
	// claim (nothing is changed).
	HoldReviewItemClaim(id, token string) (*ReviewItem, error)

	// AbandonReviewItemClaim is the operator's way out of an interrupted apply that
	// cannot or should not be re-run (its re-run keeps failing, or its action has no
	// apply handler any more). It moves an applying item whose claim no running
	// process holds to needs_attention, recording the abandoned action in
	// ApplyAbandonedAction AND as ChosenAction, and clears the claim. ChosenAction is
	// what an approve with no explicit action (an empty body, or bulk by ids) resolves
	// to (effectiveActionFor), so without it that approve would run the payload's
	// recommendation instead of the action the operator abandoned. It refuses with a
	// *ReviewClaimConflictError a live claim (ReviewClaimInProgress), an applied item
	// (ReviewClaimApplied) and any item not under a claim (ReviewClaimNotClaimed).
	// Returns (nil, nil) when the item does not exist.
	AbandonReviewItemClaim(id string) (*ReviewItem, error)

	// DecideReviewItem records a human decision (status plus, when non-empty, the
	// chosen action) on an item no apply has claimed or finished. It refuses with a
	// *ReviewClaimConflictError when the item is applied or applying — under a live
	// claim or a dead one, because a dead claim's apply may already have changed the
	// library, and only re-running it (an approve, which takes the claim over) leaves
	// a record that matches what happened. status must be pending, approved or
	// rejected: applying and applied belong to the claim, needs_attention to
	// AbandonReviewItemClaim, and any other string would park the item in a status no
	// list shows. A non-zero ifUpdatedAt makes it a compare-and-set
	// like the claim's (ReviewClaimChanged). Returns (nil, nil) when the item does not
	// exist.
	//
	// The write is narrow: the record is re-read under the lock and only Status,
	// UpdatedAt and (when non-empty) ChosenAction change, so a caller holding a stale
	// copy cannot roll Payload or Summary back, and an empty chosenAction never
	// clears a recorded decision.
	DecideReviewItem(id, status, chosenAction string, ifUpdatedAt time.Time) (*ReviewItem, error)
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
	// ReviewClaimNotClaimed: an abandon named an item that is under no claim.
	ReviewClaimNotClaimed ReviewClaimConflictReason = "not_claimed"
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
	case ReviewClaimNotClaimed:
		return fmt.Sprintf("review item %s is %s, not under an apply claim", e.ID, e.Status)
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
		// A blank instance is a claim a failed re-run, or a first run that failed after
		// writing, left (HoldReviewItemClaim): held by no process. self is never blank.
		reason := ReviewClaimInterrupted
		if item.ApplyClaimInstance != "" && item.ApplyClaimInstance == self {
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
	case ReviewStatusPending, ReviewStatusApproved, ReviewStatusRejected, ReviewStatusNeedsAttention:
	// Rejected stays claimable: approving a rejected item has always been allowed
	// (a reviewer changing their mind), and the claim must not change that. So does
	// needs_attention: an operator who has inspected the library may apply it.
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
	// ChosenAction is deliberately NOT written here: a first run that failed having
	// written nothing is released back to the status it was claimed from, and an
	// override recorded by a claim would outlive an apply that never finished,
	// becoming what a later approve with no action runs. The action rides on the claim (ApplyClaimAction) until the
	// apply is finished (ChosenAction) or abandoned (ChosenAction, as the action
	// that may be half done).
	item.Status = ReviewStatusApplying
	item.ApplyClaimedFrom = from
	item.ApplyClaimAction = action
	item.ApplyClaimToken = token
	item.ApplyClaimInstance = self
	item.ApplyClaimRerun = tookOver
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
	item.ApplyAbandonedAction = ""
	clearReviewClaim(item)
	if err := p.commitReviewItemLocked(item, ReviewStatusApplying); err != nil {
		return nil, err
	}
	return item, nil
}

func (p *PebbleStore) ReleaseReviewItemClaim(id, token string) (*ReviewItem, error) {
	p.reviewMu.Lock()
	defer p.reviewMu.Unlock()

	item, err := p.getReviewItem(id)
	if err != nil || item == nil {
		return nil, err
	}
	if item.Status != ReviewStatusApplying || token == "" || item.ApplyClaimToken != token {
		return nil, nil
	}
	if item.ApplyClaimRerun {
		// The re-run failed over a library the interrupted first run may have
		// changed: held, whatever the failure was.
		return p.holdReviewItemClaimLocked(item)
	}
	back := item.ApplyClaimedFrom
	if back == "" {
		back = ReviewStatusApproved
	}
	item.Status = back
	clearReviewClaim(item)
	if err := p.commitReviewItemLocked(item, ReviewStatusApplying); err != nil {
		return nil, err
	}
	return item, nil
}

func (p *PebbleStore) HoldReviewItemClaim(id, token string) (*ReviewItem, error) {
	p.reviewMu.Lock()
	defer p.reviewMu.Unlock()

	item, err := p.getReviewItem(id)
	if err != nil || item == nil {
		return nil, err
	}
	if item.Status != ReviewStatusApplying || token == "" || item.ApplyClaimToken != token {
		return nil, nil
	}
	return p.holdReviewItemClaimLocked(item)
}

// holdReviewItemClaimLocked leaves item applying with its claim held by no process:
// the token and instance are blanked, everything else (claimed-from status, action)
// stays, so the item reads as interrupted (claimConflictLocked) and an approve of the
// same action takes it over. The one place a held claim is made, shared by a failed
// re-run's release and a failed first run's hold. Caller holds reviewMu and has
// checked the claim.
func (p *PebbleStore) holdReviewItemClaimLocked(item *ReviewItem) (*ReviewItem, error) {
	item.ApplyClaimToken = ""
	item.ApplyClaimInstance = ""
	if err := p.commitReviewItemLocked(item, ReviewStatusApplying); err != nil {
		return nil, err
	}
	return item, nil
}

func (p *PebbleStore) AbandonReviewItemClaim(id string) (*ReviewItem, error) {
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
	switch {
	case conflict == nil:
		return nil, &ReviewClaimConflictError{ID: id, Status: item.Status, Reason: ReviewClaimNotClaimed}
	case conflict.Reason != ReviewClaimInterrupted:
		return nil, conflict
	}
	item.ApplyAbandonedAction = item.ApplyClaimAction
	// The abandoned action is the last action anyone chose for this item (the claim
	// was an approve of it, possibly an override of the payload's recommendation, and
	// nothing records an override until FinishReviewItemApply). Record it, so that
	// approving the needs_attention item without naming an action re-runs THAT
	// action and not the recommendation the reviewer overrode. A claim's action is
	// never blank (ClaimReviewItemForApply refuses one); the guard keeps a damaged
	// record from erasing a recorded choice.
	if item.ApplyClaimAction != "" {
		item.ChosenAction = item.ApplyClaimAction
	}
	item.Status = ReviewStatusNeedsAttention
	clearReviewClaim(item)
	if err := p.commitReviewItemLocked(item, ReviewStatusApplying); err != nil {
		return nil, err
	}
	return item, nil
}

func (p *PebbleStore) DecideReviewItem(id, status, chosenAction string, ifUpdatedAt time.Time) (*ReviewItem, error) {
	switch status {
	case ReviewStatusPending, ReviewStatusApproved, ReviewStatusRejected:
	case ReviewStatusApplying, ReviewStatusApplied:
		return nil, fmt.Errorf("review item %s: status %q is written only by the apply claim", id, status)
	case ReviewStatusNeedsAttention:
		return nil, fmt.Errorf("review item %s: status %q is written only by AbandonReviewItemClaim", id, status)
	default:
		return nil, fmt.Errorf("review item %s: %q is not a review decision (pending, approved or rejected)", id, status)
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
	if !ifUpdatedAt.IsZero() && !item.UpdatedAt.Equal(ifUpdatedAt) {
		return nil, &ReviewClaimConflictError{ID: id, Status: item.Status, Reason: ReviewClaimChanged}
	}
	oldStatus := item.Status
	item.Status = status
	// A decision on a needs_attention item is the operator's answer to the
	// abandoned apply; the note has done its job.
	item.ApplyAbandonedAction = ""
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
	item.ApplyClaimRerun = false
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
