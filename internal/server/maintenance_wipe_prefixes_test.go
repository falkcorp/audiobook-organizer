// file: internal/server/maintenance_wipe_prefixes_test.go
// version: 1.0.0
// guid: 7b2d4f68-3e91-4a57-b0c8-1d6a9e3f5c02
// last-edited: 2026-08-19

package server

import (
	"testing"
)

// The wipe* helpers are the destructive end of POST /maintenance/wipe: each one
// deletes a whole keyspace by raw Pebble prefix. Until now nothing asserted
// WHICH prefixes they pass — only that they could resolve a prefixWiper at all
// (prefix_wiper_capability_test.go). A typo'd or dropped prefix here deletes the
// wrong keyspace, or silently leaves a secondary index behind, and every
// existing test would still pass.
//
// These use the generated mockPrefixWiper and assert the exact argument. Never
// mock.Anything: the argument IS the thing under test.
//
// Note the division of labour with prefix_wiper_capability_test.go. That file
// proves the REAL store resolves through the indexedStore decorator, which a
// mock cannot test (a mock satisfies prefixWiper by construction). This file
// proves the helpers pass the right prefixes once resolution has succeeded.
// Neither test subsumes the other.

// wipeStoreStub is a maintenanceStore that also carries the prefix capability,
// so resolvePrefixWiper finds the mock. The embedded maintenanceStore is nil:
// the non-dry-run paths exercised here never touch the Count* methods.
type wipeStoreStub struct {
	maintenanceStore
	*mockPrefixWiper
}

func TestWipeHelpersPassExactPrefixes(t *testing.T) {
	tests := []struct {
		name     string
		fn       func(maintenanceStore, bool) (int64, error)
		want     []string
		wantRows int64
	}{
		// book_file: only. The secondary-index prefixes for files are covered by
		// the "bf:"/"bfs:" pair in wipeSegments, not here.
		{"wipeBookFiles", wipeBookFiles, []string{"book_file:"}, 12},
		// BOTH halves matter: "bf:" is the primary segment record and "bfs:" the
		// secondary index. Dropping "bfs:" leaves orphaned index entries that
		// point at deleted segments.
		{"wipeSegments", wipeSegments, []string{"bf:", "bfs:"}, 7},
		{"wipeBooks", wipeBooks, []string{"book:"}, 44},
		{"wipeAuthors", wipeAuthors, []string{"author:"}, 5},
		{"wipeSeries", wipeSeries, []string{"series:"}, 3},
		// "ext_id:" deliberately covers both "ext_id:<source>:<id>" and
		// "ext_id:book:<bookID>:<source>:<id>" — one prefix, two key shapes.
		{"wipeExternalIDs", wipeExternalIDs, []string{"ext_id:"}, 9},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMockPrefixWiper(t)
			m.EXPECT().WipeByPrefixes(tt.want).Return(int(tt.wantRows), nil).Once()

			got, err := tt.fn(wipeStoreStub{mockPrefixWiper: m}, false)
			if err != nil {
				t.Fatalf("%s returned err %v", tt.name, err)
			}
			if got != tt.wantRows {
				t.Errorf("%s returned %d rows, want %d", tt.name, got, tt.wantRows)
			}
		})
	}
}

// TestWipeSegmentsDryRunCountsPrimaryPrefixOnly pins the asymmetry: the dry-run
// COUNT uses only "bf:" while the real wipe uses "bf:" AND "bfs:". That is
// deliberate — counting the secondary index too would double-report — but it
// means the dry-run number is not the number of keys the wipe will remove, and
// nothing said so before.
func TestWipeSegmentsDryRunCountsPrimaryPrefixOnly(t *testing.T) {
	m := newMockPrefixWiper(t)
	m.EXPECT().CountByPrefix("bf:").Return(7, nil).Once()

	got, err := wipeSegments(wipeStoreStub{mockPrefixWiper: m}, true)
	if err != nil {
		t.Fatalf("wipeSegments dry-run returned err %v", err)
	}
	if got != 7 {
		t.Errorf("wipeSegments dry-run = %d, want 7", got)
	}
}

// TestWipeExternalIDsDryRunUsesSamePrefix confirms the dry-run and the real wipe
// agree here, unlike wipeSegments above.
func TestWipeExternalIDsDryRunUsesSamePrefix(t *testing.T) {
	m := newMockPrefixWiper(t)
	m.EXPECT().CountByPrefix("ext_id:").Return(9, nil).Once()

	got, err := wipeExternalIDs(wipeStoreStub{mockPrefixWiper: m}, true)
	if err != nil {
		t.Fatalf("wipeExternalIDs dry-run returned err %v", err)
	}
	if got != 9 {
		t.Errorf("wipeExternalIDs dry-run = %d, want 9", got)
	}
}
