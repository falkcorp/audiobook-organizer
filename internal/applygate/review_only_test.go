// file: internal/applygate/review_only_test.go
// version: 1.4.1
// guid: c09f46a0-0fa5-4191-b572-ea43cc5ace8b
// last-edited: 2026-10-20

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
	book := &database.Book{Title: "Synthetic Field Notes 1", Duration: new(36000)}
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
	book := &database.Book{Title: "Synthetic Field Notes 1", Duration: new(36000)}
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

// A catalog entry of the owner's manual-only libraries is held by the bulk
// guard on the stage's ManualOnly tag alone: catalog.IsManualOnly also reads
// the subtitle and every series membership, which the candidate's own fields
// (title, first series, publisher, author) can leave neutral. An owner review
// pin lifts review_only_source (review_bulk) but never this.
func TestCatalogCandidate_ManualOnlyTagIsHeldByTheBulkGuard(t *testing.T) {
	book := &database.Book{Title: "Synthetic Field Notes 1", Duration: new(36000)}
	rt := database.ComputeBookRuntime(book, nil)
	c := metafetch.MetadataCandidate{Source: metadata.CatalogSourceName, Title: "Synthetic Field Notes 1", Publisher: "Synthetic Press",
		Series: "Synthetic Series", SeriesPosition: "1", Score: 1.0, DurationSec: 36000, Stage: metadata.CatalogStageAuthor}
	bulk := ManualOnlyGuard{Bulk: true}

	untagged := EvaluateTranscribed(book, snap(book), rt, &c, nil, nil, nil, TranscribedSearch{}, bulk)
	if untagged.Reason == ReasonOwnerManualOnly {
		t.Fatalf("fixture: neutral fields already trip the guard: %+v", untagged)
	}
	c.ManualOnly = true
	v := EvaluateTranscribed(book, snap(book), rt, &c, nil, nil, nil, TranscribedSearch{}, bulk)
	if v.Allowed || v.Reason != ReasonOwnerManualOnly || v.OwnerReviewOverridable() {
		t.Fatalf("allowed=%v reason=%q overridable=%v; want refused owner_manual_only, which no review lifts", v.Allowed, v.Reason, v.OwnerReviewOverridable())
	}
	// A single-book (non-bulk) caller is the owner at the keyboard: not held.
	if one := EvaluateTranscribed(book, snap(book), rt, &c, nil, nil, nil, TranscribedSearch{}, ManualOnlyGuard{}); one.Reason == ReasonOwnerManualOnly {
		t.Fatalf("the single-book path was held: %+v", one)
	}
}
