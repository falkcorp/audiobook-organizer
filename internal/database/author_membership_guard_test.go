// file: internal/database/author_membership_guard_test.go
// version: 1.0.0
// guid: 76c6e630-e4b4-4ae7-8b7e-63d560a5af55
// last-edited: 2026-09-10

package database

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// The author MERGE path asks GetBooksByAuthorIDWithRoleCore which rows it must
// repoint before deleting an author. Until 2026-09-10 the PebbleStore wrapper
// dispatched to memdb whenever memdb was warm, with no completeness check, so a
// memdb that had lost rows produced a SHORT answer, the merge repointed only
// what it was handed, and DeleteAuthor ran anyway. TODO.md's
// AUTHOR-MEMBERSHIP-UNGUARDED entry records the shape FIRING on production on
// 2026-08-24 05:00: the same loss that series_membership_guard_test.go closed
// for series, arriving through the author twin one file over.
//
// The series guard shipped on 2026-08-24 and its own doc comment said it was
// "the only membership getter with the guard". This file closes the author
// half with the same asymmetry: the merge getter repairs itself from Pebble,
// the listing getter is left alone.
//
// The author getter differs from the series one in a way that matters here: it
// reads TWO memdb tables. Co-authors exist ONLY as book_authors junction rows,
// so a lost junction row is a credit that pass 2 (legacy Book.AuthorID) cannot
// recover. The guard therefore has to name both tables -- author_bookref.go
// made the same call for the counter -- and there is a test for each.

// degradeAuthorMemdbBook removes one BOOK row from memdb and flags the loss,
// reproducing a warmup that dropped a row.
//
// It DELETES the row rather than only setting the flag, for the reason
// degradeSeriesMemdb gives: with the flag alone the memdb and Pebble answers
// are identical and no assertion below could tell a fall-through from a
// non-fall-through.
func degradeAuthorMemdbBook(t *testing.T, p *PebbleStore, bookID string) {
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

// degradeAuthorMemdbJunction removes every book_authors row for one book from
// memdb and flags the loss against the JUNCTION table, not the books table.
// This is the author-specific trigger: a book whose credit list did not decode
// loses all of its co-authors at once while the book itself stays resident.
func degradeAuthorMemdbJunction(t *testing.T, p *PebbleStore, bookID string) {
	t.Helper()

	func() {
		txn := p.mem().db.Txn(true)
		defer txn.Commit()
		iter, err := txn.Get(memTableBookAuthors, memIdxBookID, bookID)
		require.NoError(t, err)
		var rows []interface{}
		for obj := iter.Next(); obj != nil; obj = iter.Next() {
			rows = append(rows, obj)
		}
		require.NotEmpty(t, rows, "fixture check: the junction rows to drop must be resident in memdb first")
		for _, raw := range rows {
			require.NoError(t, txn.Delete(memTableBookAuthors, raw))
		}
	}()

	p.mem().recordLostRows(memTableBookAuthors, 1)
}

// setupAuthorGuardFixture builds the conformance fixture and warms memdb. It
// does NOT degrade anything: each test picks its own trigger, because the two
// tables fail differently and one fixture cannot exercise both.
func setupAuthorGuardFixture(t *testing.T) (*PebbleStore, authorGetterConformanceFixture) {
	t.Helper()

	store, cleanup := setupPebbleTestDB(t)
	t.Cleanup(cleanup)

	fx := buildAuthorGetterConformanceFixture(t, store)

	p, ok := store.(*PebbleStore)
	require.True(t, ok, "expected *PebbleStore from setupPebbleTestDB")
	p.WaitForWarmup()
	require.True(t, p.IsMemReady(),
		"memdb must be published or the UseMemDB=true arm silently runs the Pebble path")
	return p, fx
}

// authorMergeWant is the complete live set the merge getter must return,
// spelled out from the fixture rather than read back from the scan under test
// (see the tautology note in series_membership_guard_test.go).
func authorMergeWant(fx authorGetterConformanceFixture) []string {
	return []string{fx.legacyBookID, fx.coAuthorBookID, fx.nonPrimaryBookID}
}

// authorIDsOf projects a getter result for set assertions.
func authorIDsOf(books []BookCore) []string {
	ids := make([]string, 0, len(books))
	for _, b := range books {
		ids = append(ids, b.ID)
	}
	return ids
}

// TestGetBooksByAuthorIDWithRoleCore_FallsThroughWithTheCompleteAnswer is the
// point of the design. Refusing would be merely SAFE; falling through to the
// authoritative Pebble scan is CORRECT, and the merge still completes.
//
// Pre-fix this failed at the precondition: the memdb getter answered with a nil
// error and a set short by the legacy book.
func TestGetBooksByAuthorIDWithRoleCore_FallsThroughWithTheCompleteAnswer(t *testing.T) {
	p, fx := setupAuthorGuardFixture(t)
	dropped := fx.legacyBookID
	degradeAuthorMemdbBook(t, p, dropped)

	// Prove the memdb answer is now WRONG, so the assertion below is meaningful.
	_, memErr := p.mem().GetBooksByAuthorIDAllVersions(fx.authorID, 0, 0)
	require.ErrorIs(t, memErr, ErrMemdbIncomplete,
		"precondition: the memdb merge getter must refuse once the loss is flagged")

	got, err := p.GetBooksByAuthorIDWithRoleCore(fx.authorID)
	require.NoError(t, err, "a tainted memdb must not stall the merge -- it must fall through")

	require.ElementsMatch(t, authorMergeWant(fx), authorIDsOf(got),
		"the fall-through must return every live book crediting the author -- the "+
			"complete set the merge has to repoint, named independently of the scan under test")
	require.Contains(t, authorIDsOf(got), dropped,
		"the book memdb lost is exactly the one the merge would fail to repoint before "+
			"deleting the author out from under it")
	require.NotContains(t, authorIDsOf(got), fx.softDeletedBookID,
		"the fall-through must NOT start returning trashed rows; the unfiltered "+
			"AuthorRefCounts counter is still what covers those")
	require.NotContains(t, authorIDsOf(got), fx.unrelatedBookID,
		"control: a fall-through that ignored authorID would pass every assertion above")
}

// TestAuthorMembershipGuard_LostJunctionRowBlocksTheMergeGetter is the trigger
// the series test cannot have. A co-author credit lives ONLY in book_authors;
// if memdb lost that row the books table is intact, so a guard that named
// memTableBooks alone would answer, and answer short by exactly the book whose
// junction row is the one the merge exists to rewrite.
func TestAuthorMembershipGuard_LostJunctionRowBlocksTheMergeGetter(t *testing.T) {
	p, fx := setupAuthorGuardFixture(t)
	degradeAuthorMemdbJunction(t, p, fx.coAuthorBookID)

	_, memErr := p.mem().GetBooksByAuthorIDAllVersions(fx.authorID, 0, 0)
	require.ErrorIs(t, memErr, ErrMemdbIncomplete,
		"a lost book_authors row must refuse the merge getter even though the books table is intact")

	got, err := p.GetBooksByAuthorIDWithRoleCore(fx.authorID)
	require.NoError(t, err)
	require.ElementsMatch(t, authorMergeWant(fx), authorIDsOf(got))
	require.Contains(t, authorIDsOf(got), fx.coAuthorBookID,
		"the co-author credit memdb lost is recovered from the Pebble junction")
}

// TestGetBooksByAuthorIDCore_IsNotDegradedByALostRow pins the decision to guard
// the AllVersions WRAPPER rather than the shared getBooksByAuthorID body, for
// the reason TestGetBooksBySeriesIDCore_IsNotDegradedByALostRow gives: the
// listing view must not turn into a permanent full Pebble scan on a
// degraded-but-serving process.
func TestGetBooksByAuthorIDCore_IsNotDegradedByALostRow(t *testing.T) {
	p, fx := setupAuthorGuardFixture(t)
	dropped := fx.legacyBookID
	degradeAuthorMemdbBook(t, p, dropped)

	core, err := p.GetBooksByAuthorIDCore(fx.authorID)
	require.NoError(t, err, "the listing getter must not refuse -- it has no guard by design")
	require.NotContains(t, authorIDsOf(core), dropped,
		"the listing getter must still be served from memdb; recovering the dropped row "+
			"here would mean the guard had leaked into the shared body")
	require.ElementsMatch(t, []string{fx.coAuthorBookID}, authorIDsOf(core),
		"exactly the primary co-author book: the legacy one is lost and the non-primary "+
			"one is filtered by the listing view")

	all, err := p.GetBooksByAuthorIDWithRoleCore(fx.authorID)
	require.NoError(t, err)
	require.Contains(t, authorIDsOf(all), dropped,
		"the two getters must disagree here; if they agree, either the merge getter is "+
			"not falling through or the listing getter is")
}

// TestGetBooksByAuthorIDWithRoleCore_HealthyMemdbIsStillServedFromMemdb is the
// positive control: a guard that refused unconditionally, or a wrapper that
// always took the Pebble path, would pass the tests above while turning every
// author merge into a full library scan.
func TestGetBooksByAuthorIDWithRoleCore_HealthyMemdbIsStillServedFromMemdb(t *testing.T) {
	p, fx := setupAuthorGuardFixture(t)

	got, err := p.mem().GetBooksByAuthorIDAllVersions(fx.authorID, 0, 0)
	require.NoError(t, err, "an intact memdb must answer; a guard that always refuses is not safety")
	ids := make([]string, 0, len(got))
	for _, b := range got {
		ids = append(ids, b.ID)
	}
	require.ElementsMatch(t, authorMergeWant(fx), ids)

	viaStore, err := p.GetBooksByAuthorIDWithRoleCore(fx.authorID)
	require.NoError(t, err)
	require.ElementsMatch(t, ids, authorIDsOf(viaStore))
}

// TestAuthorMembershipGuard_TaintFromAnUnrelatedTableStillBlocksTheMergeGetter
// covers a runtime memSync failure attributed to memTableUnknown, which taints
// EVERY table -- see recordLostRows.
func TestAuthorMembershipGuard_TaintFromAnUnrelatedTableStillBlocksTheMergeGetter(t *testing.T) {
	p, fx := setupAuthorGuardFixture(t)

	_, ctrlErr := p.mem().GetBooksByAuthorIDAllVersions(fx.authorID, 0, 0)
	require.NoError(t, ctrlErr,
		"control: the getter answers before any loss is recorded, so a refusal below "+
			"cannot be a store that refuses unconditionally")

	p.mem().recordLostRows(memTableUnknown, 1)

	_, err := p.mem().GetBooksByAuthorIDAllVersions(fx.authorID, 0, 0)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrMemdbIncomplete),
		"an unattributable loss taints every table, books and book_authors included")

	got, sErr := p.GetBooksByAuthorIDWithRoleCore(fx.authorID)
	require.NoError(t, sErr)
	require.ElementsMatch(t, authorMergeWant(fx), authorIDsOf(got),
		"and the wrapper still returns the complete set from Pebble")
}

// --- the Pebble arm ------------------------------------------------------
//
// Everything above degrades memdb and asserts the fall-through repairs it.
// The two tests below attack the fall-through target itself: one trigger for
// requireTablesComplete is warmup FAILING TO DECODE a row, and on that trigger
// a scan that `continue`s past the same corrupt value re-creates the short
// answer THROUGH the repair path. Both were written against the unfixed scan
// and both failed there.

// TestGetBooksByAuthorIDWithRoleCore_RefusesRatherThanSkipAnUndecodableBookRow
// mirrors the series test of the same shape.
func TestGetBooksByAuthorIDWithRoleCore_RefusesRatherThanSkipAnUndecodableBookRow(t *testing.T) {
	p, fx := setupAuthorGuardFixture(t)
	degradeAuthorMemdbBook(t, p, fx.legacyBookID)

	corrupted := fx.coAuthorBookID
	require.NoError(t, p.db.Set([]byte("book:"+corrupted), []byte("{not json"), nil))

	_, err := p.GetBooksByAuthorIDWithRoleCore(fx.authorID)
	require.Error(t, err,
		"a row the scan cannot decode may credit this author; skipping it hands the "+
			"merge a short list and the author is deleted out from under that book")
	require.Contains(t, err.Error(), corrupted,
		"the error must name the row, or an operator cannot act on it")
}

// TestGetBooksByAuthorIDWithRoleCore_RefusesRatherThanSkipAnUndecodableJunctionRow
// is the author-only half. The junction scan is the ONLY way a co-author is
// found on the Pebble path, so a credit list that will not decode is a set of
// links the merge cannot see and will not rewrite.
func TestGetBooksByAuthorIDWithRoleCore_RefusesRatherThanSkipAnUndecodableJunctionRow(t *testing.T) {
	p, fx := setupAuthorGuardFixture(t)
	degradeAuthorMemdbBook(t, p, fx.legacyBookID)

	corrupted := fx.coAuthorBookID
	require.NoError(t, p.db.Set([]byte("book_authors:"+corrupted), []byte("[{not json"), nil))

	_, err := p.GetBooksByAuthorIDWithRoleCore(fx.authorID)
	require.Error(t, err,
		"a credit list the scan cannot decode may name this author; skipping it hands the "+
			"merge a short list")
	require.Contains(t, err.Error(), corrupted,
		"the error must name the book whose credit list is unreadable")
}
