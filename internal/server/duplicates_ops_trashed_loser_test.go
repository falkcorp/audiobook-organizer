// file: internal/server/duplicates_ops_trashed_loser_test.go
// version: 1.1.0
// guid: 0c6f2a9e-5b1d-4e83-9f47-3d8a1b6c7e20
// last-edited: 2026-10-22

package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// trashRerouteBook soft-deletes a book the way the user's trash does.
func trashRerouteBook(t *testing.T, store *database.PebbleStore, id string) {
	t.Helper()
	b := mustGetRerouteBook(t, store, id)
	now := time.Now()
	b.MarkedForDeletion = new(true)
	b.MarkedForDeletionAt = &now
	_, err := store.UpdateBook(id, b)
	require.NoError(t, err)
}

// 2026-10-21 prod: a five-book merge was refused outright because one loser had
// been trashed two minutes earlier. The trashed loser is now skipped and the
// rest merge.
func TestApplyBookMergeReroute_TrashedLoserIsSkippedAndTheRestMerge(t *testing.T) {
	store := newRerouteStore(t)
	keep := createRerouteBook(t, store, "Keep", nil)
	live := createRerouteBook(t, store, "Live loser", nil)
	trashed := createRerouteBook(t, store, "Trashed loser", nil)
	trashRerouteBook(t, store, trashed)

	res, skipped, err := applyBookMergeReroute(merge.NewService(store), "", keep, []string{trashed, live})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, 1, res.SoftDeleted, "only the live loser was merged")
	require.Equal(t, []SkippedMergeLoser{{BookID: trashed, Reason: SkipReasonAlreadyTrashed}}, skipped)
	require.True(t, isSoftDeleted(mustGetRerouteBook(t, store, live)))
	require.False(t, isSoftDeleted(mustGetRerouteBook(t, store, keep)))
}

// Every loser trashed: a clear no-op (nil result, no error), every id reported.
func TestApplyBookMergeReroute_AllLosersTrashedIsANoOp(t *testing.T) {
	store := newRerouteStore(t)
	keep := createRerouteBook(t, store, "Keep", nil)
	a := createRerouteBook(t, store, "Trashed A", nil)
	b := createRerouteBook(t, store, "Trashed B", nil)
	trashRerouteBook(t, store, a)
	trashRerouteBook(t, store, b)

	res, skipped, err := applyBookMergeReroute(merge.NewService(store), "", keep, []string{a, b})
	require.NoError(t, err)
	require.Nil(t, res)
	require.Len(t, skipped, 2)
	require.False(t, isSoftDeleted(mustGetRerouteBook(t, store, keep)))
}

// A trashed SURVIVOR stays an error, and nothing is skipped to hide it.
func TestApplyBookMergeReroute_TrashedSurvivorIsStillAnError(t *testing.T) {
	store := newRerouteStore(t)
	keep := createRerouteBook(t, store, "Keep", nil)
	live := createRerouteBook(t, store, "Live loser", nil)
	trashRerouteBook(t, store, keep)

	res, skipped, err := applyBookMergeReroute(merge.NewService(store), "", keep, []string{live})
	var sd *merge.SoftDeletedInputError
	require.ErrorAs(t, err, &sd)
	require.True(t, sd.AsPrimary)
	require.Nil(t, res)
	require.Empty(t, skipped)
	require.False(t, isSoftDeleted(mustGetRerouteBook(t, store, live)), "the loser was not touched")
}

// resultReporter is opReporter plus the result payload the op stores.
type resultReporter struct {
	opReporter
	result any
}

func (r *resultReporter) SetResult(v any) error { r.result = v; return nil }

var _ opsregistry.ResultSetter = (*resultReporter)(nil)

// The op itself: succeeds, and its result payload names the skipped loser.
func TestBookMergeOpRun_ReportsSkippedTrashedLoser(t *testing.T) {
	store := newRerouteStore(t)
	keep := createRerouteBook(t, store, "Keep", nil)
	live := createRerouteBook(t, store, "Live loser", nil)
	trashed := createRerouteBook(t, store, "Trashed loser", nil)
	trashRerouteBook(t, store, trashed)

	s := &Server{store: store, dedupCache: cache.New[gin.H]("dedup-test", time.Hour)}
	def := relinkOpDef(t, s.RegisterBookMergeOp, "dedup.book-merge")
	params, err := json.Marshal(dedup.BookMergeParams{KeepID: keep, MergeIDs: []string{trashed, live}})
	require.NoError(t, err)
	rep := &resultReporter{opReporter: opReporter{id: "op-trashed-loser"}}
	require.NoError(t, def.Run(context.Background(), params, rep))

	got, ok := rep.result.(BookMergeOpResult)
	require.True(t, ok, "result payload is %T", rep.result)
	require.Equal(t, 1, got.Merged)
	require.False(t, got.NoOp)
	require.Equal(t, []SkippedMergeLoser{{BookID: trashed, Reason: SkipReasonAlreadyTrashed}}, got.Skipped)
}

func TestBookMergeOpRun_AllTrashedIsASuccessfulNoOp(t *testing.T) {
	store := newRerouteStore(t)
	keep := createRerouteBook(t, store, "Keep", nil)
	trashed := createRerouteBook(t, store, "Trashed loser", nil)
	trashRerouteBook(t, store, trashed)

	s := &Server{store: store, dedupCache: cache.New[gin.H]("dedup-test", time.Hour)}
	def := relinkOpDef(t, s.RegisterBookMergeOp, "dedup.book-merge")
	params, err := json.Marshal(dedup.BookMergeParams{KeepID: keep, MergeIDs: []string{trashed}})
	require.NoError(t, err)
	rep := &resultReporter{opReporter: opReporter{id: "op-all-trashed"}}
	require.NoError(t, def.Run(context.Background(), params, rep))
	got := rep.result.(BookMergeOpResult)
	require.True(t, got.NoOp)
	require.Zero(t, got.Merged)
	require.Len(t, got.Skipped, 1)
}

// softDeleteFaultStore fails any ModifyBook whose mutation soft-deletes the
// book, so a merge fails after its membership writes landed.
type softDeleteFaultStore struct{ *database.PebbleStore }

func (s *softDeleteFaultStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	probe := &database.Book{}
	if err := fn(probe); err == nil && probe.MarkedForDeletion != nil && *probe.MarkedForDeletion {
		return nil, errors.New("injected soft-delete failure")
	}
	return s.PebbleStore.ModifyBook(id, fn)
}

// A merge that fails stores a result the page can read: Failed, the error, and
// Partial only when the merge had changed something before it stopped.
func TestBookMergeOpRun_FailureStoresAResult(t *testing.T) {
	run := func(t *testing.T, store database.Store, keep, loser string) (BookMergeOpResult, error) {
		t.Helper()
		s := &Server{store: store, dedupCache: cache.New[gin.H]("dedup-test", time.Hour)}
		def := relinkOpDef(t, s.RegisterBookMergeOp, "dedup.book-merge")
		params, err := json.Marshal(dedup.BookMergeParams{KeepID: keep, MergeIDs: []string{loser}})
		require.NoError(t, err)
		rep := &resultReporter{opReporter: opReporter{id: "op-failed-merge"}}
		runErr := def.Run(context.Background(), params, rep)
		got, ok := rep.result.(BookMergeOpResult)
		require.True(t, ok, "result payload is %T", rep.result)
		return got, runErr
	}

	t.Run("refused before any write is failed, not partial", func(t *testing.T) {
		store := newRerouteStore(t)
		keep := createRerouteBook(t, store, "Keep", nil)
		live := createRerouteBook(t, store, "Live loser", nil)
		trashRerouteBook(t, store, keep)
		got, err := run(t, store, keep, live)
		require.Error(t, err)
		require.True(t, got.Failed)
		require.False(t, got.Partial)
		require.Zero(t, got.Merged)
		require.Equal(t, keep, got.KeepID)
		require.Contains(t, got.Error, "soft-deleted")
	})

	t.Run("failed after membership writes is partial", func(t *testing.T) {
		inner := newRerouteStore(t)
		keep := createRerouteBook(t, inner, "Keep", nil)
		live := createRerouteBook(t, inner, "Live loser", nil)
		got, err := run(t, &softDeleteFaultStore{inner}, keep, live)
		require.Error(t, err)
		var pe *merge.PartialMergeError
		require.ErrorAs(t, err, &pe)
		require.True(t, got.Failed)
		require.True(t, got.Partial)
		require.Contains(t, got.Error, "injected soft-delete failure")
		require.False(t, isSoftDeleted(mustGetRerouteBook(t, inner, live)), "the loser stayed live")
	})
}

// Only the losers that were folded in reach the candidate clean-up.
func TestMergedLoserIDs(t *testing.T) {
	res := &merge.Result{}
	skipped := []SkippedMergeLoser{{BookID: "trashed"}}
	require.Equal(t, []string{"a", "b"}, mergedLoserIDs("keep", []string{"a", "keep", "trashed", "b", "a"}, skipped, res))
	require.Empty(t, mergedLoserIDs("keep", []string{"a"}, nil, nil), "nothing merged means nothing to clean up")
}
