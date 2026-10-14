// file: internal/database/author_delete_cascade_test.go
// version: 1.1.0
// guid: 5f2b8d14-7c39-4a60-b1e8-0d6a3c9f2e57
// last-edited: 2026-10-14

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// DeleteAuthor cascades the author's aliases: the alias rows go, and so does the
// name each one claimed. The cascade used to read the alias id from the by-author
// index entry's VALUE (the alias JSON), got 0, and deleted only the index key.
func TestDeleteAuthor_CascadesItsAliasesAndFreesTheirNames(t *testing.T) {
	st, err := NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	st.WaitForWarmup()
	t.Cleanup(func() { _ = st.Close() })

	a, err := st.CreateAuthor("Cascade Author")
	require.NoError(t, err)
	_, err = st.CreateAuthorAlias(a.ID, "C. Author", "pen_name")
	require.NoError(t, err)

	require.NoError(t, st.DeleteAuthor(a.ID))

	rows, err := st.GetAuthorAliases(a.ID)
	require.NoError(t, err)
	require.Empty(t, rows)
	other, err := st.CreateAuthor("Cascade Other")
	require.NoError(t, err)
	_, err = st.CreateAuthorAlias(other.ID, "C. Author", "pen_name")
	require.NoError(t, err, "the deleted author's alias name is free again")
}

func TestRestoreAuthor_BringsAnAuthorBackUnderItsOwnIDAndRefusesATakenName(t *testing.T) {
	st := newAuthorTestStore(t)

	a, err := st.CreateAuthor("Restore Author")
	require.NoError(t, err)
	require.NoError(t, st.DeleteAuthor(a.ID))
	require.NoError(t, st.CreateAuthorTombstone(a.ID, 999))

	res, err := st.RestoreAuthor(a.ID, a.Name, AuthorRestoreOptions{OwnedNameIndex: true, DropTombstone: true})
	require.NoError(t, err)
	require.True(t, res.Restored)
	got, err := st.GetAuthorByID(a.ID)
	require.NoError(t, err)
	require.Equal(t, a.ID, got.ID)
	byName, err := st.GetAuthorByName(a.Name)
	require.NoError(t, err)
	require.Equal(t, a.ID, byName.ID)
	tomb, err := st.GetAuthorTombstone(a.ID)
	require.NoError(t, err)
	require.Zero(t, tomb)

	// A live row of the id: the row is left alone, not restored again.
	again, err := st.RestoreAuthor(a.ID, a.Name, AuthorRestoreOptions{OwnedNameIndex: true})
	require.NoError(t, err)
	require.False(t, again.Restored)

	// Another author holds the name: refused.
	require.NoError(t, st.DeleteAuthor(a.ID))
	squatter, err := st.CreateAuthor(a.Name)
	require.NoError(t, err)
	require.NotEqual(t, a.ID, squatter.ID)
	_, err = st.RestoreAuthor(a.ID, a.Name, AuthorRestoreOptions{OwnedNameIndex: true})
	require.ErrorIs(t, err, ErrAuthorNameTaken)
}

func newAuthorTestStore(t *testing.T) *PebbleStore {
	t.Helper()
	st, err := NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	st.WaitForWarmup()
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// A merged author's pen name keeps resolving, to the author it was merged into.
// DeleteAuthor cascades aliases, so a merge that called DeleteAuthor and then
// CreateAuthorTombstone (author-strip-merge, the AI dedup apply) resolved the pen
// name to nobody. MergeDeleteAuthor moves the alias instead.
func TestMergeDeleteAuthor_AliasOfMergedAuthorResolvesToTheSurvivor(t *testing.T) {
	st := newAuthorTestStore(t)
	from, err := st.CreateAuthor("Probe From")
	require.NoError(t, err)
	into, err := st.CreateAuthor("Probe Into")
	require.NoError(t, err)
	al, err := st.CreateAuthorAlias(from.ID, "P. Pen", "pen_name")
	require.NoError(t, err)

	res, err := st.MergeDeleteAuthor(from.ID, into.ID)
	require.NoError(t, err)
	require.True(t, res.OwnedNameIndex)
	require.Len(t, res.MovedAliases, 1)
	require.Equal(t, al.ID, res.MovedAliases[0].ID, "the alias keeps its id")
	require.Equal(t, into.ID, res.MovedAliases[0].AuthorID)

	got, err := st.FindAuthorByAlias("P. Pen")
	require.NoError(t, err)
	require.NotNil(t, got, "the pen name resolves to somebody")
	require.Equal(t, into.ID, got.ID)
	tomb, err := st.GetAuthorTombstone(from.ID)
	require.NoError(t, err)
	require.Equal(t, into.ID, tomb, "the redirect is written with the delete")
	onInto, err := st.GetAuthorAliases(into.ID)
	require.NoError(t, err)
	require.Len(t, onInto, 1)
	onFrom, err := st.GetAuthorAliases(from.ID)
	require.NoError(t, err)
	require.Empty(t, onFrom)
	gone, err := st.GetAuthorByName("Probe From")
	require.NoError(t, err)
	require.Nil(t, gone, "the name entry was the merged author's and went with it")

	_, err = st.MergeDeleteAuthor(from.ID, into.ID)
	require.ErrorIs(t, err, ErrAuthorNotLive, "an author merged already is not merged again")
	other, err := st.CreateAuthor("Probe Other")
	require.NoError(t, err)
	_, err = st.MergeDeleteAuthor(other.ID, from.ID)
	require.ErrorIs(t, err, ErrAuthorNotLive, "the target must be live")
}

// The undo of a merge puts everything back: the row, the name entry it held, the
// aliases (moved back under their ids), and a redirect that pointed at it and was
// flattened onto the survivor while the merge stood (ResolveTombstoneChains).
func TestRestoreAuthor_UndoesAMergeDelete(t *testing.T) {
	st := newAuthorTestStore(t)
	older, err := st.CreateAuthor("Chain Older")
	require.NoError(t, err)
	from, err := st.CreateAuthor("Chain From")
	require.NoError(t, err)
	into, err := st.CreateAuthor("Chain Into")
	require.NoError(t, err)
	al, err := st.CreateAuthorAlias(from.ID, "C. From", "pen_name")
	require.NoError(t, err)
	_, err = st.MergeDeleteAuthor(older.ID, from.ID)
	require.NoError(t, err)

	res, err := st.MergeDeleteAuthor(from.ID, into.ID)
	require.NoError(t, err)
	require.Equal(t, []int{older.ID}, res.InboundTombstones)
	n, err := st.ResolveTombstoneChains()
	require.NoError(t, err)
	require.Equal(t, 1, n)

	opts := AuthorRestoreOptions{MergedInto: into.ID, OwnedNameIndex: res.OwnedNameIndex,
		AliasIDs: []int{al.ID}, InboundTombstones: res.InboundTombstones, DropTombstone: true}
	out, err := st.RestoreAuthor(from.ID, from.Name, opts)
	require.NoError(t, err)
	require.True(t, out.Restored)
	require.Empty(t, out.AliasesNotBack)

	byName, err := st.GetAuthorByName(from.Name)
	require.NoError(t, err)
	require.Equal(t, from.ID, byName.ID)
	byAlias, err := st.FindAuthorByAlias("C. From")
	require.NoError(t, err)
	require.Equal(t, from.ID, byAlias.ID)
	viaOlder, err := st.GetAuthorByID(older.ID)
	require.NoError(t, err)
	require.Equal(t, from.ID, viaOlder.ID, "the flattened redirect points at the restored author again")
	onInto, err := st.GetAuthorAliases(into.ID)
	require.NoError(t, err)
	require.Empty(t, onInto)

	// A retry finishes what a stopped run left: idempotent, the row left alone.
	again, err := st.RestoreAuthor(from.ID, from.Name, opts)
	require.NoError(t, err)
	require.False(t, again.Restored)
	require.Empty(t, again.AliasesNotBack)
}

// Names that normalize the same share one index entry. A merged author that did
// not hold it comes back without it, and the holder is not "a taken name".
func TestAuthorNameIndexOnRestore(t *testing.T) {
	cases := []struct {
		name                       string
		owned                      bool
		holder, restored, mergedTo int
		write                      bool
		taken                      bool
	}{
		{"did not own, survivor holds", false, 2, 1, 2, false, false},
		{"did not own, a third holds", false, 3, 1, 2, false, false},
		{"owned, free", true, 0, 1, 2, true, false},
		{"owned, itself", true, 1, 1, 2, true, false},
		{"owned, survivor holds now", true, 2, 1, 2, false, false},
		{"owned, a third holds now", true, 3, 1, 2, false, true},
		{"owned, reclassify, another holds", true, 3, 1, 0, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			write, err := AuthorNameIndexOnRestore(c.owned, c.holder, c.restored, c.mergedTo)
			require.Equal(t, c.write, write)
			if c.taken {
				require.ErrorIs(t, err, ErrAuthorNameTaken)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
