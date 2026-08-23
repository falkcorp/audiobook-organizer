// file: internal/database/pebble_activity_cancel_test.go
// version: 1.3.0
// guid: 3e91b7d2-6c04-4a58-9f13-8d27e5a06b41
// last-edited: 2026-08-23

// Package database — regression suite for CANCELLATION of the activity query.
//
// WHY this file exists, and why it is separate from the bounded-scan suite:
//
//	The scan budget and cancellation solve different halves of the same
//	outage and neither substitutes for the other. The budget bounds a request
//	that RUNS TO COMPLETION. Cancellation bounds a request that is ABANDONED.
//	On production, ss -tnp showed ZERO connected clients while 30 goroutines
//	held 30.8 GB inside the activity scan against a 30 GB cap: every one of
//	those requests had been abandoned, and because the scan could not observe
//	that, it kept decoding entries into a response nobody would ever read.
//	Only a process restart freed the memory.
//
//	So the property under test here is not "the query is fast" (that is the
//	bounded suite's job) but "the query STOPS when the caller goes away".
//
// HOW the determinism works:
//
//	Cancelling from another goroutine mid-scan is a race, and cancelling
//	BEFORE the call only proves the entry check at the top of scanNewestFirst
//	works — delete the in-loop check and such a test still passes. That is a
//	test that cannot fail for the reason it claims to.
//
//	Instead these tests drive the real public Query/GetDistinctSources with a
//	context that reports itself cancelled on the Nth call to Err(). With
//	activityCtxCheckInterval pinned to 1, the call sequence is exactly:
//	one check on entry to scanNewestFirst, then one check per row. Tripping
//	on a later check therefore lands strictly INSIDE the row loop, after real
//	rows have been decoded, which is the only place the production bug could
//	be caught.
package database

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// trippingContext reports success for the first n calls to Err() and
// context.Canceled for every call after that. It stands in for a client that
// disconnects partway through a scan, without the timing race that a real
// goroutine-driven cancel would introduce.
//
// Only Err() is overridden: the scan polls Err() rather than selecting on
// Done(), which is deliberate — a select per decoded row costs more than an
// atomic load, and the scan decodes millions of rows.
type trippingContext struct {
	context.Context
	mu     sync.Mutex
	checks int
	after  int
}

func newTrippingContext(after int) *trippingContext {
	return &trippingContext{Context: context.Background(), after: after}
}

func (c *trippingContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checks++
	if c.checks > c.after {
		return context.Canceled
	}
	return nil
}

func (c *trippingContext) checkCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.checks
}

// pinCtxCheckInterval forces a context check on every row so the check
// sequence is countable, and restores the production value afterwards.
func pinCtxCheckInterval(t *testing.T) {
	t.Helper()
	prev := activityCtxCheckInterval
	activityCtxCheckInterval = 1
	t.Cleanup(func() { activityCtxCheckInterval = prev })
}

// TestQueryAbortsMidScanWhenCallerGoesAway is the core cancellation guard.
//
// The context trips on the 4th Err() call: entry check, row 1, row 2, then
// trip. So the scan is aborted with rows already decoded and a partial page
// already accumulated — exactly the state an abandoned production request was
// in when it kept going.
//
// NEGATIVE CONTROL: deleting the in-loop ctx check inside scanNewestFirst
// makes this test fail. With that check gone the only Err() call is the one on
// entry, which returns nil here, so the scan runs to completion and Query
// returns a full page and no error. Verified red, then green again.
func TestQueryAbortsMidScanWhenCallerGoesAway(t *testing.T) {
	pinCtxCheckInterval(t)
	s := newTestPebbleActivityStore(t)

	const seeded = 300
	seedActivityEntries(t, s, seeded, "change", "info", "debug", "batch")

	ctx := newTrippingContext(3)

	before := s.EntriesDecoded()
	entries, total, err := s.Query(ctx, ActivityFilter{Limit: 50})
	decoded := s.EntriesDecoded() - before

	require.Error(t, err, "an abandoned request must surface an error, not a silent partial result")
	assert.ErrorIs(t, err, context.Canceled)

	// The partial page must be dropped, not returned. Handing back a short
	// page with a plausible total is worse than an error: the caller renders
	// or caches it as though the log really were that small.
	assert.Nil(t, entries, "partial page must be released, not returned")
	assert.Zero(t, total, "a cancelled scan has no meaningful total")

	// Proof the abort happened INSIDE the row loop rather than on entry: some
	// rows were decoded, but nothing close to the whole log.
	assert.Greater(t, decoded, int64(0), "scan should have started before cancelling")
	assert.Less(t, decoded, int64(seeded/2),
		"scan must stop promptly after cancellation, not drain the log")
	assert.Greater(t, ctx.checkCount(), 1,
		"more than the entry check must have run; if this is 1 the in-loop check is missing")
}

// TestQueryHonoursAlreadyCancelledContext covers the cheap case: a request
// abandoned before the scan even starts should never open an iterator.
//
// This is the WEAKER of the two tests by design — it passes even with the
// in-loop check removed — so it is kept separate from the mid-scan test rather
// than folded into it. Merging them would produce one test that looks like it
// covers cancellation while only really covering entry.
func TestQueryHonoursAlreadyCancelledContext(t *testing.T) {
	pinCtxCheckInterval(t)
	s := newTestPebbleActivityStore(t)
	seedActivityEntries(t, s, 50, "change")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	before := s.EntriesDecoded()
	entries, total, err := s.Query(ctx, ActivityFilter{Limit: 10})

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, entries)
	assert.Zero(t, total)
	assert.Equal(t, int64(0), s.EntriesDecoded()-before,
		"a request abandoned before the scan started must decode nothing")
}

// TestGetDistinctSourcesAbortsMidScan covers the other half of the OOM heap
// profile: GetDistinctSources ran concurrently with Query on every page load
// and contributed 3.21 GB of its own.
//
// It also pins the cache invariant. Partial counts from a cancelled scan must
// NOT be memoized: caching them would serve a wrong, truncated source list to
// every subsequent caller for the whole TTL, long after the request that
// produced it had gone.
func TestGetDistinctSourcesAbortsMidScan(t *testing.T) {
	pinCtxCheckInterval(t)
	s := newTestPebbleActivityStore(t)

	const seeded = 300
	seedActivityEntries(t, s, seeded, "change", "info", "debug", "batch")

	filter := ActivityFilter{}
	ctx := newTrippingContext(3)

	sources, err := s.GetDistinctSources(ctx, filter)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, sources, "partial counts must be released, not returned")

	// Nothing should have been cached by the cancelled call: a fresh
	// uncancelled query must see the real, complete source list.
	full, err := s.GetDistinctSources(context.Background(), filter)
	require.NoError(t, err)
	assert.Len(t, full, 5, "seeded data has 5 distinct sources; a cached partial would show fewer")

	var total int
	for _, sc := range full {
		total += sc.Count
	}
	assert.Equal(t, seeded, total, "counts must come from a complete scan, not a cancelled one")
}

// TestQueryByIndexPrefixAbortsWhenCallerGoesAway guards the fast path.
//
// Query short-circuits to queryByIndexPrefix whenever OperationID or BookID is
// set, so GET /api/v1/operations/:id/activity never reaches scanNewestFirst.
// Without its own ctx checks, threading a request context into Query would be
// cosmetic on that endpoint — the handler would look fixed while the scan it
// triggers still ran to completion. This test is what makes that concrete.
func TestQueryByIndexPrefixAbortsWhenCallerGoesAway(t *testing.T) {
	pinCtxCheckInterval(t)
	s := newTestPebbleActivityStore(t)

	const opID = "op-cancel-test"
	base := time.Now().UTC().Add(-10 * time.Minute)
	for i := 0; i < 200; i++ {
		_, err := s.Record(ActivityEntry{
			Timestamp:   base.Add(time.Duration(i) * time.Second),
			Tier:        "change",
			Type:        "seeded",
			Level:       "info",
			Source:      "src",
			Summary:     "op entry",
			OperationID: opID,
		})
		require.NoError(t, err)
	}

	ctx := newTrippingContext(2)
	entries, total, err := s.Query(ctx, ActivityFilter{OperationID: opID, Limit: 50})

	require.Error(t, err, "the op-transcript fast path must be cancellable too")
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, entries)
	assert.Zero(t, total)
}

// TestWipeAllActivityAbortsMidWipe covers the third reachable-from-a-request
// scan: WipeAllActivity is invoked from handleWipe, a live HTTP handler, and
// an uncancellable wipe of a large activity log would reintroduce the same
// abandoned-request-keeps-running defect as the unbounded Query/
// GetDistinctSources scans this file otherwise covers.
//
// Unlike a test that only asserts ctx.Err() != nil — which would pass even if
// WipeAllActivity ignored ctx completely and merely happened to return the
// ambient background error — this drives WipeAllActivity's own delete-batch
// loop and proves it stops mid-tier, with the batch already committed staying
// deleted and the rest of that SAME tier left untouched:
//
//   - 750 rows are seeded into a single tier ("change"), enough for 2 delete
//     batches (500 then 250 — see the batch size in WipeAllActivity).
//   - activityCtxCheckInterval is pinned high (not to 1 as elsewhere in this
//     file) so scanTierKVs' own per-row check never fires during the scan of
//     750 rows; every observed ctx.Err() call comes from WipeAllActivity's
//     own per-tier and per-batch checks, which is what this test targets.
//   - the tripping context is calibrated (call-by-call below) to return nil
//     for the per-tier check and the first per-batch check, then Canceled
//     for the second per-batch check — i.e., AFTER the first 500-row batch
//     commits, BEFORE the second one starts.
//
// Assertions are on rows, not just the error:
//   - deleted == 500 exactly (the first batch, and only the first batch —
//     proof the count reflects real committed deletes, not a guess)
//   - deleted < seeded (proof the scan stopped, not merely errored out after
//     finishing)
//   - a fresh, uncancelled Query afterward finds exactly the other 250 rows
//     still present (proof cancellation deleted nothing beyond what it
//     reported, and lost nothing either — a retry has real, boundable work
//     left)
//
// NEGATIVE CONTROL, measured rather than reasoned about: removing the
// per-batch ctx check in WipeAllActivity's delete loop fails EXACTLY the
// three row-count assertions below -- deleted 500->750, the Less check, and
// the remaining-rows Query 250->0.
//
// It does NOT fail require.Error or assert.ErrorIs. Those two keep passing,
// because with the per-batch check gone the wipe drains all 750 rows and then
// a LATER tier's per-tier guard trips instead: "debug" is empty so its scan
// makes no Err() call at all, and "audit"'s per-tier check lands on the
// tripping context's 4th call. The error is still context.Canceled -- just
// raised for the wrong reason, after the damage.
//
// So the error assertions are documentation, not discrimination. Do not trim
// the row-count assertions as redundant on the strength of them: the counts
// are the only thing here that can tell a cancelled wipe from a completed
// one. (An earlier version of this comment claimed err went nil under this
// mutation. It does not -- that was reasoned, not run.)
func TestWipeAllActivityAbortsMidWipe(t *testing.T) {
	// Deliberately NOT pinCtxCheckInterval(t) (which sets it to 1): this test
	// wants scanTierKVs' per-row check to stay silent throughout the 750-row
	// scan, so every Err() call the tripping context sees comes from
	// WipeAllActivity's own per-tier/per-batch checks.
	prevInterval := activityCtxCheckInterval
	activityCtxCheckInterval = 100000
	t.Cleanup(func() { activityCtxCheckInterval = prevInterval })

	s := newTestPebbleActivityStore(t)

	const seeded = 750 // 2 delete batches: 500 then 250
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < seeded; i++ {
		_, err := s.Record(ActivityEntry{
			Tier: "change", Type: "test", Level: "info",
			Source: "test", Summary: "bulk seed",
			Timestamp: base.Add(time.Duration(i) * time.Millisecond),
		})
		require.NoError(t, err)
	}

	// Call sequence: (1) WipeAllActivity's per-tier check before scanning
	// "change" — nil; (2) scanTierKVs' single seen=0 check (interval pinned
	// above 750, so it never fires again) — nil; (3) the per-batch check
	// before batch 1 (rows 0-500) — nil, batch 1 commits; (4) the per-batch
	// check before batch 2 (rows 500-750) — trips.
	ctx := newTrippingContext(3)

	deleted, err := s.WipeAllActivity(ctx)

	require.Error(t, err, "an abandoned wipe request must surface an error, not silently finish or silently stop")
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, int64(500), deleted, "must report exactly the rows actually deleted (one committed batch), not 0 and not the full seeded count")
	assert.Less(t, deleted, int64(seeded), "cancellation must stop the scan early, not drain the whole log first")

	// The other 250 rows were never reached: a retry has real, boundable
	// work left, and cancellation must not have deleted anything beyond the
	// one batch it reported.
	_, total, qErr := s.Query(context.Background(), ActivityFilter{Limit: 2000})
	require.NoError(t, qErr)
	assert.Equal(t, seeded-500, total, "the un-reached rows must be untouched, not lost and not double-deleted")

	// The trip point above is hand-calibrated against three things that live
	// elsewhere: "change" being first in actTiers, the 500-row batch size, and
	// scanTierKVs checking exactly once at seen=0. If any of them moves, the
	// row counts above fail in a way that points at the wipe rather than at
	// the calibration. Pinning the call count makes that failure name itself.
	assert.Equal(t, 4, ctx.checkCount(), "Err() call sequence changed upstream -- recalibrate newTrippingContext, the wipe is not necessarily broken")
}

// TestWipeAllActivityRefusesAlreadyCancelledContext pins the property that
// matters most in this file: a destructive operation handed a context that is
// ALREADY dead deletes nothing at all. Not one batch.
//
// What this test does and does not pin, measured rather than assumed. Each of
// WipeAllActivity's three cancellation checks was removed and the test re-run:
//
//	per-tier guard removed .................... still PASSES
//	scanTierKVs(ctx) -> Background ............ still PASSES
//	per-batch guard removed ................... still PASSES
//	all three removed ......................... FAILS ("expected an error, got nil")
//
// So this is a defense-in-depth assertion, not a single-guard one: any ONE of
// the three stops an already-cancelled wipe on its own, and the test only goes
// red when the last of them is gone. That redundancy is the point on a
// destructive path -- but it does mean this test cannot be used to justify
// keeping any individual check. TestWipeAllActivityAbortsMidWipe above is what
// isolates the per-batch guard; the per-tier guard has no isolating test,
// because scanTierKVs' own seen=0 check (0 % activityCtxCheckInterval == 0
// always) shadows it for every input that reaches it.
//
// An earlier draft of this comment claimed the per-tier guard alone was pinned
// here. It ran, it wasn't, and the numbers above replaced the claim.
func TestWipeAllActivityRefusesAlreadyCancelledContext(t *testing.T) {
	s := newTestPebbleActivityStore(t)

	const seeded = 10
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < seeded; i++ {
		_, err := s.Record(ActivityEntry{
			Tier: "change", Type: "test", Level: "info",
			Source: "test", Summary: "seed",
			Timestamp: base.Add(time.Duration(i) * time.Millisecond),
		})
		require.NoError(t, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	deleted, err := s.WipeAllActivity(ctx)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, deleted, "a wipe handed a dead context must not delete anything, not even one batch")

	// Asserting the count alone would pass against a wipe that deleted rows
	// and then lied about it, so check the store itself.
	_, total, qErr := s.Query(context.Background(), ActivityFilter{Limit: 100})
	require.NoError(t, qErr)
	assert.Equal(t, seeded, total, "every seeded row must survive a wipe that never legitimately started")
}
