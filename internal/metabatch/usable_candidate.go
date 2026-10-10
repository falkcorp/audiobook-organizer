// file: internal/metabatch/usable_candidate.go
// version: 1.2.0
// guid: 6a925443-e10e-4dc5-af83-0dcf909b6256
// last-edited: 2026-10-10

package metabatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// UsableCandidateVerdict is NoUsableCandidate's finding.
type UsableCandidateVerdict struct {
	// Usable: at least one candidate survives every check.
	Usable bool
	// Why summarizes, when none is usable, what refused them.
	Why string
}

// candidateRefusalOwnerRejected is CandidateRefusal's owner-rejected reason.
const candidateRefusalOwnerRejected = "owner-rejected"

// CandidateRefusal is why one candidate is not usable for book ("" =
// usable): owner-rejected (rejected: LoadRejectedCandidateKeys),
// asin_conflict (applygate.CheckASIN), identity_stale because entry is marked
// stale (the book was retitled) or the book's ASIN was replaced after entry
// was fetched (metafetch.CandidateIdentityStale; entry
// nil skips it), or the apply gate's score leg (applygate.ScoreGate: the
// floor, or a transcribed title the candidate does not match) -- the same
// rule the bulk apply gate applies.
func CandidateRefusal(rejected metafetch.RejectedSet, book *database.Book, entry *database.MetadataCandidateCache, c *metafetch.MetadataCandidate) string {
	switch {
	case rejected.HasCandidate(c):
		return candidateRefusalOwnerRejected
	case applygate.CheckASIN(book, c).Outcome == applygate.OutcomeBlock:
		return applygate.ReasonASINConflict
	}
	if err := metafetch.CandidateIdentityStale(entry, book, c); err != nil {
		if errors.Is(err, metafetch.ErrCandidateASINReplaced) {
			return applygate.ReasonIdentityStale + " (ASIN replaced)"
		}
		return applygate.ReasonIdentityStale + " (retitled)"
	}
	if ok, _, _, reason := applygate.ScoreGate(book, c); !ok {
		return reason
	}
	return ""
}

// NoUsableCandidate decides the candidate fetch's fallback trigger (owner
// decision 2026-10-06, "no usable candidate"): entry's candidates leave book
// without one when there are none or CandidateRefusal refuses every one --
// owner-rejected, asin_conflict, an ASIN-replaced identity_stale, or the
// apply gate's score leg. The review page's "deferred" count reads it too: a
// book with a usable candidate waits on no fallback lookup, whatever its
// row's attempts say.
//
// Only checks a FALLBACK candidate could pass are counted. The row-level
// identity check (the row was fetched by a stand-in query, or for an author
// since changed) is left out on purpose: the fallback searches the same
// query and its candidates land on the same row, so they would fail it
// identically, and Google quota would be spent on books it cannot help. A
// changed author re-opens the row anyway (CachedBatchVerdict re-asks it).
//
// kv is read for the owner's rejections; nil reads none.
func NoUsableCandidate(kv RejectedCandidateReader, book *database.Book, entry *database.MetadataCandidateCache) UsableCandidateVerdict {
	if entry == nil || len(entry.Candidates) == 0 {
		return UsableCandidateVerdict{Why: "no candidates"}
	}
	rejected := rejectedKeys(kv, book.ID)
	counts := map[string]int{}
	var order []string
	undecoded := 0
	for _, raw := range entry.Candidates {
		var c metafetch.MetadataCandidate
		if err := json.Unmarshal(raw, &c); err != nil {
			undecoded++
			continue
		}
		why := CandidateRefusal(rejected, book, entry, &c)
		if why == "" {
			return UsableCandidateVerdict{Usable: true}
		}
		if counts[why] == 0 {
			order = append(order, why)
		}
		counts[why]++
	}
	parts := make([]string, 0, len(order)+1)
	for _, why := range order {
		parts = append(parts, fmt.Sprintf("%d %s", counts[why], why))
	}
	if undecoded > 0 {
		parts = append(parts, fmt.Sprintf("%d undecodable", undecoded))
	}
	return UsableCandidateVerdict{Why: "no usable candidate: " + strings.Join(parts, ", ")}
}

// Merge ranks (metafetch SearchOptions.MergeRank; lower first). Usable means
// CandidateRefusal accepts the candidate -- the same rule the fallback
// trigger (NoUsableCandidate) uses, which counts a review-only candidate as
// usable: the fallback found the owner something to review, and asking again
// would only spend quota.
const (
	// MergeRankUsable: usable, and the gate may apply it unattended.
	MergeRankUsable = iota
	// MergeRankUsableReviewOnly: usable, but from a review-only source (Open
	// Library, Google Books; applygate.ReviewOnlySource). Ranked after an
	// equally usable chain candidate so the row's first candidate is one
	// the unattended paths (bulk apply, the transcription auto-apply) can
	// use when there is one.
	MergeRankUsableReviewOnly
	// MergeRankRefused: refused by asin_conflict or the score leg, but
	// still something the owner can review and apply by hand.
	MergeRankRefused
	// MergeRankOwnerRejected: the owner already said no. Ranked last, so
	// the row's top-10 cap evicts these first.
	MergeRankOwnerRejected
)

// MergeRanker is the merge ranking for book (metafetch
// SearchOptions.MergeRank): the MergeRank* tiers above. The ASIN-replaced
// leg is not needed: a merge only carries candidates fetched for the ASIN
// the book holds now.
func MergeRanker(kv RejectedCandidateReader, book *database.Book) func(metafetch.MetadataCandidate) int {
	return MergeRankerFor(book, rejectedKeys(kv, book.ID))
}

// MergeRankerFor is MergeRanker over rejections the caller already read
// (metafetch.LoadRejectedCandidates), for a caller that must not rank on a
// failed read as if nothing were rejected (the cached-row re-rank).
func MergeRankerFor(book *database.Book, rejected metafetch.RejectedSet) func(metafetch.MetadataCandidate) int {
	return func(c metafetch.MetadataCandidate) int {
		switch why := CandidateRefusal(rejected, book, nil, &c); {
		case why == candidateRefusalOwnerRejected:
			return MergeRankOwnerRejected
		case why != "":
			return MergeRankRefused
		case applygate.ReviewOnlySource(&c):
			return MergeRankUsableReviewOnly
		}
		return MergeRankUsable
	}
}

func rejectedKeys(kv RejectedCandidateReader, bookID string) metafetch.RejectedSet {
	if kv == nil {
		return nil
	}
	return LoadRejectedCandidateKeys(kv, bookID)
}
