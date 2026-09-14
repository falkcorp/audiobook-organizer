// file: internal/metafetch/rename_path_book_row_test.go
// version: 1.0.0
// guid: b4e81d6a-2f07-4c39-a5b3-8e1d0c7f9a25
// last-edited: 2026-09-14

package metafetch

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

var errInjectedBookWrite = errors.New("injected book row write failure")

// The book row's own FilePath write (persistRenamedBookPath) goes through the
// same retry and record as the book_file writes: a `:book` record with the
// directory's old and new paths, and the rename still fails.
func TestRename_BookRowPathWriteFailureRecordsBookRecord(t *testing.T) {
	shortRetry(t, 3, time.Millisecond)
	fx, svc := newRenameFixture(t)
	prefs := map[string]string{}
	mu := prefStore(svc, prefs)
	oldDir := fx.stored.FilePath
	fx.onBookWrite = func() error { return errInjectedBookWrite }

	st := fx.stored
	err := svc.RunApplyPipelineRenameOnly(context.Background(), "b1", &st)
	require.ErrorIs(t, err, errInjectedBookWrite)

	mu.Lock()
	defer mu.Unlock()
	raw := prefs[organizer.RenamePathWriteFailureKey("b1", "")]
	require.NotEmpty(t, raw, "no :book record; have %v", prefs)
	var rec organizer.RenamePathWriteFailure
	require.NoError(t, json.Unmarshal([]byte(raw), &rec))
	require.Equal(t, "b1", rec.BookID)
	require.Empty(t, rec.BookFileID)
	require.Equal(t, oldDir, rec.OldPath)
	require.Equal(t, filepath.Dir(fx.files[0].FilePath), rec.NewPath, "record must name the directory the files moved to")
	require.Contains(t, rec.Error, errInjectedBookWrite.Error())
	_, perr := time.Parse(time.RFC3339Nano, rec.RecordedAt)
	require.NoError(t, perr)
	// The book_file writes succeeded, so they left no record.
	require.Empty(t, prefs[organizer.RenamePathWriteFailureKey("b1", "f1")])
	require.Empty(t, prefs[organizer.RenamePathWriteFailureKey("b1", "f2")])
}
