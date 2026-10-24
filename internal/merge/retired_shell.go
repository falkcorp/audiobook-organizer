// file: internal/merge/retired_shell.go
// version: 1.0.0
// guid: 7c1d9e42-8a35-4b60-95f1-d3e8a02b6c74
// last-edited: 2026-10-23

package merge

import (
	"errors"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ErrBookIsRetiredShell refuses a purge, a hard delete or a restore of a book
// whose file records the retired-versions repair re-keyed onto another book as
// retired versions, and that still hold rows. Such a book (a "shell") owns no
// live file row, so every "does it own files" check passes it:
//
//   - a purge would delete the only record of where those versions came from,
//     and the purge that journals a snapshot (so a per-version restore can put
//     the book back) is not built yet, so nothing could undo it;
//   - a restore would bring back an empty "imported" book while its files stay
//     retired under the other book.
//
// The refusal lasts until the retired versions no longer hold rows (a version
// restored as a separate book, or a revert of the retire).
var ErrBookIsRetiredShell = errors.New("the book's files were retired onto another book as retired versions")

// RetiredRowsHeldFor returns how many retired book_file rows, across every
// holder, belong to a retired version made from shellID. It fails closed: a
// read error is returned, never treated as zero.
//
// A store that cannot read retired rows (no database.RetiredBookFileReader)
// reports zero: the same store is what retires rows, so it cannot have made
// any.
func RetiredRowsHeldFor(store any, shellID string) (int, error) {
	r, ok := database.AsCapability[database.RetiredBookFileReader](store)
	if !ok {
		return 0, nil
	}
	versions, err := r.GetRetiredVersionsFromBook(shellID)
	if err != nil {
		return 0, fmt.Errorf("read the retired versions of %s: %w", shellID, err)
	}
	if len(versions) == 0 {
		return 0, nil
	}
	byHolder := map[string]map[string]bool{}
	for i := range versions {
		if byHolder[versions[i].BookID] == nil {
			byHolder[versions[i].BookID] = map[string]bool{}
		}
		byHolder[versions[i].BookID][versions[i].ID] = true
	}
	held := 0
	for holder, ids := range byHolder {
		rows, err := r.GetRetiredBookFiles(holder)
		if err != nil {
			return 0, fmt.Errorf("read the retired rows held by %s: %w", holder, err)
		}
		for i := range rows {
			if ids[rows[i].VersionID] {
				held++
			}
		}
	}
	return held, nil
}

// RefuseRetiredShell returns an error wrapping ErrBookIsRetiredShell, naming
// the row count and what to do instead, when shellID still has retired
// versions holding rows, and nil when it has none. A read error is returned
// as is (fail closed).
func RefuseRetiredShell(store any, shellID, action string) error {
	n, err := RetiredRowsHeldFor(store, shellID)
	if err != nil {
		return fmt.Errorf("%s %s refused: %w", action, shellID, err)
	}
	if n == 0 {
		return nil
	}
	return fmt.Errorf("%s %s refused: %w (%d retired file record(s) still held); restore one of its retired versions as a separate book instead",
		action, shellID, ErrBookIsRetiredShell, n)
}
