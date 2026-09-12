// file: internal/database/narrator_bookref_test.go
// version: 1.0.0
// guid: b32d6c74-f0e6-45be-ba7c-52dae7be0ce2
// last-edited: 2026-09-12

package database

import (
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/util"
)

func seedNarratorRefStore(t *testing.T) *PebbleStore {
	t.Helper()
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	store, err := NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	store.WaitForWarmup()
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func mkNarratorRefBook(t *testing.T, s *PebbleStore, title string, primary, trashed bool, narratorText string) *Book {
	t.Helper()
	b := &Book{
		Title:             title,
		FilePath:          "/narratorref/" + title,
		IsPrimaryVersion:  new(primary),
		MarkedForDeletion: new(trashed),
	}
	if narratorText != "" {
		b.Narrator = new(narratorText)
	}
	created, err := s.CreateBook(b)
	require.NoError(t, err)
	return created
}

func mkNarrator(t *testing.T, s *PebbleStore, name string) *Narrator {
	t.Helper()
	n, err := s.CreateNarrator(name)
	require.NoError(t, err)
	return n
}

func TestGetAllNarratorRefs_CountsEveryBookStateAndOrphanRows(t *testing.T) {
	s := seedNarratorRefStore(t)
	trashed := mkNarrator(t, s, "Trashed Narrator")
	nonPrimary := mkNarrator(t, s, "Secondary Narrator")
	healthy := mkNarrator(t, s, "Healthy Narrator")
	orphan := mkNarrator(t, s, "Orphan Narrator")
	nonASCII := mkNarrator(t, s, "Non ASCII Narrator")
	unlinked := mkNarrator(t, s, "Unlinked Narrator")

	b1 := mkNarratorRefBook(t, s, "trashed", true, true, "")
	b2 := mkNarratorRefBook(t, s, "secondary", false, false, "")
	b3 := mkNarratorRefBook(t, s, "healthy", true, false, "")
	require.NoError(t, s.SetBookNarrators(b1.ID, []BookNarrator{{NarratorID: trashed.ID}}))
	require.NoError(t, s.SetBookNarrators(b2.ID, []BookNarrator{{NarratorID: nonPrimary.ID}}))
	// A repeated credit on one book counts that book once.
	require.NoError(t, s.SetBookNarrators(b3.ID, []BookNarrator{{NarratorID: healthy.ID}, {NarratorID: healthy.ID, Position: 1}}))
	// Junction rows whose book row does not exist, one under a non-ASCII id
	// that a hand-written "~" upper bound would miss.
	require.NoError(t, s.SetBookNarrators("gone-book", []BookNarrator{{NarratorID: orphan.ID}}))
	require.NoError(t, s.SetBookNarrators("é-book", []BookNarrator{{NarratorID: nonASCII.ID}}))

	refs, err := NarratorRefCounts(s)
	require.NoError(t, err)
	require.Equal(t, 1, refs.ByID[trashed.ID], "trashed book's link must count")
	require.Equal(t, 1, refs.ByID[nonPrimary.ID], "non-primary book's link must count")
	require.Equal(t, 1, refs.ByID[healthy.ID], "a repeated credit counts the book once")
	require.Equal(t, 1, refs.ByID[orphan.ID], "a junction row whose book is gone still holds the id")
	require.Equal(t, 1, refs.ByID[nonASCII.ID], "non-ASCII book ids must be inside the scan range")
	_, present := refs.ByID[unlinked.ID]
	require.False(t, present)
}

func TestGetAllNarratorRefs_NameIndexFromBookText(t *testing.T) {
	s := seedNarratorRefStore(t)
	mkNarratorRefBook(t, s, "duo", true, false, "Kate Reading & Michael Kramer")
	mkNarratorRefBook(t, s, "trashed-solo", true, true, "Kate Reading")

	refs, err := s.GetAllNarratorRefs()
	require.NoError(t, err)
	require.Equal(t, 2, refs.ByName[util.NormalizeAuthor("Kate Reading")], "split credit plus a trashed book")
	require.Equal(t, 1, refs.ByName[util.NormalizeAuthor("Michael Kramer")])
	require.Zero(t, refs.ByName[util.NormalizeAuthor("Kate Reading & Michael Kramer")], "the compound is not a name")
}

func TestCountNarratorBookLinks_SeesLiveWrites(t *testing.T) {
	s := seedNarratorRefStore(t)
	n := mkNarrator(t, s, "Late Linked Narrator")
	got, err := NarratorLinkCount(s, n.ID)
	require.NoError(t, err)
	require.Zero(t, got)

	b := mkNarratorRefBook(t, s, "late", true, false, "")
	require.NoError(t, s.SetBookNarrators(b.ID, []BookNarrator{{NarratorID: n.ID}}))
	got, err = NarratorLinkCount(s, n.ID)
	require.NoError(t, err)
	require.Equal(t, 1, got)
}

func TestNarratorRefs_UndecodableJunctionRowFailsClosed(t *testing.T) {
	s := seedNarratorRefStore(t)
	require.NoError(t, s.db.Set([]byte("book_narrators:broken"), []byte("{not json"), pebble.Sync))

	_, err := s.GetAllNarratorRefs()
	require.ErrorContains(t, err, "book_narrators:broken")
	_, err = s.CountNarratorBookLinks(1)
	require.ErrorContains(t, err, "book_narrators:broken")
}

func TestNarratorRefs_FailClosedWithoutCapability(t *testing.T) {
	_, err := NarratorRefCounts(struct{}{})
	require.Error(t, err)
	_, err = NarratorLinkCount(struct{}{}, 7)
	require.Error(t, err)
	_, err = NarratorRefCounts(nil)
	require.Error(t, err)
}
