// file: internal/playlist/evaluator_peruser_parallel_test.go
// version: 2.0.0
// guid: 8e3a6c1d-47b2-4d95-a0f8-5c2b9e7d1346
// last-edited: 2026-10-25

package playlist

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// countingStates answers every even-numbered book as finished and counts reads.
type countingStates struct{ reads atomic.Int64 }

func (c *countingStates) GetUserBookState(userID, bookID string) (*database.UserBookState, error) {
	c.reads.Add(1)
	var n int
	_, _ = fmt.Sscanf(bookID, "b%d", &n)
	if n%2 == 0 {
		return &database.UserBookState{UserID: userID, BookID: bookID, Status: database.UserBookStatusFinished}, nil
	}
	return nil, nil
}

func perUserFiltersFor(t *testing.T, query string) []search.PerUserFilter {
	t.Helper()
	ast, err := search.ParseQuery(query)
	require.NoError(t, err)
	_, filters, err := search.TranslateAt(ast, time.Now())
	require.NoError(t, err)
	require.NotEmpty(t, filters)
	return filters
}

// The worker pool must give the same answer, in the same order, as one pass,
// for every size on both sides of the point where it starts splitting work. The
// worker count is passed in, not read from runtime.NumCPU, so the pooled branch
// runs on a 1-CPU CI agent too (it used to be reachable only with two or more).
func TestJudgeByPointReads_ParallelKeepsOrderAndMembership(t *testing.T) {
	for _, n := range []int{0, 1, perUserFilterChunk, 2*perUserFilterChunk - 1, 2 * perUserFilterChunk, 5000} {
		for _, workers := range []int{1, 4} {
			ids := make([]string, n)
			var want []string
			for i := range ids {
				ids[i] = fmt.Sprintf("b%d", i)
				if i%2 == 0 {
					want = append(want, ids[i])
				}
			}
			states := &countingStates{}
			viewer := &ViewerStates{userID: "alice", reader: states}
			got, err := judgeByPointReads(viewer, ids, perUserFiltersFor(t, "read_status:finished"), time.Now(), workers)
			require.NoError(t, err)
			assert.Equal(t, len(ids), int(states.reads.Load()), "n=%d workers=%d: one read per candidate", n, workers)
			if want == nil {
				want = []string{}
			}
			assert.Equal(t, want, got, "n=%d workers=%d", n, workers)
		}
	}
}

// The pooled branch really splits the work: with four workers and enough
// candidates, more than one goroutine runs fn.
func TestParallelRanges_UsesThePoolWhenThereIsEnoughWork(t *testing.T) {
	var ranges atomic.Int64
	n := 4 * perUserFilterChunk
	seen := make([]bool, n)
	require.NoError(t, parallelRanges(n, 4, perUserFilterChunk, func(lo, hi int) error {
		ranges.Add(1)
		for i := lo; i < hi; i++ {
			seen[i] = true
		}
		return nil
	}))
	assert.Equal(t, int64(4), ranges.Load(), "four chunks of work and four workers: four ranges")
	for i, ok := range seen {
		require.True(t, ok, "index %d was never visited", i)
	}
	ranges.Store(0)
	require.NoError(t, parallelRanges(n, 1, perUserFilterChunk, func(lo, hi int) error { ranges.Add(1); return nil }))
	assert.Equal(t, int64(1), ranges.Load(), "one worker runs inline")
}

// A state-read error must FAIL the evaluation. It used to be dropped, which
// evaluated the book as having no state: under a negated or unstarted filter a
// store error then turned the book into a member.
type failingStates struct{ failOn string }

func (f failingStates) GetUserBookState(userID, bookID string) (*database.UserBookState, error) {
	if bookID == f.failOn {
		return nil, errors.New("disk on fire")
	}
	return nil, nil
}

func TestJudgeByPointReads_AReadErrorFailsTheEvaluation(t *testing.T) {
	ids := make([]string, 3*perUserFilterChunk)
	for i := range ids {
		ids[i] = fmt.Sprintf("b%d", i)
	}
	for _, workers := range []int{1, 4} {
		viewer := &ViewerStates{userID: "alice", reader: failingStates{failOn: ids[len(ids)-7]}}
		got, err := judgeByPointReads(viewer, ids, perUserFiltersFor(t, "-read_status:finished"), time.Now(), workers)
		require.Error(t, err, "workers=%d: a failed read must not count as no state", workers)
		assert.Contains(t, err.Error(), "disk on fire")
		assert.Nil(t, got)
	}
}
