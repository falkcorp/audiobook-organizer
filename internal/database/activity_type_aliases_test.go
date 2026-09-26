// file: internal/database/activity_type_aliases_test.go
// version: 1.0.0
// guid: 6d2f8a41-3b7e-4c09-9e15-a4c8d0b7f362
// last-edited: 2026-09-25

package database

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A renamed op's activity rows carry the ID they were recorded under: rows from
// before the rename the former ID, rows after it the canonical one. A filter of
// Type=<canonical> plus TypeAliases=<former IDs> must return both halves as one
// newest-first history, with an exact total and correct paging, on every store
// and on both Pebble query paths (filter index and budget scan).

type typeAliasActivityStore interface {
	Record(e ActivityEntry) (int64, error)
	Query(ctx context.Context, f ActivityFilter) ([]ActivityEntry, int, error)
}

// seedTypeAliasRows writes, oldest first: 3 rows under the former ID, 2 under
// the canonical ID, and noise of another type interleaved. Rows under either
// spelling alternate between two sources so a Source filter selects a subset.
func seedTypeAliasRows(t *testing.T, s typeAliasActivityStore) {
	t.Helper()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	rows := []struct{ typ, src, summary string }{
		{"old.op", "src-a", "old 1"},
		{"noise", "src-a", "noise 1"},
		{"old.op", "src-b", "old 2"},
		{"old.op", "src-a", "old 3"},
		{"noise", "src-b", "noise 2"},
		{"new.op", "src-b", "new 1"},
		{"new.op", "src-a", "new 2"},
		{"noise", "src-a", "noise 3"},
	}
	for i, r := range rows {
		_, err := s.Record(ActivityEntry{
			Timestamp: base.Add(time.Duration(i) * time.Minute),
			Tier:      "change", Level: "info",
			Type: r.typ, Source: r.src, Summary: r.summary,
		})
		require.NoError(t, err)
	}
}

func typeAliasSummaries(es []ActivityEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Summary
	}
	return out
}

func assertTypeAliasQueries(t *testing.T, s typeAliasActivityStore) {
	t.Helper()
	ctx := context.Background()
	whole := []string{"new 2", "new 1", "old 3", "old 2", "old 1"}

	// Canonical ID plus its former ID: the whole history.
	got, total, err := s.Query(ctx, ActivityFilter{Type: "new.op", TypeAliases: []string{"old.op"}, Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, whole, typeAliasSummaries(got))
	assert.Equal(t, 5, total)

	// A page spanning the rename boundary.
	got, _, err = s.Query(ctx, ActivityFilter{Type: "new.op", TypeAliases: []string{"old.op"}, Limit: 2, Offset: 1})
	require.NoError(t, err)
	assert.Equal(t, []string{"new 1", "old 3"}, typeAliasSummaries(got))

	// Aliases combined with another indexed predicate: on the Pebble index path
	// the type family is then PROBED rather than walked, which must accept a
	// key under either spelling.
	got, _, err = s.Query(ctx, ActivityFilter{Type: "new.op", TypeAliases: []string{"old.op"}, Source: "src-a", Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, []string{"new 2", "old 3", "old 1"}, typeAliasSummaries(got))

	// Duplicate and empty aliases change nothing.
	got, _, err = s.Query(ctx, ActivityFilter{Type: "new.op", TypeAliases: []string{"", "new.op", "old.op", "old.op"}, Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, whole, typeAliasSummaries(got))

	// Without aliases, Type stays a literal match.
	got, total, err = s.Query(ctx, ActivityFilter{Type: "old.op", Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, []string{"old 3", "old 2", "old 1"}, typeAliasSummaries(got))
	assert.Equal(t, 3, total)

	// Aliases without Type are ignored: no type predicate at all.
	_, total, err = s.Query(ctx, ActivityFilter{TypeAliases: []string{"old.op"}, Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 8, total)
}

func TestActivityTypeAliases_Pebble(t *testing.T) {
	for _, indexed := range []bool{true, false} {
		name := "budget-scan"
		if indexed {
			name = "filter-index"
		}
		t.Run(name, func(t *testing.T) {
			s := newTestPebbleActivityStore(t)
			seedTypeAliasRows(t, s)
			setFilterIndexReady(t, s, indexed)
			require.Equal(t, indexed, s.FilterIndexBackfillDone())
			assertTypeAliasQueries(t, s)
		})
	}
}

func TestActivityTypeAliases_SQL(t *testing.T) {
	s := newTestSQLStore(t)
	seedTypeAliasRows(t, s)
	assertTypeAliasQueries(t, s)
}

func TestActivityFilter_TypeValues(t *testing.T) {
	assert.Nil(t, ActivityFilter{TypeAliases: []string{"x"}}.TypeValues())
	assert.Equal(t, []string{"a"}, ActivityFilter{Type: "a"}.TypeValues())
	assert.Equal(t, []string{"a", "b"}, ActivityFilter{Type: "a", TypeAliases: []string{"", "a", "b", "b"}}.TypeValues())

	f := ActivityFilter{Type: "a", TypeAliases: []string{"b", ""}}
	assert.True(t, f.acceptsType("a"))
	assert.True(t, f.acceptsType("b"))
	assert.False(t, f.acceptsType(""), "an empty alias must not match an untyped row")
	assert.False(t, f.acceptsType("c"))
	assert.True(t, ActivityFilter{}.acceptsType("anything"))
}
