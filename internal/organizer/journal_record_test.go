// file: internal/organizer/journal_record_test.go
// version: 1.0.0
// guid: d58b3e07-1a94-4c62-9f7e-b0c4a8216d93
// last-edited: 2026-10-30

package organizer

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger/logtest"
)

// journalFake is a Store whose only working method is the journal write; any
// other call panics on the nil embedded Store, which proves recordChange
// touches nothing else.
type journalFake struct {
	Store
	rows []*database.OperationChange
}

func (f *journalFake) CreateOperationChange(c *database.OperationChange) error {
	f.rows = append(f.rows, c)
	return nil
}

// TestRecordChange_RefusedTypeIsLoggedAndNotWritten: a change type nobody
// classified is not written, the caller gets no error (an organize run goes
// on), and the refusal is visible as a Warn naming the type and the operation.
func TestRecordChange_RefusedTypeIsLoggedAndNotWritten(t *testing.T) {
	buf := logtest.Capture(t, slog.LevelWarn)
	f := &journalFake{}
	svc := &Service{db: f}

	svc.recordChange(&database.OperationChange{OperationID: "op-gotham-1", BookID: "bk-1", ChangeType: "batman_unclassified"})

	if len(f.rows) != 0 {
		t.Fatalf("an unclassified row was written: %+v", f.rows)
	}
	lines := buf.Lines("journal row not recorded")
	if len(lines) != 1 {
		t.Fatalf("want one Warn, got %d; log:\n%s", len(lines), buf.String())
	}
	for _, want := range []string{"batman_unclassified", "op-gotham-1"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("Warn %q does not name %q", lines[0], want)
		}
	}

	buf.Reset()
	svc.recordChange(&database.OperationChange{OperationID: "op-gotham-1", BookID: "bk-1", ChangeType: "organize_skipped"})
	if len(f.rows) != 1 || buf.String() != "" {
		t.Fatalf("a classified row must be written without a Warn: rows=%d log=%q", len(f.rows), buf.String())
	}
}
