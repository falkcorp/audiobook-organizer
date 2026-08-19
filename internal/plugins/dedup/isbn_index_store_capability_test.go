// file: internal/plugins/dedup/isbn_index_store_capability_test.go
// version: 1.0.0
// guid: e3935a93-0a19-412c-b62c-2acb6c2a7fc4
// last-edited: 2026-08-19

// Decorator tests for resolveISBNIndexStore (dedup.build-isbn-index).
//
// The op previously asserted p.store.(ISBNIndexStore) directly. That succeeds
// against the bare *database.PebbleStore the service registry holds today, so
// these tests do not pin a live bug — they pin the decorator case that a bare
// assertion gets wrong, which is what the op is one wrapper away from meeting.

package dedup

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// pluginISBNCapableStore is a database.Store that also carries all three
// ISBNIndexStore methods — i.e. what *database.PebbleStore looks like to this
// op. The embedded interface is nil, so any call outside the three below
// panics loudly rather than returning a plausible zero value.
type pluginISBNCapableStore struct {
	database.Store
}

func (pluginISBNCapableStore) WriteISBNIndexForBook(_, _, _, _ string) error { return nil }
func (pluginISBNCapableStore) IsISBNIndexBuilt() bool                        { return true }
func (pluginISBNCapableStore) SetISBNIndexBuilt() error                      { return nil }

// pluginISBNDecorator embeds the database.Store interface and exposes the
// wrapped store via Unwrap — the same shape as internal/server's indexedStore.
// It satisfies database.Store (and so pluginStore) while hiding the concrete
// type underneath.
type pluginISBNDecorator struct {
	database.Store
	inner database.Store
}

func (d pluginISBNDecorator) Unwrap() database.Store { return d.inner }

// TestResolveISBNIndexStoreThroughDecorator is the case a bare type assertion
// fails. All three methods are absent from database.Store (compile-probed
// individually 2026-08-19), so the decorator's own method set cannot satisfy
// ISBNIndexStore; only walking Unwrap reaches the capable store beneath.
func TestResolveISBNIndexStoreThroughDecorator(t *testing.T) {
	got := resolveISBNIndexStore(pluginISBNDecorator{inner: pluginISBNCapableStore{}})
	if got == nil {
		t.Fatal("resolveISBNIndexStore returned nil through the decorator; " +
			"dedup.build-isbn-index would refuse to run with a capable store underneath")
	}
	if !got.IsISBNIndexBuilt() {
		t.Fatal("IsISBNIndexBuilt() = false through the decorator, want true")
	}
	if err := got.WriteISBNIndexForBook("b1", "", "", ""); err != nil {
		t.Fatalf("WriteISBNIndexForBook through the decorator: %v", err)
	}
	if err := got.SetISBNIndexBuilt(); err != nil {
		t.Fatalf("SetISBNIndexBuilt through the decorator: %v", err)
	}
}

// TestResolveISBNIndexStoreUndecorated guards the path that actually runs in
// production today: the registry holds the bare store, undecorated. A resolver
// that only handled the wrapped case would break the live op.
func TestResolveISBNIndexStoreUndecorated(t *testing.T) {
	if got := resolveISBNIndexStore(pluginISBNCapableStore{}); got == nil {
		t.Fatal("resolveISBNIndexStore returned nil for an undecorated capable store")
	}
}

// TestResolveISBNIndexStoreOnUncapableBackend pins the error path. A plain
// database.Store carries ~398 methods and none of these three, so the op must
// resolve nil and fail loudly rather than proceed against a store that cannot
// write index rows.
//
// Unlike the mixed composite in internal/dedup, there is no "half capable"
// case to discriminate here: all three methods are uniformly absent from
// database.Store, so partial satisfaction is not reachable through this type.
func TestResolveISBNIndexStoreOnUncapableBackend(t *testing.T) {
	type plainStore struct{ database.Store }
	if got := resolveISBNIndexStore(plainStore{}); got != nil {
		t.Fatalf("resolveISBNIndexStore = %v on a store lacking all three methods, want nil", got)
	}
	if got := resolveISBNIndexStore(pluginISBNDecorator{inner: plainStore{}}); got != nil {
		t.Fatalf("resolveISBNIndexStore = %v through a decorator over an uncapable store, want nil", got)
	}
}
