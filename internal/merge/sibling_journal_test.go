// file: internal/merge/sibling_journal_test.go
// version: 1.1.0
// guid: c4377415-6df2-417a-99f1-a4bdef8080f0
// last-edited: 2026-10-05

package merge

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

func siblingFixture(t *testing.T) *vptest.Fixture {
	t.Helper()
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	return f
}

type storeSnap struct {
	Book  database.Book
	Files []database.BookFile
}

func snapshotBooks(t *testing.T, f *vptest.Fixture, ids ...string) map[string]storeSnap {
	t.Helper()
	out := map[string]storeSnap{}
	for _, id := range ids {
		b, err := f.S.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, b, "book %s", id)
		files, err := f.S.GetBookFiles(id)
		require.NoError(t, err)
		out[id] = storeSnap{Book: *b, Files: files}
	}
	return out
}

func requireNoSiblingJournal(t *testing.T, svc *Service) {
	t.Helper()
	js, err := svc.ListSiblingMoveJournals(0)
	require.NoError(t, err)
	require.Empty(t, js, "a refused merge must leave no sibling-move journal")
}

// A sibling whose file is under a protected iTunes root is a book the merge
// would rewrite, so the merge is refused before anything is written -- the
// organizer puts an iTunes organized_source original in the same group as
// its organized copy, and that copy is a routine loser.
func TestMergeBooks_RefusesITunesSibling(t *testing.T) {
	f := siblingFixture(t)
	withITunesConfig(t, func(c *config.ITunesConfig) {
		c.LibraryReadPath = testITunesLibFolder + "/iTunes Library.itl"
		c.MediaRoot = testITunesMedia
	})

	k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true"})
	l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "false"})
	src := f.Book(t, vptest.Spec{ID: "src", Group: "G", Primary: "true", State: "organized_source", NoFile: true})
	_, err := f.S.ModifyBook(src, func(b *database.Book) error {
		b.FilePath = testITunesMedia + "/Author/Book/src.m4b"
		return nil
	})
	require.NoError(t, err)

	before := snapshotBooks(t, f, k, l, src)
	svc := NewService(f.S)
	res, err := svc.MergeBooks([]string{l, k}, k)
	require.Nil(t, res)
	var prot *ITunesProtectedError
	require.True(t, errors.As(err, &prot), "want ITunesProtectedError, got %v", err)
	require.Equal(t, before, snapshotBooks(t, f, k, l, src), "a refused merge must write nothing")
	requireNoSiblingJournal(t, svc)
}

// A sibling with an unscanned (provisional) file is refused like a
// provisional participant, with nothing written.
func TestMergeBooks_RefusesProvisionalSibling(t *testing.T) {
	f := siblingFixture(t)

	k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true"})
	l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true"})
	ls := f.Book(t, vptest.Spec{ID: "ls", Group: "G", Primary: "false"})
	files, err := f.S.GetBookFiles(ls)
	require.NoError(t, err)
	require.Len(t, files, 1)
	files[0].Scan.NeedsDeep = true
	require.NoError(t, f.S.UpdateBookFile(files[0].ID, &files[0]))

	before := snapshotBooks(t, f, k, l, ls)
	svc := NewService(f.S)
	res, err := svc.MergeBooks([]string{l, k}, k)
	require.Nil(t, res)
	var prov *ProvisionalScanError
	require.True(t, errors.As(err, &prov), "want ProvisionalScanError, got %v", err)
	require.Equal(t, ls, prov.BookID)
	require.Equal(t, before, snapshotBooks(t, f, k, l, ls), "a refused merge must write nothing")
	requireNoSiblingJournal(t, svc)
}

// Every merge that moves a sibling writes an applied sibling-move journal;
// UndoSiblingMove puts the sibling back with its exact flag pointer and
// hands its group a primary (the loser stays retired, so without the
// hand-off the group would come back with none). A second undo is refused.
// A merge that moves no sibling writes no journal.
func TestUndoSiblingMove_RestoresSiblingsAndGroupPrimary(t *testing.T) {
	f := siblingFixture(t)

	k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true"})
	l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true"})
	ls := f.Book(t, vptest.Spec{ID: "ls", Group: "G", Primary: "false"})
	lsNil := f.Book(t, vptest.Spec{ID: "lsnil", Group: "G", Primary: "nil"})

	svc := NewService(f.S)
	res, err := svc.MergeBooks([]string{l, k}, k)
	require.NoError(t, err)
	require.NotEmpty(t, res.SiblingJournalID)

	j, err := svc.GetSiblingMoveJournal(res.SiblingJournalID)
	require.NoError(t, err)
	require.Equal(t, SiblingJournalApplied, j.Status)
	require.Equal(t, "H", j.IntoGroupID)
	require.Equal(t, []string{l}, j.Losers)
	require.Equal(t, res.MovedSiblings, j.Siblings)

	u, err := svc.UndoSiblingMove(res.SiblingJournalID)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{ls, lsNil}, u.Restored)
	require.Equal(t, "G", f.GroupOf(t, ls))
	require.Equal(t, "G", f.GroupOf(t, lsNil))
	f.RequireSinglePrimary(t, "G", "")
	f.RequireSinglePrimary(t, "H", k)
	require.Equal(t, "H", f.GroupOf(t, l), "the undo leaves the merge's loser alone")

	j, err = svc.GetSiblingMoveJournal(res.SiblingJournalID)
	require.NoError(t, err)
	require.Equal(t, SiblingJournalUndone, j.Status)

	// A repeat is refused: replaying an undone journal could revert a later
	// merge that moved the same sibling again.
	_, err = svc.UndoSiblingMove(res.SiblingJournalID)
	require.ErrorIs(t, err, ErrSiblingUndoRefused)

	_, err = svc.UndoSiblingMove("nope")
	require.ErrorIs(t, err, ErrSiblingJournalNotFound)

	// No sibling, no journal.
	a := f.Book(t, vptest.Spec{ID: "a", Group: "A", Primary: "true"})
	b := f.Book(t, vptest.Spec{ID: "b", Group: "B", Primary: "true"})
	res2, err := svc.MergeBooks([]string{b, a}, a)
	require.NoError(t, err)
	require.Empty(t, res2.SiblingJournalID)
	js, err := svc.ListSiblingMoveJournals(0)
	require.NoError(t, err)
	require.Len(t, js, 1)
}
