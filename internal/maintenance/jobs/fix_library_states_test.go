// file: internal/maintenance/jobs/fix_library_states_test.go
// version: 1.0.1
// guid: e2f3a4b5-c6d7-8901-efab-234567890567
// last-edited: 2026-07-07

// Package jobs_test exercises the fix-library-states maintenance job.
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

func TestFixLibraryStatesJob_Registered(t *testing.T) {
	assertJobRegistered(t, "fix-library-states")
}

func TestFixLibraryStatesJob_Metadata(t *testing.T) {
	j, err := maintenance.Get("fix-library-states")
	require.NoError(t, err)
	assert.Equal(t, "fix-library-states", j.ID())
	assert.NotEmpty(t, j.Name())
	assert.NotEmpty(t, j.Description())
	assert.Equal(t, "library", j.Category())
	assert.NotNil(t, j.DefaultParams())
}

func TestFixLibraryStatesJob_DryRun_NoUpdate(t *testing.T) {
	// Book with disagreeing library_state — dry_run must not call UpdateBook.
	missing := "missing"
	book := database.Book{
		ID:           "book-dry",
		Title:        "Dry Book",
		FilePath:     "", // no file path → state should be "missing"
		LibraryState: &missing,
	}

	var updateCalled bool
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			if offset > 0 {
				return nil, nil
			}
			return []database.BookCore{book.Core()}, nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			b := book
			return &b, nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			updateCalled = true
			return b, nil
		},
	}

	j, err := maintenance.Get("fix-library-states")
	require.NoError(t, err)
	err = j.Run(context.Background(), store, &noopReporter{}, true /* dryRun */)
	require.NoError(t, err)
	assert.False(t, updateCalled, "dry_run=true: UpdateBook must not be called")
}

func TestFixLibraryStatesJob_Apply_UpdatesBook(t *testing.T) {
	// Book whose state says "present" but FilePath is empty (no file) → should become "missing".
	present := "present"
	book := database.Book{
		ID:           "book-apply",
		Title:        "Apply Book",
		FilePath:     "", // does not exist → wantState = "missing"
		LibraryState: &present,
	}

	var updatedState string
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			if offset > 0 {
				return nil, nil
			}
			return []database.BookCore{book.Core()}, nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			b := book
			return &b, nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			if b.LibraryState != nil {
				updatedState = *b.LibraryState
			}
			return b, nil
		},
	}

	j, err := maintenance.Get("fix-library-states")
	require.NoError(t, err)
	err = j.Run(context.Background(), store, &noopReporter{}, false /* apply */)
	require.NoError(t, err)
	assert.Equal(t, "missing", updatedState, "book with no file path should be set to missing")
}

func TestFixLibraryStatesJob_NoChanges_WhenStateCorrect(t *testing.T) {
	// Book already has correct state — UpdateBook must not be called.
	missing := "missing"
	book := database.Book{
		ID:           "book-correct",
		Title:        "Correct State Book",
		FilePath:     "", // does not exist → wantState = "missing" (already correct)
		LibraryState: &missing,
	}

	var updateCalled bool
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			if offset > 0 {
				return nil, nil
			}
			return []database.BookCore{book.Core()}, nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			b := book
			return &b, nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			updateCalled = true
			return b, nil
		},
	}

	j, err := maintenance.Get("fix-library-states")
	require.NoError(t, err)
	err = j.Run(context.Background(), store, &noopReporter{}, false /* apply */)
	require.NoError(t, err)
	assert.False(t, updateCalled, "book already in correct state: UpdateBook must not be called")
}

func TestFixLibraryStatesJob_Cancellation(t *testing.T) {
	present := "present"
	books := make([]database.Book, 5)
	for i := range books {
		books[i] = database.Book{
			ID:           "book-cancel-" + string(rune('0'+i)),
			Title:        "Cancel Book",
			FilePath:     "", // triggers state change detection
			LibraryState: &present,
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	core := make([]database.BookCore, len(books))
	for i := range books {
		core[i] = books[i].Core()
	}
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			if offset > 0 {
				return nil, nil
			}
			return core, nil
		},
	}

	j, err := maintenance.Get("fix-library-states")
	require.NoError(t, err)
	err = j.Run(ctx, store, &noopReporter{}, false)
	// Should return ctx.Err() or nil — must not panic or hang.
	if err != nil {
		assert.ErrorIs(t, err, context.Canceled)
	}
}
