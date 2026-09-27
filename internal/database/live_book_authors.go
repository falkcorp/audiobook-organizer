// file: internal/database/live_book_authors.go
// version: 1.0.0
// guid: 8d1f4b62-3e9a-4c07-a5d8-2b6e0f9c7a13
// last-edited: 2026-09-27

package database

import (
	"fmt"
	"sort"
)

// BookAuthorReader reads a book's author credits: the book_authors join and
// the author rows it points at.
type BookAuthorReader interface {
	GetBookAuthors(bookID string) ([]BookAuthor, error)
	GetAuthorByID(id int) (*Author, error)
}

// LiveBookAuthorNames returns the names of every author credited on book,
// primary first: Book.AuthorID (which can hold a primary the join lacks),
// then the book_authors join in Position order. An author reached twice (the
// same id, or two ids GetAuthorByID resolves to one row) is listed once.
//
// This is the book's LIVE author. Book.Author, the display object embedded in
// the persisted book row, is a snapshot that nothing refreshes when AuthorID
// or the join changes (UpdateBook preserves it when a write leaves it nil, and
// the metadata apply writes AuthorID and the join without touching it), so it
// is nil on most books and stale on some. Anything that JUDGES a book by its
// author must read this instead.
//
// A read failure is an error, never a shorter list: an incomplete author list
// reads as "fewer authors", which loosens every check built on it.
func LiveBookAuthorNames(s BookAuthorReader, book *Book) ([]string, error) {
	if book == nil {
		return nil, nil
	}
	var ids []int
	if book.AuthorID != nil && *book.AuthorID > 0 {
		ids = append(ids, *book.AuthorID)
	}
	links, err := s.GetBookAuthors(book.ID)
	if err != nil {
		return nil, fmt.Errorf("read book authors of %s: %w", book.ID, err)
	}
	links = append([]BookAuthor(nil), links...)
	sort.SliceStable(links, func(i, j int) bool { return links[i].Position < links[j].Position })
	for _, l := range links {
		if l.AuthorID > 0 {
			ids = append(ids, l.AuthorID)
		}
	}
	seen := make(map[int]bool, len(ids))
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		a, err := s.GetAuthorByID(id)
		if err != nil {
			return nil, fmt.Errorf("read author %d of %s: %w", id, book.ID, err)
		}
		if a == nil {
			continue
		}
		if a.ID != 0 && a.ID != id {
			if seen[a.ID] {
				continue
			}
			seen[a.ID] = true
		}
		names = append(names, a.Name)
	}
	return names, nil
}
