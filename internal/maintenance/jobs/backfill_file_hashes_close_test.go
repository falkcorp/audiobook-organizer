// file: internal/maintenance/jobs/backfill_file_hashes_close_test.go
// version: 1.0.0
// guid: 0c6d4a82-91e7-4b35-8f2a-d3a7b5e1c649
// last-edited: 2026-10-26

package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failingCloseFile is a real report file whose Close reports an error after
// closing it, the way a full or failing disk can at the very end of a run.
type failingCloseFile struct{ hbFile }

func (f failingCloseFile) Close() error {
	_ = f.hbFile.Close()
	return errors.New("close: input/output error")
}

type closeTestReporter struct{ logs []string }

func (r *closeTestReporter) SetTotal(int)                        {}
func (r *closeTestReporter) Increment()                          {}
func (r *closeTestReporter) Log(_ string, msg string, _ *string) { r.logs = append(r.logs, msg) }

// If closing the report fails after every chunk was written, the op fails and
// says nothing else: no "completed" summary is saved and the checkpoint is
// kept, so the status and the summary cannot disagree.
func TestBackfillFileHashes_CloseFailureAfterSuccessFailsTheWholeRun(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "Gotham"), 0o755))
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	prevOpen := hbOpenFile
	hbOpenFile = func(name string, flag int, perm os.FileMode) (hbFile, error) {
		f, err := prevOpen(name, flag, perm)
		if err != nil {
			return nil, err
		}
		return failingCloseFile{f}, nil
	}
	t.Cleanup(func() { hbOpenFile = prevOpen })

	state := map[string][]byte{}
	var summaries []*database.OperationSummaryLog
	st := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
			return []database.BookFileCore{{ID: "f1", BookID: "b1", FilePath: filepath.Join(root, "Gotham", "Year One.m4b")}}, nil
		},
		SaveOperationStateFunc:   func(id string, data []byte) error { state[id] = data; return nil },
		GetOperationStateFunc:    func(id string) ([]byte, error) { return state[id], nil },
		DeleteOperationStateFunc: func(id string) error { delete(state, id); return nil },
		SaveOperationSummaryLogFunc: func(op *database.OperationSummaryLog) error {
			summaries = append(summaries, op)
			return nil
		},
	}
	const opID = "01CLOSEFAILS"
	rep := &closeTestReporter{}

	err := (&backfillFileHashesJob{}).Run(maintenance.WithOperationID(context.Background(), opID), st, rep, true)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "input/output error")
	assert.Empty(t, summaries, "no completed summary for a run that returns an error")
	assert.Contains(t, state, opID, "the checkpoint is kept")
	for _, l := range rep.logs {
		assert.NotContains(t, l, "hashed=", "no result line either")
	}
}
