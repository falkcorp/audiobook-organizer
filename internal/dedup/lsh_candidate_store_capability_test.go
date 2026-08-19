// file: internal/dedup/lsh_candidate_store_capability_test.go
// version: 1.0.0
// guid: 6c1d90b4-7a3e-4f52-9d18-2b8e5a0c4d71
// last-edited: 2026-08-19

package dedup

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// lshCapableStore is a stand-in for *database.PebbleStore: it carries the
// Pebble-only LookupAcoustIDCandidates alongside GetBookFileByID, and satisfies
// database.Store through an embedded nil interface (never called here).
type lshCapableStore struct {
	database.Store
	lookups int
}

func (s *lshCapableStore) LookupAcoustIDCandidates(_ []byte, _ int) ([]string, error) {
	s.lookups++
	return []string{"file-1"}, nil
}

func (s *lshCapableStore) GetBookFileByID(_, _ string) (*database.BookFile, error) {
	return &database.BookFile{ID: "file-1"}, nil
}

// lshDecorator reproduces internal/server.indexedStore: it decorates a store by
// EMBEDDING the database.Store interface, which promotes only that interface's
// method set. Every capability outside database.Store — LookupAcoustIDCandidates
// among them — is invisible through it, and it opts into being walked past by
// exposing Unwrap.
type lshDecorator struct {
	database.Store
	inner database.Store
}

func (d *lshDecorator) Unwrap() database.Store { return d.inner }

// plainStore is a backend with no LSH capability at all (SQLite, a mock) and no
// Unwrap. resolveLSHCandidateStore must report nil for it, which is what makes
// the nil check at the call site meaningful rather than dead.
type plainStore struct{ database.Store }

// TestResolveLSHCandidateStoreThroughDecorator pins the production shape of
// AcoustIDScan's Tier-0 candidate lookup.
//
// The engine is built by a lazy service-registry factory, so it receives the
// store AFTER Server.Start() installs the Bleve indexedStore wrap. The lookup
// used to be a bare inline assertion on de.bookStore, which fails through that
// wrap and left lshStore nil — silently dropping every scan onto the O(n)
// segment walk with no log line and no error. This test fails if that bare form
// comes back.
func TestResolveLSHCandidateStoreThroughDecorator(t *testing.T) {
	inner := &lshCapableStore{}
	var wrapped database.Store = &lshDecorator{Store: inner, inner: inner}

	// The bare form the call site used to use. This assertion FAILING is the
	// bug being guarded against, so assert it fails: if a future change makes
	// LookupAcoustIDCandidates part of database.Store, the decorator promotes
	// it, the hazard is gone, and this test should be revisited rather than
	// silently keep passing for the wrong reason.
	if _, ok := wrapped.(lshCandidateStore); ok {
		t.Fatal("bare assertion unexpectedly succeeded through the decorator; " +
			"database.Store now appears to carry LookupAcoustIDCandidates — " +
			"re-check whether resolveLSHCandidateStore is still needed")
	}

	got := resolveLSHCandidateStore(wrapped)
	if got == nil {
		t.Fatal("resolveLSHCandidateStore returned nil through the decorator; " +
			"AcoustIDScan would skip Tier-0 LSH candidate lookup in production " +
			"and fall back to the O(n) segment walk")
	}

	cands, err := got.LookupAcoustIDCandidates([]byte{1, 2, 3}, 200)
	if err != nil {
		t.Fatalf("LookupAcoustIDCandidates through the resolved store: %v", err)
	}
	if len(cands) != 1 || cands[0] != "file-1" {
		t.Fatalf("resolved store returned %v, want [file-1]", cands)
	}
	if inner.lookups != 1 {
		t.Fatalf("inner store saw %d lookups, want 1", inner.lookups)
	}
}

// TestResolveLSHCandidateStoreOnUncapableBackend proves the nil return is real
// and not an artifact of the resolver always succeeding.
func TestResolveLSHCandidateStoreOnUncapableBackend(t *testing.T) {
	if got := resolveLSHCandidateStore(&plainStore{}); got != nil {
		t.Fatalf("resolveLSHCandidateStore on a backend without the capability returned %T, want nil", got)
	}
	if got := resolveLSHCandidateStore(nil); got != nil {
		t.Fatalf("resolveLSHCandidateStore(nil) returned %T, want nil", got)
	}
}
