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

// AnalyzerName is HEAD's free-text analyzer (internal/search bookIndexMapping's
// book_en_nostop), declared in Spec.Analyzers and named by every free-text field.
const AnalyzerName = "book_en_nostop"

// Analyzers is the index's own analyzer set: HEAD's book_en_nostop, component
// for component. '_' becomes a space before tokenizing (so "Arcane_Chef_2" is
// three words), then Unicode words, possessive stripped, lowercased and Porter
// stemmed, with no stop filter (a quoted phrase keeps its stop words in place).
//
// Unlike HEAD's bookIndexMapping there is no fallback to the stock English
// analyzer: core refuses a component it cannot build at Open (ErrInvalidSpec)
// rather than indexing with a default.
func Analyzers() []index.Analyzer {
	return []index.Analyzer{{
		Name:         AnalyzerName,
		CharFilters:  []index.CharFilter{index.CharUnderscoreToSpace},
		Tokenizer:    index.TokenizeUnicodeWords,
		TokenFilters: []index.TokenFilter{index.FilterEnglishPossessive, index.FilterLowercase, index.FilterPorterStem},
	}}
}

// freeTextFields are HEAD's analysed free-text fields (bookIndexMapping's
// textAnalyzed set); each names AnalyzerName.
var freeTextFields = []string{
	FieldTitle, FieldAuthor, FieldSeries, FieldNarrator, FieldPublisher,
	FieldDescription, FieldFilePath,
}

// keywordFields are HEAD's "keyword" fields (bookIndexMapping's keyword()).
var keywordFields = []string{
	FieldTags, FieldFormat, FieldGenre, FieldLanguage, FieldLibraryState,
	FieldISBN10, FieldISBN13, FieldASIN,
}

// Fields is the index's declared field set, mirroring HEAD's bookIndexMapping.
//
// The free-text fields name AnalyzerName (see Analyzers).
//
// No field declares a Field.Boost. Score weights live in the query: Translate
// carries the weights of HEAD's tree (the free-text fan-out's and the user's
// ^n) and emits index.Boost on every node HEAD weighted, so scores match HEAD
// exactly. A declared Field.Boost would also weight a fielded title:x at 3,
// which HEAD does not (seams.d/F7-09-T6-core.md ruling 2). Moving HEAD's
// textFieldBoosts onto the fields is a later, deliberate ranking change.
//
// HEAD's "keyword" fields (tags, format, genre, language, library_state, isbn,
// asin) are not keywords: HEAD analyses them with the standard analyzer (word
// tokens, lowercased, English stop words dropped; see the comment on keyword()
// in internal/search/bleve_index.go), so a term on one matches by word, not by
// whole value. They are declared Kind: index.Keyword with
// Analyzer: index.AnalyzerStandard, which core accepts, so the same queries
// answer the same way. Switching them to index.AnalyzerKeywordLowercase changes
// answers (genre:fantasy would stop matching "Epic Fantasy") and is its own
// change with its own MappingVersion bump (ruling 1 of seams.d/F7-09-T6-core.md).
func Fields() []index.Field {
	var fs []index.Field
	for _, n := range freeTextFields {
		fs = append(fs, index.Field{Name: n, Kind: index.Text, Analyzer: AnalyzerName})
	}
	for _, n := range keywordFields {
		fs = append(fs, index.Field{Name: n, Kind: index.Keyword, Analyzer: index.AnalyzerStandard})
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
