// file: internal/playlist/evaluator_peruser_parallel_test.go
// version: 1.0.0
// guid: 8e3a6c1d-47b2-4d95-a0f8-5c2b9e7d1346
// last-edited: 2026-10-25

package playlist

import (
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
// for every size on both sides of the point where it starts splitting work.
func TestApplyPerUserFilters_ParallelKeepsOrderAndMembership(t *testing.T) {
	for _, n := range []int{0, 1, perUserFilterChunk, 2*perUserFilterChunk - 1, 2 * perUserFilterChunk, 5000} {
		ids := make([]string, n)
		var want []string
		for i := range ids {
			ids[i] = fmt.Sprintf("b%d", i)
			if i%2 == 0 {
				want = append(want, ids[i])
			}
		}
		states := &countingStates{}
		got := applyPerUserFilters(states, ids, perUserFiltersFor(t, "read_status:finished"), "alice", time.Now())
		assert.Equal(t, len(ids), int(states.reads.Load()), "n=%d: one read per candidate", n)
		if want == nil {
			want = []string{}
		}
		assert.Equal(t, want, got, "n=%d", n)
	}
}

func TestApplyPerUserFilters_NoViewerReadsNothing(t *testing.T) {
	states := &countingStates{}
	got := applyPerUserFilters(states, []string{"b1", "b2"}, perUserFiltersFor(t, "read_status:unstarted"), "", time.Now())
	assert.Equal(t, []string{}, got)
	assert.Zero(t, states.reads.Load())
}
