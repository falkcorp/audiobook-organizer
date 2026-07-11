// file: internal/search/bleve_index.go
// version: 1.2.0
// guid: 3c8e1a2f-4d9b-4f70-a5c6-2f8d0e1b9a47
// last-edited: 2026-07-10
//
// BleveIndex is the single-package wrapper around a Bleve v2 scorch
// index backing library search (spec DES-1 / backlog §4.7). The
// public surface is intentionally small at this task-1 stage:
//
//   Open / Close  — lifecycle tied to Server startup
//   IndexBook     — (re-)index one BookDocument
//   DeleteBook    — remove by book ID
//   Search        — run a Bleve query string and return scored hits
//
// Richer surfaces (AST translator, auto-prefix on typeahead, per-user
// post-filter, index-build tracked op) land in subsequent tasks of
// the DES-1 plan.

package search

import (
	"fmt"
	"os"
	"sync"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/standard"
	"github.com/blevesearch/bleve/v2/analysis/lang/en"
	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/search/query"
)

// BleveIndex wraps a bleve.Index with a small, opinionated API tuned
// to this project. Concurrency: Bleve indexes are safe for
// concurrent reads + writes, so we don't need an explicit lock on
// the hot path. A mutex guards open/close transitions so shutdown
// doesn't race with in-flight writes.
type BleveIndex struct {
	mu   sync.RWMutex
	idx  bleve.Index
	path string
}

// Open creates or opens the on-disk Bleve index at path using the
// scorch backend. Index mapping is set up once at creation time;
// reopening an existing index uses the mapping stored alongside it.
func Open(path string) (*BleveIndex, error) {
	// Try opening an existing index first. If it doesn't exist,
	// create a new one with the book mapping.
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		idx, err := bleve.Open(path)
		if err != nil {
			return nil, fmt.Errorf("bleve open existing at %s: %w", path, err)
		}
		return &BleveIndex{idx: idx, path: path}, nil
	}

	m := bookIndexMapping()
	idx, err := bleve.NewUsing(path, m, "scorch", "scorch", nil)
	if err != nil {
		return nil, fmt.Errorf("bleve create at %s: %w", path, err)
	}
	return &BleveIndex{idx: idx, path: path}, nil
}

// Close releases the underlying index handle. Safe to call multiple
// times.
func (b *BleveIndex) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.idx == nil {
		return nil
	}
	err := b.idx.Close()
	b.idx = nil
	return err
}

// IndexBook indexes (or re-indexes) a single BookDocument. The
// document's BookID is used as the Bleve doc ID so subsequent indexes
// overwrite the previous version.
func (b *BleveIndex) IndexBook(doc BookDocument) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.idx == nil {
		return fmt.Errorf("bleve index not open")
	}
	if doc.BookID == "" {
		return fmt.Errorf("BookID required for indexing")
	}
	if doc.Type == "" {
		doc.Type = BookDocType
	}
	return b.idx.Index(doc.BookID, doc)
}

// IndexBookBatch indexes multiple BookDocuments in a single batch commit.
// Much faster than calling IndexBook in a loop when adding many documents.
func (b *BleveIndex) IndexBookBatch(docs []BookDocument) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.idx == nil {
		return fmt.Errorf("bleve index not open")
	}
	batch := b.idx.NewBatch()
	for i := range docs {
		if docs[i].BookID == "" {
			continue
		}
		if docs[i].Type == "" {
			docs[i].Type = BookDocType
		}
		if err := batch.Index(docs[i].BookID, docs[i]); err != nil {
			return err
		}
	}
	return b.idx.Batch(batch)
}

// DeleteBook removes the book with the given ID from the index. No-op
// if the ID wasn't indexed.
func (b *BleveIndex) DeleteBook(bookID string) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.idx == nil {
		return fmt.Errorf("bleve index not open")
	}
	return b.idx.Delete(bookID)
}

// SearchResult is a scored hit with the raw matched book ID plus
// any highlighted fragments Bleve returned per field.
type SearchResult struct {
	BookID     string
	Score      float64
	Highlights map[string][]string
}

// Search runs a Bleve query-string query and returns up to `size`
// results starting at `from`. For the full DSL → Bleve translator,
// see subsequent DES-1 plan tasks; this method is the basic access
// point used by task-1 tests.
func (b *BleveIndex) Search(queryString string, from, size int) ([]SearchResult, uint64, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.idx == nil {
		return nil, 0, fmt.Errorf("bleve index not open")
	}
	if size <= 0 {
		size = 20
	}
	q := bleve.NewQueryStringQuery(queryString)
	req := bleve.NewSearchRequestOptions(q, size, from, false)
	req.Highlight = bleve.NewHighlight()
	res, err := b.idx.Search(req)
	if err != nil {
		return nil, 0, err
	}
	out := make([]SearchResult, 0, len(res.Hits))
	for _, hit := range res.Hits {
		out = append(out, SearchResult{
			BookID:     hit.ID,
			Score:      hit.Score,
			Highlights: hit.Fragments,
		})
	}
	return out, res.Total, nil
}

// SearchNative runs a pre-built query.Query (typically produced by
// the AST → Bleve translator) against the index. Used by smart
// playlists and the library search path after DSL translation.
func (b *BleveIndex) SearchNative(q query.Query, from, size int) ([]SearchResult, uint64, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.idx == nil {
		return nil, 0, fmt.Errorf("bleve index not open")
	}
	if q == nil {
		return nil, 0, fmt.Errorf("nil query")
	}
	if size <= 0 {
		size = 20
	}
	req := bleve.NewSearchRequestOptions(q, size, from, false)
	req.Highlight = bleve.NewHighlight()
	res, err := b.idx.Search(req)
	if err != nil {
		return nil, 0, err
	}
	out := make([]SearchResult, 0, len(res.Hits))
	for _, hit := range res.Hits {
		out = append(out, SearchResult{
			BookID:     hit.ID,
			Score:      hit.Score,
			Highlights: hit.Fragments,
		})
	}
	return out, res.Total, nil
}

// DocCount returns the number of documents currently indexed. Useful
// for readiness checks and tests.
func (b *BleveIndex) DocCount() (uint64, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.idx == nil {
		return 0, fmt.Errorf("bleve index not open")
	}
	return b.idx.DocCount()
}

// textFieldBoosts is the query-time boost table for free-text search.
// bleve v2 has no index-time field boost, so translateFreeText fans a
// free-text term out across these fields with these weights. Keep in
// sync with the analyzed-text fields registered in bookIndexMapping.
var textFieldBoosts = []struct {
	Field string
	Boost float64
}{
	{"title", 3.0}, {"author", 2.0}, {"series", 1.5}, {"narrator", 1.2},
	{"publisher", 1.0}, {"description", 0.5}, {"file_path", 0.5},
}

// bookIndexMapping returns the bleve.IndexMapping for BookDocument.
// Analyzer choices and keyword vs text distinctions live here —
// changing a field's treatment requires rebuilding the index (full
// re-index on next startup). Free-text field weighting is query-time
// (see textFieldBoosts above), not part of this mapping.
func bookIndexMapping() mapping.IndexMapping {
	im := bleve.NewIndexMapping()

	// Use bleve's stock English analyzer — lowercases, drops stop
	// words, stems, and (via the registered `en` package) ascii-folds.
	// Building a custom analyzer at mapping construction time is
	// brittle because the registry lookup happens at index open, so
	// we stick to the guaranteed-available built-in.
	// textAnalyzed no longer takes a boost: bleve v2 has no index-time
	// field boost, so that parameter was dead. Query-time weighting for
	// free-text search lives in textFieldBoosts (bleve_index.go) and is
	// applied by translateFreeText in bleve_translator.go.
	textAnalyzed := func() *mapping.FieldMapping {
		f := bleve.NewTextFieldMapping()
		f.Analyzer = en.AnalyzerName
		f.Store = true
		f.IncludeInAll = true
		return f
	}
	// Keyword (no analyzer, exact match)
	keyword := func() *mapping.FieldMapping {
		f := bleve.NewTextFieldMapping()
		f.Analyzer = standard.Name
		f.Store = true
		return f
	}
	numeric := func() *mapping.FieldMapping {
		f := bleve.NewNumericFieldMapping()
		f.Store = true
		return f
	}
	boolean := func() *mapping.FieldMapping {
		f := bleve.NewBooleanFieldMapping()
		f.Store = true
		return f
	}

	book := bleve.NewDocumentMapping()

	// Analyzed text fields. Field weighting for free-text search is
	// applied at query time (see textFieldBoosts below), not here.
	title := textAnalyzed()
	author := textAnalyzed()
	series := textAnalyzed()
	narrator := textAnalyzed()
	publisher := textAnalyzed()
	description := textAnalyzed()
	filePath := textAnalyzed()

	book.AddFieldMappingsAt("title", title)
	book.AddFieldMappingsAt("author", author)
	book.AddFieldMappingsAt("narrator", narrator)
	book.AddFieldMappingsAt("series", series)
	book.AddFieldMappingsAt("publisher", publisher)
	book.AddFieldMappingsAt("description", description)
	book.AddFieldMappingsAt("file_path", filePath)

	// Tags — array of keywords
	book.AddFieldMappingsAt("tags", keyword())

	// Keyword / exact
	book.AddFieldMappingsAt("format", keyword())
	book.AddFieldMappingsAt("genre", keyword())
	book.AddFieldMappingsAt("language", keyword())
	book.AddFieldMappingsAt("library_state", keyword())
	book.AddFieldMappingsAt("isbn10", keyword())
	book.AddFieldMappingsAt("isbn13", keyword())
	book.AddFieldMappingsAt("asin", keyword())
	book.AddFieldMappingsAt("_type", keyword())

	// Numeric
	book.AddFieldMappingsAt("year", numeric())
	book.AddFieldMappingsAt("series_number", numeric())
	book.AddFieldMappingsAt("duration_seconds", numeric())
	book.AddFieldMappingsAt("bitrate_kbps", numeric())
	book.AddFieldMappingsAt("sample_rate_hz", numeric())
	book.AddFieldMappingsAt("channels", numeric())
	book.AddFieldMappingsAt("bit_depth", numeric())
	book.AddFieldMappingsAt("file_size_bytes", numeric())

	// Boolean
	book.AddFieldMappingsAt("has_cover", boolean())

	im.AddDocumentMapping(BookDocType, book)
	im.DefaultAnalyzer = en.AnalyzerName
	im.TypeField = "_type"
	im.DefaultType = BookDocType

	return im
}
