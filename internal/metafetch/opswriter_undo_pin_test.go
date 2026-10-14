// file: internal/metafetch/opswriter_undo_pin_test.go
// version: 1.0.0
// guid: 0f89dcb3-d996-4bc3-81f0-ca3662d51ac9
// last-edited: 2026-10-14

package metafetch

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter"
)

// opswriter cannot import this package (it depends on opswriter), so it
// keeps its own copy of the undo change type: a Fixer row's apply marker
// stops being evidence once an undo row covers the batch it names. A
// rename here that left the copy behind would make every undone batch look
// standing.
func TestOpsWriterUndoChangeTypeMatches(t *testing.T) {
	if opswriter.ChangeTypeUndo != ChangeTypeApplyUndo {
		t.Fatalf("opswriter.ChangeTypeUndo = %q, metafetch.ChangeTypeApplyUndo = %q", opswriter.ChangeTypeUndo, ChangeTypeApplyUndo)
	}
}
