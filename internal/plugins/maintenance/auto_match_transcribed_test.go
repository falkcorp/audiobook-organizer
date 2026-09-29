// file: internal/plugins/maintenance/auto_match_transcribed_test.go
// version: 1.2.0
// guid: 3f7e9b2a-5c8d-4e1f-a0b3-6d9c2e5f8a1b
// last-edited: 2026-09-28

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Owner rule (standing): Doctor Who / Big Finish / Torchwood are applied by
// hand, one book at a time, and this op is a bulk apply. A blank-titled Big
// Finish book whose transcription matches its top candidate exactly -- the
// book the op would otherwise fill -- is never applied, whether the marker is
// in its path, its transcribed title, the candidate, or a book_file path, and
// a failed book_file read refuses too (fail closed). None counts as eligible,
// so a dry run reports what a real run would do.
func TestAutoMatchTranscribed_OwnerManualOnlyNeverApplied(t *testing.T) {
	const unknown = "/library/Unknown Author/Unknown Title/book.m4b"
	cases := []struct {
		name       string
		path       string
		trans      string
		candAuthor string
		files      func(string) ([]database.BookFile, error)
	}{
		{name: "path names Big Finish", path: "/library/Big Finish/Unknown Title/book.m4b",
			trans: "The Chimes of Midnight", candAuthor: "Robert Shearman"},
		{name: "transcription names Doctor Who", path: unknown,
			trans: "Doctor Who: The Chimes of Midnight", candAuthor: "Robert Shearman"},
		{name: "a book_file path names Big Finish", path: unknown,
			trans: "The Chimes of Midnight", candAuthor: "Robert Shearman",
			files: func(string) ([]database.BookFile, error) {
				return []database.BookFile{{FilePath: "/library/Big Finish/Chimes/01.mp3"}}, nil
			}},
		{name: "the book_file read fails", path: unknown,
			trans: "The Chimes of Midnight", candAuthor: "Robert Shearman",
			files: func(string) ([]database.BookFile, error) { return nil, errors.New("pebble: closed") }},
	}
	for _, tc := range cases {
		for _, dryRun := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/dry_run=%v", tc.name, dryRun), func(t *testing.T) {
				books := []database.Book{{ID: "b1", Title: "", FilePath: tc.path, TranscribedTitle: new(tc.trans)}}
				deps := &autoMatchDeps{
					searchFn: func(_ context.Context, _, _, _ string) (string, string, float64, bool, error) {
						return tc.trans, tc.candAuthor, 1.8, true, nil
					},
				}
				p := newAutoMatchPlugin(books, deps)
				if tc.files != nil {
					deps.store.(*database.MockStore).GetBookFilesFunc = tc.files
				}
				rep := &progressReporter{}
				if err := p.runAutoMatchTranscribed(context.Background(), autoMatchParams(dryRun, 0), rep); err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if deps.applyCalled != 0 {
					t.Fatalf("apply called %d times for an owner-manual book, want 0", deps.applyCalled)
				}
				if s := rep.lastMsg; !strings.Contains(s, "eligible 0") || !strings.Contains(s, "owner-manual skipped 1") {
					t.Errorf("summary %q does not report the skip", s)
				}
			})
		}
	}
	// Control: the same blank-titled book with nothing marking it applies.
	books := []database.Book{{ID: "b1", Title: "", FilePath: unknown, TranscribedTitle: new("The Chimes of Midnight")}}
	deps := &autoMatchDeps{
		searchFn: func(_ context.Context, _, _, _ string) (string, string, float64, bool, error) {
			return "The Chimes of Midnight", "Robert Shearman", 1.8, true, nil
		},
	}
	p := newAutoMatchPlugin(books, deps)
	if err := p.runAutoMatchTranscribed(context.Background(), autoMatchParams(false, 0), &progressReporter{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deps.applyCalled != 1 {
		t.Fatalf("control: apply called %d times, want 1", deps.applyCalled)
	}
}

// The op's pre-check cannot read the series row; the server's apply can, and
// refuses with ErrTranscriptionOwnerManualOnly. That is a skip, not a
// failure, and the book is counted neither applied nor eligible.
func TestAutoMatchTranscribed_ServerManualOnlyRefusalIsASkip(t *testing.T) {
	trans := "The Chimes of Midnight"
	books := []database.Book{{ID: "b1", Title: "", FilePath: "/library/Unknown Author/Unknown Title/book.m4b",
		TranscribedTitle: new(trans)}}
	deps := &autoMatchDeps{
		searchFn: func(_ context.Context, _, _, _ string) (string, string, float64, bool, error) {
			return trans, "Robert Shearman", 1.8, true, nil
		},
		applyFn: func(_ context.Context, id, _, _ string) error {
			return fmt.Errorf("book %s: %w: owner_manual_only: series \"Big Finish Main Range\"", id, ErrTranscriptionOwnerManualOnly)
		},
	}
	p := newAutoMatchPlugin(books, deps)
	rep := &progressReporter{}
	if err := p.runAutoMatchTranscribed(context.Background(), autoMatchParams(false, 0), rep); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deps.applyCalled != 1 {
		t.Fatalf("apply called %d times, want 1", deps.applyCalled)
	}
	if s := rep.lastMsg; !strings.Contains(s, "eligible 0, applied 0, owner-manual skipped 1") {
		t.Errorf("summary %q does not report the skip", s)
	}
}

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
		DryRun:   new(dryRun),
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
			TranscribedTitle:  new(trans),
			TranscribedAuthor: new(author),
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
			TranscribedTitle:  new(trans),
			TranscribedAuthor: new(author),
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
			TranscribedTitle:     new(trans),
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

// Owner ruling 2026-09-14: the op is fill-only for title and author. A book
// whose title and author are both filled has nothing it may write, so it is
// skipped before the search and never counted as eligible; a dry run must not
// promise an apply the real run would refuse.
func TestAutoMatchTranscribed_FilledTitleAndAuthorSkipped(t *testing.T) {
	trans := "The Name of the Wind"
	author := "Patrick Rothfuss"
	authorID := 3
	books := []database.Book{{
		ID: "b1", Title: "Name Wind", AuthorID: &authorID,
		Author:           &database.Author{ID: authorID, Name: author},
		TranscribedTitle: new(trans), TranscribedAuthor: new(author),
	}}
	deps := &autoMatchDeps{
		searchFn: func(_ context.Context, _, _, _ string) (string, string, float64, bool, error) {
			return trans, author, 1.8, true, nil
		},
	}
	p := newAutoMatchPlugin(books, deps)
	if err := p.runAutoMatchTranscribed(context.Background(), autoMatchParams(false, 0), &fakeReporter{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deps.searchCalled != 0 || deps.applyCalled != 0 {
		t.Fatalf("filled book: search %d apply %d, want 0/0", deps.searchCalled, deps.applyCalled)
	}
}
