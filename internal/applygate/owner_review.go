// file: internal/applygate/owner_review.go
// version: 1.1.0
// guid: 3e7b2c14-8d95-4f06-a1c3-6b9e0d4f7a28
// last-edited: 2026-09-13

package applygate

import "strings"

// An OWNER-REVIEWED apply is one where the owner clicked Apply in the review
// lane on the exact candidate the server would apply (the request pins it, and
// the server checks the pin against the cache). The gate exists so that
// UNREVIEWED bulk applies cannot corrupt data; on a reviewed apply the owner is
// the review, so the certainty legs report but do not refuse.
//
// Overridden (they are judgements about how sure the match is):
//   - score: score_below_floor and transcription_mismatch;
//   - sequence: all four sequence reasons;
//   - evidence: every hard contradiction except partial_book and
//     asin_conflict, and the MinAgreements count (insufficient_evidence).
//
// Still blocking (they are not about certainty):
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
	if v.Allowed || v.Reason == ReasonIdentityStale {
		return false
	}
	for _, ch := range v.Evidence.Checks {
		if ch.Outcome == OutcomeBlock && ownerReviewHardReasons[ch.Reason] {
			return false
		}
	}
	return true
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
	if v.Reason == ReasonIdentityStale {
		out = append(out, ReasonIdentityStale)
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
