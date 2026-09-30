// file: internal/metafetch/queued_apply_history.go
// version: 1.0.0
// guid: de7ac5f2-cd18-4dc9-8b95-a2f28a9fb8b5
// last-edited: 2026-09-30

package metafetch

import (
	"fmt"
	"slices"
)

// The queued single-book apply (metadata.apply-when-scanned) is enqueued
// because the library scan is reading the book, and it runs after the scan
// has moved on. Between the two, the scanner's merge almost always rewrites
// the row from the file's tags -- that must NOT stop the apply. What must stop
// it is a LATER EDIT: another apply, a manual edit, an undo, a bulk update, a
// repair. Those are exactly the writers that record metadata change history;
// the scanner records none. So the queued apply compares change history, not
// the row:
//
//   - at enqueue, ApplyEditMark records the newest field-edit history row;
//   - at run (under the book's scan lock), ApplyEditsSince lists the rows
//     newer than that mark. Rows carrying the op's own batch id mean the apply
//     already landed (a re-run after a restart); any other row is a later
//     edit and the apply refuses.

// fileSideChangeTypes are history rows that record file work (rename, tag
// write-back, cover archive), not a change to a field the apply writes.
var fileSideChangeTypes = []string{"rename", "write-back", "cover-archive"}

func isFieldEdit(changeType string) bool {
	return !slices.Contains(fileSideChangeTypes, changeType)
}

// ApplyEditMark returns the ChangedAt (Unix nanoseconds) of the book's newest
// field-edit history row, or 0 when it has none.
func (mfs *Service) ApplyEditMark(bookID string) (int64, error) {
	history, err := mfs.db.GetBookChangeHistory(bookID, 1<<30)
	if err != nil {
		return 0, fmt.Errorf("read change history of %s: %w", bookID, err)
	}
	var mark int64
	for _, r := range history {
		if isFieldEdit(r.ChangeType) {
			mark = max(mark, r.ChangedAt.UnixNano())
		}
	}
	return mark, nil
}

// QueuedApplyEdits is what ApplyEditsSince found after the mark.
type QueuedApplyEdits struct {
	// OwnApplied: a row carries ownBatch -- this apply already committed.
	OwnApplied bool
	// Others are the later edits by anyone else, as "field (change type,
	// source)" labels, sorted.
	Others []string
}

// ApplyEditsSince lists the field-edit history rows of bookID newer than mark
// (ApplyEditMark). The whole history is read: the store orders it by field,
// not by time, so a limit would cut arbitrary rows.
func (mfs *Service) ApplyEditsSince(bookID string, mark int64, ownBatch string) (QueuedApplyEdits, error) {
	var out QueuedApplyEdits
	history, err := mfs.db.GetBookChangeHistory(bookID, 1<<30)
	if err != nil {
		return out, fmt.Errorf("read change history of %s: %w", bookID, err)
	}
	for _, r := range history {
		if !isFieldEdit(r.ChangeType) || r.ChangedAt.UnixNano() <= mark {
			continue
		}
		if ownBatch != "" && r.BatchID == ownBatch {
			out.OwnApplied = true
			continue
		}
		label := r.Field + " (" + r.ChangeType
		if r.Source != "" {
			label += ", " + r.Source
		}
		out.Others = append(out.Others, label+")")
	}
	slices.Sort(out.Others)
	out.Others = slices.Compact(out.Others)
	return out, nil
}
