// file: internal/server/batch_apply_log_test.go
// version: 1.0.0
// guid: 52dae0d7-3e3f-4a00-ae07-42d80b9fe194
// last-edited: 2026-09-28

package server

import (
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// An applied book's line names the book and the candidate it took, and the
// outcome carries both from the plan for every path through the apply.
func TestBatchApplyOutcome_AppliedLineNamesBookAndCandidate(t *testing.T) {
	svc := &fakeApplySvc{candidates: oneCandidate(t), skippedLocked: []string{"title"}}
	out := applyCachedCandidateForBook(svc, fakeBooks{}, "b1", false, nil)
	if !out.Applied {
		t.Fatalf("outcome = %+v, want applied", out)
	}
	if out.BookTitle != "A Title" || out.Candidate == nil || out.Candidate.Title != "A Title" {
		t.Fatalf("outcome lost the plan's book/candidate: title %q candidate %+v", out.BookTitle, out.Candidate)
	}
	got := batchApplyAppliedLine("b1", out)
	want := `applied: "A Title" → "A Title" by "An Author" (score 0.95); user-locked fields left unchanged: title`
	if got != want {
		t.Errorf("applied line = %q, want %q", got, want)
	}
}

// A refused book's line carries the reason, the gate's refusal and the error.
func TestBatchApplyOutcome_RefusedLineCarriesReasonGateAndError(t *testing.T) {
	blocked := errors.New("two files plan the same target\nforged line")
	svc := &fakeApplySvc{candidates: oneCandidate(t), preflightErr: blocked}
	out := applyCachedCandidateForBook(svc, fakeBooks{}, "b1", true, nil)
	if out.Applied || out.BookTitle != "A Title" {
		t.Fatalf("outcome = %+v, want refused with the book stamped", out)
	}
	got := batchApplyRefusedLine("b1", out)
	if !strings.HasPrefix(got, `not applied: "A Title" → "A Title" by "An Author"`) ||
		!strings.Contains(got, applySkipFileWorkWouldFail) || !strings.Contains(got, "two files plan the same target") {
		t.Errorf("refused line = %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("refused line carries a raw newline: %q", got)
	}

	gated := applyOutcome{
		Reason:    applySkipGateBlocked,
		BookTitle: "Big Cats 1",
		Gate:      &applygate.Verdict{Reason: "sequence_mismatch", Detail: "book 1, candidate 3"},
		Candidate: &metafetch.MetadataCandidate{Title: "Big Cats 3", Source: "Audible", Score: 0.97},
	}
	want := `not applied: "Big Cats 1" → "Big Cats 3" (Audible, score 0.97) — ` + applySkipGateBlocked +
		`; gate: sequence_mismatch (book 1, candidate 3)`
	if got := batchApplyRefusedLine("b2", gated); got != want {
		t.Errorf("gated line = %q, want %q", got, want)
	}

	// A book the plan never read is named by its id.
	if got := batchApplyRefusedLine("b3", applyOutcome{Reason: applySkipBookNotFound}); !strings.HasPrefix(got, `not applied: book "b3" — `) {
		t.Errorf("unknown-book line = %q", got)
	}
}

// The progress label is rebuilt only every batchApplyLabelEvery books.
func TestBatchApplyLabel_Cadence(t *testing.T) {
	builds := 0
	l := newBatchApplyLabel(func() string { builds++; return "x" })
	for n := int64(0); n < 3*batchApplyLabelEvery; n++ {
		l.get(n)
	}
	if builds != 3 {
		t.Errorf("label rebuilt %d times over %d books, want 3", builds, 3*batchApplyLabelEvery)
	}
}
