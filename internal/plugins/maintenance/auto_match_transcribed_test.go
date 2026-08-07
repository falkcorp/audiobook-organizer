// file: internal/plugins/maintenance/auto_match_transcribed_test.go
// version: 1.0.0
// guid: 3f7e9b2a-5c8d-4e1f-a0b3-6d9c2e5f8a1b
// last-edited: 2026-06-29

package maintenance

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// autoMatchDeps wraps fakeDeps and overrides SearchTranscriptionCandidate and
// ApplyTranscriptionCandidate so tests can inject results and count calls.
type autoMatchDeps struct {
	fakeDeps
	searchFn func(ctx context.Context, bookID, transTitle, transAuthor string) (string, string, float64, bool, error)
	applyFn  func(ctx context.Context, bookID, candTitle, candAuthor string) error
	// call counters
	searchCalled int
	applyCalled  int
}

func (d *autoMatchDeps) SearchTranscriptionCandidate(ctx context.Context, bookID, transTitle, transAuthor string) (string, string, float64, bool, error) {
	d.searchCalled++
	if d.searchFn != nil {
		return d.searchFn(ctx, bookID, transTitle, transAuthor)
	}
	return "", "", 0, false, nil
}

func (d *autoMatchDeps) ApplyTranscriptionCandidate(ctx context.Context, bookID, candTitle, candAuthor string) error {
	d.applyCalled++
	if d.applyFn != nil {
		return d.applyFn(ctx, bookID, candTitle, candAuthor)
	}
	return nil
}

func (d *autoMatchDeps) HasMetadataFetchService() bool { return true }

// ptr helpers

func boolPtr(b bool) *bool    { return &b }
func strPtr(s string) *string { return &s }

// newAutoMatchPlugin builds a Plugin + MockStore wired to the given books.
func newAutoMatchPlugin(books []database.Book, deps *autoMatchDeps) *Plugin {
	store := &database.MockStore{
		ListBookIDsFunc: func() ([]string, error) {
			ids := make([]string, len(books))
			for i, b := range books {
				ids[i] = b.ID
			}
			return ids, nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			for i := range books {
				if books[i].ID == id {
					return &books[i], nil
				}
			}
			return nil, nil
		},
	}
	deps.fakeDeps = fakeDeps{store: store}
	return New(deps)
}

func autoMatchParams(dryRun bool, minScore float64) json.RawMessage {
	b, _ := json.Marshal(autoMatchTranscribedParams{
		DryRun:   boolPtr(dryRun),
		MinScore: minScore,
	})
	return b
}

// TestAutoMatchTranscribed_Apply verifies that a book with a high-score
// transcription match is applied when dry_run=false, and that
// ApplyTranscriptionCandidate is called exactly once.
func TestAutoMatchTranscribed_Apply(t *testing.T) {
	trans := "The Name of the Wind"
	author := "Patrick Rothfuss"
	books := []database.Book{
		{
			ID:                "b1",
			Title:             "Name Wind",
			TranscribedTitle:  strPtr(trans),
			TranscribedAuthor: strPtr(author),
			// MetadataReviewStatus == nil → eligible
		},
	}

	deps := &autoMatchDeps{
		searchFn: func(_ context.Context, _, _, _ string) (string, string, float64, bool, error) {
			// Return exact title match with score above default 0.75 threshold.
			return trans, author, 1.8, true, nil
		},
	}
	p := newAutoMatchPlugin(books, deps)

	if err := p.runAutoMatchTranscribed(context.Background(), autoMatchParams(false, 0), &fakeReporter{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deps.searchCalled != 1 {
		t.Errorf("expected 1 search call, got %d", deps.searchCalled)
	}
	if deps.applyCalled != 1 {
		t.Errorf("expected 1 apply call (dry_run=false), got %d", deps.applyCalled)
	}
}

// TestAutoMatchTranscribed_DryRunNoApply verifies that a matching book is NOT
// applied when dry_run=true — ApplyTranscriptionCandidate must be called 0 times.
func TestAutoMatchTranscribed_DryRunNoApply(t *testing.T) {
	trans := "The Name of the Wind"
	author := "Patrick Rothfuss"
	books := []database.Book{
		{
			ID:                "b1",
			Title:             "Name Wind",
			TranscribedTitle:  strPtr(trans),
			TranscribedAuthor: strPtr(author),
		},
	}

	deps := &autoMatchDeps{
		searchFn: func(_ context.Context, _, _, _ string) (string, string, float64, bool, error) {
			return trans, author, 1.8, true, nil
		},
	}
	p := newAutoMatchPlugin(books, deps)

	if err := p.runAutoMatchTranscribed(context.Background(), autoMatchParams(true, 0), &fakeReporter{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deps.applyCalled != 0 {
		t.Errorf("dry-run must not call Apply, got %d apply calls", deps.applyCalled)
	}
	// Search was still called — we evaluate the candidate even in dry-run.
	if deps.searchCalled != 1 {
		t.Errorf("expected 1 search call in dry-run, got %d", deps.searchCalled)
	}
}

// TestAutoMatchTranscribed_NoTranscriptionSkipped verifies that books without a
// TranscribedTitle are skipped entirely — neither search nor apply is called.
func TestAutoMatchTranscribed_NoTranscriptionSkipped(t *testing.T) {
	books := []database.Book{
		{
			ID:    "b1",
			Title: "Some Untranscribed Book",
			// TranscribedTitle == nil → no audio signal → skip
		},
	}

	deps := &autoMatchDeps{}
	p := newAutoMatchPlugin(books, deps)

	if err := p.runAutoMatchTranscribed(context.Background(), autoMatchParams(false, 0), &fakeReporter{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deps.searchCalled != 0 {
		t.Errorf("books without TranscribedTitle must be skipped: got %d search calls", deps.searchCalled)
	}
	if deps.applyCalled != 0 {
		t.Errorf("books without TranscribedTitle must be skipped: got %d apply calls", deps.applyCalled)
	}
}

// TestAutoMatchTranscribed_AlreadyReviewedSkipped verifies that books with a
// non-nil MetadataReviewStatus are skipped — no search, no apply.
func TestAutoMatchTranscribed_AlreadyReviewedSkipped(t *testing.T) {
	status := "matched"
	trans := "Already Matched Book"
	books := []database.Book{
		{
			ID:                   "b1",
			Title:                "Already Matched Book",
			TranscribedTitle:     strPtr(trans),
			MetadataReviewStatus: &status, // already reviewed → must be skipped
		},
	}

	deps := &autoMatchDeps{}
	p := newAutoMatchPlugin(books, deps)

	if err := p.runAutoMatchTranscribed(context.Background(), autoMatchParams(false, 0), &fakeReporter{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deps.searchCalled != 0 {
		t.Errorf("already-reviewed books must be skipped: got %d search calls", deps.searchCalled)
	}
	if deps.applyCalled != 0 {
		t.Errorf("already-reviewed books must be skipped: got %d apply calls", deps.applyCalled)
	}
}
