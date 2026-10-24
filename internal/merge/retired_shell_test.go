// file: internal/merge/retired_shell_test.go
// version: 1.0.1
// guid: 4d8b0c63-1e27-4a95-b3f8-6c19e7a02d54
// last-edited: 2026-10-28

package merge

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// RefuseRetiredShell refuses a trashed book whose file rows were retired onto
// another book, for every action it is asked about; it passes a plain trashed
// book and a shell whose versions no longer hold rows. (The restore path's own
// use of it is covered by trash_restore_retired_test.go.)
func TestRefuseRetiredShell(t *testing.T) {
	s, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.WaitForWarmup()
	yes, at := true, time.Now().Add(-48*time.Hour)
	for _, id := range []string{"holder", "shell", "plain"} {
		b := &database.Book{ID: id, Title: id, FilePath: "/lib/" + id}
		if id != "holder" {
			b.MarkedForDeletion, b.MarkedForDeletionAt = &yes, &at
		}
		_, err := s.CreateBook(b)
		require.NoError(t, err)
	}
	require.NoError(t, s.CreateBookFile(&database.BookFile{ID: "shell-f", BookID: "shell", FilePath: "/lib/shell/a.mp3"}))
	rows, err := s.GetBookFiles("shell")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	ra := at.UTC().Truncate(time.Microsecond)
	_, err = s.CreateBookVersion(&database.BookVersion{ID: "rv-shell", BookID: "holder", Status: database.BookVersionStatusRetired,
		Source: "retired_book", RetiredFromBookID: "shell", RetireOpID: "op-test", RetiredAt: &ra})
	require.NoError(t, err)
	// The holder is live while rows are retired onto it.
	require.NoError(t, s.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{rows[0].ID}, SourceBookID: "shell"}}, "holder", "rv-shell"))

	for _, action := range []string{"restore", "purge", "discard progress and purge", "hard delete"} {
		err := RefuseRetiredShell(s, "shell", action)
		require.Error(t, err, action)
		require.True(t, errors.Is(err, database.ErrBookIsRetiredShell), "%s: %v", action, err)
		require.Contains(t, err.Error(), action)
	}
	require.NoError(t, RefuseRetiredShell(s, "plain", "purge"), "control: an ordinary trashed book is not a shell")
	require.NoError(t, RefuseRetiredShell(s, "holder", "purge"), "control: the holder is not a shell (CheckBookOwnsNoFiles refuses it)")

	// A store that cannot read retired rows fails closed, and the failure is
	// not mistaken for "this is a shell".
	err = RefuseRetiredShell(struct{}{}, "shell", "purge")
	require.Error(t, err)
	require.False(t, errors.Is(err, database.ErrBookIsRetiredShell), "%v", err)
	require.True(t, errors.Is(err, database.ErrRetiredReaderUnavailable), "%v", err)

	// A shell whose retired rows went back (unretire) holds nothing.
	require.NoError(t, s.UnretireBookFiles([]database.BookFileMove{{FileIDs: []string{rows[0].ID}, SourceBookID: "holder"}}, "shell", "rv-shell", nil))
	require.NoError(t, RefuseRetiredShell(s, "shell", "purge"))
}
