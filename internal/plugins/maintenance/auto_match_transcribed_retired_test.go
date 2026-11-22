// file: internal/plugins/maintenance/auto_match_transcribed_retired_test.go
// version: 1.0.0
// guid: 3c5e7a91-4b2d-4f68-8a1c-9d0e2b4f6a73
// last-edited: 2026-11-17

package maintenance

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/applycap"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// A retired book (trashed, or merged into another book) is not a candidate
// for the transcription auto-match: no apply will accept it, so it is not
// searched, not counted eligible, takes no bulk-apply cap slot, is never
// logged "apply failed", and the dry run does not report it "would-apply".

// retiredAutoMatchBooks is one live book plus one trashed and one merged book
// that pass every other gate (never reviewed, a transcribed title the fake
// search echoes at a score above the threshold).
func retiredAutoMatchBooks() []database.Book {
	books := capEligibleBooks(3)
	books[0].ID, books[0].Title, books[0].TranscribedTitle = "trashed", "Hush 1", new("Hush 1")
	books[0].MarkedForDeletion = new(true)
	books[1].ID, books[1].Title, books[1].TranscribedTitle = "merged", "Hush 2", new("Hush 2")
	books[1].MergedIntoBookID = new("live")
	books[2].ID, books[2].Title, books[2].TranscribedTitle = "live", "Hush 3", new("Hush 3")
	return books
}

func TestAutoMatchTranscribed_RetiredBooksAreNotSearchedOrApplied(t *testing.T) {
	// Cap 1: the live book must still be admitted, so a retired book that took
	// a cap slot (or counted as an apply) would abort the run.
	withAutoMatchBulkApplyCap(t, 1)
	var searched, applied []string
	deps := &autoMatchDeps{
		searchFn: func(_ context.Context, id, transTitle, transAuthor string) (string, string, float64, bool, error) {
			searched = append(searched, id)
			return transTitle, transAuthor, 1.8, true, nil
		},
		applyFn: func(_ context.Context, id, _, _ string) error {
			applied = append(applied, id)
			return nil
		},
	}
	p := newAutoMatchPlugin(retiredAutoMatchBooks(), deps)

	err := p.runAutoMatchTranscribed(context.Background(), autoMatchParams(false, 0), &fakeReporter{})
	if _, ok := errors.AsType[*applycap.ExceededError](err); ok {
		t.Fatalf("a retired book took a bulk-apply cap slot: %v", err)
	}
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if len(searched) != 1 || searched[0] != "live" {
		t.Fatalf("only the live book may be searched, got %v", searched)
	}
	if len(applied) != 1 || applied[0] != "live" {
		t.Fatalf("only the live book may be applied, got %v", applied)
	}
}

func TestAutoMatchTranscribed_DryRunDoesNotReportRetiredBooksWouldApply(t *testing.T) {
	deps := &autoMatchDeps{searchFn: capEchoSearch}
	p := newAutoMatchPlugin(retiredAutoMatchBooks()[:2], deps) // retired only

	if err := p.runAutoMatchTranscribed(context.Background(), autoMatchParams(true, 0), &fakeReporter{}); err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
	if deps.searchCalled != 0 {
		t.Fatalf("a retired book must not be searched in a dry run, got %d searches", deps.searchCalled)
	}
	if deps.applyCalled != 0 {
		t.Fatalf("dry run applies nothing, got %d", deps.applyCalled)
	}
}

// The summary the operator reads: retired books are named, and are not part of
// the eligible count.
func TestAutoMatchTranscribed_SummaryCountsRetiredSeparately(t *testing.T) {
	deps := &autoMatchDeps{searchFn: capEchoSearch}
	p := newAutoMatchPlugin(retiredAutoMatchBooks(), deps)
	rep := &progressReporter{}

	if err := p.runAutoMatchTranscribed(context.Background(), autoMatchParams(true, 0), rep); err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
	want := "scanned 3, eligible 1, would-apply 1, owner-manual skipped 0, runtime unverified 0, retired skipped 2"
	if !strings.Contains(rep.lastMsg, want) {
		t.Fatalf("summary missing %q; saw %q", want, rep.lastMsg)
	}
}
