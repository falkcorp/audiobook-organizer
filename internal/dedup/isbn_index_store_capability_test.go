// file: internal/dedup/isbn_index_store_capability_test.go
// version: 1.0.0
// guid: 9e58c3b7-1f42-4a06-85d3-7c2e4a9b6f30
// last-edited: 2026-08-19

package dedup

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type isbnCapableStore struct {
	database.Store
}

func (isbnCapableStore) IsISBNIndexBuilt() bool { return true }
func (isbnCapableStore) GetBookIDsByISBNASIN(_, _, _ string) ([]string, error) {
	return []string{"b1"}, nil
}

type isbnDecorator struct {
	database.Store
	inner database.Store
}

func (d isbnDecorator) Unwrap() database.Store { return d.inner }

// TestResolveISBNIndexStoreThroughDecorator pins the MIXED-reachability case.
//
// Compile-probed 2026-08-19: GetBookIDsByISBNASIN IS on database.Store,
// IsISBNIndexBuilt is NOT. A composite takes the worse reachability of its
// members, so the pair fails through a decorator even though half of it would
// resolve — the exact trap that made this sweep worth doing. Without the
// resolver, checkExactISBN silently keeps the O(n) GetAllBooks path forever.
func TestResolveISBNIndexStoreThroughDecorator(t *testing.T) {
	got := resolveISBNIndexStore(isbnDecorator{inner: isbnCapableStore{}})
	if got == nil {
		t.Fatal("resolveISBNIndexStore returned nil through the decorator; checkExactISBN would stay on the O(n) scan")
	}
	if !got.IsISBNIndexBuilt() {
		t.Fatal("IsISBNIndexBuilt() = false through the decorator, want true")
	}
}

// TestResolveISBNIndexStoreOnHalfCapableBackend is the discriminating case for a
// mixed composite: a store carrying ONLY the database.Store half must still
// resolve to nil, because the indexed path needs both.
func TestResolveISBNIndexStoreOnHalfCapableBackend(t *testing.T) {
	type halfOnly struct{ database.Store } // has GetBookIDsByISBNASIN, lacks IsISBNIndexBuilt
	if got := resolveISBNIndexStore(halfOnly{}); got != nil {
		t.Fatalf("resolveISBNIndexStore = %v with only half the pair, want nil", got)
	}
}
