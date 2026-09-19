// file: internal/applygate/runtime_canonical_test.go
// version: 1.0.0
// guid: ab7beb30-e82f-4592-8e09-9527424f2aed
// last-edited: 2026-09-19

package applygate

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// chapters builds n present file rows of sec seconds each; the first `known`
// carry their duration, the rest are unprobed (Duration 0).
func chapters(n, sec, known int) []database.BookFile {
	out := make([]database.BookFile, n)
	for i := range out {
		out[i] = database.BookFile{ID: "f" + itoa(i), BookID: "b"}
		if i < known {
			out[i].Duration = sec
		}
	}
	return out
}

// TestCheckRuntime_MultiFileBookComparedAtFullRuntime is the owner's
// complaint as a test: 30 chapters × 20 min is a 10 h book, and a 10 h
// candidate must AGREE — not be measured against one or two chapters.
func TestCheckRuntime_MultiFileBookComparedAtFullRuntime(t *testing.T) {
	files := chapters(30, 1200, 30)
	// Book.Duration deliberately stale at one chapter's length.
	book := &database.Book{ID: "b", Duration: intp(1200)}
	rt := database.ComputeBookRuntime(book, files)
	if rt.Seconds != 36000 || !rt.Complete() {
		t.Fatalf("runtime = %+v, want 36000s complete", rt)
	}
	got := checkRuntime(rt, &metafetch.MetadataCandidate{DurationSec: 36000}, false)
	if got.Outcome != OutcomeAgree {
		t.Fatalf("10h book vs 10h candidate: %+v, want agree", got)
	}
}

// TestCheckRuntime_PartialDurationsAreNotAMismatch: the same book with only
// two chapters probed stores a 40-minute partial sum. That is missing
// evidence, never a runtime_mismatch veto.
func TestCheckRuntime_PartialDurationsAreNotAMismatch(t *testing.T) {
	files := chapters(30, 1200, 2)
	book := &database.Book{ID: "b", Duration: intp(2400)} // what RecomputeBookAggregates stores
	rt := database.ComputeBookRuntime(book, files)
	if rt.Complete() || !rt.Partial() {
		t.Fatalf("runtime = %+v, want partial", rt)
	}
	for _, overwriting := range []bool{false, true} {
		got := checkRuntime(rt, &metafetch.MetadataCandidate{DurationSec: 36000}, overwriting)
		if got.Reason == ReasonRuntimeMismatch {
			t.Fatalf("overwriting=%v: partial runtime produced a mismatch veto: %+v", overwriting, got)
		}
		if !overwriting && got.Outcome != OutcomeUnknown {
			t.Fatalf("fill-only: %+v, want unknown", got)
		}
	}
}

// TestCheckEvidence_PartialRuntimeDoesNotBlock runs the whole evidence leg:
// a partial-coverage book must not be refused for runtime_mismatch.
func TestCheckEvidence_PartialRuntimeDoesNotBlock(t *testing.T) {
	book := &database.Book{ID: "b", Title: "Shadow Rising", Duration: intp(2400)}
	rt := database.ComputeBookRuntime(book, chapters(30, 1200, 2))
	c := &metafetch.MetadataCandidate{Title: "Shadow Rising", DurationSec: 36000}
	v := CheckEvidence(book, rt, c, false)
	if v.Reason == ReasonRuntimeMismatch {
		t.Fatalf("partial runtime blocked the apply: %+v", v)
	}
}
