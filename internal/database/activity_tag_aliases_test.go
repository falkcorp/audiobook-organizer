// file: internal/database/activity_tag_aliases_test.go
// version: 1.0.1
// guid: 9e4a7c12-5f38-4d6b-b0e1-3a8d2c7f6b95
// last-edited: 2026-09-26

package database

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Op activity rows are tagged def:<the def ID the run used> and never
// rewritten, so a renamed op's rows carry def:<former> before the rename and
// def:<canonical> after it. A Tags entry with TagAliases is one AND term that
// any of its spellings satisfies; the other Tags entries stay required. Every
// store, and both Pebble query paths, must agree.

// seedTagAliasRows writes, oldest first, rows under both def: spellings, some
// also tagged "failed", plus noise under another def and untagged rows.
func seedTagAliasRows(t *testing.T, s typeAliasActivityStore) {
	t.Helper()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	rows := []struct {
		summary string
		tags    []string
	}{
		{"old 1", []string{"def:library.optimize"}},
		{"noise 1", []string{"def:other.op"}},
		{"old 2", []string{"def:library.optimize", "failed"}},
		{"plain 1", nil},
		{"new 1", []string{"def:maintenance.library-optimize"}},
		{"noise 2", []string{"def:other.op", "failed"}},
		{"new 2", []string{"failed", "def:maintenance.library-optimize"}},
	}
	for i, r := range rows {
		_, err := s.Record(ActivityEntry{
			Timestamp: base.Add(time.Duration(i) * time.Minute),
			Tier:      "change", Level: "info",
			Type: "op", Source: "src", Summary: r.summary, Tags: r.tags,
		})
		require.NoError(t, err)
	}
}

func assertTagAliasQueries(t *testing.T, s typeAliasActivityStore) {
	t.Helper()
	ctx := context.Background()
	canonical := "def:maintenance.library-optimize"
	aliases := map[string][]string{canonical: {"def:library.optimize"}}

	// Canonical tag plus its former spelling: the whole history.
	got, total, err := s.Query(ctx, ActivityFilter{Tags: []string{canonical}, TagAliases: aliases, Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, []string{"new 2", "new 1", "old 2", "old 1"}, typeAliasSummaries(got))
	assert.Equal(t, 4, total)

	// A page spanning the rename boundary.
	got, _, err = s.Query(ctx, ActivityFilter{Tags: []string{canonical}, TagAliases: aliases, Limit: 2, Offset: 1})
	require.NoError(t, err)
	assert.Equal(t, []string{"new 1", "old 2"}, typeAliasSummaries(got))

	// The group is ONE AND term: a second required tag still applies, and a
	// row carrying it under the other op's def does not qualify.
	got, total, err = s.Query(ctx, ActivityFilter{Tags: []string{canonical, "failed"}, TagAliases: aliases, Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, []string{"new 2", "old 2"}, typeAliasSummaries(got))
	assert.Equal(t, 2, total)

	// Aliases for the second tag too: both terms are any-of groups. "old 1"
	// satisfies both through its one def:library.optimize tag; "new 1"
	// satisfies only the first.
	got, _, err = s.Query(ctx, ActivityFilter{
		Tags:       []string{canonical, "failed"},
		TagAliases: map[string][]string{canonical: {"def:library.optimize"}, "failed": {"def:library.optimize"}},
		Limit:      50,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"new 2", "old 2", "old 1"}, typeAliasSummaries(got))

	// With an indexed predicate (Source) alongside: on the Pebble filter-index
	// path the act:src: family is walked and the tag groups are decided by the
	// residual matchesFilter check on each candidate.
	got, total, err = s.Query(ctx, ActivityFilter{Source: "src", Tags: []string{canonical}, TagAliases: aliases, Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, []string{"new 2", "new 1", "old 2", "old 1"}, typeAliasSummaries(got))
	assert.Equal(t, 4, total)
	got, _, err = s.Query(ctx, ActivityFilter{Source: "src", Tags: []string{canonical, "failed"}, TagAliases: aliases, Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, []string{"new 2", "old 2"}, typeAliasSummaries(got))

	// Empty and duplicate aliases change nothing.
	got, _, err = s.Query(ctx, ActivityFilter{
		Tags:       []string{canonical},
		TagAliases: map[string][]string{canonical: {"", canonical, "def:library.optimize", "def:library.optimize"}},
		Limit:      50,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"new 2", "new 1", "old 2", "old 1"}, typeAliasSummaries(got))

	// Without aliases a tag stays a literal match.
	got, total, err = s.Query(ctx, ActivityFilter{Tags: []string{"def:library.optimize"}, Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, []string{"old 2", "old 1"}, typeAliasSummaries(got))
	assert.Equal(t, 2, total)

	// An alias keyed by a tag the filter does not require is ignored.
	_, total, err = s.Query(ctx, ActivityFilter{TagAliases: aliases, Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 7, total)
}

func TestActivityTagAliases_Nuts(t *testing.T) {
	s := newTestNutsActivityStore(t)
	seedTagAliasRows(t, s)
	assertTagAliasQueries(t, s)
}

func TestActivityTagAliases_SQL(t *testing.T) {
	s := newTestSQLStore(t)
	seedTagAliasRows(t, s)
	assertTagAliasQueries(t, s)
}

func TestActivityTagAliases_Pebble(t *testing.T) {
	for _, indexed := range []bool{true, false} {
		name := "budget-scan"
		if indexed {
			name = "filter-index"
		}
		t.Run(name, func(t *testing.T) {
			s := newTestPebbleActivityStore(t)
			seedTagAliasRows(t, s)
			setFilterIndexReady(t, s, indexed)
			require.Equal(t, indexed, s.FilterIndexBackfillDone())
			assertTagAliasQueries(t, s)
		})
	}
}

// The Pebble sources cache is keyed by the filter; a filter whose tag carries
// aliases must not share a slot with the literal one.
func TestPactSourcesCacheKey_TagAliases(t *testing.T) {
	literal := ActivityFilter{Tags: []string{"def:new"}}
	aliased := ActivityFilter{Tags: []string{"def:new"}, TagAliases: map[string][]string{"def:new": {"def:old"}}}
	assert.NotEqual(t, pactSourcesCacheKey(literal), pactSourcesCacheKey(aliased))
	// An alias for a tag the filter does not require changes nothing.
	ignored := ActivityFilter{Tags: []string{"def:new"}, TagAliases: map[string][]string{"x": {"y"}}}
	assert.Equal(t, pactSourcesCacheKey(literal), pactSourcesCacheKey(ignored))
}

func TestActivityFilter_TagTerms(t *testing.T) {
	assert.Nil(t, ActivityFilter{TagAliases: map[string][]string{"a": {"b"}}}.TagTerms())
	assert.Equal(t, [][]string{{"a"}, {"c", "d"}}, ActivityFilter{
		Tags:       []string{"a", "c"},
		TagAliases: map[string][]string{"c": {"", "c", "d", "d"}},
	}.TagTerms())

	f := ActivityFilter{Tags: []string{"a", "c"}, TagAliases: map[string][]string{"c": {"d", ""}}}
	assert.True(t, f.acceptsTags([]string{"a", "c"}))
	assert.True(t, f.acceptsTags([]string{"d", "a"}))
	assert.False(t, f.acceptsTags([]string{"a"}))
	assert.False(t, f.acceptsTags([]string{"d"}), "the other AND term still applies")
	assert.False(t, f.acceptsTags([]string{"a", ""}), "an empty alias must not match an empty tag")
	assert.True(t, ActivityFilter{}.acceptsTags(nil))
}
