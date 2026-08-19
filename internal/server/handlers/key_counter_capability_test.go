// file: internal/server/handlers/key_counter_capability_test.go
// version: 1.0.0
// guid: 8c31f7a4-5d92-4e08-b613-9a7c2f4d6e51
// last-edited: 2026-08-19

package handlers

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// keyCountingStore carries the capability; keyCountDecorator embeds the
// database.Store INTERFACE and unwraps, exactly as internal/server's
// indexedStore does in production.
type keyCountingStore struct {
	database.Store
}

func (keyCountingStore) KeyCount() (int64, uint64, error) { return 7, 11, nil }

type keyCountDecorator struct {
	database.Store
	inner database.Store
}

func (d keyCountDecorator) Unwrap() database.Store { return d.inner }

// TestResolveKeyCounterThroughDecorator pins db-health's Pebble section to the
// production store shape. KeyCount is not on database.Store (compile-probed),
// so a bare assertion returns nil through the decorator and the endpoint
// silently reports a healthy store with no Pebble section.
func TestResolveKeyCounterThroughDecorator(t *testing.T) {
	inner := keyCountingStore{}
	wrapped := keyCountDecorator{inner: inner}

	got := resolveKeyCounter(wrapped)
	if got == nil {
		t.Fatal("resolveKeyCounter returned nil through the decorator; db-health would drop resp.Pebble in production")
	}
	n, size, err := got.KeyCount()
	if err != nil || n != 7 || size != 11 {
		t.Fatalf("KeyCount() = (%d, %d, %v), want (7, 11, nil)", n, size, err)
	}
}

// TestResolveKeyCounterOnUncapableBackend stops the test above from passing
// against a resolver that returns non-nil unconditionally.
func TestResolveKeyCounterOnUncapableBackend(t *testing.T) {
	type plain struct{ database.Store }
	if got := resolveKeyCounter(plain{}); got != nil {
		t.Fatalf("resolveKeyCounter = %v on a store without KeyCount, want nil", got)
	}
}
