// file: internal/plugins/maintenance/revert_retired_key_owner_test.go
// version: 1.1.0
// guid: c4a7d915-2e63-48b0-9f1d-5b8e3a07c162
// last-edited: 2026-10-26

package maintenance

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// retiredKeyFixture: a retired row K holds the single-owner key of a shared
// path (no live row has the path), and X is a live row elsewhere that a
// repoint will move onto that path.
func retiredKeyFixture(t *testing.T) (d *dcFixture, path, kRow, holder, xBook, xRow string) {
	d = newDCFixture(t)
	path = d.file(t, "lib/shared/01.mp3", 500)
	other := d.file(t, "lib/x/01.mp3", 501)
	holder = d.book(t, "H", "Holder", d.path("lib/holder"), nil)
	shell := d.book(t, "S", "Shell", path, nil)
	v, err := d.s.CreateBookVersion(&database.BookVersion{
		BookID: holder, Status: database.BookVersionStatusRetired, Format: "mp3", Source: "retired_book",
		RetiredFromBookID: shell, RetireOpID: "op-ret", RetiredAt: new(time.Now()),
	})
	require.NoError(t, err)
	kf := &database.BookFile{BookID: shell, FilePath: path, FileSize: 500, Duration: 600, FileHash: "k"}
	require.NoError(t, d.s.CreateBookFile(kf))
	require.NoError(t, d.s.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{kf.ID}, SourceBookID: shell}}, holder, v.ID))
	xBook = d.book(t, "X", "X", other, nil)
	xf := &database.BookFile{BookID: xBook, FilePath: other, FileSize: 501, Duration: 600, FileHash: "k"}
	require.NoError(t, d.s.CreateBookFile(xf))
	owner, err := d.s.GetBookFileByPathIncludingRetired(path)
	require.NoError(t, err)
	require.NotNil(t, owner)
	require.Equal(t, kf.ID, owner.ID, "precondition: the retired row holds the key")
	return d, path, kf.ID, holder, xBook, xf.ID
}

// A repoint onto a path a retired row keys journals that row as the key's
// owner, and the revert gives the key back to it: the single-row claim read
// does not see the path as free afterwards.
func TestRevertRepoint_KeyGoesBackToARetiredOwner(t *testing.T) {
	d, path, kRow, holder, xBook, xRow := retiredKeyFixture(t)
	repointAndRevert(t, d, xBook, xRow, path, false)

	got, err := d.s.GetBookFileByPathIncludingRetired(path)
	require.NoError(t, err)
	require.NotNil(t, got, "the path is still claimed by the retired row after the revert")
	require.Equal(t, kRow, got.ID)
	require.Equal(t, holder, got.BookID)
	require.True(t, got.Retired)
	live, err := d.s.GetBookFileByPath(path)
	require.NoError(t, err)
	require.Nil(t, live, "the live getter still reads the retired row's path as no live file")
}

// retiredClaimRecorder counts the revert's attempts to hand a path key to a
// retired row.
type retiredClaimRecorder struct {
	*database.PebbleStore
	retiredClaims int
}

func (r *retiredClaimRecorder) ClaimRetiredBookFilePathKey(bookID, fileID, path string) (bool, error) {
	r.retiredClaims++
	return r.PebbleStore.ClaimRetiredBookFilePathKey(bookID, fileID, path)
}

// A live row at the path outranks the journaled retired owner: the key goes to
// the live row, never back onto the retired one.
//
// The live row L is created BETWEEN the repoint and the revert, so the journal
// names the retired row K as the key's owner (L did not exist when the repoint
// ran) and L is at the path when the revert re-indexes it. A live row created
// before the repoint would be journaled as the owner itself and never put K and
// L in competition.
//
// The store refuses to give a key held by a live row to a retired row, which
// alone keeps L the key's holder even if the revert asked K first. So the test
// also pins the revert's own ordering: it must not attempt the retired claim at
// all while a non-retired row can take the key.
func TestRevertRepoint_LiveRowAtPathOutranksTheRetiredOwner(t *testing.T) {
	d, path, kRow, _, xBook, xRow := retiredKeyFixture(t)
	was := repointOnto(t, d, xBook, xRow, path, false)

	liveBook := d.book(t, "L", "Live", path, nil)
	lf := &database.BookFile{BookID: liveBook, FilePath: path, FileSize: 500, Duration: 600, FileHash: "k"}
	require.NoError(t, d.s.CreateBookFile(lf))
	atPath, err := d.s.GetBookFileByPathIncludingRetired(path)
	require.NoError(t, err)
	require.NotNil(t, atPath)
	require.Equal(t, lf.ID, atPath.ID, "precondition: creating L took the path key")

	rec := &retiredClaimRecorder{PebbleStore: d.s}
	revertRepoint(t, d, rec, xBook, xRow, was)

	got, err := d.s.GetBookFileByPathIncludingRetired(path)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, lf.ID, got.ID, "a live row always owns the key over a retired one")
	require.False(t, got.Retired)
	require.NotEqual(t, kRow, got.ID)
	require.Zero(t, rec.retiredClaims, "the retired owner is only a last resort, never asked while a live row can take the key")
}
