// file: internal/applygate/owner_review_test.go
// version: 1.1.0
// guid: 6d2f9b41-0e73-4c58-a9b6-3f1e7c8d2a05
// last-edited: 2026-09-13

package applygate

import (
	"reflect"
	"testing"
)

func TestOwnerReviewOverridable(t *testing.T) {
	cases := []struct {
		name string
		v    Verdict
		want bool
	}{
		{"allowed: nothing to override", Verdict{Allowed: true}, false},
		{"transcription mismatch", Verdict{Reason: ReasonTranscriptionMismatch, ScoreReason: ReasonTranscriptionMismatch}, true},
		{"score below floor", Verdict{Reason: ReasonScoreBelowFloor, ScoreReason: ReasonScoreBelowFloor}, true},
		{"sequence missing", Verdict{Reason: ReasonSequenceMissingOnCandidate}, true},
		{"runtime unknown", Verdict{Reason: ReasonRuntimeUnknownOverwrite,
			Evidence: EvidenceVerdict{Checks: []CheckResult{{Name: "runtime", Outcome: OutcomeBlock, Reason: ReasonRuntimeUnknownOverwrite}}}}, true},
		{"identity stale", Verdict{Reason: ReasonIdentityStale}, false},
		{"partial book behind another leg", Verdict{Reason: ReasonSequenceMismatch,
			Evidence: EvidenceVerdict{Checks: []CheckResult{{Name: "partial_book", Outcome: OutcomeBlock, Reason: ReasonPartialBook}}}}, false},
		{"asin conflict stays hard", Verdict{Reason: ReasonASINConflict,
			Evidence: EvidenceVerdict{Checks: []CheckResult{{Name: "asin", Outcome: OutcomeBlock, Reason: ReasonASINConflict}}}}, false},
		{"asin conflict behind a runtime refusal", Verdict{Reason: ReasonRuntimeUnknownOverwrite,
			Evidence: EvidenceVerdict{Checks: []CheckResult{
				{Name: "runtime", Outcome: OutcomeBlock, Reason: ReasonRuntimeUnknownOverwrite},
				{Name: "asin", Outcome: OutcomeBlock, Reason: ReasonASINConflict}}}}, false},
	}
	for _, tc := range cases {
		if got := tc.v.OwnerReviewOverridable(); got != tc.want {
			t.Errorf("%s: OwnerReviewOverridable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRefusingReasonsNamesEveryLeg(t *testing.T) {
	v := Verdict{
		Reason:      ReasonTranscriptionMismatch,
		ScoreReason: ReasonTranscriptionMismatch,
		Sequence:    SequenceVerdict{Reason: ReasonSequenceMissingOnCandidate},
		Evidence: EvidenceVerdict{Reason: ReasonRuntimeUnknownOverwrite, Checks: []CheckResult{
			{Name: "runtime", Outcome: OutcomeBlock, Reason: ReasonRuntimeUnknownOverwrite},
			{Name: "title", Outcome: OutcomeAgree},
		}},
	}
	want := []string{ReasonTranscriptionMismatch, ReasonSequenceMissingOnCandidate, ReasonRuntimeUnknownOverwrite}
	if got := v.RefusingReasons(); !reflect.DeepEqual(got, want) {
		t.Fatalf("RefusingReasons = %v, want %v", got, want)
	}
	thin := Verdict{Reason: ReasonInsufficientEvidence, Sequence: SequenceVerdict{Pass: true}, Evidence: EvidenceVerdict{Reason: ReasonInsufficientEvidence}}
	if got := thin.RefusingReasons(); !reflect.DeepEqual(got, []string{ReasonInsufficientEvidence}) {
		t.Fatalf("insufficient evidence: %v", got)
	}
}
