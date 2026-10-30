// file: internal/database/retired_book_files_surface_test.go
// version: 1.1.1
// guid: 3b7d5a19-c0e2-4f84-9a61-d8e24b70f315
// last-edited: 2026-10-30

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// opaqueStore is a store the retired readers cannot resolve: it implements none
// of RetiredBookFileReader.
type opaqueStore struct{}

// The include-retired readers fail closed. A store that cannot answer them used
// to be read through the live-only twin, which silently dropped every retired
// row from a scan whose whole point is to see them (a prefix rewrite, an orphan
// scan, the PID census).
func TestRetiredReaders_FailClosedWithoutTheCapability(t *testing.T) {
	_, err := AllBookFilesCoreIncludingRetired(opaqueStore{})
	require.ErrorIs(t, err, ErrRetiredReaderUnavailable)
	_, err = BookFileByPathIncludingRetired(opaqueStore{}, "/lib/x.mp3")
	require.ErrorIs(t, err, ErrRetiredReaderUnavailable)

	fx := newRetiredFixture(t)
	rows, err := AllBookFilesCoreIncludingRetired(fx.ps)
	require.NoError(t, err)
	require.Empty(t, rows)
}

// GetBookFileByPathIncludingRetired names a retired-only path and the live row
// when a live row shares the path; GetBookFileByPath still hides the retired one.
func TestGetBookFileByPathIncludingRetired(t *testing.T) {
	fx := newRetiredFixture(t)
	x := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/inc/only.mp3"})
	require.NoError(t, fx.retire(t, x))

	live, err := fx.ps.GetBookFileByPath(x.FilePath)
	require.NoError(t, err)
	require.Nil(t, live)
	got, err := fx.ps.GetBookFileByPathIncludingRetired(x.FilePath)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, x.ID, got.ID)
	require.True(t, got.Retired)
	viaHelper, err := BookFileByPathIncludingRetired(fx.ps, x.FilePath)
	require.NoError(t, err)
	require.Equal(t, x.ID, viaHelper.ID)

	// A live row written later claims the single-owner key and is what both answer.
	liveRow := fx.create(t, fx.holder.ID, BookFile{FilePath: x.FilePath})
	got, err = fx.ps.GetBookFileByPathIncludingRetired(x.FilePath)
	require.NoError(t, err)
	require.Equal(t, liveRow.ID, got.ID)
	none, err := fx.ps.GetBookFileByPathIncludingRetired("/lib/ret/inc/none.mp3")
	require.NoError(t, err)
	require.Nil(t, none)
}

func TestRetiredClaimOnPath(t *testing.T) {
	require.Nil(t, RetiredClaimOnPath(nil))
	require.Nil(t, RetiredClaimOnPath([]BookFile{{ID: "a", BookID: "b1"}}), "a live row is not a claim")
	rows := []BookFile{
		{ID: "live", BookID: "b0"},
		{ID: "r2", BookID: "b2", Retired: true},
		{ID: "r1b", BookID: "b1", Retired: true},
		{ID: "r1a", BookID: "b1", Retired: true},
	}
	got := RetiredClaimOnPath(rows)
	require.NotNil(t, got)
	require.Equal(t, "r1a", got.ID, "the smallest (BookID, ID) retired row, whatever the input order")
	rows[0], rows[3] = rows[3], rows[0]
	require.Equal(t, "r1a", RetiredClaimOnPath(rows).ID)
}

// The single-identity lookups answer the LIVE file: a fingerprint segment or a
// content hash held only by a retired row names nobody.
func TestFilteredIdentityLookups_IgnoreARetiredHolder(t *testing.T) {
	fx := newRetiredFixture(t)
	x := fx.create(t, fx.shell.ID, BookFile{
		FilePath: "/lib/ret/idn/x.mp3", FileHash: "idn-hash", OriginalFileHash: "idn-orig",
		AcoustIDFingerprint: []byte("fp-idn-x"),
	})
	liveBefore, err := fx.ps.GetBookBySegmentFileHash("idn-hash")
	require.NoError(t, err)
	require.NotNil(t, liveBefore, "premise: the shell's live row answers before the retire")

	require.NoError(t, fx.retire(t, x))

	for _, h := range []string{"idn-hash", "idn-orig"} {
		b, err := fx.ps.GetBookBySegmentFileHash(h)
		require.NoError(t, err)
		require.Nil(t, b, "hash %s is held only by a retired row", h)
	}
	// The complete lookup still finds it.
	rows, err := fx.ps.BookFilesWithHash("idn-hash")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.True(t, rows[0].Retired)

	// A live row with the same hash makes both lookups answer it.
	liveRow := fx.create(t, fx.holder.ID, BookFile{FilePath: "/lib/ret/idn/live.mp3", FileHash: "idn-hash"})
	b, err := fx.ps.GetBookBySegmentFileHash("idn-hash")
	require.NoError(t, err)
	require.NotNil(t, b)
	require.Equal(t, fx.holder.ID, b.ID)
	require.NotEmpty(t, liveRow.ID)
}

func TestGetBookFileByAcoustID_IgnoresARetiredHolder(t *testing.T) {
	fx := newRetiredFixture(t)
	const fp = "acoustid-seg-retired"
	// A legacy row: the segment is stored and indexed, as before T020. A create
	// no longer indexes a segment its row does not store, so the row and its key
	// are planted the way an old binary left them.
	x := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/ac/x.mp3"})
	x.AcoustIDSeg0 = fp
	plantLegacyAcoustIDRow(t, fx.ps, x)
	got, err := fx.ps.GetBookFileByAcoustID(fp)
	require.NoError(t, err)
	require.NotNil(t, got, "premise: the live row answers before the retire")
	require.Equal(t, x.ID, got.ID)

	require.NoError(t, fx.retire(t, x))
	got, err = fx.ps.GetBookFileByAcoustID(fp)
	require.NoError(t, err)
	require.Nil(t, got, "a fingerprint held only by a retired row names nobody")
}

// A retired version cannot be rehomed onto the shell it was retired out of.
func TestRehomeRetiredVersion_RefusesTheShellAsHolder(t *testing.T) {
	fx := newRetiredFixture(t)
	x := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/rh/x.mp3"})
	require.NoError(t, fx.retire(t, x))
	require.ErrorIs(t, fx.ps.RehomeRetiredVersion(fx.version.ID, fx.shell.ID), ErrRetiredVersionMismatch)
	got, err := fx.ps.GetBookFileByID(fx.holder.ID, x.ID)
	require.NoError(t, err)
	require.NotNil(t, got, "nothing moved")
	require.True(t, got.Retired)
}
