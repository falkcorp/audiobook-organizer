// file: internal/playlist/evaluator.go
// version: 1.7.2
// guid: 9c2d5f1e-6b4a-4a70-b8c5-3d7e0f1b9a68
// last-edited: 2026-10-26
//
// Smart playlist query evaluator (spec 3.4 task 2).
//
// A smart playlist carries a DSL query string plus an optional
// SortJSON and Limit directive. Evaluating the playlist produces
// the ordered list of book IDs that currently match. The result is
// also cached onto the playlist row (MaterializedBookIDs) so the
// iTunes push worker doesn't need the index online to flush pending
// playlists.
//
// The evaluator prefers the Bleve index when available: it yields
// full text/relevance support and handles ranges natively. Per-user
// fields (read_status, progress_pct, last_played) are split off by
// the translator into a PerUserFilter slice and applied here against the
// evaluating user's own state rows (Bleve only holds library-global state):
// read in one prefix scan per viewer (ViewerStates), never one lookup per
// candidate book, and never through a result window that could truncate the
// answer. See matchViewerStates.
//
// When the Bleve index is nil (e.g. during early startup) the
// evaluator returns an error — iTunes sync and the HTTP GET path
// both retry after the index opens.

package playlist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/search/query"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// PlaylistSort is one ordering directive inside a smart playlist's
// SortJSON. The parsed form of the playlist's SortJSON field.
type PlaylistSort struct {
	Field     string `json:"field"`
	Direction string `json:"direction"` // "asc" | "desc"
}

// ErrSearchIndexUnavailable indicates the Bleve index is not open.
// Callers can retry once the server has finished its startup phase.
var ErrSearchIndexUnavailable = errors.New("search index not yet available")

// ErrViewerStateRead marks a failure to read the viewer's own listening state. The
// text of such an error can carry the viewer's user id and a book id (a state row
// that does not decode is reported as "<userID>/<bookID>"), so callers that log an
// evaluation failure for other people to read redact exactly this class and print
// every other error as it is. errors.Is(err, ErrViewerStateRead) is the test; the
// cause stays reachable through errors.Is / errors.As as before.
var ErrViewerStateRead = errors.New("viewer state read failed")

// viewerStateReadError carries a state-read failure as ErrViewerStateRead without
// changing its text or hiding its cause.
type viewerStateReadError struct{ err error }

func (e *viewerStateReadError) Error() string        { return e.err.Error() }
func (e *viewerStateReadError) Unwrap() error        { return e.err }
func (e *viewerStateReadError) Is(target error) bool { return target == ErrViewerStateRead }

// bookLookup, userStateReader and playlistEvalStore are the two methods the
// evaluator calls on a store (it was 43 methods of database.* embeds), measured
// by emptying the interface and reading the compiler's enumeration. The per-user
// bulk read (database.UserBookStateLister) is deliberately NOT part of it: it is a
// capability resolved with database.AsCapability, so neither database.Store nor
// every test double has to grow it.
type bookLookup interface {
	GetBookByID(id string) (*database.Book, error)
}

type userStateReader interface {
	GetUserBookState(userID, bookID string) (*database.UserBookState, error)
}

type playlistEvalStore interface {
	bookLookup
	userStateReader
}

// ViewerStates is one user's per-book state, loaded ONCE and shared read-only by
// every evaluation made for that viewer (a collections page evaluates every
// per-viewer collection for the same person). Build it with LoadViewerStates.
//
// With a store that can list a user's rows (the Pebble store) it holds the whole
// set in memory: the evaluator then answers a per-user filter from this map and
// never reads a row per candidate book. A store that cannot is read by point
// reads instead (rows is nil), which is the same answer, slower.
type ViewerStates struct {
	userID string
	rows   map[string]*database.UserBookState // book id -> row; nil when the store cannot list
	reader userStateReader                    // point reads, used only when rows is nil
}

// UserID is the user the states belong to. "" means no viewer.
func (v *ViewerStates) UserID() string {
	if v == nil {
		return ""
	}
	return v.userID
}

// LoadViewerStates reads userID's state for the evaluator. An empty userID is
// "no viewer": nothing is read, and any query with a per-user filter then
// matches nothing (see evaluate). An error means the rows could not be read or
// one did not decode; the caller must fail the evaluation rather than treat the
// user as having no state, which would match every book under a negated or
// `unstarted` filter.
func LoadViewerStates(store playlistEvalStore, userID string) (*ViewerStates, error) {
	if userID == "" {
		return &ViewerStates{}, nil
	}
	if lister, ok := database.AsCapability[database.UserBookStateLister](store); ok {
		rows, err := lister.ListUserBookStates(userID)
		if err != nil {
			return nil, &viewerStateReadError{fmt.Errorf("read user state: %w", err)}
		}
		return &ViewerStates{userID: userID, rows: rows}, nil
	}
	return &ViewerStates{userID: userID, reader: store}, nil
}

// lookup returns the viewer's row for a book, nil when there is none.
func (v *ViewerStates) lookup(bookID string) (*database.UserBookState, error) {
	if v.rows != nil {
		return v.rows[bookID], nil
	}
	return v.reader.GetUserBookState(v.userID, bookID)
}

// evalWindowOverride, when positive, replaces the initial result window of an
// uncapped search. Tests set it tiny to prove the follow-up pass returns every
// hit; production leaves it zero, so the window is the index's document count.
var evalWindowOverride int

// EvaluateSmartPlaylist parses the playlist query, runs it against
// the Bleve index, applies any per-user post-filters, sorts, caps
// to Limit, and returns the ordered list of matching book IDs.
//
// store is required for per-user filter lookups + sort enrichment.
// idx is the Bleve index; nil yields ErrSearchIndexUnavailable.
// userID is the user the playlist evaluates for — per-user filters
// read that user's state rows. An EMPTY userID means "no viewer" (a
// background or system evaluation): a query with any per-user filter then
// matches nothing — see evaluate — and a query with none is unaffected.
//
// The result is COMPLETE: no result window truncates it (until 2026-10-25 the
// search was capped at the first 10,000 documents by id, so on a 122k library a
// per-user collection silently covered only the oldest tenth of it).
func EvaluateSmartPlaylist(
	ctx context.Context,
	store playlistEvalStore,
	idx *search.BleveIndex,
	query string,
	sortJSON string,
	limit int,
	userID string,
) ([]string, error) {
	return evaluate(ctx, store, idx, query, sortJSON, limit, userID, nil)
}

// EvaluateSmartPlaylistFor is EvaluateSmartPlaylist for a viewer whose state was
// already loaded, so a caller evaluating many queries for one viewer reads the
// state once. A nil viewer is no viewer.
func EvaluateSmartPlaylistFor(
	ctx context.Context,
	store playlistEvalStore,
	idx *search.BleveIndex,
	query string,
	sortJSON string,
	limit int,
	viewer *ViewerStates,
) ([]string, error) {
	return evaluate(ctx, store, idx, query, sortJSON, limit, viewer.UserID(), viewer)
}

func evaluate(
	ctx context.Context,
	store playlistEvalStore,
	idx *search.BleveIndex,
	query string,
	sortJSON string,
	limit int,
	userID string,
	viewer *ViewerStates,
) ([]string, error) {
	// ONE instant for the whole evaluation: relative dates (last_played:>-30d)
	// in the Bleve translation and in the per-user pass resolve against the same
	// now, so a window cannot shift between the first book and the last.
	now := time.Now()
	var ast search.Node
	if strings.TrimSpace(query) != "" {
		var err error
		if ast, err = search.ParseQuery(query); err != nil {
			return nil, fmt.Errorf("parse query: %w", err)
		}
		// A per-user term the per-user pass cannot evaluate as written (an OR
		// with a sibling, a NOT over a group, a value that is not a status, a
		// number or a date, a pattern) is refused rather than answered as
		// something else. Creates and updates refuse it up front; this is the same
		// check for a stored row that predates it, so it reads as unavailable
		// instead of wrong. It runs before the index is looked at: the query is
		// what is wrong, and "try again shortly" would be the wrong advice.
		if err := search.CheckPerUserTerms(ast, now); err != nil {
			return nil, fmt.Errorf("query: %w", err)
		}
	}
	// A rebuilding index (created empty, not yet drained) would yield a
	// partial membership that callers persist and push; refuse instead.
	if idx == nil || idx.Rebuilding() {
		return nil, ErrSearchIndexUnavailable
	}
	if ast == nil {
		return nil, fmt.Errorf("empty query")
	}
	bleveQ, perUserFilters, err := search.TranslateAt(ast, now)
	if err != nil {
		return nil, fmt.Errorf("translate query: %w", err)
	}

	var ids []string
	switch {
	case len(perUserFilters) == 0:
		ids, err = searchAllIDs(ctx, idx, bleveQ, 0)
	case userID == "":
		// NO VIEWER, NO STATE, NO MATCH. With no user there is no state row to
		// read, and a nil state is evaluated as the zero value, which satisfies
		// read_status:unstarted, progress_pct:<50 and every negated filter: left
		// to fall through, a no-viewer evaluation would match nearly the whole
		// library instead of nothing. A system caller must also never be
		// answered with some other user's state (dynamic collections used to fall
		// back to their creator, which is the leak this guard exists to keep
		// closed), so the result is empty by construction and nothing is searched.
		return []string{}, nil
	default:
		if viewer == nil || viewer.userID != userID {
			if viewer, err = LoadViewerStates(store, userID); err != nil {
				return nil, err
			}
		}
		ids, err = matchViewerStates(ctx, idx, bleveQ, perUserFilters, viewer, now)
	}
	if err != nil {
		return nil, err
	}

	// The search and the state pass stop when ctx is done; the sort below reads one
	// book per id, so a request that went away is not carried through it either.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ids, err = sortBookIDs(store, ids, sortJSON)
	if err != nil {
		return nil, fmt.Errorf("sort: %w", err)
	}

	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

// searchAllIDs returns the id of EVERY document matching q, with no result window
// to truncate it and no highlighting (the hits are used for their ids alone; the
// highlighted form loaded each hit's stored document for nothing).
//
// The first pass asks for `window` hits, or the index's document count when
// window is 0, which no query can exceed. The search reports the total it matched,
// so if documents were indexed between the count and the search (total above the
// hits returned) it asks again for the full total: a short answer is never
// returned as if it were complete. Order is the index's (score, then id); every
// caller sorts afterwards, so only the set matters.
func searchAllIDs(ctx context.Context, idx *search.BleveIndex, q query.Query, window int) ([]string, error) {
	if window <= 0 {
		window = evalWindowOverride
	}
	if window <= 0 {
		n, err := idx.DocCount()
		if err != nil {
			return nil, fmt.Errorf("bleve doc count: %w", err)
		}
		window = int(n)
	}
	if window < 1 {
		window = 1 // a size of 0 means "the default page" to the search layer
	}
	return collectAllHits(window, func(size int) ([]search.SearchResult, uint64, error) {
		return idx.SearchNativeIDsContext(ctx, q, 0, size)
	})
}

// collectAllHits runs fetch with a window of size and, if the search reports more
// matches than it returned (documents were indexed between the count and the
// search), again with the reported total. The second pass ends the loop unless the
// index grew again in between; a bounded number of passes keeps an index that is
// indexed faster than it is searched from holding the caller (and a shared
// evaluation slot) forever. Running out of passes is an error, never a short list.
func collectAllHits(window int, fetch func(size int) ([]search.SearchResult, uint64, error)) ([]string, error) {
	for range searchAllIDsMaxPasses {
		hits, total, err := fetch(window)
		if err != nil {
			return nil, fmt.Errorf("bleve search: %w", err)
		}
		if uint64(len(hits)) >= total {
			ids := make([]string, len(hits))
			for i, h := range hits {
				ids[i] = h.BookID
			}
			return ids, nil
		}
		window = int(total)
	}
	return nil, fmt.Errorf("bleve search: the index kept growing during %d passes: %w", searchAllIDsMaxPasses, ErrSearchIndexUnavailable)
}

// searchAllIDsMaxPasses bounds searchAllIDs's re-query when documents are indexed
// between its count and its search. One re-query is the normal worst case.
const searchAllIDsMaxPasses = 4

// matchViewerStates answers a query with per-user filters for one viewer: the
// books that satisfy the indexed part AND every per-user filter, judged against
// the viewer's own state. Per-user terms always apply as AND post-filters (a
// term negated on its own, or an alternation of one field, is one filter);
// evaluate has already refused, through search.CheckPerUserBoolean, every query
// where that is not what the boolean tree says.
//
// Two strategies, chosen by whether a book the viewer has NO row for can match:
//
//   - It cannot (finished, in_progress, last_played:>-30d, progress_pct:>0 ...):
//     the matching books are among the viewer's own rows, which number in the
//     hundreds or low thousands, not the library. Those rows' ids are intersected
//     with the indexed part inside Bleve (a document-id query), so the search
//     returns at most that many hits. Per-user-only queries are the whole
//     answer: the ids that are still in the index.
//   - It can (unstarted, negated filters, progress_pct:<50): the answer is
//     library-scale by definition, so the indexed part is searched uncapped for
//     ids alone and each id is judged against the in-memory rows.
//
// A store that cannot list a viewer's rows takes the second strategy with a point
// read per candidate; a read error fails the evaluation (see judgeByPointReads).
func matchViewerStates(
	ctx context.Context,
	idx *search.BleveIndex,
	bleveQ query.Query,
	filters []search.PerUserFilter,
	viewer *ViewerStates,
	now time.Time,
) ([]string, error) {
	if viewer.rows != nil && !search.MatchPerUserFilters(nil, filters, now) {
		candidates := make([]string, 0, len(viewer.rows))
		for id, row := range viewer.rows {
			if search.MatchPerUserFilters(row, filters, now) {
				candidates = append(candidates, id)
			}
		}
		if len(candidates) == 0 {
			return []string{}, nil
		}
		q := query.Query(bleve.NewDocIDQuery(candidates))
		if _, all := bleveQ.(*query.MatchAllQuery); !all {
			q = bleve.NewConjunctionQuery(bleveQ, q)
		}
		// The window is the candidate count: no hit can exceed it.
		return searchAllIDs(ctx, idx, q, len(candidates))
	}

	ids, err := searchAllIDs(ctx, idx, bleveQ, 0)
	if err != nil {
		return nil, err
	}
	if viewer.rows != nil {
		kept := ids[:0]
		for _, id := range ids {
			if search.MatchPerUserFilters(viewer.rows[id], filters, now) {
				kept = append(kept, id)
			}
		}
		return kept, nil
	}
	return judgeByPointReads(ctx, viewer, ids, filters, now, evalWorkers())
}

// judgeByPointReads keeps the candidates whose state satisfies every filter,
// reading one state row per candidate (the path for a store that cannot list a
// user's rows; the Pebble store never takes it). The reads are spread over
// `workers` goroutines in contiguous chunks and each writes only its own slots,
// so the result keeps the candidates' order with no lock.
//
// A read error FAILS the evaluation. It used to be dropped, which evaluated the
// book as having no state, and under a negated or `unstarted` filter that makes
// it match: a store error turned into extra members.
func judgeByPointReads(
	ctx context.Context,
	viewer *ViewerStates,
	ids []string,
	filters []search.PerUserFilter,
	now time.Time,
	workers int,
) ([]string, error) {
	matched := make([]bool, len(ids))
	err := parallelRanges(len(ids), workers, perUserFilterChunk, func(lo, hi int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		for i := lo; i < hi; i++ {
			state, err := viewer.lookup(ids[i])
			if err != nil {
				return &viewerStateReadError{fmt.Errorf("read user state for book %s: %w", ids[i], err)}
			}
			matched[i] = search.MatchPerUserFilters(state, filters, now)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	kept := make([]string, 0, len(ids))
	for i, id := range ids {
		if matched[i] {
			kept = append(kept, id)
		}
	}
	return kept, nil
}

// perUserFilterChunk is the fewest items worth giving a worker of their own in
// parallelRanges; below two chunks the work runs inline.
const perUserFilterChunk = 256

// evalWorkers is the worker count for the evaluator's per-item passes.
func evalWorkers() int { return runtime.NumCPU() }

// parallelRanges runs fn over [0, n) split into contiguous ranges, one goroutine
// per range, and returns the first error in range order (so the answer does not
// depend on scheduling). It runs inline when there are fewer than two chunks of
// work or fewer than two workers. fn must write only to its own range.
func parallelRanges(n, workers, minChunk int, fn func(lo, hi int) error) error {
	if workers > n/minChunk {
		workers = n / minChunk
	}
	if workers < 2 {
		return fn(0, n)
	}
	chunk := (n + workers - 1) / workers
	errs := make([]error, (n+chunk-1)/chunk)
	var wg sync.WaitGroup
	for part, lo := 0, 0; lo < n; part, lo = part+1, lo+chunk {
		hi := min(lo+chunk, n)
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[part] = fn(lo, hi)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// ValidateSortJSON reports whether sortJSON can be used as a sort spec: empty (the
// default order) or a JSON array of {field, direction}. A spec that is not that
// fails every evaluation of a list of two or more books, so it is refused when it
// is saved (HTTP 400) instead of leaving a collection or playlist unavailable.
func ValidateSortJSON(sortJSON string) error {
	if strings.TrimSpace(sortJSON) == "" {
		return nil
	}
	var directives []PlaylistSort
	if err := json.Unmarshal([]byte(sortJSON), &directives); err != nil {
		return fmt.Errorf("sort_json must be a JSON array of {\"field\", \"direction\"} objects: %w", err)
	}
	return nil
}

// sortBookIDs reorders ids per the playlist's SortJSON directives.
// When SortJSON is empty or invalid, ids are sorted by ID string for
// deterministic output (ULIDs sort in insertion order). Unknown fields
// are skipped so a partly-broken sort spec still produces a stable result.
func sortBookIDs(store bookLookup, ids []string, sortJSON string) ([]string, error) {
	if len(ids) < 2 {
		return ids, nil
	}
	if strings.TrimSpace(sortJSON) == "" {
		out := make([]string, len(ids))
		copy(out, ids)
		sort.Strings(out)
		return out, nil
	}
	var directives []PlaylistSort
	if err := json.Unmarshal([]byte(sortJSON), &directives); err != nil {
		// Fallback to deterministic ID sort on parse error.
		out := make([]string, len(ids))
		copy(out, ids)
		sort.Strings(out)
		return out, fmt.Errorf("parse sort_json: %w", err)
	}
	if len(directives) == 0 {
		out := make([]string, len(ids))
		copy(out, ids)
		sort.Strings(out)
		return out, nil
	}

	type loaded struct {
		id   string
		book *database.Book
	}
	// One book read (a full row decode) per id, and an uncapped evaluation can
	// hand this a library's worth of ids, so the reads are spread over the same
	// bounded chunked pool as the state pass, each worker writing only its own
	// slots. A book that cannot be read is nil and sorts as "missing", as before.
	rows := make([]loaded, len(ids))
	_ = parallelRanges(len(ids), evalWorkers(), perUserFilterChunk, func(lo, hi int) error {
		for i := lo; i < hi; i++ {
			b, _ := store.GetBookByID(ids[i])
			rows[i] = loaded{id: ids[i], book: b}
		}
		return nil
	})

	sort.SliceStable(rows, func(i, j int) bool {
		for _, d := range directives {
			c := compareBookField(rows[i].book, rows[j].book, d.Field)
			if c == 0 {
				continue
			}
			if strings.EqualFold(d.Direction, "desc") {
				return c > 0
			}
			return c < 0
		}
		// Tiebreaker: sort by ID for deterministic output.
		return rows[i].id < rows[j].id
	})

	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.id
	}
	return out, nil
}

// compareBookField returns -1/0/+1 for a/b on the named Book field.
// Missing books are treated as "less than" present books so they
// cluster at one end rather than breaking the sort mid-list.
func compareBookField(a, b *database.Book, field string) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return -1
	}
	if b == nil {
		return 1
	}
	switch strings.ToLower(field) {
	case "title":
		return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
	case "year":
		return intCmp(derefInt(a.PrintYear), derefInt(b.PrintYear))
	case "audiobook_year":
		return intCmp(derefInt(a.AudiobookReleaseYear), derefInt(b.AudiobookReleaseYear))
	case "rating":
		return intCmp(derefInt(a.ITunesRating), derefInt(b.ITunesRating))
	case "duration":
		return intCmp(derefInt(a.Duration), derefInt(b.Duration))
	case "date_added", "added":
		return timeCmp(derefTime(a.CreatedAt), derefTime(b.CreatedAt))
	case "date_modified", "modified":
		return timeCmp(derefTime(a.UpdatedAt), derefTime(b.UpdatedAt))
	case "itunes_last_played":
		return timeCmp(derefTime(a.ITunesLastPlayed), derefTime(b.ITunesLastPlayed))
	case "itunes_play_count":
		return intCmp(derefInt(a.ITunesPlayCount), derefInt(b.ITunesPlayCount))
	default:
		return 0
	}
}

func derefTime(p *time.Time) time.Time {
	if p == nil {
		return time.Time{}
	}
	return *p
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func intCmp(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func timeCmp(a, b time.Time) int {
	switch {
	case a.Before(b):
		return -1
	case a.After(b):
		return 1
	default:
		return 0
	}
}
