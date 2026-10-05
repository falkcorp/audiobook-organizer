// file: internal/server/merge_sibling_undo_test.go
// version: 1.0.1
// guid: 43e2fa1b-c761-4fd6-a2f1-e668c89b5335
// last-edited: 2026-10-05

package server

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// siblingUndoFixture seeds K (primary of H) and loser L (primary of G) with
// its non-primary sibling LS. Neither caller below journals the merge itself
// or keeps merge.Result, so the only undo for LS is the sibling-move journal
// merge.Service writes.
func siblingUndoFixture(t *testing.T) (f *vptest.Fixture, k, l, ls string) {
	t.Helper()
	f = vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	k = f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true"})
	l = f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true"})
	ls = f.Book(t, vptest.Spec{ID: "ls", Group: "G", Primary: "false"})
	return f, k, l, ls
}

// requireSiblingUndo finds the one sibling-move journal the merge wrote,
// undoes it, and asserts LS is back in G as G's only primary (L stays
// retired, so the undo's hand-off must elect LS) while H keeps K.
func requireSiblingUndo(t *testing.T, f *vptest.Fixture, k, ls string) {
	t.Helper()
	require.Equal(t, "H", f.GroupOf(t, ls), "the merge carried the sibling along")
	ms := merge.NewService(f.S)
	js, err := ms.ListSiblingMoveJournals(0)
	require.NoError(t, err)
	require.Len(t, js, 1)
	res, err := ms.UndoSiblingMove(js[0].ID)
	require.NoError(t, err)
	require.Equal(t, []string{ls}, res.Restored)
	require.Equal(t, "G", f.GroupOf(t, ls))
	f.RequireSinglePrimary(t, "G", ls)
	f.RequireSinglePrimary(t, "H", k)
}

// dedup.book-merge (applyBookMergeReroute) writes no dedup journal.
func TestApplyBookMergeReroute_SiblingMoveIsUndoable(t *testing.T) {
	f, k, l, ls := siblingUndoFixture(t)
	_, err := applyBookMergeReroute(merge.NewService(f.S), k, []string{l})
	require.NoError(t, err)
	requireSiblingUndo(t, f, k, ls)
}

// maintenance BookMerger ((*Server).MergeBooks; merge_same_path_dupes) writes
// no dedup journal.
func TestServerBookMerger_SiblingMoveIsUndoable(t *testing.T) {
	f, k, l, ls := siblingUndoFixture(t)
	n, err := (&Server{store: f.S}).MergeBooks([]string{l, k}, k)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	requireSiblingUndo(t, f, k, ls)
}
