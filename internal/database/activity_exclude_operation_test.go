// file: internal/database/activity_exclude_operation_test.go
// version: 1.1.0
// guid: 6b1e84c3-d927-4f50-a3b8-5e0c7d219f64
// last-edited: 2026-10-24

package database

import (
	"context"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ExcludeEntry hides the entries of operations a caller may not read
// (Repairs operations of a fixer the caller may not use) inside the store's
// scan, so page, offset and total are counted after the hiding. The entry shapes
// that carry an operation id are the reporter's rows (own OperationID plus def:
// tag), the slog "system" rows (OperationID, no def: tag) and Summarize rows
// (op:<id> tags); every Pebble query path must agree.

// seedExcludeEntryRows writes, oldest first, rows of three operations plus
// rows with none: "hidden" is the operation to hide.
func seedExcludeEntryRows(t *testing.T, s typeAliasActivityStore) {
	t.Helper()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	rows := []ActivityEntry{
		{Summary: "plain 1", Type: "system"},
		{Summary: "hidden reporter", Type: "repairs.apply", OperationID: "op-hidden", Tags: []string{"def:repairs.apply"}},
		{Summary: "shown reporter", Type: "library.scan", OperationID: "op-shown", Tags: []string{"def:library.scan"}},
		{Summary: "hidden system", Type: "system", OperationID: "op-hidden"},
		{Summary: "plain 2", Type: "system"},
		{Summary: "shown system", Type: "system", OperationID: "op-shown"},
		{Summary: "digest names hidden", Type: "digest", Source: "summarize", Tags: []string{"op:op-shown", "op:op-hidden"}},
		{Summary: "digest names shown", Type: "digest", Source: "summarize", Tags: []string{"op:op-shown"}},
		{Summary: "hidden again", Type: "repairs.plan", OperationID: "op-hidden"},
		{Summary: "plain 3", Type: "system"},
	}
	for i, e := range rows {
		e.Timestamp = base.Add(time.Duration(i) * time.Minute)
		e.Tier, e.Level = "change", "info"
		if e.Source == "" {
			e.Source = "src"
		}
		_, err := s.Record(e)
		require.NoError(t, err)
	}
}

func assertExcludeEntryQueries(t *testing.T, s typeAliasActivityStore) {
	t.Helper()
	ctx := context.Background()
	hide := func(ids ...string) func(ActivityEntry) bool {
		return func(e ActivityEntry) bool {
			return EntryOperationIDs(e, func(id string) bool { return slices.Contains(ids, id) })
		}
	}
	names := func(es []ActivityEntry) []string { return typeAliasSummaries(es) }

	// Every row of the hidden operation is gone, a digest that names it too, and
	// the total counts only what is left.
	got, total, err := s.Query(ctx, ActivityFilter{ExcludeEntry: hide("op-hidden"), Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, []string{"plain 3", "digest names shown", "shown system", "plain 2", "shown reporter", "plain 1"}, names(got))
	assert.Equal(t, 6, total)

	// Paging is counted after the hiding: the second page starts where the
	// first ended and holds no hidden row.
	p1, total, err := s.Query(ctx, ActivityFilter{ExcludeEntry: hide("op-hidden"), Limit: 4})
	require.NoError(t, err)
	// A page that does not exhaust the matches reports a lower bound (one past
	// the page), never a number that counts a hidden row.
	assert.Equal(t, 5, total)
	p2, _, err := s.Query(ctx, ActivityFilter{ExcludeEntry: hide("op-hidden"), Limit: 4, Offset: 4})
	require.NoError(t, err)
	assert.Equal(t, []string{"plain 3", "digest names shown", "shown system", "plain 2"}, names(p1))
	assert.Equal(t, []string{"shown reporter", "plain 1"}, names(p2))

	// No predicate (or one that hides nothing) changes nothing.
	_, total, err = s.Query(ctx, ActivityFilter{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 10, total)
	_, total, err = s.Query(ctx, ActivityFilter{ExcludeEntry: hide("nobody"), Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 10, total)

	// Hiding two operations; the digest naming either is hidden.
	_, total, err = s.Query(ctx, ActivityFilter{ExcludeEntry: hide("op-hidden", "op-shown"), Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 3, total)

	// Alongside an indexed predicate (Source): decided by the residual check.
	got, total, err = s.Query(ctx, ActivityFilter{Source: "src", ExcludeEntry: hide("op-hidden"), Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 5, total)
	assert.NotContains(t, names(got), "hidden system")

	// An operation_id filter for the hidden operation (the id-index pushdown is
	// refused when a predicate rides along) finds nothing but its digest rows are
	// hidden too; for a shown operation, only the digest naming a hidden one goes.
	got, total, err = s.Query(ctx, ActivityFilter{OperationID: "op-hidden", ExcludeEntry: hide("op-hidden"), Limit: 50})
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Equal(t, 0, total)
	got, total, err = s.Query(ctx, ActivityFilter{OperationID: "op-shown", ExcludeEntry: hide("op-hidden"), Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, []string{"digest names shown", "shown system", "shown reporter"}, names(got))
	assert.Equal(t, 3, total)
}

func TestActivityExcludeEntry_Pebble(t *testing.T) {
	for _, indexed := range []bool{true, false} {
		name := "budget-scan"
		if indexed {
			name = "filter-index"
		}
		t.Run(name, func(t *testing.T) {
			s := newTestPebbleActivityStore(t)
			seedExcludeEntryRows(t, s)
			setFilterIndexReady(t, s, indexed)
			require.Equal(t, indexed, s.FilterIndexBackfillDone())
			assertExcludeEntryQueries(t, s)
		})
	}
}

// A function-valued predicate is a live predicate for the id-index pushdown,
// which would otherwise answer an operation_id read from the index and apply
// no predicate to its rows.
func TestActivityExcludeEntry_RefusesIndexPushdown(t *testing.T) {
	assert.True(t, pactIndexPushdownEligible("act:op:x:", ActivityFilter{OperationID: "x"}))
	assert.False(t, pactIndexPushdownEligible("act:op:x:", ActivityFilter{OperationID: "x", ExcludeEntry: func(ActivityEntry) bool { return false }}))
}

// A digest entry whose own OperationID is empty is still reached through its
// op: tags, and its own id is asked only once.
func TestEntryOperationIDs(t *testing.T) {
	var asked []string
	e := ActivityEntry{OperationID: "a", Source: summarizeSource, Tags: []string{"x", "op:a", "op:b", "op:"}}
	assert.False(t, EntryOperationIDs(e, func(id string) bool { asked = append(asked, id); return false }))
	assert.Equal(t, []string{"a", "b"}, asked)
	assert.True(t, EntryOperationIDs(e, func(id string) bool { return id == "b" }))
	// A non-summary row's tags are not operation ids.
	asked = nil
	EntryOperationIDs(ActivityEntry{Tags: []string{"op:c"}, Source: "src"}, func(id string) bool { asked = append(asked, id); return false })
	assert.Empty(t, asked)
}

// A caller who is denied nothing still carries an ExcludeEntry (the handler sets
// one on every read); it must change nothing: same entries, same total, same
// partial flag as no predicate, on both Pebble paths, for the filter shapes the
// Activity page uses, and when the scan budget is exhausted.
func TestActivityExcludeEntry_HidingNothingChangesNothing(t *testing.T) {
	never := func(ActivityEntry) bool { return false }
	filters := map[string]ActivityFilter{
		"default":         {Limit: 50},
		"small page":      {Limit: 3},
		"second page":     {Limit: 3, Offset: 3},
		"source":          {Source: "src", Limit: 4},
		"type":            {Type: "system", Limit: 2},
		"operation":       {OperationID: "op-shown", Limit: 50},
		"operation page":  {OperationID: "op-shown", Limit: 1, Offset: 1},
		"book":            {BookID: "book-1", Limit: 50},
		"tier and search": {Tier: "change", Search: "plain", Limit: 50},
	}
	for _, indexed := range []bool{true, false} {
		for _, budget := range []int{20000, 4} {
			t.Run(map[bool]string{true: "filter-index", false: "budget-scan"}[indexed]+"/budget-"+strconv.Itoa(budget), func(t *testing.T) {
				old := activityQueryScanBudget
				t.Cleanup(func() { activityQueryScanBudget = old })
				s := newTestPebbleActivityStore(t)
				seedExcludeEntryRows(t, s)
				_, err := s.Record(ActivityEntry{Summary: "book row", Type: "system", Tier: "change", Level: "info", Source: "src", BookID: "book-1", Timestamp: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)})
				require.NoError(t, err)
				setFilterIndexReady(t, s, indexed)
				activityQueryScanBudget = budget
				ctx := context.Background()
				for name, f := range filters {
					want, err := s.QueryWithPartial(ctx, f)
					require.NoError(t, err, name)
					f.ExcludeEntry = never
					got, err := s.QueryWithPartial(ctx, f)
					require.NoError(t, err, name)
					assert.Equal(t, want.Entries, got.Entries, name)
					assert.Equal(t, want.Total, got.Total, name)
					assert.Equal(t, want.Partial, got.Partial, name)
				}
			})
		}
	}
}
