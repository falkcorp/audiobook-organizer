// file: internal/merge/retired_shell.go
// version: 1.1.0
// guid: 7c1d9e42-8a35-4b60-95f1-d3e8a02b6c74
// last-edited: 2026-10-24

package merge

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ErrBookIsRetiredShell refuses a purge, a hard delete, a restore or a
// whole-row revert of a book whose file records the retired-versions repair
// re-keyed onto another book as retired versions, and that still hold rows.
// Such a book (a "shell") owns no live file row, so every "does it own files"
// check passes it:
//
//   - a purge would delete the only record of where those versions came from,
//     and the purge that journals a snapshot (so a per-version restore can put
//     the book back) is not built yet, so nothing could undo it;
//   - a restore would bring back an empty "imported" book while its files stay
//     retired under the other book.
//
// The refusal lasts until the retired versions no longer hold rows (a version
// restored as a separate book, or a revert of the retire).
//
// It is database.ErrBookIsRetiredShell: the store's own backstop (DeleteBook,
// RevertBookToVersion) returns the same sentinel, so a caller's check and the
// primitive's refusal are one error.
var ErrBookIsRetiredShell = database.ErrBookIsRetiredShell

// RetiredRowsHeldFor returns how many retired book_file rows, across every
// holder, belong to a retired version made from shellID. It fails closed: a
// read error is returned, never treated as zero, and so is a store that cannot
// read retired rows (database.ErrRetiredReaderUnavailable) unless it declares
// that it never holds one (database.NoRetiredBookFiles). See
// database.RetiredRowsHeldFor.
func RetiredRowsHeldFor(store any, shellID string) (int, error) {
	return database.RetiredRowsHeldFor(store, shellID)
}

// RefuseRetiredShell returns an error wrapping ErrBookIsRetiredShell, naming
// the row count and what to do instead, when shellID still has retired
// versions holding rows, and nil when it has none. A read error, or a store
// that cannot read retired rows, is returned as is (fail closed).
func RefuseRetiredShell(store any, shellID, action string) error {
	return database.RefuseRetiredShell(store, shellID, action)
}
