// file: internal/dedup/unmerge_auto_retired_shell_test.go
// version: 1.0.0
// guid: 3e9b5c71-a204-4d68-b1f7-82c0d6e4a915
// last-edited: 2026-10-24

package dedup

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// UnmergeAuto refuses, before any write, an entry whose loser has since been
// retired as a shell: the whole-row revert would un-trash it and clear its
// merged-into link while its files stay retired under the winner. The sibling
// undo (its first write) is not run either.
func TestUnmergeAuto_RefusesARetiredShellLoser(t *testing.T) {
	engine, store, es := setupRealStoreEngine(t)
	yes, no := true, false
	seedGrouped(t, store, "K", "H", &yes)
	seedGrouped(t, store, "L", "G", &no)
	seedGrouped(t, store, "LS", "G", &yes)

	_, keys, err := engine.MergeBooksJournaled(0, []string{"L", "K"}, "K", "dedup:merge-source:test")
	require.NoError(t, err)
	require.Len(t, keys, 1)

	// The retire repair then moved L's file row onto the winner K.
	ps := store.(*database.PebbleStore)
	require.NoError(t, ps.CreateBookFile(&database.BookFile{ID: "lf", BookID: "L", FilePath: "/lib/L/a.mp3"}))
	at := time.Now().UTC().Truncate(time.Microsecond)
	_, err = ps.CreateBookVersion(&database.BookVersion{ID: "rv-L", BookID: "K", Status: database.BookVersionStatusRetired,
		Source: "retired_book", RetiredFromBookID: "L", RetireOpID: "op-x", RetiredAt: &at})
	require.NoError(t, err)
	require.NoError(t, ps.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{"lf"}, SourceBookID: "L"}}, "K", "rv-L"))

	err = engine.UnmergeAuto(keys[0])
	require.ErrorIs(t, err, database.ErrBookIsRetiredShell)
	require.ErrorContains(t, err, "nothing reverted")

	g, _, deleted := groupAndFlag(t, store, "L")
	require.True(t, deleted, "the loser stays in the trash")
	require.Equal(t, "H", g, "the loser keeps its merged group")
	g, _, _ = groupAndFlag(t, store, "LS")
	require.Equal(t, "H", g, "the sibling undo did not run")
	entry, err := es.GetAutoMergeJournalEntry(keys[0])
	require.NoError(t, err)
	require.Zero(t, entry.UndoneAt, "the entry is not marked undone")
}
