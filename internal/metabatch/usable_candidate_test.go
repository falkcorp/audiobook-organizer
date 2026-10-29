// file: internal/metabatch/usable_candidate_test.go
// version: 1.0.2
// guid: 2be72b85-1d35-4b20-a84d-8612dec97417
// last-edited: 2026-10-29

package metabatch

import (
	"encoding/json"
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

type filesStore map[string][]database.BookFile

func (f filesStore) GetBookFiles(id string) ([]database.BookFile, error) { return f[id], nil }

// A cached candidate with no stamp and an old boosted score, for a book the
// audio intro named: the apply gate's score leg compares its runtime LIVE.
// A fragment-length book refuses it (transcription_runtime_unverified), the
// merge ranker ranks it refused, and the fallback trigger still counts it
// usable because a fallback provider carries no runtime and could not cure it.
func TestCandidateRefusal_TranscriptionRuntimeLive(t *testing.T) {
	heard := "Year One"
	book := &database.Book{ID: "b1", Title: "Year One", TranscribedTitle: &heard, TranscribedAuthor: new("Alfred Pennyworth")}
	cand := metafetch.MetadataCandidate{Source: "Audible", Title: "Year One", Author: "Alfred Pennyworth", Score: 3.09, DurationSec: 36000}
	raw, err := json.Marshal(cand)
	if err != nil {
		t.Fatal(err)
	}
	entry := &database.MetadataCandidateCache{Candidates: []json.RawMessage{raw}}

	fragment := filesStore{"b1": {{ID: "f1", BookID: "b1", FileSize: 28_800_000, Duration: 1800}}} // 30 min of a 10 h book
	whole := filesStore{"b1": {{ID: "f1", BookID: "b1", FileSize: 288_000_000, Duration: 36000}}}

	if why := CandidateRefusal(metafetch.RejectedSet{}, book, nil, &cand, RuntimeLoaderFor(fragment, book)); why != "transcription_runtime_unverified" {
		t.Errorf("fragment book: refusal %q, want transcription_runtime_unverified", why)
	}
	if why := CandidateRefusal(metafetch.RejectedSet{}, book, nil, &cand, RuntimeLoaderFor(whole, book)); why != "" {
		t.Errorf("whole book: refusal %q, want usable", why)
	}
	if why := CandidateRefusal(metafetch.RejectedSet{}, book, nil, &cand, nil); why != "transcription_runtime_unverified" {
		t.Errorf("no runtime source: refusal %q, want the unknown runtime to refuse", why)
	}
	if got := MergeRankerFor(book, metafetch.RejectedSet{}, RuntimeLoaderFor(fragment, book))(cand); got != MergeRankRefused {
		t.Errorf("merge rank for the fragment = %d, want MergeRankRefused", got)
	}
	if v := NoUsableCandidate(nil, fragment, book, entry); !v.Usable {
		t.Errorf("NoUsableCandidate = %+v; a runtime-unverified candidate must not trigger the fallback", v)
	}
	// Control: a below-floor candidate still counts as unusable.
	low := cand
	low.Score = 0.1
	lowRaw, _ := json.Marshal(low)
	if v := NoUsableCandidate(nil, whole, book, &database.MetadataCandidateCache{Candidates: []json.RawMessage{lowRaw}}); v.Usable {
		t.Errorf("a below-floor candidate must still be unusable, got %+v", v)
	}
}

// The merge ranking (synthetic fixtures): a usable chain candidate, then a
// usable review-only one, then a refused-but-reviewable one, then an
// owner-rejected one -- whatever their scores.
func TestMergeRanker_Tiers(t *testing.T) {
	tenHours := 36000
	book := &database.Book{ID: "b1", Title: "Tier Example", Duration: &tenHours}
	rank := MergeRanker(rejectedReader{"Audible|Rejected Tier": true}, nil, book)
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
	if why := CandidateRefusal(metafetch.RejectedSet{}, book, nil, &cases[1].c, nil); why != "" {
		t.Errorf("CandidateRefusal(usable Google) = %q, want usable", why)
	}
}
