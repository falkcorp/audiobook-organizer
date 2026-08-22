// file: internal/maintenance/jobs/bulk_deluge_import_test.go
// version: 1.0.0
// guid: ffab1405-9fb5-45b7-ac8a-8baf146cb3c3
// last-edited: 2026-08-21

// package jobs, not jobs_test: bulkDelugeImportJob is unexported, so the test
// has to live inside the package to construct it directly. That is also why
// this file carries its own reporter rather than reusing noopReporter from
// testhelpers_test.go, which is in the external jobs_test package.

package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

// bdiReporter is a maintenance.ProgressReporter that records what it was told.
type bdiReporter struct {
	total      int
	increments int
}

func (r *bdiReporter) SetTotal(n int)                    { r.total = n }
func (r *bdiReporter) Increment()                        { r.increments++ }
func (r *bdiReporter) Log(_ string, _ string, _ *string) {}

// bdiStore wraps database.MockStore to capture CreateOperationResult calls.
// MockStore's own CreateOperationResult is a hardcoded no-op with no Func
// field, so the override has to live here (same shape as sentinelStore in
// recompute_book_aggregates_sentinel_test.go).
type bdiStore struct {
	*database.MockStore
	results []*database.OperationResult
}

func (s *bdiStore) CreateOperationResult(r *database.OperationResult) error {
	s.results = append(s.results, r)
	return nil
}

const bdiOpID = "op-bulk-deluge-1"

// bdiFixture builds the one-pending-file world shared by the hydrate-failure
// and hydrate-success tests. Only GetBookFileByIDFunc differs between them, so
// a green happy-path run is the positive control proving the failure tests'
// "exactly one error result" is not satisfied by an empty pending list.
func bdiFixture(t *testing.T) (root string, full *database.BookFile) {
	t.Helper()

	root = t.TempDir()
	srcDir := t.TempDir() // outside root, so dest != src and the copy really runs
	srcPath := filepath.Join(srcDir, "book.m4b")
	require.NoError(t, os.WriteFile(srcPath, []byte("audiodata"), 0o644))

	origRoot := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	t.Cleanup(func() { config.AppConfig.RootDir = origRoot })

	// Pin DelugeMoveEnabled off rather than inheriting it from whatever ran
	// before: bdi_buildDelugeClient reads the global DelugeWebURL, so a
	// prior test leaving that set would give the success path a live client,
	// and this flag is the only thing standing between that and a real
	// MoveStorage POST on a 5-minute timeout.
	origMove := config.AppConfig.DelugeMoveEnabled
	config.AppConfig.DelugeMoveEnabled = false
	t.Cleanup(func() { config.AppConfig.DelugeMoveEnabled = origMove })

	full = &database.BookFile{
		ID:         "f1",
		BookID:     "b1",
		FilePath:   srcPath,
		DelugeHash: "abc123",
	}
	return root, full
}

// runBDI runs the job against a store whose hydrate call is supplied by the
// caller, and returns the captured operation results plus the BookFile handed
// to UpdateBookFile (nil when bdi_importToLibrary was never reached far enough
// to write back).
func runBDI(
	t *testing.T,
	full *database.BookFile,
	hydrate func(bookID, fileID string) (*database.BookFile, error),
) (*bdiStore, *database.BookFile, *bdiReporter) {
	t.Helper()

	var updated *database.BookFile
	store := &bdiStore{MockStore: &database.MockStore{
		// Returning no params leaves len(raw) == 0, so Run keeps the dryRun
		// argument it was passed (false) instead of unmarshalling over it.
		GetOperationParamsFunc: func(string) ([]byte, error) { return nil, nil },
		GetBookFilesNeedingDelugeImportCoreFunc: func() ([]database.BookFileCore, error) {
			return []database.BookFileCore{full.Core()}, nil
		},
		GetBookFileByIDFunc: hydrate,
		UpdateBookFileFunc: func(id string, file *database.BookFile) error {
			updated = file
			return nil
		},
	}}

	// The operation ID must be in the context: CreateOperationResult is gated
	// on opID != "", so without it the job records nothing and every
	// assertion below would run against an empty slice.
	ctx := maintenance.WithOperationID(context.Background(), bdiOpID)
	reporter := &bdiReporter{}

	j := &bulkDelugeImportJob{}
	require.NoError(t, j.Run(ctx, store, reporter, false))

	return store, updated, reporter
}

// bdiResultError pulls the "error" field out of a captured result's ResultJSON.
func bdiResultError(t *testing.T, r *database.OperationResult) string {
	t.Helper()
	var payload struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(r.ResultJSON), &payload))
	return payload.Error
}

// TestBulkDelugeImportJob_HydrateFailure pins the `hydrateErr != nil ||
// full == nil` guard at internal/maintenance/jobs/bulk_deluge_import.go:97.
// Both arms must record an error OperationResult and skip
// bdi_importToLibrary rather than silently dropping the file.
//
// Why the assertion is on the exact error STRING and not on a call count or
// a result count: if the guard is deleted, the nil BookFile flows into
// bdi_importToLibrary, whose own `bookFile == nil` check returns an error
// immediately. The unguarded path therefore produces the SAME
// one-result / Status "error" shape and never reaches UpdateBookFile either.
// The error text is the only signal that discriminates the guard from
// bdi_importToLibrary's nil check — do not "simplify" these assertions back
// to a count, it removes the coverage.
func TestBulkDelugeImportJob_HydrateFailure(t *testing.T) {
	hydrateErr := errors.New("pebble: get f1: io error")

	cases := []struct {
		name    string
		hydrate func(bookID, fileID string) (*database.BookFile, error)
		wantErr string
	}{
		{
			// Non-nil error variant: the store's own error text is surfaced.
			name: "hydrate returns an error",
			hydrate: func(string, string) (*database.BookFile, error) {
				return nil, hydrateErr
			},
			wantErr: hydrateErr.Error(),
		},
		{
			// Nil-error variant: a missing row with no error must still be
			// treated as a failure (the `|| full == nil` half of the guard).
			name: "hydrate returns nil without an error",
			hydrate: func(string, string) (*database.BookFile, error) {
				return nil, nil
			},
			wantErr: "book file not found",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, full := bdiFixture(t)
			store, updated, reporter := runBDI(t, full, tc.hydrate)

			assert.Equal(t, 1, reporter.total, "one file was pending")
			require.Len(t, store.results, 1, "the hydrate failure must be recorded, not dropped")
			assert.Equal(t, bdiOpID, store.results[0].OperationID)
			assert.Equal(t, "error", store.results[0].Status)
			// The discriminating assertion — see the doc comment above.
			assert.Equal(t, tc.wantErr, bdiResultError(t, store.results[0]))
			assert.Nil(t, updated, "bdi_importToLibrary must not write back on a hydrate failure")
		})
	}
}

// TestBulkDelugeImportJob_HydrateSuccess is the positive control for the test
// above: same fixture, same pending file, a working hydrate. It proves the
// pending list is non-empty and that the import path is reachable, so
// "exactly one error result" there cannot be passing vacuously.
func TestBulkDelugeImportJob_HydrateSuccess(t *testing.T) {
	root, full := bdiFixture(t)

	store, updated, reporter := runBDI(t, full, func(bookID, fileID string) (*database.BookFile, error) {
		if bookID == full.BookID && fileID == full.ID {
			return full, nil
		}
		return nil, nil
	})

	assert.Equal(t, 1, reporter.total)
	assert.Equal(t, 1, reporter.increments)
	require.Len(t, store.results, 1)
	assert.Equal(t, "imported", store.results[0].Status)

	var payload struct {
		NewPath string `json:"new_path"`
	}
	require.NoError(t, json.Unmarshal([]byte(store.results[0].ResultJSON), &payload))
	assert.Equal(t, filepath.Join(root, "book.m4b"), payload.NewPath)

	require.NotNil(t, updated, "a successful hydrate must reach bdi_importToLibrary's UpdateBookFile")
	assert.NotNil(t, updated.ImportedFromDelugeAt)
}
