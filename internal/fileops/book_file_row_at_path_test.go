// file: internal/fileops/book_file_row_at_path_test.go
// version: 1.0.0
// guid: 7c3e0d58-1a94-4f26-b6d9-a58e2f7c1b03
// last-edited: 2026-10-26

package fileops

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// opaqueRecorder answers only the live-only path read and declares nothing
// about retired rows, like a store that cannot be asked about them.
type opaqueRecorder struct {
	live map[string]*database.BookFile
}

func (r *opaqueRecorder) GetBookFileByPath(p string) (*database.BookFile, error) {
	return r.live[p], nil
}
func (r *opaqueRecorder) UpdateBookFileHashes(string, string, string, string) error { return nil }

// retiredAwareRecorder reads like the production store: the plain getter
// answers nil for a path only a retired row holds, and the retired-aware read
// names it. The embedded reader is nil; only the path read is called.
type retiredAwareRecorder struct {
	opaqueRecorder
	database.RetiredBookFileReader
	retired map[string]*database.BookFile
}

func (r *retiredAwareRecorder) GetBookFileByPathIncludingRetired(p string) (*database.BookFile, error) {
	if row := r.live[p]; row != nil {
		return row, nil
	}
	return r.retired[p], nil
}

func TestBookFileRowAtPath_FindsARetiredRow(t *testing.T) {
	st := &retiredAwareRecorder{retired: map[string]*database.BookFile{"/lib/a.m4b": {ID: "ret", Retired: true}}}
	row, err := BookFileRowAtPath(st, "/lib/a.m4b")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Equal(t, "ret", row.ID)

	opts := HashOptionsForPath(st, "/lib/a.m4b")
	require.Equal(t, "ret", opts.BookFileID, "a rewrite's hashes go on the retired row that holds the path")
	require.NotNil(t, opts.Store)
}

// A store that cannot read retired rows still records on the live row: the
// recording is best effort and must not stop working for it.
func TestBookFileRowAtPath_OpaqueStoreFallsBackToTheLiveRow(t *testing.T) {
	st := &opaqueRecorder{live: map[string]*database.BookFile{"/lib/b.m4b": {ID: "live"}}}
	row, err := BookFileRowAtPath(st, "/lib/b.m4b")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Equal(t, "live", row.ID)
	require.Equal(t, "live", HashOptionsForPath(st, "/lib/b.m4b").BookFileID)

	none, err := BookFileRowAtPath(st, "/lib/none.m4b")
	require.NoError(t, err)
	require.Nil(t, none)
}
