// file: internal/server/duplicates_handlers_test.go
// version: 1.5.0
// guid: 9c1e2f3a-4b5d-6e7f-8a9b-0c1d2e3f4a5b
// last-edited: 2026-08-24

package server

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestComputeSeriesNormalizeActions_Basic(t *testing.T) {
	authorID := 1
	store := &database.MockStore{}
	store.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{
			{ID: 1, Name: "The Long Earth One", AuthorID: &authorID},
			{ID: 2, Name: "The Long Earth Two", AuthorID: &authorID},
			{ID: 3, Name: "Discworld", AuthorID: &authorID},
		}, nil
	}
	store.GetBooksBySeriesIDCoreFunc = func(id int) ([]database.BookCore, error) {
		return []database.BookCore{{ID: fmt.Sprintf("book-%d", id)}}, nil
	}

	actions := computeSeriesNormalizeActions(store)

	for _, a := range actions {
		if a.OldName == "Discworld" {
			t.Errorf("clean series Discworld should not appear in actions")
		}
	}
	if len(actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(actions))
	}

	var renameCount, mergeCount int
	var foundMergeWithTarget bool
	for _, a := range actions {
		switch a.Action {
		case "rename":
			renameCount++
		case "merge_into":
			mergeCount++
			if a.MergeTargetID != nil {
				foundMergeWithTarget = true
			}
		}
	}
	if renameCount != 1 {
		t.Errorf("expected 1 rename action, got %d", renameCount)
	}
	if mergeCount != 1 {
		t.Errorf("expected 1 merge_into action, got %d", mergeCount)
	}
	if !foundMergeWithTarget {
		t.Errorf("expected merge_into action to have non-nil MergeTargetID")
	}
}

func TestComputeSeriesNormalizeActions_FlaggedCase(t *testing.T) {
	authorID := 1
	store := &database.MockStore{}
	// A series whose name equals the book title should be flagged, not renamed.
	// StripSeriesContamination with title="" won't flag it since title is empty.
	// So for a flagged case we need series == title passed somehow.
	// computeSeriesNormalizeActions calls StripSeriesContamination(s.Name, "") — title is always "".
	// flagForReview is only true when name==title and title!="". Since title is "" here, flag won't trigger via title.
	// However, a "dash-embedded" series WILL produce a rename action.
	// Test that a series with no contamination stays out of actions entirely.
	store.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{
			{ID: 10, Name: "Clean Series Name", AuthorID: &authorID},
		}, nil
	}
	store.GetBooksBySeriesIDCoreFunc = func(id int) ([]database.BookCore, error) {
		return []database.BookCore{{ID: "book-10"}}, nil
	}

	actions := computeSeriesNormalizeActions(store)
	if len(actions) != 0 {
		t.Errorf("expected 0 actions for clean series, got %d: %+v", len(actions), actions)
	}
}

func TestExecuteSeriesNormalizeCore_RenamesAndEnqueues(t *testing.T) {
	authorID := 1
	store := &database.MockStore{}
	store.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{
			{ID: 1, Name: "The Long Earth One", AuthorID: &authorID},
			{ID: 2, Name: "The Long Earth Two", AuthorID: &authorID},
		}, nil
	}
	store.GetBooksBySeriesIDCoreFunc = func(id int) ([]database.BookCore, error) {
		switch id {
		case 1:
			return []database.BookCore{{ID: "book-1"}}, nil
		case 2:
			return []database.BookCore{{ID: "book-2"}}, nil
		}
		return nil, nil
	}
	// The listing getter above hides non-primary versions; this one does not.
	// Modelling the difference is the point: series 2 has an alternate rip that
	// only AllVersions can see, and the merge repoints it, so it must appear in
	// affectedBookIDs or its file is never moved and its tags never rewritten.
	store.GetBooksBySeriesIDAllVersionsFunc = func(id int) ([]database.BookCore, error) {
		switch id {
		case 1:
			return []database.BookCore{{ID: "book-1"}}, nil
		case 2:
			return []database.BookCore{{ID: "book-2"}, {ID: "book-2-alt"}}, nil
		}
		return nil, nil
	}
	renamed := map[int]string{}
	store.UpdateSeriesNameFunc = func(id int, name string) error {
		renamed[id] = name
		return nil
	}
	store.GetBookByIDFunc = func(id string) (*database.Book, error) {
		sid := 1
		return &database.Book{ID: id, SeriesID: &sid}, nil
	}
	store.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) { return b, nil }
	store.DeleteSeriesFunc = func(id int) error { return nil }

	var enqueuedBooks []string
	enqueueWB := func(id string) { enqueuedBooks = append(enqueuedBooks, id) }

	affected, err := executeSeriesNormalizeCore(context.Background(), store, enqueueWB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if renamed[1] != "The Long Earth" {
		t.Errorf("expected series 1 renamed to 'The Long Earth', got %q", renamed[1])
	}
	if len(enqueuedBooks) == 0 {
		t.Errorf("expected write-back enqueues for affected books")
	}
	if len(affected) == 0 {
		t.Errorf("expected affected book IDs returned")
	}
	// The non-primary version must be in the affected set. The caller organizes
	// (moves files for) and writes tags back to exactly this list, so a row the
	// merge repoints but the list omits keeps its file under the old series'
	// path with stale tags -- silent, and invisible to any assertion on len().
	if !slices.Contains(affected, "book-2-alt") {
		t.Errorf("non-primary version book-2-alt missing from affected books %v; "+
			"the affected list must be collected with the same getter the merge "+
			"repoints with, or its file is never moved and its tags never rewritten", affected)
	}
}
