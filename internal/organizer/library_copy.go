// file: internal/organizer/library_copy.go
// version: 1.0.0
// guid: f3144b62-7dfb-4ee1-95d2-ea314e1fde3c
// last-edited: 2026-09-30

package organizer

import (
	"errors"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// LibraryCopyResolver maps a book to the row an organize should act on. For a
// protected original (an import or iTunes path) that already has a library
// copy under RootDir it returns that copy; for every other book it returns the
// book itself. ok=false means the book is protected and has no usable copy
// yet, and the caller organizes the original, which creates one.
//
// Production wires metafetch's read-only lookup (Service.ExistingLibraryCopy),
// the same one the metadata apply pipeline uses to decide which row's files it
// may touch, so the organize preview, the organize apply and the metadata
// apply all agree on which row is "the library copy". A resolver must never
// create anything.
type LibraryCopyResolver func(book *database.Book) (libraryCopy *database.Book, ok bool)

// ResolveOrganizeSubject applies resolve to book and returns the row to
// organize. A nil resolver, ok=false, or a nil copy all mean "the book
// itself": organizing the original is exactly what happened before resolvers
// existed, and for a protected book with no copy it is what makes one.
func ResolveOrganizeSubject(resolve LibraryCopyResolver, book *database.Book) *database.Book {
	if resolve == nil || book == nil {
		return book
	}
	target, ok := resolve(book)
	if !ok || target == nil {
		return book
	}
	return target
}

// SameVersionGroup reports whether a and b are versions of one book: both
// carry the same non-empty VersionGroupID. It is the test the dedup engine's
// version_group_same suppressor applies (dedup.PairEligibility, and the file
// hash check in dedup's engine), so the organizer and the dedup queue agree
// on which same-content pairs are not duplicates.
func SameVersionGroup(a, b *database.Book) bool {
	if a == nil || b == nil || a.VersionGroupID == nil || b.VersionGroupID == nil {
		return false
	}
	return *a.VersionGroupID != "" && *a.VersionGroupID == *b.VersionGroupID
}

// ErrLibraryCopyExists is the sentinel LibraryCopyExistsError unwraps to.
var ErrLibraryCopyExists = errors.New("organize: book already has a library copy")

// LibraryCopyExistsError is Organizer.OrganizeBook declining to copy a book
// whose content already sits under RootDir as another version of the same book
// (same file hash, same version group) -- in practice the library copy
// metafetch made for a protected original. It is a refusal, not a duplicate:
// no collision hook fires. The caller should organize CopyID instead. Before
// 2026-09-30 this case was reported as "duplicate file already organized",
// fired the collision hook, and filed a dedup candidate pairing the book with
// its own copy.
type LibraryCopyExistsError struct {
	BookID   string
	CopyID   string
	CopyPath string
}

func (e *LibraryCopyExistsError) Error() string {
	return fmt.Sprintf("organize: book %s already has a library copy (book %s) at %s; organize the copy instead", e.BookID, e.CopyID, e.CopyPath)
}

func (e *LibraryCopyExistsError) Unwrap() error { return ErrLibraryCopyExists }
