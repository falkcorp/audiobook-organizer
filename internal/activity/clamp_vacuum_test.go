// file: internal/activity/clamp_vacuum_test.go
// version: 1.0.0
// guid: 6a3e1c7d-90b4-4f28-8d51-2b7c40e9af63
// last-edited: 2026-09-08

package activity

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// An explicit vacuum request must be honoured even when the clamp pass found
// nothing left to clamp.
//
// This is the case the old `res.Clamped > 0` guard made unreachable, and it is
// not hypothetical: on 2026-09-08 the first production clamp reclaimed 10.66 GB
// from the main file and left 11,514,065,152 bytes held by the -wal. Every
// subsequent call then found zero oversized rows, skipped the vacuum on that
// guard, and returned "success" while reclaiming nothing — so no request could
// reach the code that would have released the space. Restarting does not help
// either: SQLite deletes the -wal only on a clean last-connection close.
func TestClampSummaries_VacuumRunsEvenWhenNothingWasClamped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.sqlite")
	store, err := database.OpenSQLiteActivityStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := NewService(store)

	// Build a WAL with ordinary writes. These go through Record, so they are
	// already clamped — which is exactly the point: the table ends up with zero
	// oversized rows, the state the guard used to treat as "nothing to do".
	for i := range 400 {
		if _, err := store.Record(database.ActivityEntry{
			Timestamp: time.Now(),
			Tier:      "info",
			Type:      "system",
			Level:     "info",
			Source:    "test",
			Summary:   fmt.Sprintf("%d %s", i, strings.Repeat("padding ", 64)),
		}); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}

	walPath := path + "-wal"
	before, err := os.Stat(walPath)
	if err != nil || before.Size() == 0 {
		t.Skipf("no WAL to reclaim at %s (%v); nothing to assert", walPath, err)
	}

	res, err := svc.ClampSummaries(context.Background(), 0, false, true)
	if err != nil {
		t.Fatalf("ClampSummaries: %v", err)
	}
	if res.Clamped != 0 {
		t.Fatalf("fixture wrote oversized rows (clamped=%d); this test must exercise "+
			"the zero-clamped path", res.Clamped)
	}

	after, err := os.Stat(walPath)
	if err != nil {
		return // truncated away entirely is a fine outcome
	}
	if after.Size() >= before.Size() {
		t.Errorf("vacuum did not run on an explicit request with 0 rows clamped: "+
			"WAL %d bytes before, %d after. `vacuum` defaults to false, so passing it "+
			"is an explicit request and must not be conditioned on Clamped > 0 — that "+
			"guard makes reclaiming an already-clamped database impossible",
			before.Size(), after.Size())
	}
}
