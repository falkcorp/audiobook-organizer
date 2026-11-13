// file: core/internal/logjournal/ulid_internal_test.go
// version: 1.0.0
// guid: 2f8d6a40-b3e9-4c17-95a2-7d04e1c8b653
// last-edited: 2026-11-13

package logjournal

import (
	"testing"
	"time"
)

// TestImportULID_KeepsTheRecordsMillisecond: an imported record's key sorts at
// its own time (Query orders by key, Prune cuts by key), and the key is a pure
// function of (time, source ID).
func TestImportULID_KeepsTheRecordsMillisecond(t *testing.T) {
	at := time.Date(2026, 11, 10, 15, 37, 13, 179_000_000, time.UTC)
	ms, err := msOf(at)
	if err != nil {
		t.Fatal(err)
	}
	a := importULID(ms, "syslog:2026-11-10T15:37:13.179Z:scanner")
	got, _, err := parseULID(a)
	if err != nil {
		t.Fatal(err)
	}
	if got != ms {
		t.Errorf("the key's millisecond = %d, want the record's %d", got, ms)
	}
	if b := importULID(ms, "syslog:2026-11-10T15:37:13.179Z:scanner"); a != b {
		t.Errorf("the same (time, source) gave two keys: %s and %s", a, b)
	}
	if b := importULID(ms, "syslog:2026-11-10T15:37:13.179Z:organizer"); a == b {
		t.Errorf("two sources of one millisecond share the key %s", a)
	}
	if b := importULID(ms+1, "syslog:2026-11-10T15:37:13.179Z:scanner"); a == b {
		t.Errorf("two milliseconds of one source share the key %s", a)
	}
}
