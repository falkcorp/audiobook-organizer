// file: internal/itunes/service/retired_pid_test.go
// version: 1.0.0
// guid: 8e2d4b71-6a39-4c05-9f1e-5b7a03c82d94
// last-edited: 2026-10-23

package itunesservice

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

// pidRows answers GetBookFileByPID from a PID -> row table, like the store's
// single-owner PID key: a live row owns the key over a retired one, so a table
// entry is whichever row would hold it.
func pidRows(rows map[string]*database.BookFile) *database.MockStore {
	m := &database.MockStore{}
	m.GetBookFileByPIDFunc = func(pid string) (*database.BookFile, error) { return rows[pid], nil }
	return m
}

func twoTrackGroup(pids ...string) albumGroup {
	g := albumGroup{key: "Album"}
	for _, p := range pids {
		g.tracks = append(g.tracks, &itunes.Track{PersistentID: p})
	}
	return g
}

// A track whose PID only a retired row holds is a known retired version of
// that row's holder. The import is refused (a skip, not a failure) so it
// creates no second book and writes nothing onto the holder; the holder is not
// even loaded, since nothing may be linked to it.
func TestFindExistingImportTarget_RetiredOnlyPIDIsRefusedNotCreatedOrLinked(t *testing.T) {
	var bookReads atomic.Int32
	m := pidRows(map[string]*database.BookFile{
		"PID-RET": {ID: "f1", BookID: "HOLDER01", Retired: true, ITunesPersistentID: "PID-RET"},
	})
	m.GetBookByIDFunc = func(string) (*database.Book, error) { bookReads.Add(1); return nil, nil }
	imp := newMockImporter(m)

	id, err := imp.findExistingImportTarget(&database.Book{Title: "Retired Album"}, twoTrackGroup("PID-NEW", "PID-RET"))

	require.Empty(t, id, "no book to link to")
	refused, ok := errors.AsType[*importTargetRefusedError](err)
	require.True(t, ok, "want an importTargetRefusedError (a skip), got %v", err)
	require.Contains(t, refused.Error(), "HOLDER01")
	require.Contains(t, refused.Error(), "retired version")
	require.Zero(t, bookReads.Load(), "the holder must not be loaded for linking")
}

// A live row for the same album wins over a retired row: an album with one track
// retired into another book and the rest live is ordinary, and the live book is
// the link target.
func TestFindExistingImportTarget_LiveMatchWinsOverRetiredTrack(t *testing.T) {
	m := pidRows(map[string]*database.BookFile{
		"PID-LIVE": {ID: "f1", BookID: "LIVEBOOK", ITunesPersistentID: "PID-LIVE"},
		"PID-RET":  {ID: "f2", BookID: "HOLDER01", Retired: true, ITunesPersistentID: "PID-RET"},
	})
	m.GetBookByIDFunc = func(id string) (*database.Book, error) { return &database.Book{ID: id}, nil }
	imp := newMockImporter(m)

	id, err := imp.findExistingImportTarget(&database.Book{Title: "Mixed Album"}, twoTrackGroup("PID-LIVE", "PID-RET"))
	require.NoError(t, err)
	require.Equal(t, "LIVEBOOK", id)
}

// A failed lookup is a failure, not "no match" and not a refusal.
func TestFindExistingImportTarget_PIDLookupErrorIsAFailure(t *testing.T) {
	m := &database.MockStore{}
	m.GetBookFileByPIDFunc = func(string) (*database.BookFile, error) { return nil, errors.New("store down") }
	imp := newMockImporter(m)

	_, err := imp.findExistingImportTarget(&database.Book{Title: "Album"}, twoTrackGroup("PID-1"))
	require.Error(t, err)
	_, refused := errors.AsType[*importTargetRefusedError](err)
	require.False(t, refused)
	require.Contains(t, err.Error(), "PID-1")
}

// retiredPIDHolder names the holder of a retired-only PID and answers "" for a
// live PID and for an unknown one: the external-ID shortcut links only then.
func TestRetiredPIDHolder(t *testing.T) {
	imp := newMockImporter(pidRows(map[string]*database.BookFile{
		"PID-RET":  {ID: "f1", BookID: "HOLDER01", Retired: true},
		"PID-LIVE": {ID: "f2", BookID: "LIVEBOOK"},
	}))
	for pid, want := range map[string]string{"PID-RET": "HOLDER01", "PID-LIVE": "", "PID-NONE": ""} {
		got, err := imp.retiredPIDHolder(pid)
		require.NoError(t, err)
		require.Equal(t, want, got, "pid %s", pid)
	}

	failing := &database.MockStore{}
	failing.GetBookFileByPIDFunc = func(string) (*database.BookFile, error) { return nil, errors.New("boom") }
	_, err := newMockImporter(failing).retiredPIDHolder("PID-RET")
	require.Error(t, err)
}
