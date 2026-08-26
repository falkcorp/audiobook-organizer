// file: internal/maintenance/jobs/backfill_book_files_test.go
// version: 1.0.0
// guid: 73490888-1dfc-4aa9-93a9-5d730a8d4f79
// last-edited: 2026-08-25

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

func TestBackfillBookFilesReportsAppliedWritesAndFailure(t *testing.T) {
	single := filepath.Join(t.TempDir(), "single.m4b")
	require.NoError(t, os.WriteFile(single, []byte("audio"), 0o600))
	books := []database.BookCore{{ID: "single", FilePath: single}}

	t.Run("applied", func(t *testing.T) {
		var rows []*database.BookFile
		var summary *database.OperationSummaryLog
		store := &database.MockStore{
			GetAllBooksCoreFunc:         func(int, int) ([]database.BookCore, error) { return books, nil },
			CreateBookFileFunc:          func(row *database.BookFile) error { rows = append(rows, row); return nil },
			SaveOperationSummaryLogFunc: func(got *database.OperationSummaryLog) error { summary = got; return nil },
		}
		job, _ := maintenance.Get("backfill-book-files")
		require.NoError(t, job.Run(maintenance.WithOperationID(context.Background(), "apply"), store, &noopReporter{}, false))
		require.Len(t, rows, 1)
		assert.Equal(t, single, rows[0].FilePath)
		var result struct {
			Created int `json:"created"`
			Errors  int `json:"errors"`
		}
		require.NoError(t, json.Unmarshal([]byte(*summary.Result), &result))
		assert.Equal(t, 1, result.Created)
		assert.Zero(t, result.Errors)
	})

	t.Run("write failure is visible", func(t *testing.T) {
		var summary *database.OperationSummaryLog
		store := &database.MockStore{
			GetAllBooksCoreFunc:         func(int, int) ([]database.BookCore, error) { return books, nil },
			CreateBookFileFunc:          func(*database.BookFile) error { return errors.New("write failed") },
			SaveOperationSummaryLogFunc: func(got *database.OperationSummaryLog) error { summary = got; return nil },
		}
		job, _ := maintenance.Get("backfill-book-files")
		require.Error(t, job.Run(maintenance.WithOperationID(context.Background(), "failure"), store, &noopReporter{}, false))
		require.NotNil(t, summary)
		var result struct {
			Errors int `json:"errors"`
		}
		require.NoError(t, json.Unmarshal([]byte(*summary.Result), &result))
		assert.Equal(t, 1, result.Errors)
	})
}
