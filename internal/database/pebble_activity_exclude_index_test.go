// file: internal/database/pebble_activity_exclude_index_test.go
// version: 1.0.0
// guid: 5b1f3c0e-7a42-4d8e-9c6a-2e0f81d4b7a3
// last-edited: 2026-10-26

package database

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hideType is an ExcludeEntry that hides one entry type, counting its calls.
func hideType(typ string, calls *atomic.Int64) func(ActivityEntry) bool {
	return func(e ActivityEntry) bool {
		if calls != nil {
			calls.Add(1)
		}
		return e.Type == typ
	}
}

// TestIndexPathFor_ExcludeEntryKeepsTheIndexPath pins the dispatch. A wrong
// choice returns the same rows and differs only in cost, so no result assertion
// can see it; this is the control for the S6 fix.
func TestIndexPathFor_ExcludeEntryKeepsTheIndexPath(t *testing.T) {
	hide := func(ActivityEntry) bool { return false }
	const opPrefix = "act:op:op-x:"
	const bkPrefix = "act:bk:book-x:"

	assert.Equal(t, pactIndexPathExcluding,
		pactIndexPathFor(opPrefix, ActivityFilter{OperationID: "op-x", Limit: 10, ExcludeEntry: hide}))
	assert.Equal(t, pactIndexPathExcluding,
		pactIndexPathFor(bkPrefix, ActivityFilter{BookID: "book-x", Limit: 10, Offset: 5, ExcludeEntry: hide}))
	assert.Equal(t, pactIndexPathPaged,
		pactIndexPathFor(opPrefix, ActivityFilter{OperationID: "op-x", Limit: 10}),
		"no predicate: the pushdown, unchanged")

	// Any OTHER undecidable field still takes the reference path: the streaming
	// path applies matchesFilter to every row, but is only claimed for the shape
	// the differential tests cover.
	assert.Equal(t, pactIndexPathFull,
		pactIndexPathFor(opPrefix, ActivityFilter{OperationID: "op-x", Limit: 10, Type: "scan", ExcludeEntry: hide}))
	assert.Equal(t, pactIndexPathFull,
		pactIndexPathFor(opPrefix, ActivityFilter{OperationID: "op-x", Limit: -1, ExcludeEntry: hide}))
	assert.Equal(t, pactIndexPathFull,
		pactIndexPathFor("act:zz:thing:", ActivityFilter{Limit: 10, ExcludeEntry: hide}))
}

// TestExcludingPathMatchesFullPath is the differential: for every page of a
// predicate-bearing read, the streaming path and the reference return identical
// entries AND an identical total, on the op index and on the book index, with
// orphaned refs in the way.
func TestExcludingPathMatchesFullPath(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-gotham-ledger"
	const seeded = 90
	seedOpEntries(t, s, opID, seeded)
	// A second operation shares the book ids, so the book index mixes ops.
	seedOpEntries(t, s, "op-arkham-roster", 40)

	// Orphan three rows of the op: ref left, primary gone.
	for _, k := range indexRefKeys(t, s, "act:op:"+opID+":")[:3] {
		orphanPrimaryRow(t, s, k)
	}

	cases := []struct {
		name   string
		prefix string
		f      ActivityFilter
	}{
		{"op", "act:op:" + opID + ":", ActivityFilter{OperationID: opID}},
		{"book", "act:bk:book-2:", ActivityFilter{BookID: "book-2"}},
	}
	for _, tc := range cases {
		for _, offset := range []int{0, 1, 9, 30, 80, 200} {
			for _, limit := range []int{1, 7, 50, 500} {
				f := tc.f
				f.Offset, f.Limit = offset, limit
				f.ExcludeEntry = hideType("type-1", nil)
				t.Run(fmt.Sprintf("%s/offset=%d/limit=%d", tc.name, offset, limit), func(t *testing.T) {
					require.Equal(t, pactIndexPathExcluding, pactIndexPathFor(tc.prefix, f))
					wantE, wantT, err := s.queryByIndexPrefixFull(context.Background(), tc.prefix, f)
					require.NoError(t, err)
					gotE, gotT, err := s.queryByIndexPrefixExcluding(context.Background(), tc.prefix, f)
					require.NoError(t, err)
					assert.Equal(t, wantT, gotT, "total")
					assert.Equal(t, wantE, gotE, "entries")
					for _, e := range gotE {
						assert.NotEqual(t, "type-1", e.Type, "a hidden row must never be returned")
					}
				})
			}
		}
	}
}

// TestExcludingPathCountsAfterTheHiding pins what ExcludeEntry is for: the hidden
// rows are in neither the total nor the offset arithmetic. With every third row
// hidden the total is exactly the visible rows, and the pages tile them with no
// gap and no repeat.
func TestExcludingPathCountsAfterTheHiding(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-bane-breaks"
	const seeded = 60 // type-0/1/2 cycle, 20 each
	seedOpEntries(t, s, opID, seeded)

	var seen []string
	for offset := 0; ; offset += 7 {
		entries, total, err := s.Query(context.Background(), ActivityFilter{
			OperationID: opID, Limit: 7, Offset: offset, ExcludeEntry: hideType("type-1", nil),
		})
		require.NoError(t, err)
		assert.Equal(t, 40, total, "hidden rows are not counted")
		if len(entries) == 0 {
			break
		}
		for _, e := range entries {
			seen = append(seen, e.Summary)
		}
	}
	require.Len(t, seen, 40)
	uniq := map[string]bool{}
	for _, s := range seen {
		uniq[s] = true
	}
	assert.Len(t, uniq, 40, "no row repeats across pages")
}

// TestExcludingPathJudgesEveryLiveRow is the "exact, never partial" half: the
// predicate sees every live row of the id (a stale ref is not a row), however
// small the page, so the total is never an estimate.
func TestExcludingPathJudgesEveryLiveRow(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-two-face-coin"
	seedOpEntries(t, s, opID, 100)
	orphanPrimaryRow(t, s, indexRefKeys(t, s, "act:op:"+opID+":")[0])

	var calls atomic.Int64
	_, total, err := s.Query(context.Background(), ActivityFilter{
		OperationID: opID, Limit: 1, ExcludeEntry: hideType("none", &calls),
	})
	require.NoError(t, err)
	assert.Equal(t, 99, total)
	assert.EqualValues(t, 99, calls.Load())
}

// TestExcludingPathTiedTimestampsOrderLikeTheFullPath: rows written in the same
// instant are ordered by the ULID, in both implementations.
func TestExcludingPathTiedTimestampsOrderLikeTheFullPath(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-joker-tie"
	ts := time.Now().UTC().Add(-time.Hour)
	for i := range 25 {
		_, err := s.Record(ActivityEntry{
			Timestamp: ts, Tier: "info", Type: fmt.Sprintf("type-%d", i%3),
			OperationID: opID, Summary: fmt.Sprintf("tied %02d", i),
		})
		require.NoError(t, err)
	}
	prefix := "act:op:" + opID + ":"
	f := ActivityFilter{OperationID: opID, Limit: 10, Offset: 3, ExcludeEntry: hideType("type-0", nil)}
	wantE, wantT, err := s.queryByIndexPrefixFull(context.Background(), prefix, f)
	require.NoError(t, err)
	gotE, gotT, err := s.queryByIndexPrefixExcluding(context.Background(), prefix, f)
	require.NoError(t, err)
	assert.Equal(t, wantT, gotT)
	assert.Equal(t, wantE, gotE)
}

// TestExcludingPathStopsOnCancel: a cancelled context is an error, never a short
// answer presented as a complete one.
func TestExcludingPathStopsOnCancel(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-robin-cancel"
	seedOpEntries(t, s, opID, 200)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	entries, total, err := s.Query(ctx, ActivityFilter{
		OperationID: opID, Limit: 10, ExcludeEntry: hideType("none", nil),
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, entries)
	assert.Zero(t, total)
}

// TestExcludingPathEmptyResultMatchesFullPath: a predicate that hides everything
// returns the same nil-vs-empty answer as the reference.
func TestExcludingPathEmptyResultMatchesFullPath(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-riddler-blank"
	seedOpEntries(t, s, opID, 12)
	prefix := "act:op:" + opID + ":"
	f := ActivityFilter{OperationID: opID, Limit: 5, ExcludeEntry: func(ActivityEntry) bool { return true }}
	wantE, wantT, err := s.queryByIndexPrefixFull(context.Background(), prefix, f)
	require.NoError(t, err)
	gotE, gotT, err := s.queryByIndexPrefixExcluding(context.Background(), prefix, f)
	require.NoError(t, err)
	assert.Zero(t, gotT)
	assert.Equal(t, wantT, gotT)
	assert.Equal(t, wantE, gotE)
}
