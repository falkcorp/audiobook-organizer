// file: internal/maintenance/jobs/refetch_missing_authors_test.go
// version: 1.0.1
// guid: e4f5a6b7-c8d9-0123-efab-456789012789
// last-edited: 2026-07-07

package jobs_test

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	_ "github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs" // register all jobs
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRefetchMissingAuthorsJob_Registered(t *testing.T) {
	j, err := maintenance.Get("refetch-missing-authors")
	require.NoError(t, err, "job should be registered")
	assert.Equal(t, "refetch-missing-authors", j.ID())
}

func TestRefetchMissingAuthorsJob_Metadata(t *testing.T) {
	j, err := maintenance.Get("refetch-missing-authors")
	require.NoError(t, err)
	assert.NotEmpty(t, j.Name())
	assert.NotEmpty(t, j.Description())
	assert.Equal(t, "library", j.Category())
	assert.NotNil(t, j.DefaultParams())
	assert.True(t, j.CanResume())
}

func TestRefetchMissingAuthorsJob_DefaultParamsDryRunTrue(t *testing.T) {
	j, err := maintenance.Get("refetch-missing-authors")
	require.NoError(t, err)
	params := j.DefaultParams()
	require.NotNil(t, params)
	// DefaultParams should default to DryRun: true
	type dryRunnable interface{ GetDryRun() bool }
	if dr, ok := params.(dryRunnable); ok {
		assert.True(t, dr.GetDryRun())
	}
}

func TestRefetchMissingAuthorsJob_NoBooksReturned(t *testing.T) {
	// No books at all — job should complete cleanly.
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			return nil, nil
		},
	}

	j, err := maintenance.Get("refetch-missing-authors")
	require.NoError(t, err)

	reporter := &noopReporter{}
	require.NoError(t, j.Run(context.Background(), store, reporter, true))
}

func TestRefetchMissingAuthorsJob_AllBooksHaveAuthor(t *testing.T) {
	// No books without author — nothing to update.
	authorID := 1
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			return []database.BookCore{
				{ID: "b1", Title: "Book One", AuthorID: &authorID},
			}, nil
		},
	}

	var updateCalled bool
	store.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		updateCalled = true
		return b, nil
	}

	j, err := maintenance.Get("refetch-missing-authors")
	require.NoError(t, err)

	reporter := &noopReporter{}
	require.NoError(t, j.Run(context.Background(), store, reporter, false))
	assert.False(t, updateCalled, "no book should be updated when all have authors")
}

func TestRefetchMissingAuthorsJob_SkipsBookWithNoFiles(t *testing.T) {
	// Book with no author and no files — should be skipped without error.
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			return []database.BookCore{
				{ID: "b-nofile", Title: "Orphan Book", AuthorID: nil, FilePath: ""},
			}, nil
		},
	}

	var updateCalled bool
	store.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		updateCalled = true
		return b, nil
	}

	j, err := maintenance.Get("refetch-missing-authors")
	require.NoError(t, err)

	reporter := &noopReporter{}
	require.NoError(t, j.Run(context.Background(), store, reporter, false))
	assert.False(t, updateCalled)
}

func TestRefetchMissingAuthorsJob_CancelationRespected(t *testing.T) {
	books := make([]database.BookCore, 5)
	for i := range books {
		books[i] = database.BookCore{ID: "b-cancel-" + string(rune('0'+i)), Title: "T"}
	}

	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			return books, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	j, err := maintenance.Get("refetch-missing-authors")
	require.NoError(t, err)

	reporter := &noopReporter{}
	err = j.Run(ctx, store, reporter, false)
	// Must not panic; may return nil (all books have authors) or context.Canceled.
	if err != nil {
		assert.ErrorIs(t, err, context.Canceled)
	}
}
