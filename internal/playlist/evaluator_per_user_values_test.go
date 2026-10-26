// file: internal/playlist/evaluator_per_user_values_test.go
// version: 1.0.0
// guid: 7d1f4b62-3a95-4c08-b6e1-90a5c2d8f314
// last-edited: 2026-10-26

package playlist

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// A per-user term whose value the per-user pass cannot evaluate is refused when a
// stored row is evaluated, never answered: "completed" is not a status, so
// read_status:completed matched nothing and -read_status:completed matched every
// book, for every viewer, with no sign anything was wrong.
func TestEvaluate_RefusesPerUserValuesTheMatcherCannotEvaluate(t *testing.T) {
	store, idx, _ := collectionFixture(t)
	for _, q := range []string{
		"read_status:completed",
		"-read_status:completed",
		"read_status:fin*",
		"-read_status:fin*",
		"read_status:(finished|in-progress)",
		"progress_pct:>half",
		"-progress_pct:>abc",
		"last_played:>30d",
		"READ_STATUS:finished",
	} {
		t.Run(q, func(t *testing.T) {
			for _, user := range []string{"alice", ""} {
				ids, err := EvaluateSmartPlaylist(context.Background(), store, idx, q, "", 0, user)
				require.Error(t, err, "user %q", user)
				assert.True(t, search.IsPerUserQueryError(err), "user %q: %v", user, err)
				assert.Nil(t, ids)
			}
		})
	}
}

// The query is what is wrong, so a closed or rebuilding index must not hide it
// behind "try again shortly": the refusal comes first, and a good query on a closed
// index is still the retryable error.
func TestEvaluate_AQueryRefusalComesBeforeTheIndexState(t *testing.T) {
	store, _, _ := collectionFixture(t)
	_, err := EvaluateSmartPlaylist(context.Background(), store, nil, "read_status:finished OR title:b1", "", 0, "alice")
	require.Error(t, err)
	assert.ErrorIs(t, err, search.ErrPerUserBoolean)
	assert.NotErrorIs(t, err, ErrSearchIndexUnavailable)

	_, err = EvaluateSmartPlaylist(context.Background(), store, nil, "read_status:completed", "", 0, "alice")
	require.Error(t, err)
	assert.True(t, search.IsPerUserQueryError(err))

	_, err = EvaluateSmartPlaylist(context.Background(), store, nil, "read_status:finished", "", 0, "alice")
	assert.ErrorIs(t, err, ErrSearchIndexUnavailable)
}

// Only a failure to read the viewer's state carries ErrViewerStateRead; the log
// withholds exactly that class. A bad sort spec, a search error or a query error is
// not it.
func TestEvaluate_OnlyStateReadFailuresAreMarkedAsSuch(t *testing.T) {
	store, idx, _ := collectionFixture(t)
	_, err := EvaluateSmartPlaylist(context.Background(), store, idx, "Synthetic", "title", 0, "alice")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse sort_json")
	assert.NotErrorIs(t, err, ErrViewerStateRead)

	_, err = EvaluateSmartPlaylist(context.Background(), store, idx, "read_status:completed", "", 0, "alice")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrViewerStateRead)
}

func TestValidateSortJSON(t *testing.T) {
	for _, ok := range []string{"", "  ", "[]", `[{"field":"title","direction":"asc"}]`} {
		assert.NoError(t, ValidateSortJSON(ok), "%q", ok)
	}
	for _, bad := range []string{"title", `{"field":"title"}`, `[{"field":1}]`, "[", `"title"`} {
		err := ValidateSortJSON(bad)
		require.Error(t, err, "%q", bad)
		assert.Contains(t, err.Error(), "sort_json")
	}
}

// An evaluation whose request is gone stops: the error is the context's, so the
// caller can tell a client that left from a collection that failed.
func TestEvaluate_ACancelledContextStopsTheEvaluation(t *testing.T) {
	store, idx, _ := collectionFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, q := range []string{"Synthetic", "read_status:finished", "-read_status:finished"} {
		ids, err := EvaluateSmartPlaylist(ctx, store, idx, q, "", 0, "alice")
		require.Error(t, err, q)
		assert.True(t, errors.Is(err, context.Canceled), "%q: %v", q, err)
		assert.Nil(t, ids, q)
	}
}

// errOnlyContext reports itself cancelled through Err() but never closes Done(), so
// the index search (which watches Done) runs to completion and only the evaluator's
// own check before the sort can stop the evaluation. It pins that check on its own:
// the search honours a context too, so a context that is cancelled before the call is
// stopped by either, and removing one of them would go unnoticed.
type errOnlyContext struct{ context.Context }

func (errOnlyContext) Err() error { return context.Canceled }

func TestEvaluate_ARequestThatWentAwayDuringTheSearchDoesNotReachTheSort(t *testing.T) {
	store, idx, _ := collectionFixture(t)
	ids, err := EvaluateSmartPlaylist(errOnlyContext{context.Background()}, store, idx, "Synthetic", "", 0, "alice")
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "%v", err)
	assert.Nil(t, ids)
}
