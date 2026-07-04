// file: internal/server/server_lifecycle_countgate_test.go
// version: 1.0.0
// guid: 9f2c1a7e-4b8d-4c1a-9e3f-2d6b7a0c5e11
// last-edited: 2026-07-03

package server

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestCountLegacyV1Ops verifies the SYS-4 observability gate: countLegacyV1Ops
// counts only operations whose Type is a pre-UOS v1 legacy name still handled
// by resumeLegacyOp, and ignores v2/registered/namespaced types.
func TestCountLegacyV1Ops(t *testing.T) {
	ops := []database.Operation{
		{ID: "1", Type: "itunes_import"},      // legacy v1
		{ID: "2", Type: "reconcile_scan"},     // legacy v1
		{ID: "3", Type: "bulk_write_back"},    // legacy v1
		{ID: "4", Type: "library.scan"},       // v2 registered — not counted
		{ID: "5", Type: "maintenance:vacuum"}, // namespaced default branch — not counted
		{ID: "6", Type: ""},                   // empty — not counted
	}

	if got, want := countLegacyV1Ops(ops), 3; got != want {
		t.Fatalf("countLegacyV1Ops = %d, want %d", got, want)
	}

	if got := countLegacyV1Ops(nil); got != 0 {
		t.Fatalf("countLegacyV1Ops(nil) = %d, want 0", got)
	}

	// Every name in the legacyV1OpTypes set must be counted.
	for typ := range legacyV1OpTypes {
		if got := countLegacyV1Ops([]database.Operation{{Type: typ}}); got != 1 {
			t.Errorf("countLegacyV1Ops for legacy type %q = %d, want 1", typ, got)
		}
	}
}
