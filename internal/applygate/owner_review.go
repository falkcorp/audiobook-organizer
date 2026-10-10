// file: internal/applygate/owner_review.go
// version: 1.7.0
// guid: 3e7b2c14-8d95-4f06-a1c3-6b9e0d4f7a28
// last-edited: 2026-10-10

package applygate

import "strings"

// An OWNER-REVIEWED apply is one the owner started from an apply button on the
// review page: a single row or a bulk button (owner ruling 2026-09-27, every
// review-page apply button is the owner's manual apply). The request pins the
// candidate each book showed and the server checks the pin against the cache;
// a book the lane held no candidate hash for carries a hashless owner marker
// instead (metafetch.CandidatePin.IsUnseenOwnerReview). The gate exists so
// that UNREVIEWED applies (scripts, API callers, the metadata upgrade,
// scheduled ops) cannot corrupt data; on a reviewed apply the owner is the
// review, so the certainty legs report but do not refuse.
//
// Overridden (they are judgements about how sure the match is):
//   - score: score_below_floor and transcription_mismatch;
//   - sequence: all four sequence reasons;
//   - evidence: every hard contradiction except partial_book and
//     asin_conflict, and the MinAgreements count (insufficient_evidence).
//
// Still blocking (they are not about certainty):
//   - owner_rejected and owner_rejection_check_failed: the owner already said
//     no to this candidate (owner_rejected.go). A pin is sent for whatever the
//     lane showed; only an explicit un-reject lifts a rejection.
//   - identity_stale: the cache row was fetched for a different title/author,
//     so the candidate answers a question the book no longer asks;
//   - partial_book: another folder holds a part of the same book. The owner
//     reviewed a candidate, not the folder layout, and applying one identity
//     onto half a book is corruption the candidate view does not show.
//   - asin_conflict: the book already carries a different ASIN. An ASIN is a
//     record identity, not a judgement; overwriting one on a review of the
//     title/author the row shows would silently re-point the book at another
//     Audible record.
//
//   - review_only_source, for the hashless marker only
//     (UnseenOwnerReviewOverridable): nobody saw the candidate it would apply.
//
// Everything outside the gate (book not found, a stale pin, a rename that
// cannot land, policy:no-metadata, field locks, the scan stand-down) is
// enforced by the caller exactly as for an unreviewed apply.

// ReasonOwnerReviewed marks an apply that went through on an owner review
// despite a refusing verdict. It is a log/report label, never a refusal.
const ReasonOwnerReviewed = "owner_reviewed"

// OwnerReviewOverridable reports whether an owner review may apply despite v:
// v refused, and every leg that refused is one an owner review overrides.
// false for an allowed verdict: there is nothing to override.
func (v Verdict) OwnerReviewOverridable() bool {
	if v.Allowed || v.Reason == ReasonIdentityStale || v.Reason == ReasonOwnerManualOnly ||
		v.Reason == ReasonOwnerManualCheckFailed || v.Reason == ReasonOwnerRejected ||
		v.Reason == ReasonOwnerRejectionCheckFailed {
		return false
	}
	for _, ch := range v.Evidence.Checks {
		if ch.Outcome == OutcomeBlock && ownerReviewHardReasons[ch.Reason] {
			return false
		}
	}
	return true
}

// UnseenOwnerReviewOverridable is OwnerReviewOverridable for the hashless
// owner marker (metafetch.CandidatePin.IsUnseenOwnerReview): a review-page
// bulk button applied a book whose candidate the lane never showed (select-all
// past the loaded rows, or a selection that outlived a refresh). It lifts the
// same certainty legs, except review_only_source. A review-only candidate
// (Open Library, Google Books) is applied only by an owner who SAW it, and the
// marker proves nobody did: the merged row ranks a fallback candidate first
// once it arrives, so the candidate the server would apply can be one that
// reached the cache after the lane loaded. planCachedApply already refuses to
// let the marker lift a no_match mark for the same reason. A hash-bearing pin
// that matches the shown candidate still lifts review_only_source.
func (v Verdict) UnseenOwnerReviewOverridable() bool {
	return v.Reason != ReasonReviewOnlySource && v.OwnerReviewOverridable()
}

// ownerReviewHardReasons are the evidence refusals an owner review does NOT
// lift (see the file comment for why).
var ownerReviewHardReasons = map[string]bool{
	ReasonPartialBook:  true,
	ReasonASINConflict: true,
}

// RefusingReasons lists every reason that refused in v, one per failing leg
// or evidence check, in leg order. A verdict reports only its first failing
// leg in Reason; an override record must name all of them.
func (v Verdict) RefusingReasons() []string {
	var out []string
	if v.Reason == ReasonOwnerRejected || v.Reason == ReasonOwnerRejectionCheckFailed {
		out = append(out, v.Reason)
	}
	if v.Reason == ReasonIdentityStale {
		out = append(out, ReasonIdentityStale)
	}
	if v.Reason == ReasonReviewOnlySource {
		out = append(out, ReasonReviewOnlySource)
	}
	if v.ScoreReason != "" {
		out = append(out, v.ScoreReason)
	}
	if !v.Sequence.Pass && v.Sequence.Reason != "" {
		out = append(out, v.Sequence.Reason)
	}
	if !v.Evidence.Pass {
		blocked := false
		for _, ch := range v.Evidence.Checks {
			if ch.Outcome == OutcomeBlock && ch.Reason != "" {
				out = append(out, ch.Reason)
				blocked = true
			}
		}
		if !blocked && v.Evidence.Reason != "" {
			out = append(out, v.Evidence.Reason)
		}
	}
	return out
}

// OverrideSummary is RefusingReasons joined for a log line or history label.
func (v Verdict) OverrideSummary() string {
	return strings.Join(v.RefusingReasons(), ", ")
}
