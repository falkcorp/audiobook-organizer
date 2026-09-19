// file: internal/applygate/runtime_canonical_test.go
// version: 1.0.1
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

// probeBook is a book the other evidence checks accept for its candidate
// (same title, author in the path), so the runtime and narrator legs decide.
func probeBook(narrator string, dur int) *database.Book {
	b := &database.Book{ID: "b", Title: "Shadow Rising", FilePath: "/lib/Robert Jordan/Shadow Rising/01.mp3", Duration: intp(dur)}
	if narrator != "" {
		b.Narrator = &narrator
	}
	return b
}

// TestCheckEvidence_PartialLowerBoundAboveCandidateStillBlocks (review ERROR 1):
// 30 × 30 min chapters with 20 probed is AT LEAST 10 h. A 5 h candidate is a
// contradiction whatever the other 10 chapters hold, and main blocked it.
func TestCheckEvidence_PartialLowerBoundAboveCandidateStillBlocks(t *testing.T) {
	book := probeBook("", 36000)
	rt := database.ComputeBookRuntime(book, chapters(30, 1800, 20))
	c := &metafetch.MetadataCandidate{Title: "Shadow Rising", Author: "Robert Jordan", DurationSec: 18000}
	v := CheckEvidence(book, rt, c, false)
	if v.Pass || v.Reason != ReasonRuntimeMismatch {
		t.Fatalf("10h+ book vs 5h candidate: pass=%v reason=%s, want runtime_mismatch block", v.Pass, v.Reason)
	}
}

// TestCheckEvidence_PartialRuntimeKeepsNarratorVeto (review ERROR 2): 28 of
// 30 × 20 min chapters probed (560 min), candidate 600 min read by someone
// else. Main compared the 560 min sum, landed in the neutral band and let
// the narrator contradiction block; an incomplete runtime confirms nothing,
// so the veto must stay armed.
func TestCheckEvidence_PartialRuntimeKeepsNarratorVeto(t *testing.T) {
	book := probeBook("Kate Reading", 33600)
	rt := database.ComputeBookRuntime(book, chapters(30, 1200, 28))
	c := &metafetch.MetadataCandidate{Title: "Shadow Rising", Author: "Robert Jordan", Narrator: "Michael Kramer", DurationSec: 36000}
	v := CheckEvidence(book, rt, c, false)
	if v.Pass || v.Reason != ReasonNarratorMismatch {
		t.Fatalf("partial runtime, different narrator: pass=%v reason=%s, want narrator_mismatch block", v.Pass, v.Reason)
	}
}

// TestCheckEvidence_CompleteRuntimeContradictionsStillBlock: the fix must not
// soften a real contradiction on a fully measured multi-file book.
func TestCheckEvidence_CompleteRuntimeContradictionsStillBlock(t *testing.T) {
	book := probeBook("Kate Reading", 1200)
	rt := database.ComputeBookRuntime(book, chapters(30, 1200, 30)) // 10 h
	short := &metafetch.MetadataCandidate{Title: "Shadow Rising", Author: "Robert Jordan", DurationSec: 18000}
	if v := CheckEvidence(book, rt, short, false); v.Pass || v.Reason != ReasonRuntimeMismatch {
		t.Fatalf("10h vs 5h: pass=%v reason=%s, want runtime_mismatch", v.Pass, v.Reason)
	}
	other := &metafetch.MetadataCandidate{Title: "Shadow Rising", Author: "Robert Jordan", Narrator: "Michael Kramer", DurationSec: 38500}
	if v := CheckEvidence(book, rt, other, false); v.Pass || v.Reason != ReasonNarratorMismatch {
		t.Fatalf("10h vs 10.7h other narrator: pass=%v reason=%s, want narrator_mismatch", v.Pass, v.Reason)
	}
}

// TestCheckEvidence_IncompleteRuntimeNeverLooserThanMain is the round's
// invariant as a sweep. "Main" is the same book judged on its stored partial
// sum as if it were the total (what main's checkRuntime read). For every
// partial coverage, candidate runtime, narrator pairing and fill/overwrite
// mode, the canonical runtime may only unblock the one case the fix exists
// for: main's runtime_mismatch where the known sum is BELOW the candidate
// (a lower bound under the candidate proves nothing) and nothing else objects.
func TestCheckEvidence_IncompleteRuntimeNeverLooserThanMain(t *testing.T) {
	for known := 1; known < 30; known += 3 {
		for _, candMin := range []int{60, 300, 540, 570, 600, 630, 660, 700, 900} {
			for _, narr := range [][2]string{{"", ""}, {"Kate Reading", "Kate Reading"}, {"Kate Reading", "Michael Kramer"}} {
				for _, overwrite := range []bool{false, true} {
					lb := known * 1200
					book := probeBook(narr[0], lb)
					if overwrite {
						book.Title = "Shadow Rising (Wheel of Time)" // candidate would drop a token
					}
					rt := database.ComputeBookRuntime(book, chapters(30, 1200, known))
					c := &metafetch.MetadataCandidate{Title: "Shadow Rising", Author: "Robert Jordan", Narrator: narr[1], DurationSec: candMin * 60}
					mainV := CheckEvidence(book, database.BookRuntime{Seconds: lb, Source: database.RuntimeSourceBook}, c, false)
					newV := CheckEvidence(book, rt, c, false)
					if mainV.Pass || !newV.Pass {
						continue
					}
					intended := mainV.Reason == ReasonRuntimeMismatch && lb < c.DurationSec
					if !intended {
						t.Errorf("known=%d cand=%dmin narr=%v overwrite=%v: main blocked (%s: %s), canonical passes",
							known, candMin, narr, overwrite, mainV.Reason, mainV.Detail)
					}
				}
			}
		}
	}
}
