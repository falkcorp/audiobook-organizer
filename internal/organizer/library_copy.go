// file: internal/organizer/library_copy.go
// version: 1.1.0
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
// Production wires metafetch's Service.ExistingLibraryCopyOfFile: the metadata
// apply pipeline's lookup (same root/protected gates, same clean-sibling
// rules), narrowed by IsLibraryCopyOf to a copy of THIS book's file, so the
// organize preview and the organize apply agree on the row and never act on a
// different edition of the book. A resolver must never create anything.
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

// IsLibraryCopyOf reports whether candidate is a library copy of original's
// OWN file -- not merely another version of the same book. A version group
// can hold several editions (an iTunes m4b and an mp3 rip, say); organizing
// the m4b must never act on the mp3 edition just because it is the group's
// clean member under RootDir.
//
// The test is content identity. original's identities are its FileHash (a
// protected original's file is never rewritten, so this is stable) and its
// OriginalFileHash. candidate's are FileHash, OriginalFileHash and
// OrganizedFileHash. They match when the two sets share a non-empty value.
// The extra candidate fields matter because a library copy's FileHash does
// NOT stay equal to the original's: CreateOrganizedVersion sets it (via
// ApplyOrganizedFileMetadata) to the hash of the freshly copied bytes, but
// the copy's tags are then written, and the next scan re-hashes the file and
// overwrites FileHash (applyScannerFields). OrganizedFileHash (the hash at
// organize time) and OriginalFileHash (inherited from the original and
// preserved by every rescan) still carry the original's content hash.
func IsLibraryCopyOf(original, candidate *database.Book) bool {
	if original == nil || candidate == nil {
		return false
	}
	var ids []string
	for _, h := range []*string{original.FileHash, original.OriginalFileHash} {
		if h != nil && *h != "" {
			ids = append(ids, *h)
		}
	}
	for _, h := range []*string{candidate.FileHash, candidate.OriginalFileHash, candidate.OrganizedFileHash} {
		if h == nil || *h == "" {
			continue
		}
		for _, id := range ids {
			if *h == id {
				return true
			}
		}
	}
	return false
}

// CollisionLibraryCopyExists is the batch organize's skip category (Stats
// Collisions, organize_skipped change rows) for a LibraryCopyExistsError.
const CollisionLibraryCopyExists = "library_copy_exists"

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
