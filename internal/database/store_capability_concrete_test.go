// file: internal/database/store_capability_concrete_test.go
// version: 1.0.0
// guid: 3f7a1e28-9c44-4b16-8de3-5a02c7f9b418
// last-edited: 2026-07-31

package database

import "testing"

// concreteDecorator mirrors the shape of internal/server.indexedStore: it embeds
// the Store INTERFACE (so it promotes only Store's method set and hides
// everything else, including the concrete inner type) and opts into unwrapping.
type concreteDecorator struct {
	Store
}

func (d *concreteDecorator) Unwrap() Store { return d.Store }

// opaqueDecorator embeds Store but deliberately does NOT implement
// StoreUnwrapper, standing in for a wrapper whose behaviour must not be bypassed.
type opaqueDecorator struct {
	Store
}

// TestAsPebbleStoreThroughDecorator is the regression test for the bug this file
// exists to prevent: a bare store.(*PebbleStore) assertion returns nil through a
// decorator, and every caller of that assertion has a "non-Pebble backend"
// fallback that makes the failure look like a supported configuration.
func TestAsPebbleStoreThroughDecorator(t *testing.T) {
	inner := &PebbleStore{}

	t.Run("bare store resolves", func(t *testing.T) {
		if got := AsPebbleStore(inner); got != inner {
			t.Fatalf("AsPebbleStore(bare) = %p, want %p", got, inner)
		}
	})

	t.Run("bare assertion fails through decorator", func(t *testing.T) {
		// This is the bug, asserted directly so the test documents WHY
		// AsPebbleStore has to exist. If this ever starts passing, Go's embedding
		// semantics changed and the helper can go away.
		var wrapped Store = &concreteDecorator{Store: inner}
		if _, ok := wrapped.(*PebbleStore); ok {
			t.Fatal("bare type assertion unexpectedly saw through the decorator; " +
				"AsPebbleStore may no longer be necessary")
		}
	})

	t.Run("AsPebbleStore resolves through one decorator", func(t *testing.T) {
		var wrapped Store = &concreteDecorator{Store: inner}
		if got := AsPebbleStore(wrapped); got != inner {
			t.Fatalf("AsPebbleStore(1 decorator) = %p, want %p", got, inner)
		}
	})

	t.Run("AsPebbleStore resolves through nested decorators", func(t *testing.T) {
		var wrapped Store = &concreteDecorator{
			Store: &concreteDecorator{Store: inner},
		}
		if got := AsPebbleStore(wrapped); got != inner {
			t.Fatalf("AsPebbleStore(2 decorators) = %p, want %p", got, inner)
		}
	})

	t.Run("opaque decorator is not bypassed", func(t *testing.T) {
		// A wrapper that has not opted in stays opaque on purpose: reaching past
		// an access-control or read-only wrapper would write through it.
		var wrapped Store = &opaqueDecorator{Store: inner}
		if got := AsPebbleStore(wrapped); got != nil {
			t.Fatalf("AsPebbleStore(opaque) = %p, want nil", got)
		}
	})

	t.Run("nil input", func(t *testing.T) {
		if got := AsPebbleStore(nil); got != nil {
			t.Fatalf("AsPebbleStore(nil) = %p, want nil", got)
		}
	})

	t.Run("genuinely non-Pebble store yields nil", func(t *testing.T) {
		// The legitimate case the callers' fallbacks were written for. It must
		// still report nil, or a SQLite/mock backend would be handed a nil
		// *PebbleStore and panic on first use.
		if got := AsPebbleStore(struct{}{}); got != nil {
			t.Fatalf("AsPebbleStore(non-store) = %p, want nil", got)
		}
	})
}

// TestAsCapabilityDepthCapTerminates covers the behaviour change made when
// GetOpsV2/GetAIJobs were folded into AsCapability: their hand-rolled loops had no
// depth bound, so a decorator whose Unwrap returned itself spun forever. This test
// hangs (and fails on the package timeout) if maxUnwrapDepth is removed.
//
// It probes with *PebbleStore rather than OpsV2Store deliberately — see
// TestCapabilityFamiliesDifferInVisibility for why an OpsV2Store probe would
// resolve on the first layer and never reach the cycle at all.
func TestAsCapabilityDepthCapTerminates(t *testing.T) {
	c := &selfCycleDecorator{}
	c.self = c

	if _, ok := AsCapability[*PebbleStore](c); ok {
		t.Fatal("AsCapability resolved a *PebbleStore from a cyclic decorator")
	}
}

// TestCapabilityFamiliesDifferInVisibility pins down a distinction that is easy to
// get wrong and that determines whether a given capability is at risk from a
// Store-embedding decorator at all.
//
// A capability whose method set is a SUBSET of Store is satisfied by any decorator
// that embeds the Store interface, because embedding promotes Store's whole method
// set. Such capabilities were never broken by indexedStore — which is exactly why
// GetOpsV2/GetAIJobs worked in production while AsSyncIdentityStore did not, even
// though all four look like the same pattern in the source.
//
// A capability with even one method outside Store is invisible through the
// decorator and MUST be resolved via AsCapability. If a future edit moves a
// capability's methods into Store (or removes them), this test tells you which
// side of the line it has landed on rather than leaving it to be discovered in
// production.
func TestCapabilityFamiliesDifferInVisibility(t *testing.T) {
	inner := &PebbleStore{}
	var wrapped Store = &concreteDecorator{Store: inner}

	// Subset-of-Store family: visible directly through the decorator.
	if _, ok := wrapped.(OpsV2Store); !ok {
		t.Error("OpsV2Store is no longer a subset of Store; it has joined the " +
			"at-risk family and every bare assertion on it needs auditing")
	}
	if _, ok := wrapped.(AIJobsStore); !ok {
		t.Error("AIJobsStore is no longer a subset of Store; it has joined the " +
			"at-risk family and every bare assertion on it needs auditing")
	}

	// At-risk family: invisible through the decorator, resolvable only by walking.
	if _, ok := wrapped.(SyncIdentityStore); ok {
		t.Error("SyncIdentityStore is now visible through a Store-embedding " +
			"decorator; it may have been folded into Store")
	}
	if AsSyncIdentityStore(wrapped) == nil {
		t.Error("AsSyncIdentityStore failed to resolve through the decorator")
	}
	if AsSyncFileStore(wrapped) == nil {
		t.Error("AsSyncFileStore failed to resolve through the decorator")
	}
	if AsBookmarkStore(wrapped) == nil {
		t.Error("AsBookmarkStore failed to resolve through the decorator")
	}

	// Both helpers must still resolve through the decorator regardless of which
	// family they belong to — consolidating them onto AsCapability must not have
	// changed their observable behaviour.
	if GetOpsV2(wrapped) == nil {
		t.Error("GetOpsV2 failed to resolve through the decorator")
	}
	if GetAIJobs(wrapped) == nil {
		t.Error("GetAIJobs failed to resolve through the decorator")
	}
}

// selfCycleDecorator returns itself from Unwrap, the degenerate cycle the old
// unbounded loops could not escape.
type selfCycleDecorator struct {
	Store
	self Store
}

func (d *selfCycleDecorator) Unwrap() Store { return d.self }
