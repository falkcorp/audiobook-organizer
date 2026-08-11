// file: internal/server/indexed_store_capability_test.go
// version: 1.2.0
// guid: 2c7f4b18-6e93-4a52-9d81-5f0a3b6c8e27
// last-edited: 2026-08-10

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

// TestIndexedStoreResolvesConcretePebbleStore covers the second half of the same
// bug: the wipe fixups in maintenance_fixups.go assert on the CONCRETE
// *database.PebbleStore rather than a narrow interface, and a bare assertion fails
// through this decorator exactly the same way.
//
// Those sites are worse than the interface ones because each has a deliberate
// "different backend" fallback written for SQLite and test doubles. A wrapped
// Pebble store is indistinguishable from an unsupported backend at a bare
// assertion, so the fallback fires and the configuration looks supported.
func TestIndexedStoreResolvesConcretePebbleStore(t *testing.T) {
	inner, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = inner.Close() })

	var wrapped database.Store = &indexedStore{Store: inner, server: nil}

	if got := database.AsPebbleStore(wrapped); got != inner {
		t.Fatalf("AsPebbleStore through indexedStore = %p, want the inner store %p",
			got, inner)
	}

	// And the bare form still fails, which is the whole reason AsPebbleStore has
	// to be used at those call sites.
	if _, ok := wrapped.(*database.PebbleStore); ok {
		t.Error("bare *database.PebbleStore assertion unexpectedly saw through " +
			"indexedStore; the AsPebbleStore call sites may no longer need it")
	}
}

// TestWipeFixupsReachPebbleThroughDecorator exercises the repaired call sites
// themselves rather than the helper in isolation, because the helper being correct
// says nothing about whether these six functions actually call it.
//
// Only the two fixups that assert BEFORE their dry-run branch are exercised —
// wipeSegments and wipeExternalIDs. Both run a counting query in dry-run mode, so
// nothing is deleted. The other four (wipeBookFiles, wipeBooks, wipeAuthors,
// wipeSeries) return from their dry-run branch before reaching the assertion, so a
// dry-run call cannot distinguish fixed from broken and a non-dry-run call would
// wipe the store; they are covered by AsPebbleStore's own tests instead.
func TestWipeFixupsReachPebbleThroughDecorator(t *testing.T) {
	inner, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = inner.Close() })

	var wrapped database.Store = &indexedStore{Store: inner, server: nil}
	ms, ok := wrapped.(maintenanceStore)
	if !ok {
		t.Fatal("indexedStore does not satisfy maintenanceStore")
	}

	// Before the fix these returned "unsupported store type *server.indexedStore".
	if _, err := wipeSegments(ms, true); err != nil {
		t.Errorf("wipeSegments(dryRun) through the decorator: %v", err)
	}
	if _, err := wipeExternalIDs(ms, true); err != nil {
		t.Errorf("wipeExternalIDs(dryRun) through the decorator: %v", err)
	}
}

// TestIndexedStoreExposesVersionGroupBackfill is the THIRD instance of the bug
// this file exists to prevent, and the first one caught in production by its own
// log line rather than by a failing job.
//
// server_lifecycle.go used a bare `s.Store().(vgBackfiller)` type assertion.
// BackfillVersionGroupIndex is a *PebbleStore method that deliberately lives
// outside database.Store, so it is not promoted through the embedded interface
// and the assertion missed on every boot where the Bleve index opened — i.e.
// always, in production. MEASURED 2026-08-10 23:07:40 on the prod host:
//
//	versiongroup-backfill: store does not implement BackfillVersionGroupIndex,
//	index will NOT be rebuilt   store_type=*server.indexedStore
//
// So the "one-time production repair" for the under-reporting version-group
// index had never run once. The fix is database.AsCapability, which walks the
// Unwrap chain.
//
// SCOPE: this asserts the capability RESOLVES and EXECUTES through the
// decorator. It does not re-prove that the backfill writes correct index rows —
// chunk boundaries, row contents and idempotency are covered by
// pebble_store_versiongroup_backfill_test.go in package database, which can
// reach the raw keys.
func TestIndexedStoreExposesVersionGroupBackfill(t *testing.T) {
	inner, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = inner.Close() })
	inner.WaitForWarmup()

	var wrapped database.Store = &indexedStore{Store: inner, server: nil}

	// The shape of the original bug: a bare assertion cannot see it. If this
	// ever starts succeeding, the decorator has changed and the guard below
	// stops testing anything — so assert it rather than imply it.
	if _, ok := wrapped.(vgBackfiller); ok {
		t.Fatal("a bare type assertion now resolves vgBackfiller through the " +
			"decorator; this test no longer reproduces the production bug")
	}

	// The PRODUCTION resolver, not database.AsCapability directly. Calling the
	// helper here would prove only that the helper works, which says nothing
	// about whether server_lifecycle.go uses it — the same distinction
	// TestWipeFixupsReachPebbleThroughDecorator above is built around. Reverting
	// resolveVGBackfiller to a bare assertion must turn this red.
	b, ok := resolveVGBackfiller(wrapped)
	if !ok {
		t.Fatal("resolveVGBackfiller through indexedStore failed; the " +
			"version-group index backfill would silently never run")
	}

	// Seed through the INNER store. Going through the decorator would call
	// s.server.enqueueIndex on a nil *Server and panic — the same reason every
	// other test in this file stays read-only. The read below still goes through
	// the decorator, which is the direction that matters here.
	gid := "vg-decorator-writethrough"
	created, err := inner.CreateBook(&database.Book{
		Title:          "Decorator Backfill Book",
		VersionGroupID: &gid,
	})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	if err := b.BackfillVersionGroupIndex(); err != nil {
		t.Fatalf("BackfillVersionGroupIndex through the decorator: %v", err)
	}
	got, err := wrapped.GetBooksByVersionGroup(gid)
	if err != nil {
		t.Fatalf("GetBooksByVersionGroup: %v", err)
	}
	if len(got) != 1 || got[0].ID != created.ID {
		t.Fatalf("version group lookup after backfill: got %d books, want 1 (%s)",
			len(got), created.ID)
	}
}
