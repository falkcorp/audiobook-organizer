// file: internal/database/pebble_store_syncid_test.go
// version: 1.0.0
// guid: c4877e93-ba6a-468d-b428-30be15fdfa27
// last-edited: 2026-07-30

// Tests for the sync_item:/sync_item:book: keyspace (durable ABS libraryItemId
// identity). Covers: mint-on-first-encounter idempotency, distinct IDs per book,
// UUIDv4 shape, unminted lookup, repoint on untagged move, merge-redirect
// resolution (including a 3-way chain), merge idempotency, and a concurrent
// mint race under -race.

package database

import (
	"path/filepath"
	"regexp"
	"sync"
	"testing"
)

// newPebbleStoreForSyncID opens a fresh PebbleStore in a temp directory and
// registers cleanup. Returns a *PebbleStore so tests can call sync-id-specific
// methods directly.
func newPebbleStoreForSyncID(t *testing.T) *PebbleStore {
	t.Helper()
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "syncid-db"))
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

var syncIDShapeRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestSyncID_MintOnFirstEncounter_IsIdempotent(t *testing.T) {
	store := newPebbleStoreForSyncID(t)

	first, err := store.MintOrGetSyncID("book-a")
	if err != nil {
		t.Fatalf("MintOrGetSyncID first call: %v", err)
	}
	second, err := store.MintOrGetSyncID("book-a")
	if err != nil {
		t.Fatalf("MintOrGetSyncID second call: %v", err)
	}
	if first != second {
		t.Fatalf("MintOrGetSyncID not idempotent: first=%q second=%q", first, second)
	}
}

func TestSyncID_DifferentBooksGetDifferentIDs(t *testing.T) {
	store := newPebbleStoreForSyncID(t)

	idA, err := store.MintOrGetSyncID("book-a")
	if err != nil {
		t.Fatalf("MintOrGetSyncID book-a: %v", err)
	}
	idB, err := store.MintOrGetSyncID("book-b")
	if err != nil {
		t.Fatalf("MintOrGetSyncID book-b: %v", err)
	}
	if idA == idB {
		t.Fatalf("two different books got the same syncID: %q", idA)
	}
}

func TestSyncID_MintedIDMatchesUUIDv4Shape(t *testing.T) {
	store := newPebbleStoreForSyncID(t)

	id, err := store.MintOrGetSyncID("book-a")
	if err != nil {
		t.Fatalf("MintOrGetSyncID: %v", err)
	}
	if len(id) != 36 {
		t.Fatalf("syncID length = %d, want 36 (Absorb splits compound keys at fixed offset substring(0,36)): %q", len(id), id)
	}
	if !syncIDShapeRE.MatchString(id) {
		t.Fatalf("syncID %q does not match canonical UUIDv4 shape %s", id, syncIDShapeRE.String())
	}
}

func TestSyncID_GetSyncIDForBook_Unminted(t *testing.T) {
	store := newPebbleStoreForSyncID(t)

	id, ok, err := store.GetSyncIDForBook("never-seen-book")
	if err != nil {
		t.Fatalf("GetSyncIDForBook on unminted book returned error: %v", err)
	}
	if ok {
		t.Fatalf("GetSyncIDForBook on unminted book returned ok=true, want false")
	}
	if id != "" {
		t.Fatalf("GetSyncIDForBook on unminted book returned id=%q, want empty", id)
	}
}

func TestSyncID_RepointSyncItem_MovesReverseIndexAndCurrentBookID(t *testing.T) {
	store := newPebbleStoreForSyncID(t)

	oldID, err := store.MintOrGetSyncID("book-old")
	if err != nil {
		t.Fatalf("MintOrGetSyncID book-old: %v", err)
	}

	if err := store.RepointSyncItem("book-old", "book-new"); err != nil {
		t.Fatalf("RepointSyncItem: %v", err)
	}

	// The reverse index for the OLD book id must be gone.
	_, ok, err := store.GetSyncIDForBook("book-old")
	if err != nil {
		t.Fatalf("GetSyncIDForBook book-old after repoint: %v", err)
	}
	if ok {
		t.Fatalf("GetSyncIDForBook book-old after repoint still resolves, want gone")
	}

	// The reverse index for the NEW book id must resolve to the SAME syncID.
	newLookupID, ok, err := store.GetSyncIDForBook("book-new")
	if err != nil {
		t.Fatalf("GetSyncIDForBook book-new after repoint: %v", err)
	}
	if !ok {
		t.Fatalf("GetSyncIDForBook book-new after repoint: want ok=true")
	}
	if newLookupID != oldID {
		t.Fatalf("repoint changed the syncID itself: got %q, want %q (same identity, new current book)", newLookupID, oldID)
	}

	// The SyncItem record's CurrentBookID must be updated.
	item, err := store.ResolveSyncItem(oldID)
	if err != nil {
		t.Fatalf("ResolveSyncItem: %v", err)
	}
	if item == nil {
		t.Fatalf("ResolveSyncItem returned nil for a known syncID")
	}
	if item.CurrentBookID != "book-new" {
		t.Fatalf("SyncItem.CurrentBookID = %q, want %q", item.CurrentBookID, "book-new")
	}

	// Calling MintOrGetSyncID on the OLD book id now must mint a BRAND NEW,
	// different ID — proving the reverse index really moved, not just got
	// copied.
	freshOldID, err := store.MintOrGetSyncID("book-old")
	if err != nil {
		t.Fatalf("MintOrGetSyncID book-old after repoint: %v", err)
	}
	if freshOldID == oldID {
		t.Fatalf("MintOrGetSyncID book-old after repoint returned the SAME id %q, want a new one (reverse index should have moved, not been copied)", freshOldID)
	}
}

func TestSyncID_RecordSyncMerge_RedirectsLoserToWinner(t *testing.T) {
	store := newPebbleStoreForSyncID(t)

	loserID, err := store.MintOrGetSyncID("book-loser")
	if err != nil {
		t.Fatalf("MintOrGetSyncID book-loser: %v", err)
	}
	winnerID, err := store.MintOrGetSyncID("book-winner")
	if err != nil {
		t.Fatalf("MintOrGetSyncID book-winner: %v", err)
	}

	if err := store.RecordSyncMerge("book-loser", "book-winner"); err != nil {
		t.Fatalf("RecordSyncMerge: %v", err)
	}

	resolved, err := store.ResolveSyncItem(loserID)
	if err != nil {
		t.Fatalf("ResolveSyncItem(loserID): %v", err)
	}
	if resolved == nil {
		t.Fatalf("ResolveSyncItem(loserID) returned nil, want the winner's live record")
	}
	if resolved.SyncID != winnerID {
		t.Fatalf("ResolveSyncItem(loserID).SyncID = %q, want winner's %q", resolved.SyncID, winnerID)
	}
	if resolved.RedirectTo != "" {
		t.Fatalf("resolved (winner) record has RedirectTo=%q, want empty (live record)", resolved.RedirectTo)
	}

	winnerItem, err := store.ResolveSyncItem(winnerID)
	if err != nil {
		t.Fatalf("ResolveSyncItem(winnerID): %v", err)
	}
	found := false
	for _, m := range winnerItem.MergedFrom {
		if m == loserID {
			found = true
		}
	}
	if !found {
		t.Fatalf("winner's MergedFrom %v does not contain loser's syncID %q", winnerItem.MergedFrom, loserID)
	}

	// Idempotency: calling RecordSyncMerge again with the same pair is a no-op.
	if err := store.RecordSyncMerge("book-loser", "book-winner"); err != nil {
		t.Fatalf("RecordSyncMerge (second call): %v", err)
	}
	winnerItem2, err := store.ResolveSyncItem(winnerID)
	if err != nil {
		t.Fatalf("ResolveSyncItem(winnerID) after second merge: %v", err)
	}
	if len(winnerItem2.MergedFrom) != len(winnerItem.MergedFrom) {
		t.Fatalf("MergedFrom length changed on re-run: before=%d after=%d", len(winnerItem.MergedFrom), len(winnerItem2.MergedFrom))
	}

	loserRaw, err := store.ResolveSyncItem(loserID)
	if err != nil {
		t.Fatalf("ResolveSyncItem(loserID) after second merge: %v", err)
	}
	if loserRaw.SyncID != winnerID {
		t.Fatalf("RedirectTo changed on re-run: resolved to %q, want %q", loserRaw.SyncID, winnerID)
	}
}

func TestSyncID_RecordSyncMerge_ThreeWayRedirectChain(t *testing.T) {
	store := newPebbleStoreForSyncID(t)

	bID, err := store.MintOrGetSyncID("book-b")
	if err != nil {
		t.Fatalf("MintOrGetSyncID book-b: %v", err)
	}
	if _, err := store.MintOrGetSyncID("book-a"); err != nil {
		t.Fatalf("MintOrGetSyncID book-a: %v", err)
	}
	cID, err := store.MintOrGetSyncID("book-c")
	if err != nil {
		t.Fatalf("MintOrGetSyncID book-c: %v", err)
	}

	// Merge B into A.
	if err := store.RecordSyncMerge("book-b", "book-a"); err != nil {
		t.Fatalf("RecordSyncMerge(B into A): %v", err)
	}
	// Merge A's book into C (A's syncID now redirects to C's).
	if err := store.RecordSyncMerge("book-a", "book-c"); err != nil {
		t.Fatalf("RecordSyncMerge(A into C): %v", err)
	}

	// B's ORIGINAL syncID must resolve all the way through A to C's live record.
	resolved, err := store.ResolveSyncItem(bID)
	if err != nil {
		t.Fatalf("ResolveSyncItem(bID): %v", err)
	}
	if resolved == nil {
		t.Fatalf("ResolveSyncItem(bID) returned nil, want C's live record")
	}
	if resolved.SyncID != cID {
		t.Fatalf("ResolveSyncItem(bID).SyncID = %q, want C's %q (chain B->A->C not followed)", resolved.SyncID, cID)
	}
	if resolved.RedirectTo != "" {
		t.Fatalf("resolved (C's) record has RedirectTo=%q, want empty (live record)", resolved.RedirectTo)
	}
}

func TestSyncID_ConcurrentMintRace_SingleWinner(t *testing.T) {
	store := newPebbleStoreForSyncID(t)

	const goroutines = 16
	results := make([]string, goroutines)
	errs := make([]error, goroutines)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			id, err := store.MintOrGetSyncID("race-book")
			results[i] = id
			errs[i] = err
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: MintOrGetSyncID error: %v", i, err)
		}
	}

	want := results[0]
	if want == "" {
		t.Fatalf("goroutine 0 returned empty syncID")
	}
	for i, got := range results {
		if got != want {
			t.Fatalf("goroutine %d returned syncID %q, want %q (all callers should mint/observe the SAME id)", i, got, want)
		}
	}

	// Exactly one live record for the book — no orphaned second sync_item
	// record from a lost race.
	finalID, ok, err := store.GetSyncIDForBook("race-book")
	if err != nil {
		t.Fatalf("GetSyncIDForBook: %v", err)
	}
	if !ok {
		t.Fatalf("GetSyncIDForBook: want ok=true after concurrent mint")
	}
	if finalID != want {
		t.Fatalf("GetSyncIDForBook returned %q, want %q", finalID, want)
	}
}
