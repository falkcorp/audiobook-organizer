// file: internal/server/duplicates_ops_combine_test.go
// version: 1.0.0
// guid: 9d2e6b14-3a87-4c50-b1f9-7e0a5c3d8f26
// last-edited: 2026-10-26

package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// runCombineOp drives the registered dedup.book-combine op's real Run.
func runCombineOp(t *testing.T, store database.Store, p dedup.BookCombineParams) (BookCombineOpResult, *resultReporter, error) {
	t.Helper()
	s := &Server{store: store, dedupCache: cache.New[gin.H]("dedup-test", time.Hour)}
	def := relinkOpDef(t, s.RegisterBookCombineOp, "dedup.book-combine")
	params, err := json.Marshal(p)
	require.NoError(t, err)
	rep := &resultReporter{opReporter: opReporter{id: "op-combine-run"}}
	runErr := def.Run(context.Background(), params, rep)
	got, ok := rep.result.(BookCombineOpResult)
	if runErr == nil || rep.result != nil {
		require.True(t, ok, "result payload is %T", rep.result)
	}
	return got, rep, runErr
}

// The op runs the combine the HTTP request used to run inline: the absorbed
// book goes to the trash, the result carries what the endpoint used to answer,
// and the undo operation is the op's own id.
func TestBookCombineOpRun_CombinesAndReportsTheResult(t *testing.T) {
	store := newRerouteStore(t)
	keep := createRerouteBook(t, store, "Keep", nil)
	absorbed := createRerouteBook(t, store, "Absorbed", nil)

	got, _, err := runCombineOp(t, store, dedup.BookCombineParams{KeepID: keep, MergeIDs: []string{absorbed}})
	require.NoError(t, err)
	require.False(t, got.Failed)
	require.Equal(t, keep, got.PrimaryID)
	require.Equal(t, 1, got.BooksDeleted)
	require.Equal(t, "op-combine-run", got.OperationID, "the combine's undo operation is the op itself")
	require.NotEmpty(t, got.JournalID)
	require.Contains(t, got.Message, "Combined")
	require.Contains(t, got.Message, "revert operation op-combine-run")
	require.True(t, isSoftDeleted(mustGetRerouteBook(t, store, absorbed)))
	require.False(t, isSoftDeleted(mustGetRerouteBook(t, store, keep)))
}

// A combine refused when the op runs (a book trashed between the request's
// check and the op) fails the op with a result that says so; nothing changed,
// so it is not "incomplete".
func TestBookCombineOpRun_RefusalStoresAFailedResult(t *testing.T) {
	store := newRerouteStore(t)
	keep := createRerouteBook(t, store, "Keep", nil)
	absorbed := createRerouteBook(t, store, "Absorbed", nil)
	trashRerouteBook(t, store, absorbed)

	got, _, err := runCombineOp(t, store, dedup.BookCombineParams{KeepID: keep, MergeIDs: []string{absorbed}})
	var sd *merge.SoftDeletedInputError
	require.ErrorAs(t, err, &sd)
	require.True(t, got.Failed)
	require.False(t, got.Incomplete)
	require.Contains(t, got.Error, absorbed)
	require.Empty(t, got.Message, "a failed combine carries no success message")
	require.Zero(t, got.BooksDeleted)
}

// A combine that fails after its undo pointer was journaled may have changed
// the library: the result names the ids that undo it and says so, and the store
// error stays out of it.
func TestBookCombineOpRun_IncompleteCarriesTheUndoIDs(t *testing.T) {
	inner := newRerouteStore(t)
	keep := createRerouteBook(t, inner, "Keep", nil)
	absorbed := createRerouteBook(t, inner, "Absorbed", nil)

	got, _, err := runCombineOp(t, &softDeleteFaultStore{inner}, dedup.BookCombineParams{KeepID: keep, MergeIDs: []string{absorbed}})
	var inc *merge.CombineIncompleteError
	require.ErrorAs(t, err, &inc)
	require.True(t, got.Failed)
	require.True(t, got.Incomplete)
	require.Equal(t, "op-combine-run", got.OperationID)
	require.Equal(t, inc.JournalID, got.JournalID)
	require.NotEmpty(t, got.JournalID)
	require.Contains(t, got.Error, "stopped part-way")
	require.Contains(t, got.Error, "Revert operation op-combine-run")
	require.NotContains(t, got.Error, "injected soft-delete failure", "the store error stays in the log")
}

func TestBookCombineOpRun_RequiresKeepAndMergeIDs(t *testing.T) {
	store := newRerouteStore(t)
	s := &Server{store: store, dedupCache: cache.New[gin.H]("dedup-test", time.Hour)}
	def := relinkOpDef(t, s.RegisterBookCombineOp, "dedup.book-combine")
	for name, p := range map[string]dedup.BookCombineParams{
		"no keep":      {MergeIDs: []string{"b"}},
		"no merge ids": {KeepID: "a"},
	} {
		raw, err := json.Marshal(p)
		require.NoError(t, err)
		require.Error(t, def.Run(context.Background(), raw, &resultReporter{opReporter: opReporter{id: "op-" + name}}), name)
	}
}

func TestCombinedAwayIDs(t *testing.T) {
	require.Equal(t, []string{"b", "c"}, combinedAwayIDs([]string{"b", "a", "c"}, "a"))
}

// The Undo action on the outcome toast is built from operation_id, so a
// success payload must never lack it: the service's id when it reports one,
// the op's own id otherwise (the service journals under the op's id).
func TestNewBookCombineOpResult_AlwaysCarriesTheUndoOperationID(t *testing.T) {
	got := newBookCombineOpResult(&merge.CombineResult{PrimaryID: "a", FilesMoved: 3, BooksDeleted: 2}, "op-self")
	require.Equal(t, "op-self", got.OperationID)
	require.Contains(t, got.Message, "revert operation op-self")

	got = newBookCombineOpResult(&merge.CombineResult{PrimaryID: "a", OperationID: "op-svc"}, "op-self")
	require.Equal(t, "op-svc", got.OperationID)

	got = newBookCombineOpResult(&merge.CombineResult{PrimaryID: "a", OperationID: "op-svc", JournalPending: "j"}, "op-self")
	require.Contains(t, got.Message, "reverting operation op-svc")
}
