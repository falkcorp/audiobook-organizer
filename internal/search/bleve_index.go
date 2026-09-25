// file: internal/search/bleve_index.go
// version: 1.6.0
// guid: 3c8e1a2f-4d9b-4f70-a5c6-2f8d0e1b9a47
// last-edited: 2026-09-25
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
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/custom"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/standard"
	regexpchar "github.com/blevesearch/bleve/v2/analysis/char/regexp"
	"github.com/blevesearch/bleve/v2/analysis/lang/en"
	"github.com/blevesearch/bleve/v2/analysis/token/lowercase"
	"github.com/blevesearch/bleve/v2/analysis/token/porter"
	"github.com/blevesearch/bleve/v2/analysis/tokenizer/unicode"
	"github.com/blevesearch/bleve/v2/index/scorch"
	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/search/query"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

var indexLog = logger.New("search-index")

// asyncErrorCallbackName is the scorch registry key for onAsyncError.
//
// WHY: scorch's merger and persister run in their own goroutines and report
// failures ONLY through config["asyncErrorCallbackName"]. With no callback
// registered (the state until 2026-09-25) every failure is counted in a stat
// and otherwise discarded. Production ran 92 consecutive failed file-merge
// plans in 13 minutes with zero log lines; the persister, which waits for the
// merger once the index directory holds >= 1000 files, paused forever, and
// every safe Batch/Index/Delete blocked on it. The whole search-index write
// path wedged and nothing said why.
const asyncErrorCallbackName = "audiobook-organizer"

// asyncErrors counts scorch async errors for the process lifetime.
var asyncErrors atomic.Int64

// lastAsyncErrorLog rate-limits the Error line: a failing merge plan retries
// in a tight loop and would otherwise log hundreds of identical lines.
var lastAsyncErrorLog atomic.Int64

func init() {
	scorch.RegistryAsyncErrorCallbacks[asyncErrorCallbackName] = func(err error, path string) {
		n := asyncErrors.Add(1)
		now := time.Now().UnixNano()
		last := lastAsyncErrorLog.Load()
		if n > 1 && now-last < int64(time.Minute) {
			return
		}
		if !lastAsyncErrorLog.CompareAndSwap(last, now) {
			return
		}
		indexLog.Error("search index background error (scorch merger/persister): %v (path=%s, total_async_errors=%d)",
			err, logger.SanitizeLogValue(path), n)
	}
}

// AsyncErrorCount reports scorch async errors seen since process start.
func AsyncErrorCount() int64 { return asyncErrors.Load() }

// runtimeConfig is passed on EVERY open, not just create: bleve.Open with no
// config would drop the callback on the second boot of any index.
func runtimeConfig() map[string]interface{} {
	return map[string]interface{}{"asyncErrorCallbackName": asyncErrorCallbackName}
}

// BleveIndex wraps a bleve.Index with a small, opinionated API tuned
// to this project. Concurrency: Bleve indexes are safe for
// concurrent reads + writes, so we don't need an explicit lock on
// the hot path. A mutex guards open/close transitions so shutdown
// doesn't race with in-flight writes.
type BleveIndex struct {
	mu   sync.RWMutex
	idx  bleve.Index
	path string

	// recreatedForMapping records that Open threw away an existing index
	// because its mapping version was stale. Read via
	// RecreatedForMappingChange; set once at Open and never mutated after.
	recreatedForMapping bool

	// rebuilding is true from the moment an index is created empty until
	// MarkRebuilt: the index does not yet cover the library, so callers must
	// not treat a miss as "no such book". Backed by a sibling marker file so
	// it survives a restart mid-drain (RecreatedForMappingChange does not).
	rebuilding atomic.Bool

	// inflight tracks the start time of every write currently inside bleve,
	// so a watchdog can see a write that never returns (see OldestWrite).
	inflightMu  sync.Mutex
	inflightSeq uint64
	inflight    map[uint64]time.Time
}

// bookTextAnalyzerName is the analyzer every free-text field is indexed
// with. It is bleve's stock English chain MINUS the stopword filter:
// unicode tokenizer, possessive stripper, lowercase, porter stemmer.
//
// # WHY NOT THE STOCK `en` ANALYZER
//
// The stop filter deletes tokens without renumbering the positions of the
// survivors, and MatchPhraseQuery rebuilds the phrase from those positions
// (tokenStreamToPhrase, bleve search/query/match_phrase.go), sizing it
// lastPosition-firstPosition+1 and leaving unfilled slots nil. So under the
// stock analyzer:
//
//	"All Jobs"          -> [jobs@2]          -> phrase of length 1, i.e. NO
//	                                            adjacency constraint at all
//	"Lord of the Rings" -> [lord@1, ring@4]  -> length 4 with slots 2-3 nil,
//	                                            i.e. "Lord ANY ANY Rings"
//
// Measured on production 2026-08-13: `"All Jobs"` returned 300 rows, a set
// byte-identical to the unquoted query. A leading stopword collapses a
// phrase to a bare term; an interior one turns into a wildcard.
//
// Keeping stopwords in the index costs term-dictionary space and makes
// unquoted conjunctions stricter in principle — but only in principle here,
// because dropStopwordOnlyConjuncts (bleve_translator.go) still removes them
// from unquoted queries, and it detects them with the STOCK analyzer resolved
// by name, independently of this mapping. That decoupling is load-bearing:
// it is what keeps unquoted recall identical across this change.
const bookTextAnalyzerName = "book_en_nostop"

// bookMappingVersion identifies the on-disk index mapping. Bleve persists the
// mapping inside the index and bleve.Open uses the STORED one, so changing
// bookIndexMapping() has no effect on an index that already exists — the
// change only takes hold on a freshly created index. Bump this whenever
// bookIndexMapping changes in a way that alters indexed terms, and Open will
// recreate the index.
//
//	1 (implicit, unmarked) — stock `en` analyzer on all text fields
//	2                      — bookTextAnalyzerName, stopwords preserved
//	3                      — '_' mapped to a space before tokenising, so
//	                         "Arcane_Chef_2__A_LitRPG" is five tokens, not one
//
// A bump deletes the on-disk index and rebuilds it through the durable dirty
// set; while that runs the index reports Rebuilding and library search uses
// the substring path (see audiobooks.AudiobookService.bleveSearchable).
const bookMappingVersion = "3"

// underscoreCharFilterName maps '_' to ' ' ahead of the unicode tokenizer.
// The tokenizer follows Unicode word-break rules, under which '_' is an
// ExtendNumLet word-joiner, so a filename-derived title such as
// "Arcane_Chef_2__A_LitRPG_Adventure" indexed as ONE token and no word
// query could reach it. database.searchFold does the same fold for the
// substring path (#3559).
const underscoreCharFilterName = "underscore_to_space"

// mappingMarkerPath returns the sibling file recording which mapping version
// built the index. Deliberately a SIBLING of the index directory rather than a
// file inside it: bleve owns that directory's contents and scorch enumerates
// its segment files, so an unexpected entry there is asking for trouble.
func mappingMarkerPath(indexPath string) string {
	return filepath.Join(filepath.Dir(indexPath),
		filepath.Base(indexPath)+".mapping")
}

// Open creates or opens the on-disk Bleve index at path using the scorch
// backend.
//
// When an existing index was built by a different mapping version it is
// DELETED and recreated empty, and RecreatedForMappingChange reports true.
// Callers must react to that: see server_lifecycle.go, which uses it to skip
// the non-resumable bulk backfill and seed the durable dirty set instead.
func Open(path string) (*BleveIndex, error) {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		stored := readMappingMarker(path)
		if stored == bookMappingVersion {
			idx, err := bleve.OpenUsing(path, runtimeConfig())
			if err != nil {
				return nil, fmt.Errorf("bleve open existing at %s: %w", path, err)
			}
			b := &BleveIndex{idx: idx, path: path}
			if _, err := os.Stat(rebuildingMarkerPath(path)); err == nil {
				b.rebuilding.Store(true)
			}
			return b, nil
		}
		slog.Warn("search index mapping version changed; recreating index. "+
			"Library search uses the substring path until the reconciler "+
			"drains the dirty set.",
			"path", path, "stored", stored, "want", bookMappingVersion)
		if err := os.RemoveAll(path); err != nil {
			return nil, fmt.Errorf("bleve remove stale index at %s: %w", path, err)
		}
		b, err := createIndex(path)
		if err != nil {
			return nil, err
		}
		b.recreatedForMapping = true
		return b, nil
	}

	return createIndex(path)
}

// createIndex builds a new index and records its mapping version.
//
// The marker is written only AFTER a successful create. Writing it first
// would, on a create failure, leave a marker with no index — and writing it
// unconditionally on failure would be worse still: a marker that cannot be
// written must not be treated as written, or every boot recreates the index
// forever.
func createIndex(path string) (*BleveIndex, error) {
	// Rebuilding marker FIRST: a crash between create and the drain finishing
	// must come back as "rebuilding", never as a complete index.
	rebuildMarker := rebuildingMarkerPath(path)
	if err := os.WriteFile(rebuildMarker, []byte("1\n"), 0o644); err != nil {
		indexLog.Error("search: failed to write rebuilding marker %s: %v; a restart mid-rebuild will serve partial search results",
			logger.SanitizeLogValue(rebuildMarker), err)
	}
	idx, err := bleve.NewUsing(path, bookIndexMapping(), "scorch", "scorch", runtimeConfig())
	if err != nil {
		return nil, fmt.Errorf("bleve create at %s: %w", path, err)
	}
	marker := mappingMarkerPath(path)
	if err := os.WriteFile(marker, []byte(bookMappingVersion+"\n"), 0o644); err != nil {
		// Not fatal — the index is valid and usable. But it WILL be
		// recreated on every restart until this succeeds, so it must be
		// loud rather than a debug line nobody reads.
		slog.Error("search: failed to write index mapping marker; the index "+
			"will be rebuilt on EVERY restart until this is fixed",
			"marker", marker, "err", err)
	}
	b := &BleveIndex{idx: idx, path: path}
	b.rebuilding.Store(true)
	return b, nil
}

// rebuildingMarkerPath is the sibling file that marks an index as still
// being filled. Sibling, not inside the index dir, for the same reason as
// mappingMarkerPath.
func rebuildingMarkerPath(indexPath string) string {
	return filepath.Join(filepath.Dir(indexPath),
		filepath.Base(indexPath)+".rebuilding")
}

// Rebuilding reports whether the index was created empty and has not yet
// been confirmed complete by MarkRebuilt.
func (b *BleveIndex) Rebuilding() bool {
	if b == nil {
		return false
	}
	return b.rebuilding.Load()
}

// MarkRebuilt records that the index now covers the library. The caller
// (the server's reconciler) decides that: coverage was seeded in this
// process AND the dirty set drained to zero.
func (b *BleveIndex) MarkRebuilt() error {
	if b == nil || !b.rebuilding.Load() {
		return nil
	}
	if err := os.Remove(rebuildingMarkerPath(b.path)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove rebuilding marker: %w", err)
	}
	b.rebuilding.Store(false)
	return nil
}

// trackWrite registers an in-flight write and returns its release func.
func (b *BleveIndex) trackWrite() func() {
	b.inflightMu.Lock()
	if b.inflight == nil {
		b.inflight = map[uint64]time.Time{}
	}
	b.inflightSeq++
	id := b.inflightSeq
	b.inflight[id] = time.Now()
	b.inflightMu.Unlock()
	return func() {
		b.inflightMu.Lock()
		delete(b.inflight, id)
		b.inflightMu.Unlock()
	}
}

// OldestWrite reports how long the oldest still-running write has been
// inside bleve, and how many writes are in flight. A safe scorch write
// returns only after persistence, so a large value means the persister is
// not making progress.
func (b *BleveIndex) OldestWrite() (time.Duration, int) {
	if b == nil {
		return 0, 0
	}
	b.inflightMu.Lock()
	defer b.inflightMu.Unlock()
	var oldest time.Duration
	now := time.Now()
	for _, t := range b.inflight {
		if d := now.Sub(t); d > oldest {
			oldest = d
		}
	}
	return oldest, len(b.inflight)
}

// HealthStats returns the scorch counters that diagnose a stalled write
// path: file count against the persister's 1000-file pause threshold, and
// merge plan outcomes. Missing keys are omitted.
func (b *BleveIndex) HealthStats() map[string]uint64 {
	out := map[string]uint64{}
	if b == nil {
		return out
	}
	b.mu.RLock()
	idx := b.idx
	b.mu.RUnlock()
	if idx == nil {
		return out
	}
	sm, ok := idx.StatsMap()["index"].(map[string]interface{})
	if !ok {
		return out
	}
	for _, k := range []string{"CurOnDiskFiles", "TotFileSegmentsAtRoot", "TotFileMergePlanTasksErr",
		"TotFileMergePlanTasksDone", "LastMergedEpoch", "LastPersistedEpoch", "CurRootEpoch",
		"TotPersisterSlowMergerPause", "TotPersisterSlowMergerResume"} {
		if v, ok := sm[k].(uint64); ok {
			out[k] = v
		}
	}
	return out
}

// readMappingMarker returns the recorded mapping version, or "" when the
// marker is absent or unreadable. "" is the correct answer for an index built
// before markers existed (mapping version 1) and forces a recreate.
func readMappingMarker(indexPath string) string {
	b, err := os.ReadFile(mappingMarkerPath(indexPath))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// RecreatedForMappingChange reports whether Open discarded an existing index
// because it had been built with an older mapping. When true the index is
// EMPTY and every book needs re-indexing.
func (b *BleveIndex) RecreatedForMappingChange() bool {
	if b == nil {
		return false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.recreatedForMapping
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
	defer b.trackWrite()()
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
	defer b.trackWrite()()
	return b.idx.Batch(batch)
}

// ApplyBatch upserts docs and deletes deleteIDs in ONE bleve batch, i.e. one
// segment and one wait for persistence. It returns only once the batch is
// durable (scorch's safe-batch mode), so a caller may clear a durable
// "needs re-index" record after a nil return and not before.
func (b *BleveIndex) ApplyBatch(docs []BookDocument, deleteIDs []string) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.idx == nil {
		return fmt.Errorf("bleve index not open")
	}
	if len(docs) == 0 && len(deleteIDs) == 0 {
		return nil
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
			return fmt.Errorf("batch index %s: %w", docs[i].BookID, err)
		}
	}
	for _, id := range deleteIDs {
		if id != "" {
			batch.Delete(id)
		}
	}
	defer b.trackWrite()()
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
	defer b.trackWrite()()
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

// FacetCounts returns value->count maps for the genre, language, and
// tags keyword fields via a MatchAll facet search. size caps distinct
// values per facet; <=0 defaults to 200. A facet with no matching
// documents yields an empty map, never nil (e.g. an empty index).
func (b *BleveIndex) FacetCounts(size int) (genres, languages, tags map[string]int, err error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.idx == nil {
		return nil, nil, nil, fmt.Errorf("bleve index not open")
	}
	if size <= 0 {
		size = 200
	}
	req := bleve.NewSearchRequestOptions(bleve.NewMatchAllQuery(), 0, 0, false)
	req.AddFacet("genres", bleve.NewFacetRequest("genre", size))
	req.AddFacet("languages", bleve.NewFacetRequest("language", size))
	req.AddFacet("tags", bleve.NewFacetRequest("tags", size))
	res, err := b.idx.Search(req)
	if err != nil {
		return nil, nil, nil, err
	}
	return facetTermCounts(res, "genres"), facetTermCounts(res, "languages"), facetTermCounts(res, "tags"), nil
}

// facetTermCounts extracts one named term-facet's value->count map from a
// Bleve search result. A missing facet (e.g. zero indexed documents) yields
// an empty map, never nil.
func facetTermCounts(res *bleve.SearchResult, facetName string) map[string]int {
	out := map[string]int{}
	fr := res.Facets[facetName]
	if fr == nil || fr.Terms == nil {
		return out
	}
	for _, t := range fr.Terms.Terms() {
		out[t.Term] = t.Count
	}
	return out
}

// AllDocIDs returns the external document ID of every indexed document,
// via the low-level DocIDReader — one sequential pass, no scoring, no
// document loading. It exists for the boot coverage gate, which needs the
// index's ID SET: DocCount alone cannot distinguish "every live book is
// indexed" from "the count is padded by documents whose books are gone"
// (measured on production 2026-08-14: 67,824 docs vs 63,871 live books —
// the surplus was 3,953 soft-deleted books indexed by a backfill that
// enumerated with a trash-including lister).
func (b *BleveIndex) AllDocIDs() ([]string, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.idx == nil {
		return nil, fmt.Errorf("bleve index not open")
	}
	advanced, err := b.idx.Advanced()
	if err != nil {
		return nil, fmt.Errorf("advanced index access: %w", err)
	}
	reader, err := advanced.Reader()
	if err != nil {
		return nil, fmt.Errorf("index reader: %w", err)
	}
	defer func() { _ = reader.Close() }()
	itr, err := reader.DocIDReaderAll()
	if err != nil {
		return nil, fmt.Errorf("doc id reader: %w", err)
	}
	defer func() { _ = itr.Close() }()
	var out []string
	for {
		internalID, err := itr.Next()
		if err != nil {
			return nil, fmt.Errorf("doc id iterate: %w", err)
		}
		if internalID == nil {
			return out, nil
		}
		extID, err := reader.ExternalID(internalID)
		if err != nil {
			return nil, fmt.Errorf("external id: %w", err)
		}
		out = append(out, extID)
	}
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

	// Register the stopword-preserving analyzer this mapping uses for
	// every free-text field. See bookTextAnalyzerName for why the stock
	// `en` analyzer is not used. A failure here can only mean a bleve
	// upgrade renamed one of the built-in components, so fall back to
	// the stock analyzer rather than shipping an index with no analyzer
	// at all — degraded phrase precision beats unusable search.
	textAnalyzerName := bookTextAnalyzerName
	if err := im.AddCustomCharFilter(underscoreCharFilterName, map[string]any{
		"type":    regexpchar.Name,
		"regexp":  "_",
		"replace": " ",
	}); err != nil {
		indexLog.Error("search: underscore char filter registration failed: %v", err)
	}
	if err := im.AddCustomAnalyzer(bookTextAnalyzerName, map[string]any{
		"type":         custom.Name,
		"char_filters": []string{underscoreCharFilterName},
		"tokenizer":    unicode.Name,
		"token_filters": []string{
			en.PossessiveName, // strip trailing 's
			lowercase.Name,
			porter.Name, // stem; NOTE: no en.StopName — that is the point
		},
	}); err != nil {
		slog.Error("search: custom analyzer registration failed; "+
			"falling back to stock English analyzer, quoted phrases "+
			"containing stopwords will stay imprecise",
			"analyzer", bookTextAnalyzerName, "err", err)
		textAnalyzerName = en.AnalyzerName
	}

	// textAnalyzed no longer takes a boost: bleve v2 has no index-time
	// field boost, so that parameter was dead. Query-time weighting for
	// free-text search lives in textFieldBoosts (bleve_index.go) and is
	// applied by translateFreeText in bleve_translator.go.
	textAnalyzed := func() *mapping.FieldMapping {
		f := bleve.NewTextFieldMapping()
		f.Analyzer = textAnalyzerName
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
	im.DefaultAnalyzer = textAnalyzerName
	im.TypeField = "_type"
	im.DefaultType = BookDocType

	return im
}
