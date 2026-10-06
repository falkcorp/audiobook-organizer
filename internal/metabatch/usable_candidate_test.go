// file: internal/metabatch/usable_candidate_test.go
// version: 1.0.0
// guid: 2be72b85-1d35-4b20-a84d-8612dec97417
// last-edited: 2026-10-06

package metabatch

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

type rejectedReader map[string]bool

func (r rejectedReader) ScanPrefix(prefix string) ([]database.KVPair, error) {
	var out []database.KVPair
	for k := range r {
		out = append(out, database.KVPair{Key: prefix + k, Value: []byte("1")})
	}
	return out, nil
}

// The merge ranking (synthetic fixtures): a usable chain candidate, then a
// usable review-only one, then a refused-but-reviewable one, then an
// owner-rejected one -- whatever their scores.
func TestMergeRanker_Tiers(t *testing.T) {
	tenHours := 36000
	book := &database.Book{ID: "b1", Title: "Tier Example", Duration: &tenHours}
	rank := MergeRanker(rejectedReader{"Audible|Rejected Tier": true}, book)
	cases := []struct {
		name string
		c    metafetch.MetadataCandidate
		want int
	}{
		{"usable chain", metafetch.MetadataCandidate{Source: "Audible", Title: "Tier Example", Score: 0.95, DurationSec: 36000}, MergeRankUsable},
		{"usable Google", metafetch.MetadataCandidate{Source: "Google Books", Title: "Tier Example", Score: 0.99, DurationSec: 36000}, MergeRankUsableReviewOnly},
		{"usable Open Library", metafetch.MetadataCandidate{Source: "Open Library", Title: "Tier Example", Score: 0.99, DurationSec: 36000}, MergeRankUsableReviewOnly},
		{"below floor", metafetch.MetadataCandidate{Source: "Audible", Title: "Tier Example", Score: 0.1, DurationSec: 36000}, MergeRankRefused},
		{"owner-rejected", metafetch.MetadataCandidate{Source: "Audible", Title: "Rejected Tier", Score: 0.99, DurationSec: 36000}, MergeRankOwnerRejected},
	}
	for _, tc := range cases {
		if got := rank(tc.c); got != tc.want {
			t.Errorf("%s: rank %d, want %d", tc.name, got, tc.want)
		}
	}
	// The fallback trigger still counts a usable review-only candidate as
	// usable: the fallback found something to review.
	if why := CandidateRefusal(nil, book, nil, &cases[1].c); why != "" {
		t.Errorf("CandidateRefusal(usable Google) = %q, want usable", why)
	}
}
