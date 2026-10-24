// file: internal/merge/retired_shell_test.go
// version: 1.0.0
// guid: 4d8b0c63-1e27-4a95-b3f8-6c19e7a02d54
// last-edited: 2026-10-23

package merge

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A trashed book whose file rows were retired onto another book is refused a
// restore (it would come back as an empty imported book), a plain trashed
// book is restored, and a shell whose versions no longer hold rows is
// restorable again.
func TestRestoreFromTrash_RefusesARetireShell(t *testing.T) {
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

	n, err := RetiredRowsHeldFor(s, "shell")
	require.NoError(t, err)
	require.Equal(t, 1, n)

	res, err := RestoreFromTrash(s, "shell", nil, nil)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrBookIsRetiredShell), "%v", err)
	require.False(t, res.Restored)
	b, err := s.GetBookByID("shell")
	require.NoError(t, err)
	require.True(t, database.IsInTrash(b), "the refused restore must leave the shell in the trash")

	res, err = RestoreFromTrash(s, "plain", nil, nil)
	require.NoError(t, err)
	require.True(t, res.Restored, "control: an ordinary trashed book still restores")

	// A shell whose retired rows went back (unretire) holds nothing.
	require.NoError(t, s.UnretireBookFiles([]database.BookFileMove{{FileIDs: []string{rows[0].ID}, SourceBookID: "shell"}}, "shell", "rv-shell", nil))
	n, err = RetiredRowsHeldFor(s, "shell")
	require.NoError(t, err)
	require.Zero(t, n)
	res, err = RestoreFromTrash(s, "shell", nil, nil)
	require.NoError(t, err)
	require.True(t, res.Restored)
}
