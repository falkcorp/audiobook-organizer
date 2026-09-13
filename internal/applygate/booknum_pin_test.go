// file: internal/applygate/booknum_pin_test.go
// version: 1.1.0
// guid: 5e8a1c47-9d20-4b36-a7f1-2c6d0b93e815
// last-edited: 2026-09-13

package applygate

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// TestSeriesNumberLost_NameNumbersArePinned pins how series_number treats a
// number that is part of the book's NAME, not a volume. Change these only on
// purpose.
//
//   - "Catch-22" -> "Catch Twenty-Two": the 22 is TRAILING and no series
//     position backs it, so it is read as a name or track number: neutral.
//   - "Room 101 - Stories" -> "Stories": the 101 stands between segments, the
//     shape of "Empire of Man 04 - We Few", so it still blocks as lost. That
//     is a deliberate cost: the row goes to manual review and nothing is
//     written.
func TestSeriesNumberLost_NameNumbersArePinned(t *testing.T) {
	cases := []struct {
		stored, cand, want string
	}{
		{"Catch-22", "Catch Twenty-Two", ""},
		{"Room 101 - Stories", "Stories", ReasonSeriesNumberLost},
	}
	for _, c := range cases {
		t.Run(c.stored, func(t *testing.T) {
			r := checkSeriesNumberLost(&database.Book{Title: c.stored}, &metafetch.MetadataCandidate{Title: c.cand}, []string{"title"})
			if r.Reason != c.want {
				t.Fatalf("%q -> %q: reason %q (%s), pinned %q", c.stored, c.cand, r.Reason, r.Detail, c.want)
			}
		})
	}
}

// TestCheckEvidence_SeriesNumberLostIsWired proves the check is reached from
// the evidence leg: dropping it from CheckEvidenceInBatch fails this test.
func TestCheckEvidence_SeriesNumberLostIsWired(t *testing.T) {
	book := database.Book{Title: "Empire of Man 04 - We Few", Duration: intp(61200),
		Author: &database.Author{Name: "David Weber"}, FilePath: "/lib/David Weber/Empire of Man/We Few.m4b"}
	cand := metafetch.MetadataCandidate{Title: "We Few", Author: "David Weber", DurationSec: 61200}
	v := CheckEvidence(&book, &cand, false)
	if v.Pass {
		t.Fatalf("passed: %+v", v)
	}
	for _, ch := range v.Checks {
		if ch.Name == "series_number" {
			if ch.Outcome != OutcomeBlock || ch.Reason != ReasonSeriesNumberLost {
				t.Fatalf("series_number ran but did not block: %+v", ch)
			}
			return
		}
	}
	t.Fatalf("series_number is not among the evidence checks: %+v", v.Checks)
}
