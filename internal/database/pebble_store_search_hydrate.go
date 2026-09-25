// file: internal/database/pebble_store_search_hydrate.go
// version: 1.0.0
// guid: 5d7c2f1e-8a43-4b6e-9d1f-2c7a0e4b8f63
// last-edited: 2026-09-25

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/cockroachdb/pebble/v2"
	"github.com/hashicorp/go-memdb"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

var searchHydrateLog = logger.New("database.search")

// SearchResultStore is what the shared search result cache needs from the
// store beyond database.Store. It is a capability (reached through
// AsSearchResultStore, which looks through the indexedStore decorator), not a
// database.Store method, for the reason SearchIndexDirtyStore gives: only
// PebbleStore implements it and every mock would otherwise have to grow.
type SearchResultStore interface {
	// GetBooksForSearch reads ids in order, skipping each row it cannot use
	// instead of stopping at the first one. GetBooksByIDs returns the rows
	// read so far at the first undecodable row or unreadable signature, so a
	// cached list or page hydrated through it silently lost every book after
	// that row; here one bad row costs that row only, and its ID is returned
	// in bad so the caller can log it.
	//
	// withSig folds in the book_sig: sidecar exactly as GetBooksByIDs does
	// (the web list shows book_sig_coverage_pct); false reads the bare row
	// as SearchBooks does (the ABS search never serializes signatures).
	//
	// A storage error other than not-found aborts with err: that is not a bad
	// row but a failing read, and a caller building a cached list must not
	// cache what it got before the failure.
	GetBooksForSearch(ids []string, withSig bool) (books []Book, bad []string, err error)
	// SearchBookRanksFiltered returns the SubstringSearchRank of each of ids
	// that f admits and that matches query, by point lookups: the cost is
	// proportional to len(ids), never to the library. Missing IDs, and IDs
	// that are filtered out or do not match, are absent from the map.
	SearchBookRanksFiltered(query string, ids []string, f BookSummaryFilter) (map[string]SearchRank, error)
}

// AsSearchResultStore returns s as a SearchResultStore if the underlying store
// supports it (true for *PebbleStore), or nil. Callers MUST nil-check.
func AsSearchResultStore(s any) SearchResultStore {
	if s == nil {
		return nil
	}
	if rs, ok := AsCapability[SearchResultStore](s); ok {
		return rs
	}
	return nil
}

var _ SearchResultStore = (*PebbleStore)(nil)

// errSkipRow marks a row-local failure: the row is skipped, the read goes on.
var errSkipRow = errors.New("skip row")

// GetBooksForSearch implements SearchResultStore.
func (p *PebbleStore) GetBooksForSearch(ids []string, withSig bool) ([]Book, []string, error) {
	books := make([]Book, 0, len(ids))
	var bad []string
	for _, id := range ids {
		b, err := p.readBookForSearch(id, withSig)
		if errors.Is(err, errSkipRow) {
			bad = append(bad, id)
			continue
		}
		if err != nil {
			return books, bad, err
		}
		if b != nil {
			books = append(books, *b)
		}
	}
	return books, bad, nil
}

func (p *PebbleStore) readBookForSearch(id string, withSig bool) (*Book, error) {
	value, closer, err := p.db.Get([]byte("book:" + id))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get book %q: %w", id, err)
	}
	var book Book
	uErr := json.Unmarshal(value, &book)
	closer.Close()
	if uErr != nil {
		return nil, fmt.Errorf("unmarshal book %q: %v: %w", id, uErr, errSkipRow)
	}
	if withSig {
		if sErr := p.hydrateBookSig(&book); sErr != nil {
			return nil, fmt.Errorf("%v: %w", sErr, errSkipRow)
		}
	}
	return &book, nil
}

// SearchBookRanksFiltered implements SearchResultStore: memdb point lookups
// when warm, Pebble point reads otherwise.
func (p *PebbleStore) SearchBookRanksFiltered(query string, ids []string, f BookSummaryFilter) (map[string]SearchRank, error) {
	lowerQuery := strings.ToLower(query)
	out := make(map[string]SearchRank, len(ids))
	if p.UseMemDB && p.mem() != nil {
		if err := p.mem().searchBookRanks(lowerQuery, ids, f, out); err == nil {
			return out, nil
		}
		clear(out)
	}
	books := make([]Book, 0, len(ids))
	authorSet := map[int]struct{}{}
	for _, id := range ids {
		b, err := p.getBookRowForSearch(id)
		if err != nil || b == nil {
			continue
		}
		books = append(books, *b)
		if b.AuthorID != nil {
			authorSet[*b.AuthorID] = struct{}{}
		}
	}
	authorNames := make(map[int]string, len(authorSet))
	if len(authorSet) > 0 {
		aids := make([]int, 0, len(authorSet))
		for id := range authorSet {
			aids = append(aids, id)
		}
		sort.Ints(aids)
		authors, err := p.GetAuthorsByIDs(aids)
		if err != nil {
			return nil, fmt.Errorf("search ranks: authors: %w", err)
		}
		for id, a := range authors {
			if a != nil {
				authorNames[id] = util.NormalizeAuthor(a.Name)
			}
		}
	}
	for i := range books {
		b := &books[i]
		if !bookMatchesSummaryFilter(b, f) {
			continue
		}
		if r, ok := SubstringSearchRank(b.ID, b.Title, b.Narrator, b.AuthorID, authorNames, lowerQuery); ok {
			out[b.ID] = r
		}
	}
	return out, nil
}

// searchBookRanks is the memdb half of SearchBookRanksFiltered.
func (m *MemStore) searchBookRanks(lowerQuery string, ids []string, f BookSummaryFilter, out map[string]SearchRank) error {
	txn := m.db.Txn(false)
	defer txn.Abort()
	authorNames := map[int]string{}
	for _, id := range ids {
		raw, err := txn.First(memTableBooks, memIdxID, id)
		if err != nil {
			return fmt.Errorf("memdb search ranks: book %q: %w", id, err)
		}
		b, ok := raw.(*Book)
		if !ok || b == nil || !bookMatchesSummaryFilter(b, f) {
			continue
		}
		if b.AuthorID != nil {
			if _, have := authorNames[*b.AuthorID]; !have {
				authorNames[*b.AuthorID] = memAuthorName(txn, *b.AuthorID)
			}
		}
		if r, ok := SubstringSearchRank(b.ID, b.Title, b.Narrator, b.AuthorID, authorNames, lowerQuery); ok {
			out[b.ID] = r
		}
	}
	return nil
}

func memAuthorName(txn *memdb.Txn, id int) string {
	obj, err := txn.First(memTableAuthors, memIdxID, id)
	if err != nil || obj == nil {
		return ""
	}
	if a, ok := obj.(*Author); ok {
		return util.NormalizeAuthor(a.Name)
	}
	return ""
}

// searchBookIDsDisk is the Pebble disk scan of searchBooks keeping only IDs,
// so the pre-warmup fallback of SearchBookIDsFiltered with limit 0 does not
// hold a decoded Book for every match of a broad query.
func (p *PebbleStore) searchBookIDsDisk(query string, limit, offset int, f *BookSummaryFilter) ([]string, error) {
	authorNames := p.diskAuthorNames()
	lowerQuery := strings.ToLower(query)
	ranker := newSearchRanker[string](limit, offset)
	if err := forEachBookRow(p.db, func(_ string, rowValue []byte) error {
		var book Book
		if err := json.Unmarshal(rowValue, &book); err != nil {
			return nil
		}
		if f != nil && !bookMatchesSummaryFilter(&book, *f) {
			return nil
		}
		if rank, ok := SubstringSearchRank(book.ID, book.Title, book.Narrator, book.AuthorID, authorNames, lowerQuery); ok {
			ranker.Add(rank, book.ID)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return ranker.Result(offset), nil
}

// diskAuthorNames is the author id -> normalized name map the disk scans
// match author names against.
func (p *PebbleStore) diskAuthorNames() map[int]string {
	authorNames := make(map[int]string)
	authIter, authErr := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("author:0"),
		UpperBound: []byte("author:;"),
	})
	if authErr != nil {
		return authorNames
	}
	defer authIter.Close()
	for authIter.First(); authIter.Valid(); authIter.Next() {
		key := string(authIter.Key())
		if strings.Contains(key, ":name:") || strings.Contains(key, ":book:") {
			continue
		}
		var a Author
		if err := json.Unmarshal(authIter.Value(), &a); err == nil {
			authorNames[a.ID] = util.NormalizeAuthor(a.Name)
		}
	}
	return authorNames
}
