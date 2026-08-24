// file: internal/database/series_membership_guard_test.go
// version: 1.1.0
// guid: 4b7f9c02-8e31-4d6a-b5f8-1c93a70de244
// last-edited: 2026-08-24

package database

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// The series MERGE path asks GetBooksBySeriesIDAllVersions which rows it must
// repoint before deleting a series. Until 2026-08-24 the PebbleStore wrapper
// dispatched to memdb whenever memdb was warm, with no completeness check, so a
// memdb that had lost rows produced a SHORT answer, the merge repointed only
// what it was handed, and DeleteSeries ran anyway. series_bookref.go records
// what that shape already did on production: 6,893 phantom series IDs held by
// 13,322 live books.
//
// requireTablesComplete existed the whole time -- wired into author_bookref.go
// and series_bookref.go, both of which are reference COUNTERS that only report.
// The getter that authorizes the delete had no guard. These tests pin the
// asymmetry that fixes: the merge getter repairs itself from Pebble, the
// listing getter is left alone.
//
// The existing conformance suite cannot cover any of this. Both of its tests
// require.True(p.IsMemReady()) and run only against a COMPLETE memdb, so they
// pass unchanged either way -- which reads as "conformance still holds" while
// covering none of the new behaviour.

// degradeSeriesMemdb removes one book row from memdb and flags the loss,
// reproducing a warmup that dropped a row.
//
// It DELETES the row rather than only setting the flag. With the flag alone the
// memdb and Pebble answers are identical, so every assertion below would hold
// whether or not a fall-through happened and the tests could not tell them
// apart. Deleting makes the two answers DIFFER, so only a real fall-through can
// produce the complete set.
func degradeSeriesMemdb(t *testing.T, p *PebbleStore, bookID string) {
	t.Helper()

	func() {
		txn := p.mem().db.Txn(true)
		defer txn.Commit()
		raw, err := txn.First(memTableBooks, memIdxID, bookID)
		require.NoError(t, err)
		require.NotNil(t, raw, "fixture check: the row to drop must be resident in memdb first")
		require.NoError(t, txn.Delete(memTableBooks, raw))
	}()

	p.mem().recordLostRows(memTableBooks, 1)
}

// setupDegradedSeriesFixture builds the conformance fixture, warms memdb, then
// drops one LIVE PRIMARY book from memdb only. Primary and live is what makes
// the row visible to BOTH getters, so a single fixture can show one of them
// recovering it and the other not.
func setupDegradedSeriesFixture(t *testing.T) (*PebbleStore, seriesGetterConformanceFixture, string) {
	t.Helper()

	store, cleanup := setupPebbleTestDB(t)
	t.Cleanup(cleanup)

	fx := buildSeriesGetterConformanceFixture(t, store)

	p, ok := store.(*PebbleStore)
	require.True(t, ok, "expected *PebbleStore from setupPebbleTestDB")
	p.WaitForWarmup()
	require.True(t, p.IsMemReady(),
		"memdb must be published or the UseMemDB=true arm silently runs the Pebble path")

	dropped := fx.wantOrderedIDs[0]
	degradeSeriesMemdb(t, p, dropped)
	return p, fx, dropped
}

// seriesIDsOf projects a getter result for set/order assertions.
func seriesIDsOf(books []BookCore) []string {
	ids := make([]string, 0, len(books))
	for _, b := range books {
		ids = append(ids, b.ID)
	}
	return ids
}

// TestGetBooksBySeriesIDAllVersions_FallsThroughWithTheCompleteAnswer is the
// point of the design. Refusing would be merely SAFE; falling through to the
// authoritative Pebble scan is CORRECT, and the merge still completes.
//
// Asserting only errors.Is(ErrMemdbIncomplete) on the memdb method would not
// prove the caller ever gets a usable set, so this asserts the set.
func TestGetBooksBySeriesIDAllVersions_FallsThroughWithTheCompleteAnswer(t *testing.T) {
	p, fx, dropped := setupDegradedSeriesFixture(t)

	// Prove the memdb answer is now WRONG, so the assertion below is meaningful.
	_, memErr := p.mem().GetBooksBySeriesIDAllVersions(fx.seriesID, 0, 0)
	require.ErrorIs(t, memErr, ErrMemdbIncomplete,
		"precondition: the memdb merge getter must refuse once the loss is flagged")

	got, err := p.GetBooksBySeriesIDAllVersions(fx.seriesID)
	require.NoError(t, err, "a tainted memdb must not stall the merge -- it must fall through")

	// The expected set is spelled out from the fixture, NOT read back from
	// getBooksBySeriesIDFull. Comparing against that function would be
	// comparing the fall-through to its own callee: if the scan silently
	// dropped a row, the expectation would drop it identically and this would
	// pass while its failure message claimed to have verified authoritativeness.
	// That tautology shipped in v1.0.0 of this file and is exactly the "passes
	// for the wrong reason" shape these tests were written to call out
	// elsewhere.
	wantIDs := append(append([]string{}, fx.wantOrderedIDs...), fx.nonPrimaryBookID)
	require.ElementsMatch(t, wantIDs, seriesIDsOf(got),
		"the fall-through must return every live book in the series -- the complete "+
			"set the merge has to repoint, named independently of the scan under test")

	require.Contains(t, seriesIDsOf(got), dropped,
		"the book memdb lost is exactly the one the merge would fail to repoint before "+
			"deleting the series out from under it")
	require.Contains(t, seriesIDsOf(got), fx.nonPrimaryBookID,
		"the fall-through must still be the COMPLETE set -- a non-primary version left "+
			"behind is the other half of the same orphaning bug")
	require.NotContains(t, seriesIDsOf(got), fx.softDeletedBookID,
		"the fall-through must NOT start returning trashed rows; the unfiltered "+
			"SeriesRefCounts counter is still what covers those")
	require.NotContains(t, seriesIDsOf(got), fx.otherSeriesBookID,
		"control: a fall-through that ignored seriesID would pass every assertion above")
}

// TestGetBooksBySeriesIDCore_IsNotDegradedByALostRow pins the decision to guard
// the AllVersions WRAPPER rather than the shared getBooksBySeriesID body.
//
// Core is the listing view. Guarding the shared body would make every series
// page fall through to a full Pebble scan for the rest of the process's life --
// a lost row stays short until restart -- so on a 41k-book library that is a
// permanent hot-path regression on a degraded-but-serving process, not a blip.
//
// Same fixture, same lost row: this asserts Core still answers SHORT from
// memdb. That is a deliberately imperfect listing, and it is the trade being
// made. Without this test, moving the guard down to the shared body would look
// like a harmless tidy-up.
func TestGetBooksBySeriesIDCore_IsNotDegradedByALostRow(t *testing.T) {
	p, fx, dropped := setupDegradedSeriesFixture(t)

	core, err := p.GetBooksBySeriesIDCore(fx.seriesID)
	require.NoError(t, err, "the listing getter must not refuse -- it has no guard by design")
	require.NotContains(t, seriesIDsOf(core), dropped,
		"the listing getter must still be served from memdb; recovering the dropped row "+
			"here would mean the guard had leaked into the shared body and every series "+
			"page now costs a full Pebble scan")
	require.Len(t, core, len(fx.wantOrderedIDs)-1,
		"exactly one row short -- the one memdb lost")

	all, err := p.GetBooksBySeriesIDAllVersions(fx.seriesID)
	require.NoError(t, err)
	require.Contains(t, seriesIDsOf(all), dropped,
		"the two getters must disagree here; if they agree, either the merge getter is "+
			"not falling through or the listing getter is")
}

// TestGetBooksBySeriesIDAllVersions_HealthyMemdbIsStillServedFromMemdb is the
// positive control. Without it, a guard that refused unconditionally -- or a
// wrapper that always took the Pebble path -- would pass the test above while
// silently turning every merge into a full library scan.
//
// memdb and Pebble return the same SET here, so set equality cannot distinguish
// the paths. Flagging a loss in an UNRELATED table is what separates them: the
// books table is intact, so requireTablesComplete must not refuse.
func TestGetBooksBySeriesIDAllVersions_HealthyMemdbIsStillServedFromMemdb(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	fx := buildSeriesGetterConformanceFixture(t, store)

	p, ok := store.(*PebbleStore)
	require.True(t, ok, "expected *PebbleStore from setupPebbleTestDB")
	p.WaitForWarmup()
	require.True(t, p.IsMemReady())

	got, err := p.mem().GetBooksBySeriesIDAllVersions(fx.seriesID, 0, 0)
	require.NoError(t, err, "an intact memdb must answer; a guard that always refuses is not safety")
	require.ElementsMatch(t, append(append([]string{}, fx.wantOrderedIDs...), fx.nonPrimaryBookID),
		seriesIDsOf(got),
		"every live book in the series, non-primary version included -- named, not counted, "+
			"so a fixture change cannot quietly re-satisfy a length assertion")

	viaStore, err := p.GetBooksBySeriesIDAllVersions(fx.seriesID)
	require.NoError(t, err)
	require.Equal(t, seriesIDsOf(got), seriesIDsOf(viaStore))
}

// TestSeriesMembershipGuard_TaintFromAnUnrelatedTableStillBlocksTheMergeGetter
// covers the trigger being broader than "warmup lost a books row". A runtime
// memSync failure is attributed to memTableUnknown, which taints EVERY table --
// see recordLostRows. A guard that only checked memTableBooks by name would
// miss it, and requireTablesComplete checks memTableUnknown first for exactly
// this reason.
func TestSeriesMembershipGuard_TaintFromAnUnrelatedTableStillBlocksTheMergeGetter(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	fx := buildSeriesGetterConformanceFixture(t, store)

	p, ok := store.(*PebbleStore)
	require.True(t, ok)
	p.WaitForWarmup()
	require.True(t, p.IsMemReady())

	require.NoError(t, mustSeriesMergeGetterErr(p.mem(), fx.seriesID),
		"control: the getter answers before any loss is recorded, so a refusal below "+
			"cannot be a store that refuses unconditionally")

	p.mem().recordLostRows(memTableUnknown, 1)

	err := mustSeriesMergeGetterErr(p.mem(), fx.seriesID)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrMemdbIncomplete),
		"an unattributable loss taints every table, the books table included")

	got, sErr := p.GetBooksBySeriesIDAllVersions(fx.seriesID)
	require.NoError(t, sErr)
	require.ElementsMatch(t, append(append([]string{}, fx.wantOrderedIDs...), fx.nonPrimaryBookID),
		seriesIDsOf(got),
		"and the wrapper still returns the complete set from Pebble")
}

// mustSeriesMergeGetterErr returns the error (or nil) from the memdb merge
// getter, so a refusal assertion can be paired with a control on the same call.
func mustSeriesMergeGetterErr(m *MemStore, seriesID int) error {
	_, err := m.GetBooksBySeriesIDAllVersions(seriesID, 0, 0)
	return err
}

// --- the Pebble arm ------------------------------------------------------
//
// Everything above degrades memdb and asserts the fall-through repairs it.
// That covers the wrapper. It says nothing about whether the thing it falls
// through TO is complete, and the four tests above cannot: degradeSeriesMemdb
// leaves the Pebble row perfectly readable, so the scan always succeeds.
//
// The two tests below attack the fall-through target itself. Both were written
// against the unfixed scan and both failed there, which is the only reason to
// believe they reach the path.

// TestGetBooksBySeriesIDAllVersions_RefusesRatherThanSkipAnUndecodableRow
// closes the hole that made the guard partly decorative.
//
// requireTablesComplete fires on three triggers, and one of them is memdb
// warmup FAILING TO DECODE a book row (memdb_warmup.go). On that trigger the
// old fall-through re-read the same corrupt value from Pebble, hit
// json.Unmarshal, and `continue`d -- returning a SHORT list with a nil error.
// So the merge repointed only what it was handed and DeleteSeries ran anyway:
// the precise failure the guard was built to prevent, reached THROUGH the
// repair path, while the log line claimed it had fallen through to safety.
//
// series_bookref.go already made this call for the ref counter and wrote down
// why: undercounting is fail-OPEN for every caller, because the delete proceeds
// and strands the very row that could not be read. The membership getter
// authorizes the same delete and gets the same answer.
func TestGetBooksBySeriesIDAllVersions_RefusesRatherThanSkipAnUndecodableRow(t *testing.T) {
	p, fx, _ := setupDegradedSeriesFixture(t)

	// Corrupt the Pebble row for a book that IS in the series, so a scan that
	// skips it returns a set that is short by exactly one live member.
	corrupted := fx.wantOrderedIDs[1]
	require.NoError(t, p.db.Set([]byte("book:"+corrupted), []byte("{not json"), nil))

	_, err := p.GetBooksBySeriesIDAllVersions(fx.seriesID)
	require.Error(t, err,
		"a row the scan cannot decode may hold this series ID; skipping it hands the "+
			"merge a short list and the series is deleted out from under that book")
	require.Contains(t, err.Error(), corrupted,
		"the error must name the row, or an operator cannot act on it")
}

// TestGetBooksBySeriesIDAllVersions_SkipsSecondaryIndexKeys is the other half
// of making the unmarshal fatal, and without it that change is a live bug.
//
// Widening the bounds to the true "book:" prefix range (see the test below)
// pulls the secondary indexes INTO the scan for the first time: book:hash:,
// book:versiongroup:, book:asin:, book:isbn13: and friends, whose values are
// bare book IDs, not book JSON. Under the old ["book:0","book:;") bounds they
// were out of range entirely, which is why the pre-existing ":path:"-only skip
// looked adequate -- it was dead code guarding a range nothing reached.
//
// So the structural one-colon filter is not tidying. It is what stops every
// indexed book from turning the merge getter into a hard error. Measured: with
// the filter reverted to the ":path:" test, this is the only test that fails.
// The conformance fixture creates no hashed books, so nothing else in this
// package writes a non-path secondary key.
func TestGetBooksBySeriesIDAllVersions_SkipsSecondaryIndexKeys(t *testing.T) {
	p, fx, _ := setupDegradedSeriesFixture(t)

	// Every non-path index family, with the bare-ID values they really carry.
	member := fx.wantOrderedIDs[0]
	for _, k := range []string{
		"book:hash:deadbeef",
		"book:originalhash:cafebabe",
		"book:versiongroup:vg-1",
		"book:asin:B00TEST123",
		"book:isbn13:9780000000000",
	} {
		require.NoError(t, p.db.Set([]byte(k), []byte(member), nil))
	}

	got, err := p.GetBooksBySeriesIDAllVersions(fx.seriesID)
	require.NoError(t, err,
		"a secondary-index key is not a corrupt book row; treating one as undecodable "+
			"would fail every merge for any book that has ever been hashed or grouped")
	require.ElementsMatch(t,
		append(append([]string{}, fx.wantOrderedIDs...), fx.nonPrimaryBookID),
		seriesIDsOf(got),
		"and the index rows must not be admitted as books either")
}

// TestGetBooksBySeriesIDAllVersions_SeesALetterLeadingBookID covers the same
// short-answer bug arriving through the iterator BOUNDS rather than the decode.
//
// The scan ran over ["book:0", "book:;") -- a byte range that admits only
// '0'-'9' and ':' as the first character after the prefix. Book IDs are ULIDs
// and today's ULIDs start with a digit, so this was latent. But CreateBook
// mints a ULID only when book.ID == "", so a caller-supplied letter-leading ID
// is constructible, sorts above the upper bound, and is invisible to the scan.
//
// Invisible to a LISTING is a missing row. Invisible to the MERGE getter is a
// stranded book. Identical reasoning to the versionGroupBackfillKey v2 -> v3
// bump (pebble_store_versiongroup_backfill.go), which found the same bounds bug
// in the same key family one day earlier and fixed it the same way.
func TestGetBooksBySeriesIDAllVersions_SeesALetterLeadingBookID(t *testing.T) {
	p, fx, _ := setupDegradedSeriesFixture(t)

	yes := true
	seq := 9
	// CreateBook honours a caller-supplied ID; this is the constructible case.
	letterLed, err := p.CreateBook(&Book{
		ID:               "ZZTOP000000000000000000000",
		Title:            "Letter Leading ID",
		FilePath:         "/lib/series/Letter Leading ID",
		SeriesID:         &fx.seriesID,
		SeriesSequence:   &seq,
		IsPrimaryVersion: &yes,
	})
	require.NoError(t, err)
	require.Equal(t, "ZZTOP000000000000000000000", letterLed.ID,
		"fixture check: CreateBook must not have re-minted the ID, or this tests nothing")

	got, err := p.GetBooksBySeriesIDAllVersions(fx.seriesID)
	require.NoError(t, err)
	require.Contains(t, seriesIDsOf(got), letterLed.ID,
		"a book the merge getter cannot see is a book the merge will not repoint "+
			"before deleting the series")
}
