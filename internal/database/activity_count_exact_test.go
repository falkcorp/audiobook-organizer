// file: internal/database/activity_count_exact_test.go
// version: 1.0.0
// guid: 41ba9c07-52e8-4f36-a7d1-6e0b83c4915f
// last-edited: 2026-09-08

// CountActivity exists because Query's total is a pagination probe, not a
// census, on BOTH backends — and that is not obvious from either signature.
//
// Query returns (entries, total, error). Reading `total` as "how many rows
// match" is the natural reading and it is wrong: the Pebble store stops walking
// at Offset+Limit+1 matches (the bounded newest-first scan that replaced a full
// decode after it OOMed production at 8.86 GB), and the SQL store caps its
// COUNT subquery at sqlActCountCap. Both are correct for driving a "next page"
// button and both under-report a large keyspace by orders of magnitude.
//
// That misreading was live in the activity reclaim during development: the
// census called Query with Limit 1 and reported 2 rows for a store holding 5.
// Scaled to production's keyspace it would have reported single digits for
// millions — and the parity guard that decides whether deleting is safe would
// have been comparing two of those numbers.
//
// These tests pin the difference on both backends, so a future refactor that
// routes CountActivity back through Query fails here instead of silently
// resurrecting a lying census.
package database

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const countExactRows = 25

func TestPebbleCountActivity_IsExactWhereQueryTotalIsNot(t *testing.T) {
	store := newTestPebbleActivityStore(t)
	for range countExactRows {
		seedActivity(t, store, "change", 10*24*time.Hour, "row")
	}

	// The trap: Query's total with a small limit.
	_, queryTotal, err := store.Query(context.Background(), ActivityFilter{Tier: "change", Limit: 1})
	require.NoError(t, err)
	assert.Less(t, queryTotal, countExactRows,
		"precondition: Query's total is a bounded probe. If this ever equals the real "+
			"count, Query changed and this file's premise needs rechecking")

	// The fix: an exact count.
	n, err := store.CountActivity(context.Background(), "change", nil)
	require.NoError(t, err)
	assert.Equal(t, countExactRows, n, "CountActivity must count every row, not a page of them")
}

func TestSQLCountActivity_IsExact(t *testing.T) {
	store := newTestSQLStore(t)
	for range countExactRows {
		seedActivity(t, store, "change", 10*24*time.Hour, "row")
	}

	n, err := store.CountActivity(context.Background(), "change", nil)
	require.NoError(t, err)
	assert.Equal(t, countExactRows, n)
}

func TestCountActivity_CutoffBoundIsExclusiveAndPerTier(t *testing.T) {
	// The reclaim decides what to delete from these two numbers, so both the
	// time bound and the tier bound have to be real.
	for _, tc := range []struct {
		name  string
		build func(t *testing.T) ActivityCounter
	}{
		{"pebble", func(t *testing.T) ActivityCounter { return newTestPebbleActivityStore(t) }},
		{"sqlite", func(t *testing.T) ActivityCounter { return newTestSQLStore(t) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counter := tc.build(t)
			store, ok := counter.(ActivityStorer)
			require.True(t, ok)

			for range 3 {
				seedActivity(t, store, "change", 10*24*time.Hour, "old change")
			}
			seedActivity(t, store, "change", time.Minute, "new change")
			seedActivity(t, store, "debug", 10*24*time.Hour, "old debug")

			cutoff := time.Now().UTC().Add(-48 * time.Hour)

			old, err := counter.CountActivity(context.Background(), "change", &cutoff)
			require.NoError(t, err)
			assert.Equal(t, 3, old, "only rows behind the cutoff, and only this tier")

			all, err := counter.CountActivity(context.Background(), "change", nil)
			require.NoError(t, err)
			assert.Equal(t, 4, all, "a nil cutoff means every row in the tier")

			debug, err := counter.CountActivity(context.Background(), "debug", &cutoff)
			require.NoError(t, err)
			assert.Equal(t, 1, debug, "tiers must not bleed into each other")
		})
	}
}

func TestPebbleCountActivity_DoesNotCountSecondaryIndexKeys(t *testing.T) {
	// Index keys (act:op:, act:bk:) live in the same keyspace as primary rows.
	// A key-only counter that used a loose prefix would count each row two or
	// three times and inflate the census.
	store := newTestPebbleActivityStore(t)
	_, err := store.Record(ActivityEntry{
		Timestamp:   time.Now().UTC().Add(-10 * 24 * time.Hour),
		Tier:        "change",
		Type:        "test_event",
		Level:       "info",
		Source:      "reclaim_test",
		OperationID: "op-1", // writes an act:op: index key
		BookID:      "bk-1", // writes an act:bk: index key
		Summary:     "one row, three keys",
	})
	require.NoError(t, err)

	n, err := store.CountActivity(context.Background(), "change", nil)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "one row is one row however many index keys it wrote")
}
