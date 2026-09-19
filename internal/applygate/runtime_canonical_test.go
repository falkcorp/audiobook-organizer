// file: internal/applygate/runtime_canonical_test.go
// version: 1.0.2
// guid: ab7beb30-e82f-4592-8e09-9527424f2aed
// last-edited: 2026-09-19

package applygate

import (
	"fmt"
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

// sweepShape is one row layout for the never-looser sweep: the book's file
// rows, and the Book.Duration main would have compared (main's
// RecomputeBookAggregates summed the known durations of EVERY row, missing
// ones included; for a book with no file durations it kept whatever an
// earlier writer stored).
type sweepShape struct {
	name     string
	files    []database.BookFile
	mainBook int
}

func sweepShapes() []sweepShape {
	var out []sweepShape
	for known := 1; known < 30; known += 3 {
		out = append(out, sweepShape{fmt.Sprintf("%d of 30 probed", known), chapters(30, 1200, known), known * 1200})
	}
	var discs, cbr, oneMissing []database.BookFile
	for d := 1; d <= 2; d++ {
		for i := 1; i <= 10; i++ {
			discs = append(discs, database.BookFile{ID: fmt.Sprint(d, i), FilePath: fmt.Sprintf("/lib/x/CD%d/%02d.mp3", d, i),
				OriginalFilename: fmt.Sprintf("%02d.mp3", i), FileSize: int64(d*1_000_000 + i), Duration: 1800, Missing: d == 2})
		}
	}
	for i := 0; i < 20; i++ {
		cbr = append(cbr, database.BookFile{ID: fmt.Sprint(i), FilePath: fmt.Sprintf("/lib/x/Part %02d.mp3", i),
			OriginalFilename: fmt.Sprintf("Part %02d.mp3", i), FileSize: 28_800_000, Duration: 1800, Missing: i >= 10})
		oneMissing = append(oneMissing, database.BookFile{ID: fmt.Sprint(i), FilePath: fmt.Sprintf("/lib/x/%02d.mp3", i),
			FileSize: int64(1_000_000 + i), Duration: 1800, Missing: i == 19})
	}
	out = append(out,
		sweepShape{"disc folders sharing base names, CD2 missing", discs, 36000},
		sweepShape{"equal-size CBR split, half missing", cbr, 36000},
		sweepShape{"all durations known, one chapter missing", oneMissing, 36000},
		sweepShape{"multi-file, no file durations, stored = one chapter", chapters(30, 1200, 0), 1200},
		sweepShape{"multi-file, no file durations, stored = whole book", chapters(30, 1200, 0), 36000},
	)
	return out
}

// TestCheckEvidence_IncompleteRuntimeNeverLooserThanMain is the owner rule as
// a sweep: across row shapes, candidate runtimes, narrator pairings and
// fill/overwrite, the canonical runtime never passes where main blocked —
// with ONE exemption, the fix itself: main's runtime_mismatch where the
// runtime is PARTIAL (some counted row's duration unknown) and its known sum
// is below the candidate. A lower bound under the candidate proves nothing.
func TestCheckEvidence_IncompleteRuntimeNeverLooserThanMain(t *testing.T) {
	violations := 0
	for _, sh := range sweepShapes() {
		for _, candMin := range []int{60, 300, 540, 570, 600, 630, 660, 700, 900, 1200} {
			for _, narr := range [][2]string{{"", ""}, {"Kate Reading", "Kate Reading"}, {"Kate Reading", "Michael Kramer"}} {
				for _, overwrite := range []bool{false, true} {
					book := probeBook(narr[0], sh.mainBook)
					if overwrite {
						book.Title = "Shadow Rising (Wheel of Time)" // candidate would drop a token
					}
					rt := database.ComputeBookRuntime(book, sh.files)
					c := &metafetch.MetadataCandidate{Title: "Shadow Rising", Author: "Robert Jordan", Narrator: narr[1], DurationSec: candMin * 60}
					mainV := CheckEvidence(book, database.BookRuntime{Seconds: sh.mainBook, Source: database.RuntimeSourceBook}, c, false)
					newV := CheckEvidence(book, rt, c, false)
					if mainV.Pass || !newV.Pass {
						continue
					}
					// Stated on the rows, not on rt.Partial(), so a change to
					// what "partial" means cannot widen the exemption.
					someUnknown := rt.Source == database.RuntimeSourceFiles && rt.FilesKnown < rt.FilesCounted
					if mainV.Reason == ReasonRuntimeMismatch && someUnknown && rt.Seconds < c.DurationSec {
						continue // the fix: a lower bound below the candidate
					}
					violations++
					t.Errorf("%s, cand=%dmin narr=%v overwrite=%v: main blocked (%s: %s), canonical (%s %ds) passes",
						sh.name, candMin, narr, overwrite, mainV.Reason, mainV.Detail, rt.Status(), rt.Seconds)
				}
			}
		}
	}
	if violations != 0 {
		t.Fatalf("%d looser-than-main violations", violations)
	}
}
