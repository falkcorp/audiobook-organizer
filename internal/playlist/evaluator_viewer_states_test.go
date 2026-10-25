// file: internal/playlist/evaluator_viewer_states_test.go
// version: 1.0.0
// guid: 5a9d2e47-8c13-4f60-b7a1-d3e6f0928c5b
// last-edited: 2026-10-25

package playlist

import (
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

// referenceEvaluate is the evaluator as it was before the viewer-state rewrite,
// minus its 10,000-document window: search for EVERY indexed match, read one
// state row per candidate, keep the ones every filter accepts. It is the
// semantics the rewrite must preserve, so the rewrite is compared against it
// rather than against hand-written expectations.
func referenceEvaluate(t *testing.T, store *database.PebbleStore, idx *search.BleveIndex, query, userID string) []string {
	t.Helper()
	ast, err := search.ParseQuery(query)
	require.NoError(t, err)
	now := time.Now()
	bleveQ, filters, err := search.TranslateAt(ast, now)
	require.NoError(t, err)
	n, err := idx.DocCount()
	require.NoError(t, err)
	hits, _, err := idx.SearchNative(bleveQ, 0, int(n))
	require.NoError(t, err)
	var ids []string
	for _, h := range hits {
		if len(filters) > 0 {
			st, err := store.GetUserBookState(userID, h.BookID)
			require.NoError(t, err)
			if !search.MatchPerUserFilters(st, filters, now) {
				continue
			}
		}
		ids = append(ids, h.BookID)
	}
	out, err := sortBookIDs(store, ids, "")
	require.NoError(t, err)
	if out == nil {
		out = []string{}
	}
	return out
}

// The bulk-read evaluator answers exactly what the per-candidate evaluator did,
// for every shape of per-user query: positive and negated filters, unstarted,
// per-user only, mixed with an indexed term, and per-user terms inside an OR.
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
		"read_status:finished OR title:b4",
		"(read_status:finished | read_status:in_progress)",
		"title:nomatch read_status:unstarted",
	}
	for _, user := range []string{"alice", "bob", "carol"} {
		for _, q := range queries {
			t.Run(user+"/"+q, func(t *testing.T) {
				want := referenceEvaluate(t, store, idx, q, user)
				got, err := EvaluateSmartPlaylist(store, idx, q, "", 0, user)
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
	got, err := EvaluateSmartPlaylist(store, idx, "read_status:finished", sortJSON, 1, "alice")
	require.NoError(t, err)
	assert.Equal(t, []string{"b2"}, got, "alice finished b1 and b2; title desc, limit 1")
	got, err = EvaluateSmartPlaylist(store, idx, "-read_status:finished", sortJSON, 0, "alice")
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
func (c *countingStore) ListUserBookStates(u string) (map[string]*database.UserBookState, error) {
	c.listCalls.Add(1)
	if c.listErr != nil {
		return nil, c.listErr
	}
	return c.real.ListUserBookStates(u)
}

// A viewer's state is read with ONE prefix scan and then answered from memory:
// no point read per candidate book, whatever the filter.
func TestEvaluate_ReadsTheViewersRowsOnceAndNeverPerCandidate(t *testing.T) {
	store, idx, _ := collectionFixture(t)
	cs := &countingStore{real: store}
	for _, q := range []string{"read_status:finished", "-read_status:finished", "read_status:unstarted", "Synthetic progress_pct:<50"} {
		cs.listCalls.Store(0)
		_, err := EvaluateSmartPlaylist(cs, idx, q, "", 0, "alice")
		require.NoError(t, err, q)
		assert.Equal(t, int64(1), cs.listCalls.Load(), "%q: one prefix scan", q)
		assert.Zero(t, cs.pointReads.Load(), "%q: no point read per candidate", q)
	}

	// A preloaded viewer is not loaded again: one scan serves every collection on
	// a page.
	cs.listCalls.Store(0)
	viewer, err := LoadViewerStates(cs, "alice")
	require.NoError(t, err)
	for _, q := range []string{"read_status:finished", "-read_status:finished", "read_status:unstarted"} {
		_, err := EvaluateSmartPlaylistFor(cs, idx, q, "", 0, viewer)
		require.NoError(t, err)
	}
	assert.Equal(t, int64(1), cs.listCalls.Load(), "three evaluations, one scan")
}

// A viewer whose rows cannot be read FAILS the evaluation. Treated as "no
// state", the book matches every negated or unstarted filter.
func TestEvaluate_AStateReadErrorFailsTheEvaluation(t *testing.T) {
	store, idx, _ := collectionFixture(t)
	cs := &countingStore{real: store, listErr: errors.New("disk on fire")}
	got, err := EvaluateSmartPlaylist(cs, idx, "-read_status:finished", "", 0, "alice")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disk on fire")
	assert.Nil(t, got)
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
		got, err := EvaluateSmartPlaylist(plain, idx, q, "", 0, "alice")
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
		got, err := EvaluateSmartPlaylist(store, idx, tc.query, "", 0, tc.user)
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

	got, err := EvaluateSmartPlaylist(store, idx, "read_status:finished", "", 0, "bob")
	require.NoError(t, err)
	assert.Equal(t, finished, got, "40 finished books, all newer than the first 10,000")

	got, err = EvaluateSmartPlaylist(store, idx, "read_status:unstarted", "", 0, "bob")
	require.NoError(t, err)
	assert.Len(t, got, total-40, "unstarted must cover the whole library, not its first 10,000")
	assert.False(t, slices.Contains(got, finished[0]))

	got, err = EvaluateSmartPlaylist(store, idx, "-read_status:finished", "", 0, "bob")
	require.NoError(t, err)
	assert.Len(t, got, total-40)

	got, err = EvaluateSmartPlaylist(store, idx, "Synthetic", "", 0, "bob")
	require.NoError(t, err)
	assert.Len(t, got, total, "a query with no per-user filter is uncapped too")
}
