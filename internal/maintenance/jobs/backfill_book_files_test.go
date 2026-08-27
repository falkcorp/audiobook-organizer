// file: internal/maintenance/jobs/backfill_book_files_test.go
// version: 1.3.0
// guid: 73490888-1dfc-4aa9-93a9-5d730a8d4f79
// last-edited: 2026-08-27

package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackfillBookFilesReportsDryRunCandidatesForDirectoryAndFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "one.mp3"), []byte("audio"), 0o600))
	single := filepath.Join(t.TempDir(), "single.m4b")
	require.NoError(t, os.WriteFile(single, []byte("audio"), 0o600))

	books := []database.BookCore{{ID: "directory", FilePath: dir}, {ID: "single", FilePath: single}}
	var summary *database.OperationSummaryLog
	store := &database.MockStore{
		GetAllBooksCoreFunc:         func(int, int) ([]database.BookCore, error) { return books, nil },
		SaveOperationSummaryLogFunc: func(got *database.OperationSummaryLog) error { summary = got; return nil },
		CreateBookFileFunc:          func(*database.BookFile) error { t.Fatal("dry run must not write"); return nil },
		BatchCreateBookFilesFunc:    func([]*database.BookFile) error { t.Fatal("dry run must not write"); return nil },
	}

	job, err := maintenance.Get("backfill-book-files")
	require.NoError(t, err)
	require.NoError(t, job.Run(maintenance.WithOperationID(context.Background(), "dry-run"), store, &noopReporter{}, true))

	require.NotNil(t, summary)
	var result struct {
		DryRun         bool `json:"dry_run"`
		CandidateFiles int  `json:"candidate_files"`
		Created        int  `json:"created"`
		Errors         int  `json:"errors"`
	}
	require.NoError(t, json.Unmarshal([]byte(*summary.Result), &result))
	assert.True(t, result.DryRun)
	assert.Equal(t, 2, result.CandidateFiles)
	assert.Zero(t, result.Created)
	assert.Zero(t, result.Errors)
}

func TestBackfillBookFilesBatchesEveryMissingRowForAnEligibleBook(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.mp3")
	second := filepath.Join(dir, "second.m4b")
	require.NoError(t, os.WriteFile(first, []byte("audio"), 0o600))
	require.NoError(t, os.WriteFile(second, []byte("audio"), 0o600))
	books := []database.BookCore{{ID: "directory", FilePath: dir}}

	var batches [][]*database.BookFile
	var summary *database.OperationSummaryLog
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(int, int) ([]database.BookCore, error) { return books, nil },
		CreateBookFileFunc: func(*database.BookFile) error {
			t.Fatal("eligible books must use BatchCreateBookFiles")
			return nil
		},
		BatchCreateBookFilesFunc: func(rows []*database.BookFile) error {
			batches = append(batches, rows)
			return nil
		},
		SaveOperationSummaryLogFunc: func(got *database.OperationSummaryLog) error { summary = got; return nil },
	}
	job, _ := maintenance.Get("backfill-book-files")
	require.NoError(t, job.Run(maintenance.WithOperationID(context.Background(), "apply"), store, &noopReporter{}, false))
	require.Len(t, batches, 1)
	require.Len(t, batches[0], 2)
	assert.ElementsMatch(t, []string{first, second}, []string{batches[0][0].FilePath, batches[0][1].FilePath})

	var result struct {
		CandidateFiles int `json:"candidate_files"`
		Created        int `json:"created"`
		Errors         int `json:"errors"`
	}
	require.NoError(t, json.Unmarshal([]byte(*summary.Result), &result))
	assert.Equal(t, 2, result.CandidateFiles)
	assert.Equal(t, 2, result.Created)
	assert.Zero(t, result.Errors)
}

func TestBackfillBookFilesSkipsBooksWithExistingRows(t *testing.T) {
	single := filepath.Join(t.TempDir(), "single.m4b")
	require.NoError(t, os.WriteFile(single, []byte("audio"), 0o600))
	books := []database.BookCore{{ID: "existing", FilePath: single}}
	var summary *database.OperationSummaryLog
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(int, int) ([]database.BookCore, error) { return books, nil },
		GetBookFilesFunc:    func(string) ([]database.BookFile, error) { return []database.BookFile{{ID: "already-there"}}, nil },
		BatchCreateBookFilesFunc: func([]*database.BookFile) error {
			t.Fatal("books with any existing rows must be skipped")
			return nil
		},
		SaveOperationSummaryLogFunc: func(got *database.OperationSummaryLog) error { summary = got; return nil },
	}
	job, _ := maintenance.Get("backfill-book-files")
	require.NoError(t, job.Run(maintenance.WithOperationID(context.Background(), "skip"), store, &noopReporter{}, false))

	var result struct {
		CandidateFiles int `json:"candidate_files"`
		Created        int `json:"created"`
		Errors         int `json:"errors"`
	}
	require.NoError(t, json.Unmarshal([]byte(*summary.Result), &result))
	assert.Zero(t, result.CandidateFiles)
	assert.Zero(t, result.Created)
	assert.Zero(t, result.Errors)
}

func TestBackfillBookFilesCountsEveryRowInAFailedBatch(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "first.mp3"), []byte("audio"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "second.m4b"), []byte("audio"), 0o600))
	books := []database.BookCore{{ID: "directory", FilePath: dir}}
	var summary *database.OperationSummaryLog
	store := &database.MockStore{
		GetAllBooksCoreFunc:      func(int, int) ([]database.BookCore, error) { return books, nil },
		BatchCreateBookFilesFunc: func([]*database.BookFile) error { return errors.New("write failed") },
		SaveOperationSummaryLogFunc: func(got *database.OperationSummaryLog) error {
			summary = got
			return nil
		},
	}
	job, _ := maintenance.Get("backfill-book-files")
	require.Error(t, job.Run(maintenance.WithOperationID(context.Background(), "failure"), store, &noopReporter{}, false))

	var result struct {
		CandidateFiles int `json:"candidate_files"`
		Created        int `json:"created"`
		Errors         int `json:"errors"`
	}
	require.NoError(t, json.Unmarshal([]byte(*summary.Result), &result))
	assert.Equal(t, 2, result.CandidateFiles)
	assert.Zero(t, result.Created)
	assert.Equal(t, 2, result.Errors)
}
