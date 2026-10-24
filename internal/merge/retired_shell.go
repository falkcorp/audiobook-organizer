// file: internal/merge/retired_shell.go
// version: 1.0.1
// guid: 7c1d9e42-8a35-4b60-95f1-d3e8a02b6c74
// last-edited: 2026-10-28

package merge

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// RefuseRetiredShell returns an error wrapping database.ErrBookIsRetiredShell
// when id is the shell of a retired version that still holds retired rows, and
// nil when it is not. It is the one check behind every refusal of a trashed
// book that the retired-versions repair emptied: the restore
// (RestoreFromTrashLocked), the purge (and its preview), the discard-progress
// purge and the hard delete.
//
// Such a book's audio was re-keyed under another book (the holder), so it owns
// no live row and every "does it own files" check passes it:
//
//   - restoring it would bring back a live book with no files that ABS never
//     lists, and take it out of the trash where the retire's later steps look
//     for it;
//   - purging or deleting it would drop the only record of where those versions
//     came from.
//
// The book stays in the trash and its files stay safe, listed as retired
// versions of the holder. A retired version that holds no rows any more (its
// rows were un-retired) does not block.
//
// Fail closed: a store that cannot say whether the book has retired versions
// (database.ErrRetiredReaderUnavailable) or an unreadable list is an error that
// does not wrap ErrBookIsRetiredShell, because neither is proof the book is a
// shell and neither is proof it is not.
//
// action names what is being refused, for the message ("restore", "purge",
// "discard progress and purge", "hard delete").
func RefuseRetiredShell(store any, id, action string) error {
	vers, err := database.RetiredVersionsFromBook(store, id)
	if err != nil {
		return fmt.Errorf("%s %s: cannot verify it is not the shell of a retired version: %w", action, id, err)
	}
	for i := range vers {
		rows, err := database.BookFilesIncludingRetired(store, vers[i].BookID)
		if err != nil {
			return fmt.Errorf("%s %s: cannot verify retired version %s holds no rows: %w", action, id, vers[i].ID, err)
		}
		for j := range rows {
			if rows[j].Retired && rows[j].VersionID == vers[i].ID {
				consequence := "deleting this book would drop the record of where they came from"
				if action == "restore" {
					consequence = "this book would come back with none"
				}
				return fmt.Errorf("%s %s: %w (version %s keeps its files under book %s, so %s); leave it in the trash, its files are safe and listed as retired versions of book %s, and restoring them as a separate book is not available yet",
					action, id, database.ErrBookIsRetiredShell, vers[i].ID, vers[i].BookID, consequence, vers[i].BookID)
			}
		}
	}
	return nil
}
