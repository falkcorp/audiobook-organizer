// file: internal/server/server_maintenance_store_accessors_test.go
// version: 1.0.0
// guid: 7c3e9a15-2d48-4b6f-9e01-5a8c7d3b2f64
// last-edited: 2026-09-02

package server

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The maintenance plugin's capability accessors run at OP-RUN time, i.e. after
// Start() has replaced s.store with the indexedStore decorator. indexedStore
// embeds the Store interface and so exposes none of the capability methods; a
// bare `s.store.(database.X)` therefore returns nil in production while passing
// every test that hands the accessor a bare *PebbleStore.
//
// FileProvenanceStore shipped that way on 2026-08-21 and ReviewStatusIndexStore
// copied it on 2026-09-02; the ops behind them ("provenance store not
// initialized", "store does not support a status-index rebuild") were inert in
// prod. This test wraps the store exactly the way Start() does and holds both
// accessors to resolving through it.
func TestMaintenanceStoreAccessorsResolveThroughIndexedStore(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	srv := NewServer(store)

	// Sanity: on the bare store both resolve, so a failure below is the wrap.
	if srv.FileProvenanceStore() == nil {
		t.Fatalf("BARE store: FileProvenanceStore() = nil; *PebbleStore must implement it")
	}
	if srv.ReviewStatusIndexStore() == nil {
		t.Fatalf("BARE store: ReviewStatusIndexStore() = nil; *PebbleStore must implement it")
	}

	// What Start() installs when the Bleve index opens -- always, in production.
	srv.store = &indexedStore{Store: store, server: srv}

	if srv.FileProvenanceStore() == nil {
		t.Errorf("WRAPPED store: FileProvenanceStore() = nil -> maintenance.file-provenance-capture reports 'not initialized' in prod")
	}
	if srv.ReviewStatusIndexStore() == nil {
		t.Errorf("WRAPPED store: ReviewStatusIndexStore() = nil -> maintenance.review-status-index-repair reports 'not supported' in prod")
	}
}

// A store with no layer implementing the capability still yields nil rather
// than a panic or a stub, so the ops' "not supported" path stays reachable.
func TestMaintenanceStoreAccessorsNilWhenNoLayerImplements(t *testing.T) {
	srv := NewServer(&database.MockStore{})
	if got := srv.FileProvenanceStore(); got != nil {
		t.Errorf("FileProvenanceStore() on MockStore = %T, want nil", got)
	}
	if got := srv.ReviewStatusIndexStore(); got != nil {
		t.Errorf("ReviewStatusIndexStore() on MockStore = %T, want nil", got)
	}
	// And wrapping that store does not conjure one either.
	srv.store = &indexedStore{Store: &database.MockStore{}, server: srv}
	if got := srv.ReviewStatusIndexStore(); got != nil {
		t.Errorf("ReviewStatusIndexStore() on wrapped MockStore = %T, want nil", got)
	}
}
