// file: internal/server/series_position_writeback_test.go
// version: 1.0.0
// guid: 1e6f4a92-8c07-4d31-b5a8-72c9e0d3f416
// last-edited: 2026-09-02

package server

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// normalizeFixture is a small but NON-EMPTY library: contaminated series rows,
// each with a real book row behind it. An empty fixture cannot observe this bug
// -- the old code computed the position, serialized it into the dry-run preview,
// and then dropped it on apply, which looks identical to "there was nothing to
// write" unless a book actually exists to write it to.
type normalizeFixture struct {
	mu    sync.Mutex
	store *database.MockStore
	books map[string]*database.Book
}

func newNormalizeFixture(t *testing.T, series []database.Series, books map[string]*database.Book) *normalizeFixture {
	t.Helper()
	f := &normalizeFixture{store: &database.MockStore{}, books: books}

	f.store.GetAllSeriesFunc = func() ([]database.Series, error) { return series, nil }
	f.store.GetBooksBySeriesIDCoreFunc = func(id int) ([]database.BookCore, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var out []database.BookCore
		for _, b := range f.books {
			if b.SeriesID != nil && *b.SeriesID == id {
				out = append(out, database.BookCore{ID: b.ID})
			}
		}
		return out, nil
	}
	f.store.GetBooksBySeriesIDAllVersionsFunc = f.store.GetBooksBySeriesIDCoreFunc
	f.store.UpdateSeriesNameFunc = func(int, string) error { return nil }
	f.store.DeleteSeriesFunc = func(int) error { return nil }
	f.store.GetBookByIDFunc = func(id string) (*database.Book, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		b, ok := f.books[id]
		if !ok {
			return nil, nil
		}
		cp := *b // hand out a copy, like a real store round-trip
		return &cp, nil
	}
	f.store.UpdateBookFunc = func(id string, book *database.Book) (*database.Book, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		cp := *book
		f.books[id] = &cp
		return &cp, nil
	}
	return f
}

func (f *normalizeFixture) run(t *testing.T) {
	t.Helper()
	if _, err := executeSeriesNormalizeCore(context.Background(), f.store, func(string) {}); err != nil {
		t.Fatalf("executeSeriesNormalizeCore: %v", err)
	}
}

func (f *normalizeFixture) seq(t *testing.T, id string) *int {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.books[id]
	if !ok {
		t.Fatalf("book %s missing", id)
	}
	return b.SeriesSequence
}

func seriesID(n int) *int { return &n }

// The apply pass renamed "Discworld 05" to "Discworld" and threw the 5 away.
// Renaming without recording the number destroys the only statement of where
// the book sits in its series.
func TestExecuteSeriesNormalize_WritesStrippedPositionIntoSequence(t *testing.T) {
	authorID := 1
	f := newNormalizeFixture(t,
		[]database.Series{
			{ID: 1, Name: "Discworld 05", AuthorID: &authorID},
			{ID: 2, Name: "Nameless Sovereign #5", AuthorID: &authorID},
			{ID: 3, Name: "Dragon Born [04]", AuthorID: &authorID},
		},
		map[string]*database.Book{
			"book-1": {ID: "book-1", Title: "Wyrd Sisters", SeriesID: seriesID(1)},
			"book-2": {ID: "book-2", Title: "The Fifth", SeriesID: seriesID(2)},
			"book-3": {ID: "book-3", Title: "Blood Bond", SeriesID: seriesID(3)},
		})

	f.run(t)

	for _, tc := range []struct {
		id   string
		want int
	}{{"book-1", 5}, {"book-2", 5}, {"book-3", 4}} {
		got := f.seq(t, tc.id)
		if got == nil {
			t.Errorf("%s: series_sequence was not written; the stripped number was DELETED", tc.id)
			continue
		}
		if *got != tc.want {
			t.Errorf("%s: series_sequence = %d, want %d", tc.id, *got, tc.want)
		}
	}
}

// An existing non-zero sequence came from the file's tags or a provider's own
// field and must survive the normalize pass untouched.
func TestExecuteSeriesNormalize_DoesNotOverwriteExistingSequence(t *testing.T) {
	authorID := 1
	existing := 3
	f := newNormalizeFixture(t,
		[]database.Series{{ID: 1, Name: "Discworld 05", AuthorID: &authorID}},
		map[string]*database.Book{
			"book-1": {ID: "book-1", Title: "Wyrd Sisters", SeriesID: seriesID(1), SeriesSequence: &existing},
		})

	f.run(t)

	got := f.seq(t, "book-1")
	if got == nil {
		t.Fatalf("series_sequence was cleared")
	}
	if *got != 3 {
		t.Errorf("series_sequence = %d, want 3 -- an existing sequence must never be overwritten", *got)
	}
}

// A library with nothing to strip must not have sequences invented for it, and
// an un-vouched name must be reported rather than rewritten.
func TestExecuteSeriesNormalize_LeavesCleanAndUnvouchedSeriesAlone(t *testing.T) {
	authorID := 1
	var renamed []string
	f := newNormalizeFixture(t,
		[]database.Series{
			{ID: 1, Name: "The Expanse", AuthorID: &authorID},
			{ID: 2, Name: "08. Battle for the Abyss", AuthorID: &authorID},
		},
		map[string]*database.Book{
			"book-1": {ID: "book-1", Title: "Leviathan Wakes", SeriesID: seriesID(1)},
			"book-2": {ID: "book-2", Title: "Battle for the Abyss", SeriesID: seriesID(2)},
		})
	f.store.UpdateSeriesNameFunc = func(id int, name string) error {
		renamed = append(renamed, fmt.Sprintf("%d=%s", id, name))
		return nil
	}

	f.run(t)

	if len(renamed) != 0 {
		t.Errorf("no series should have been renamed, got %v", renamed)
	}
	for _, id := range []string{"book-1", "book-2"} {
		if got := f.seq(t, id); got != nil {
			t.Errorf("%s: series_sequence = %d, want none invented", id, *got)
		}
	}
}

// The un-vouched flag must reach the preview with its REASON and the candidate
// it declined, so an operator can act on it. Action:"flag" now covers two
// unrelated populations and merging them would hide both.
func TestComputeSeriesNormalizeActions_UnvouchedFlagCarriesReason(t *testing.T) {
	authorID := 1
	store := &database.MockStore{}
	store.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{{ID: 1, Name: "08. Battle for the Abyss", AuthorID: &authorID}}, nil
	}
	store.GetBooksBySeriesIDCoreFunc = func(int) ([]database.BookCore, error) {
		return []database.BookCore{{ID: "book-1"}}, nil
	}

	actions, err := computeSeriesNormalizeActions(store)
	if err != nil {
		t.Fatalf("computeSeriesNormalizeActions: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(actions))
	}
	a := actions[0]
	if a.Action != "flag" {
		t.Errorf("action = %q, want flag", a.Action)
	}
	if a.NewName != "08. Battle for the Abyss" {
		t.Errorf("a flagged name must not be rewritten, got %q", a.NewName)
	}
	if a.FlagReason != string(metadata.FlagUnvouchedPosition) {
		t.Errorf("flag_reason = %q, want %q", a.FlagReason, metadata.FlagUnvouchedPosition)
	}
	if a.CandidateName != "Battle for the Abyss" || a.CandidatePosition != "8" {
		t.Errorf("candidate = %q/%q, want %q/%q",
			a.CandidateName, a.CandidatePosition, "Battle for the Abyss", "8")
	}
}
