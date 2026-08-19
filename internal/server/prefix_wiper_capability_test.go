// file: internal/server/prefix_wiper_capability_test.go
// version: 1.0.0
// guid: 4b8d1e6a-93c7-4f20-8a15-6c2e9d7b4f03
// last-edited: 2026-08-19

package server

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestResolvePrefixWiperThroughDecorator pins the /maintenance/wipe helpers to
// the real production store shape.
//
// The wipe* helpers take their store from Server.Store() at REQUEST time, which
// on the production host is &indexedStore{Store: inner} — installed by
// Server.Start whenever Bleve opened successfully. indexedStore embeds the
// database.Store INTERFACE, and neither WipeByPrefixes nor CountByPrefix is
// declared on database.Store (compile-probed 2026-08-19), so a bare
// store.(*database.PebbleStore) assertion returns nil through it and every wipe
// target silently takes its non-Pebble branch.
//
// Reverting resolvePrefixWiper to a bare assertion turns this test red, which is
// the point of naming the resolver instead of inlining it.
func TestResolvePrefixWiperThroughDecorator(t *testing.T) {
	inner, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = inner.Close() })

	// The real decorator, constructed exactly as server_lifecycle.go does.
	// server is nil: this test performs no book mutation, so the index-enqueue
	// path is never reached.
	var wrapped database.Store = &indexedStore{Store: inner, server: nil}

	if got := resolvePrefixWiper(wrapped); got == nil {
		t.Fatal("resolvePrefixWiper returned nil through the indexedStore decorator; " +
			"the /maintenance/wipe helpers would all take their non-Pebble fallback in production")
	}

	// It must resolve on the bare store too — that is what NewServer holds
	// before Start installs the decorator.
	if got := resolvePrefixWiper(database.Store(inner)); got == nil {
		t.Fatal("resolvePrefixWiper returned nil on the bare store")
	}
}

// prefixlessStore is a database.Store that does NOT carry the prefix
// capability, standing in for a non-Pebble backend or a test double.
type prefixlessStore struct {
	database.Store
}

// TestResolvePrefixWiperOnUncapableBackend proves the resolver reports absence
// rather than panicking, so each wipe helper keeps its existing fallback branch.
//
// Without this the decorator test above would pass against a resolver that
// simply returned a non-nil value unconditionally.
func TestResolvePrefixWiperOnUncapableBackend(t *testing.T) {
	if got := resolvePrefixWiper(&prefixlessStore{}); got != nil {
		t.Fatalf("resolvePrefixWiper = %v on a store without the capability, want nil", got)
	}
}
