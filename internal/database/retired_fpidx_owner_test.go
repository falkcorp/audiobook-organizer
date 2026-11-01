// file: internal/database/retired_fpidx_owner_test.go
// version: 1.0.0
// guid: 04be65b2-d123-45b9-80f7-1d25279847ab
// last-edited: 2026-10-31

package database

import (
	"errors"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/stretchr/testify/require"
)

// fpidxValues returns the distinct values stored in the fpidx: rows of one
// file (the owning book id each names), read through the file's fpidx_meta:
// member list, plus how many rows there are.
func fpidxValues(t *testing.T, ps *PebbleStore, fileID string) (values map[string]struct{}, rows int) {
	t.Helper()
	mv, closer, err := ps.db.Get(lshMetaKey(fileID))
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, 0
	}
	require.NoError(t, err)
	meta := append([]byte(nil), mv...)
	require.NoError(t, closer.Close())
	subs, bands := decodeLSHMeta(meta)
	values = map[string]struct{}{}
	for i := range subs {
		v, c, err := ps.db.Get(lshIndexKey(bands[i], subs[i], fileID))
		require.NoError(t, err, "fpidx row %d of %s", i, fileID)
		values[string(v)] = struct{}{}
		require.NoError(t, c.Close())
		rows++
	}
	return values, rows
}

// requireFpidxOwner asserts every fpidx: row of fileID names bookID, and that a
// probe with the file's own fingerprint finds the file.
func requireFpidxOwner(t *testing.T, ps *PebbleStore, f *BookFile, bookID string) {
	t.Helper()
	values, rows := fpidxValues(t, ps, f.ID)
	require.NotZero(t, rows, "the fixture file must be indexed")
	require.Equal(t, map[string]struct{}{bookID: {}}, values)

	subs, bands, err := fingerprint.Subprints(f.AcoustIDFingerprint)
	require.NoError(t, err)
	hits, err := ps.LSHProbe(subs, bands, 0)
	require.NoError(t, err)
	require.Contains(t, hits, f.ID)
	// The probe answers file ids; the row a hit resolves to is keyed under the
	// same book the stored value names.
	row, err := ps.GetBookFileByFileID(f.ID)
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Equal(t, bookID, row.BookID)
}

func (f *retiredFixture) createPrinted(t *testing.T, bookID, path string, seed int64) *BookFile {
	t.Helper()
	return f.create(t, bookID, BookFile{
		FilePath:            path,
		AcoustIDFingerprint: synthRaw(seed, 57600),
		AcoustIDFPVersion:   fingerprint.PrintEncodingVersion,
	})
}

// Retire re-keys the row under the holder and the same batch moves its fpidx:
// values to the holder; un-retire moves them back to the shell. A fingerprint
// hit therefore never names the book the row left.
func TestRetireBookFiles_FpidxValueFollowsTheOwnerBothWays(t *testing.T) {
	fx := newRetiredFixture(t)
	x := fx.createPrinted(t, fx.shell.ID, "/lib/ret/fp/01.mp3", 7001)
	requireFpidxOwner(t, fx.ps, x, fx.shell.ID) // precondition: indexed under the shell

	require.NoError(t, fx.retire(t, x))
	requireFpidxOwner(t, fx.ps, x, fx.holder.ID)

	require.NoError(t, fx.ps.UnretireBookFiles(
		[]BookFileMove{{FileIDs: []string{x.ID}, SourceBookID: fx.holder.ID}}, fx.shell.ID, fx.version.ID, nil))
	requireFpidxOwner(t, fx.ps, x, fx.shell.ID)
}

// A re-home changes the holder of a retired row, so its fpidx: value follows,
// and the revert follows it back.
func TestRehomeRetiredVersion_FpidxValueFollowsTheHolder(t *testing.T) {
	fx := newRetiredFixture(t)
	x := fx.createPrinted(t, fx.shell.ID, "/lib/ret/fp/02.mp3", 7002)
	require.NoError(t, fx.retire(t, x))
	requireFpidxOwner(t, fx.ps, x, fx.holder.ID)

	newHolder, err := fx.ps.CreateBook(&Book{Title: "Retire New Holder", FilePath: "/lib/ret/newholder"})
	require.NoError(t, err)
	require.NoError(t, fx.ps.RehomeRetiredVersion(fx.version.ID, newHolder.ID))
	requireFpidxOwner(t, fx.ps, x, newHolder.ID)

	require.NoError(t, fx.ps.RestoreRetiredVersionHolder(fx.version.ID, fx.holder.ID))
	requireFpidxOwner(t, fx.ps, x, fx.holder.ID)
}

// A row with no fingerprint has no fpidx: rows and gets none from a retire or
// an un-retire; a fingerprinted sibling moved in the same batch is rewritten.
func TestRetireBookFiles_FpidxRewriteSkipsRowsWithoutAFingerprint(t *testing.T) {
	fx := newRetiredFixture(t)
	bare := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/fp/03.mp3", FileHash: "bare-hash"})
	printed := fx.createPrinted(t, fx.shell.ID, "/lib/ret/fp/04.mp3", 7003)

	require.NoError(t, fx.retire(t, bare, printed))
	values, rows := fpidxValues(t, fx.ps, bare.ID)
	require.Zero(t, rows)
	require.Empty(t, values)
	require.False(t, fx.ps.HasLSHIndex(bare.ID))
	requireFpidxOwner(t, fx.ps, printed, fx.holder.ID)

	require.NoError(t, fx.ps.UnretireBookFiles(
		[]BookFileMove{{FileIDs: []string{bare.ID, printed.ID}, SourceBookID: fx.holder.ID}}, fx.shell.ID, fx.version.ID, nil))
	_, rows = fpidxValues(t, fx.ps, bare.ID)
	require.Zero(t, rows)
	require.False(t, fx.ps.HasLSHIndex(bare.ID))
	requireFpidxOwner(t, fx.ps, printed, fx.shell.ID)
}

// The index build op carries the book id it read when it picked the file up.
// A write of that id after the file was retired must not put the shell back.
func TestPutLSHEntries_TakesTheOwnerFromTheFileRow(t *testing.T) {
	fx := newRetiredFixture(t)
	x := fx.createPrinted(t, fx.shell.ID, "/lib/ret/fp/05.mp3", 7004)
	require.NoError(t, fx.retire(t, x))

	subs, bands, err := fingerprint.Subprints(x.AcoustIDFingerprint)
	require.NoError(t, err)
	require.NoError(t, fx.ps.PutLSHEntries(x.ID, fx.shell.ID, subs, bands)) // stale caller
	requireFpidxOwner(t, fx.ps, x, fx.holder.ID)

	// A file with no row keeps the caller's id.
	require.NoError(t, fx.ps.PutLSHEntries("file-without-a-row", "book-caller", subs, bands))
	values, _ := fpidxValues(t, fx.ps, "file-without-a-row")
	require.Equal(t, map[string]struct{}{"book-caller": {}}, values)
}

// The member list decides which rows the retire rewrites, so it is watched: a
// meta row that appears between the read and the commit fails the verify hook
// and the attempt re-stages.
func TestIndexCAS_WatchFailsWhenTheLSHMemberListAppears(t *testing.T) {
	fx := newRetiredFixture(t)
	x := fx.createPrinted(t, fx.shell.ID, "/lib/ret/fp/06.mp3", 7005)
	other := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/fp/07.mp3"})

	cas := newIndexCAS()
	got, err := cas.watch(fx.ps, lshMetaKey(other.ID))
	require.NoError(t, err)
	require.False(t, got.found)
	require.NoError(t, cas.verify(fx.ps)())

	// The file gets a fingerprint (so a member list) after the read.
	subs, bands, err := fingerprint.Subprints(x.AcoustIDFingerprint)
	require.NoError(t, err)
	require.NoError(t, fx.ps.PutLSHEntries(other.ID, fx.shell.ID, subs, bands))
	require.ErrorIs(t, cas.verify(fx.ps)(), errBookFileRowVanished)
}
