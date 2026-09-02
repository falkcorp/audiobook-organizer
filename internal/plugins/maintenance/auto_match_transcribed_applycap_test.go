// file: internal/plugins/maintenance/auto_match_transcribed_applycap_test.go
// version: 1.0.0
// guid: 6a8e2f95-1d4c-4b7a-9e3f-0c5b7d2a8e41
// last-edited: 2026-09-02

package maintenance

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/applycap"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The bulk apply cap (internal/applycap) on auto-match-transcribed. The op
// walks the whole library and decides per book, so the cap is a running
// counter: exactly cap applies are admitted, the (cap+1)th aborts the op.

func withAutoMatchBulkApplyCap(t *testing.T, n int) {
	t.Helper()
	prev := config.AppConfig.BulkApplyMaxItems
	config.AppConfig.BulkApplyMaxItems = n
	t.Cleanup(func() { config.AppConfig.BulkApplyMaxItems = prev })
}

// capEligibleBooks builds n books that pass every gate: never reviewed, with a
// transcribed title the fake search echoes back at a score above threshold.
func capEligibleBooks(n int) []database.Book {
	books := make([]database.Book, 0, n)
	for i := range n {
		books = append(books, database.Book{
			ID:                "b" + strconv.Itoa(i),
			Title:             "Book " + strconv.Itoa(i),
			TranscribedTitle:  new("Book " + strconv.Itoa(i)),
			TranscribedAuthor: new("Some Author"),
		})
	}
	return books
}

func capEchoSearch(_ context.Context, _, transTitle, transAuthor string) (string, string, float64, bool, error) {
	return transTitle, transAuthor, 1.8, true, nil
}

func TestAutoMatchTranscribed_StopsAtTheBulkApplyCap(t *testing.T) {
	withAutoMatchBulkApplyCap(t, 3)
	deps := &autoMatchDeps{searchFn: capEchoSearch}
	p := newAutoMatchPlugin(capEligibleBooks(5), deps)

	err := p.runAutoMatchTranscribed(context.Background(), autoMatchParams(false, 0), &fakeReporter{})
	if err == nil {
		t.Fatal("5 eligible books against a cap of 3 must abort the op")
	}
	var ex *applycap.ExceededError
	if !errors.As(err, &ex) {
		t.Fatalf("want *applycap.ExceededError, got %T: %v", err, err)
	}
	if ex.Cap != 3 {
		t.Fatalf("want cap 3 in the error, got %+v", ex)
	}
	if deps.applyCalled != 3 {
		t.Fatalf("exactly the cap must be applied before the abort, got %d applies", deps.applyCalled)
	}
}

func TestAutoMatchTranscribed_ExactlyTheCapCompletes(t *testing.T) {
	withAutoMatchBulkApplyCap(t, 3)
	deps := &autoMatchDeps{searchFn: capEchoSearch}
	p := newAutoMatchPlugin(capEligibleBooks(3), deps)

	if err := p.runAutoMatchTranscribed(context.Background(), autoMatchParams(false, 0), &fakeReporter{}); err != nil {
		t.Fatalf("exactly the cap must complete, got %v", err)
	}
	if deps.applyCalled != 3 {
		t.Fatalf("want 3 applies, got %d", deps.applyCalled)
	}
}

// Dry runs mutate nothing, so the cap must not stop them: a dry run over more
// than cap eligible books is exactly how an operator sizes the real run.
func TestAutoMatchTranscribed_DryRunIsNotCapped(t *testing.T) {
	withAutoMatchBulkApplyCap(t, 3)
	deps := &autoMatchDeps{searchFn: capEchoSearch}
	p := newAutoMatchPlugin(capEligibleBooks(5), deps)

	if err := p.runAutoMatchTranscribed(context.Background(), autoMatchParams(true, 0), &fakeReporter{}); err != nil {
		t.Fatalf("dry run must not be capped, got %v", err)
	}
	if deps.applyCalled != 0 {
		t.Fatalf("dry run must apply nothing, got %d", deps.applyCalled)
	}
	if deps.searchCalled != 5 {
		t.Fatalf("dry run must visit every book, got %d searches", deps.searchCalled)
	}
}
