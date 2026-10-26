// file: internal/database/book_file_path_key_retired_test.go
// version: 1.0.0
// guid: 3b8f6d21-9e04-4c57-a2d8-71c5e0b94a36
// last-edited: 2026-10-26

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func pathKeyOf(path string) string { return "book_file_path:" + bookFilePathCRC(path) }

// A retired row gets a dropped path key back, and only a retired row can take
// it through the retired claim.
func TestClaimRetiredBookFilePathKey_HandsADroppedKeyBackToTheRetiredRow(t *testing.T) {
	fx := newRetiredFixture(t)
	path := "/lib/Gotham/ret/shell/01.mp3"
	k := fx.create(t, fx.shell.ID, BookFile{FilePath: path, FileHash: "hk", TrackNumber: 1})
	require.NoError(t, fx.retire(t, k))
	require.Equal(t, fx.holder.ID+":"+k.ID, indexOwner(t, fx.ps, pathKeyOf(path)), "precondition: the retired row keeps the key while no live row has the path")

	require.NoError(t, fx.ps.db.Delete([]byte(pathKeyOf(path)), nil))
	require.Equal(t, "", indexOwner(t, fx.ps, pathKeyOf(path)))

	ok, err := fx.ps.ClaimBookFilePathKey(fx.holder.ID, k.ID, path)
	require.NoError(t, err)
	require.False(t, ok, "the plain claim still refuses a retired row")
	require.Equal(t, "", indexOwner(t, fx.ps, pathKeyOf(path)))

	ok, err = fx.ps.ClaimRetiredBookFilePathKey(fx.holder.ID, k.ID, path)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, fx.holder.ID+":"+k.ID, indexOwner(t, fx.ps, pathKeyOf(path)))
	got, err := fx.ps.GetBookFileByPathIncludingRetired(path)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, k.ID, got.ID)
}

// The retired claim never takes a key from a live row at the path, and refuses
// a row that is not retired or has left the path.
func TestClaimRetiredBookFilePathKey_NeverTakesTheKeyFromALiveRow(t *testing.T) {
	fx := newRetiredFixture(t)
	path := "/lib/Gotham/ret/shell/02.mp3"
	k := fx.create(t, fx.shell.ID, BookFile{FilePath: path, FileHash: "hk2", TrackNumber: 1})
	require.NoError(t, fx.retire(t, k))
	live := fx.create(t, fx.holder.ID, BookFile{FilePath: path, FileHash: "hk2", TrackNumber: 2})
	require.Equal(t, fx.holder.ID+":"+live.ID, indexOwner(t, fx.ps, pathKeyOf(path)), "precondition: the live row owns the key")

	ok, err := fx.ps.ClaimRetiredBookFilePathKey(fx.holder.ID, k.ID, path)
	require.NoError(t, err)
	require.False(t, ok, "a live row at the path keeps the key")
	require.Equal(t, fx.holder.ID+":"+live.ID, indexOwner(t, fx.ps, pathKeyOf(path)))

	ok, err = fx.ps.ClaimRetiredBookFilePathKey(fx.holder.ID, live.ID, path)
	require.NoError(t, err)
	require.False(t, ok, "a live row is not claimed through the retired claim")

	ok, err = fx.ps.ClaimRetiredBookFilePathKey(fx.holder.ID, k.ID, "/lib/Gotham/ret/elsewhere.mp3")
	require.NoError(t, err)
	require.False(t, ok, "the row is not at that path")
}
