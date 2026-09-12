// file: internal/database/author_relink_guard.go
// version: 1.0.0
// guid: 8d41c2e7-5b39-4f0a-a6e2-1c7b93f05d48
// last-edited: 2026-09-12

package database

import (
	"errors"
	"fmt"
	"strings"
)

// ErrAuthorStillLinked is returned by VerifyAuthorUnlinked when a book still
// credits the author after a relink. Callers must skip DeleteAuthor on it.
var ErrAuthorStillLinked = errors.New("author still linked to books after relink")

// AuthorRelinkLister is the one method VerifyAuthorUnlinked needs.
type AuthorRelinkLister interface {
	GetBooksByAuthorIDForRelinkCore(authorID int) ([]BookCore, error)
}

// VerifyAuthorUnlinked is the last check every relink-then-DeleteAuthor path
// runs before DeleteAuthor: it re-reads the author's links in ANY book state
// and refuses when one survived the relink.
//
// DeleteAuthor sweeps the author out of every book_authors row, trashed books
// included, and leaves any legacy Book.AuthorID pointing at the deleted id. So
// a link the relink failed to move -- a GetBookAuthors or SetBookAuthors error
// the loop logged and skipped, an AuthorID rewrite whose UpdateBook failed, a
// book written concurrently -- is destroyed by the delete, not preserved. This
// turns every such case into "author kept, error reported".
//
// It is a POST-relink check rather than a pre-flight reference count on
// purpose. The split paths (maintenance/author.go, scheduler/extra_ops.go)
// iterate every author in the library, and a per-author full-library
// reference scan inside that loop is the single-core hotspot shape CLAUDE.md
// warns about. The re-read here is one indexed lookup.
//
// Dangling junction rows (a book_authors row whose book row no longer exists)
// are deliberately invisible to it: the relink getter emits only books it can
// resolve, nothing could relink such a row, and DeleteAuthor's sweep removes it.
func VerifyAuthorUnlinked(store AuthorRelinkLister, authorID int) error {
	books, err := store.GetBooksByAuthorIDForRelinkCore(authorID)
	if err != nil {
		return fmt.Errorf("re-check links for author %d before delete: %w", authorID, err)
	}
	if len(books) == 0 {
		return nil
	}
	const sample = 5
	ids := make([]string, 0, sample)
	for i := range books {
		if i == sample {
			break
		}
		ids = append(ids, books[i].ID)
	}
	return fmt.Errorf("%w: author %d is still credited by %d book(s) (e.g. %s); refusing to delete it",
		ErrAuthorStillLinked, authorID, len(books), strings.Join(ids, ", "))
}
