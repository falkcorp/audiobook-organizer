// file: internal/operations/registry/external_id_reassigner_capability_internal_test.go
// version: 1.0.0
// guid: edde5f04-bafb-4c86-bae9-d95e29a23481
// last-edited: 2026-09-12

package registry

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// TestProdSchedulerStore_ExternalIDReassignerResolves runs
// merge.AsExternalIDReassigner against a real production decorator.
//
// prodSchedulerStore embeds the NARROW opRegistryStore, which does not carry
// ReassignExternalIDs, and exposes the full store only through Unwrap. A bare
// `s.(merge.ExternalIDReassigner)` therefore fails through it, and merge's two
// MergeBooks call sites would silently skip iTunes PID/ASIN reassignment.
// Resolving through database.AsCapability walks Unwrap and finds it.
//
// internal/server.indexedStore is deliberately NOT the wrapper used here: it
// embeds database.Store, which already includes ReassignExternalIDs, so the
// bare assertion succeeds through it and it cannot exhibit this hazard.
func TestProdSchedulerStore_ExternalIDReassignerResolves(t *testing.T) {
	inner, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = inner.Close() })

	wrapped := &prodSchedulerStore{opRegistryStore: inner, full: inner}

	if _, ok := any(wrapped).(merge.ExternalIDReassigner); ok {
		t.Fatal("bare assertion unexpectedly succeeded through prodSchedulerStore; " +
			"opRegistryStore now appears to carry ReassignExternalIDs — re-check this test")
	}

	eid := merge.AsExternalIDReassigner(wrapped)
	if eid == nil {
		t.Fatal("merge.AsExternalIDReassigner returned nil through prodSchedulerStore; " +
			"a merge handed this store would drop external-ID reassignment silently")
	}

	// Round trip: the resolved capability must write through to the inner
	// store, not merely satisfy the type assertion.
	if err := inner.CreateExternalIDMapping(&database.ExternalIDMapping{
		Source: "itunes", ExternalID: "PID-1", BookID: "loser",
	}); err != nil {
		t.Fatalf("seed mapping: %v", err)
	}
	if err := eid.ReassignExternalIDs("loser", "winner"); err != nil {
		t.Fatalf("ReassignExternalIDs: %v", err)
	}
	got, err := inner.GetExternalIDsForBook("winner")
	if err != nil {
		t.Fatalf("GetExternalIDsForBook(winner): %v", err)
	}
	if len(got) != 1 || got[0].ExternalID != "PID-1" {
		t.Fatalf("expected PID-1 on winner after reassignment, got %+v", got)
	}
	left, err := inner.GetExternalIDsForBook("loser")
	if err != nil {
		t.Fatalf("GetExternalIDsForBook(loser): %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("expected no mappings left on loser, got %+v", left)
	}
}
