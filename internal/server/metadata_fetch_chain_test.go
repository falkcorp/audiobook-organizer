// file: internal/server/metadata_fetch_chain_test.go
// version: 1.0.0
// guid: 9c31f6a8-24de-4b70-a5e1-7d0f38b912c6
// last-edited: 2026-09-02

package server

import (
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// TestResolveSkipCached pins "absent means true". A bulk fetch that re-hits
// every provider for books whose cache is still fresh is the difference between
// "fetch what we're missing" and "re-fetch the entire library"; absent used to
// mean false, so the default dispatch did the latter.
func TestResolveSkipCached(t *testing.T) {
	tru, fls := true, false
	for _, tc := range []struct {
		name string
		in   *bool
		want bool
	}{
		{"absent defaults to skipping cached books", nil, true},
		{"explicit true", &tru, true},
		{"explicit false forces a full refresh", &fls, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := handlers.BulkMetadataFetchV2Params{SkipCached: tc.in}
			if got := p.ResolveSkipCached(); got != tc.want {
				t.Errorf("ResolveSkipCached() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestContinuationParamsAreByteDistinct guards the silent-failure mode that
// would end a chain while making it look complete.
//
// registry.EnqueueOp dedupes on BYTE equality of marshalled params against any
// queued-or-running op for the same def, returning the EXISTING op id rather
// than queueing. A successor whose params marshal identically to its still-
// running predecessor's is therefore dropped on the floor, the chain stops, and
// the operation reports success with books left unfetched.
//
// Continuation is the field that keeps them distinct, so this test asserts the
// bytes actually differ rather than trusting that they do.
func TestContinuationParamsAreByteDistinct(t *testing.T) {
	// Mid-chain is the dangerous case: the run key is already set, so
	// Continuation is the ONLY thing keeping the bytes distinct.
	cur := handlers.BulkMetadataFetchV2Params{RunKey: "01CHAIN", Continuation: 3}
	next := nextContinuationParams(cur, cur.RunKey)

	a, err := json.Marshal(cur)
	if err != nil {
		t.Fatalf("marshal current: %v", err)
	}
	b, err := json.Marshal(next)
	if err != nil {
		t.Fatalf("marshal successor: %v", err)
	}
	if string(a) == string(b) {
		t.Fatalf("successor params are byte-identical to the running op's (%s);"+
			" EnqueueOp would dedupe it away and the chain would stop silently", a)
	}

	// The run key must NOT change across the chain: it is the ledger key, so a
	// successor that mints a fresh one resumes nothing and refetches everything.
	if next.RunKey != cur.RunKey {
		t.Errorf("RunKey changed across the chain: %q -> %q", cur.RunKey, next.RunKey)
	}
}

// TestFreshDispatchOpensANewEpoch: an empty RunKey must not be treated as a
// chain continuation. Reusing a completed chain's ledger would make every later
// dispatch skip every book and report success -- a silent kill switch.
func TestFreshDispatchOpensANewEpoch(t *testing.T) {
	p := handlers.BulkMetadataFetchV2Params{}
	if p.RunKey != "" {
		t.Fatalf("a fresh dispatch should carry no run key, got %q", p.RunKey)
	}
	if p.Continuation != 0 {
		t.Errorf("a fresh dispatch should start at continuation 0, got %d", p.Continuation)
	}
}
