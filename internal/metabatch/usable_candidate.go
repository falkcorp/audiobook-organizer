// file: internal/metabatch/usable_candidate.go
// version: 1.0.0
// guid: 6a925443-e10e-4dc5-af83-0dcf909b6256
// last-edited: 2026-10-06

package metabatch

import (
	"encoding/json"
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

// CandidateRefusal is why one candidate is not usable for book ("" =
// usable): owner-rejected (rejected: LoadRejectedCandidateKeys),
// asin_conflict (applygate.CheckASIN), identity_stale because the book's ASIN
// was replaced after entry was fetched (metafetch.CandidateASINStale; entry
// nil skips it), or the apply gate's score leg (applygate.ScoreGate: the
// floor, or a transcribed title the candidate does not match) -- the same
// rule the bulk apply gate applies.
func CandidateRefusal(rejected map[string]bool, book *database.Book, entry *database.MetadataCandidateCache, c *metafetch.MetadataCandidate) string {
	switch {
	case rejected[c.Source+"|"+c.Title]:
		return "owner-rejected"
	case applygate.CheckASIN(book, c).Outcome == applygate.OutcomeBlock:
		return applygate.ReasonASINConflict
	case metafetch.CandidateASINStale(entry, book, c) != nil:
		return applygate.ReasonIdentityStale + " (ASIN replaced)"
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

// UsableRanker is the merge ranking for book (metafetch
// SearchOptions.MergeUsable): CandidateRefusal accepts the candidate. The
// ASIN-replaced leg is not needed: a merge only carries candidates fetched
// for the ASIN the book holds now.
func UsableRanker(kv RejectedCandidateReader, book *database.Book) func(metafetch.MetadataCandidate) bool {
	rejected := rejectedKeys(kv, book.ID)
	return func(c metafetch.MetadataCandidate) bool { return CandidateRefusal(rejected, book, nil, &c) == "" }
}

func rejectedKeys(kv RejectedCandidateReader, bookID string) map[string]bool {
	if kv == nil {
		return nil
	}
	return LoadRejectedCandidateKeys(kv, bookID)
}
