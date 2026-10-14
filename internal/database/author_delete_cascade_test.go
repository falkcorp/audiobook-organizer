// file: internal/database/author_delete_cascade_test.go
// version: 1.0.0
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
	st, err := NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	st.WaitForWarmup()
	t.Cleanup(func() { _ = st.Close() })

	a, err := st.CreateAuthor("Restore Author")
	require.NoError(t, err)
	require.NoError(t, st.DeleteAuthor(a.ID))
	require.NoError(t, st.CreateAuthorTombstone(a.ID, 999))

	restored, err := st.RestoreAuthor(a.ID, a.Name)
	require.NoError(t, err)
	require.True(t, restored)
	require.NoError(t, st.DeleteAuthorTombstone(a.ID))
	got, err := st.GetAuthorByID(a.ID)
	require.NoError(t, err)
	require.Equal(t, a.ID, got.ID)
	byName, err := st.GetAuthorByName(a.Name)
	require.NoError(t, err)
	require.Equal(t, a.ID, byName.ID)
	tomb, err := st.GetAuthorTombstone(a.ID)
	require.NoError(t, err)
	require.Zero(t, tomb)

	// A live row of the id: nothing written, not restored again.
	again, err := st.RestoreAuthor(a.ID, a.Name)
	require.NoError(t, err)
	require.False(t, again)

	// Another author holds the name: refused.
	require.NoError(t, st.DeleteAuthor(a.ID))
	squatter, err := st.CreateAuthor(a.Name)
	require.NoError(t, err)
	require.NotEqual(t, a.ID, squatter.ID)
	_, err = st.RestoreAuthor(a.ID, a.Name)
	require.ErrorIs(t, err, ErrAuthorNameTaken)
}
