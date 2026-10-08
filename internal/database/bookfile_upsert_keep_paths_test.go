// file: internal/database/bookfile_upsert_keep_paths_test.go
// version: 1.0.0
// guid: 1f8d3c6b-7a2e-4b9d-a5c0-8e4f2b6d9a31
// last-edited: 2026-10-07

package database

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// BatchUpsertBookFilesKeepPaths: a matched row keeps its stored FilePath and
// Format (by PID and by in-batch duplicate), records the incoming ITunesPath,
// and an unmatched row is created at its incoming path. BatchUpsertBookFiles
// is pinned alongside so the difference is the option, not the fixture.
func TestBatchUpsertBookFilesKeepPaths(t *testing.T) {
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	book, err := store.CreateBook(&Book{Title: "B", FilePath: "/lib/B"})
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&BookFile{
		BookID: book.ID, FilePath: "/lib/B/01.m4b", Format: "m4b", ITunesPersistentID: "PID1",
	}))

	incoming := func(pid, path string) *BookFile {
		return &BookFile{
			BookID: book.ID, FilePath: path, Format: "mp3", ITunesPersistentID: pid,
			ITunesPath: "file:///itunes/" + pid, Title: "t-" + pid,
		}
	}
	require.NoError(t, store.BatchUpsertBookFilesKeepPaths([]*BookFile{
		incoming("PID1", "/itunes/01.mp3"),
		incoming("PID1", "/itunes/01-again.mp3"), // in-batch duplicate of the same row
		incoming("PID2", "/itunes/02.mp3"),
	}))

	got, err := store.GetBookFileByPID("PID1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "/lib/B/01.m4b", got.FilePath)
	assert.Equal(t, "m4b", got.Format)
	assert.Equal(t, "file:///itunes/PID1", got.ITunesPath)
	assert.Equal(t, "t-PID1", got.Title)

	created, err := store.GetBookFileByPID("PID2")
	require.NoError(t, err)
	require.NotNil(t, created)
	assert.Equal(t, "/itunes/02.mp3", created.FilePath)

	files, err := store.GetBookFiles(book.ID)
	require.NoError(t, err)
	assert.Len(t, files, 2)

	// The plain upsert still treats FilePath as caller-owned.
	require.NoError(t, store.BatchUpsertBookFiles([]*BookFile{incoming("PID1", "/elsewhere/01.mp3")}))
	got, err = store.GetBookFileByPID("PID1")
	require.NoError(t, err)
	assert.Equal(t, "/elsewhere/01.mp3", got.FilePath)
}
