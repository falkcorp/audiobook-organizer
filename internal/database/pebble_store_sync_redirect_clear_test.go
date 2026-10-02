// file: internal/database/pebble_store_sync_redirect_clear_test.go
// version: 1.0.0
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
