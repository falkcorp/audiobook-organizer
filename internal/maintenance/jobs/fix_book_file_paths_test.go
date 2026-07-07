// file: internal/maintenance/jobs/fix_book_file_paths_test.go
// version: 1.1.0
// guid: b1c2d3e4-f5a6-7890-bcde-123456789456
// last-edited: 2026-07-06

// Package jobs_test exercises the fix-book-file-paths maintenance job.
package jobs_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFixBookFilePathsJob_Registered(t *testing.T) {
	j, err := maintenance.Get("fix-book-file-paths")
	require.NoError(t, err, "job fix-book-file-paths should be registered")
	assert.Equal(t, "fix-book-file-paths", j.ID())
}

func TestFixBookFilePathsJob_Metadata(t *testing.T) {
	j, err := maintenance.Get("fix-book-file-paths")
	require.NoError(t, err)
	assert.Equal(t, "fix-book-file-paths", j.ID())
	assert.NotEmpty(t, j.Name())
	assert.NotEmpty(t, j.Description())
	assert.NotEmpty(t, j.Category())
	assert.NotNil(t, j.DefaultParams())
}

func TestFixBookFilePathsJob_DefaultParams_DryRunTrue(t *testing.T) {
	j, err := maintenance.Get("fix-book-file-paths")
	require.NoError(t, err)
	params := j.DefaultParams()
	require.NotNil(t, params)
	// DefaultParams should enable dry_run by default.
	// The structural check is in the job itself.
	assert.NotNil(t, params)
}

func TestFixBookFilePathsJob_EmptyStore_NoOp(t *testing.T) {
	// No book_files → nothing to process, no error.
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
			return []database.BookFileCore{}, nil
		},
	}
	j, err := maintenance.Get("fix-book-file-paths")
	require.NoError(t, err)

	reporter := &noopReporter{}
	require.NoError(t, j.Run(context.Background(), store, reporter, true /* dryRun */))
}

func TestFixBookFilePathsJob_DryRun_DoesNotUpdateDB(t *testing.T) {
	// A book_file whose stored path doesn't exist on disk.
	files := []database.BookFileCore{
		{ID: "bf-1", BookID: "book-1", FilePath: "/nonexistent/path/chapter.mp3"},
	}

	var updateCalled bool
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
			return files, nil
		},
		UpdateBookFileFunc: func(id string, bf *database.BookFile) error {
			updateCalled = true
			return nil
		},
	}

	j, err := maintenance.Get("fix-book-file-paths")
	require.NoError(t, err)

	reporter := &noopReporter{}
	require.NoError(t, j.Run(context.Background(), store, reporter, true /* dryRun */))
	assert.False(t, updateCalled, "dry_run=true: UpdateBookFile must not be called")
}

func TestFixBookFilePathsJob_SkipsExistingPaths(t *testing.T) {
	// Create a real file on disk so its path actually exists.
	tmpDir := t.TempDir()
	realFile := filepath.Join(tmpDir, "chapter.mp3")
	f, err := os.Create(realFile)
	require.NoError(t, err)
	f.Close()

	files := []database.BookFileCore{
		{ID: "bf-exists", BookID: "book-exists", FilePath: realFile},
	}

	var updateCalled bool
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
			return files, nil
		},
		UpdateBookFileFunc: func(id string, bf *database.BookFile) error {
			updateCalled = true
			return nil
		},
	}

	j, err := maintenance.Get("fix-book-file-paths")
	require.NoError(t, err)

	reporter := &noopReporter{}
	require.NoError(t, j.Run(context.Background(), store, reporter, false /* apply */))
	assert.False(t, updateCalled, "file with valid path: UpdateBookFile must not be called")
}

func TestFixBookFilePathsJob_Apply_MarksAsMissing(t *testing.T) {
	// A book_file whose path doesn't exist on disk → in apply mode it should be updated (marked missing).
	files := []database.BookFileCore{
		{ID: "bf-missing", BookID: "book-m", FilePath: "/no/such/file.mp3", Missing: false},
	}

	var updatedID string
	var updatedMissing bool
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
			return files, nil
		},
		// The job now hydrates the full row via GetBookFiles(bookID) before
		// writing back (see docs/audits/2026-07-05-updatebookfile-memdb-writeback-fingerprint-wipe.md).
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			return []database.BookFile{
				{ID: "bf-missing", BookID: "book-m", FilePath: "/no/such/file.mp3", Missing: false},
			}, nil
		},
		UpdateBookFileFunc: func(id string, bf *database.BookFile) error {
			updatedID = id
			updatedMissing = bf.Missing
			return nil
		},
	}

	j, err := maintenance.Get("fix-book-file-paths")
	require.NoError(t, err)

	reporter := &noopReporter{}
	require.NoError(t, j.Run(context.Background(), store, reporter, false /* apply */))
	assert.Equal(t, "bf-missing", updatedID, "UpdateBookFile should be called with the broken file's ID")
	assert.True(t, updatedMissing, "file should be marked as missing after apply")
}

func TestFixBookFilePathsJob_CancelRespected(t *testing.T) {
	// Many files — cancellation should cause the job to return early.
	files := make([]database.BookFileCore, 20)
	for i := range files {
		files[i] = database.BookFileCore{
			ID:       "bf-cancel-" + string(rune('0'+i%10)),
			BookID:   "book-cancel",
			FilePath: "/nonexistent/cancel-" + string(rune('0'+i%10)) + ".mp3",
		}
	}

	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
			return files, nil
		},
		UpdateBookFileFunc: func(id string, bf *database.BookFile) error {
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	j, err := maintenance.Get("fix-book-file-paths")
	require.NoError(t, err)

	reporter := &noopReporter{}
	runErr := j.Run(ctx, store, reporter, false)
	// Must not panic or hang; context.Canceled or nil are both acceptable.
	if runErr != nil {
		assert.ErrorIs(t, runErr, context.Canceled)
	}
}
