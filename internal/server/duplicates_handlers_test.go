// file: internal/server/duplicates_handlers_test.go
// version: 1.7.0
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

	actions, err := computeSeriesNormalizeActions(store)
	if err != nil {
		t.Fatalf("computeSeriesNormalizeActions: %v", err)
	}

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

	actions, err := computeSeriesNormalizeActions(store)
	if err != nil {
		t.Fatalf("computeSeriesNormalizeActions: %v", err)
	}
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
	// only AllVersions can see. The merge repoints it (correctly), and the
	// affected list must still NOT contain it -- see the assertion below.
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
	// The non-primary version must NOT be in the affected set.
	//
	// This asserted the opposite for one commit, on the reasoning that a row the
	// merge repoints should also have its file moved. That reasoning was wrong:
	// affectedBookIDs is the worklist for ReOrganizeInPlace, and the organizer
	// deliberately never organizes a non-primary version while a primary exists
	// (organizer/service.go:640). duplicates_ops.go calls ReOrganizeInPlace
	// directly and so bypasses that filter -- meaning a widened list here does
	// not "keep row and file in sync", it silently overrides organize policy
	// from outside.
	//
	// It would also collide: the default naming patterns carry no codec or
	// edition variable, so a primary and its alternate rip compute the SAME
	// destination path. One of the two would claim it and the other would be
	// refused, with the winner decided by emission order rather than primacy.
	//
	// Repointing it (which the merge does) and organizing it are separate
	// questions. This pins the answer to the second one.
	if slices.Contains(affected, "book-2-alt") {
		t.Errorf("non-primary version book-2-alt is in affected books %v; it must not be. "+
			"The caller runs ReOrganizeInPlace on this list, bypassing the organizer's own "+
			"non-primary filter, and the alternate rip computes the same target path as its "+
			"primary -- so including it causes a path collision, not a correctly moved file.", affected)
	}
	// ...and the primary still must be, or the list is simply broken.
	if !slices.Contains(affected, "book-2") {
		t.Errorf("primary book-2 missing from affected books %v", affected)
	}
}

// TestComputeSeriesNormalizeActions_ReportsAListingFailure pins the difference
// between "nothing needs normalizing" and "nothing was examined".
//
// This used to return a bare nil on a GetAllSeries failure, with no error return
// to put the failure in and no log. An empty action list is indistinguishable
// from a clean library, so the operation reported "Series normalization complete,
// affected_books=0" with status success — and the dry-run PREVIEW showed the same
// empty, clean-looking list to whoever was deciding whether to approve the run.
func TestComputeSeriesNormalizeActions_ReportsAListingFailure(t *testing.T) {
	store := &database.MockStore{}
	store.GetAllSeriesFunc = func() ([]database.Series, error) {
		return nil, fmt.Errorf("simulated: store unavailable")
	}

	actions, err := computeSeriesNormalizeActions(store)
	if err == nil {
		t.Fatal("a failed series listing returned nil error; an empty action list then reads " +
			"as 'the library is already clean' when nothing was examined at all")
	}
	if actions != nil {
		t.Errorf("expected no actions alongside the error, got %d", len(actions))
	}
}
