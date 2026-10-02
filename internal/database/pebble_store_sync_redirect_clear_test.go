// file: internal/database/pebble_store_sync_redirect_clear_test.go
// version: 1.1.0
// guid: df1766e8-aceb-48b1-aea6-f13f8a737f63
// last-edited: 2026-10-01

package database

import (
	"slices"
	"testing"
)

// ClearSyncRedirect is the un-merge a trash restore needs: it removes the
// loser's redirect without being told the winner (a combine shell and a
// MergeBooks loser record no MergedIntoBookID), and leaves every other link
// of a chain alone.
func TestSyncID_ClearSyncRedirect(t *testing.T) {
	store := newPebbleStoreForSyncID(t)
	sync := map[string]string{}
	for _, b := range []string{"loser", "winner", "final", "plain"} {
		id, err := store.MintOrGetSyncID("book-" + b)
		if err != nil {
			t.Fatalf("mint %s: %v", b, err)
		}
		sync[b] = id
	}
	// loser -> winner, then winner -> final: a chain.
	for _, pair := range [][2]string{{"loser", "winner"}, {"winner", "final"}} {
		if err := store.RecordSyncMerge("book-"+pair[0], "book-"+pair[1]); err != nil {
			t.Fatalf("RecordSyncMerge %v: %v", pair, err)
		}
	}

	winner, cleared, err := store.ClearSyncRedirect("book-loser")
	if err != nil || !cleared || winner != "book-winner" {
		t.Fatalf("ClearSyncRedirect = (%q, %v, %v), want (book-winner, true, nil)", winner, cleared, err)
	}
	if it, err := store.ResolveSyncItem(sync["loser"]); err != nil || it == nil || it.SyncID != sync["loser"] {
		t.Fatalf("the loser must resolve to itself: %+v, %v", it, err)
	}
	if it, err := store.ResolveSyncItem(sync["winner"]); err != nil || it == nil || it.SyncID != sync["final"] {
		t.Fatalf("the winner's own redirect must stay: %+v, %v", it, err)
	}
	aliases, err := store.ListSyncAliases(sync["final"])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(aliases, []string{sync["winner"]}) {
		t.Fatalf("aliases of final = %v, want only the winner %s", aliases, sync["winner"])
	}

	// Idempotent, and a no-op for a book with no redirect or no sync item.
	for _, b := range []string{"book-loser", "book-plain", "book-never-minted"} {
		if w, c, err := store.ClearSyncRedirect(b); err != nil || c || w != "" {
			t.Errorf("ClearSyncRedirect(%s) = (%q, %v, %v), want a no-op", b, w, c, err)
		}
	}

	// RecordSyncMerge puts it back (the restore's rollback).
	if err := store.RecordSyncMerge("book-loser", winner); err != nil {
		t.Fatal(err)
	}
	if it, err := store.ResolveSyncItem(sync["loser"]); err != nil || it == nil || it.SyncID != sync["final"] {
		t.Fatalf("re-recorded redirect must resolve through the chain: %+v, %v", it, err)
	}
}

// Two losers merged into one winner: clearing one removes only that loser's
// link. The other still resolves to the winner and stays its alias.
func TestSyncID_ClearSyncRedirect_TwoLosersOneWinner(t *testing.T) {
	store := newPebbleStoreForSyncID(t)
	sync := map[string]string{}
	for _, b := range []string{"l1", "l2", "w"} {
		id, err := store.MintOrGetSyncID("book-" + b)
		if err != nil {
			t.Fatalf("mint %s: %v", b, err)
		}
		sync[b] = id
	}
	for _, l := range []string{"l1", "l2"} {
		if err := store.RecordSyncMerge("book-"+l, "book-w"); err != nil {
			t.Fatalf("RecordSyncMerge %s: %v", l, err)
		}
	}

	winner, cleared, err := store.ClearSyncRedirect("book-l1")
	if err != nil || !cleared || winner != "book-w" {
		t.Fatalf("ClearSyncRedirect = (%q, %v, %v), want (book-w, true, nil)", winner, cleared, err)
	}
	if it, err := store.ResolveSyncItem(sync["l1"]); err != nil || it == nil || it.SyncID != sync["l1"] {
		t.Fatalf("the cleared loser must resolve to itself: %+v, %v", it, err)
	}
	if it, err := store.ResolveSyncItem(sync["l2"]); err != nil || it == nil || it.SyncID != sync["w"] {
		t.Fatalf("the other loser must still resolve to the winner: %+v, %v", it, err)
	}
	aliases, err := store.ListSyncAliases(sync["w"])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(aliases, []string{sync["l2"]}) {
		t.Fatalf("aliases of the winner = %v, want only %s", aliases, sync["l2"])
	}
}

// A chain A -> B -> C with B restored: B is its own item again, and A, whose
// redirect names B, now resolves to B instead of C.
func TestSyncID_ClearSyncRedirect_ChainMiddleRestored(t *testing.T) {
	store := newPebbleStoreForSyncID(t)
	sync := map[string]string{}
	for _, b := range []string{"a", "b", "c"} {
		id, err := store.MintOrGetSyncID("book-" + b)
		if err != nil {
			t.Fatalf("mint %s: %v", b, err)
		}
		sync[b] = id
	}
	for _, pair := range [][2]string{{"a", "b"}, {"b", "c"}} {
		if err := store.RecordSyncMerge("book-"+pair[0], "book-"+pair[1]); err != nil {
			t.Fatalf("RecordSyncMerge %v: %v", pair, err)
		}
	}
	if it, err := store.ResolveSyncItem(sync["a"]); err != nil || it == nil || it.SyncID != sync["c"] {
		t.Fatalf("precondition: A resolves through B to C: %+v, %v", it, err)
	}

	winner, cleared, err := store.ClearSyncRedirect("book-b")
	if err != nil || !cleared || winner != "book-c" {
		t.Fatalf("ClearSyncRedirect = (%q, %v, %v), want (book-c, true, nil)", winner, cleared, err)
	}
	for _, b := range []string{"a", "b"} {
		if it, err := store.ResolveSyncItem(sync[b]); err != nil || it == nil || it.SyncID != sync["b"] {
			t.Fatalf("%s must resolve to the restored B: %+v, %v", b, it, err)
		}
	}
	if it, err := store.ResolveSyncItem(sync["c"]); err != nil || it == nil || it.SyncID != sync["c"] {
		t.Fatalf("C resolves to itself: %+v, %v", it, err)
	}
	if aliases, err := store.ListSyncAliases(sync["c"]); err != nil || len(aliases) != 0 {
		t.Fatalf("aliases of C = %v, %v; want none", aliases, err)
	}
	if aliases, err := store.ListSyncAliases(sync["b"]); err != nil || !slices.Equal(aliases, []string{sync["a"]}) {
		t.Fatalf("aliases of B = %v, %v; want only A %s", aliases, err, sync["a"])
	}
}
