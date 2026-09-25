// file: internal/server/handlers/abs/search_book_cache.go
// version: 1.0.0
// guid: 1aad78ca-65d9-4c69-8875-323ce8dd1196
// last-edited: 2026-09-25

package abs

import (
	"context"
	"errors"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/searchcache"
)

var absSearchLog = logger.New("abs-search")

// searchGeneration is the store change generation the /search document cache
// is stamped with; 0 when no shared result cache is wired (TTL only, as
// before).
func (h *Handler) searchGeneration() uint64 {
	if h.bookSearch == nil {
		return 0
	}
	return h.bookSearch.Changes().Generation()
}

// absBookSearchKey folds the query the way the substring search it runs
// folds it (database.searchFold: lowercase, '_' to space, runs of spaces
// collapsed), so every spelling that search treats alike shares one entry.
func absBookSearchKey(query string) string {
	q := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(query)), "_", " ")
	return "abs\x00" + strings.Join(strings.FieldsFunc(q, func(r rune) bool { return r == ' ' }), " ")
}

// searchBookHits returns the first limit visible books matching query, in the
// store's relevance order. With the shared result cache wired, the full
// ranked ID list is computed once per query and kept current by the store
// change log, so a repeated search, or the same search at a larger limit,
// costs one hydration of limit books.
//
// ABS clients cannot poll, so there is no 202 here: the request waits for the
// build. If the build outlives the wait, the request is answered by the
// direct limited search, exactly what it got before the cache existed, and
// complete=false keeps that document out of the document cache. The build
// keeps running and a later request is a hit.
func (h *Handler) searchBookHits(ctx context.Context, query string, limit int) (books []database.Book, complete bool, err error) {
	direct := func() ([]database.Book, bool, error) {
		b, err := h.library.SearchBooksFiltered(query, limit, 0, absItemFilterBase())
		return b, true, err
	}
	if h.bookSearch == nil {
		return direct()
	}
	ev := &absBookSearchEvaluator{lib: h.library, query: query}
	res, err := h.bookSearch.Lookup(ctx, absBookSearchKey(query), ev, 0)
	var pending *searchcache.PendingError
	if errors.As(err, &pending) {
		absSearchLog.Warn("abs: search %s outlived the wait; answering with the direct search", pending.SearchID)
		b, _, dErr := direct()
		return b, false, dErr
	}
	if err != nil {
		return nil, false, err
	}
	ids := res.IDs
	if len(ids) > limit {
		ids = ids[:limit]
	}
	if len(ids) == 0 {
		return []database.Book{}, !res.Stale, nil
	}
	books, err = h.library.GetBooksByIDs(ids)
	if err != nil {
		return nil, false, err
	}
	// A stale list is served, but never pinned into the document cache.
	return books, !res.Stale, nil
}

// absBookSearchEvaluator maintains one ABS book-search entry through the
// store's filtered substring search (SearchBookIDsFiltered with
// absItemFilterBase — the /items visibility predicate), IDs only.
type absBookSearchEvaluator struct {
	lib   LibraryStore
	query string
}

func (e *absBookSearchEvaluator) restricted(ids []string) ([]string, error) {
	f := absItemFilterBase()
	f.RestrictToIDs = make(map[string]struct{}, len(ids))
	for _, id := range ids {
		f.RestrictToIDs[id] = struct{}{}
	}
	return e.lib.SearchBookIDsFiltered(e.query, 0, 0, f)
}

func (e *absBookSearchEvaluator) Build(ctx context.Context, progress func(int)) ([]string, error) {
	ids, err := e.lib.SearchBookIDsFiltered(e.query, 0, 0, absItemFilterBase())
	if err == nil {
		progress(len(ids))
	}
	return ids, err
}

func (e *absBookSearchEvaluator) Match(ctx context.Context, ids []string) ([]string, bool, error) {
	out, err := e.restricted(ids)
	return out, err == nil, err
}

// Less orders a pair by the same ranked scan confined to the two books.
func (e *absBookSearchEvaluator) Less(ctx context.Context, a, b string) (bool, error) {
	ids, err := e.restricted([]string{a, b})
	if err != nil || len(ids) == 0 {
		return false, err
	}
	return ids[0] == a, nil
}
