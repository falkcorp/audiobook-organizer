// file: internal/database/store_capability_test.go
// version: 1.0.0
// guid: 6f2b0a91-4d3c-4e57-9a10-2c8d5b7e4f13
// last-edited: 2026-07-30

package database

import "testing"

// decoratorStore mimics internal/server.indexedStore: it embeds the
// database.Store INTERFACE, so only that interface's method set is promoted.
// Every narrow capability interface (SyncIdentityStore, SyncFileStore,
// BookmarkStore) is deliberately absent from database.Store, so a plain type
// assertion against a decorator finds nothing — which is the production bug this
// test pins down.
type decoratorStore struct {
	Store
}

func (d *decoratorStore) Unwrap() Store { return d.Store }

// decoratorNoUnwrap is a decorator that forgot to expose Unwrap. Capability
// discovery through it MUST fail: silently reaching around a decorator that has
// not opted in would bypass whatever behaviour it exists to add.
type decoratorNoUnwrap struct {
	Store
}

func newCapabilityTestStore(t *testing.T) *PebbleStore {
	t.Helper()
	ps, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	return ps
}

// TestCapabilityLookupsSurviveOneDecorator is the regression test for the
// production failure "backfill-sync-ids: store does not implement the
// sync-identity capability interfaces": the server wraps s.store in an
// indexedStore during Start(), and every As*Store helper then returned nil.
func TestCapabilityLookupsSurviveOneDecorator(t *testing.T) {
	inner := newCapabilityTestStore(t)
	var wrapped Store = &decoratorStore{Store: inner}

	if AsSyncIdentityStore(wrapped) == nil {
		t.Error("AsSyncIdentityStore through a decorator == nil; want the inner capability")
	}
	if AsSyncFileStore(wrapped) == nil {
		t.Error("AsSyncFileStore through a decorator == nil; want the inner capability")
	}
	if AsBookmarkStore(wrapped) == nil {
		t.Error("AsBookmarkStore through a decorator == nil; want the inner capability")
	}
}

// TestCapabilityLookupsSurviveNestedDecorators guards against a future second
// decorator being layered on (the wrap site already stacks conditionally).
func TestCapabilityLookupsSurviveNestedDecorators(t *testing.T) {
	inner := newCapabilityTestStore(t)
	var wrapped Store = &decoratorStore{Store: &decoratorStore{Store: inner}}

	if AsSyncIdentityStore(wrapped) == nil {
		t.Error("AsSyncIdentityStore through two decorators == nil")
	}
	if AsSyncFileStore(wrapped) == nil {
		t.Error("AsSyncFileStore through two decorators == nil")
	}
	if AsBookmarkStore(wrapped) == nil {
		t.Error("AsBookmarkStore through two decorators == nil")
	}
}

// TestCapabilityLookupsStillFindBareStore pins the pre-existing behaviour so the
// unwrap change cannot regress the common case.
func TestCapabilityLookupsStillFindBareStore(t *testing.T) {
	var bare Store = newCapabilityTestStore(t)

	if AsSyncIdentityStore(bare) == nil {
		t.Error("AsSyncIdentityStore on a bare *PebbleStore == nil")
	}
	if AsSyncFileStore(bare) == nil {
		t.Error("AsSyncFileStore on a bare *PebbleStore == nil")
	}
	if AsBookmarkStore(bare) == nil {
		t.Error("AsBookmarkStore on a bare *PebbleStore == nil")
	}
}

// TestCapabilityLookupsRefuseOpaqueDecorator asserts we do NOT reach through a
// decorator that has not opted in via Unwrap.
func TestCapabilityLookupsRefuseOpaqueDecorator(t *testing.T) {
	inner := newCapabilityTestStore(t)
	var opaque Store = &decoratorNoUnwrap{Store: inner}

	if AsSyncIdentityStore(opaque) != nil {
		t.Error("AsSyncIdentityStore reached through a decorator with no Unwrap; want nil")
	}
}

// TestCapabilityLookupsHandleNilAndNilChain covers the degenerate inputs.
func TestCapabilityLookupsHandleNilAndNilChain(t *testing.T) {
	if AsSyncIdentityStore(nil) != nil {
		t.Error("AsSyncIdentityStore(nil) != nil")
	}
	// A decorator whose inner store is nil must not panic and must not claim a
	// capability it cannot serve.
	var nilChain Store = &decoratorStore{Store: nil}
	if got := AsSyncIdentityStore(nilChain); got != nil {
		t.Errorf("AsSyncIdentityStore over a nil inner store = %v; want nil", got)
	}
}
