// file: internal/database/file_hash_holders.go
// version: 1.0.0
// guid: 3d7c91a4-6e28-4b05-a1f3-82c4e0b9d756
// last-edited: 2026-10-26

package database

import "errors"

// FileHashHolderLookup is the capability that lists EVERY book holding a file
// hash, live and trashed, ascending by id (*PebbleStore.BooksByFileHash,
// database.MockStore). It is not part of Store: Store's width is ratcheted, and
// a consumer reaches it by AsCapability, which looks through decorators.
//
// Why it exists beside GetBookByFileHash. That lookup answers with ONE book (the
// live holder with the smallest id, else the trashed one with the smallest id),
// and a caller asking "is there a book OTHER than this one holding this file"
// gets the wrong answer whenever the one book it is handed is itself: the
// organizer organizing the oldest holder, or the dedup engine checking it, was
// told "yourself", skipped its duplicate check, and never saw the other holder
// (round 4 of #4113, after the per-book index made the choice deterministic).
// Such a caller must see every holder.
type FileHashHolderLookup interface {
	BooksByFileHash(hash string) ([]*Book, error)
}

// ErrNoFileHashHolderLookup is returned by BooksHoldingFileHash for a store
// that cannot list every holder of a hash.
var ErrNoFileHashHolderLookup = errors.New("the store cannot list every book holding a file hash")

// BooksHoldingFileHash returns every book that holds file hash hash, live and
// trashed, ascending by id, through the FileHashHolderLookup capability of s
// (looking through store decorators). An empty hash has no holders.
//
// It fails closed: a store without the capability, or a list that could not be
// completed (an index candidate whose row cannot be read), is an error, never
// an empty answer, so a caller's duplicate check cannot pass on a store that
// could not look.
func BooksHoldingFileHash(s any, hash string) ([]*Book, error) {
	if hash == "" {
		return nil, nil
	}
	lookup, ok := AsCapability[FileHashHolderLookup](s)
	if !ok {
		return nil, ErrNoFileHashHolderLookup
	}
	return lookup.BooksByFileHash(hash)
}
