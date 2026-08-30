// file: internal/database/zz_review2987_test.go
// version: 1.0.0
// guid: 7c1a55f2-4d9e-4a21-9f31-8e0b6a2c1d40
// last-edited: 2026-08-30

// Adversarial differential review harness for PR #2987. NOT for merge.
package database

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// recAt records one entry for opID at an explicit instant.
func recAt(t *testing.T, s *PebbleActivityStore, opID string, ts time.Time, summary string) {
	t.Helper()
	_, err := s.Record(ActivityEntry{
		Timestamp:   ts,
		Tier:        "change",
		Type:        "t",
		Level:       "info",
		Source:      "src",
		OperationID: opID,
		Summary:     summary,
	})
	require.NoError(t, err)
}

// ── DEFECT 1: total inflated by a filter-failing ref OUTSIDE the page window ──
//
// The author's own TestIndexPushdownRefPointingAtAForeignRowIsRejected asserts
// "a ref pointing at another operation's row must not be counted", but uses
// Limit:50 over 9 refs so the foreign ref is always decoded inside the page.
// With a limit small enough that the foreign ref falls outside the page, the
// pushdown counts it and the full path does not.
func TestReview_ForeignRefOutsidePageInflatesTotal(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const mine = "op-mine"
	const theirs = "op-theirs"

	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 8; i++ {
		recAt(t, s, mine, base.Add(time.Duration(i+10)*time.Second), fmt.Sprintf("mine-%d", i))
	}
	// One row belonging to a DIFFERENT operation, timestamped older than all of
	// mine so it sorts last.
	recAt(t, s, theirs, base, "theirs-0")

	foreign := indexRefKeys(t, s, "act:op:"+theirs+":")
	require.Len(t, foreign, 1)
	suffix, ok := pactPrimaryKeySuffix(foreign[0])
	require.True(t, ok)
	ref := foreign[0][len("act:"):]
	// Plant it under MY operation's index, key suffix identical to the row's own
	// so key order and ref order stay consistent (this is not an ordering test).
	require.NoError(t, s.db.Set([]byte("act:op:"+mine+":"+suffix), ref, nil))

	prefix := "act:op:" + mine + ":"
	for _, limit := range []int{1, 2, 4, 8, 9, 50} {
		f := ActivityFilter{OperationID: mine, Limit: limit}
		require.True(t, pactIndexPushdownEligible(prefix, f))
		_, wantTotal, err := s.queryByIndexPrefixFull(context.Background(), prefix, f)
		require.NoError(t, err)
		_, gotTotal, err := s.queryByIndexPrefixPaged(context.Background(), prefix, f)
		require.NoError(t, err)
		t.Logf("limit=%2d  full.total=%d  paged.total=%d  %s", limit, wantTotal, gotTotal,
			map[bool]string{true: "AGREE", false: "*** DISAGREE ***"}[wantTotal == gotTotal])
	}
}

// ── DEFECT 1b: a filter-failing ref BEFORE the offset consumes a rank ─────────
//
// The page loop skips refs while rank < Offset without ever running
// matchesFilter on them, so a foreign row before the offset shifts the page.
// The PR documents this only for UNDECODABLE rows.
func TestReview_ForeignRefBeforeOffsetShiftsPage(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const mine = "op-mine2"
	const theirs = "op-theirs2"

	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 8; i++ {
		recAt(t, s, mine, base.Add(time.Duration(i+10)*time.Second), fmt.Sprintf("mine-%d", i))
	}
	// Newest of all -> lands at rank 0, inside the skipped prefix for Offset>=1.
	recAt(t, s, theirs, base.Add(time.Hour), "theirs-newest")

	foreign := indexRefKeys(t, s, "act:op:"+theirs+":")
	require.Len(t, foreign, 1)
	suffix, ok := pactPrimaryKeySuffix(foreign[0])
	require.True(t, ok)
	require.NoError(t, s.db.Set([]byte("act:op:"+mine+":"+suffix), foreign[0][len("act:"):], nil))

	prefix := "act:op:" + mine + ":"
	for _, off := range []int{0, 1, 2, 3} {
		f := ActivityFilter{OperationID: mine, Limit: 2, Offset: off}
		wantE, wantT, err := s.queryByIndexPrefixFull(context.Background(), prefix, f)
		require.NoError(t, err)
		gotE, gotT, err := s.queryByIndexPrefixPaged(context.Background(), prefix, f)
		require.NoError(t, err)
		t.Logf("offset=%d full{total=%d %s} paged{total=%d %s}", off,
			wantT, summaries(wantE), gotT, summaries(gotE))
	}
}

func summaries(es []ActivityEntry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Summary)
	}
	return out
}

// ── DEFECT 2: tied timestamps ────────────────────────────────────────────────
//
// The full path sorts with sort.Slice (UNSTABLE) on Timestamp.After. The
// pushdown orders by index key, i.e. by ULID descending within a nanosecond.
// The author's fixture uses strictly increasing timestamps and so cannot
// observe this.
func TestReview_TiedTimestampsDivergeOrDoNot(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-ties"
	ts := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	for i := 0; i < 12; i++ {
		recAt(t, s, opID, ts, fmt.Sprintf("tied-%02d", i))
	}
	prefix := "act:op:" + opID + ":"
	disagreements := 0
	for _, off := range []int{0, 1, 3, 6} {
		for _, lim := range []int{1, 3, 5, 12} {
			f := ActivityFilter{OperationID: opID, Limit: lim, Offset: off}
			wantE, wantT, err := s.queryByIndexPrefixFull(context.Background(), prefix, f)
			require.NoError(t, err)
			gotE, gotT, err := s.queryByIndexPrefixPaged(context.Background(), prefix, f)
			require.NoError(t, err)
			if wantT != gotT || fmt.Sprint(summaries(wantE)) != fmt.Sprint(summaries(gotE)) {
				disagreements++
				t.Logf("DISAGREE off=%d lim=%d full{t=%d %v} paged{t=%d %v}",
					off, lim, wantT, summaries(wantE), gotT, summaries(gotE))
			}
		}
	}
	t.Logf("tied-timestamp disagreements: %d / 16", disagreements)
}

// ── DEFECT 3: pre-1970 timestamps break lexicographic==chronological ─────────
func TestReview_NegativeNanoOrdering(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-neg"
	// Three instants, strictly increasing in time, two of them pre-epoch.
	stamps := []time.Time{
		time.Unix(-2_000_000_000, 0).UTC(), // 1906
		time.Unix(-100_000_000, 0).UTC(),   // 1966
		time.Unix(1_700_000_000, 0).UTC(),  // 2023
	}
	for i, ts := range stamps {
		recAt(t, s, opID, ts, fmt.Sprintf("neg-%d", i))
	}
	prefix := "act:op:" + opID + ":"
	f := ActivityFilter{OperationID: opID, Limit: 10}
	wantE, wantT, err := s.queryByIndexPrefixFull(context.Background(), prefix, f)
	require.NoError(t, err)
	gotE, gotT, err := s.queryByIndexPrefixPaged(context.Background(), prefix, f)
	require.NoError(t, err)
	t.Logf("full  total=%d order=%v", wantT, summaries(wantE))
	t.Logf("paged total=%d order=%v", gotT, summaries(gotE))
	if fmt.Sprint(summaries(wantE)) != fmt.Sprint(summaries(gotE)) {
		t.Logf("*** ORDER DISAGREES ***")
	}
}

// ── Randomized differential sweep ────────────────────────────────────────────
func TestReview_RandomizedDifferential(t *testing.T) {
	rng := rand.New(rand.NewSource(20260830))
	failures := 0
	for trial := 0; trial < 40; trial++ {
		s := newTestPebbleActivityStore(t)
		opID := fmt.Sprintf("op-rand-%d", trial)
		n := 1 + rng.Intn(40)
		base := time.Now().UTC().Add(-24 * time.Hour)
		for i := 0; i < n; i++ {
			recAt(t, s, opID, base.Add(time.Duration(i)*time.Millisecond), fmt.Sprintf("e%03d", i))
		}
		prefix := "act:op:" + opID + ":"
		// Randomly orphan some primary rows.
		keys := indexRefKeys(t, s, prefix)
		for _, k := range keys {
			if rng.Intn(3) == 0 {
				orphanPrimaryRow(t, s, k)
			}
		}
		for probe := 0; probe < 12; probe++ {
			f := ActivityFilter{
				OperationID: opID,
				Limit:       rng.Intn(n + 3),
				Offset:      rng.Intn(n + 3),
			}
			if !pactIndexPushdownEligible(prefix, f) {
				continue
			}
			wantE, wantT, wErr := s.queryByIndexPrefixFull(context.Background(), prefix, f)
			gotE, gotT, gErr := s.queryByIndexPrefixPaged(context.Background(), prefix, f)
			if (wErr == nil) != (gErr == nil) {
				failures++
				t.Errorf("trial=%d err mismatch %v vs %v", trial, wErr, gErr)
				continue
			}
			if wantT != gotT {
				failures++
				t.Errorf("trial=%d n=%d f=%+v total %d != %d", trial, n, f, wantT, gotT)
			}
			if fmt.Sprint(summaries(wantE)) != fmt.Sprint(summaries(gotE)) {
				failures++
				t.Errorf("trial=%d n=%d f=%+v entries %v != %v", trial, n, f, summaries(wantE), summaries(gotE))
			}
			if (wantE == nil) != (gotE == nil) {
				failures++
				t.Errorf("trial=%d f=%+v nil-vs-empty mismatch: full nil=%v paged nil=%v",
					trial, f, wantE == nil, gotE == nil)
			}
		}
	}
	t.Logf("randomized differential failures: %d", failures)
}

// ── Boundary sweep: offset exactly at / beyond surviving count, limit 0 ───────
func TestReview_BoundaryNilVsEmpty(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-bounds"
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 6; i++ {
		recAt(t, s, opID, base.Add(time.Duration(i)*time.Second), fmt.Sprintf("b%d", i))
	}
	prefix := "act:op:" + opID + ":"
	// Orphan 2 -> 4 survive.
	keys := indexRefKeys(t, s, prefix)
	orphanPrimaryRow(t, s, keys[0])
	orphanPrimaryRow(t, s, keys[3])

	for _, f := range []ActivityFilter{
		{OperationID: opID, Limit: 0},
		{OperationID: opID, Limit: 0, Offset: 4},
		{OperationID: opID, Limit: 10, Offset: 4},
		{OperationID: opID, Limit: 10, Offset: 5},
		{OperationID: opID, Limit: 10, Offset: 999},
		{OperationID: opID, Limit: 100},
		{OperationID: opID, Limit: 1, Offset: 3},
	} {
		wantE, wantT, err := s.queryByIndexPrefixFull(context.Background(), prefix, f)
		require.NoError(t, err)
		gotE, gotT, err := s.queryByIndexPrefixPaged(context.Background(), prefix, f)
		require.NoError(t, err)
		status := "OK"
		if wantT != gotT || (wantE == nil) != (gotE == nil) || len(wantE) != len(gotE) {
			status = "*** DISAGREE ***"
		}
		t.Logf("%s lim=%d off=%d full{t=%d n=%d nil=%v} paged{t=%d n=%d nil=%v}",
			status, f.Limit, f.Offset, wantT, len(wantE), wantE == nil, gotT, len(gotE), gotE == nil)
	}
}
