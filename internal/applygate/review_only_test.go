// file: internal/applygate/review_only_test.go
// version: 1.3.0
// guid: c09f46a0-0fa5-4191-b572-ea43cc5ace8b
// last-edited: 2026-10-12

package applygate

import (
	"errors"
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
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

// Owner decision D31: a candidate from the local author catalog
// (metadata.CatalogSourceName, the B1/B2 blocking stage) is review-only. A
// perfect-score one is refused by the gate in every bulk path, and only an
// owner review of the candidate that was shown applies it.
func TestCatalogCandidate_IsReviewOnly(t *testing.T) {
	book := &database.Book{Title: "Synthetic Field Notes 1", Duration: intp(36000)}
	rt := database.ComputeBookRuntime(book, nil)
	c := metafetch.MetadataCandidate{Source: metadata.CatalogSourceName, Title: "Synthetic Field Notes 1",
		SeriesPosition: "1", Score: 1.0, DurationSec: 36000, Stage: metadata.CatalogStageAuthor}
	if !metafetch.IsReviewOnlyCandidateSource(c.Source) || !ReviewOnlySource(&c) {
		t.Fatal("the catalog source is not review-only")
	}
	audible := c
	audible.Source = "Audible"
	if v := Evaluate(book, snap(book), rt, &audible, nil); !v.Allowed {
		t.Fatalf("fixture: the same perfect candidate from Audible is refused: %+v", v)
	}

	v := Evaluate(book, snap(book), rt, &c, nil)
	if v.Allowed || v.Reason != "review_only_source" {
		t.Fatalf("catalog candidate: allowed=%v reason=%q, want refused review_only_source", v.Allowed, v.Reason)
	}
	if v2 := EvaluateInBatch(book, snap(book), rt, &c, nil, nil, nil); v2.Allowed || v2.Reason != ReasonReviewOnlySource {
		t.Fatalf("EvaluateInBatch: allowed=%v reason=%q", v2.Allowed, v2.Reason)
	}
	if !v.OwnerReviewOverridable() || v.UnseenOwnerReviewOverridable() {
		t.Fatalf("an owner review applies it by hand, the hashless marker does not (overridable=%v unseen=%v)",
			v.OwnerReviewOverridable(), v.UnseenOwnerReviewOverridable())
	}
}

// A catalog entry of the owner's manual-only libraries (here by publisher) is
// held by the bulk guard as any manual-only record is, on the candidate's own
// fields; the stage's ManualOnly tag is informational.
func TestCatalogCandidate_ManualOnlyEntryIsHeldByTheBulkGuard(t *testing.T) {
	book := &database.Book{Title: "Synthetic Field Notes 1", Duration: intp(36000)}
	rt := database.ComputeBookRuntime(book, nil)
	c := metafetch.MetadataCandidate{Source: metadata.CatalogSourceName, Title: "Synthetic Field Notes 1", Publisher: "Big Finish Productions",
		SeriesPosition: "1", Score: 1.0, DurationSec: 36000, Stage: metadata.CatalogStageAuthor, ManualOnly: true}
	v := EvaluateTranscribed(book, snap(book), rt, &c, nil, nil, nil, TranscribedSearch{}, ManualOnlyGuard{Bulk: true})
	if v.Allowed || v.Reason != ReasonOwnerManualOnly || v.OwnerReviewOverridable() {
		t.Fatalf("allowed=%v reason=%q overridable=%v; want refused owner_manual_only, which no review lifts", v.Allowed, v.Reason, v.OwnerReviewOverridable())
	}
}
