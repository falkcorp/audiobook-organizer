// file: internal/operations/registry/run_state_v3_resolve_test.go
// version: 1.0.0
// guid: 36be2604-c7f2-40f7-a227-ed84dcfb1b84
// last-edited: 2026-10-12

package registry_test

// The registry's v3 run state is not part of database.Store, so it is
// resolved from the store New is given as a capability. In production that
// store is the server's search-index decorator, which embeds database.Store
// and so promotes none of the v3 methods: without the Unwrap walk the
// registry would silently run every v3 path as "no record". These tests pin
// that walk, and its negative control.

import (
	"log/slog"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// unwrappingDecorator mirrors server.indexedStore's shape: it embeds
// database.Store (so only Store's method set is promoted) and opts in to
// capability lookups through Unwrap.
type unwrappingDecorator struct {
	database.Store
}

func (d unwrappingDecorator) Unwrap() database.Store { return d.Store }

// opaqueDecorator embeds database.Store and does NOT expose Unwrap.
type opaqueDecorator struct {
	database.Store
}

func TestNew_ResolvesRunStateV3ThroughAnUnwrappingDecorator(t *testing.T) {
	store := newV3Store(t)
	r := registry.New(unwrappingDecorator{Store: store}, slog.Default(), 1, nil)
	if !r.HasRunStateV3ForTest() {
		t.Fatal("registry did not resolve the v3 run state through a decorator that exposes Unwrap; " +
			"in production every v3 path (stopping, boot sweep, requeue successor) would act as if no run had a record")
	}
}

func TestNew_ResolvesRunStateV3FromABareStore(t *testing.T) {
	r := registry.New(newV3Store(t), slog.Default(), 1, nil)
	if !r.HasRunStateV3ForTest() {
		t.Fatal("registry did not resolve the v3 run state from a bare *PebbleStore")
	}
}

func TestNew_NoRunStateV3BehindAnOpaqueDecorator(t *testing.T) {
	// Negative control: the decorator hides the v3 methods and offers no
	// Unwrap, so there is nothing to resolve. If this ever passes with a
	// resolved state, the two tests above prove nothing about the walk.
	r := registry.New(opaqueDecorator{Store: newV3Store(t)}, slog.Default(), 1, nil)
	if r.HasRunStateV3ForTest() {
		t.Fatal("registry resolved v3 run state through a decorator that neither carries it nor unwraps; " +
			"database.Store must not carry the v3 surface (TestStoreFlattenedWidth)")
	}
}
