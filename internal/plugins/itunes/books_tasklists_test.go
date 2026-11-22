// file: internal/plugins/itunes/books_tasklists_test.go
// version: 0.1.0
// guid: 2f9c6e18-a437-4b05-8d71-c5e0b3a91f64
// last-edited: 2026-11-22

package itunes

import (
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"
)

func TestBooksTaskLists_BuildsTheKindWithItsSchedule(t *testing.T) {
	sched := tasks.Every(37 * time.Minute)
	asked := map[tasks.Kind]bool{}
	defs, err := BooksTaskLists(BooksTaskInputs{Schedules: func(k tasks.Kind) *tasks.Schedule {
		asked[k] = true
		return sched
	}})
	require.NoError(t, err)
	require.Len(t, defs, 1)
	require.Empty(t, tasks.Problems(defs[0]))
	require.True(t, asked[defs[0].Kind()])
	// CommonOf deep-copies the schedule, so identity is not observable: compare
	// what the copy reports.
	got := tasks.CommonOf(defs[0]).Schedule
	require.NotNil(t, got)
	require.Equal(t, sched.Interval(), got.Interval())
}

func TestBooksTaskLists_NilScheduleHookRefuses(t *testing.T) {
	_, err := BooksTaskLists(BooksTaskInputs{})
	require.ErrorContains(t, err, "Schedules")
}
