// file: internal/database/pebble_activity_bounded_test.go
// version: 1.0.0
// guid: 7f2a1c5e-9b34-4d61-8a70-2c6e5b91d403
// last-edited: 2026-08-11

// Package database — regression suite for the BOUNDED activity query path.
//
// WHY this file exists:
//
//	PebbleActivityStore.Query used to decode every entry in every tier into one
//	[]ActivityEntry, filter it, sort it, and only then slice out the requested
//	page. On production that put 8.86 GB (71.9% of the process heap) inside
//	scanTierKVs, 8.02 GB of it in encoding/json.Unmarshal, and
//	GET /api/v1/activity?limit=5 ran for 120s without returning. The server was
//	OOM-killed repeatedly.
//
//	These tests assert the COST of a query, not just its result. The instrument
//	is PebbleActivityStore.EntriesDecoded(), which is incremented at every
//	stored-entry decode site in the store — including the full-scan
//	scanTierKVs path — so a regression to full-scan behaviour makes the decode
//	counts explode and these tests fail loudly.
package database

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedActivityEntries writes n entries spread over the given tiers with
// strictly increasing timestamps, so newest-first order is unambiguous.
// Returns the timestamp of the newest entry written.
func seedActivityEntries(t *testing.T, s *PebbleActivityStore, n int, tiers ...string) time.Time {
	t.Helper()
	if len(tiers) == 0 {
		tiers = []string{"change"}
	}
	base := time.Now().UTC().Add(-time.Duration(n) * time.Second)
	var newest time.Time
	for i := 0; i < n; i++ {
		ts := base.Add(time.Duration(i) * time.Second)
		newest = ts
		_, err := s.Record(ActivityEntry{
			Timestamp: ts,
			Tier:      tiers[i%len(tiers)],
			Type:      "seeded",
			Level:     "info",
			Source:    fmt.Sprintf("src-%d", i%5),
			Summary:   fmt.Sprintf("seeded entry %d", i),
		})
		require.NoError(t, err)
	}
	return newest
}

// TestPebbleActivityStore_QueryDoesNotScanEntireLog is the core regression
// guard: a small page must cost a small number of decodes regardless of how
// much history is stored.
func TestPebbleActivityStore_QueryDoesNotScanEntireLog(t *testing.T) {
	s := newTestPebbleActivityStore(t)

	const seeded = 400
	seedActivityEntries(t, s, seeded, "change", "info", "debug", "batch")

	before := s.EntriesDecoded()
	entries, total, err := s.Query(ActivityFilter{Limit: 5})
	require.NoError(t, err)
	decoded := s.EntriesDecoded() - before

	assert.Len(t, entries, 5, "page should be full")

	// probe = offset + limit + 1 = 6. The walk stops there, so exactly 6 rows
	// are decoded. The pre-fix implementation decoded all 400.
	assert.Equal(t, int64(6), decoded,
		"query decoded %d of %d seeded entries; a paged query must not scan the whole log", decoded, seeded)
	assert.Equal(t, 6, total, "total is the probe value: one past the requested page")

	// Newest-first ordering is preserved.
	for i := 1; i < len(entries); i++ {
		assert.Falsef(t, entries[i].Timestamp.After(entries[i-1].Timestamp),
			"entry %d (%s) is newer than entry %d (%s): order must be newest-first",
			i, entries[i].Timestamp, i-1, entries[i-1].Timestamp)
	}
}

// TestPebbleActivityStore_QueryHonorsScanBudget proves the hard safety bound:
// a filter that matches nothing must NOT degrade into a full scan.
//
// Not parallel: it mutates the package-level budget var.
func TestPebbleActivityStore_QueryHonorsScanBudget(t *testing.T) {
	oldBudget := activityQueryScanBudget
	activityQueryScanBudget = 25
	t.Cleanup(func() { activityQueryScanBudget = oldBudget })

	s := newTestPebbleActivityStore(t)
	const seeded = 300
	seedActivityEntries(t, s, seeded, "change", "info")

	before := s.EntriesDecoded()
	entries, total, err := s.Query(ActivityFilter{Limit: 5, Search: "zzz-matches-nothing"})
	require.NoError(t, err)
	decoded := s.EntriesDecoded() - before

	assert.Empty(t, entries, "no entry matches the filter")
	assert.Equal(t, 0, total)
	assert.Equal(t, int64(activityQueryScanBudget), decoded,
		"a filter matching nothing must stop at the scan budget, not scan all %d entries", seeded)
}

// TestPebbleActivityStore_QueryExactTotalWhenExhausted pins the other half of
// the total contract: when the walk drains the log, total is exact.
func TestPebbleActivityStore_QueryExactTotalWhenExhausted(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	seedActivityEntries(t, s, 7, "change")

	entries, total, err := s.Query(ActivityFilter{Limit: 50})
	require.NoError(t, err)
	assert.Len(t, entries, 7)
	assert.Equal(t, 7, total, "limit exceeds the log size, so the walk exhausts and total is exact")
}

// TestPebbleActivityStore_QueryOffsetPastEnd guards the clamping behaviour the
// bounded rewrite had to preserve: an offset beyond the data returns an empty
// page rather than panicking on a bad slice range.
func TestPebbleActivityStore_QueryOffsetPastEnd(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	seedActivityEntries(t, s, 4, "change")

	entries, total, err := s.Query(ActivityFilter{Limit: 10, Offset: 999})
	require.NoError(t, err)
	assert.Empty(t, entries)
	assert.Equal(t, 4, total)
}

// TestPebbleActivityStore_QueryDigestSortsLast pins the two-group result order
// the pre-fix global sort produced: every non-digest entry precedes every
// digest entry, newest-first within each group.
func TestPebbleActivityStore_QueryDigestSortsLast(t *testing.T) {
	s := newTestPebbleActivityStore(t)

	base := time.Now().UTC().Add(-time.Hour)
	// Digest entries are written NEWEST so a naive global timestamp sort would
	// put them first — only the two-group order keeps them last.
	for i := 0; i < 3; i++ {
		_, err := s.Record(ActivityEntry{
			Timestamp: base.Add(time.Duration(i) * time.Minute),
			Tier:      "change", Type: "seeded", Level: "info", Source: "s",
			Summary: fmt.Sprintf("change-%d", i),
		})
		require.NoError(t, err)
	}
	for i := 0; i < 2; i++ {
		_, err := s.Record(ActivityEntry{
			Timestamp: base.Add(time.Duration(30+i) * time.Minute),
			Tier:      "digest", Type: "daily_digest", Level: "info", Source: "compaction",
			Summary: fmt.Sprintf("digest-%d", i),
		})
		require.NoError(t, err)
	}

	entries, total, err := s.Query(ActivityFilter{Limit: 50})
	require.NoError(t, err)
	require.Len(t, entries, 5)
	assert.Equal(t, 5, total)

	seenDigest := false
	for _, e := range entries {
		if e.Tier == "digest" {
			seenDigest = true
			continue
		}
		assert.False(t, seenDigest, "non-digest entry %q appeared after a digest entry", e.Summary)
	}
	assert.Equal(t, "digest", entries[3].Tier)
	assert.Equal(t, "digest", entries[4].Tier)
}

// TestPebbleActivityStore_GetDistinctSourcesIsBoundedAndCached proves the
// second half of the OOM: GetDistinctSources ran a full scan concurrently with
// Query on every page load (3.21 GB of the production heap on its own).
//
// Not parallel: it mutates the package-level budget var.
func TestPebbleActivityStore_GetDistinctSourcesIsBoundedAndCached(t *testing.T) {
	oldBudget := activityQueryScanBudget
	activityQueryScanBudget = 30
	t.Cleanup(func() { activityQueryScanBudget = oldBudget })

	s := newTestPebbleActivityStore(t)
	const seeded = 250
	seedActivityEntries(t, s, seeded, "change", "info")

	before := s.EntriesDecoded()
	sources, err := s.GetDistinctSources(ActivityFilter{})
	require.NoError(t, err)
	decoded := s.EntriesDecoded() - before

	assert.NotEmpty(t, sources)
	assert.Equal(t, int64(activityQueryScanBudget), decoded,
		"distinct-sources must stop at the scan budget, not scan all %d entries", seeded)

	// Second call inside the TTL must be served from cache: zero extra decodes.
	beforeCached := s.EntriesDecoded()
	cached, err := s.GetDistinctSources(ActivityFilter{})
	require.NoError(t, err)
	assert.Equal(t, int64(0), s.EntriesDecoded()-beforeCached,
		"a repeat call within the TTL must not touch the store at all")
	assert.Equal(t, sources, cached)

	// A different filter must NOT be served the previous filter's counts.
	beforeOther := s.EntriesDecoded()
	_, err = s.GetDistinctSources(ActivityFilter{Tier: "change"})
	require.NoError(t, err)
	assert.Greater(t, s.EntriesDecoded()-beforeOther, int64(0),
		"a different filter must miss the cache and run its own scan")
}

// TestPebbleActivityStore_DecodeFailuresAreCounted proves that a row whose
// stored JSON cannot be decoded is counted rather than silently dropped by a
// bare `continue`.
func TestPebbleActivityStore_DecodeFailuresAreCounted(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	seedActivityEntries(t, s, 3, "change")

	// Write a corrupt value under a well-formed primary key.
	corruptKey := pactPrimaryKey("change", time.Now().UTC(), "01CORRUPTCORRUPTCORRUPT")
	require.NoError(t, s.db.Set(corruptKey, []byte("{not json"), nil))

	before := s.DecodeFailures()
	entries, _, err := s.Query(ActivityFilter{Limit: 50})
	require.NoError(t, err)

	assert.Equal(t, int64(1), s.DecodeFailures()-before,
		"the undecodable row must be counted as a drop, not silently skipped")
	for _, e := range entries {
		assert.NotEmpty(t, e.Type, "decoded entries must be intact")
	}
}
