// file: internal/search/per_user_mixed.go
// version: 1.0.0
// guid: 6c0d40cc-1952-436b-8cb3-57af681bbbff
// last-edited: 2026-10-30

package search

import (
	"errors"
	"sort"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/search/query"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// errPerUserNeedsRows is what TranslateAt returns for a query whose per-user terms
// it cannot express as its flat list (see perUserNeedsTree). Callers that can take
// the viewer's rows translate with TranslateQuery, which returns a MixedQuery.
var errPerUserNeedsRows = errors.New(
	"search: per-user terms under an OR or a NOT over a group need the viewer's rows; translate with TranslateQuery")

// MixedQuery is a query whose per-user terms (read_status, progress_pct,
// last_played) sit under an OR with a sibling, or under a NOT over a group, so the
// flat AND list of TranslateAt would give them the wrong boolean meaning
// (title:Alpha OR read_status:finished used to be evaluated as an AND).
//
// The viewer's own state is the one thing the index does not hold, and it is small
// beside the library: a few thousand rows. So each per-user term is turned into the
// index query it is equivalent to, once the viewer's rows are known (Query), and the
// index evaluates the WHOLE boolean tree in one pass. Scoring, paging and the match
// count therefore stay the index's own; nothing is re-implemented in Go.
//
// The tree is translated by the same translator as every other query (it only swaps
// what a per-user leaf becomes), so stopword handling, dates, the field routing and
// every other term behave as they do on the flat path.
type MixedQuery struct {
	node Node
	now  time.Time
}

// newMixedQuery validates n by translating it once against a viewer with no rows, so
// a term the translator refuses (a malformed date) is refused at translation time,
// like every other term, and not at the first evaluation.
func newMixedQuery(n Node, now time.Time) (*MixedQuery, error) {
	m := &MixedQuery{node: n, now: now}
	if _, err := m.Query(nil); err != nil {
		return nil, err
	}
	return m, nil
}

// Query returns the index query for the whole expression, judging each per-user
// term against rows: the viewer's state rows keyed by book id. A nil or empty map
// is a viewer with no state at all (no user, or a user who never opened a book): the
// terms are then judged as the zero-value state, the same rule MatchPerUserFilters
// applies to a book with no row, so read_status:unstarted holds for every book and
// read_status:finished for none.
//
// A per-user term becomes one of two exact queries, chosen by whether a book the
// viewer has NO row for satisfies it:
//
//   - it does not (finished, in_progress, last_played:>-30d, progress_pct:>0 ...):
//     the books that satisfy it are among the rows, so it is a DocIDQuery over
//     exactly the rows that match, or match-none when there are none;
//   - it does (unstarted, progress_pct:<50, a term that is true of the zero state):
//     every book satisfies it except the rows that fail it, so it is match-all
//     with those rows excluded.
//
// Both are exact for every book in the index, with or without a row, and the
// surrounding AND, OR and NOT of the expression (and a NOT over a group) are the
// index's. The cost is one pass over rows per per-user term, and a DocIDQuery as
// long as the rows that match (or fail) it: a viewer's rows number in the hundreds
// or low thousands, never the library.
func (m *MixedQuery) Query(rows map[string]*database.UserBookState) (query.Query, error) {
	tc := &translateCtx{now: m.now, leaf: func(f PerUserFilter) query.Query {
		return perUserLeafQuery(f, rows, m.now)
	}}
	q, err := translateNode(m.node, tc, false)
	if err != nil {
		return nil, err
	}
	if q == nil {
		// Every term translated to nothing (an empty alternation): the flat path's
		// rule for a query with no indexed part.
		return bleve.NewMatchAllQuery(), nil
	}
	return q, nil
}

// perUserLeafQuery is the index query one per-user term stands for, given the
// viewer's rows. See MixedQuery.Query.
func perUserLeafQuery(f PerUserFilter, rows map[string]*database.UserBookState, now time.Time) query.Query {
	one := []PerUserFilter{f}
	if MatchPerUserFilters(nil, one, now) {
		// A book with no row satisfies the term, so the matching books are "all but
		// the rows that fail it".
		failing := rowIDs(rows, func(row *database.UserBookState) bool {
			return !MatchPerUserFilters(row, one, now)
		})
		if len(failing) == 0 {
			return bleve.NewMatchAllQuery()
		}
		b := bleve.NewBooleanQuery()
		b.AddMust(bleve.NewMatchAllQuery())
		b.AddMustNot(bleve.NewDocIDQuery(failing))
		return b
	}
	matching := rowIDs(rows, func(row *database.UserBookState) bool {
		return MatchPerUserFilters(row, one, now)
	})
	if len(matching) == 0 {
		return bleve.NewMatchNoneQuery()
	}
	return bleve.NewDocIDQuery(matching)
}

// rowIDs returns, sorted (so the same rows always build the same query), the ids of
// the rows keep accepts.
func rowIDs(rows map[string]*database.UserBookState, keep func(*database.UserBookState) bool) []string {
	var ids []string
	for id, row := range rows {
		if keep(row) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
