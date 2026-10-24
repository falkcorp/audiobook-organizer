// file: internal/audiobooks/retired_holdings.go
// version: 1.0.0
// guid: 9e3c5a17-4d82-4b60-8f2a-1c7d06b9e453
// last-edited: 2026-10-23

package audiobooks

import (
	"errors"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ReasonHoldsRetiredVersions is the reason a purge or delete reports for a book
// that owns no live book_file row but is the holder of retired versions.
const ReasonHoldsRetiredVersions = "holds_retired_versions"

// ErrHoldsRetiredVersions is returned by a purge or hard delete refused because
// the book holds retired versions: book_file rows the retire-versions repair
// keyed under it, which GetBookFiles hides. It always wraps
// database.ErrBookOwnsFiles as well (DeleteBook refuses such a book with that
// error, and the 409 mapping callers already have for it stays right), and
// errors.Is(err, ErrHoldsRetiredVersions) tells the two apart.
var ErrHoldsRetiredVersions = errors.New(ReasonHoldsRetiredVersions)

// bookFileHoldings is what a book keeps in the book_file table, split by kind.
type bookFileHoldings struct {
	// live counts the rows GetBookFiles returns: the book's own files.
	live int
	// retired counts the retired-version rows keyed under the book (versions of
	// other books a live book keeps out of its own file list).
	retired int
}

// readBookFileHoldings reads every book_file row keyed under bookID, live and
// retired, and counts each kind. A purge or delete pre-check must use it, not
// GetBookFiles: GetBookFiles skips retired rows, so a holder with only retired
// rows would look empty, pass the pre-check, and then be refused by DeleteBook
// (which counts every row key) after the pre-check's side effects had run: an
// external-ID tombstone, a hash block, carried listening state.
//
// Fail closed: an unreadable list, or a store that cannot say whether it holds
// retired rows (database.ErrRetiredReaderUnavailable), is an error, never
// "no rows".
func (svc *AudiobookService) readBookFileHoldings(bookID string) (bookFileHoldings, error) {
	rows, err := database.BookFilesIncludingRetired(svc.store, bookID)
	if err != nil {
		return bookFileHoldings{}, err
	}
	var h bookFileHoldings
	for i := range rows {
		if rows[i].Retired {
			h.retired++
		} else {
			h.live++
		}
	}
	return h, nil
}

// refusal is the error a purge or delete pre-check returns for a book that must
// not be deleted, or nil when it holds nothing. A live row wins over a retired
// one: the book owns files of its own, which is the more basic reason.
// verb and advice are the caller's wording ("hard delete", "soft-delete it
// instead, ...").
func (h bookFileHoldings) refusal(verb, bookID, advice string) error {
	switch {
	case h.live > 0:
		return fmt.Errorf("%s %s: %w (%d row(s)); %s", verb, bookID, database.ErrBookOwnsFiles, h.live, advice)
	case h.retired > 0:
		return fmt.Errorf("%s %s: %w: %w (%d retired version row(s) are kept under it; restore or move them first); %s",
			verb, bookID, ErrHoldsRetiredVersions, database.ErrBookOwnsFiles, h.retired, advice)
	default:
		return nil
	}
}
