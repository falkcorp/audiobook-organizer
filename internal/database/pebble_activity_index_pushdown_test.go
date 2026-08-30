// file: internal/database/pebble_activity_index_pushdown_test.go
// version: 1.1.1
// guid: 2e9eb1e1-29af-4a5d-8cd9-2be21b5aad0c
// last-edited: 2026-08-30

// Package database — differential suite for the activity secondary-index limit
// pushdown.
//
// WHY this file exists:
//
//	queryByIndexPrefix used to fetch and decode EVERY entry of an operation
//	before slicing out the requested page: 50,000 point-Gets and 50,000
//	json.Unmarshals to return 1,000 rows. queryByIndexPrefixPaged decodes only
//	the page and counts the rest with key-only scans.
//
//	The danger in that trade is not the page — a wrong page is visible. It is
//	`total`, which drives the UI's pager and is returned with no error attached,
//	so an off-by-anything total is a silent wrong answer. Every test here
//	therefore runs the pushdown AND the retained full implementation over the
//	SAME fixture and asserts both the entries and the total are equal. A test
//	that only asserted the pushdown's own output would pass against a pushdown
//	that had quietly redefined what total means.
package database

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── fixture helpers ──────────────────────────────────────────────────────────

// seedOpEntries writes n entries for opID with strictly increasing timestamps
// (so newest-first order is unambiguous and never depends on a tie-break) and
// deliberately varied Type/Level/Source/Tags/BookID so that filter shapes which
// must fall back to the full path have something to reject.
func seedOpEntries(t *testing.T, s *PebbleActivityStore, opID string, n int) {
	t.Helper()
	tiers := []string{"change", "info", "debug", "batch"}
	base := time.Now().UTC().Add(-time.Duration(n) * time.Second)
	for i := 0; i < n; i++ {
		_, err := s.Record(ActivityEntry{
			Timestamp:   base.Add(time.Duration(i) * time.Second),
			Tier:        tiers[i%len(tiers)],
			Type:        fmt.Sprintf("type-%d", i%3),
			Level:       []string{"info", "warn", "error"}[i%3],
			Source:      fmt.Sprintf("src-%d", i%4),
			OperationID: opID,
			BookID:      fmt.Sprintf("book-%d", i%5),
			Summary:     fmt.Sprintf("op entry %03d", i),
			Tags:        []string{fmt.Sprintf("tag-%d", i%2)},
		})
		require.NoError(t, err)
	}
}

// indexRefKeys returns every primary key referenced under prefix, newest-first.
func indexRefKeys(t *testing.T, s *PebbleActivityStore, prefix string) [][]byte {
	t.Helper()
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(prefix),
		UpperBound: []byte(prefix[:len(prefix)-1] + ";"),
	})
	require.NoError(t, err)
	defer iter.Close()

	var out [][]byte
	for iter.Last(); iter.Valid(); iter.Prev() {
		pk, ok := pactPrimaryKeyFromRef(iter.Value())
		require.True(t, ok)
		out = append(out, pk)
	}
	return out
}

// orphanPrimaryRow deletes ONLY the primary row, leaving the secondary index
// ref behind — the exact state production is full of, because deletion paths
// did not remove index keys for most of this store's life.
func orphanPrimaryRow(t *testing.T, s *PebbleActivityStore, primaryKey []byte) {
	t.Helper()
	require.NoError(t, s.db.Delete(primaryKey, nil))
}

// assertPathsAgree is the core assertion: the pushdown and the retained full
// implementation must return identical entries AND an identical total.
func assertPathsAgree(t *testing.T, s *PebbleActivityStore, prefix string, f ActivityFilter) []ActivityEntry {
	t.Helper()
	require.True(t, pactIndexPushdownEligible(prefix, f),
		"this assertion is only meaningful for a filter the pushdown accepts")

	wantEntries, wantTotal, wantErr := s.queryByIndexPrefixFull(context.Background(), prefix, f)
	require.NoError(t, wantErr)
	gotEntries, gotTotal, gotErr := s.queryByIndexPrefixPaged(context.Background(), prefix, f)
	require.NoError(t, gotErr)

	assert.Equal(t, wantTotal, gotTotal, "total must be identical (filter=%+v)", f)
	assert.Equal(t, wantEntries, gotEntries, "entries must be identical (filter=%+v)", f)
	return gotEntries
}

// ── differential matrix ──────────────────────────────────────────────────────

// TestIndexPushdownMatchesFullPathAcrossPages sweeps the pagination surface.
// Every (offset, limit) pair the HTTP layer can produce, plus the boundaries
// around the end of the result set, must agree with the reference on both
// halves of the return value.
func TestIndexPushdownMatchesFullPathAcrossPages(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-matrix"
	const seeded = 137
	seedOpEntries(t, s, opID, seeded)
	prefix := "act:op:" + opID + ":"

	for _, offset := range []int{0, 1, 7, seeded - 1, seeded, seeded + 40} {
		for _, limit := range []int{0, 1, 10, seeded, seeded + 25} {
			f := ActivityFilter{OperationID: opID, Limit: limit, Offset: offset}
			t.Run(fmt.Sprintf("offset=%d/limit=%d", offset, limit), func(t *testing.T) {
				got := assertPathsAgree(t, s, prefix, f)

				want := limit
				if room := seeded - offset; room < want {
					want = room
				}
				if want < 0 {
					want = 0
				}
				assert.Len(t, got, want, "page size")
			})
		}
	}
}

// TestIndexPushdownReturnsNewestFirst pins the ORDER the reverse scan is
// supposed to make free. The full path sorts decoded entries by timestamp
// descending; the pushdown never sorts and relies on the 20-digit zero-padded
// nanos in the index key making lexicographic order chronological. If that
// premise were wrong, this fails while every count-based test still passes.
func TestIndexPushdownReturnsNewestFirst(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-order"
	seedOpEntries(t, s, opID, 40)

	entries := assertPathsAgree(t, s, "act:op:"+opID+":",
		ActivityFilter{OperationID: opID, Limit: 40})

	require.Len(t, entries, 40)
	for i := 1; i < len(entries); i++ {
		assert.True(t, entries[i-1].Timestamp.After(entries[i].Timestamp),
			"entry %d must be newer than entry %d", i-1, i)
	}
}

// ── the subtle one: orphaned index refs ──────────────────────────────────────

// TestIndexPushdownOrphanedRefDoesNotConsumeAPageSlot is the sharpest test in
// this file.
//
// An index ref whose primary row was pruned is skipped by both paths. The trap
// is that a pushdown which took the page by INDEX POSITION would let that
// skipped ref eat a slot and hand back limit-1 rows with no error — a page that
// is silently short. Orphaning the three NEWEST refs puts them exactly where a
// position-based page would swallow them.
func TestIndexPushdownOrphanedRefDoesNotConsumeAPageSlot(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-orphan-head"
	const seeded = 50
	seedOpEntries(t, s, opID, seeded)
	prefix := "act:op:" + opID + ":"

	keys := indexRefKeys(t, s, prefix)
	require.Len(t, keys, seeded)
	for i := 0; i < 3; i++ {
		orphanPrimaryRow(t, s, keys[i]) // the three newest
	}

	entries, total, err := s.queryByIndexPrefixPaged(context.Background(), prefix,
		ActivityFilter{OperationID: opID, Limit: 10})
	require.NoError(t, err)

	assert.Len(t, entries, 10, "a pruned row must shift the page, not shrink it")
	assert.Equal(t, seeded-3, total, "orphaned refs must not be counted")

	assertPathsAgree(t, s, prefix, ActivityFilter{OperationID: opID, Limit: 10})
}

// TestIndexPushdownOrphansScatteredThroughoutAgreeWithFullPath spreads orphans
// across the range — head, middle, tail — and over several page windows, which
// is where an off-by-one in the rank bookkeeping shows up.
func TestIndexPushdownOrphansScatteredThroughoutAgreeWithFullPath(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-orphan-scatter"
	const seeded = 80
	seedOpEntries(t, s, opID, seeded)
	prefix := "act:op:" + opID + ":"

	keys := indexRefKeys(t, s, prefix)
	require.Len(t, keys, seeded)
	orphaned := 0
	for i := 0; i < len(keys); i++ {
		if i%3 == 0 { // 0, 3, 6, ... — includes the newest and the oldest
			orphanPrimaryRow(t, s, keys[i])
			orphaned++
		}
	}

	for _, offset := range []int{0, 5, 20, seeded} {
		for _, limit := range []int{1, 7, 50} {
			f := ActivityFilter{OperationID: opID, Limit: limit, Offset: offset}
			t.Run(fmt.Sprintf("offset=%d/limit=%d", offset, limit), func(t *testing.T) {
				assertPathsAgree(t, s, prefix, f)
			})
		}
	}

	_, total, err := s.queryByIndexPrefixPaged(context.Background(), prefix,
		ActivityFilter{OperationID: opID, Limit: 5})
	require.NoError(t, err)
	assert.Equal(t, seeded-orphaned, total)
}

// TestIndexPushdownEveryRefOrphanedReturnsNilNotEmpty covers the degenerate
// index: refs exist, not one primary row does. The full path's `all` is nil and
// all[0:0] on a nil slice is nil, so the pushdown must return nil too — an
// empty non-nil slice is a difference reflect.DeepEqual sees and JSON does not,
// which is the kind of divergence that survives review.
func TestIndexPushdownEveryRefOrphanedReturnsNilNotEmpty(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-all-orphan"
	seedOpEntries(t, s, opID, 12)
	prefix := "act:op:" + opID + ":"

	for _, k := range indexRefKeys(t, s, prefix) {
		orphanPrimaryRow(t, s, k)
	}

	entries, total, err := s.queryByIndexPrefixPaged(context.Background(), prefix,
		ActivityFilter{OperationID: opID, Limit: 50})
	require.NoError(t, err)
	assert.Nil(t, entries)
	assert.Zero(t, total)

	assertPathsAgree(t, s, prefix, ActivityFilter{OperationID: opID, Limit: 50})
}

// ── degenerate shapes ────────────────────────────────────────────────────────

// TestIndexPushdownSingleEntryOperation covers the smallest non-empty index:
// one ref, one row. The reverse scan's Last/Prev loop and the existence merge's
// "seek then compare" both have a first-iteration branch that a multi-row
// fixture would exercise only incidentally.
func TestIndexPushdownSingleEntryOperation(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-single"
	seedOpEntries(t, s, opID, 1)
	prefix := "act:op:" + opID + ":"

	entries := assertPathsAgree(t, s, prefix, ActivityFilter{OperationID: opID, Limit: 50})
	require.Len(t, entries, 1)
	assert.Equal(t, "op entry 000", entries[0].Summary)

	assertPathsAgree(t, s, prefix, ActivityFilter{OperationID: opID, Limit: 50, Offset: 1})
}

// TestIndexPushdownUnknownOperationIsEmpty covers a prefix with no refs at all.
func TestIndexPushdownUnknownOperationIsEmpty(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	seedOpEntries(t, s, "op-present", 5)

	prefix := "act:op:op-absent:"
	entries, total, err := s.queryByIndexPrefixPaged(context.Background(), prefix,
		ActivityFilter{OperationID: "op-absent", Limit: 50})
	require.NoError(t, err)
	assert.Nil(t, entries)
	assert.Zero(t, total)

	assertPathsAgree(t, s, prefix, ActivityFilter{OperationID: "op-absent", Limit: 50})
}

// TestIndexPushdownLimitZeroReturnsEmptyPageWithExactTotal pins the current API
// semantics rather than "fixing" them. Today `end := start + f.Limit` with
// Limit 0 yields an empty page and a full total; Query never passes 0 (it
// defaults to 50) but direct callers can, and quietly turning 0 into "all rows"
// would be an unreviewed API change.
func TestIndexPushdownLimitZeroReturnsEmptyPageWithExactTotal(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-limit-zero"
	const seeded = 25
	seedOpEntries(t, s, opID, seeded)
	prefix := "act:op:" + opID + ":"

	entries, total, err := s.queryByIndexPrefixPaged(context.Background(), prefix,
		ActivityFilter{OperationID: opID, Limit: 0})
	require.NoError(t, err)
	assert.Empty(t, entries)
	assert.Equal(t, seeded, total, "limit 0 still reports the exact total")

	assertPathsAgree(t, s, prefix, ActivityFilter{OperationID: opID, Limit: 0})
}

// TestIndexPushdownOffsetPastEndClampsToEmpty pins the other clamp.
func TestIndexPushdownOffsetPastEndClampsToEmpty(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-offset-past-end"
	const seeded = 9
	seedOpEntries(t, s, opID, seeded)
	prefix := "act:op:" + opID + ":"

	entries, total, err := s.queryByIndexPrefixPaged(context.Background(), prefix,
		ActivityFilter{OperationID: opID, Limit: 10, Offset: 500})
	require.NoError(t, err)
	assert.Empty(t, entries)
	assert.Equal(t, seeded, total)

	assertPathsAgree(t, s, prefix, ActivityFilter{OperationID: opID, Limit: 10, Offset: 500})
}

// ── the book index ───────────────────────────────────────────────────────────

// TestIndexPushdownBookIndexAgreesWithFullPath: the book family shares the code
// path but not the prefix, and pactIndexPushdownEligible decides the id
// predicate per family. A test on act:op: alone would not notice the book
// branch being wrong.
func TestIndexPushdownBookIndexAgreesWithFullPath(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	seedOpEntries(t, s, "op-for-books", 60) // BookID cycles book-0..book-4

	prefix := "act:bk:book-2:"
	entries := assertPathsAgree(t, s, prefix, ActivityFilter{BookID: "book-2", Limit: 5})
	assert.Len(t, entries, 5)
	for _, e := range entries {
		assert.Equal(t, "book-2", e.BookID)
	}

	assertPathsAgree(t, s, prefix, ActivityFilter{BookID: "book-2", Limit: 50, Offset: 3})
}

// ── eligibility ──────────────────────────────────────────────────────────────

// TestIndexPushdownEligibilityRefusesUndecidableFilters is the guard on the
// whole design: a filter that reads an entry field the index does not carry
// must NOT take the pushdown, because the pushdown counts rows it never decodes
// and would report a total that includes rows matchesFilter rejects.
func TestIndexPushdownEligibilityRefusesUndecidableFilters(t *testing.T) {
	const opPrefix = "act:op:op-x:"
	const bkPrefix = "act:bk:book-x:"

	refused := map[string]ActivityFilter{
		"type":            {OperationID: "op-x", Limit: 10, Type: "scan"},
		"level":           {OperationID: "op-x", Limit: 10, Level: "error"},
		"source":          {OperationID: "op-x", Limit: 10, Source: "src-1"},
		"search":          {OperationID: "op-x", Limit: 10, Search: "boom"},
		"tags":            {OperationID: "op-x", Limit: 10, Tags: []string{"a"}},
		"exclude_sources": {OperationID: "op-x", Limit: 10, ExcludeSources: []string{"a"}},
		"exclude_tags":    {OperationID: "op-x", Limit: 10, ExcludeTags: []string{"a"}},
		"tier":            {OperationID: "op-x", Limit: 10, Tier: "change"},
		"exclude_tiers":   {OperationID: "op-x", Limit: 10, ExcludeTiers: []string{"debug"}},
		"book_on_op":      {OperationID: "op-x", Limit: 10, BookID: "book-1"},
		"negative_limit":  {OperationID: "op-x", Limit: -1},
		"negative_offset": {OperationID: "op-x", Limit: 10, Offset: -1},
	}
	for name, f := range refused {
		t.Run("refused/"+name, func(t *testing.T) {
			assert.False(t, pactIndexPushdownEligible(opPrefix, f))
		})
	}

	accepted := map[string]ActivityFilter{
		"bare":       {OperationID: "op-x", Limit: 10},
		"offset":     {OperationID: "op-x", Limit: 10, Offset: 20},
		"limit_zero": {OperationID: "op-x", Limit: 0},
		// Since/Until are ignored by BOTH paths (matchesFilter never reads them
		// and neither path applies a time bound), so allowing them changes
		// nothing. Pinned here so a later "helpful" time-bounding of the reverse
		// scan has to confront this test.
		"since_until": {OperationID: "op-x", Limit: 10, Since: timePtr(time.Now()), Until: timePtr(time.Now())},
	}
	for name, f := range accepted {
		t.Run("accepted/"+name, func(t *testing.T) {
			assert.True(t, pactIndexPushdownEligible(opPrefix, f))
		})
	}

	t.Run("refused/op_on_book_path", func(t *testing.T) {
		assert.False(t, pactIndexPushdownEligible(bkPrefix,
			ActivityFilter{BookID: "book-x", OperationID: "op-1", Limit: 10}))
	})
	t.Run("accepted/book_path", func(t *testing.T) {
		assert.True(t, pactIndexPushdownEligible(bkPrefix, ActivityFilter{BookID: "book-x", Limit: 10}))
	})
	t.Run("refused/unknown_family", func(t *testing.T) {
		assert.False(t, pactIndexPushdownEligible("act:zz:thing:", ActivityFilter{Limit: 10}))
	})
}

// TestIndexPushdownIneligibleFiltersStillCorrectThroughQuery proves the refusal
// is a fallback, not a hole: an ineligible filter still returns the right rows
// AND the right total, via the full path.
//
// The limit here is deliberately SMALLER than the number of matching rows. With
// a limit larger than the match count, a pushdown that wrongly accepted this
// filter would still walk every ref (its loop only stops when the page is full)
// and decrement its way to the correct total by accident — the test would pass
// over a broken dispatch. At limit 5 of 20 matches it stops early and reports an
// inflated total, which is the failure that has to be visible.
func TestIndexPushdownIneligibleFiltersStillCorrectThroughQuery(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-ineligible"
	const seeded = 60
	seedOpEntries(t, s, opID, seeded) // Type cycles type-0..type-2

	entries, total, err := s.Query(context.Background(),
		ActivityFilter{OperationID: opID, Type: "type-1", Limit: 5})
	require.NoError(t, err)
	assert.Equal(t, seeded/3, total,
		"total must count every matching row, not stop when the page filled")
	assert.Len(t, entries, 5)
	for _, e := range entries {
		assert.Equal(t, "type-1", e.Type)
	}
}

// ── the cost claim ───────────────────────────────────────────────────────────

// TestIndexPushdownDecodesOnlyThePage is the instrument that proves the
// pushdown is real rather than merely correct.
//
// EntriesDecoded counts every stored-entry decode in this store. The full path
// decodes one per entry of the operation; the pushdown decodes one per row of
// the requested page. Asserting the exact counts on the SAME fixture makes a
// silent regression to full materialization fail here, which is the failure
// this whole change exists to prevent coming back.
func TestIndexPushdownDecodesOnlyThePage(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-cost"
	const seeded = 300
	const limit = 10
	seedOpEntries(t, s, opID, seeded)
	prefix := "act:op:" + opID + ":"
	f := ActivityFilter{OperationID: opID, Limit: limit}

	beforeFull := s.EntriesDecoded()
	_, _, err := s.queryByIndexPrefixFull(context.Background(), prefix, f)
	require.NoError(t, err)
	fullDecodes := s.EntriesDecoded() - beforeFull

	beforePaged := s.EntriesDecoded()
	_, _, err = s.queryByIndexPrefixPaged(context.Background(), prefix, f)
	require.NoError(t, err)
	pagedDecodes := s.EntriesDecoded() - beforePaged

	assert.Equal(t, int64(seeded), fullDecodes,
		"the reference path decodes every entry of the operation")
	assert.Equal(t, int64(limit), pagedDecodes,
		"the pushdown must decode the page and nothing else")
}

// TestIndexPushdownDecodesOffsetPageOnly pins the same bound for a deep page:
// cost must track the page, not the offset.
func TestIndexPushdownDecodesOffsetPageOnly(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-cost-offset"
	seedOpEntries(t, s, opID, 300)

	before := s.EntriesDecoded()
	_, _, err := s.queryByIndexPrefixPaged(context.Background(), "act:op:"+opID+":",
		ActivityFilter{OperationID: opID, Limit: 5, Offset: 200})
	require.NoError(t, err)
	assert.Equal(t, int64(5), s.EntriesDecoded()-before,
		"skipping to an offset must not decode the rows it skipped")
}

// ── corrupt rows ─────────────────────────────────────────────────────────────

// TestIndexPushdownUndecodableRowInPageIsCountedAndCorrected covers the one
// documented boundary of the design. A row that EXISTS but whose stored JSON
// will not decode is excluded from the full path's total. The pushdown counts
// existence without decoding, so it can only correct for such a row when the
// row falls inside the page window — and it must: dropped from the total, and
// not consuming a page slot.
func TestIndexPushdownUndecodableRowInPageIsCountedAndCorrected(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-corrupt"
	const seeded = 20
	seedOpEntries(t, s, opID, seeded)
	prefix := "act:op:" + opID + ":"

	keys := indexRefKeys(t, s, prefix)
	require.Len(t, keys, seeded)
	// Corrupt the newest row's value in place: the ref stays, the row stays,
	// only the body becomes undecodable.
	require.NoError(t, s.db.Set(keys[0], []byte("{not json"), nil))

	beforeFailures := s.DecodeFailures()
	entries, total, err := s.queryByIndexPrefixPaged(context.Background(), prefix,
		ActivityFilter{OperationID: opID, Limit: 5})
	require.NoError(t, err)

	assert.Len(t, entries, 5, "the undecodable row must not consume a page slot")
	assert.Equal(t, seeded-1, total, "an undecodable row inside the page corrects the total")
	assert.Equal(t, int64(1), s.DecodeFailures()-beforeFailures,
		"the drop must be counted, not silent")

	// Order matters below this line: assertPathsAgree runs BOTH implementations
	// over the same corrupt row, so it adds two more decode failures to the
	// store's counters. The DecodeFailures assertion above must stay ahead of
	// it — moving it after would silently change what that assertion measures
	// from "the pushdown counted one drop" to "three drops accumulated".
	assertPathsAgree(t, s, prefix, ActivityFilter{OperationID: opID, Limit: 5})
}

// TestIndexPushdownFullPathCountsItsOwnDecodeFailures pins the instrument gap
// this change also closed: before it, the index path was the one scan in this
// store that decoded rows without touching EntriesDecoded/DecodeFailures, so an
// undecodable row here vanished with no counter and no log.
func TestIndexPushdownFullPathCountsItsOwnDecodeFailures(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-corrupt-full"
	seedOpEntries(t, s, opID, 6)
	prefix := "act:op:" + opID + ":"

	keys := indexRefKeys(t, s, prefix)
	require.NoError(t, s.db.Set(keys[0], []byte("{not json"), nil))

	beforeFailures := s.DecodeFailures()
	beforeDecodes := s.EntriesDecoded()
	_, total, err := s.queryByIndexPrefixFull(context.Background(), prefix,
		ActivityFilter{OperationID: opID, Type: "type-0", Limit: 50})
	require.NoError(t, err)

	assert.Equal(t, int64(1), s.DecodeFailures()-beforeFailures)
	assert.Equal(t, int64(6), s.EntriesDecoded()-beforeDecodes)
	assert.GreaterOrEqual(t, total, 0)
}

// ── malformed refs ───────────────────────────────────────────────────────────

// TestIndexPushdownMalformedRefIsSkippedByBothPaths: pactPrimaryKeyFromRef
// rejects a ref with no ':' and the full path skips it, so it is absent from
// that path's total. The pushdown's reverse scan applies the same rejection,
// and this pins that the two rejections stay identical.
func TestIndexPushdownMalformedRefIsSkippedByBothPaths(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const opID = "op-malformed"
	const seeded = 10
	seedOpEntries(t, s, opID, seeded)
	prefix := "act:op:" + opID + ":"

	// A ref value with no ':' at all, under a well-formed index key that sorts
	// newest (a far-future timestamp).
	badKey := []byte(fmt.Sprintf("%s%020d:%s", prefix, time.Now().Add(time.Hour).UnixNano(), "01MALFORMEDMALFORMEDMALF"))
	require.NoError(t, s.db.Set(badKey, []byte("no-colon-here"), nil))

	entries, total, err := s.queryByIndexPrefixPaged(context.Background(), prefix,
		ActivityFilter{OperationID: opID, Limit: 50})
	require.NoError(t, err)
	assert.Len(t, entries, seeded)
	assert.Equal(t, seeded, total)

	assertPathsAgree(t, s, prefix, ActivityFilter{OperationID: opID, Limit: 50})
}

// TestIndexPushdownRefPointingAtAForeignRowIsRejected exercises the one
// assumption pactIndexPushdownEligible makes that is not provable from the
// filter alone: that every ref under act:op:<X>: belongs to an entry whose
// OperationID is X, because that is how Record and the backfill build the key.
//
// If the index ever disagreed with the row it points at, counting refs would
// leak a foreign entry into someone else's transcript. The page rows are run
// through matchesFilter for exactly this reason, and this test is what makes
// that line load-bearing instead of decorative: it plants a ref under one
// operation pointing at another operation's row.
func TestIndexPushdownRefPointingAtAForeignRowIsRejected(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const mine = "op-mine"
	const theirs = "op-theirs"
	seedOpEntries(t, s, mine, 8)
	seedOpEntries(t, s, theirs, 4)

	// Take one of the OTHER operation's rows and index it under ours.
	foreign := indexRefKeys(t, s, "act:op:"+theirs+":")
	require.NotEmpty(t, foreign)
	suffix, ok := pactPrimaryKeySuffix(foreign[0])
	require.True(t, ok)
	ref := foreign[0][len("act:"):]
	require.NoError(t, s.db.Set([]byte("act:op:"+mine+":"+suffix), ref, nil))

	prefix := "act:op:" + mine + ":"
	entries, total, err := s.queryByIndexPrefixPaged(context.Background(), prefix,
		ActivityFilter{OperationID: mine, Limit: 50})
	require.NoError(t, err)

	assert.Equal(t, 8, total, "a ref pointing at another operation's row must not be counted")
	for _, e := range entries {
		assert.Equal(t, mine, e.OperationID, "no foreign row may leak into the page")
	}

	assertPathsAgree(t, s, prefix, ActivityFilter{OperationID: mine, Limit: 50})
}

// ── cancellation ─────────────────────────────────────────────────────────────

// TestIndexPushdownAbortsWhenCallerGoesAway: the pushdown replaced the loops
// that carried the fast path's cancellation checks, so those checks have to be
// re-proved on the new loops rather than inherited from the old test.
func TestIndexPushdownAbortsWhenCallerGoesAway(t *testing.T) {
	pinCtxCheckInterval(t)
	s := newTestPebbleActivityStore(t)

	const opID = "op-pushdown-cancel"
	seedOpEntries(t, s, opID, 120)

	ctx := newTrippingContext(2)
	entries, total, err := s.queryByIndexPrefixPaged(ctx, "act:op:"+opID+":",
		ActivityFilter{OperationID: opID, Limit: 50})

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, entries)
	assert.Zero(t, total)
}

// TestIndexPushdownIndexScanIsCancellable pins the FIRST loop's check on its
// own. Driving cancellation through queryByIndexPrefixPaged cannot distinguish
// the two loops — deleting the scan's check just lets the existence pass's
// check fire instead, and the end-to-end test stays green over a loop that no
// longer checks anything. Calling scanIndexRefs directly is what makes that
// mutation fail.
func TestIndexPushdownIndexScanIsCancellable(t *testing.T) {
	pinCtxCheckInterval(t)
	s := newTestPebbleActivityStore(t)

	const opID = "op-scan-cancel"
	seedOpEntries(t, s, opID, 30)

	sc, err := s.scanIndexRefs(newTrippingContext(0), "act:op:"+opID+":")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, sc)
}

// TestIndexPushdownExistencePassIsCancellable drives cancellation past the
// index scan and into markLiveRefs, the loop that does the per-ref work. A
// context that trips during the first loop would leave the second loop's check
// unproven.
func TestIndexPushdownExistencePassIsCancellable(t *testing.T) {
	pinCtxCheckInterval(t)
	s := newTestPebbleActivityStore(t)

	const opID = "op-pushdown-cancel-2"
	const seeded = 40
	seedOpEntries(t, s, opID, seeded)

	sc, err := s.scanIndexRefs(context.Background(), "act:op:"+opID+":")
	require.NoError(t, err)
	require.Equal(t, seeded, sc.len())

	alive := make([]bool, sc.len())
	live, err := s.markLiveRefs(newTrippingContext(0), sc, alive)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, live)
}

// TestIndexPushdownPageFetchIsCancellable pins the THIRD loop's check.
//
// Cancelling through the whole query trips in the first loop, and cancelling
// markLiveRefs directly trips in the second, so neither notices the page loop's
// check disappearing. The fixture is one entry so the call sequence is short
// enough to name exactly: one check on entry, one in the index scan, one in the
// existence merge, and the FOURTH is the page loop's — the only one this
// context lets through as an error.
func TestIndexPushdownPageFetchIsCancellable(t *testing.T) {
	pinCtxCheckInterval(t)
	s := newTestPebbleActivityStore(t)

	const opID = "op-page-cancel"
	seedOpEntries(t, s, opID, 1)

	ctx := newTrippingContext(3)
	entries, total, err := s.queryByIndexPrefixPaged(ctx, "act:op:"+opID+":",
		ActivityFilter{OperationID: opID, Limit: 50})

	require.Error(t, err, "the page fetch must abandon an abandoned request too")
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, entries)
	assert.Zero(t, total)
	assert.Equal(t, 4, ctx.checkCount(),
		"exactly one check per loop plus the entry check; if this count moves, "+
			"re-derive it before changing the number — a check may have been deleted")
}

// ── key-layout premise ───────────────────────────────────────────────────────

// TestIndexKeyOrderIsChronological pins the premise the whole pushdown rests
// on: because the unix-nano is zero-padded to a FIXED 20 digits, byte order is
// time order. Drop the padding and lexicographic order stops matching
// chronological order the moment the digit count changes — this test is what
// would catch that.
func TestIndexKeyOrderIsChronological(t *testing.T) {
	early := time.Unix(0, 999999999).UTC()
	late := time.Unix(0, 1000000000).UTC()

	earlyKey := pactIndexRef("change", early, "01AAA")
	lateKey := pactIndexRef("change", late, "01AAA")

	require.True(t, late.After(early))
	assert.Negative(t, bytes.Compare(earlyKey, lateKey),
		"a later timestamp must sort strictly after an earlier one across a digit-count boundary")
}

func timePtr(t time.Time) *time.Time { return &t }
