// file: internal/merge/sync_identity_survival_test.go
// version: 1.0.0
// guid: 44a928e0-6f46-412d-826b-198c12f52dc7
// last-edited: 2026-07-30

// Package merge: cross-mechanism ID-survival acceptance suite.
//
// This is the acceptance bar for docs/specs/2026-07-29-abs-sync-api-design.md
// §4.3: the client-visible libraryItemId (syncID) and the associated user
// progress must survive every operation this app's core loop performs on a
// Book/BookFile row -- rename, move (tagged and untagged), retag, the two
// merge-family paths (merge.Service.MergeBooks/CombineBooks and the separate
// dedup.MergeBooks hard-delete path), and file replacement -- plus the
// redirect-chain resolution a chain of merges leaves behind.
//
// Several of those operations already have thorough, mechanism-specific
// coverage elsewhere in the tree (see the doc comment above each scenario
// below for exactly which file and why this suite does not repeat it). This
// file's job is the scenarios that were NOT already proven end-to-end: a
// straightforward same-ID survival check for rename/move-tagged/retag, both
// halves of file-level replacement, and a pathological-cycle regression for
// ResolveSyncItem that no existing test exercises.
package merge

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
)

// seedBookWithProgress creates a Book plus one UserBookState and one
// UserPosition for userID, and returns the new book's ID. Shared setup for
// every same-ID survival scenario below (rename, move-tagged, retag): each
// one only changes a single field via UpdateBook and re-checks the syncID and
// this seeded progress are untouched.
func seedBookWithProgress(t *testing.T, store database.Store, userID, title, format string, progressPct int) (bookID string) {
	t.Helper()
	bookID = ulid.Make().String()
	_, err := store.CreateBook(&database.Book{
		ID:       bookID,
		Title:    title,
		Format:   format,
		FilePath: "/tmp/survival/" + bookID + "." + format,
	})
	require.NoError(t, err)
	require.NoError(t, store.SetUserBookState(&database.UserBookState{
		UserID:         userID,
		BookID:         bookID,
		Status:         database.UserBookStatusInProgress,
		ProgressPct:    progressPct,
		LastActivityAt: time.Now(),
	}))
	require.NoError(t, store.SetUserPosition(userID, bookID, "seg-1", 111))
	return bookID
}

// TestSyncIdentitySurvives_Rename: a rename is a Book.Title change via
// UpdateBook(id, ...) with the SAME id. Since sync_item:book:<bookID> is
// keyed on that unchanged id, there is no repoint logic to exercise here --
// this proves nothing broke, not new behavior.
func TestSyncIdentitySurvives_Rename(t *testing.T) {
	store := setupTestStore(t)
	ids := database.AsSyncIdentityStore(store)
	require.NotNil(t, ids, "PebbleStore must implement SyncIdentityStore")
	user := seedSyncUser(t, store)

	bookID := seedBookWithProgress(t, store, user.ID, "Original Title", "mp3", 33)
	syncID, err := ids.MintOrGetSyncID(bookID)
	require.NoError(t, err)

	book, err := store.GetBookByID(bookID)
	require.NoError(t, err)
	require.NotNil(t, book)
	book.Title = "Renamed Title"
	_, err = store.UpdateBook(bookID, book)
	require.NoError(t, err)

	got, has, err := ids.GetSyncIDForBook(bookID)
	require.NoError(t, err)
	require.True(t, has)
	require.Equal(t, syncID, got, "rename must not change the syncID: Book.ID is unchanged")

	state, err := store.GetUserBookState(user.ID, bookID)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Equal(t, 33, state.ProgressPct, "rename must not disturb progress")

	pos, err := store.GetUserPosition(user.ID, bookID)
	require.NoError(t, err)
	require.NotNil(t, pos)
	require.InDelta(t, 111.0, pos.PositionSeconds, 0.001)
}

// TestSyncIdentitySurvives_MoveTagged: an in-place move of a TAGGED file
// (embedded AUDIOBOOK_ORGANIZER_ID) is, at the store level, identical to a
// rename -- FilePath changes via UpdateBook(id, ...) with the SAME id,
// because the embedded tag lets the scanner re-link it to its existing Book
// row instead of minting a new one. Kept as its own named test (rather than
// folded into the rename test) because it documents a distinct real-world
// scenario a reader of this suite should see called out explicitly, even
// though the assertions are the mechanical twin of the rename case.
func TestSyncIdentitySurvives_MoveTagged(t *testing.T) {
	store := setupTestStore(t)
	ids := database.AsSyncIdentityStore(store)
	require.NotNil(t, ids)
	user := seedSyncUser(t, store)

	bookID := seedBookWithProgress(t, store, user.ID, "Moved Book", "m4b", 47)
	syncID, err := ids.MintOrGetSyncID(bookID)
	require.NoError(t, err)

	book, err := store.GetBookByID(bookID)
	require.NoError(t, err)
	require.NotNil(t, book)
	book.FilePath = "/tmp/survival/moved-to/" + bookID + ".m4b"
	_, err = store.UpdateBook(bookID, book)
	require.NoError(t, err)

	got, has, err := ids.GetSyncIDForBook(bookID)
	require.NoError(t, err)
	require.True(t, has)
	require.Equal(t, syncID, got, "a tagged move must not change the syncID: Book.ID is unchanged")

	state, err := store.GetUserBookState(user.ID, bookID)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Equal(t, 47, state.ProgressPct)
}

// TestSyncIdentitySurvives_MoveUntagged: the untagged-move case (no embedded
// tag, so the scanner re-matches by hash and mints a NEW Book ULID via
// version-linking) is NOT re-tested here -- it already has real end-to-end
// coverage that exercises the actual production call path, not just the
// underlying primitive:
//
//   - internal/scanner/sync_identity_move_test.go's
//     TestSaveBookToDatabase_UntaggedMove_CarriesSyncIdentity drives the real
//     saveBookToDatabase entry point, which (via
//     followSyncIdentityOnVersionLink in internal/scanner/scanner.go) calls
//     merge.FollowBookIDChange -> RepointSyncItem, and asserts both the
//     syncID and the seeded progress land on the new ULID.
//
// At the time this suite was written the wiring already existed in
// production (internal/scanner/scanner.go calls merge.FollowBookIDChange at
// its version-link call site), so there is no "primitive only" gap left to
// flag for this scenario -- unlike the file-replace-primitive case below,
// where no such caller exists yet.

// TestSyncIdentitySurvives_Retag: a retag changes a tag-derived field
// (Narrator here) via UpdateBook(id, ...) with the SAME id -- the third
// member of the rename/move-tagged/retag trio that is a "prove nothing
// broke" check rather than a test of new repoint logic.
func TestSyncIdentitySurvives_Retag(t *testing.T) {
	store := setupTestStore(t)
	ids := database.AsSyncIdentityStore(store)
	require.NotNil(t, ids)
	user := seedSyncUser(t, store)

	bookID := seedBookWithProgress(t, store, user.ID, "Retagged Book", "mp3", 61)
	syncID, err := ids.MintOrGetSyncID(bookID)
	require.NoError(t, err)

	book, err := store.GetBookByID(bookID)
	require.NoError(t, err)
	require.NotNil(t, book)
	narrator := "New Narrator"
	book.Narrator = &narrator
	_, err = store.UpdateBook(bookID, book)
	require.NoError(t, err)

	got, has, err := ids.GetSyncIDForBook(bookID)
	require.NoError(t, err)
	require.True(t, has)
	require.Equal(t, syncID, got, "a retag must not change the syncID: Book.ID is unchanged")

	state, err := store.GetUserBookState(user.ID, bookID)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Equal(t, 61, state.ProgressPct)
}

// The two merge-family paths that soft/hard-delete a loser book already have
// thorough, dedicated coverage this suite deliberately does not repeat:
//
//   - merge.Service.MergeBooks (soft-delete) and merge.Service.CombineBooks
//     (hard-delete): internal/merge/sync_follow_test.go covers redirect,
//     minting a syncID for a winner that never had one, both directions of
//     the "furthest position wins" progress rule, idempotent re-merge, a
//     THREE-BOOK chained redirect (B->A then A->C, i.e. the happy-path
//     redirect-chain case this task also asks for), and CombineBooks'
//     hard-delete shell case. internal/merge/sync_follow_concurrent_test.go
//     adds a 16-goroutine exactly-once regression on top of that.
//   - dedup.MergeBooks (the SEPARATE hard-delete path used by
//     internal/reconcile/itunes_heal.go, the highest-stakes case because
//     there is no surviving row to repoint after the fact):
//     internal/dedup/book_dedup_sync_follow_test.go's
//     TestMergeBooks_SyncIdentityFollowsHardDelete asserts the redirect, the
//     progress carry-forward, AND that the loser row is actually gone
//     (proving this really is the hard-delete path, not a stand-in), plus an
//     idempotent-retry test.
//
// Re-driving any of those through this file would assert the exact same
// production code paths a second time with no new failure mode covered.

// TestSyncIdentitySurvives_FileReplace_SameID is the file-level analogue of
// the rename/move-tagged/retag trio above: today's ONLY production file
// replacement mechanism is UpdateBookFile(id, file) with the SAME id (a
// remux or quality-upgrade re-tags the row in place) -- UpdateBookFile never
// reassigns file.ID; only CreateBookFile does. Since sync_file:lookup:<book>:
// <file> is keyed on that unchanged id, the syncFileID must survive
// untouched.
func TestSyncIdentitySurvives_FileReplace_SameID(t *testing.T) {
	store := setupTestStore(t)
	sf := database.AsSyncFileStore(store)
	require.NotNil(t, sf, "PebbleStore must implement SyncFileStore")

	bookID := ulid.Make().String()
	_, err := store.CreateBook(&database.Book{ID: bookID, Title: "Remuxed Book", Format: "m4b", FilePath: "/tmp/survival/remux.m4b"})
	require.NoError(t, err)

	fileID := ulid.Make().String()
	require.NoError(t, store.CreateBookFile(&database.BookFile{
		ID: fileID, BookID: bookID, FilePath: "/tmp/survival/remux.m4b",
		Format: "m4b", FileHash: "original-hash", FileSize: 1000,
	}))

	syncFileID, err := sf.MintOrGetSyncFileID(bookID, fileID)
	require.NoError(t, err)

	// Simulate a remux: same logical track, same BookFile.ID, new bytes.
	require.NoError(t, store.UpdateBookFile(fileID, &database.BookFile{
		ID: fileID, BookID: bookID, FilePath: "/tmp/survival/remux.m4b",
		Format: "m4b", FileHash: "remuxed-hash", FileSize: 2000,
	}))

	got, has, err := sf.GetSyncFileID(bookID, fileID)
	require.NoError(t, err)
	require.True(t, has)
	require.Equal(t, syncFileID, got, "an in-place remux must not change the syncFileID: BookFile.ID is unchanged")
}

// TestSyncIdentitySurvives_FileReplace_Primitive exercises RepointSyncFile
// directly.
//
// This calls the RepointSyncFile primitive directly because no production
// code path invokes it yet -- there is currently no delete-and-recreate file
// replacement path in this codebase (today's only replacement mechanism is
// the same-ID UpdateBookFile case proven above). This test stands in for a
// HYPOTHETICAL future replace path that deletes the old BookFile row and
// creates a genuinely new one (a different remux strategy, or a
// quality-upgrade that intentionally drops the old row rather than
// overwriting it). Wiring a real caller for that path is an open follow-up,
// not proven by this test.
func TestSyncIdentitySurvives_FileReplace_Primitive(t *testing.T) {
	store := setupTestStore(t)
	sf := database.AsSyncFileStore(store)
	require.NotNil(t, sf)

	bookID := ulid.Make().String()
	_, err := store.CreateBook(&database.Book{ID: bookID, Title: "Upgraded Book", Format: "mp3", FilePath: "/tmp/survival/upgrade.mp3"})
	require.NoError(t, err)

	oldFileID := ulid.Make().String()
	require.NoError(t, store.CreateBookFile(&database.BookFile{
		ID: oldFileID, BookID: bookID, FilePath: "/tmp/survival/upgrade-128k.mp3",
		Format: "mp3", FileHash: "low-quality-hash", FileSize: 500,
	}))
	syncFileID, err := sf.MintOrGetSyncFileID(bookID, oldFileID)
	require.NoError(t, err)

	newFileID := ulid.Make().String()
	require.NoError(t, sf.RepointSyncFile(bookID, oldFileID, newFileID))

	gotNew, has, err := sf.GetSyncFileID(bookID, newFileID)
	require.NoError(t, err)
	require.True(t, has, "the new fileID must own the existing syncFileID")
	require.Equal(t, syncFileID, gotNew)

	_, has, err = sf.GetSyncFileID(bookID, oldFileID)
	require.NoError(t, err)
	require.False(t, has, "the old (bookID, fileID) pair must no longer resolve after repoint")
}

// TestSyncIdentitySurvives_RedirectChain_PathologicalCycle is the "cycle"
// half of ResolveSyncItem's documented protection ("caps at 10 hops and
// tracks visited ids to guard against a cycle or runaway chain" --
// internal/database/pebble_store_syncid.go). The happy-path chain
// (B -> A -> C, a client holding B's id resolving all the way to C) already
// has two independent tests -- internal/database/pebble_store_syncid_test.go's
// TestSyncID_RecordSyncMerge_ThreeWayRedirectChain and this package's
// TestMergeBooks_SyncIdentity_ChainedMerges (sync_follow_test.go) -- so this
// test does not repeat that; it is the pathological case neither of them
// covers.
//
// A 2-node cycle (A redirects to B, B redirects to A) is reachable through
// nothing more exotic than two ordinary RecordSyncMerge calls in opposite
// directions -- e.g. two conflicting merge operations resolved against each
// other, or a retried merge racing a reversed one. RecordSyncMerge never
// touches the reverse index (sync_item:book:<bookID>), only the SyncItem
// records' RedirectTo, so book A's reverse index still resolves to A's
// original syncID even after A's SyncItem has been redirected away -- which
// is exactly what lets the second call construct the cycle instead of a
// third-party record. A real occurrence of this would be a data
// inconsistency bug, not a designed code path; ResolveSyncItem must fail
// loudly (a bounded error) rather than loop forever or silently return
// stale/wrong data.
func TestSyncIdentitySurvives_RedirectChain_PathologicalCycle(t *testing.T) {
	store := setupTestStore(t)
	ids := database.AsSyncIdentityStore(store)
	require.NotNil(t, ids)

	aID := ulid.Make().String()
	bID := ulid.Make().String()
	_, err := store.CreateBook(&database.Book{ID: aID, Title: "Cycle A", Format: "mp3", FilePath: "/tmp/survival/cycle-a.mp3"})
	require.NoError(t, err)
	_, err = store.CreateBook(&database.Book{ID: bID, Title: "Cycle B", Format: "mp3", FilePath: "/tmp/survival/cycle-b.mp3"})
	require.NoError(t, err)

	aSync, err := ids.MintOrGetSyncID(aID)
	require.NoError(t, err)
	_, err = ids.MintOrGetSyncID(bID)
	require.NoError(t, err)

	// A "merges into" B: A's SyncItem now redirects to B's.
	require.NoError(t, ids.RecordSyncMerge(aID, bID))
	// B "merges into" A: B's SyncItem now redirects to A's. A's reverse index
	// (sync_item:book:<aID>) was never touched by the first call, so this
	// resolves to A's ORIGINAL syncID and completes the cycle:
	// aSync.RedirectTo == bSync, bSync.RedirectTo == aSync.
	require.NoError(t, ids.RecordSyncMerge(bID, aID))

	_, err = ids.ResolveSyncItem(aSync)
	require.Error(t, err, "a redirect cycle must return an error, not loop forever or return stale data")
}

// TestSyncIdentitySurvives_ComposedLifecycle is this suite's centerpiece.
// Every test above (and every existing per-mechanism test elsewhere in the
// tree) resets to a fresh book per operation. None of them prove the
// mechanisms COMPOSE -- that a repoint, followed by a redirect, followed by
// another redirect, does not drop the identity or the progress somewhere in
// the middle. That is exactly the failure mode a real device would hit: a
// book gets renamed, later deduped into another book, and that book is later
// absorbed into a third. This test drives ONE originating book's identity
// through all three same-ID edits and then through both merge-family hops in
// sequence, and only checks the final state -- if any single link in that
// chain silently drops the identity or the progress, this is the one test
// that would catch it.
//
// dedup.MergeBooks cannot join this chain: internal/dedup imports
// internal/merge (for FollowMergeWithStore), so a test in package merge
// cannot call into internal/dedup without an import cycle. Its hard-delete
// case is proven on its own, end-to-end, by
// internal/dedup/book_dedup_sync_follow_test.go's
// TestMergeBooks_SyncIdentityFollowsHardDelete instead.
//
// Sequence: book X is renamed, moved (tagged), and retagged (three
// UpdateBook calls, same ID throughout) -- proving the syncID survives all
// three before anything merges. X is then merged into Y via MergeBooks (X
// becomes a soft-deleted loser; its progress carries onto Y, since Y has none
// yet -- "winner with no state always loses" per loserIsFurther). Y is then
// absorbed into Z via CombineBooks (Y is hard-deleted; its progress, now
// originally X's, carries onto Z the same way). A client still holding X's
// ORIGINAL syncID must resolve through both hops to Z's live item, and Z must
// hold the progress and position that started on X.
func TestSyncIdentitySurvives_ComposedLifecycle(t *testing.T) {
	store := setupTestStore(t)
	ids := database.AsSyncIdentityStore(store)
	require.NotNil(t, ids)
	user := seedSyncUser(t, store)

	xID := seedBookWithProgress(t, store, user.ID, "Composed X", "mp3", 50)
	xSync, err := ids.MintOrGetSyncID(xID)
	require.NoError(t, err)

	// Rename, move (tagged), retag -- same Book.ID throughout.
	xBook, err := store.GetBookByID(xID)
	require.NoError(t, err)
	xBook.Title = "Composed X Renamed"
	_, err = store.UpdateBook(xID, xBook)
	require.NoError(t, err)

	xBook, err = store.GetBookByID(xID)
	require.NoError(t, err)
	xBook.FilePath = "/tmp/survival/composed-moved.mp3"
	_, err = store.UpdateBook(xID, xBook)
	require.NoError(t, err)

	xBook, err = store.GetBookByID(xID)
	require.NoError(t, err)
	narrator := "Composed Narrator"
	xBook.Narrator = &narrator
	_, err = store.UpdateBook(xID, xBook)
	require.NoError(t, err)

	stillXSync, has, err := ids.GetSyncIDForBook(xID)
	require.NoError(t, err)
	require.True(t, has)
	require.Equal(t, xSync, stillXSync, "the rename/move/retag trio must not disturb the syncID before any merge")

	yID := ulid.Make().String()
	_, err = store.CreateBook(&database.Book{ID: yID, Title: "Composed Y", Format: "m4b", FilePath: "/tmp/survival/composed-y.m4b"})
	require.NoError(t, err)
	zID := ulid.Make().String()
	_, err = store.CreateBook(&database.Book{ID: zID, Title: "Composed Z", Format: "m4b", FilePath: "/tmp/survival/composed-z.m4b"})
	require.NoError(t, err)

	ms := NewService(store)

	// Hop 1: X merges into Y (soft-delete). Y has no progress yet, so X's
	// carries over in full.
	_, err = ms.MergeBooks([]string{yID, xID}, yID)
	require.NoError(t, err)

	// Hop 2: Y is absorbed into Z (hard-delete). Z has no progress yet
	// either, so Y's (originally X's) carries over in full.
	_, err = ms.CombineBooks([]string{yID, zID}, zID, nil)
	require.NoError(t, err)

	zSync, has, err := ids.GetSyncIDForBook(zID)
	require.NoError(t, err)
	require.True(t, has)

	resolved, err := ids.ResolveSyncItem(xSync)
	require.NoError(t, err)
	require.NotNil(t, resolved)
	require.Equal(t, zSync, resolved.SyncID, "X's ORIGINAL syncID must resolve through both the merge and the combine to Z")

	state, err := store.GetUserBookState(user.ID, zID)
	require.NoError(t, err)
	require.NotNil(t, state, "the progress that started on X must have survived both hops onto Z")
	require.Equal(t, 50, state.ProgressPct)

	positions, err := store.ListUserPositionsForBook(user.ID, zID)
	require.NoError(t, err)
	require.Len(t, positions, 1)
	require.Equal(t, "seg-1", positions[0].SegmentID)
	require.InDelta(t, 111.0, positions[0].PositionSeconds, 0.001)
}
