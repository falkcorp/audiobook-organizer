// file: internal/playlist/evaluator_viewer_states_test.go
// version: 1.1.0
// guid: 5a9d2e47-8c13-4f60-b7a1-d3e6f0928c5b
// last-edited: 2026-10-30

package playlist

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// referenceEvaluate answers a query from its boolean TREE, one book at a time:
// AND is all, OR is any, NOT is the complement, and each leaf is judged on its
// own (an indexed term by searching for it alone, a per-user term against the
// viewer's state row). It shares nothing with the translator's flattening of
// per-user terms into one AND list, so it is the independent statement of what a
// query means that the evaluator must agree with, for every shape the evaluator
// accepts (see TestEvaluate_PerUserOrAndGroupedNotMatchTheReference for the
// per-user terms under an OR or a NOT over a group).
func referenceEvaluate(t *testing.T, store *database.PebbleStore, idx *search.BleveIndex, query, userID string) []string {
	t.Helper()
	ast, err := search.ParseQuery(query)
	require.NoError(t, err)
	now := time.Now()
	all, _, err := search.TranslateAt(nil, now)
	require.NoError(t, err)
	allIDs, err := searchAllIDs(context.Background(), idx, all, 0)
	require.NoError(t, err)

	indexedHits := map[search.Node]map[string]bool{}
	leaf := func(n search.Node, id string) bool {
		bq, filters, err := search.TranslateAt(n, now)
		require.NoError(t, err)
		if len(filters) > 0 {
			st, err := store.GetUserBookState(userID, id)
			require.NoError(t, err)
			return search.MatchPerUserFilters(st, filters, now)
		}
		hits, ok := indexedHits[n]
		if !ok {
			ids, err := searchAllIDs(context.Background(), idx, bq, 0)
			require.NoError(t, err)
			hits = make(map[string]bool, len(ids))
			for _, h := range ids {
				hits[h] = true
			}
			indexedHits[n] = hits
		}
		return hits[id]
	}
	var holds func(n search.Node, id string) bool
	holds = func(n search.Node, id string) bool {
		switch v := n.(type) {
		case *search.AndNode:
			for _, c := range v.Children {
				if !holds(c, id) {
					return false
				}
			}
			return true
		case *search.OrNode:
			for _, c := range v.Children {
				if holds(c, id) {
					return true
				}
			}
			return false
		case *search.NotNode:
			return !holds(v.Child, id)
		default:
			return leaf(n, id)
		}
	}
	var ids []string
	for _, id := range allIDs {
		if holds(ast, id) {
			ids = append(ids, id)
		}
	}
	out, err := sortBookIDs(context.Background(), store, ids, "")
	require.NoError(t, err)
	if out == nil {
		out = []string{}
	}
	return out
}

// The bulk-read evaluator answers exactly what the per-candidate evaluator did,
// for every shape of per-user query the evaluator accepts: positive and negated
// filters, unstarted, per-user only, mixed with an indexed term, an alternation of
// one per-user field, and an OR that holds no per-user term.
func TestEvaluate_ViewerStatesMatchThePerCandidateReference(t *testing.T) {
	store, idx, _ := collectionFixture(t)
	queries := []string{
		"read_status:finished",
		"-read_status:finished",
		"read_status:unstarted",
		"read_status:in_progress",
		"progress_pct:>0",
		"progress_pct:<50",
		"last_played:>-30d",
		"-last_played:>-30d",
		"last_played:*",
		"read_status:finished last_played:>-30d",
		"Synthetic read_status:finished",
		"Synthetic -read_status:finished",
		"title:b1 read_status:unstarted",
		"title:b2 OR title:b4",
		"read_status:(finished|in_progress)",
		"-read_status:(finished|in_progress)",
		"(title:b2 OR title:b4) read_status:finished",
		"-read_status:finished title:b2",
		"NOT NOT read_status:finished",
		// two negations of a group cancel: a plain group of terms
		"NOT NOT (read_status:finished title:b2)",
		"-(-(title:b2 read_status:finished))",
		"-(title:b1 OR title:b3) read_status:unstarted",
		"title:nomatch read_status:unstarted",
	}
	for _, user := range []string{"alice", "bob", "carol"} {
		for _, q := range queries {
			t.Run(user+"/"+q, func(t *testing.T) {
				want := referenceEvaluate(t, store, idx, q, user)
				got, err := EvaluateSmartPlaylist(context.Background(), store, idx, q, "", 0, user)
				require.NoError(t, err)
				assert.Equal(t, want, got)
			})
		}
	}
}

// Sort and limit apply after the per-user pass, on the whole answer.
func TestEvaluate_ViewerStatesWithSortAndLimit(t *testing.T) {
	store, idx, _ := collectionFixture(t)
	sortJSON := `[{"field":"title","direction":"desc"}]`
	got, err := EvaluateSmartPlaylist(context.Background(), store, idx, "read_status:finished", sortJSON, 1, "alice")
	require.NoError(t, err)
	assert.Equal(t, []string{"b2"}, got, "alice finished b1 and b2; title desc, limit 1")
	got, err = EvaluateSmartPlaylist(context.Background(), store, idx, "-read_status:finished", sortJSON, 0, "alice")
	require.NoError(t, err)
	assert.Equal(t, []string{"b4", "b3"}, got)
}

// countingStore answers from a real store and counts how it was read.
type countingStore struct {
	real       *database.PebbleStore
	listCalls  atomic.Int64
	pointReads atomic.Int64
	listErr    error
}

func (c *countingStore) GetBookByID(id string) (*database.Book, error) { return c.real.GetBookByID(id) }
func (c *countingStore) GetUserBookState(u, b string) (*database.UserBookState, error) {
	c.pointReads.Add(1)
	return c.real.GetUserBookState(u, b)
}
func (c *countingStore) ListUserBookStates(ctx context.Context, u string) (map[string]*database.UserBookState, error) {
	c.listCalls.Add(1)
	if c.listErr != nil {
		return nil, c.listErr
	}
	return c.real.ListUserBookStates(ctx, u)
}

// A viewer's state is read with ONE prefix scan and then answered from memory:
// no point read per candidate book, whatever the filter.
func TestEvaluate_ReadsTheViewersRowsOnceAndNeverPerCandidate(t *testing.T) {
	store, idx, _ := collectionFixture(t)
	cs := &countingStore{real: store}
	for _, q := range []string{"read_status:finished", "-read_status:finished", "read_status:unstarted", "Synthetic progress_pct:<50"} {
		cs.listCalls.Store(0)
		_, err := EvaluateSmartPlaylist(context.Background(), cs, idx, q, "", 0, "alice")
		require.NoError(t, err, q)
		assert.Equal(t, int64(1), cs.listCalls.Load(), "%q: one prefix scan", q)
		assert.Zero(t, cs.pointReads.Load(), "%q: no point read per candidate", q)
	}

	// A preloaded viewer is not loaded again: one scan serves every collection on
	// a page.
	cs.listCalls.Store(0)
	viewer, err := LoadViewerStates(context.Background(), cs, "alice")
	require.NoError(t, err)
	for _, q := range []string{"read_status:finished", "-read_status:finished", "read_status:unstarted"} {
		_, err := EvaluateSmartPlaylistFor(context.Background(), cs, idx, q, "", 0, viewer)
		require.NoError(t, err)
	}
	assert.Equal(t, int64(1), cs.listCalls.Load(), "three evaluations, one scan")
}

// A viewer whose rows cannot be read FAILS the evaluation. Treated as "no
// state", the book matches every negated or unstarted filter.
func TestEvaluate_AStateReadErrorFailsTheEvaluation(t *testing.T) {
	store, idx, _ := collectionFixture(t)
	cs := &countingStore{real: store, listErr: errors.New("disk on fire")}
	got, err := EvaluateSmartPlaylist(context.Background(), cs, idx, "-read_status:finished", "", 0, "alice")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disk on fire")
	assert.Nil(t, got)
	// The class the log redacts: marked, with the cause still reachable.
	assert.ErrorIs(t, err, ErrViewerStateRead)
	assert.ErrorIs(t, err, cs.listErr)
}

// A store that cannot list a user's rows (a test double, a non-Pebble backend)
// takes the point-read path and gives the same answer.
func TestEvaluate_FallsBackToPointReadsWithoutTheCapability(t *testing.T) {
	store, idx, _ := collectionFixture(t)
	plain := struct {
		bookLookup
		userStateReader
	}{store, store}
	for _, q := range []string{"read_status:finished", "-read_status:finished", "read_status:unstarted"} {
		want := referenceEvaluate(t, store, idx, q, "alice")
		got, err := EvaluateSmartPlaylist(context.Background(), plain, idx, q, "", 0, "alice")
		require.NoError(t, err, q)
		assert.Equal(t, want, got, q)
	}
}

// NO 10,000-DOCUMENT WINDOW. The search used to return the first 10,000 hits by
// id, so on a 122k-book library `read_status:finished` looked only at the oldest
// tenth and a viewer who finished recent books saw none of them, silently. The
// initial window is forced to 3 here: every hit must still come back.
func TestEvaluate_NoResultWindowTruncatesTheAnswer(t *testing.T) {
	old := evalWindowOverride
	evalWindowOverride = 3
	t.Cleanup(func() { evalWindowOverride = old })

	store, idx, _ := collectionFixture(t)
	cases := []struct {
		query, user string
		want        []string
	}{
		{"Synthetic", "alice", []string{"b1", "b2", "b3", "b4"}},             // no per-user filter
		{"read_status:unstarted", "carol", []string{"b1", "b2", "b3", "b4"}}, // library-scale
		{"-read_status:finished", "bob", []string{"b1", "b3"}},               // negated
		{"Synthetic -last_played:>-30d", "alice", []string{"b2", "b4"}},      // mixed
	}
	for _, tc := range cases {
		got, err := EvaluateSmartPlaylist(context.Background(), store, idx, tc.query, "", 0, tc.user)
		require.NoError(t, err, tc.query)
		assert.Equal(t, tc.want, got, "%q as %s", tc.query, tc.user)
	}
}

// The same property at the real scale: more than 10,000 indexed books, with the
// viewer's finished books all NEWER than the first 10,000 by id.
func TestEvaluate_OverTenThousandBooks(t *testing.T) {
	if testing.Short() {
		t.Skip("indexes 10,300 books")
	}
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	idx, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = idx.Close() })
	require.NoError(t, idx.MarkRebuilt())

	const total = 10300
	docs := make([]search.BookDocument, total)
	for i := range docs {
		docs[i] = search.BookDocument{BookID: fmt.Sprintf("b%06d", i), Title: "Synthetic"}
	}
	require.NoError(t, idx.IndexBookBatch(docs))
	var finished []string
	for i := total - 40; i < total; i++ {
		id := fmt.Sprintf("b%06d", i)
		finished = append(finished, id)
		require.NoError(t, store.SetUserBookState(&database.UserBookState{
			UserID: "bob", BookID: id, Status: database.UserBookStatusFinished, LastActivityAt: time.Now(),
		}))
	}

	got, err := EvaluateSmartPlaylist(context.Background(), store, idx, "read_status:finished", "", 0, "bob")
	require.NoError(t, err)
	assert.Equal(t, finished, got, "40 finished books, all newer than the first 10,000")

	got, err = EvaluateSmartPlaylist(context.Background(), store, idx, "read_status:unstarted", "", 0, "bob")
	require.NoError(t, err)
	assert.Len(t, got, total-40, "unstarted must cover the whole library, not its first 10,000")
	assert.False(t, slices.Contains(got, finished[0]))

	got, err = EvaluateSmartPlaylist(context.Background(), store, idx, "-read_status:finished", "", 0, "bob")
	require.NoError(t, err)
	assert.Len(t, got, total-40)

	got, err = EvaluateSmartPlaylist(context.Background(), store, idx, "Synthetic", "", 0, "bob")
	require.NoError(t, err)
	assert.Len(t, got, total, "a query with no per-user filter is uncapped too")
}

// Per-user terms under an OR with a sibling, or under a NOT over a group, are
// evaluated as written (search.MixedQuery), for every viewer: each of these queries
// answers exactly what the tree-walking reference above answers. They were refused
// until 2026-10-30, and before that evaluated as an AND.
func TestEvaluate_PerUserOrAndGroupedNotMatchTheReference(t *testing.T) {
	store, idx, _ := collectionFixture(t)
	queries := []string{
		"read_status:finished OR title:b4",
		"title:b4 OR read_status:finished",
		"read_status:finished OR read_status:unstarted",
		"title:b2 OR (title:b4 AND progress_pct:>0)",
		"title:b4 OR read_status:(finished|in_progress)",
		"title:b4 OR -read_status:finished",
		"(title:b4 OR read_status:finished) AND title:b2",
		"(title:b4 OR read_status:in_progress) title:b3",
		"title:b3 OR last_played:>-30d",
		"title:b1 OR read_status:unstarted",
		"-(read_status:finished title:b2)",
		"NOT (read_status:finished OR title:b4)",
		"NOT (title:b1 (title:b2 OR last_played:*))",
		"Synthetic -(title:b1 OR read_status:finished)",
	}
	for _, user := range []string{"alice", "bob", "carol"} {
		for _, q := range queries {
			t.Run(user+"/"+q, func(t *testing.T) {
				want := referenceEvaluate(t, store, idx, q, user)
				got, err := EvaluateSmartPlaylist(context.Background(), store, idx, q, "", 0, user)
				require.NoError(t, err)
				assert.Equal(t, want, got)
			})
		}
	}

	// The exact answers for alice (finished b1, b2; in progress b3; no row for b4).
	for q, want := range map[string][]string{
		"read_status:finished OR title:b4":            {"b1", "b2", "b4"},
		"(title:b4 OR read_status:finished) title:b2": {"b2"},
		"NOT (read_status:finished OR title:b4)":      {"b3"},
	} {
		got, err := EvaluateSmartPlaylist(context.Background(), store, idx, q, "", 0, "alice")
		require.NoError(t, err, q)
		assert.Equal(t, want, got, q)
	}
}

// No viewer, no match: the rule for the flat path holds for the tree route, so a
// system caller is never answered with the zero-value state's reading of a
// per-user term under an OR (title:b4 OR read_status:unstarted would be every book).
func TestEvaluate_PerUserOrWithNoViewerMatchesNothing(t *testing.T) {
	store, idx, _ := collectionFixture(t)
	for _, q := range []string{
		"read_status:finished OR title:b4",
		"title:b4 OR read_status:unstarted",
		"-(read_status:finished title:b4)",
	} {
		ids, err := EvaluateSmartPlaylist(context.Background(), store, idx, q, "", 0, "")
		require.NoError(t, err, q)
		assert.Empty(t, ids, q)
		assert.NotNil(t, ids, q)
	}
}

// The tree route reads the viewer's rows once (one scan, no point read per book),
// and a preloaded viewer is not read again.
func TestEvaluate_PerUserOrReadsTheViewersRowsOnce(t *testing.T) {
	store, idx, _ := collectionFixture(t)
	cs := &countingStore{real: store}
	_, err := EvaluateSmartPlaylist(context.Background(), cs, idx, "read_status:finished OR title:b4", "", 0, "alice")
	require.NoError(t, err)
	assert.Equal(t, int64(1), cs.listCalls.Load())
	assert.Zero(t, cs.pointReads.Load())

	cs.listCalls.Store(0)
	viewer, err := LoadViewerStates(context.Background(), cs, "alice")
	require.NoError(t, err)
	for _, q := range []string{"read_status:finished OR title:b4", "-(read_status:finished title:b2)"} {
		_, err := EvaluateSmartPlaylistFor(context.Background(), cs, idx, q, "", 0, viewer)
		require.NoError(t, err)
	}
	assert.Equal(t, int64(1), cs.listCalls.Load(), "two evaluations, one scan")
}

// pointReadStore cannot list a viewer's rows, so the tree route reads every indexed
// book's row by id; the answer is the same.
type pointReadStore struct {
	real       *database.PebbleStore
	pointReads atomic.Int64
	failOn     string
}

func (p *pointReadStore) GetBookByID(id string) (*database.Book, error) {
	return p.real.GetBookByID(id)
}
func (p *pointReadStore) GetUserBookState(u, b string) (*database.UserBookState, error) {
	p.pointReads.Add(1)
	if b == p.failOn {
		return nil, errors.New("disk")
	}
	return p.real.GetUserBookState(u, b)
}

func TestEvaluate_PerUserOrOnAStoreThatCannotListReadsEveryRowByID(t *testing.T) {
	store, idx, _ := collectionFixture(t)
	for _, q := range []string{"read_status:finished OR title:b4", "-(read_status:finished title:b2)", "title:b4 OR read_status:unstarted"} {
		ps := &pointReadStore{real: store}
		got, err := EvaluateSmartPlaylist(context.Background(), ps, idx, q, "", 0, "alice")
		require.NoError(t, err, q)
		assert.Equal(t, referenceEvaluate(t, store, idx, q, "alice"), got, q)
		assert.EqualValues(t, 4, ps.pointReads.Load(), "%q: one read per indexed book", q)
	}

	// A read error fails the evaluation (it is not read as "no row").
	_, err := EvaluateSmartPlaylist(context.Background(), &pointReadStore{real: store, failOn: "b3"}, idx, "read_status:finished OR title:b4", "", 0, "alice")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrViewerStateRead)
}

// The refusals that remain are about a per-user VALUE, not a shape.
func TestEvaluate_StillRefusesAPerUserValueItCannotEvaluate(t *testing.T) {
	store, idx, _ := collectionFixture(t)
	for _, q := range []string{"read_status:completed OR title:b4", "title:b4 OR -read_status:fin*"} {
		ids, err := EvaluateSmartPlaylist(context.Background(), store, idx, q, "", 0, "alice")
		require.Error(t, err, q)
		assert.True(t, search.IsPerUserQueryError(err), q)
		assert.Nil(t, ids)
	}
}

// The wrong answer this change replaces: the OR evaluated as an AND returned only
// the books where BOTH sides held.
func TestEvaluate_TheOrIsNoLongerAnAnd(t *testing.T) {
	store, idx, _ := collectionFixture(t)
	or, err := EvaluateSmartPlaylist(context.Background(), store, idx, "read_status:finished OR title:b4", "", 0, "alice")
	require.NoError(t, err)
	assert.Equal(t, []string{"b1", "b2", "b4"}, or, "alice finished b1 and b2; b4 matches by title")
	and, err := EvaluateSmartPlaylist(context.Background(), store, idx, "read_status:finished title:b4", "", 0, "alice")
	require.NoError(t, err)
	assert.NotEqual(t, or, and, "AND answers fewer books than OR: that was the silent wrong answer")
}
