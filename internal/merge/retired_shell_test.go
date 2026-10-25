// file: internal/merge/retired_shell_test.go
// version: 1.1.1
// guid: 4d8b0c63-1e27-4a95-b3f8-6c19e7a02d54
// last-edited: 2026-10-24

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

	n, err := database.RetiredRowsHeldFor(s, "shell")
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
	require.NoError(t, s.UnretireBookFiles([]database.BookFileMove{{FileIDs: []string{rows[0].ID}, SourceBookID: "holder"}}, "shell", "rv-shell", nil))
	n, err = database.RetiredRowsHeldFor(s, "shell")
	require.NoError(t, err)
	require.Zero(t, n)
	res, err = RestoreFromTrash(s, "shell", nil, nil)
	require.NoError(t, err)
	require.True(t, res.Restored)
}

// A combine's undo restores every absorbed book whole. One that the retired-
// versions repair has since turned into a shell cannot come back: the undo is
// refused in its preconditions, before any write, and the journal stays
// applied. The control is the same combine undone while no absorbed book is a
// shell.
func TestUndoCombine_RefusesAnAbsorbedRetiredShell(t *testing.T) {
	for _, shell := range []bool{true, false} {
		name := map[bool]string{true: "absorbed book is a shell", false: "control: no shell"}[shell]
		t.Run(name, func(t *testing.T) {
			store := setupTestStore(t).(*database.PebbleStore)
			f := seedUndoFixture(t, store)
			ms := NewService(store)
			res, err := ms.CombineBooks([]string{f.survivor, f.absA, f.absB}, f.survivor, nil)
			require.NoError(t, err)

			if shell {
				// Its files moved to the survivor in the combine; a stray row it
				// still had is what the repair retired onto the survivor.
				require.NoError(t, store.CreateBookFile(&database.BookFile{ID: "stray-a", BookID: f.absB, FilePath: "/tmp/undo/b/stray.mp3"}))
				at := time.Now().UTC().Truncate(time.Microsecond)
				_, err = store.CreateBookVersion(&database.BookVersion{ID: "rv-b", BookID: f.survivor,
					Status: database.BookVersionStatusRetired, Source: "retired_book",
					RetiredFromBookID: f.absB, RetireOpID: "op-test", RetiredAt: &at})
				require.NoError(t, err)
				require.NoError(t, store.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{"stray-a"}, SourceBookID: f.absB}}, f.survivor, "rv-b"))
			}

			_, err = ms.UndoCombine(res.JournalID)
			if !shell {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.ErrorContains(t, err, "retired version")
			j, jerr := ms.GetCombineJournal(res.JournalID)
			require.NoError(t, jerr)
			require.Equal(t, CombineJournalApplied, j.Status, "a refused undo changes nothing")
			b, gerr := store.GetBookByID(f.absB)
			require.NoError(t, gerr)
			require.True(t, database.IsInTrash(b), "the shell was not restored")
		})
	}
}
