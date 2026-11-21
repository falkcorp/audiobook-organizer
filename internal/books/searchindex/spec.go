// file: internal/books/searchindex/spec.go
// version: 0.1.0
// guid: cb0ec592-4f72-4552-9dc1-2c389fada6ae
// last-edited: 2026-11-20

// Package searchindex is books' native search index (F7-09 T6): the full-text
// index declaration over core's storage/index, the document builder, the query
// translator, and the declarations of the three app-level derived indexes
// (memdb, hnsw, chromem) with their warm-start hooks.
//
// It is dark: nothing outside its own tests calls it, and internal/search stays
// the live implementation until the F7-09 fold registers these declarations and
// retires that package. The fold-time seams are listed in
// docs/agent-tasks/platform-core/seams.d/F7-09-T6.md.
package searchindex

import (
	"github.com/falkcorp/mediacore/storage/index"
)

const (
	// Module is the books module's name: the index lives at
	// <db parent>/index/books/books.bleve.
	Module = "books"
	// Name is the full-text index's name inside the module.
	Name = "books"
	// MappingVersion is HEAD's bookMappingVersion carried over unchanged. It
	// changes when this field set or an analyzer changes (the engine drops and
	// rebuilds on a change). The first R2 boot builds the index with no earlier
	// one to compare against, so the value is only a baseline.
	MappingVersion = "4"
)

// Indexed field names, the JSON names of search.BookDocument.
const (
	FieldTitle        = "title"
	FieldAuthor       = "author"
	FieldNarrator     = "narrator"
	FieldSeries       = "series"
	FieldPublisher    = "publisher"
	FieldDescription  = "description"
	FieldFilePath     = "file_path"
	FieldTags         = "tags"
	FieldFormat       = "format"
	FieldGenre        = "genre"
	FieldLanguage     = "language"
	FieldLibraryState = "library_state"
	FieldISBN10       = "isbn10"
	FieldISBN13       = "isbn13"
	FieldASIN         = "asin"
	FieldYear         = "year"
	FieldSeriesNumber = "series_number"
	FieldDuration     = "duration_seconds"
	FieldBitrate      = "bitrate_kbps"
	FieldSampleRate   = "sample_rate_hz"
	FieldChannels     = "channels"
	FieldBitDepth     = "bit_depth"
	FieldFileSize     = "file_size_bytes"
	FieldDateAdded    = "date_added"
	FieldDateModified = "date_modified"
	FieldHasCover     = "has_cover"
)

// textFields are the fields an unfielded term is matched against: HEAD's _all
// (every text-analysed field, the "keyword" ones included, because HEAD analyses
// those with the standard analyzer too). The order is HEAD's textFieldBoosts
// order first, then the rest.
var textFields = []string{
	FieldTitle, FieldAuthor, FieldSeries, FieldNarrator, FieldDescription,
	FieldPublisher, FieldFilePath,
	FieldTags, FieldFormat, FieldGenre, FieldLanguage, FieldLibraryState,
	FieldISBN10, FieldISBN13, FieldASIN,
}

// TextFields returns the fields an unfielded term fans out over.
func TextFields() []string { return append([]string(nil), textFields...) }

// Fields is the index's declared field set, mirroring HEAD's bookIndexMapping.
//
// HEAD's "keyword" fields (tags, format, genre, language, library_state, isbn,
// asin) are analysed with the standard analyzer, not the keyword analyzer: a
// term on one matches by word, not by whole value (see the comment on keyword()
// in internal/search/bleve_index.go). They are declared Text here, so the same
// queries answer the same way.
//
// The free-text fields use "standard" because the contract has no module
// analyzer: HEAD's book_en_nostop (stemming, underscore splitting, stop words
// kept) is a seam, "switch at the fold".
func Fields() []index.Field {
	var fs []index.Field
	for _, n := range textFields {
		fs = append(fs, index.Field{Name: n, Kind: index.Text, Analyzer: "standard"})
	}
	for _, n := range []string{
		FieldYear, FieldSeriesNumber, FieldDuration, FieldBitrate,
		FieldSampleRate, FieldChannels, FieldBitDepth, FieldFileSize,
	} {
		fs = append(fs, index.Field{Name: n, Kind: index.Number})
	}
	fs = append(fs,
		index.Field{Name: FieldDateAdded, Kind: index.Date},
		index.Field{Name: FieldDateModified, Kind: index.Date},
		index.Field{Name: FieldHasCover, Kind: index.Bool},
	)
	return fs
}
