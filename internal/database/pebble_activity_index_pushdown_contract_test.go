// file: internal/database/pebble_activity_index_pushdown_contract_test.go
// version: 1.3.1
// guid: 7c1a55f2-4d9e-4a21-9f31-8e0b6a2c1d40
// last-edited: 2026-09-02

// Contract tests for the activity index limit pushdown.
//
// pebble_activity_index_pushdown_test.go proves the two implementations AGREE.
// This file covers the cases where agreement is not the contract:
//
//   - the two ways total is knowingly NOT exact, each pinned to its exact
//     magnitude so it cannot quietly get worse (see queryByIndexPrefixPaged);
//   - the ordering contract on TIED timestamps, which the agreement fixture
//     forecloses by construction because it uses strictly increasing instants;
//   - the write-time rejection that keeps the "lexicographic == chronological"
//     premise of the reverse scan true for every row on disk;
//   - the branches no differential fixture executes at all.
//
// It began as an adversarial differential harness written against PR #2987 and
// is kept because every defect it found was a defect no agreement test could
// see.
package database

import (
	"context"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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

func summaries(es []ActivityEntry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Summary)
	}
	return out
}

// plantForeignRef indexes one of `theirs`'s rows under `mine`'s index prefix,
// keeping the key suffix identical to the row's own so that key order and ref
// order stay consistent. This is the state the store's write path CANNOT
// produce — only pactIndexKeysFor writes these keys — so it stands in for index
// corruption, and it is the one shape pactIndexKeyNamesExactly cannot detect:
// the key names `mine` correctly and only the ROW BODY disagrees.
func plantForeignRef(t *testing.T, s *PebbleActivityStore, mine, theirs string) {
	t.Helper()
	foreign := indexRefKeys(t, s, "act:op:"+theirs+":")
	require.Len(t, foreign, 1, "fixture expects exactly one row for the foreign operation")
	suffix, ok := pactPrimaryKeySuffix(foreign[0])
	require.True(t, ok)
	require.NoError(t, s.db.Set([]byte("act:op:"+mine+":"+suffix), foreign[0][len("act:"):], nil))
}

// ── total: the two documented inexactness cases, pinned to their magnitude ───

// TestIndexPushdownForeignRefOutsidePageInflatesTotalByExactlyOne pins case 2 of
// queryByIndexPrefixPaged's contract.
//
// The agreement test TestIndexPushdownRefPointingAtAForeignRowIsRejected uses
// Limit:50 over 9 refs, so the foreign ref is always decoded INSIDE the page and
// matchesFilter corrects the total. That makes its assertion true but says
// nothing about the limits production actually uses. With a limit small enough
// that the foreign ref falls outside the page window, the pushdown counts it and
// the full path does not.
//
// This is asserted rather than merely documented because a documented hazard is
// not a control: the divergence is +1 per foreign ref, never more, and it
// vanishes once the window reaches the ref. If either half of that changes, this
// fails.
func TestIndexPushdownForeignRefOutsidePageInflatesTotalByExactlyOne(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const mine = "op-mine-outside"
	const theirs = "op-theirs-outside"

	base := time.Now().UTC().Add(-time.Hour)
	for i := range 8 {
		recAt(t, s, mine, base.Add(time.Duration(i+10)*time.Second), fmt.Sprintf("mine-%d", i))
	}
	// Older than every row of mine, so it sorts LAST and a short page never
	// reaches it.
	recAt(t, s, theirs, base, "theirs-0")
	plantForeignRef(t, s, mine, theirs)

	prefix := "act:op:" + mine + ":"
	for _, tc := range []struct {
		limit     int
		wantDelta int
	}{
		{1, 1}, {2, 1}, {4, 1}, {8, 1}, // foreign ref outside the window
		{9, 0}, {50, 0}, // window reaches it; matchesFilter corrects
	} {
		f := ActivityFilter{OperationID: mine, Limit: tc.limit}
		require.True(t, pactIndexPushdownEligible(prefix, f))

		wantEntries, wantTotal, err := s.queryByIndexPrefixFull(context.Background(), prefix, f)
		require.NoError(t, err)
		gotEntries, gotTotal, err := s.queryByIndexPrefixPaged(context.Background(), prefix, f)
		require.NoError(t, err)

		assert.Equal(t, 8, wantTotal, "the reference total is the ground truth (limit=%d)", tc.limit)
		assert.Equal(t, tc.wantDelta, gotTotal-wantTotal,
			"total inflation must be exactly %d at limit=%d", tc.wantDelta, tc.limit)

		// Whatever total says, no foreign row may ever reach a transcript.
		assert.Equal(t, summaries(wantEntries), summaries(gotEntries),
			"the PAGE must agree exactly even where total does not (limit=%d)", tc.limit)
		for _, e := range gotEntries {
			assert.Equal(t, mine, e.OperationID, "no foreign row may leak into the page")
		}
	}
}

// TestIndexPushdownForeignRefBeforeOffsetShiftsPage pins the rank-consumption
// half of the same case.
//
// The page loop skips refs while rank < Offset WITHOUT running matchesFilter on
// them — running it would require the decode the pushdown exists to avoid — so a
// foreign row ranked before the offset consumes a slot and shifts the window by
// one. At offset 0 nothing is skipped and the paths agree exactly; from offset 1
// on, the pushdown's page is the full path's page shifted by one.
func TestIndexPushdownForeignRefBeforeOffsetShiftsPage(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const mine = "op-mine-offset"
	const theirs = "op-theirs-offset"

	base := time.Now().UTC().Add(-time.Hour)
	for i := range 8 {
		recAt(t, s, mine, base.Add(time.Duration(i+10)*time.Second), fmt.Sprintf("mine-%d", i))
	}
	// NEWEST of all, so it lands at rank 0 — inside the skipped prefix for any
	// offset >= 1.
	recAt(t, s, theirs, base.Add(time.Hour), "theirs-newest")
	plantForeignRef(t, s, mine, theirs)

	prefix := "act:op:" + mine + ":"
	for _, off := range []int{0, 1, 2, 3} {
		f := ActivityFilter{OperationID: mine, Limit: 2, Offset: off}
		wantEntries, wantTotal, err := s.queryByIndexPrefixFull(context.Background(), prefix, f)
		require.NoError(t, err)
		gotEntries, gotTotal, err := s.queryByIndexPrefixPaged(context.Background(), prefix, f)
		require.NoError(t, err)

		assert.Equal(t, 8, wantTotal)
		if off == 0 {
			assert.Equal(t, 8, gotTotal, "nothing is skipped at offset 0, so the ref is decoded and corrected")
			assert.Equal(t, summaries(wantEntries), summaries(gotEntries))
			continue
		}
		assert.Equal(t, 9, gotTotal, "the skipped foreign ref stays in total (offset=%d)", off)
		// The reference page at offset N is the pushdown's page at offset N-1:
		// exactly one rank was eaten, never more.
		shifted, _, err := s.queryByIndexPrefixFull(context.Background(), prefix,
			ActivityFilter{OperationID: mine, Limit: 2, Offset: off - 1})
		require.NoError(t, err)
		assert.Equal(t, summaries(shifted), summaries(gotEntries),
			"the page must be shifted by exactly one rank (offset=%d)", off)
	}
}

// TestIndexPushdownUndecodableRowOutsidePageInflatesTotal pins case 1 of the
// contract — the same asymmetry, reached through a corrupt row body instead of a
// corrupt index.
//
// TestIndexPushdownUndecodableRowInPageIsCountedAndCorrected covers the row
// INSIDE the window, where the decode happens and the total self-corrects. This
// is its complement: outside the window there is no decode, so nothing corrects.
func TestIndexPushdownUndecodableRowOutsidePageInflatesTotal(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-undecodable-outside"
	const seeded = 8
	seedOpEntries(t, s, opID, seeded)
	prefix := "act:op:" + opID + ":"

	// Corrupt the OLDEST row's body, so it sorts last and a short page never
	// reaches it. indexRefKeys returns newest-first.
	keys := indexRefKeys(t, s, prefix)
	require.Len(t, keys, seeded)
	require.NoError(t, s.db.Set(keys[seeded-1], []byte("{not json"), nil))

	f := ActivityFilter{OperationID: opID, Limit: 2}
	_, wantTotal, err := s.queryByIndexPrefixFull(context.Background(), prefix, f)
	require.NoError(t, err)
	_, gotTotal, err := s.queryByIndexPrefixPaged(context.Background(), prefix, f)
	require.NoError(t, err)

	assert.Equal(t, seeded-1, wantTotal, "the reference drops the undecodable row")
	assert.Equal(t, 1, gotTotal-wantTotal, "an undecodable row outside the page inflates total by exactly one")

	// And it self-corrects the moment the window reaches it.
	wide := ActivityFilter{OperationID: opID, Limit: 50}
	_, wideWant, err := s.queryByIndexPrefixFull(context.Background(), prefix, wide)
	require.NoError(t, err)
	_, wideGot, err := s.queryByIndexPrefixPaged(context.Background(), prefix, wide)
	require.NoError(t, err)
	assert.Equal(t, wideWant, wideGot, "a window that reaches the row decodes it and corrects total")
}

// ── the guard that keeps a prefix match from standing in for an id match ─────

// TestIndexKeyNamesExactlyRejectsALongerID unit-tests pactIndexKeyNamesExactly
// directly.
//
// It gets its own test because production data cannot reach its reject branch:
// every stored OperationID and BookID is a ULID and therefore colon-free, so no
// real index holds a key for a longer id. That makes this the ONLY place the
// guard is exercised — without it the check would be unverified code defending
// an invariant nothing else states.
func TestIndexKeyNamesExactlyRejectsALongerID(t *testing.T) {
	const prefix = "act:op:A:"
	nano := fmt.Sprintf("%020d", time.Now().UnixNano())
	const ulidStr = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

	for _, tc := range []struct {
		name string
		key  string
		want bool
	}{
		{"exact id", prefix + nano + ":" + ulidStr, true},
		{"longer id A:B", prefix + "B:" + nano + ":" + ulidStr, false},
		{"longer id whose first component is 20 digits", prefix + nano + ":" + nano + ":" + ulidStr, false},
		{"nano too short", prefix + "123:" + ulidStr, false},
		{"nano non-numeric", prefix + "0000000000000000000x:" + ulidStr, false},
		{"negative nano (pre-epoch)", prefix + "-0000000000000000001:" + ulidStr, false},
		{"missing ulid", prefix + nano + ":", false},
		{"no suffix at all", prefix, false},
		{"different id entirely", "act:op:B:" + nano + ":" + ulidStr, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, pactIndexKeyNamesExactly([]byte(tc.key), prefix))
		})
	}
}

// TestIndexPushdownPrefixCollisionIsExcludedFromTotalAndRank proves the guard
// does its job end to end: an id that STARTS WITH the queried id must not be
// counted and must not consume a rank, so both paths agree exactly.
//
// Nothing is planted by hand: the sibling operation is simply RECORDED with an
// id that happens to start with ours plus a colon, and Record then writes
// act:op:op-collide:sub:<nano>:<ulid> through the ordinary path — a key that
// prefix-matches act:op:op-collide: while belonging to a different operation.
// No writer can mint such an id today (every id is a colon-free ULID), which is
// exactly why the guard needs a test: it defends an invariant that currently
// holds by construction and would otherwise be unverified.
func TestIndexPushdownPrefixCollisionIsExcludedFromTotalAndRank(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const mine = "op-collide"
	const sibling = "op-collide:sub"
	const seeded = 6
	seedOpEntries(t, s, mine, seeded)
	// Recorded AFTER, so these rows are newer and would occupy the newest ranks
	// — inside every page window and before every offset — if they were counted.
	seedOpEntries(t, s, sibling, 4)

	prefix := "act:op:" + mine + ":"
	// The collision is real: the sibling's keys do live under our prefix.
	require.Len(t, indexRefKeys(t, s, prefix), seeded+4,
		"fixture precondition: the sibling's keys must prefix-match ours")

	for _, f := range []ActivityFilter{
		{OperationID: mine, Limit: 1},
		{OperationID: mine, Limit: 2, Offset: 1},
		{OperationID: mine, Limit: 3, Offset: 2},
		{OperationID: mine, Limit: 50},
	} {
		entries, total, err := s.queryByIndexPrefixPaged(context.Background(), prefix, f)
		require.NoError(t, err)
		assert.Equal(t, seeded, total,
			"a key for the longer id %q must not be counted (filter=%+v)", sibling, f)
		for _, e := range entries {
			assert.Equal(t, mine, e.OperationID, "no sibling row may leak into the page")
		}
		// And the reference agrees — this is a case where total IS exact,
		// because the guard decides it from the key without a decode.
		assertPathsAgree(t, s, prefix, f)
	}

	// The sibling itself is still queryable on its own exact prefix.
	_, subTotal, err := s.queryByIndexPrefixPaged(context.Background(), "act:op:"+sibling+":",
		ActivityFilter{OperationID: sibling, Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 4, subTotal, "excluding the sibling from OUR total must not hide it from its own")
}

// ── ordering: ties ───────────────────────────────────────────────────────────

// TestIndexPushdownTiedTimestampsAgreeExactly is the fixture the agreement
// matrix cannot provide.
//
// seedOpEntries uses strictly increasing timestamps, so it can never observe a
// tie — and before pactSortEntriesNewestFirst existed, all 16 (offset, limit)
// pairs here disagreed, because the reference sorted with an UNSTABLE sort.Slice
// and had no defined tie order at all. Both paths now order ties by descending
// ULID, and this is what holds them there.
func TestIndexPushdownTiedTimestampsAgreeExactly(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-ties"
	ts := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	const n = 12
	for i := range n {
		recAt(t, s, opID, ts, fmt.Sprintf("tied-%02d", i))
	}
	prefix := "act:op:" + opID + ":"

	for _, off := range []int{0, 1, 3, 6} {
		for _, lim := range []int{1, 3, 5, 12} {
			assertPathsAgree(t, s, prefix, ActivityFilter{OperationID: opID, Limit: lim, Offset: off})
		}
	}

	// And the shared order is the DOCUMENTED one, not merely a shared accident:
	// ties resolve newest-written first, which for ulid.Make's monotonic entropy
	// is descending ULID and therefore reverse insertion order.
	entries, _, err := s.queryByIndexPrefixPaged(context.Background(), prefix,
		ActivityFilter{OperationID: opID, Limit: n})
	require.NoError(t, err)
	want := make([]string, 0, n)
	for i := n - 1; i >= 0; i-- {
		want = append(want, fmt.Sprintf("tied-%02d", i))
	}
	assert.Equal(t, want, summaries(entries), "tied rows must come back newest-written first")
}

// ── the write-time rejection the reverse scan's premise depends on ───────────

// TestPreEpochTimestampIsRefusedAtWrite pins Record's refusal of a timestamp it
// cannot key.
//
// %020d of a negative UnixNano emits a leading '-' (0x2D, below '0'), so such a
// key sorts before every post-epoch key AND sorts in REVERSE order among other
// negatives. Three strictly increasing instants (1906, 1966, 2023) came back
// from the pushdown as [neg-2 neg-0 neg-1] — a silent wrong order with no error
// anywhere. Refusing the write is what makes "lexicographic key order IS
// chronological order" true of every row on disk rather than merely usually
// true, which is the premise scanIndexRefs is built on.
func TestPreEpochTimestampIsRefusedAtWrite(t *testing.T) {
	s := newTestPebbleActivityStore(t)

	for _, ts := range []time.Time{
		time.Unix(-2_000_000_000, 0).UTC(), // 1906
		time.Unix(-100_000_000, 0).UTC(),   // 1966
		time.Unix(-1, 0).UTC(),             // one second before the epoch
	} {
		_, err := s.Record(ActivityEntry{
			Timestamp:   ts,
			Tier:        "change",
			OperationID: "op-preepoch",
			Summary:     "should not be stored",
		})
		require.ErrorIs(t, err, pactErrPreEpochTimestamp, "pre-epoch instant %s must be refused", ts)
	}

	// The epoch itself is representable and must still be accepted.
	_, err := s.Record(ActivityEntry{
		Timestamp:   time.Unix(0, 0).UTC(),
		Tier:        "change",
		OperationID: "op-preepoch",
		Summary:     "epoch is fine",
	})
	require.NoError(t, err)

	// Nothing unsortable reached the index.
	for _, k := range indexRefKeys(t, s, "act:op:op-preepoch:") {
		assert.NotContains(t, string(k), "-", "no key may carry a negative nano: %s", k)
	}

	entries, total, err := s.queryByIndexPrefixPaged(context.Background(), "act:op:op-preepoch:",
		ActivityFilter{OperationID: "op-preepoch", Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total, "only the representable row was stored")
	assert.Equal(t, []string{"epoch is fine"}, summaries(entries))
}

// TestPreEpochRejectionDoesNotDoomTheWholeBatch: the rejection lands in
// prepareEntry, which is by design the one step that can fail for a reason
// attributable to a SINGLE entry — RecordBatch's error semantics rest on that
// split. So one unrepresentable row must not cost the batch its good rows.
func TestPreEpochRejectionDoesNotDoomTheWholeBatch(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	now := time.Now().UTC()

	_, err := s.RecordBatch([]ActivityEntry{
		{Timestamp: now, Tier: "change", OperationID: "op-batch", Summary: "good-0"},
		{Timestamp: time.Unix(-5, 0).UTC(), Tier: "change", OperationID: "op-batch", Summary: "bad"},
		{Timestamp: now.Add(time.Second), Tier: "change", OperationID: "op-batch", Summary: "good-1"},
	})
	require.Error(t, err, "the bad row must be reported, not silently dropped")

	entries, total, qErr := s.queryByIndexPrefixPaged(context.Background(), "act:op:op-batch:",
		ActivityFilter{OperationID: "op-batch", Limit: 10})
	require.NoError(t, qErr)
	assert.Equal(t, 2, total, "both representable rows survive the batch")
	assert.Equal(t, []string{"good-1", "good-0"}, summaries(entries))
}

// ── the branch no differential fixture executes ──────────────────────────────

// TestIndexPushdownRowPrunedBetweenPassesIsUncounted reaches the concurrent-prune
// branch in fetchIndexPage.
//
// That branch fires when a row passes the existence merge and is then gone by the
// time the page loop Gets it, and it adjusts total. Mutation testing showed a
// bare panic() planted there left the WHOLE suite green: no test executed the
// line at all. Splitting fetchIndexPage out of queryByIndexPrefixPaged is what
// makes it reachable — the test runs the existence pass, deletes a primary row
// itself, and then runs the page fetch, so the real branch executes with no
// test-only seam in the production path.
func TestIndexPushdownRowPrunedBetweenPassesIsUncounted(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-pruned-between"
	const seeded = 6
	seedOpEntries(t, s, opID, seeded)
	prefix := "act:op:" + opID + ":"
	ctx := context.Background()

	sc, err := s.scanIndexRefs(ctx, prefix)
	require.NoError(t, err)
	alive := make([]bool, sc.len())
	total, err := s.markLiveRefs(ctx, sc, alive)
	require.NoError(t, err)
	require.Equal(t, seeded, total, "every row exists at the existence pass")

	// The prune lands between the two passes, on the NEWEST row — inside any
	// page window, so the branch is reached rather than skipped.
	keys := indexRefKeys(t, s, prefix)
	require.Len(t, keys, seeded)
	orphanPrimaryRow(t, s, keys[0])

	entries, gotTotal, err := s.fetchIndexPage(ctx, sc, alive, total, prefix,
		ActivityFilter{OperationID: opID, Limit: 50})
	require.NoError(t, err)

	assert.Equal(t, seeded-1, gotTotal, "a row pruned between the passes must be removed from total")
	assert.Len(t, entries, seeded-1, "and must not consume a page slot")
	for _, e := range entries {
		assert.NotEqual(t, "op entry 005", e.Summary, "the pruned row must not be returned")
	}
}

// TestIndexPushdownPrunedRowDoesNotConsumeAPageSlot: the same branch, this time
// proving the RANK behaviour rather than the total. A row pruned between the
// passes must shift the page up, never return a short page.
func TestIndexPushdownPrunedRowDoesNotConsumeAPageSlot(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-pruned-rank"
	const seeded = 6
	seedOpEntries(t, s, opID, seeded)
	prefix := "act:op:" + opID + ":"
	ctx := context.Background()

	sc, err := s.scanIndexRefs(ctx, prefix)
	require.NoError(t, err)
	alive := make([]bool, sc.len())
	total, err := s.markLiveRefs(ctx, sc, alive)
	require.NoError(t, err)

	keys := indexRefKeys(t, s, prefix) // newest-first
	orphanPrimaryRow(t, s, keys[0])

	entries, gotTotal, err := s.fetchIndexPage(ctx, sc, alive, total, prefix,
		ActivityFilter{OperationID: opID, Limit: 2})
	require.NoError(t, err)

	assert.Equal(t, seeded-1, gotTotal)
	assert.Len(t, entries, 2, "the page must still be full — the pruned row shifts it, it does not shorten it")
	assert.Equal(t, []string{"op entry 004", "op entry 003"}, summaries(entries))
}

// ── the eligibility gate must fail CLOSED as ActivityFilter grows ────────────

// TestActivityFilterFieldCountIsPinned fails the suite when a field is added to
// ActivityFilter, so that nobody can add one without classifying it against
// pactIndexPushdownEligible.
//
// WHY a pin and not just a review habit: the gate is an ALLOW-LIST
// (pactPushdownDecidable), so a field added later is REFUSED as soon as it
// carries a value — it silently costs the fast path rather than silently
// returning a wrong total.
// That is the safe failure, but it is still a silent one, and nobody profiling
// a slow endpoint would trace it back to an unclassified filter field. This pin
// is what turns that quiet loss into a build failure that names the cause.
//
// The pin's PRIMARY job is therefore no longer correctness — the allow-list
// refuses an unclassified field on its own. It still carries exactly one
// correctness duty, and it is the Since/Until dependency spelled out below. Do
// not weaken this test on the grounds that the gate now fails closed.
//
// THE LOADED CASE, and it is not hypothetical. Since and Until are accepted
// today, and that is only correct because NEITHER path honours them: matchesFilter
// does not read them and the index path applies no time bounds, so both
// implementations ignore them identically. This is a known separate defect —
// GET /api/v1/activity?operation_id=X&since=... silently ignores `since` — left
// unfixed here on purpose.
//
// The moment somebody fixes THAT defect by teaching queryByIndexPrefixFull to
// honour time bounds, this gate will still say "eligible" and the two paths will
// diverge, silently, on every request that carries a time bound. Whoever fixes
// `since` MUST also make pactIndexPushdownEligible refuse a filter that sets
// Since or Until (or push the bounds into the index scan). This comment is the
// only place that dependency is written down.
func TestActivityFilterFieldCountIsPinned(t *testing.T) {
	const classified = 15
	got := reflect.TypeFor[ActivityFilter]().NumField()
	require.Equal(t, classified, got,
		"ActivityFilter gained or lost a field. pactIndexPushdownEligible is an ALLOW-LIST "+
			"(pactPushdownDecidable): an unclassified field REFUSES the pushdown, so this is a "+
			"silent loss of the fast path, not a wrong answer. Decide whether the new field is "+
			"decidable from the index key ALONE (without decoding the row) — add it to the map "+
			"if so, leave it out if not — then update this count. Read this test's doc comment "+
			"before touching Since/Until.")

	// The classification itself, so a RENAMED field cannot keep the count at 15
	// while quietly changing what is decided.
	var names []string
	for i := range got {
		names = append(names, reflect.TypeFor[ActivityFilter]().Field(i).Name)
	}
	assert.Equal(t, []string{
		"Limit", "Offset", // pagination, handled explicitly (negatives refuse)
		"Type", "Tier", "Level", // refused: not in the index key
		"OperationID", "BookID", // the id predicates — the ONLY ones pushed down
		"Since", "Until", // in pactPushdownDecidable ONLY because both paths ignore them
		"Tags", "Search", "Source", // refused: not in the index key
		"ExcludeSources", "ExcludeTiers", "ExcludeTags", // refused: not in the index key
	}, names, "ActivityFilter's fields changed; re-read pactIndexPushdownEligible")

	// The gate's ALLOW-LIST is pinned too, symmetric with the field list above.
	// Pinning ActivityFilter alone does not catch the sequence that silently
	// widens the pushdown, because every step of it looks reasonable: add a field
	// to ActivityFilter, add it to pactPushdownDecidable, add it to the
	// stillEligible map in the field-by-field test, bump the count here. Four
	// coordinated edits by one author in one sitting, and without this assertion
	// nothing objects. Requiring the list to be restated HERE, under a doc comment
	// that says what an entry costs, is the point.
	var decidable []string
	for name := range pactPushdownDecidable {
		decidable = append(decidable, name)
	}
	sort.Strings(decidable)
	assert.Equal(t, []string{"BookID", "Limit", "Offset", "OperationID", "Since", "Until"}, decidable,
		"pactPushdownDecidable changed. Every entry is a predicate the pushdown claims it can "+
			"evaluate from the INDEX KEY ALONE; a wrong entry returns a wrong `total` with no "+
			"error anywhere. Since and Until must be REMOVED from it when the `since` defect is "+
			"fixed — see this test's doc comment.")
}

// TestIndexPushdownEligibilityIsDecidedFieldByField sets EVERY field of
// ActivityFilter in turn and asserts the gate's answer, one field at a time, so
// a gate that stopped checking one of them cannot hide behind the others. It
// asserts ACCEPTANCE as well as refusal, which is why it is not named for
// refusal alone.
//
// It walks the struct REFLECTIVELY on purpose. The hand-written table this
// replaced listed the ten undecidable fields by name, which meant a field added
// to ActivityFilter later was never exercised by it at all — the test kept
// passing while saying nothing about the new field. Driving the loop off
// reflect.TypeOf means a new field is covered the moment it exists, and the
// expectation below is a deliberately NARROWER restatement of the gate's
// allow-list — see the note on BookID — written out by hand rather than
// imported from pactPushdownDecidable, so that editing the map cannot move the
// test's expectation with it.
func TestIndexPushdownEligibilityIsDecidedFieldByField(t *testing.T) {
	const prefix = "act:op:X:"
	base := ActivityFilter{OperationID: "X", Limit: 10}
	require.True(t, pactIndexPushdownEligible(prefix, base), "the base filter must be eligible")

	// Restated independently of pactPushdownDecidable: on the OP index family a
	// set BookID is the "other id" and must refuse, even though BookID IS in the
	// gate's map (the family switch, not the field walk, is what refuses it).
	stillEligible := map[string]bool{
		"Limit": true, "Offset": true, "OperationID": true, "Since": true, "Until": true,
	}

	ft := reflect.TypeFor[ActivityFilter]()
	for i := 0; i < ft.NumField(); i++ {
		field := ft.Field(i)
		t.Run(field.Name, func(t *testing.T) {
			val := nonZeroFilterValue(field.Type)
			require.True(t, val.IsValid(),
				"ActivityFilter.%s has kind %s, which nonZeroFilterValue cannot build. "+
					"Teach it that kind — otherwise this field is silently untested. If the "+
					"kind has a nil-vs-empty distinction (map, slice), ALSO classify it in "+
					"pactFilterFieldCarriesPredicate: IsZero on those is IsNil, so an empty "+
					"non-nil value would read as a live predicate. See M19.",
				field.Name, field.Type.Kind())

			f := base
			reflect.ValueOf(&f).Elem().Field(i).Set(val)

			assert.Equal(t, stillEligible[field.Name], pactIndexPushdownEligible(prefix, f),
				"%s: a field is eligible only if it is decidable from the index key alone",
				field.Name)
		})
	}

	// An unknown index family refuses outright rather than guessing.
	assert.False(t, pactIndexPushdownEligible("act:zz:X:", base))

	// Negative pagination refuses even though Limit/Offset are decidable:
	// queryByIndexPrefixFull ends in all[start:end], which PANICS when end < start.
	for name, mutate := range map[string]func(*ActivityFilter){
		"negative Limit":  func(f *ActivityFilter) { f.Limit = -1 },
		"negative Offset": func(f *ActivityFilter) { f.Offset = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			f := base
			mutate(&f)
			assert.False(t, pactIndexPushdownEligible(prefix, f))
		})
	}
}

// nonZeroFilterValue builds a value that a filter field would recognise as "set".
// It returns an invalid Value for a kind it does not handle, which the caller
// turns into a failure rather than a silent skip.
func nonZeroFilterValue(t reflect.Type) reflect.Value {
	switch t.Kind() {
	case reflect.String:
		return reflect.ValueOf("x").Convert(t)
	case reflect.Int:
		return reflect.ValueOf(1).Convert(t)
	case reflect.Slice:
		s := reflect.MakeSlice(t, 1, 1)
		elem := nonZeroFilterValue(t.Elem())
		if !elem.IsValid() {
			return reflect.Value{}
		}
		s.Index(0).Set(elem)
		return s
	case reflect.Pointer:
		return reflect.New(t.Elem())
	default:
		return reflect.Value{}
	}
}

// TestIndexPushdownEligibleAcceptsNonNilEmptySlices pins the one behaviour that
// a careless rewrite of the field walk silently breaks.
//
// The gate asks len(slice) > 0, NOT reflect.Value.IsZero. IsZero on a slice is
// defined as IsNil, so a non-nil EMPTY slice would read as a live predicate and
// refuse the pushdown. `"tags": []` in a JSON body unmarshals to exactly that.
// The result would be a correct answer served by the slow path — no error, no
// failing differential test, just the fast path quietly gone for those callers.
// This is the only test that can see the difference.
func TestIndexPushdownEligibleAcceptsNonNilEmptySlices(t *testing.T) {
	const prefix = "act:op:X:"

	// Driven off reflect for the same reason the field-by-field test is: a
	// hand-written list of today's four slice fields would give a slice field
	// added later len-1 coverage from that test and NO empty-slice coverage at
	// all — reintroducing, in this file, the exact hand-enumeration hole this
	// pair of tests exists to close.
	f := ActivityFilter{OperationID: "X", Limit: 10}
	fv := reflect.ValueOf(&f).Elem()
	ft := fv.Type()
	var covered []string
	for i := 0; i < ft.NumField(); i++ {
		if ft.Field(i).Type.Kind() != reflect.Slice {
			continue
		}
		fv.Field(i).Set(reflect.MakeSlice(ft.Field(i).Type, 0, 0))
		covered = append(covered, ft.Field(i).Name)
	}
	require.NotEmpty(t, covered, "no slice fields found — this test would assert nothing")

	assert.True(t, pactIndexPushdownEligible(prefix, f),
		"a non-nil EMPTY slice carries no predicate and must not cost the pushdown (set: %v)",
		covered)
}

// ── randomized differential sweeps ───────────────────────────────────────────

// randomizedDifferential runs the two implementations against each other over
// randomly sized fixtures with randomly orphaned rows, at random pages.
//
// tieGroup > 1 makes groups of that many entries share a timestamp. Both shapes
// are run: the strictly-increasing one is what the hand-written matrix already
// covers, and the tie-bearing one is the shape that produced 149 entry
// mismatches before the ordering contract existed.
func randomizedDifferential(t *testing.T, seed int64, trials, tieGroup int) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	for trial := range trials {
		s := newTestPebbleActivityStore(t)
		opID := fmt.Sprintf("op-rand-%d-%d", tieGroup, trial)
		n := tieGroup + rng.Intn(40)
		base := time.Now().UTC().Add(-24 * time.Hour)
		for i := range n {
			recAt(t, s, opID, base.Add(time.Duration(i/tieGroup)*time.Second), fmt.Sprintf("e%03d", i))
		}
		prefix := "act:op:" + opID + ":"

		// Orphan a random subset — the normal state of this index in production.
		for _, k := range indexRefKeys(t, s, prefix) {
			if rng.Intn(3) == 0 {
				orphanPrimaryRow(t, s, k)
			}
		}

		for range 12 {
			f := ActivityFilter{
				OperationID: opID,
				Limit:       rng.Intn(n + 3),
				Offset:      rng.Intn(n + 3),
			}
			if !pactIndexPushdownEligible(prefix, f) {
				continue
			}
			wantEntries, wantTotal, wantErr := s.queryByIndexPrefixFull(context.Background(), prefix, f)
			gotEntries, gotTotal, gotErr := s.queryByIndexPrefixPaged(context.Background(), prefix, f)

			require.Equal(t, wantErr == nil, gotErr == nil, "trial=%d f=%+v error disagreement: %v vs %v", trial, f, wantErr, gotErr)
			require.NoError(t, wantErr)
			assert.Equal(t, wantTotal, gotTotal, "trial=%d n=%d f=%+v total", trial, n, f)
			assert.Equal(t, summaries(wantEntries), summaries(gotEntries), "trial=%d n=%d f=%+v entries", trial, n, f)
			assert.Equal(t, wantEntries == nil, gotEntries == nil,
				"trial=%d f=%+v nil-vs-empty must match exactly", trial, f)
		}
	}
}

func TestIndexPushdownRandomizedDifferential(t *testing.T) {
	randomizedDifferential(t, 20260830, 40, 1)
}

// TestIndexPushdownRandomizedDifferentialWithTies is the same sweep with tied
// timestamps permitted. seedOpEntries and the strictly-increasing sweep above
// both foreclose ties by construction, so this is the only randomized fixture
// that can observe the ordering contract.
func TestIndexPushdownRandomizedDifferentialWithTies(t *testing.T) {
	randomizedDifferential(t, 20260830, 25, 4)
}

// ── boundaries where nil and empty are distinguishable ───────────────────────

// TestIndexPushdownBoundaryNilVsEmpty sweeps offsets at and past the surviving
// count with orphans present. The two paths must agree not just on length but on
// nil-vs-empty, which a caller can distinguish and which the two return through
// completely different code (a reslice of `all` against a make + append).
func TestIndexPushdownBoundaryNilVsEmpty(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-bounds"
	seedOpEntries(t, s, opID, 6)
	prefix := "act:op:" + opID + ":"

	keys := indexRefKeys(t, s, prefix)
	orphanPrimaryRow(t, s, keys[0])
	orphanPrimaryRow(t, s, keys[3]) // 4 survive

	for _, f := range []ActivityFilter{
		{OperationID: opID, Limit: 0},
		{OperationID: opID, Limit: 0, Offset: 4},
		{OperationID: opID, Limit: 10, Offset: 3},
		{OperationID: opID, Limit: 10, Offset: 4},
		{OperationID: opID, Limit: 10, Offset: 5},
		{OperationID: opID, Limit: 10, Offset: 999},
		{OperationID: opID, Limit: 100},
		{OperationID: opID, Limit: 1, Offset: 3},
	} {
		wantEntries, wantTotal, err := s.queryByIndexPrefixFull(context.Background(), prefix, f)
		require.NoError(t, err)
		gotEntries, gotTotal, err := s.queryByIndexPrefixPaged(context.Background(), prefix, f)
		require.NoError(t, err)

		assert.Equal(t, 4, wantTotal, "four rows survive (filter=%+v)", f)
		assert.Equal(t, wantTotal, gotTotal, "total (filter=%+v)", f)
		assert.Equal(t, summaries(wantEntries), summaries(gotEntries), "entries (filter=%+v)", f)
		assert.Equal(t, wantEntries == nil, gotEntries == nil, "nil-vs-empty (filter=%+v)", f)
	}
}
