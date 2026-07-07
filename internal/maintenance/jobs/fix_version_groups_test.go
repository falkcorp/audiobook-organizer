// file: internal/maintenance/jobs/fix_version_groups_test.go
// version: 1.1.0
// guid: d0e1f2a3-b4c5-6789-defa-012345678901
// last-edited: 2026-07-07

// Package jobs_test exercises the fix-version-groups maintenance job.
// The blank import in fix_read_by_narrator_test.go already registers all
// jobs; this file relies on that side-effect.
package jobs_test

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

func TestFixVersionGroupsJob_Registered(t *testing.T) {
	j, err := maintenance.Get("fix-version-groups")
	if err != nil {
		t.Fatalf("job not registered: %v", err)
	}
	if j.ID() != "fix-version-groups" {
		t.Fatalf("unexpected ID: %q", j.ID())
	}
	if j.Name() == "" {
		t.Fatal("Name() must not be empty")
	}
	if j.Description() == "" {
		t.Fatal("Description() must not be empty")
	}
	if j.Category() == "" {
		t.Fatal("Category() must not be empty")
	}
}

func TestFixVersionGroupsJob_DefaultParams(t *testing.T) {
	j, err := maintenance.Get("fix-version-groups")
	if err != nil {
		t.Fatalf("job not registered: %v", err)
	}
	params := j.DefaultParams()
	if params == nil {
		t.Fatal("DefaultParams() must not be nil")
	}
}

func TestFixVersionGroupsJob_DryRunNoGroups(t *testing.T) {
	// No books → no-op, no error.
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			return []database.BookCore{}, nil
		},
	}

	j, err := maintenance.Get("fix-version-groups")
	if err != nil {
		t.Fatalf("job not registered: %v", err)
	}

	if err = j.Run(context.Background(), store, &noopReporter{}, true); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestFixVersionGroupsJob_DryRunDoesNotUpdate(t *testing.T) {
	// Two books in the same version group with mismatched titles.
	// In dry-run mode no UpdateBook calls should occur.
	groupID := "group-abc"
	books := []database.Book{
		{ID: "book-vg-1", Title: "The Odyssey", VersionGroupID: &groupID},
		{ID: "book-vg-2", Title: "Something Completely Different", VersionGroupID: &groupID},
	}

	var updateCalled bool
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			cores := make([]database.BookCore, len(books))
			for i := range books {
				cores[i] = books[i].Core()
			}
			return cores, nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			updateCalled = true
			return b, nil
		},
	}

	j, err := maintenance.Get("fix-version-groups")
	if err != nil {
		t.Fatalf("job not registered: %v", err)
	}

	if err = j.Run(context.Background(), store, &noopReporter{}, true /* dryRun */); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if updateCalled {
		t.Fatal("dry_run=true: UpdateBook must not be called")
	}
}

func TestFixVersionGroupsJob_ApplyUnlinksOutlier(t *testing.T) {
	// Two books in the same version group; titles are very different so
	// the outlier should be unlinked (assigned a new group ID).
	groupID := "group-xyz"
	books := []database.Book{
		{ID: "book-match-1", Title: "The Odyssey", VersionGroupID: &groupID},
		{ID: "book-match-2", Title: "The Odyssey (Unabridged)", VersionGroupID: &groupID},
		{ID: "book-outlier", Title: "Unrelated Title Entirely", VersionGroupID: &groupID},
	}

	bookMap := map[string]*database.Book{}
	for i := range books {
		cp := books[i]
		bookMap[cp.ID] = &cp
	}

	var updatedIDs []string
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			cores := make([]database.BookCore, len(books))
			for i := range books {
				cores[i] = books[i].Core()
			}
			return cores, nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			b := bookMap[id]
			if b == nil {
				return nil, nil
			}
			cp := *b
			return &cp, nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			updatedIDs = append(updatedIDs, id)
			bookMap[id] = b
			return b, nil
		},
	}

	j, err := maintenance.Get("fix-version-groups")
	if err != nil {
		t.Fatalf("job not registered: %v", err)
	}

	if err = j.Run(context.Background(), store, &noopReporter{}, false /* apply */); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// The outlier book should have been updated (group re-assigned).
	found := false
	for _, id := range updatedIDs {
		if id == "book-outlier" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected book-outlier to be updated; updated IDs: %v", updatedIDs)
	}
}

func TestFixVersionGroupsJob_Cancellation(t *testing.T) {
	books := make([]database.Book, 5)
	groupID := "group-cancel"
	for i := range books {
		books[i] = database.Book{
			ID:             "book-cancel-vg-" + string(rune('0'+i)),
			Title:          "The Odyssey",
			VersionGroupID: &groupID,
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			cores := make([]database.BookCore, len(books))
			for i := range books {
				cores[i] = books[i].Core()
			}
			return cores, nil
		},
	}

	j, err := maintenance.Get("fix-version-groups")
	if err != nil {
		t.Fatalf("job not registered: %v", err)
	}

	err = j.Run(ctx, store, &noopReporter{}, false)
	// Must not panic or hang; context.Canceled or nil are both acceptable.
	if err != nil && err != context.Canceled {
		t.Fatalf("unexpected error: %v", err)
	}
}
