// file: internal/server/merge_sibling_undo_test.go
// version: 1.1.0
// guid: 43e2fa1b-c761-4fd6-a2f1-e668c89b5335
// last-edited: 2026-10-18

package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
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

// requireSiblingUndo asserts the merge carried LS into H and journaled its
// sibling-move undo pointer under opID, then reverts opID through the
// operations revert (Server.revertOperation, what POST
// /operations/:id/revert runs) and asserts LS is back in G as G's only
// primary (L stays retired, so the undo's hand-off must elect LS) while H
// keeps K. The retired POST /merge/sibling-undo/:journal_id was the only
// other way back.
func requireSiblingUndo(t *testing.T, f *vptest.Fixture, opID, k, ls string) {
	t.Helper()
	require.Equal(t, "H", f.GroupOf(t, ls), "the merge carried the sibling along")
	ms := merge.NewService(f.S)
	js, err := ms.ListSiblingMoveJournals(0)
	require.NoError(t, err)
	require.Len(t, js, 1)
	rows, err := f.S.GetOperationChanges(opID)
	require.NoError(t, err)
	require.Len(t, rows, 1, "the pointer is under the calling operation")
	require.Equal(t, undo.ChangeTypeMergeSiblingJournal, rows[0].ChangeType)
	require.Equal(t, js[0].ID, rows[0].NewValue)

	res, err := (&Server{store: f.S}).revertOperation(opID)
	require.NoError(t, err)
	require.Equal(t, 1, res.RestoredTypes[undo.ChangeTypeMergeSiblingJournal])
	require.Equal(t, "G", f.GroupOf(t, ls))
	f.RequireSinglePrimary(t, "G", ls)
	f.RequireSinglePrimary(t, "H", k)
}

// dedup.book-merge (applyBookMergeReroute) writes no dedup journal; its
// sibling-move pointer goes under the op.
func TestApplyBookMergeReroute_SiblingMoveIsUndoable(t *testing.T) {
	f, k, l, ls := siblingUndoFixture(t)
	_, err := applyBookMergeReroute(merge.NewService(f.S), "op-book-merge", k, []string{l})
	require.NoError(t, err)
	requireSiblingUndo(t, f, "op-book-merge", k, ls)
}

// maintenance BookMerger ((*Server).MergeBooks; merge_same_path_dupes) writes
// no dedup journal.
func TestServerBookMerger_SiblingMoveIsUndoable(t *testing.T) {
	f, k, l, ls := siblingUndoFixture(t)
	n, err := (&Server{store: f.S}).MergeBooks("op-same-path", []string{l, k}, k)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	requireSiblingUndo(t, f, "op-same-path", k, ls)
}

// The dedup.book-merge op's own Run, not applyBookMergeReroute called with a
// hand-picked id: the merge must journal under the id the registry gives the
// run (opsregistry.ReporterOpID), so dropping that lookup leaves the pointer
// under "" and this fails. The revert is of the run's id, which is the id the
// Operations page offers.
func TestBookMergeOpRun_SiblingMoveIsUndoableByRunID(t *testing.T) {
	f, k, l, ls := siblingUndoFixture(t)
	s := &Server{store: f.S, dedupCache: cache.New[gin.H]("dedup-test", time.Hour)}
	def := relinkOpDef(t, s.RegisterBookMergeOp, "dedup.book-merge")
	params, err := json.Marshal(dedup.BookMergeParams{KeepID: k, MergeIDs: []string{l}})
	require.NoError(t, err)
	require.NoError(t, def.Run(context.Background(), params, &opReporter{id: "op-book-merge-run"}))
	requireSiblingUndo(t, f, "op-book-merge-run", k, ls)
}

// A dedup engine merge (MergeBooksJournaled: auto-resolve, merge-candidate,
// review applies) runs with no operation of its own, so merge.Service mints a
// per-merge id (owner decision: one id per merge, not per batch). That id is
// on the result, the surviving book's change list (GET
// /audiobooks/:id/changes) carries it, and reverting it through the operations revert puts the sibling
// back. This is the path the retired POST /merge/sibling-undo used to cover.
func TestMergeBooksJournaled_SiblingMoveIsUndoableByItsOperation(t *testing.T) {
	f, k, l, ls := siblingUndoFixture(t)
	edb, err := pebble.Open(t.TempDir(), &pebble.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = edb.Close() })
	engine, err := dedup.NewEngine(database.NewEmbeddingStore(database.NewGatedDB(edb)), f.S,
		nil, nil, merge.NewService(f.S), unified.DefaultScoreConfig())
	require.NoError(t, err)

	res, keys, err := engine.MergeBooksJournaled(0, []string{l, k}, k, "dedup:merge-source:test")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.NotEmpty(t, res.SiblingOperationID, "the merge minted an operation for its undo pointer")

	changes, err := f.S.GetBookChanges(k)
	require.NoError(t, err)
	found := false
	for _, c := range changes {
		found = found || c.OperationID == res.SiblingOperationID
	}
	require.True(t, found, "the survivor's change list names the merge's operation")

	requireSiblingUndo(t, f, res.SiblingOperationID, k, ls)
}
