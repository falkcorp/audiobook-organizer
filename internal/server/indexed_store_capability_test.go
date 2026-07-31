// file: internal/server/indexed_store_capability_test.go
// version: 1.0.0
// guid: 2c7f4b18-6e93-4a52-9d81-5f0a3b6c8e27
// last-edited: 2026-07-30

package server

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestIndexedStorePreservesCapabilityLookups pins the production regression at
// its real site.
//
// Server.Start() replaces s.store with &indexedStore{Store: inner} whenever the
// Bleve index opened successfully — true on the production host, false in most
// local runs, which is why this class of bug reproduced nowhere else. Because
// indexedStore embeds the database.Store INTERFACE, every narrow capability
// interface that deliberately lives outside database.Store became unreachable
// through it, and every database.As*Store lookup started returning nil.
//
// Observed fallout on 2026-07-30: the backfill-sync-ids maintenance job failed
// with "store does not implement the sync-identity capability interfaces", and
// internal/merge's sync-follow hook — the thing that keeps a listener's position
// attached to a book across a dedup merge — silently degraded to a no-op.
func TestIndexedStorePreservesCapabilityLookups(t *testing.T) {
	inner, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = inner.Close() })

	// The real decorator, constructed exactly as server_lifecycle.go does.
	// server is nil: no test here triggers a book mutation, so the index-enqueue
	// path is never reached.
	var wrapped database.Store = &indexedStore{Store: inner, server: nil}

	if database.AsSyncIdentityStore(wrapped) == nil {
		t.Error("AsSyncIdentityStore through indexedStore == nil; " +
			"the ABS sync-identity backfill and merge-follow hook both break")
	}
	if database.AsSyncFileStore(wrapped) == nil {
		t.Error("AsSyncFileStore through indexedStore == nil; " +
			"per-file ABS ids stop resolving")
	}
	if database.AsBookmarkStore(wrapped) == nil {
		t.Error("AsBookmarkStore through indexedStore == nil; " +
			"/api/me would report an empty bookmark list")
	}
}

// TestIndexedStoreCapabilityRoundTrip proves the resolved capability actually
// writes through to the inner store, rather than merely satisfying the type
// assertion.
func TestIndexedStoreCapabilityRoundTrip(t *testing.T) {
	inner, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = inner.Close() })

	var wrapped database.Store = &indexedStore{Store: inner, server: nil}

	ids := database.AsSyncIdentityStore(wrapped)
	if ids == nil {
		t.Fatal("AsSyncIdentityStore through indexedStore == nil")
	}

	minted, err := ids.MintOrGetSyncID("book-capability-roundtrip")
	if err != nil {
		t.Fatalf("MintOrGetSyncID through the decorator: %v", err)
	}
	if len(minted) != 36 {
		t.Errorf("minted sync id = %q (len %d); want a 36-char UUID", minted, len(minted))
	}

	// Read it back through the BARE inner store: proves the write landed in the
	// real Pebble keyspace and was not absorbed by the decorator.
	got, ok, err := inner.GetSyncIDForBook("book-capability-roundtrip")
	if err != nil {
		t.Fatalf("GetSyncIDForBook on the inner store: %v", err)
	}
	if !ok || got != minted {
		t.Errorf("inner store has (%q, %v); want (%q, true)", got, ok, minted)
	}
}
