// file: internal/database/orphan_membership_guard_test.go
// version: 1.0.0
// guid: bfdcfabd-2948-49d9-aecb-b3f33b74142e
// last-edited: 2026-09-10

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The orphan book_file sweep (internal/plugins/maintenance/orphan_book_files.go)
// builds ONE membership set out of two getters -- the full book list and the
// soft-deleted book list -- and then HARD-DELETES every book_file row whose
// BookID is absent from it. Until 2026-09-10 both getters answered from memdb
// whenever memdb was warm, with no completeness check, so a single book row
// dropped by a lossy warmup removed that book from the set and every file row
// it owned was deleted. The book survived as a fileless shell and nothing
// reported it.
//
// These tests pin the same asymmetry the series merge getter established:
// the getter whose answer authorizes a delete refuses a short memdb, and the
// PebbleStore wrapper repairs that refusal from the authoritative Pebble scan
// so the sweep still completes -- correctly, and only slower.

// orphanMembershipFixture is one live book plus one soft-deleted book. Both are
// needed because they are read by DIFFERENT getters: the live one by
// GetAllBooksCoreComplete, the trashed one by ListSoftDeletedBooks, and the
// orphan sweep unions the two because a soft-deleted book still owns its files.
//
// Helper name is task-unique on purpose: several suites in this package define
// fixture builders and a generic name collides on rebase.
type orphanMembershipFixture struct {
	liveBookID    string
	trashedBookID string
}

func buildOrphanMembershipFixture(t *testing.T, store Store) orphanMembershipFixture {
	t.Helper()

	live, err := store.CreateBook(&Book{
		Title:    "Orphan Guard Live",
		FilePath: "/lib/orphan-guard/live",
	})
	require.NoError(t, err)

	trashed, err := store.CreateBook(&Book{
		Title:    "Orphan Guard Trashed",
		FilePath: "/lib/orphan-guard/trashed",
	})
	require.NoError(t, err)

	yes := true
	trashed.MarkedForDeletion = &yes
	_, err = store.UpdateBook(trashed.ID, trashed)
	require.NoError(t, err)

	return orphanMembershipFixture{liveBookID: live.ID, trashedBookID: trashed.ID}
}

// degradeOrphanMemdb removes bookID from memdb and flags the loss, reproducing
// a warmup that dropped a row.
//
// It DELETES the row rather than only setting the flag. With the flag alone the
// memdb and Pebble answers are identical, so every assertion below would hold
// whether or not a fall-through happened and the tests could not tell them
// apart. Deleting makes the two answers DIFFER, so only a real fall-through can
// produce the complete set.
func degradeOrphanMemdb(t *testing.T, p *PebbleStore, bookID string) {
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

func setupDegradedOrphanFixture(t *testing.T) (*PebbleStore, orphanMembershipFixture) {
	t.Helper()

	store, cleanup := setupPebbleTestDB(t)
	t.Cleanup(cleanup)

	fx := buildOrphanMembershipFixture(t, store)

	p, ok := store.(*PebbleStore)
	require.True(t, ok, "expected *PebbleStore from setupPebbleTestDB")
	p.WaitForWarmup()
	require.True(t, p.IsMemReady(),
		"memdb must be published or the UseMemDB=true arm silently runs the Pebble path")

	return p, fx
}

// bookCoreIDsOf projects a getter result for set assertions.
func bookCoreIDsOf(books []BookCore) []string {
	ids := make([]string, 0, len(books))
	for _, b := range books {
		ids = append(ids, b.ID)
	}
	return ids
}

func bookIDsOf(books []Book) []string {
	ids := make([]string, 0, len(books))
	for _, b := range books {
		ids = append(ids, b.ID)
	}
	return ids
}

// TestGetAllBooksCoreComplete_FallsThroughWithTheCompleteAnswer is the live
// half. The book memdb lost is exactly the book whose file rows the orphan
// sweep would otherwise delete, so the assertion is on the SET, not merely on
// the absence of an error.
func TestGetAllBooksCoreComplete_FallsThroughWithTheCompleteAnswer(t *testing.T) {
	p, fx := setupDegradedOrphanFixture(t)

	// Control first: the getter answers before any loss is recorded, so the
	// refusal below cannot be a store that refuses unconditionally.
	_, ctlErr := p.mem().GetAllBooksCoreComplete(0, 0)
	require.NoError(t, ctlErr, "control: an intact memdb must still be answered from memdb")

	degradeOrphanMemdb(t, p, fx.liveBookID)

	// Prove the memdb answer is now WRONG, so the assertion below is meaningful.
	_, memErr := p.mem().GetAllBooksCoreComplete(0, 0)
	require.ErrorIs(t, memErr, ErrMemdbIncomplete,
		"precondition: the delete-authorizing getter must refuse once the loss is flagged")

	// The plain listing getter is deliberately NOT guarded -- it is on ~100 call
	// sites and a recorded loss does not clear without a restart. Pinning that
	// here keeps a later "make it consistent" change from silently converting
	// every library listing into a full Pebble scan.
	short, shortErr := p.mem().GetAllBooksCore(0, 0, nil)
	require.NoError(t, shortErr, "the listing getter must keep serving from the degraded memdb")
	require.NotContains(t, bookCoreIDsOf(short), fx.liveBookID,
		"fixture check: the listing getter is the short answer this guard exists to not consume")

	got, err := p.GetAllBooksCoreComplete(0, 0)
	require.NoError(t, err, "a tainted memdb must not stall the orphan sweep -- it must fall through")

	// The expected set is spelled out from the fixture, NOT read back from
	// getAllBooksCoreFromPebble. Comparing against that function would be
	// comparing the fall-through to its own callee: if the scan silently dropped
	// a row, the expectation would drop it identically and this would pass while
	// claiming to have verified authoritativeness.
	require.Contains(t, bookCoreIDsOf(got), fx.liveBookID,
		"the book memdb lost is exactly the one whose book_file rows would have been "+
			"hard-deleted as orphans")
	require.NotContains(t, bookCoreIDsOf(got), fx.trashedBookID,
		"the fall-through must keep excluding soft-deleted books -- they arrive via "+
			"ListSoftDeletedBooks, and double-counting them would hide a regression there")
}

// TestListSoftDeletedBooks_FallsThroughWithTheCompleteTrashSet is the trashed
// half, and it is the more dangerous one: a soft-deleted book is restorable and
// still OWNS its book_files, so a row missing from THIS answer removes
// protection rather than adding it. The orphan sweep would delete the files of
// a book sitting in the trash, and the restore would bring back an empty shell.
func TestListSoftDeletedBooks_FallsThroughWithTheCompleteTrashSet(t *testing.T) {
	p, fx := setupDegradedOrphanFixture(t)

	_, ctlErr := p.mem().ListSoftDeletedBooks(0, 0, nil)
	require.NoError(t, ctlErr, "control: an intact memdb must still be answered from memdb")

	degradeOrphanMemdb(t, p, fx.trashedBookID)

	_, memErr := p.mem().ListSoftDeletedBooks(0, 0, nil)
	require.ErrorIs(t, memErr, ErrMemdbIncomplete,
		"precondition: the trash getter must refuse once the loss is flagged")

	got, err := p.ListSoftDeletedBooks(0, 0, nil)
	require.NoError(t, err,
		"refusing outright would stall the trash UI and the purge selection over a "+
			"condition Pebble can answer correctly")
	require.Contains(t, bookIDsOf(got), fx.trashedBookID,
		"the fall-through must return the restorable book whose files the sweep would "+
			"otherwise treat as garbage")
}

// TestOrphanMembershipGuard_TaintFromAnUnrelatedTableStillBlocksBothGetters
// covers the trigger being broader than "warmup lost a books row". A runtime
// memSync failure is attributed to memTableUnknown, which taints EVERY table --
// see recordLostRows. A guard that only checked memTableBooks by name would
// miss it, and requireTablesComplete checks memTableUnknown first for exactly
// this reason.
func TestOrphanMembershipGuard_TaintFromAnUnrelatedTableStillBlocksBothGetters(t *testing.T) {
	p, fx := setupDegradedOrphanFixture(t)

	_, ctlBooks := p.mem().GetAllBooksCoreComplete(0, 0)
	require.NoError(t, ctlBooks, "control: both getters answer before any loss is recorded")
	_, ctlTrash := p.mem().ListSoftDeletedBooks(0, 0, nil)
	require.NoError(t, ctlTrash, "control: both getters answer before any loss is recorded")

	p.mem().recordLostRows(memTableUnknown, 1)

	_, booksErr := p.mem().GetAllBooksCoreComplete(0, 0)
	require.ErrorIs(t, booksErr, ErrMemdbIncomplete,
		"an unattributable loss taints every table, the books table included")
	_, trashErr := p.mem().ListSoftDeletedBooks(0, 0, nil)
	require.ErrorIs(t, trashErr, ErrMemdbIncomplete,
		"an unattributable loss taints every table, the books table included")

	// And both wrappers still hand the sweep a usable, complete set.
	books, err := p.GetAllBooksCoreComplete(0, 0)
	require.NoError(t, err)
	require.Contains(t, bookCoreIDsOf(books), fx.liveBookID)

	trashed, err := p.ListSoftDeletedBooks(0, 0, nil)
	require.NoError(t, err)
	require.Contains(t, bookIDsOf(trashed), fx.trashedBookID)
}
