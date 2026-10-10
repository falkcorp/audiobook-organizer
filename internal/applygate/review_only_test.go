// file: internal/applygate/review_only_test.go
// version: 1.1.1
// guid: c09f46a0-0fa5-4191-b572-ea43cc5ace8b
// last-edited: 2026-10-10

package applygate

import (
	"errors"
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// Owner decision 2026-10-06: Open Library and Google Books candidates (the
// candidate fetch's fallback providers) are REVIEW-ONLY. Every bulk
// evaluation refuses one however good it is; an owner review applies it --
// but never past a stale identity.
func TestEvaluate_ReviewOnlySources(t *testing.T) {
	book := &database.Book{Title: "Synthetic Field Notes 1", Duration: intp(36000)}
	rt := database.ComputeBookRuntime(book, nil)
	for _, source := range []string{"Open Library", "Google Books"} {
		c := metafetch.MetadataCandidate{Source: source, Title: "Synthetic Field Notes 1", SeriesPosition: "1", Score: 0.95, DurationSec: 36000}
		audible := c
		audible.Source = "Audible"
		if v := Evaluate(book, snap(book), rt, &audible, nil); !v.Allowed {
			t.Fatalf("fixture: the same candidate from Audible is refused: %+v", v)
		}

		v := Evaluate(book, snap(book), rt, &c, nil)
		if v.Allowed || v.Reason != ReasonReviewOnlySource {
			t.Fatalf("%s: allowed=%v reason=%q, want refused %q", source, v.Allowed, v.Reason, ReasonReviewOnlySource)
		}
		if v2 := EvaluateInBatch(book, snap(book), rt, &c, nil, nil, nil); v2.Allowed || v2.Reason != ReasonReviewOnlySource {
			t.Fatalf("%s: EvaluateInBatch allowed=%v reason=%q", source, v2.Allowed, v2.Reason)
		}
		if !v.OwnerReviewOverridable() || !slices.Contains(v.RefusingReasons(), ReasonReviewOnlySource) {
			t.Fatalf("%s: an owner review must apply it by hand (overridable=%v reasons=%v)",
				source, v.OwnerReviewOverridable(), v.RefusingReasons())
		}

		if v.UnseenOwnerReviewOverridable() {
			t.Fatalf("%s: the hashless owner marker must not apply a review-only candidate nobody saw", source)
		}
		low := audible
		low.Score = 0.1
		if lv := Evaluate(book, snap(book), rt, &low, nil); lv.Allowed || !lv.UnseenOwnerReviewOverridable() {
			t.Fatalf("%s: the marker must still lift a certainty leg on a chain candidate (allowed=%v reason=%q)", source, lv.Allowed, lv.Reason)
		}

		stale := Evaluate(book, snap(book), rt, &c, errors.New("hash drift"))
		if stale.Reason != ReasonIdentityStale || stale.OwnerReviewOverridable() {
			t.Fatalf("%s with a stale identity: reason=%q overridable=%v, want identity_stale, not overridable",
				source, stale.Reason, stale.OwnerReviewOverridable())
		}
	}
}
