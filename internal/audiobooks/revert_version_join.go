// file: internal/audiobooks/revert_version_join.go
// version: 1.0.0
// guid: 119df611-0727-437b-923d-15523fd6172b
// last-edited: 2026-11-03

package audiobooks

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

// revertRepairVersionGroupJoin puts a book a repair put into a minted version
// group (undo.ChangeTypeRepairVersionGroupJoin) back to the group and primary
// flag it carried, exactly (a nil group stays nil, an unset flag stays unset),
// but only while the book is still what the join left and nobody else is in
// the group (undo.CheckVersionGroupJoinCurrent). A book that already holds the
// old state counts restored. Rows run newest first, so the version the same
// operation added was hidden by its own repair_book_create revert before this
// runs; a live member it did not hide refuses the row, and the book keeps its
// group.
//
// Lock order: merge lock, then the group stripes of the group left ("" is the
// no-group sentinel, the one joined back), then the book's write stripe
// (modifyBook). The group is listed and the book written under the same
// stripes, so no member joins between the check and the write.
func (rs *RevertService) revertRepairVersionGroupJoin(c *database.OperationChange) error {
	old, now, err := undo.DecodeVersionGroupJoin(c)
	if err != nil {
		return &undo.ReferentError{Reason: undo.ReasonOldValueUnparsable, Detail: err.Error()}
	}
	defer rs.lockMerge()()
	defer versionprimary.LockGroups("", now.GroupID())()
	if _, err := rs.loadBook(c.BookID); err != nil {
		return err
	}
	members, err := rs.db.GetBooksByVersionGroup(now.GroupID())
	if err != nil {
		return &undo.ReferentError{Reason: undo.ReasonFieldUnreadable,
			Detail: fmt.Sprintf("read version group %s: %v", now.GroupID(), err)}
	}
	return rs.modifyBook(c.OperationID, c.BookID, func(book *database.Book) error {
		if err := undo.CheckVersionGroupJoinCurrent(book, members, nil, c); err != nil {
			return err
		}
		old.Apply(book)
		return nil
	})
}
