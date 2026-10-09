// file: internal/database/book_listing_fields.go
// version: 1.1.0
// guid: 4c6a2e85-1f93-4b7d-9e08-7a5d3c1b2f69
// last-edited: 2026-10-09

package database

import (
	"errors"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// bookListingFieldsLog carries this file's diagnostics through the log-injection barrier.
var bookListingFieldsLog = logger.New("database.book-listing-fields")

// BookListingFields is the slice of a book a per-row listing over the whole
// metadata cache reads: whether the book exists, its title and its review
// status.
type BookListingFields struct {
	Title string
	// MetadataReviewStatus is "" when the book has none.
	MetadataReviewStatus string
}

// BookListingFieldsReader is the optional capability of reading
// BookListingFields for many books at once without a full-fidelity Book read
// per id. Obtained with AsCapability (a read; the search decorator overrides
// only writes), not added to Store.
type BookListingFieldsReader interface {
	// GetBookListingFields returns an entry for every id that names a book
	// (live or soft-deleted, as GetBooksByIDs does); ids that resolve to
	// nothing are absent.
	GetBookListingFields(ids []string) (map[string]BookListingFields, error)
}

func listingFieldsOf(b *Book) BookListingFields {
	f := BookListingFields{Title: b.Title}
	if b.MetadataReviewStatus != nil {
		f.MetadataReviewStatus = *b.MetadataReviewStatus
	}
	return f
}

// GetBookListingFields reads the two fields off each book's memdb row: one
// pointer lookup per id, no JSON and no copy. It refuses (ErrMemdbIncomplete)
// when the books table is known to be missing rows, because a missing row
// reads as "no such book" and the caller drops it.
func (m *MemStore) GetBookListingFields(ids []string) (map[string]BookListingFields, error) {
	if err := m.requireTablesComplete("book listing fields (a missing row reads as a deleted book)", memTableBooks); err != nil {
		return nil, err
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	out := make(map[string]BookListingFields, len(ids))
	for _, id := range ids {
		raw, err := txn.First(memTableBooks, memIdxID, id)
		if err != nil {
			return nil, fmt.Errorf("memdb book %s: %w", id, err)
		}
		if raw == nil {
			continue
		}
		out[id] = listingFieldsOf(raw.(*Book))
	}
	return out, nil
}

// GetBookListingFields serves from memdb when it is published and complete,
// and otherwise from GetBooksByIDs -- the read the metadata-cache listing
// made before this existed: two Pebble point reads and a full JSON decode
// per book, which for 40-56k cached books was seconds of every request.
func (p *PebbleStore) GetBookListingFields(ids []string) (map[string]BookListingFields, error) {
	if m := p.mem(); p.UseMemDB && m != nil {
		out, err := m.GetBookListingFields(ids)
		if err == nil {
			return out, nil
		}
		if !errors.Is(err, ErrMemdbIncomplete) {
			return nil, err
		}
		bookListingFieldsLog.Warn("book listing fields: memdb is missing rows; reading from Pebble: error=%v", err)
	}
	books, err := p.GetBooksByIDs(ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]BookListingFields, len(books))
	for i := range books {
		out[books[i].ID] = listingFieldsOf(&books[i])
	}
	return out, nil
}
