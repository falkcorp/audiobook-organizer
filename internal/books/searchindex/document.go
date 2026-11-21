// file: internal/books/searchindex/document.go
// version: 0.1.0
// guid: 55528791-664a-4539-ba05-93eabfce931b
// last-edited: 2026-11-20

package searchindex

import (
	"github.com/falkcorp/mediacore/storage/index"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// Source is the narrow slice of the book store the index reads: one keyset page
// walk for the rebuild, the point reads for one document, and the three batch
// reads that resolve author, series and tag names. database.Store satisfies it.
type Source interface {
	GetAllBooksFullFrom(afterID string, limit int) ([]database.Book, error)
	GetBookByID(id string) (*database.Book, error)
	GetAuthorsByIDs(ids []int) (map[int]*database.Author, error)
	GetSeriesByIDs(ids []int) (map[int]*database.Series, error)
	GetBookTagsByBookIDs(bookIDs []string) (map[string][]string, error)
}

// Document builds the index document of one book. Zero values are omitted, as
// in HEAD's omitempty BookDocument, so a book with no year indexes no year (a
// year:<2000 query must not match it). Numbers are int64, the date fields are
// time.Time (UTC) and tags are a multi-valued string field. HEAD's "_type"
// marker is dropped: the contract reserves "_" names and an index holds only
// books.
//
// It reuses search.BookToDocWithRelations, so a field's value is whatever HEAD
// indexes today; the fold carries that function's body over when it retires
// internal/search.
func Document(book *database.Book, rel *search.BookRelations) index.Doc {
	return docFromBookDocument(search.BookToDocWithRelations(book, rel))
}

func docFromBookDocument(b search.BookDocument) index.Doc {
	d := index.Doc{}
	str := func(field, v string) {
		if v != "" {
			d[field] = v
		}
	}
	num := func(field string, v int64) {
		if v != 0 {
			d[field] = v
		}
	}
	str(FieldTitle, b.Title)
	str(FieldAuthor, b.Author)
	str(FieldNarrator, b.Narrator)
	str(FieldSeries, b.Series)
	str(FieldPublisher, b.Publisher)
	str(FieldDescription, b.Description)
	str(FieldFilePath, b.FilePath)
	if len(b.Tags) > 0 {
		d[FieldTags] = append([]string(nil), b.Tags...)
	}
	str(FieldFormat, b.Format)
	str(FieldGenre, b.Genre)
	str(FieldLanguage, b.Language)
	str(FieldLibraryState, b.LibraryState)
	str(FieldISBN10, b.ISBN10)
	str(FieldISBN13, b.ISBN13)
	str(FieldASIN, b.ASIN)
	num(FieldYear, int64(b.Year))
	num(FieldSeriesNumber, int64(b.SeriesNumber))
	num(FieldDuration, int64(b.DurationSec))
	num(FieldBitrate, int64(b.BitrateKbps))
	num(FieldSampleRate, int64(b.SampleRateHz))
	num(FieldChannels, int64(b.Channels))
	num(FieldBitDepth, int64(b.BitDepth))
	num(FieldFileSize, b.FileSizeBytes)
	if b.DateAdded != nil {
		d[FieldDateAdded] = b.DateAdded.UTC()
	}
	if b.DateModified != nil {
		d[FieldDateModified] = b.DateModified.UTC()
	}
	if b.HasCover {
		d[FieldHasCover] = true
	}
	return d
}
