// file: internal/plugins/maintenance/folder_books_handoff_void_test.go
// version: 1.0.0
// guid: 6b0d1f4e-3a2c-4e71-9c58-2f7d8a1e0b93
// last-edited: 2026-10-06

package maintenance

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunesguard"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

// handOffChanges returns opID's journal rows of book id, by change type.
func handOffChanges(t *testing.T, d *dcFixture, opID, id string) map[string][]database.OperationChange {
	t.Helper()
	all, err := d.s.GetOperationChanges(opID)
	require.NoError(t, err)
	out := map[string][]database.OperationChange{}
	for _, c := range all {
		if c.BookID == id {
			out[c.ChangeType] = append(out[c.ChangeType], *c)
		}
	}
	return out
}

// handOffGroup seeds the dune pair plus an iTunes copy I in one version
// group, every flag nil: a crown of S would demote L and I.
func handOffGroup(t *testing.T, d *dcFixture) (s, l, it, gid string) {
	t.Helper()
	s, l = d.dune(t)
	it = d.copyBook(t, "I", "Dune (iTunes)", "books/itunes/Dune", dcRow{track: 1, dur: 600, hash: "zz"})
	gid = "vg-dune"
	for _, id := range []string{s, l, it} {
		_, err := d.s.ModifyBook(id, func(b *database.Book) error { b.VersionGroupID = &gid; return nil })
		require.NoError(t, err)
	}
	return s, l, it, gid
}

// TestFolderBooksHandOff_RefusedCrownVoidsItsDemoteRows: the demote row of
// a hand-off is journaled before the crown; when the iTunes guard refuses
// the crown nothing is written, so the row is voided (never "restored" by
// the op revert) and no hand-off note is written.
func TestFolderBooksHandOff_RefusedCrownVoidsItsDemoteRows(t *testing.T) {
	d := newDCFixture(t)
	s, l, it, gid := handOffGroup(t, d)
	fx := newFolderBooksFixer(d.p)
	w := repairs.NewWriter(d.s, d.s, dcFixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, "op-refused")

	n, err := fx.handOff(d.s, w, []string{l}, gid, s, itunesguard.MayWrite(d.s, gid))
	require.ErrorIs(t, err, versionprimary.ErrWriteRefused)
	require.Equal(t, 0, n, "a refused crown wrote nothing")

	for _, id := range []string{s, l, it} {
		b, gerr := d.s.GetBookByID(id)
		require.NoError(t, gerr)
		require.Nil(t, b.IsPrimaryVersion, "book %s untouched", id)
	}
	ch := handOffChanges(t, d, "op-refused", l)
	require.Len(t, ch[undo.ChangeTypeBookPrimaryDemote], 1)
	require.True(t, ch[undo.ChangeTypeBookPrimaryDemote][0].Voided, "the demote that never happened is voided")
	require.NotNil(t, ch[undo.ChangeTypeBookPrimaryDemote][0].RevertedAt)
	require.Empty(t, ch[undo.ChangeTypeBookPrimaryHandoff], "no hand-off note for a crown that wrote nothing")
}

// TestFolderBooksHandOff_WrittenCrownKeepsItsDemoteRows is the control: the
// unguarded crown (folder-books) demotes L, its row stays live and the note
// says the heir's true was written by this hand-off.
func TestFolderBooksHandOff_WrittenCrownKeepsItsDemoteRows(t *testing.T) {
	d := newDCFixture(t)
	s, l, _, gid := handOffGroup(t, d)
	fx := newFolderBooksFixer(d.p)
	w := repairs.NewWriter(d.s, d.s, dcFixerID, "bulk_update", "repairs-").WithJournal(d.s, d.s, "op-wrote")

	n, err := fx.handOff(d.s, w, []string{l}, gid, s, nil)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	ch := handOffChanges(t, d, "op-wrote", l)
	require.Len(t, ch[undo.ChangeTypeBookPrimaryDemote], 1)
	require.False(t, ch[undo.ChangeTypeBookPrimaryDemote][0].Voided)
	require.Len(t, ch[undo.ChangeTypeBookPrimaryHandoff], 1)
	require.Equal(t, undo.HandOffNoteValue(s, true), ch[undo.ChangeTypeBookPrimaryHandoff][0].OldValue)
}
