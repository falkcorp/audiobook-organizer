// file: internal/maintenance/jobs/bulk_deluge_import_test.go
// version: 1.6.0
// guid: ffab1405-9fb5-45b7-ac8a-8baf146cb3c3
// last-edited: 2026-10-11

// package jobs, not jobs_test: bulkDelugeImportJob is unexported, so the test
// has to live inside the package to construct it directly. That is also why
// this file carries its own reporter rather than reusing noopReporter from
// testhelpers_test.go, which is in the external jobs_test package.

package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

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

// The copy never overwrites a file already at the destination: a pending file
// outside the library lands at RootDir/<basename>, so a second pending file
// with the same basename, or a file a torrent seeds from the library root,
// sits exactly there. Until now the job opened the destination with os.Create
// and truncated it. A destination with other bytes now refuses the file with
// an error and is left as it was; one holding the file's own bytes (a copy an
// earlier run made and could not record) is adopted: the row is repointed to
// it without a rewrite.
func TestBulkDelugeImportJob_ExistingDestinationNeverOverwritten(t *testing.T) {
	t.Run("other bytes: refused, untouched", func(t *testing.T) {
		root, full := bdiFixture(t)
		dest := filepath.Join(root, "book.m4b")
		require.NoError(t, os.WriteFile(dest, []byte("another book's bytes"), 0o644))

		store, updated, _ := runBDI(t, full, func(string, string) (*database.BookFile, error) { return full, nil })

		require.Len(t, store.results, 1)
		assert.Equal(t, "error", store.results[0].Status)
		assert.Contains(t, bdiResultError(t, store.results[0]), "already exists with different content")
		got, err := os.ReadFile(dest)
		require.NoError(t, err)
		assert.Equal(t, "another book's bytes", string(got), "the file already at the destination was overwritten")
		assert.Nil(t, updated, "the row must not be repointed to a file that is not its copy")
	})
	// Same size, other bytes: only a content comparison refuses this one.
	t.Run("same size, other bytes: refused, untouched", func(t *testing.T) {
		root, full := bdiFixture(t)
		dest := filepath.Join(root, "book.m4b")
		require.NoError(t, os.WriteFile(dest, []byte("audiodatA"), 0o644)) // fixture source is "audiodata"

		store, updated, _ := runBDI(t, full, func(string, string) (*database.BookFile, error) { return full, nil })

		require.Len(t, store.results, 1)
		assert.Equal(t, "error", store.results[0].Status)
		assert.Contains(t, bdiResultError(t, store.results[0]), "already exists with different content")
		got, err := os.ReadFile(dest)
		require.NoError(t, err)
		assert.Equal(t, "audiodatA", string(got), "the file already at the destination was overwritten")
		assert.Nil(t, updated, "the row must not be repointed to a file that is not its copy")
	})
	t.Run("same bytes: adopted without a rewrite", func(t *testing.T) {
		root, full := bdiFixture(t)
		dest := filepath.Join(root, "book.m4b")
		require.NoError(t, os.WriteFile(dest, []byte("audiodata"), 0o644))
		old := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		require.NoError(t, os.Chtimes(dest, old, old))

		store, updated, _ := runBDI(t, full, func(string, string) (*database.BookFile, error) { return full, nil })

		require.Len(t, store.results, 1)
		assert.Equal(t, "imported", store.results[0].Status)
		require.NotNil(t, updated)
		assert.Equal(t, dest, updated.FilePath)
		info, err := os.Stat(dest)
		require.NoError(t, err)
		assert.True(t, info.ModTime().Equal(old), "an adopted destination was rewritten")
	})
}

// bdiPebbleStore is a real Pebble store that records operation results.
type bdiPebbleStore struct {
	*database.PebbleStore
	results []*database.OperationResult
}

func (s *bdiPebbleStore) CreateOperationResult(r *database.OperationResult) error {
	s.results = append(s.results, r)
	return nil
}

// TestBulkDelugeImportJob_LinkedRowMarkedInPlace: a row already at its library
// destination (deluge.link-backfill links exactly these: in the library, with
// a DelugeHash, ImportedFromDelugeAt unset) is marked imported in place --
// ImportedFromDelugeAt only, nothing copied -- reported already_in_library
// rather than imported, and leaves the pending list, so the next run does not
// list it again. Until review round 1 it was skipped unmarked and counted
// "imported" on every run. The "..Extras" directory is inside the root
// (pathutil.RelEscapesBase): the importer used to read it as outside and copy
// the file to the top of the root.
func TestBulkDelugeImportJob_LinkedRowMarkedInPlace(t *testing.T) {
	for _, dir := range []string{"Example Author", "..Extras"} {
		t.Run(dir, func(t *testing.T) {
			root := t.TempDir()
			origRoot, origMove := config.AppConfig.RootDir, config.AppConfig.DelugeMoveEnabled
			config.AppConfig.RootDir, config.AppConfig.DelugeMoveEnabled = root, false
			t.Cleanup(func() { config.AppConfig.RootDir, config.AppConfig.DelugeMoveEnabled = origRoot, origMove })

			ps, err := database.NewPebbleStore(t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { _ = ps.Close() })
			store := &bdiPebbleStore{PebbleStore: ps}
			book, err := ps.CreateBook(&database.Book{Title: "Example Book", FilePath: filepath.Join(root, dir)})
			require.NoError(t, err)
			path := filepath.Join(root, dir, "01.mp3")
			row := &database.BookFile{BookID: book.ID, FilePath: path, DelugeHash: "0123456789abcdef0123456789abcdef01234567", FileHash: "example-hash"}
			require.NoError(t, ps.CreateBookFile(row))
			pending, err := ps.GetBookFilesNeedingDelugeImportCore()
			require.NoError(t, err)
			require.Len(t, pending, 1, "fixture: the linked row is pending")

			ctx := maintenance.WithOperationID(context.Background(), bdiOpID)
			j := &bulkDelugeImportJob{}

			// Preview: says already_in_library, writes nothing.
			require.NoError(t, j.Run(ctx, store, &bdiReporter{}, true))
			require.Len(t, store.results, 1)
			assert.Contains(t, store.results[0].ResultJSON, `"action":"already_in_library"`)
			pending, err = ps.GetBookFilesNeedingDelugeImportCore()
			require.NoError(t, err)
			require.Len(t, pending, 1, "a preview must not mark the row")

			store.results = nil
			require.NoError(t, j.Run(ctx, store, &bdiReporter{}, false))
			require.Len(t, store.results, 1)
			assert.Equal(t, bdiStatusInPlace, store.results[0].Status)

			got, err := ps.GetBookFileByID(book.ID, row.ID)
			require.NoError(t, err)
			require.NotNil(t, got)
			assert.NotNil(t, got.ImportedFromDelugeAt)
			assert.Equal(t, path, got.FilePath)
			assert.Empty(t, got.DelugeOriginalPath)
			assert.Equal(t, row.DelugeHash, got.DelugeHash)
			assert.Equal(t, row.FileHash, got.FileHash)
			_, statErr := os.Stat(filepath.Join(root, "01.mp3"))
			assert.True(t, os.IsNotExist(statErr), "nothing may be copied to the top of the root")

			pending, err = ps.GetBookFilesNeedingDelugeImportCore()
			require.NoError(t, err)
			assert.Empty(t, pending, "the marked row must leave the pending list")

			reporter := &bdiReporter{}
			store.results = nil
			require.NoError(t, j.Run(ctx, store, reporter, false))
			assert.Equal(t, 0, reporter.total, "a second run lists nothing")
		})
	}
}

// bdiSeedingDeluge is a Deluge JSON-RPC endpoint with one torrent seeding from
// savePath: enough for deluge.ProtectedPathCache's refresh (auth.login, then
// core.get_torrents_status).
func bdiSeedingDeluge(t *testing.T, savePath string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			ID     int64  `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result any
		switch req.Method {
		case "auth.login":
			result = true
		case "core.get_torrents_status":
			result = map[string]any{"0123456789abcdef0123456789abcdef01234567": map[string]any{
				"hash": "0123456789abcdef0123456789abcdef01234567", "save_path": savePath,
			}}
		default:
			t.Errorf("unexpected Deluge call %q", req.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": req.ID, "result": result, "error": nil})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestBulkDelugeImportJob_InPlaceProtectedLeftPending: a pending row that is
// its own library destination but protected -- under config.ProtectedPaths,
// or under the save_path of a torrent seeding from inside the library -- is
// left pending with nothing written, as /discovery/import's
// ImportToLibraryWith refuses it (tagger.ErrProtectedPathWrite). The preview
// says "protected" with the same predicate, and the live run reports it
// under that status, not already_in_library or an error. While the Deluge
// list has never loaded the row is left pending the same way but reported
// protected_list_not_loaded: Deluge being down is no verdict on the file.
func TestBulkDelugeImportJob_InPlaceProtectedLeftPending(t *testing.T) {
	for _, tc := range []struct {
		name     string
		seeding  bool
		unloaded bool // Deluge configured but never reachable
	}{
		{name: "config protected path"},
		{name: "torrent seeding from the library", seeding: true},
		// The protected list never loaded, so no seeding file is known
		// unprotected: fail closed, preview and live alike.
		{name: "protected list not loaded", unloaded: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "Example Author")
			c := &config.AppConfig
			origRoot, origMove, origProt, origURL := c.RootDir, c.DelugeMoveEnabled, c.ProtectedPaths, c.DelugeWebURL
			origHost := c.DownloadClient.Torrent.Deluge.Host
			t.Cleanup(func() {
				c.RootDir, c.DelugeMoveEnabled, c.ProtectedPaths, c.DelugeWebURL = origRoot, origMove, origProt, origURL
				c.DownloadClient.Torrent.Deluge.Host = origHost
			})
			c.RootDir, c.DelugeMoveEnabled, c.ProtectedPaths, c.DelugeWebURL = root, false, nil, ""
			c.DownloadClient.Torrent.Deluge.Host = ""
			if tc.unloaded {
				down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					http.Error(w, "down", http.StatusServiceUnavailable)
				}))
				t.Cleanup(down.Close)
				c.DelugeWebURL = down.URL
			} else if tc.seeding {
				c.DelugeWebURL = bdiSeedingDeluge(t, dir)
			} else {
				c.ProtectedPaths = []string{dir}
			}

			ps, err := database.NewPebbleStore(t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { _ = ps.Close() })
			store := &bdiPebbleStore{PebbleStore: ps}
			book, err := ps.CreateBook(&database.Book{Title: "Example Book", FilePath: dir})
			require.NoError(t, err)
			row := &database.BookFile{BookID: book.ID, FilePath: filepath.Join(dir, "01.mp3"), DelugeHash: "0123456789abcdef0123456789abcdef01234567"}
			require.NoError(t, ps.CreateBookFile(row))

			ctx := maintenance.WithOperationID(context.Background(), bdiOpID)
			j := &bulkDelugeImportJob{}

			wantStatus := bdiStatusProtected
			if tc.unloaded {
				wantStatus = bdiStatusListNotLoaded
			}
			require.NoError(t, j.Run(ctx, store, &bdiReporter{}, true))
			require.Len(t, store.results, 1)
			assert.Contains(t, store.results[0].ResultJSON, `"action":"`+wantStatus+`"`)

			store.results = nil
			require.NoError(t, j.Run(ctx, store, &bdiReporter{}, false))
			require.Len(t, store.results, 1)
			assert.Equal(t, wantStatus, store.results[0].Status)

			got, err := ps.GetBookFileByID(book.ID, row.ID)
			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Nil(t, got.ImportedFromDelugeAt, "a protected row must not be marked imported")
			assert.Equal(t, row.FilePath, got.FilePath)
			pending, err := ps.GetBookFilesNeedingDelugeImportCore()
			require.NoError(t, err)
			assert.Len(t, pending, 1, "the protected row stays pending")
		})
	}
}

// bdiPebbleWorld is a Pebble store with RootDir pointed at a fresh library
// root and the Deluge globals cleared, so no test reaches a real Deluge.
func bdiPebbleWorld(t *testing.T) (root string, ps *database.PebbleStore, store *bdiPebbleStore) {
	t.Helper()
	root = t.TempDir()
	c := &config.AppConfig
	origRoot, origMove, origProt, origURL := c.RootDir, c.DelugeMoveEnabled, c.ProtectedPaths, c.DelugeWebURL
	origHost := c.DownloadClient.Torrent.Deluge.Host
	t.Cleanup(func() {
		c.RootDir, c.DelugeMoveEnabled, c.ProtectedPaths, c.DelugeWebURL = origRoot, origMove, origProt, origURL
		c.DownloadClient.Torrent.Deluge.Host = origHost
	})
	c.RootDir, c.DelugeMoveEnabled, c.ProtectedPaths, c.DelugeWebURL = root, false, nil, ""
	c.DownloadClient.Torrent.Deluge.Host = ""

	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	return root, ps, &bdiPebbleStore{PebbleStore: ps}
}

// TestBulkDelugeImportJob_SameBasenameSameBytesSecondRefused: two pending rows
// in two download directories outside the library, one basename, identical
// bytes. Both resolve to RootDir/<basename>. The first is copied there and
// repointed; the second must be refused, because that file is now the first
// row's. Until review round 3 the job called deluge.CopyIntoLibrary without
// the ownership check ImportToLibraryWith makes, so the second row adopted the
// first row's copy and two book_file rows named one file. No row is deleted:
// the refused one stays pending at its source.
func TestBulkDelugeImportJob_SameBasenameSameBytesSecondRefused(t *testing.T) {
	root, ps, store := bdiPebbleWorld(t)
	book, err := ps.CreateBook(&database.Book{Title: "Example Book", FilePath: root})
	require.NoError(t, err)
	var rows []*database.BookFile
	for i, dir := range []string{t.TempDir(), t.TempDir()} {
		src := filepath.Join(dir, "01.mp3")
		require.NoError(t, os.WriteFile(src, []byte("identical audio bytes"), 0o644))
		row := &database.BookFile{BookID: book.ID, FilePath: src, DelugeHash: fmt.Sprintf("%040d", i+1)}
		require.NoError(t, ps.CreateBookFile(row))
		rows = append(rows, row)
	}
	dest := filepath.Join(root, "01.mp3")

	ctx := maintenance.WithOperationID(context.Background(), bdiOpID)
	require.NoError(t, (&bulkDelugeImportJob{}).Run(ctx, store, &bdiReporter{}, false))

	require.Len(t, store.results, 2)
	statuses := map[string]int{}
	for _, r := range store.results {
		statuses[r.Status]++
	}
	assert.Equal(t, map[string]int{"imported": 1, "error": 1}, statuses)

	owner, err := ps.GetBookFileByPath(dest)
	require.NoError(t, err)
	require.NotNil(t, owner, "the first row must own the library copy")
	var first, second *database.BookFile
	for _, r := range rows {
		got, err := ps.GetBookFileByID(book.ID, r.ID)
		require.NoError(t, err)
		require.NotNil(t, got, "no book_file row may be deleted")
		if got.ID == owner.ID {
			first = got
		} else {
			second = got
		}
	}
	require.NotNil(t, first)
	require.NotNil(t, second)
	assert.Equal(t, dest, first.FilePath)
	assert.NotNil(t, first.ImportedFromDelugeAt)
	assert.NotEqual(t, dest, second.FilePath, "two rows name one file")
	assert.Nil(t, second.ImportedFromDelugeAt, "the refused row stays pending")
	assert.Empty(t, second.DelugeOriginalPath)
	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "identical audio bytes", string(got), "the first row's copy was disturbed")
	for _, r := range store.results {
		if r.Status == "error" {
			assert.Contains(t, bdiResultError(t, r), "already recorded for book file "+owner.ID)
		}
	}
}

// TestBulkDelugeImportJob_StaticProtectedSourceNotCopied: a source under a
// static protected prefix (config.ProtectedPaths here; the iTunes library is
// the same rule) is never copied out and repointed, as /discovery/import's
// ImportToLibraryWith refuses it. The preview says "protected", the live run
// reports it under that status, writes nothing and creates no file.
func TestBulkDelugeImportJob_StaticProtectedSourceNotCopied(t *testing.T) {
	root, ps, store := bdiPebbleWorld(t)
	srcDir := t.TempDir()
	config.AppConfig.ProtectedPaths = []string{srcDir}
	src := filepath.Join(srcDir, "01.mp3")
	require.NoError(t, os.WriteFile(src, []byte("audio"), 0o644))
	book, err := ps.CreateBook(&database.Book{Title: "Example Book", FilePath: srcDir})
	require.NoError(t, err)
	row := &database.BookFile{BookID: book.ID, FilePath: src, DelugeHash: "0123456789abcdef0123456789abcdef01234567"}
	require.NoError(t, ps.CreateBookFile(row))

	ctx := maintenance.WithOperationID(context.Background(), bdiOpID)
	j := &bulkDelugeImportJob{}
	require.NoError(t, j.Run(ctx, store, &bdiReporter{}, true))
	require.Len(t, store.results, 1)
	assert.Contains(t, store.results[0].ResultJSON, `"action":"protected"`)

	store.results = nil
	require.NoError(t, j.Run(ctx, store, &bdiReporter{}, false))
	require.Len(t, store.results, 1)
	assert.Equal(t, bdiStatusProtected, store.results[0].Status)
	got, err := ps.GetBookFileByID(book.ID, row.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, src, got.FilePath)
	assert.Nil(t, got.ImportedFromDelugeAt)
	_, statErr := os.Stat(filepath.Join(root, "01.mp3"))
	assert.True(t, os.IsNotExist(statErr), "a static protected source was copied into the library")
}

// TestBulkDelugeImportJob_UnloadedListProbedOnce: with Deluge configured but
// down, the run asks it for the protected list once, not twice per in-place
// row: three in-place rows cost the same Deluge requests as one.
func TestBulkDelugeImportJob_UnloadedListProbedOnce(t *testing.T) {
	hitsFor := func(t *testing.T, n int, dryRun bool) int64 {
		root, ps, store := bdiPebbleWorld(t)
		var hits atomic.Int64
		down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			http.Error(w, "down", http.StatusServiceUnavailable)
		}))
		t.Cleanup(down.Close)
		config.AppConfig.DelugeWebURL = down.URL
		dir := filepath.Join(root, "Example Author")
		book, err := ps.CreateBook(&database.Book{Title: "Example Book", FilePath: dir})
		require.NoError(t, err)
		for i := range n {
			row := &database.BookFile{BookID: book.ID, FilePath: filepath.Join(dir, fmt.Sprintf("%02d.mp3", i+1)), DelugeHash: fmt.Sprintf("%040d", i+1)}
			require.NoError(t, ps.CreateBookFile(row))
		}
		ctx := maintenance.WithOperationID(context.Background(), bdiOpID)
		require.NoError(t, (&bulkDelugeImportJob{}).Run(ctx, store, &bdiReporter{}, dryRun))
		require.Len(t, store.results, n)
		for _, r := range store.results {
			assert.Contains(t, r.Status+r.ResultJSON, bdiStatusListNotLoaded)
		}
		pending, err := ps.GetBookFilesNeedingDelugeImportCore()
		require.NoError(t, err)
		assert.Len(t, pending, n, "every row stays pending")
		return hits.Load()
	}
	for _, dry := range []bool{true, false} {
		t.Run(fmt.Sprintf("dry_run=%v", dry), func(t *testing.T) {
			one, three := hitsFor(t, 1, dry), hitsFor(t, 3, dry)
			assert.Positive(t, one, "fixture: the probe must reach the Deluge endpoint")
			assert.Equal(t, one, three, "Deluge requests grew with the in-place rows")
		})
	}
}
