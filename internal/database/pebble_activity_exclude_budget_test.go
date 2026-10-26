// file: internal/database/pebble_activity_exclude_budget_test.go
// version: 1.2.0
// guid: 8b3f6d21-5a94-4c07-9e1d-2f7a0c4b8e65
// last-edited: 2026-10-26

package database

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hideNewerThan hides every row of seedOpEntries whose sequence number is above
// n, so the visible rows are the OLDEST n+1 and the newest-first walk meets all
// the hidden ones first.
func hideNewerThan(n int) func(ActivityEntry) bool {
	return func(e ActivityEntry) bool {
		var seq int
		_, err := fmt.Sscanf(strings.TrimPrefix(e.Summary, "op entry "), "%d", &seq)
		return err != nil || seq > n
	}
}

// An id read with an ExcludeEntry predicate decodes the rows of the id to judge
// them. A viewer must not be able to make that unbounded: it decodes at most
// activityQueryScanBudget plus the page probe, however long the operation's
// transcript is, and a walk the budget cut short before the page was full says
// so (Partial) instead of presenting "no rows" as the whole story.
//
// Not parallel: it mutates the package-level budget var.
func TestExcludingPath_DecodesAreBoundedByTheBudget(t *testing.T) {
	const (
		opID   = "op-hush-transcript"
		rows   = 100 // ten times the page
		limit  = 10
		budget = 20
	)
	probe := limit + 1
	old := activityQueryScanBudget
	t.Cleanup(func() { activityQueryScanBudget = old })

	cases := []struct {
		name        string
		hide        func(ActivityEntry) bool
		budget      int
		wantDecoded func(got int64) bool
		wantPartial bool
		wantEntries int
	}{
		{"visible rows fill the page inside the budget", hideType("type-1", nil), budget,
			func(got int64) bool { return got <= int64(budget+probe) }, false, limit},
		{"every row hidden: the budget ends the walk and says so", func(ActivityEntry) bool { return true }, budget,
			func(got int64) bool { return got == int64(budget+probe) }, true, 0},
		{"only the oldest rows are visible: cut before them, partial", hideNewerThan(20), budget,
			func(got int64) bool { return got == int64(budget+probe) }, true, 0},
		{"the same read with room is exact and reads them all", hideNewerThan(20), 1000,
			func(got int64) bool { return got == rows }, false, limit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestPebbleActivityStore(t)
			seedOpEntries(t, s, opID, rows)
			activityQueryScanBudget = tc.budget

			before := s.EntriesDecoded()
			res, err := s.QueryWithPartial(context.Background(), ActivityFilter{
				OperationID: opID, Limit: limit, ExcludeEntry: tc.hide,
			})
			require.NoError(t, err)
			decoded := s.EntriesDecoded() - before
			assert.True(t, tc.wantDecoded(decoded), "decoded %d rows of %d with budget %d", decoded, rows, tc.budget)
			assert.Equal(t, tc.wantPartial, res.Partial)
			assert.Len(t, res.Entries, tc.wantEntries)
			for _, e := range res.Entries {
				assert.False(t, tc.hide(e), "a hidden row was returned: %s", e.Summary)
			}
		})
	}
}

// The budget cuts the oldest rows, not the newest: what a bounded read returns
// is the newest rows the caller may see, in the same order the exact read gives.
func TestExcludingPath_BoundedReadKeepsTheNewestVisibleRows(t *testing.T) {
	const opID = "op-knightfall-tail"
	old := activityQueryScanBudget
	t.Cleanup(func() { activityQueryScanBudget = old })
	s := newTestPebbleActivityStore(t)
	seedOpEntries(t, s, opID, 100)
	f := ActivityFilter{OperationID: opID, Limit: 5, ExcludeEntry: hideType("type-1", nil)}

	activityQueryScanBudget = 1000
	exact, err := s.QueryWithPartial(context.Background(), f)
	require.NoError(t, err)
	require.False(t, exact.Partial)

	activityQueryScanBudget = 10
	cut, err := s.QueryWithPartial(context.Background(), f)
	require.NoError(t, err)
	assert.Equal(t, exact.Entries, cut.Entries, "the page is the same newest visible rows")
	assert.Less(t, cut.Total, exact.Total, "only the total is a lower bound once the budget ends the walk")
	assert.False(t, cut.Partial, "a full page with the probe seen is not partial")
}

// A caller's Offset and Limit reach the decode budget (scan budget plus the
// page probe Offset+Limit+1), so they have to be bounded before any path runs:
// the operation and book index paths used to take them as given, which let one
// request make the budget unbounded. Every path now refuses an Offset past
// activityQueryMaxOffset and clamps a Limit to activityQueryMaxLimit.
func TestQuery_DeepOffsetIsRefusedOnEveryPath(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	seedOpEntries(t, s, "op-year-one", 10)
	hide := hideType("type-1", nil)
	filters := map[string]ActivityFilter{
		"operation id":              {OperationID: "op-year-one"},
		"operation id, excluding":   {OperationID: "op-year-one", ExcludeEntry: hide},
		"book id":                   {BookID: "book-2"},
		"book id, excluding":        {BookID: "book-2", ExcludeEntry: hide},
		"source (filter index)":     {Source: "src-1"},
		"nothing (general path)":    {},
		"operation id, huge offset": {OperationID: "op-year-one", Offset: 1 << 40},
	}
	for name, f := range filters {
		t.Run(name, func(t *testing.T) {
			deep := f
			if deep.Offset == 0 {
				deep.Offset = activityQueryMaxOffset + 1
			}
			deep.Limit = 10
			_, err := s.QueryWithPartial(context.Background(), deep)
			require.ErrorIs(t, err, ErrActivityOffsetTooDeep)
			_, _, err = s.Query(context.Background(), deep)
			require.ErrorIs(t, err, ErrActivityOffsetTooDeep)

			atCap := f
			atCap.Offset = activityQueryMaxOffset
			atCap.Limit = 10
			if f.Offset == 0 {
				_, err = s.QueryWithPartial(context.Background(), atCap)
				require.NoError(t, err, "the cap itself is allowed")
			}
		})
	}
}

// A Limit past activityQueryMaxLimit is clamped on every path (it also sizes the
// page slice, which a huge Limit made a huge allocation), and no query decodes
// more than activityQueryMaxDecodeBudget however large the Offset and Limit.
func TestQuery_LimitIsClampedAndTheBudgetIsBoundedByConstants(t *testing.T) {
	for _, tc := range []struct{ off, lim int }{
		{0, 1 << 40}, {activityQueryMaxOffset, 1 << 40}, {1 << 40, 1 << 40}, {1 << 40, 0}, {activityQueryMaxOffset, activityQueryMaxLimit},
	} {
		f, err := normalizeActivityFilter(ActivityFilter{Offset: tc.off, Limit: tc.lim})
		if tc.off > activityQueryMaxOffset {
			require.ErrorIs(t, err, ErrActivityOffsetTooDeep, "offset %d", tc.off)
			continue
		}
		require.NoError(t, err)
		assert.LessOrEqual(t, f.Limit, activityQueryMaxLimit)
		assert.LessOrEqual(t, activityDecodeBudget(f), activityQueryMaxDecodeBudget(),
			"offset %d limit %d", tc.off, tc.lim)
	}
	assert.Equal(t, activityQueryScanBudget+activityQueryMaxOffset+activityQueryMaxLimit+1, activityQueryMaxDecodeBudget())

	s := newTestPebbleActivityStore(t)
	seedOpEntries(t, s, "op-hush", 30)
	hide := hideType("type-1", nil)
	for name, f := range map[string]ActivityFilter{
		"operation id":            {OperationID: "op-hush", Limit: 1 << 40},
		"operation id, excluding": {OperationID: "op-hush", Limit: 1 << 40, ExcludeEntry: hide},
		"book id, excluding":      {BookID: "book-2", Limit: 1 << 40, ExcludeEntry: hide},
		"source":                  {Source: "src-1", Limit: 1 << 40},
		"nothing":                 {Limit: 1 << 40},
	} {
		t.Run(name, func(t *testing.T) {
			before := s.EntriesDecoded()
			res, err := s.QueryWithPartial(context.Background(), f)
			require.NoError(t, err)
			assert.NotEmpty(t, res.Entries)
			assert.LessOrEqual(t, s.EntriesDecoded()-before, int64(activityQueryMaxDecodeBudget()))
		})
	}
}

// activityQueryMaxDecodeBudget is the most entries any single query decodes:
// the scan budget plus the page probe at the deepest allowed page. A function
// because the scan budget is a var (tests lower it).
func activityQueryMaxDecodeBudget() int {
	return activityQueryScanBudget + activityQueryMaxOffset + activityQueryMaxLimit + 1
}
