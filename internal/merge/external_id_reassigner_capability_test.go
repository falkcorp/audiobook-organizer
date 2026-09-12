// file: internal/merge/external_id_reassigner_capability_test.go
// version: 1.0.0
// guid: 796c0588-3a53-4897-b715-bd745e0714c0
// last-edited: 2026-09-12

package merge

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// narrowMergeDecorator decorates a store by EMBEDDING the narrow merge.Store
// interface — the same shape as internal/operations/registry.prodSchedulerStore,
// which embeds a narrow ops interface and exposes the full store only through
// Unwrap. Embedding promotes only merge.Store's method set, and merge.Store does
// not carry ReassignExternalIDs, so the capability is invisible through this
// wrapper to a bare type assertion.
//
// Why not a double shaped like internal/server.indexedStore (embedding
// database.Store)? ReassignExternalIDs IS part of database.Store (via
// enrichmentStore -> ExternalIDStore -> ExternalIDLifecycle), so an
// indexedStore-shaped wrapper promotes it and the bare assertion succeeds
// through it. That double would pass against the old code and prove nothing.
type narrowMergeDecorator struct {
	Store
	inner database.Store
}

func (d narrowMergeDecorator) Unwrap() database.Store { return d.inner }

// opaqueMergeDecorator is the same narrow wrap WITHOUT Unwrap: a decorator that
// has not opted into being walked past. AsCapability treats it as opaque on
// purpose, so the result must stay nil — exactly what the bare assertion gave.
type opaqueMergeDecorator struct {
	Store
}

// TestAsExternalIDReassigner_ResolvesThroughDecorator pins that a merge running
// against a decorator-wrapped store still reaches ReassignExternalIDs. With the
// old bare `s.(ExternalIDReassigner)` this returned nil, and both MergeBooks call
// sites treat nil as "backend has no external IDs" — the loser book's iTunes
// PID/ASIN mappings would be silently left behind on a successful merge.
func TestAsExternalIDReassigner_ResolvesThroughDecorator(t *testing.T) {
	var gotOld, gotNew string
	inner := &database.MockStore{
		ReassignExternalIDsFunc: func(oldBookID, newBookID string) error {
			gotOld, gotNew = oldBookID, newBookID
			return nil
		},
	}
	wrapped := narrowMergeDecorator{Store: inner, inner: inner}

	// The hazard this test guards against: the bare form fails through the
	// wrapper. If a future change adds ReassignExternalIDs to merge.Store, the
	// wrapper promotes it, the hazard is gone, and this test should be
	// revisited rather than keep passing for the wrong reason.
	if _, ok := any(wrapped).(ExternalIDReassigner); ok {
		t.Fatal("bare assertion unexpectedly succeeded through the narrow decorator; " +
			"merge.Store now appears to carry ReassignExternalIDs — re-check this test")
	}

	eid := AsExternalIDReassigner(wrapped)
	if eid == nil {
		t.Fatal("AsExternalIDReassigner returned nil through an Unwrap-capable decorator; " +
			"MergeBooks would skip external-ID reassignment without an error")
	}
	if err := eid.ReassignExternalIDs("loser", "winner"); err != nil {
		t.Fatalf("ReassignExternalIDs: %v", err)
	}
	if gotOld != "loser" || gotNew != "winner" {
		t.Fatalf("resolved capability did not reach the inner store: got (%q, %q)", gotOld, gotNew)
	}
}

// TestAsExternalIDReassigner_DirectStore is the anti-regression for the
// undecorated path: a store that implements the capability itself resolves on
// the first AsCapability iteration, same as the bare assertion did.
func TestAsExternalIDReassigner_DirectStore(t *testing.T) {
	wantErr := errors.New("sentinel")
	inner := &database.MockStore{
		ReassignExternalIDsFunc: func(_, _ string) error { return wantErr },
	}
	eid := AsExternalIDReassigner(inner)
	if eid == nil {
		t.Fatal("AsExternalIDReassigner returned nil for a store implementing the capability directly")
	}
	if err := eid.ReassignExternalIDs("a", "b"); !errors.Is(err, wantErr) {
		t.Fatalf("expected the inner store's error to surface, got %v", err)
	}
}

// TestAsExternalIDReassigner_AbsentCapabilityUnchanged pins the fallback: when no
// layer offers the capability — an opaque decorator, or an Unwrap chain whose
// inner value is nil — the result is nil, the same as the old bare assertion.
// The existing TestB3_AsExternalIDReassigner_Nil and _NotImplementing cover the
// nil argument and a plain non-implementing value.
func TestAsExternalIDReassigner_AbsentCapabilityUnchanged(t *testing.T) {
	inner := &database.MockStore{}
	if eid := AsExternalIDReassigner(opaqueMergeDecorator{Store: inner}); eid != nil {
		t.Fatal("opaque decorator (no Unwrap) must stay opaque: expected nil")
	}
	if eid := AsExternalIDReassigner(narrowMergeDecorator{Store: inner, inner: nil}); eid != nil {
		t.Fatal("decorator unwrapping to nil must resolve to nil")
	}
}
