// file: internal/search/index_builder.go
// version: 1.5.0
// guid: 8a1c2f4d-5b3e-4f70-b7d6-2e8d0f1b9a57
// last-edited: 2026-09-11
//
// Helpers that project a database.Book (with its author, series,
// and tag relations resolved) into a BookDocument ready for
// indexing. Used by both the startup full-build path and the
// incremental indexing hooks that fire on book create/update/
// delete.

package search

import (
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// indexBuilderStore is what this package actually calls, measured by emptying the
// interface and reading the compiler's enumeration: 4 methods. It was an
// inline anonymous interface of database.* embeds — 81 methods — repeated
// at 2 parameters, invisible to any line-oriented tool.
type indexBuilderStore interface {
	GetBookByID(id string) (*database.Book, error)
	GetAuthorByID(id int) (*database.Author, error)
	GetSeriesByID(id int) (*database.Series, error)
	GetBookTags(bookID string) ([]string, error)
}

// defaultDescriptionMaxChars caps the number of UTF-8 characters
// (runes) of Book.Description that are fed to the bleve index. The
// full description body contributes the bulk of index residency
// (~2GB across the production library) while most queries match on
// Title/Author/Series, not description prose. Truncating to the
// opening ~500 runes preserves the most query-relevant terms.
//
// Override via env BLEVE_DESCRIPTION_MAX_CHARS. A value of 0
// disables truncation entirely (full description indexed).
const defaultDescriptionMaxChars = 500

// descriptionLimit returns the configured max-rune limit for the
// description field, from config.AppConfig.BleveDescriptionMaxChars.
func descriptionLimit() int {
	if n := config.AppConfig.BleveDescriptionMaxChars; n >= 0 {
		return n
	}
	return defaultDescriptionMaxChars
}

// truncateForIndex returns the first n UTF-8 runes of s. n == 0
// means no truncation. The result is always valid UTF-8 (cut on a
// rune boundary). Invalid UTF-8 in the source is replaced with the
// Unicode replacement character via strings.ToValidUTF8-equivalent
// behavior at the rune-decode boundary.
func truncateForIndex(s string, n int) string {
	if n <= 0 || s == "" {
		return s
	}
	// Fast path: ASCII-only strings shorter than n bytes are also
	// shorter than n runes.
	if len(s) <= n {
		return s
	}
	count := 0
	i := 0
	for i < len(s) {
		if count == n {
			return s[:i]
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		count++
	}
	return s
}

// relationsBatchStore is the batch-read slice of the store that
// LoadBookRelations needs: one call per relation kind per page of
// books, instead of one point read per relation kind per book.
type relationsBatchStore interface {
	GetAuthorsByIDs(ids []int) (map[int]*database.Author, error)
	GetSeriesByIDs(ids []int) (map[int]*database.Series, error)
	GetBookTagsByBookIDs(bookIDs []string) (map[string][]string, error)
}

// BookRelations holds the author, series and tag rows a batch of books
// refers to, keyed the way BookToDocWithRelations looks them up. A nil
// *BookRelations or a missing key resolves to "no relation", exactly as
// a failed point read did in BookToDoc.
type BookRelations struct {
	Authors map[int]*database.Author
	Series  map[int]*database.Series
	Tags    map[string][]string
}

// LoadBookRelations resolves every distinct AuthorID and SeriesID in
// books plus the tags of every book with THREE store calls, however
// many books are passed. The returned *BookRelations is never nil: when
// a batch read fails the relation kind it covers is left empty and the
// error is returned alongside, so the caller can log it and still index
// the books best-effort — the same document shape a failed point read
// produced in BookToDoc, but with the failure visible instead of
// swallowed.
func LoadBookRelations(store relationsBatchStore, books []database.Book) (*BookRelations, error) {
	rel := &BookRelations{
		Authors: map[int]*database.Author{},
		Series:  map[int]*database.Series{},
		Tags:    map[string][]string{},
	}
	if store == nil || len(books) == 0 {
		return rel, nil
	}
	authorIDs := make([]int, 0, len(books))
	seriesIDs := make([]int, 0, len(books))
	bookIDs := make([]string, 0, len(books))
	seenAuthor := map[int]struct{}{}
	seenSeries := map[int]struct{}{}
	for i := range books {
		b := &books[i]
		bookIDs = append(bookIDs, b.ID)
		if b.AuthorID != nil {
			if _, ok := seenAuthor[*b.AuthorID]; !ok {
				seenAuthor[*b.AuthorID] = struct{}{}
				authorIDs = append(authorIDs, *b.AuthorID)
			}
		}
		if b.SeriesID != nil {
			if _, ok := seenSeries[*b.SeriesID]; !ok {
				seenSeries[*b.SeriesID] = struct{}{}
				seriesIDs = append(seriesIDs, *b.SeriesID)
			}
		}
	}
	var errs []error
	if len(authorIDs) > 0 {
		authors, err := store.GetAuthorsByIDs(authorIDs)
		if err != nil {
			errs = append(errs, fmt.Errorf("authors: %w", err))
		} else if authors != nil {
			rel.Authors = authors
		}
	}
	if len(seriesIDs) > 0 {
		series, err := store.GetSeriesByIDs(seriesIDs)
		if err != nil {
			errs = append(errs, fmt.Errorf("series: %w", err))
		} else if series != nil {
			rel.Series = series
		}
	}
	tags, err := store.GetBookTagsByBookIDs(bookIDs)
	if err != nil {
		errs = append(errs, fmt.Errorf("tags: %w", err))
	} else if tags != nil {
		rel.Tags = tags
	}
	return rel, errors.Join(errs...)
}

// BookToDocWithRelations projects a Book into a BookDocument using
// relations already resolved by LoadBookRelations, so building a page
// of documents touches the store zero times. The document produced is
// field-for-field identical to BookToDoc's for the same rows.
func BookToDocWithRelations(book *database.Book, rel *BookRelations) BookDocument {
	doc := bookToDocCore(book)
	if rel == nil {
		return doc
	}
	if book.AuthorID != nil {
		if author := rel.Authors[*book.AuthorID]; author != nil {
			doc.Author = author.Name
		}
	}
	if book.SeriesID != nil {
		if series := rel.Series[*book.SeriesID]; series != nil {
			doc.Series = series.Name
		}
	}
	if tags, ok := rel.Tags[book.ID]; ok {
		doc.Tags = tags
	}
	return doc
}

// BookToDoc resolves a Book's related rows through the Store and
// returns the flat BookDocument for indexing. Missing relations are
// silently skipped — the document is built best-effort.
//
// This is the single-book path (create/update hooks). Anything that
// walks many books must use LoadBookRelations + BookToDocWithRelations
// instead: this function costs three store reads per call.
func BookToDoc(store indexBuilderStore, book *database.Book) BookDocument {
	doc := bookToDocCore(book)

	// Resolve author name.
	if store != nil && book.AuthorID != nil {
		if author, err := store.GetAuthorByID(*book.AuthorID); err == nil && author != nil {
			doc.Author = author.Name
		}
	}
	// Resolve series name.
	if store != nil && book.SeriesID != nil {
		if series, err := store.GetSeriesByID(*book.SeriesID); err == nil && series != nil {
			doc.Series = series.Name
		}
	}
	// Resolve tags (user + system). Tags on a book come from the
	// existing BookTag / BookUserTag APIs.
	if store != nil {
		if tags, err := store.GetBookTags(book.ID); err == nil {
			doc.Tags = tags
		}
	}
	return doc
}

// bookToDocCore copies every field that lives on the Book row itself.
// Relations (author, series, tags) are layered on by the caller.
func bookToDocCore(book *database.Book) BookDocument {
	doc := BookDocument{
		BookID: book.ID,
		Type:   BookDocType,
		Title:  book.Title,
	}
	if book.Narrator != nil {
		doc.Narrator = *book.Narrator
	}
	if book.Publisher != nil {
		doc.Publisher = *book.Publisher
	}
	if book.Description != nil {
		doc.Description = truncateForIndex(*book.Description, descriptionLimit())
	}
	doc.FilePath = book.FilePath
	doc.Format = book.Format
	if book.Genre != nil {
		doc.Genre = *book.Genre
	}
	if book.Language != nil {
		doc.Language = *book.Language
	}
	if book.LibraryState != nil {
		doc.LibraryState = *book.LibraryState
	}
	if book.ISBN10 != nil {
		doc.ISBN10 = *book.ISBN10
	}
	if book.ISBN13 != nil {
		doc.ISBN13 = *book.ISBN13
	}
	if book.ASIN != nil {
		doc.ASIN = *book.ASIN
	}
	if book.AudiobookReleaseYear != nil {
		doc.Year = *book.AudiobookReleaseYear
	} else if book.PrintYear != nil {
		doc.Year = *book.PrintYear
	}
	if book.SeriesSequence != nil {
		doc.SeriesNumber = *book.SeriesSequence
	}
	if book.Duration != nil {
		doc.DurationSec = *book.Duration
	}
	if book.Bitrate != nil {
		doc.BitrateKbps = *book.Bitrate
	}
	if book.SampleRate != nil {
		doc.SampleRateHz = *book.SampleRate
	}
	if book.Channels != nil {
		doc.Channels = *book.Channels
	}
	if book.BitDepth != nil {
		doc.BitDepth = *book.BitDepth
	}
	if book.FileSize != nil {
		doc.FileSizeBytes = *book.FileSize
	}
	doc.HasCover = book.CoverURL != nil && *book.CoverURL != ""
	return doc
}

// ReindexBookByID convenience: load the book + project + index.
// Used by the update/create hook path; callers that already have a
// Book struct should call BookToDoc + IndexBook directly.
func ReindexBookByID(store indexBuilderStore, idx *BleveIndex, bookID string) error {
	if store == nil || idx == nil {
		return nil
	}
	book, err := store.GetBookByID(bookID)
	if err != nil || book == nil {
		return err
	}
	return idx.IndexBook(BookToDoc(store, book))
}
