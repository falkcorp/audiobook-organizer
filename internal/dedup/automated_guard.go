// file: internal/dedup/automated_guard.go
// version: 1.2.0
// guid: 7bc59120-8b4f-48c2-8ac8-c8119a588036
// last-edited: 2026-09-25

package dedup

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Owner rule (2026-09-25): two kinds of candidate are REVIEW-QUEUE ONLY.
//
//   - A manual candidate (database.IsManualCandidate): a human enqueued or
//     pinned the pair so that a human decides it.
//   - A same-path pair (SamePathPair): two book rows resolving to one cleaned
//     file path.
//
// No automated path may merge, link, purge, restale or rescore them away. The
// passes that act on candidates without a human looking at each pair (LLM
// verdict apply, auto-resolve, bulk-link, link-series, drain-stale,
// purge-legacy-fp, rescore, the maintenance dismissals) go through the two
// guards below and through database.EmbeddingStore's guarded writes
// (ReclassifyCandidate, UpdateCandidateLLM, UpdateCandidateScores,
// DeleteCandidate), rather than each spelling the checks out.
//
// The store-side writes close the pin-during-pass race for status and score
// changes, because they re-check under the lock EnqueueManualCandidate holds.
// A merge cannot be made atomic with a pin: it spans the book store, the
// version groups and the undo journal. RecheckAutomatedMerge therefore
// re-reads the candidate immediately before the merge, which narrows the
// window to the merge call itself.

// AutomatedResolutionRefusal reports whether an automated pass must leave
// candidate c alone, and why. a and b are the pair's books; either may be nil
// when the caller has not loaded them, in which case only the manual check
// applies.
func AutomatedResolutionRefusal(c database.DedupCandidate, a, b *database.Book) (reason string, refused bool) {
	if database.IsManualCandidate(c) {
		return "manual: enqueued or pinned by a human, review queue only", true
	}
	if SamePathPair(a, b) {
		return "same_path: two book rows at one cleaned path, review queue only", true
	}
	return "", false
}

// pairPinnedForReview reports whether the book pair (aID, bID) has a pending
// manual candidate. It is for automated merges that start from a pair of books
// rather than a candidate row (the exact-file-hash auto-merge in
// handleFileHashMatch). A lookup error is reported as pinned, so the caller
// falls back to queueing the pair rather than merging it blind.
func (de *Engine) pairPinnedForReview(aID, bID string) (bool, error) {
	cands, err := de.embedStore.ListCandidatesForEntity("book", aID, "pending")
	if err != nil {
		return true, err
	}
	for _, c := range cands {
		if !database.IsManualCandidate(c) {
			continue
		}
		if (c.EntityAID == aID && c.EntityBID == bID) || (c.EntityAID == bID && c.EntityBID == aID) {
			return true, nil
		}
	}
	return false, nil
}

// CandidateReader is the one store method RecheckAutomatedMerge needs.
// *database.EmbeddingStore satisfies it, as does the dedup handler's narrow
// EmbeddingStore interface.
type CandidateReader interface {
	GetCandidateByID(id int64) (*database.DedupCandidate, error)
}

// RecheckAutomatedMerge re-reads candidate id and applies
// AutomatedResolutionRefusal to the row as it stands now, and refuses it if
// its status is no longer expectedStatus (the status the pass listed it
// under: "pending" for the engine's passes, the request's status filter for
// bulk-link). Call it immediately before an automated merge: the snapshot the
// pass listed may be hours old (an OpenAI batch) and a human may have pinned
// or decided the pair since. A row that cannot be read is refused.
func RecheckAutomatedMerge(store CandidateReader, id int64, expectedStatus string, a, b *database.Book) (reason string, refused bool) {
	if store == nil {
		return "candidate store unavailable", true
	}
	current, err := store.GetCandidateByID(id)
	if err != nil {
		return fmt.Sprintf("cannot re-read candidate %d: %v", id, err), true
	}
	if current == nil {
		return fmt.Sprintf("candidate %d no longer exists", id), true
	}
	if current.Status != expectedStatus {
		return fmt.Sprintf("candidate %d is %q, not %q", id, current.Status, expectedStatus), true
	}
	return AutomatedResolutionRefusal(*current, a, b)
}
