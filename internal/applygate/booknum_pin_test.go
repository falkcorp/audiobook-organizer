// file: internal/applygate/booknum_pin_test.go
// version: 1.0.0
// guid: 5e8a1c47-9d20-4b36-a7f1-2c6d0b93e815
// last-edited: 2026-09-13

package applygate

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// TestSeriesNumberLost_NameNumbersArePinned pins a deliberate cost of
// series_number_lost (review F6): a number that is part of the book's NAME,
// not a volume, still blocks a title overwrite that drops it. Those rows go to
// manual review and nothing is written; telling a name-number from a volume
// needs knowledge the stored title does not carry. Change these only on
// purpose.
func TestSeriesNumberLost_NameNumbersArePinned(t *testing.T) {
	cases := []struct {
		stored, cand string
	}{
		{"Catch-22", "Catch Twenty-Two"},
		{"Room 101 - Stories", "Stories"},
	}
	for _, c := range cases {
		t.Run(c.stored, func(t *testing.T) {
			r := checkSeriesNumberLost(&database.Book{Title: c.stored}, &metafetch.MetadataCandidate{Title: c.cand}, []string{"title"})
			if r.Reason != ReasonSeriesNumberLost {
				t.Fatalf("%q -> %q: reason %q (%s); pinned as a block", c.stored, c.cand, r.Reason, r.Detail)
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
				t.Fatalf("series_number_lost ran but did not block: %+v", ch)
			}
			return
		}
	}
	t.Fatalf("series_number_lost is not among the evidence checks: %+v", v.Checks)
}
