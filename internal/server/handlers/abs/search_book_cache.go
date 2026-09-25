// file: internal/server/handlers/abs/search_book_cache.go
// version: 2.0.0
// guid: 1aad78ca-65d9-4c69-8875-323ce8dd1196
// last-edited: 2026-09-25

package abs

import (
	"context"
	"errors"
	"sort"

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

// absBookSearchKey is database.SearchQueryKey, exactly the fold the substring
// search applies to the query: lowercase, '_' to space, space runs collapsed,
// ends NOT trimmed. "_foo" and "foo" match different books (the first needs
// a space before "foo"), so they must not share an entry.
func absBookSearchKey(query string) string {
	return "abs\x00" + database.SearchQueryKey(query)
}

// absLookup is what an ABS search accepts from the result cache. ABS clients
// cannot poll, so there is no pending answer: a new search waits for its
// build (or the request ctx), as the per-request search always did. A list
// that predates an unpatchable change is acceptable — it is served while a
// rebuild runs and never pinned into the document cache (complete=false) —
// and so is the old list when a patch outlives the cache's wait.
func (h *Handler) absLookup() searchcache.LookupOptions {
	return searchcache.LookupOptions{Wait: h.bookSearch.DefaultWait(), AllowStale: true}
}

// searchBookHits returns the first limit visible books matching query, in the
// store's relevance order, and the change generation they are current to (0
// without the cache). With the shared result cache wired, the full ranked ID
// list is computed once per query and kept current by the store change log,
// so a repeated search, or the same search at a larger limit, costs one
// hydration of limit books.
//
// complete=false keeps the document out of the document cache: a stale list,
// a page with unreadable rows skipped, or the direct fallback.
func (h *Handler) searchBookHits(ctx context.Context, query string, limit int) (books []database.Book, complete bool, gen uint64, err error) {
	direct := func() ([]database.Book, bool, uint64, error) {
		b, err := h.library.SearchBooksFiltered(query, limit, 0, absItemFilterBase())
		return b, true, 0, err
	}
	if h.bookSearch == nil {
		return direct()
	}
	ev := &absBookSearchEvaluator{lib: h.library, query: query}
	res, err := h.bookSearch.Lookup(ctx, absBookSearchKey(query), ev, h.absLookup())
	if errors.Is(err, searchcache.ErrBusy) {
		// The build queue is full: answer this request the way it was
		// answered before the cache, at its own limit.
		b, _, _, dErr := direct()
		return b, false, 0, dErr
	}
	if err != nil {
		return nil, false, 0, err
	}
	ids := res.IDs
	if len(ids) > limit {
		ids = ids[:limit]
	}
	if len(ids) == 0 {
		return []database.Book{}, !res.Stale, res.Gen, nil
	}
	books, bad, err := h.library.GetBooksForSearch(ids, false)
	if err != nil {
		// A failing read: serve what was read, uncached, rather than fail the
		// whole search (the per-request search skipped unreadable rows too).
		absSearchLog.Warn("abs: search hydrate failed after %d rows: %v", len(books), err)
		return books, false, res.Gen, nil
	}
	if len(bad) > 0 {
		absSearchLog.Warn("abs: search skipped %d unreadable book rows (first %s)", len(bad), bad[0])
		return books, false, res.Gen, nil
	}
	return books, !res.Stale, res.Gen, nil
}

// absBookSearchEvaluator maintains one ABS book-search entry through the
// store's filtered substring search (absItemFilterBase — the /items
// visibility predicate). The ranked list is SubstringSearchRank order, a
// per-book total order, so re-evaluation and ordering are point lookups of
// the books involved (SearchBookRanksFiltered), never a library scan.
type absBookSearchEvaluator struct {
	lib   LibraryStore
	query string
}

func (e *absBookSearchEvaluator) Build(ctx context.Context, progress func(int)) ([]string, error) {
	ids, err := e.lib.SearchBookIDsFiltered(e.query, 0, 0, absItemFilterBase())
	if err == nil {
		progress(len(ids))
	}
	return ids, err
}

func (e *absBookSearchEvaluator) Match(ctx context.Context, ids []string) ([]string, bool, error) {
	ranks, err := e.lib.SearchBookRanksFiltered(e.query, ids, absItemFilterBase())
	if err != nil {
		return nil, false, err
	}
	out := make([]string, 0, len(ranks))
	for id := range ranks {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return ranks[out[i]].Less(ranks[out[j]]) })
	return out, true, nil
}

// Less compares the two books' SubstringSearchRank: two point lookups.
func (e *absBookSearchEvaluator) Less(ctx context.Context, a, b string) (bool, error) {
	ranks, err := e.lib.SearchBookRanksFiltered(e.query, []string{a, b}, absItemFilterBase())
	if err != nil {
		return false, err
	}
	ra, okA := ranks[a]
	rb, okB := ranks[b]
	switch {
	case okA && okB:
		return ra.Less(rb), nil
	default:
		// One of them stopped matching between Match and here: put the
		// survivor first; the next patch removes the other.
		return okA, nil
	}
}
