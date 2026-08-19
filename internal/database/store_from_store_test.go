// file: internal/database/store_from_store_test.go
// version: 1.0.0
// guid: 1c4e7b28-6d93-4051-8f2a-9b6c3e5d7a14
// last-edited: 2026-08-19

package database

import "testing"

// fromStoreDecorator embeds the Store INTERFACE and unwraps, exactly as
// internal/server's indexedStore does in production.
type fromStoreDecorator struct {
	Store
	inner Store
}

func (d fromStoreDecorator) Unwrap() Store { return d.inner }

// TestNewFromStoreConstructorsResolveThroughDecorator is the reason these
// constructors exist: five packages used to do AsPebbleStore + DB() themselves,
// and a bare assertion at any of those sites failed through the decorator and
// silently disabled embeddings / metrics / AI scan / the activity log.
func TestNewFromStoreConstructorsResolveThroughDecorator(t *testing.T) {
	inner, err := NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = inner.Close() })

	var wrapped Store = fromStoreDecorator{inner: inner}

	if got := NewEmbeddingStoreFromStore(wrapped); got == nil {
		t.Error("NewEmbeddingStoreFromStore returned nil through the decorator")
	}
	if got := NewPebbleMetricsStoreFromStore(wrapped); got == nil {
		t.Error("NewPebbleMetricsStoreFromStore returned nil through the decorator")
	}
	if got := NewPebbleActivityStoreFromStore(wrapped); got == nil {
		t.Error("NewPebbleActivityStoreFromStore returned nil through the decorator")
	}
	got, err := NewAIScanStoreFromStore(wrapped)
	if err != nil {
		t.Errorf("NewAIScanStoreFromStore through the decorator: %v", err)
	}
	if got == nil {
		t.Error("NewAIScanStoreFromStore returned nil through the decorator")
	}
}

// TestNewFromStoreConstructorsOnUncapableBackend keeps the test above from
// passing against constructors that ignore their argument. A non-Pebble backend
// must yield nil, which is what every call site's nil-check depends on.
func TestNewFromStoreConstructorsOnUncapableBackend(t *testing.T) {
	type plain struct{ Store }
	p := plain{}

	if got := NewEmbeddingStoreFromStore(p); got != nil {
		t.Errorf("NewEmbeddingStoreFromStore = %v on a non-Pebble backend, want nil", got)
	}
	if got := NewPebbleMetricsStoreFromStore(p); got != nil {
		t.Errorf("NewPebbleMetricsStoreFromStore = %v on a non-Pebble backend, want nil", got)
	}
	if got := NewPebbleActivityStoreFromStore(p); got != nil {
		t.Errorf("NewPebbleActivityStoreFromStore = %v on a non-Pebble backend, want nil", got)
	}
	got, err := NewAIScanStoreFromStore(p)
	if err != nil {
		t.Errorf("NewAIScanStoreFromStore on a non-Pebble backend returned err %v, want nil", err)
	}
	if got != nil {
		t.Errorf("NewAIScanStoreFromStore = %v on a non-Pebble backend, want nil", got)
	}
}
