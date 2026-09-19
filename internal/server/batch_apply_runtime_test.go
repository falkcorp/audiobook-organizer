// file: internal/server/batch_apply_runtime_test.go
// version: 1.0.1
// guid: 6742e59d-bc2b-4c69-997e-ccbfa8ba1b0c
// last-edited: 2026-09-19

package server

import (
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// chapterFiles is n present rows of sec seconds; only the first known carry
// a duration.
func chapterFiles(bookID string, n, sec, known int) []database.BookFile {
	out := make([]database.BookFile, n)
	for i := range out {
		out[i] = database.BookFile{ID: fmt.Sprintf("%s-%02d", bookID, i), BookID: bookID, FilePath: fmt.Sprintf("/lib/Robert Jordan/Shadow Rising/%02d.mp3", i)}
		if i < known {
			out[i].Duration = sec
		}
	}
	return out
}

func runtimeCheck(t *testing.T, p cachedApplyPlan) applygate.CheckResult {
	t.Helper()
	if p.Gate == nil {
		t.Fatalf("no gate verdict: reason=%q err=%v", p.Reason, p.Err)
	}
	for _, c := range p.Gate.Evidence.Checks {
		if c.Name == "runtime" {
			return c
		}
	}
	t.Fatalf("no runtime check in %+v", p.Gate.Evidence.Checks)
	return applygate.CheckResult{}
}

// TestPlanCachedApply_GateReadsTheBooksFiles drives the real batch-apply
// planner with multi-file rows: the gate's runtime is the sum over the book's
// files (gateRuntime), not Book.Duration.
func TestPlanCachedApply_GateReadsTheBooksFiles(t *testing.T) {
	stale := 1200 // Book.Duration left at one chapter
	book := &database.Book{ID: "b1", Title: "Shadow Rising", Duration: &stale,
		FilePath: "/lib/Robert Jordan/Shadow Rising", Author: &database.Author{Name: "Robert Jordan"}}

	t.Run("30 measured chapters agree with a 10h candidate", func(t *testing.T) {
		cand := metafetch.MetadataCandidate{Title: "Shadow Rising", Author: "Robert Jordan", DurationSec: 36000, Score: 0.99}
		books := filesBooks{fakeBooks: fakeBooks{"b1": book}, files: map[string][]database.BookFile{"b1": chapterFiles("b1", 30, 1200, 30)}}
		p := planCachedApply(&fakeApplySvc{candidates: candidateJSON(t, cand)}, books, "b1", nil, nil)
		if rc := runtimeCheck(t, p); rc.Outcome != applygate.OutcomeAgree {
			t.Fatalf("runtime check = %+v, want agree (10h vs 10h)", rc)
		}
	})

	t.Run("partial runtime keeps the narrator veto", func(t *testing.T) {
		n := "Kate Reading"
		b := *book
		b.Narrator = &n
		// Book.Duration is the partial known sum RecomputeBookAggregates
		// stores (28 × 20 min), so only the narrator can decide.
		knownSum := 28 * 1200
		b.Duration = &knownSum
		cand := metafetch.MetadataCandidate{Title: "Shadow Rising", Author: "Robert Jordan", Narrator: "Michael Kramer", DurationSec: 36000, Score: 0.99}
		books := filesBooks{fakeBooks: fakeBooks{"b1": &b}, files: map[string][]database.BookFile{"b1": chapterFiles("b1", 30, 1200, 28)}}
		p := planCachedApply(&fakeApplySvc{candidates: candidateJSON(t, cand)}, books, "b1", nil, nil)
		if p.Gate == nil || p.Gate.Allowed || p.Gate.Evidence.Reason != applygate.ReasonNarratorMismatch {
			t.Fatalf("gate = %+v, want narrator_mismatch block", p.Gate)
		}
	})
}
