// file: internal/batch/hard_delete_state_test.go
// version: 1.0.0
// guid: 3ef207f6-91b5-4980-9272-904c924edc09
// last-edited: 2026-10-05

package batch

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A: the batch hard delete carries a book's users' listening state to the
// version Audiobookshelf lists and then deletes, refuses when there is no
// such version (the book and its state stay), and deletes a book with no
// state as before.
func TestExecuteOperations_HardDeleteCarriesOrRefuses(t *testing.T) {
	s, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.WaitForWarmup()
	gid := "g1"
	mk := func(id string, listed bool, group *string) {
		b := &database.Book{ID: id, Title: id, Format: "m4b", VersionGroupID: group}
		if listed {
			b.IsPrimaryVersion, b.LibraryState = new(true), new("organized")
		} else {
			b.IsPrimaryVersion, b.LibraryState = new(false), new("imported")
		}
		_, err := s.CreateBook(b)
		require.NoError(t, err)
	}
	mk("keep", true, &gid)
	mk("copy", false, &gid)
	mk("alone", false, nil)
	mk("plain", false, nil)
	u, err := s.CreateUser("reader", "reader@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)
	for _, id := range []string{"copy", "alone"} {
		require.NoError(t, s.SetUserBookState(&database.UserBookState{UserID: u.ID, BookID: id, Status: database.UserBookStatusInProgress, ProgressPct: 40, LastActivityAt: time.Now()}))
	}

	resp := NewBatchService(s).ExecuteOperations(&BatchOperationsRequest{Operations: []BatchOperationItem{
		{ID: "copy", Action: "delete", HardDelete: true},
		{ID: "alone", Action: "delete", HardDelete: true},
		{ID: "plain", Action: "delete", HardDelete: true},
	}})
	require.Equal(t, 2, resp.Success, "results %+v", resp.Results)
	require.Equal(t, 1, resp.Failed)

	gone := func(id string) bool { b, _ := s.GetBookByID(id); return b == nil }
	require.True(t, gone("copy"))
	require.True(t, gone("plain"))
	require.False(t, gone("alone"), "a book with progress and no listed copy is kept")
	st, err := s.GetUserBookState(u.ID, "keep")
	require.NoError(t, err)
	require.NotNil(t, st)
	require.Equal(t, 40, st.ProgressPct, "the copy's progress moved to the listed version")
	st, err = s.GetUserBookState(u.ID, "alone")
	require.NoError(t, err)
	require.Equal(t, 40, st.ProgressPct, "the refused book keeps its progress")
}
