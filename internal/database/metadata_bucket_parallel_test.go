// file: internal/database/metadata_bucket_parallel_test.go
// version: 1.0.0
// guid: 9f1f8930-ec96-4070-a620-dda756a2b000
// last-edited: 2026-10-30

package database

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/matcher"
	"github.com/stretchr/testify/require"
)

// refMetadataTitleSimilarity is the PRE-optimization SHAPE of the title metric,
// kept as the oracle for the grouping algorithm: both strings are normalized and
// decoded from scratch for every pair, with nothing carried between pairs. (That
// matcher.SymmetricScore equals the previous string-based two-direction
// ScoreMatch is pinned where the scorer lives, in the matcher package's
// property test.)
func refMetadataTitleSimilarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	pa, pb := matcher.Prepare(a), matcher.Prepare(b)
	var sc matcher.Scratch
	return float64(matcher.SymmetricScore(&pa, &pb, &sc)) / 100.0
}

// refGroupMetadataBucket is the PRE-optimization groupMetadataBucket: every
// pair scored from scratch, every pair compared whether or not its members
// already share a component.
func refGroupMetadataBucket(bucket []metadataDupEntry, threshold float64) [][]BookCore {
	n := len(bucket)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	find := func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	for i := range n {
		for j := i + 1; j < n; j++ {
			if refMetadataTitleSimilarity(bucket[i].normTitle, bucket[j].normTitle) >= threshold {
				ra, rb := find(i), find(j)
				if ra != rb {
					parent[ra] = rb
				}
			}
		}
	}
	comps := make(map[int][]BookCore)
	rootsOrder := make([]int, 0, n)
	for i := range n {
		r := find(i)
		if _, ok := comps[r]; !ok {
			rootsOrder = append(rootsOrder, r)
		}
		comps[r] = append(comps[r], bucket[i].book)
	}
	var groups [][]BookCore
	for _, r := range rootsOrder {
		if len(comps[r]) >= 2 {
			groups = append(groups, comps[r])
		}
	}
	return groups
}

// refBucketMetadataDuplicates is the PRE-optimization serial driver loop.
func refBucketMetadataDuplicates(entries []metadataDupEntry, threshold float64) [][]BookCore {
	buckets := make(map[string][]metadataDupEntry)
	order := make([]string, 0, len(entries))
	for _, e := range entries {
		key := e.authorKey + "\x00" + e.titleToken
		if _, exists := buckets[key]; !exists {
			order = append(order, key)
		}
		buckets[key] = append(buckets[key], e)
	}
	var groups [][]BookCore
	for _, key := range order {
		bucket := buckets[key]
		if len(bucket) < 2 || len(bucket) > metadataFuzzyBucketCap {
			continue
		}
		groups = append(groups, refGroupMetadataBucket(bucket, threshold)...)
	}
	return groups
}

// bucketGroupIDs flattens groups to book IDs so a mismatch prints readably.
func bucketGroupIDs(groups [][]BookCore) [][]string {
	out := make([][]string, len(groups))
	for i, g := range groups {
		for _, b := range g {
			out[i] = append(out[i], b.ID)
		}
	}
	return out
}

// mixedBucketEntries builds the shape the driver loop must handle at once: many
// scoreable buckets, a singleton bucket (skipped), a bucket one past the cap
// (skipped with a warning) and a bucket of identical titles (one big group).
func mixedBucketEntries(t *testing.T) []metadataDupEntry {
	t.Helper()
	entries := syntheticMetadataBuckets(8, 30)

	add := func(authorID int, title, id string) {
		aid := authorID
		e, ok := newMetadataDupEntry(BookCore{ID: id, Title: title, AuthorID: &aid})
		require.True(t, ok)
		entries = append(entries, e)
	}
	add(5000, "Batman: Hush", "single-0") // singleton bucket
	for i := 0; i <= metadataFuzzyBucketCap; i++ {
		add(5001, fmt.Sprintf("Batman Knightfall %d", i), fmt.Sprintf("oversize-%d", i))
	}
	for i := 0; i < 6; i++ {
		add(5002, "Batman: The Long Halloween", fmt.Sprintf("same-%d", i))
	}
	return entries
}

// TestGroupMetadataBucketMatchesReference proves the precomputed, score-once
// grouping returns exactly the groups the previous implementation did, across
// thresholds from "everything unions" to "only identical titles".
func TestGroupMetadataBucketMatchesReference(t *testing.T) {
	bucket := syntheticMetadataBuckets(1, 60)
	for _, threshold := range []float64{-1, 0.5, 0.85, 1.0} {
		want := refGroupMetadataBucket(bucket, threshold)
		got := groupMetadataBucket(bucket, threshold)
		require.Equal(t, bucketGroupIDs(want), bucketGroupIDs(got), "threshold %v", threshold)
	}
	// At a mid threshold the fixture must produce both grouped and ungrouped
	// books, or the comparison above proves nothing.
	mid := groupMetadataBucket(bucket, 0.85)
	grouped := 0
	for _, g := range mid {
		grouped += len(g)
	}
	require.NotEmpty(t, mid)
	require.Less(t, grouped, len(bucket), "fixture groups every book at 0.85; it cannot tell a missed union from a spurious one")
}

// TestBucketMetadataDuplicatesParallelMatchesSerial is the determinism test:
// the same input run serially (one worker), with several workers and with more
// workers than buckets must produce identical groups in identical order, and
// that must equal the previous serial implementation. Run under -race it also
// proves the workers share no mutable state.
func TestBucketMetadataDuplicatesParallelMatchesSerial(t *testing.T) {
	entries := mixedBucketEntries(t)

	old := metadataBucketWorkers
	t.Cleanup(func() { metadataBucketWorkers = old })

	// The reference is the slow pre-optimization code, so compare against it at
	// one threshold; the other thresholds compare every worker count against the
	// single-worker run, which is the serial order by construction.
	require.Equal(t,
		bucketGroupIDs(refBucketMetadataDuplicates(entries, 0.85)),
		bucketGroupIDs(bucketMetadataDuplicates(entries, 0.85)),
		"new implementation vs previous serial implementation")

	for _, threshold := range []float64{0, 0.7, 0.85} {
		metadataBucketWorkers = 1
		want := bucketGroupIDs(bucketMetadataDuplicates(entries, threshold))
		require.Greater(t, len(want), 3, "fixture must yield several groups for order to matter (threshold %v)", threshold)

		for _, workers := range []int{2, 4, 32} {
			metadataBucketWorkers = workers
			// Repeat: an ordering bug depends on scheduling and may not show on
			// the first run.
			for rep := 0; rep < 5; rep++ {
				got := bucketGroupIDs(bucketMetadataDuplicates(entries, threshold))
				require.Equal(t, want, got, "threshold %v workers %d rep %d", threshold, workers, rep)
			}
		}
	}
}

// TestBucketMetadataDuplicatesStillSkipsOversizeBuckets pins that the cap
// guard survived the move to a pool: the over-cap bucket is skipped, and the
// warning is still logged once, from the calling goroutine, before scoring.
func TestBucketMetadataDuplicatesStillSkipsOversizeBuckets(t *testing.T) {
	entries := mixedBucketEntries(t)

	var logged strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	for _, g := range bucketMetadataDuplicates(entries, 0) {
		for _, b := range g {
			require.False(t, strings.HasPrefix(b.ID, "oversize-"), "oversize bucket member %s was grouped", b.ID)
		}
	}
	require.Equal(t, 1, strings.Count(logged.String(), "metadata dedup bucket exceeds cap"))
}
