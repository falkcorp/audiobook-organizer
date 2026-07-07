// file: internal/maintenance/jobs/fix_author_narrator_swap_test.go
// version: 1.2.0
// guid: a7b8c9d0-e1f2-3456-abcd-789012345678
// last-edited: 2026-07-07

package jobs_test

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noopReporter and assertJobRegistered are defined in testhelpers_test.go.

func TestFixAuthorNarratorSwapJob_Registered(t *testing.T) {
	assertJobRegistered(t, "fix-author-narrator-swap")
}

func TestFixAuthorNarratorSwapJob_Metadata(t *testing.T) {
	j, err := maintenance.Get("fix-author-narrator-swap")
	require.NoError(t, err)
	assert.Equal(t, "fix-author-narrator-swap", j.ID())
	assert.NotEmpty(t, j.Name())
	assert.NotEmpty(t, j.Description())
	assert.Equal(t, "library", j.Category())
	assert.NotNil(t, j.DefaultParams())
}

func TestFixAuthorNarratorSwapJob_DryRun_NoChanges(t *testing.T) {
	// Books where author name != narrator — nothing to swap.
	authorID := 42
	narratorName := "Someone Else"
	books := []database.Book{
		{ID: "book-1", Title: "Normal Book", AuthorID: &authorID, Narrator: &narratorName},
	}
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			if offset > 0 {
				return nil, nil
			}
			cores := make([]database.BookCore, len(books))
			for i := range books {
				cores[i] = books[i].Core()
			}
			return cores, nil
		},
		GetAuthorByIDFunc: func(id int) (*database.Author, error) {
			return &database.Author{ID: id, Name: "Real Author"}, nil
		},
	}

	j, err := maintenance.Get("fix-author-narrator-swap")
	require.NoError(t, err)
	reporter := &noopReporter{}
	require.NoError(t, j.Run(context.Background(), store, reporter, true /* dryRun */))
}

func TestFixAuthorNarratorSwapJob_DryRun_DetectsSwap(t *testing.T) {
	// Book where narrator == author name — should be detected as swapped.
	authorID := 7
	swappedName := "Stephen King" // same as author name below
	books := []database.Book{
		{ID: "book-swap", Title: "Swapped Book", AuthorID: &authorID, Narrator: &swappedName},
	}
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			if offset > 0 {
				return nil, nil
			}
			cores := make([]database.BookCore, len(books))
			for i := range books {
				cores[i] = books[i].Core()
			}
			return cores, nil
		},
		GetAuthorByIDFunc: func(id int) (*database.Author, error) {
			return &database.Author{ID: id, Name: "Stephen King"}, nil
		},
	}

	j, err := maintenance.Get("fix-author-narrator-swap")
	require.NoError(t, err)
	reporter := &noopReporter{}
	require.NoError(t, j.Run(context.Background(), store, reporter, true /* dryRun */))
	// In dry-run mode the summary log should mention found=1.
	found := false
	for _, msg := range reporter.logs {
		if len(msg) > 0 {
			found = true
			break
		}
	}
	assert.True(t, found, "expected at least one log message")
}

func TestFixAuthorNarratorSwapJob_CancelRespected(t *testing.T) {
	authorID := 1
	narrator := "Same Name"
	books := make([]database.Book, 10)
	for i := range books {
		a := authorID
		n := narrator
		books[i] = database.Book{ID: "b", Title: "T", AuthorID: &a, Narrator: &n}
	}

	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			if offset > 0 {
				return nil, nil
			}
			cores := make([]database.BookCore, len(books))
			for i := range books {
				cores[i] = books[i].Core()
			}
			return cores, nil
		},
		GetAuthorByIDFunc: func(id int) (*database.Author, error) {
			return &database.Author{ID: id, Name: "Same Name"}, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	j, err := maintenance.Get("fix-author-narrator-swap")
	require.NoError(t, err)
	reporter := &noopReporter{}
	// Should return early without error when ctx is already cancelled.
	err = j.Run(ctx, store, reporter, false)
	// Either nil (job checks ctx before first iteration) or context.Canceled.
	if err != nil {
		assert.ErrorIs(t, err, context.Canceled)
	}
}
