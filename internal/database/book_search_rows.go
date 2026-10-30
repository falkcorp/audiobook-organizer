// file: internal/database/book_search_rows.go
// version: 1.0.0
// guid: 23bed6ea-c068-403e-a3f9-7039f8876d38
// last-edited: 2026-10-30

package database

import (
	"encoding/json"
	"fmt"
)

// BookSearchRow is the narrow projection a text search needs from a book: its
// ID and the three strings a needle is matched against. AuthorName is the
// resolved name of the book's AuthorID, joined by the store, in the author's
// stored casing (the caller folds case). It is empty when the book has no
// author or its AuthorID points at a row that does not exist -- production
// carries a documented population of those dangling references, and an empty
// name can never contain a non-empty needle.
//
// It is deliberately a four-string struct and not a BookCore: BookCore is a
// ~60-field value, and a whole-library search that only reads three strings of
// it was allocating the library to answer a substring question.
type BookSearchRow struct {
	ID         string
	Title      string
	FilePath   string
	AuthorName string
}

// ListBookSearchRows returns one BookSearchRow per non-deleted book, in the
// memdb ID order. The deletion rule is the one GetAllBooksCore and ListBookIDs
// apply (bookIsSoftDeleted), so a caller migrating off GetAllBooksCore(0, 0)
// sees the same set of books.
//
// One read transaction covers the books walk and every author lookup, so a
// book and the author name beside it come from the same snapshot. Rows borrow
// the strings already held by the memdb entries (Go strings are immutable and
// the memdb never mutates a published row in place), so the only allocation
// is the row slice itself plus one lookup per DISTINCT author.
func (m *MemStore) ListBookSearchRows() ([]BookSearchRow, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()

	iter, err := txn.Get(memTableBooks, memIdxID)
	if err != nil {
		return nil, fmt.Errorf("memdb book search rows: %w", err)
	}

	names := make(map[int]string, 4096)
	rows := make([]BookSearchRow, 0, 1024)
	for obj := iter.Next(); obj != nil; obj = iter.Next() {
		b := obj.(*Book)
		if bookIsSoftDeleted(b) {
			continue
		}
		row := BookSearchRow{ID: b.ID, Title: b.Title, FilePath: b.FilePath}
		if b.AuthorID != nil {
			name, seen := names[*b.AuthorID]
			if !seen {
				// A lookup error and a missing row both mean "no name": an
				// int primary-key lookup cannot fail in a way a search could
				// act on, and a dangling AuthorID is an expected state.
				if obj, err := txn.First(memTableAuthors, memIdxID, *b.AuthorID); err == nil {
					if a, ok := obj.(*Author); ok && a != nil {
						name = a.Name
					}
				}
				names[*b.AuthorID] = name
			}
			row.AuthorName = name
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// ListBookSearchRows is MemStore.ListBookSearchRows with a Pebble fallback for
// the cold-start window before memdb publishes (and for UseMemDB=false).
//
// The fallback decodes only the fields the projection needs plus the deletion
// flag, so the row codec (JSON) skips the rest of the book instead of
// materialising it. Author names come from GetAllAuthors, the same read the
// handler this replaces used, so both paths resolve names identically.
func (p *PebbleStore) ListBookSearchRows() ([]BookSearchRow, error) {
	if mem := p.memOrFallback("ListBookSearchRows"); mem != nil {
		return mem.ListBookSearchRows()
	}

	authors, err := p.GetAllAuthors()
	if err != nil {
		return nil, err
	}
	names := make(map[int]string, len(authors))
	for _, a := range authors {
		names[a.ID] = a.Name
	}

	rows := make([]BookSearchRow, 0, 1024)
	if err := forEachBookRow(p.db, func(_ string, rowValue []byte) error {
		// probe is declared per row on purpose: AuthorID and
		// MarkedForDeletion are pointers and omitempty drops them, so a
		// reused struct would carry the previous book's values onto a row
		// that omits them.
		var probe struct {
			ID                string `json:"id"`
			Title             string `json:"title"`
			FilePath          string `json:"file_path"`
			AuthorID          *int   `json:"author_id,omitempty"`
			MarkedForDeletion *bool  `json:"marked_for_deletion,omitempty"`
		}
		if err := json.Unmarshal(rowValue, &probe); err != nil {
			return err
		}
		if markedForDeletionFlag(probe.MarkedForDeletion) {
			return nil
		}
		row := BookSearchRow{ID: probe.ID, Title: probe.Title, FilePath: probe.FilePath}
		if probe.AuthorID != nil {
			row.AuthorName = names[*probe.AuthorID]
		}
		rows = append(rows, row)
		return nil
	}); err != nil {
		return nil, err
	}
	return rows, nil
}
