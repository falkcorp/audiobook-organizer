// file: internal/audiobooks/block_hash_guard.go
// version: 1.0.0
// guid: 5e8c1b94-3a60-4d27-9f4e-7b2d0a6c8e13
// last-edited: 2026-10-22

package audiobooks

import (
	"fmt"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// hashHolderLookup is the two lookups that find the books holding a file hash
// without scanning the library: the single-owner book:hash: index, and the
// multi-valued book_file hash indexes. It is a capability, not part of the
// service's store interface, to keep that interface from widening.
type hashHolderLookup interface {
	GetBookByFileHash(hash string) (*database.Book, error)
	BookFilesWithHash(hash string) ([]database.BookFile, error)
}

// liveHashHolders returns the ids of live (not trashed) books other than book
// whose file has this hash. The book:hash: index holds one owner per hash (last
// writer wins), so it alone cannot see a second book holding the same file;
// the book_file hash indexes can, so both are read and the candidates are
// verified against their own rows.
//
// An error means the answer is not complete.
func (svc *AudiobookService) liveHashHolders(book *database.Book, hash string) ([]string, error) {
	lookup, ok := database.AsCapability[hashHolderLookup](svc.store)
	if !ok {
		return nil, fmt.Errorf("the store cannot look up which books hold a file hash")
	}
	candidates := map[string]struct{}{}
	owner, err := lookup.GetBookByFileHash(hash)
	if err != nil {
		return nil, fmt.Errorf("read the book:hash index: %w", err)
	}
	if owner != nil {
		candidates[owner.ID] = struct{}{}
	}
	files, err := lookup.BookFilesWithHash(hash)
	if err != nil {
		return nil, fmt.Errorf("read the book_file hash indexes: %w", err)
	}
	for _, f := range files {
		candidates[f.BookID] = struct{}{}
	}
	delete(candidates, book.ID)

	var live []string
	for id := range candidates {
		row, err := svc.store.GetBookByID(id)
		if err != nil {
			return nil, fmt.Errorf("read book %s: %w", id, err)
		}
		if row != nil && !database.IsInTrash(row) {
			live = append(live, id)
		}
	}
	sort.Strings(live)
	return live, nil
}

// planHashBlock decides, BEFORE the delete, whether the book's hash may be
// blocked: it returns ok=true to block it, or a warning saying why not
// (naming the live books that hold the file). It must run before a hard
// delete, because DeleteBook removes the book:hash: index row, which is also
// how the other holder is found.
//
// A hash that a live book still holds is not blocked: the scanner skips a
// blocked hash, so blocking it stops the library scanning that book's own
// file. The delete itself is unaffected. Failing to tell who holds the hash
// also skips the block: not blocking only allows a reimport the user can
// block later, blocking wrongly hides a book.
func (svc *AudiobookService) planHashBlock(book *database.Book) (ok bool, warning string) {
	if book.FileHash == nil || *book.FileHash == "" {
		return false, ""
	}
	holders, err := svc.liveHashHolders(book, *book.FileHash)
	if err != nil {
		return false, fmt.Sprintf("the file's hash was not blocked: could not check whether other books still hold this file: %v", err)
	}
	if len(holders) > 0 {
		return false, fmt.Sprintf("the file's hash was not blocked, because %d other live book(s) hold the same file (%s); blocking it would stop the library from scanning them",
			len(holders), strings.Join(holders, ", "))
	}
	return true, ""
}

// blockHash writes the block planHashBlock allowed. A failure is a warning for
// the response; it never undoes or prevents the delete.
func (svc *AudiobookService) blockHash(book *database.Book, reason string) (blocked bool, warning string) {
	if err := svc.store.AddBlockedHash(*book.FileHash, reason); err != nil {
		return false, fmt.Sprintf("the file's hash could not be blocked: %v", err)
	}
	return true, ""
}

// blockHashUnlessShared is planHashBlock then blockHash, for the soft delete
// (the row and its index stay) where nothing is removed in between.
func (svc *AudiobookService) blockHashUnlessShared(book *database.Book, reason string) (blocked bool, warning string) {
	ok, warning := svc.planHashBlock(book)
	if !ok {
		return false, warning
	}
	return svc.blockHash(book, reason)
}
