// file: internal/server/store_capability_resolve_test.go
// version: 2.2.0
// guid: 2c7f4b18-6e93-4a52-9d81-5f0a3b6c8e27
// last-edited: 2026-10-15

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// embeddingDecorator is a store decorator shaped like the one NewServer used to
// install for search indexing (indexedStore, removed in 07-S3): it embeds the
// database.Store INTERFACE, so only that interface's method set is promoted and
// every narrow capability interface that deliberately lives outside it is
// unreachable by a bare type assertion; Unwrap is what makes them discoverable
// again. The server's own store is the raw store now, but other layers wrap
// stores the same way, and the resolvers below must keep seeing through them.
type embeddingDecorator struct{ database.Store }

func (d *embeddingDecorator) Unwrap() database.Store { return d.Store }

// TestDecoratedStorePreservesCapabilityLookups pins the production regression
// at its real site.
//
// Until 2026-09-13 Server.Start() replaced s.store with &indexedStore{Store: inner}
// whenever the Bleve index opened successfully (from then until 07-S3, NewServer
// installed it unconditionally) -- true on the production host,
// false in most local runs, which is why this class of bug reproduced nowhere
// else. Because indexedStore embedded the database.Store INTERFACE, every
// narrow capability interface that deliberately lives outside database.Store
// became unreachable through it, and every database.As*Store lookup started
// returning nil.
//
// Observed fallout on 2026-07-30: the backfill-sync-ids maintenance job failed
// with "store does not implement the sync-identity capability interfaces", and
// internal/merge's sync-follow hook -- the thing that keeps a listener's position
// attached to a book across a dedup merge -- silently degraded to a no-op.
func TestDecoratedStorePreservesCapabilityLookups(t *testing.T) {
	inner, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = inner.Close() })

	var wrapped database.Store = &embeddingDecorator{Store: inner}

	if database.AsSyncIdentityStore(wrapped) == nil {
		t.Error("AsSyncIdentityStore through a decorator == nil; " +
			"the ABS sync-identity backfill and merge-follow hook both break")
	}
	if database.AsSyncFileStore(wrapped) == nil {
		t.Error("AsSyncFileStore through a decorator == nil; " +
			"per-file ABS ids stop resolving")
	}
	if database.AsBookmarkStore(wrapped) == nil {
		t.Error("AsBookmarkStore through a decorator == nil; " +
			"/api/me would report an empty bookmark list")
	}
	if database.AsSyncAliasUseStore(wrapped) == nil {
		t.Error("AsSyncAliasUseStore through a decorator == nil; " +
			"the ABS surface refuses to start")
	}
}

// TestDecoratedStoreCapabilityRoundTrip proves the resolved capability actually
// writes through to the inner store, rather than merely satisfying the type
// assertion.
func TestDecoratedStoreCapabilityRoundTrip(t *testing.T) {
	inner, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = inner.Close() })

	var wrapped database.Store = &embeddingDecorator{Store: inner}

	ids := database.AsSyncIdentityStore(wrapped)
	if ids == nil {
		t.Fatal("AsSyncIdentityStore through a decorator == nil")
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

// TestDecoratedStoreResolvesConcretePebbleStore covers the second half of the same
// bug: a call site that asserts on the CONCRETE *database.PebbleStore rather than
// a narrow interface fails through this decorator exactly the same way.
//
// Those sites are worse than the interface ones because each has a deliberate
// "different backend" fallback written for SQLite and test doubles. A wrapped
// Pebble store is indistinguishable from an unsupported backend at a bare
// assertion, so the fallback fires and the configuration looks supported.
func TestDecoratedStoreResolvesConcretePebbleStore(t *testing.T) {
	inner, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = inner.Close() })

	var wrapped database.Store = &embeddingDecorator{Store: inner}

	if got := database.AsPebbleStore(wrapped); got != inner {
		t.Fatalf("AsPebbleStore through a decorator = %p, want the inner store %p",
			got, inner)
	}

	// And the bare form still fails, which is the whole reason AsPebbleStore has
	// to be used at those call sites.
	if _, ok := wrapped.(*database.PebbleStore); ok {
		t.Error("bare *database.PebbleStore assertion unexpectedly saw through " +
			"a decorator; the AsPebbleStore call sites may no longer need it")
	}
}

// TestStoreExposesVersionGroupBackfill is the THIRD instance of the bug
// this file exists to prevent, and the first one caught in production by its own
// log line rather than by a failing job.
//
// server_lifecycle.go used a bare `s.Ops().(vgBackfiller)` type assertion.
// BackfillVersionGroupIndex is a *PebbleStore method that deliberately lives
// outside database.Store, so it is not promoted through the embedded interface
// and the assertion missed on every boot where the Bleve index opened — i.e.
// always, in production. MEASURED 2026-08-10 23:07:40 on the prod host:
//
//	versiongroup-backfill: store does not implement BackfillVersionGroupIndex,
//	index will NOT be rebuilt   store_type=*server.indexedStore  (the since-removed decorator)
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
func TestStoreExposesVersionGroupBackfill(t *testing.T) {
	inner, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = inner.Close() })
	inner.WaitForWarmup()

	var wrapped database.Store = &embeddingDecorator{Store: inner}

	// The shape of the original bug: a bare assertion cannot see it. If this
	// ever starts succeeding, the decorator has changed and the guard below
	// stops testing anything — so assert it rather than imply it.
	if _, ok := wrapped.(vgBackfiller); ok {
		t.Fatal("a bare type assertion now resolves vgBackfiller through the " +
			"decorator; this test no longer reproduces the production bug")
	}

	// The PRODUCTION resolver, not database.AsCapability directly. Calling the
	// helper here would prove only that the helper works, which says nothing
	// about whether server_lifecycle.go uses it. Reverting
	// resolveVGBackfiller to a bare assertion must turn this red.
	b, ok := resolveVGBackfiller(wrapped)
	if !ok {
		t.Fatal("resolveVGBackfiller through a decorator failed; the " +
			"version-group index backfill would silently never run")
	}

	// Seed through the INNER store; the read below goes through the decorator,
	// which is the direction that matters here.
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

// TestWarmupWaiterResolvesThroughDecorator is the FOURTH instance of the bug
// this file exists to prevent.
//
// wire_abs_routes.go used a bare `s.Ops().(*database.PebbleStore)` to reach
// WaitForWarmup before building the ABS contributor cache. WaitForWarmup is a
// *PebbleStore method, so the assertion misses whenever a store decorator is
// installed (until 07-S3 that was the Bleve indexedStore) -- and the fallback is
// to skip the wait entirely and warm anyway.
//
// That fallback is not a degraded mode, it is a wrong answer with a long life:
// the cache stores the set of authors of VISIBLE books, so building it against a
// half-published memdb caches a view of a library that does not exist and serves
// it for the whole TTL. The warm is launched from a goroutine at wire time, so
// whether the decorator is installed yet is a race rather than a constant, which
// is why this never presented as a clean always-broken failure.
//
// SCOPE: asserts the capability RESOLVES through the decorator. It does not
// re-prove that WaitForWarmup itself blocks correctly.
func TestWarmupWaiterResolvesThroughDecorator(t *testing.T) {
	inner, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = inner.Close() })

	var wrapped database.Store = &embeddingDecorator{Store: inner}

	// The shape of the original bug: a bare assertion cannot see through the
	// decorator. Assert it rather than imply it — if this ever starts
	// succeeding, the decorator has changed and the check below stops testing
	// anything.
	if _, ok := wrapped.(*database.PebbleStore); ok {
		t.Fatal("a bare *database.PebbleStore assertion now resolves through the " +
			"decorator; this test no longer reproduces the production bug")
	}

	// The PRODUCTION resolver, not database.AsCapability directly. Calling the
	// helper here would prove only that the helper works, which says nothing
	// about whether wire_abs_routes.go uses it. Reverting resolveWarmupWaiter to
	// a bare assertion must turn this red.
	w, ok := resolveWarmupWaiter(wrapped)
	if !ok {
		t.Fatal("resolveWarmupWaiter through a decorator failed; the ABS " +
			"contributor cache would be warmed against a half-published memdb")
	}
	w.WaitForWarmup()
}

// TestStoreExposesBookAtPathBackfill pins that LiveBookIDsAtPath is on
// database.Store, so a decorator forwards it with no capability lookup.
// #3335's fs-regroup-xml apply asserts a narrow interface containing it against
// OpsStore(); if the method ever leaves database.Store, that assertion misses
// and the op refuses to run.
//
// The startup build's capability half moved to the maintenance package
// (TestBookAtPathBackfill_IfNeededThroughStoreDecorator) in 04-P6 part 2: the
// boot goroutine and resolveBookAtPathBackfiller are gone, and the production
// resolution is now the op's own AsCapability call.
func TestStoreExposesBookAtPathBackfill(t *testing.T) {
	inner, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = inner.Close() })
	inner.WaitForWarmup()

	var wrapped database.Store = &embeddingDecorator{Store: inner}

	// Seed and build through the inner store.
	created, err := inner.CreateBook(&database.Book{Title: "At Path", FilePath: "/lib/a"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	res, err := inner.BackfillBookAtPathIndex(context.Background(), nil)
	if err != nil {
		t.Fatalf("BackfillBookAtPathIndex: %v", err)
	}
	if res.Skipped || res.Scanned != 1 {
		t.Fatalf("backfill result = %+v, want one book scanned", res)
	}

	ids, err := wrapped.LiveBookIDsAtPath("/lib/a")
	if err != nil {
		t.Fatalf("LiveBookIDsAtPath through the decorator: %v", err)
	}
	if len(ids) != 1 || ids[0] != created.ID {
		t.Fatalf("LiveBookIDsAtPath = %v, want [%s]", ids, created.ID)
	}
}

// The opchange_by_book: ensure's decorator case moved to the maintenance
// package (TestOpchangeBookIndexEnsure_ThroughStoreDecorator) in 04-P7: the
// boot goroutine and resolveOpChangeIndexBackfiller are gone, and the
// production resolution is now the op's own AsCapability call.

// TestDecoratedStoreResolvesStrictUserLister pins the fail-closed user listing
// the hard deletes rely on (#3769 review NIT c). merge.NewUserStateProbe
// resolves database.StrictUserLister with AsCapability; through this
// decorator that only works because of Unwrap. Without it the probe falls
// back to ListUsers, which skips an undecodable user row, and a delete would
// go ahead on a "no state" that never looked at that user's rows.
func TestDecoratedStoreResolvesStrictUserLister(t *testing.T) {
	inner, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = inner.Close() })
	if err := inner.SetRaw("u:broken", []byte("{not json")); err != nil {
		t.Fatalf("seed undecodable user row: %v", err)
	}

	var wrapped database.Store = &embeddingDecorator{Store: inner}

	strict, ok := database.AsCapability[database.StrictUserLister](wrapped)
	if !ok {
		t.Fatal("AsCapability[StrictUserLister] through a decorator failed")
	}
	if _, err := strict.ListUsersStrict(); !errors.Is(err, database.ErrUndecodableUserRows) {
		t.Fatalf("ListUsersStrict through a decorator = %v, want ErrUndecodableUserRows", err)
	}
	if _, err := merge.NewUserStateProbe(wrapped); !errors.Is(err, database.ErrUndecodableUserRows) {
		t.Fatalf("NewUserStateProbe through a decorator = %v, want ErrUndecodableUserRows (fail closed)", err)
	}
}
