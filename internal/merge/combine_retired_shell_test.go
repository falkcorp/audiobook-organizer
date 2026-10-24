// file: internal/merge/combine_retired_shell_test.go
// version: 1.0.0
// guid: 4d8b0c63-1e27-4a95-b3f8-6c19e7a02d54
// last-edited: 2026-10-28

package merge

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

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
