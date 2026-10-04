// file: internal/database/census_exact_test.go
// version: 1.0.0
// guid: 855ae87b-f3ac-4536-b212-083ff3eba047
// last-edited: 2026-10-04

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

// buildHistoryCensus is the reference (map-based) history distribution the
// streaming fold replaced; the fold must match it exactly.
func buildHistoryCensus(counts map[string]int64) *HistoryCensus {
	h := &HistoryCensus{Buckets: map[string]int64{
		"1-9": 0, "10-49": 0, "50-99": 0, "100-499": 0, "500-999": 0, "1000+": 0,
	}}
	if len(counts) == 0 {
		h.Top = []BookHistoryCount{}
		return h
	}
	all := make([]BookHistoryCount, 0, len(counts))
	for id, c := range counts {
		all = append(all, BookHistoryCount{BookID: id, Entries: c})
		h.Entries += c
		switch {
		case c < 10:
			h.Buckets["1-9"]++
		case c < 50:
			h.Buckets["10-49"]++
		case c < 100:
			h.Buckets["50-99"]++
		case c < 500:
			h.Buckets["100-499"]++
		case c < 1000:
			h.Buckets["500-999"]++
		default:
			h.Buckets["1000+"]++
		}
	}
	h.BooksWithHistory = int64(len(all))
	h.Mean = float64(h.Entries) / float64(len(all))
	sort.Slice(all, func(i, j int) bool {
		if all[i].Entries != all[j].Entries {
			return all[i].Entries > all[j].Entries
		}
		return all[i].BookID < all[j].BookID
	})
	h.Max = all[0].Entries
	pct := func(p float64) int64 {
		rank := int(math.Ceil(p * float64(len(all))))
		if rank < 1 {
			rank = 1
		}
		return all[len(all)-rank].Entries
	}
	h.P50, h.P90, h.P99 = pct(0.50), pct(0.90), pct(0.99)
	top := censusHistoryTopN
	if top > len(all) {
		top = len(all)
	}
	h.Top = append([]BookHistoryCount(nil), all[:top]...)
	return h
}

// historyFixture returns per-book counts and the sorted book_ver: keys.
func historyFixture(seed int64, books int) (map[string]int64, [][]byte) {
	rng := rand.New(rand.NewSource(seed))
	counts := map[string]int64{}
	var keys [][]byte
	for b := 0; b < books; b++ {
		// Variable-length ids, including ids that are prefixes of others.
		id := fmt.Sprintf("B%d", rng.Intn(books*3))
		if _, dup := counts[id]; dup {
			continue
		}
		n := int64(1 + rng.Intn(60))
		if rng.Intn(20) == 0 {
			n += int64(rng.Intn(1500))
		}
		counts[id] = n
		for i := int64(0); i < n; i++ {
			keys = append(keys, []byte(fmt.Sprintf("book_ver:%s:%020d", id, i)))
		}
	}
	sort.Slice(keys, func(i, j int) bool { return string(keys[i]) < string(keys[j]) })
	return counts, keys
}

func requireSameHistory(t *testing.T, want, got *HistoryCensus) {
	t.Helper()
	require.Equal(t, want.BooksWithHistory, got.BooksWithHistory)
	require.Equal(t, want.Entries, got.Entries)
	require.InDelta(t, want.Mean, got.Mean, 1e-9)
	require.Equal(t, want.P50, got.P50)
	require.Equal(t, want.P90, got.P90)
	require.Equal(t, want.P99, got.P99)
	require.Equal(t, want.Max, got.Max)
	require.Equal(t, want.Buckets, got.Buckets)
	require.Equal(t, want.Top, got.Top)
}

// SF4: the streaming fold equals the map-based distribution, including
// across a save/restore in the middle of a book, and its state stays small.
func TestExactHistoryFold_MatchesTheMapBasedHistory(t *testing.T) {
	for _, seed := range []int64{1, 2, 3, 4, 5} {
		counts, keys := historyFixture(seed, 400)
		want := buildHistoryCensus(counts)

		f := newExactHistoryFold()
		for _, k := range keys {
			f.add(k, nil)
		}
		requireSameHistory(t, want, f.finish(nil))

		// Interrupt in the middle (likely inside a book), round-trip the
		// state through JSON, and continue.
		g := newExactHistoryFold()
		half := len(keys) / 2
		for _, k := range keys[:half] {
			g.add(k, nil)
		}
		blob, err := json.Marshal(g)
		require.NoError(t, err)
		require.Less(t, len(blob), 16<<10, "fold state must stay a few KB, got %d bytes", len(blob))
		var h exactHistoryFold
		require.NoError(t, json.Unmarshal(blob, &h))
		for _, k := range keys[half:] {
			h.add(k, nil)
		}
		requireSameHistory(t, want, h.finish(nil))
	}
}

// The fold through a real run, with orphans from memdb.
func TestRunExactCensus_HistoryMatchesTheMapBasedHistory(t *testing.T) {
	p := newCensusRawStore(t)
	counts, keys := historyFixture(9, 300)
	b := p.db.NewBatch()
	for _, k := range keys {
		require.NoError(t, b.Set(k, []byte("{}"), nil))
	}
	require.NoError(t, b.Commit(pebble.Sync))
	ex := runExact(t, p)
	requireSameHistory(t, buildHistoryCensus(counts), ex.History)
	require.False(t, ex.History.OrphansKnown, "no memdb")
}

// growTombstoneFamily writes n cat: keys and deletes them all, leaving a
// family of tombstones and shadowed puts with no live key.
func growTombstoneFamily(t *testing.T, p *PebbleStore, n int) {
	t.Helper()
	val := make([]byte, 200)
	b := p.db.NewBatch()
	for i := 0; i < n; i++ {
		require.NoError(t, b.Set([]byte(fmt.Sprintf("cat:%07d", i)), val, nil))
	}
	require.NoError(t, b.Commit(pebble.Sync))
	require.NoError(t, p.db.Flush())
	d := p.db.NewBatch()
	for i := 0; i < n; i++ {
		require.NoError(t, d.Delete([]byte(fmt.Sprintf("cat:%07d", i)), nil))
	}
	require.NoError(t, d.Commit(pebble.Sync))
	require.NoError(t, p.db.Flush())
}

// SF3: an all-tombstone family under a low budget is cut into sub-ranges,
// heartbeats well inside a short watchdog, and makes resumable progress
// across reruns that are each killed by a deadline.
func TestRunExactCensus_TombstoneFamilyHeartbeatsAndResumes(t *testing.T) {
	oldSub, oldBeat, oldRefresh := exactCensusSubrangeBytes, exactCensusHeartbeat, exactCensusIterRefresh
	exactCensusSubrangeBytes, exactCensusHeartbeat, exactCensusIterRefresh = 64<<10, 50*time.Millisecond, 0
	t.Cleanup(func() {
		exactCensusSubrangeBytes, exactCensusHeartbeat, exactCensusIterRefresh = oldSub, oldBeat, oldRefresh
	})
	p := newCensusRawStore(t)
	const n = 20000
	growTombstoneFamily(t, p, n)

	subs, err := p.exactSubranges([]byte("cat:"), []byte("cat;"))
	require.NoError(t, err)
	require.Greater(t, len(subs), 4, "the family is cut into several sub-ranges")

	const watchdog = 500 * time.Millisecond
	var resumePoints []string
	for attempt := 0; attempt < 200; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
		var lastBeat = time.Now()
		var maxGap time.Duration
		_, err := p.RunExactCensus(ctx, ExactCensusOptions{
			ReadBytesPerSec: 2 << 20,
			Progress: func(ExactCensusProgress, string) {
				if g := time.Since(lastBeat); g > maxGap {
					maxGap = g
				}
				lastBeat = time.Now()
			},
		})
		cancel()
		require.Less(t, maxGap, watchdog, "attempt %d: heartbeat gap %s", attempt, maxGap)
		if err == nil {
			break
		}
		require.ErrorIs(t, err, context.DeadlineExceeded)
		st, lerr := p.loadExactCensusState()
		require.NoError(t, lerr)
		if st != nil && st.Partial != nil && st.Partial.Family == "cat:" {
			resumePoints = append(resumePoints, string(st.Partial.ResumeAt))
		}
	}
	last, err := p.LastExactCensus()
	require.NoError(t, err)
	require.NotNil(t, last, "the run finished across reruns")
	cat := censusFamily(t, last, "cat:")
	require.Zero(t, cat.Keys)
	require.Equal(t, int64(2*n), cat.Entries, "every put and tombstone counted once across the reruns")
	require.NotEmpty(t, resumePoints, "at least one rerun stopped inside the tombstone family")
	for i := 1; i < len(resumePoints); i++ {
		require.GreaterOrEqual(t, resumePoints[i], resumePoints[i-1], "the saved position only moves forward")
	}
}

// N1: with a refresh at every check, shadowed versions of the key a refresh
// stops after are still counted exactly once.
func TestRunExactCensus_RefreshCountsShadowedVersionsExactly(t *testing.T) {
	oldRefresh, oldCheck := exactCensusIterRefresh, exactCensusCheckBytes
	exactCensusIterRefresh, exactCensusCheckBytes = 0, 1
	t.Cleanup(func() { exactCensusIterRefresh, exactCensusCheckBytes = oldRefresh, oldCheck })
	p := newCensusRawStore(t)
	for v := 0; v < 3; v++ { // three versions of every key, in three tables
		writeCensusKeys(t, p, 500, func(i int) string { return fmt.Sprintf("work:%05d", i) })
		require.NoError(t, p.db.Flush())
	}
	ex := runExact(t, p)
	w := censusFamily(t, ex, "work:")
	require.Equal(t, int64(500), w.Keys)
	require.Equal(t, int64(1500), w.Entries)
	require.Equal(t, int64(1000), w.Deletions, "the shadowed versions")
}

// SF2 at the store level: a run that resumes its own progress keeps it; a
// restart discards it.
func TestRunExactCensus_RunIDAndRestart(t *testing.T) {
	p := newCensusRawStore(t)
	writeCensusKeys(t, p, 100, func(i int) string { return fmt.Sprintf("work:%05d", i) })
	ctx, cancel := context.WithCancel(context.Background())
	_, err := p.RunExactCensus(ctx, ExactCensusOptions{RunID: "op-A", Progress: func(pr ExactCensusProgress, _ string) {
		if pr.FamiliesDone >= 2 {
			cancel()
		}
	}})
	require.ErrorIs(t, err, context.Canceled)
	prog, err := p.ExactCensusInProgress()
	require.NoError(t, err)
	require.Equal(t, "op-A", prog.RunID)
	require.False(t, prog.Stale)

	ex := runExact(t, p)
	requireNoteContains(t, ex.Notes, "resumed")
}

// N3/N4: progress not updated for a day is reported stale and not resumed.
func TestRunExactCensus_StaleProgressIsNotResumed(t *testing.T) {
	p := newCensusRawStore(t)
	writeCensusKeys(t, p, 100, func(i int) string { return fmt.Sprintf("work:%05d", i) })
	old := time.Now().Add(-exactCensusProgressMaxAge - time.Hour)
	st := &exactCensusState{Registry: censusRegistryFingerprint(), RunID: "old", StartedAt: old, UpdatedAt: old,
		Done: map[string]exactFamilyCount{"work:": {Live: 1, Points: 1}}}
	require.NoError(t, p.saveExactCensusState(st, 10, true))
	prog, err := p.ExactCensusInProgress()
	require.NoError(t, err)
	require.True(t, prog.Stale)
	require.True(t, freshCensus(t, p).ExactInProgress.Stale)

	// Recently started but last updated a day ago: still stale (N3 measures
	// from UpdatedAt, not StartedAt).
	st.StartedAt = time.Now()
	require.NoError(t, p.saveExactCensusState(st, 10, true))
	prog, err = p.ExactCensusInProgress()
	require.NoError(t, err)
	require.True(t, prog.Stale)

	ex := runExact(t, p)
	require.Equal(t, int64(100), censusFamily(t, ex, "work:").Keys, "the stale state's 1 was not reused")
	for _, n := range ex.Notes {
		require.NotContains(t, n, "resumed")
	}
}

// N2: the fingerprint carries the format version.
func TestCensusRegistryFingerprint_IncludesFormat(t *testing.T) {
	require.Len(t, censusRegistryFingerprint(), 16)
	require.Equal(t, 2, censusExactFormatVersion)
}

// N7: a budget wait refused because it would pass the deadline reports
// context.DeadlineExceeded.
func TestCensusWaitBytes_DeadlineIsDeadlineExceeded(t *testing.T) {
	lim := rate.NewLimiter(rate.Limit(1000), 1000)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := censusWaitBytes(ctx, lim, 1<<20, nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

// N6: a range taken as non-empty without a seek is not claimed to hold a
// live key.
func TestCensusProbe_LargeSpanIsEntriesOnly(t *testing.T) {
	p := newCensusRawStore(t)
	ranges := keyFamilyRanges(keyFamilies)
	span := make([]uint64, len(ranges))
	span[0] = censusProbeMaxSpan + 1
	probes, err := p.censusProbeRanges(context.Background(), ranges, span)
	require.NoError(t, err)
	require.True(t, probes[0].entries)
	require.False(t, probes[0].live)
}

func TestCensusMidKey(t *testing.T) {
	cases := [][2]string{{"a", "b"}, {"cat:", "cat;"}, {"", "\xff\xff\xff\xff"}, {"book_ver:A", "book_ver:B"}}
	for _, c := range cases {
		m := censusMidKey([]byte(c[0]), []byte(c[1]))
		require.NotNil(t, m, "%q", c)
		require.Less(t, c[0], string(m))
		require.Less(t, string(m), c[1])
	}
}
