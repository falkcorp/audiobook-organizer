// file: internal/database/search_rank.go
// version: 1.0.0
// guid: a46bc2cf-3cc2-4402-b32f-c2fba2c7c95c
// last-edited: 2026-09-25

package database

import (
	"container/heap"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Relevance tiers for the SearchBooks substring predicate, best first.
//
// Before these existed, SearchBooks returned the FIRST `limit` matches in
// book-ULID (creation) order and stopped. ABS search has no pagination, so on
// production q="Roll" returned "The Silas Kane Scrolls" and "The Apocalypse
// Troll" and never reached the book titled exactly "Roll" — and the newest
// books were the likeliest to be cut. Ranking happens before offset and limit
// are applied, so the cut drops the least relevant matches, never arbitrary
// ones.
const (
	SearchTierExactTitle  = 0 // normalized title equals the query
	SearchTierTitlePrefix = 1 // title starts with the query
	SearchTierTitleWord   = 2 // query is a whole word / word sequence in the title
	SearchTierTitleSubstr = 3 // query is somewhere inside the title
	SearchTierAuthor      = 4 // only the primary author's name matches
	SearchTierNarrator    = 5 // only the narrator matches
)

// SearchRank is one match's sort key. Order: Tier ascending, then TitleLen
// ascending (a shorter title is a closer match for the same tier), then ID
// ascending. ID is unique, so the order is total and deterministic: the memdb
// scan, the Pebble disk scan and the scoped service fallback return the same
// rows in the same order for the same data, and offset paging over it is stable.
type SearchRank struct {
	Tier     int
	TitleLen int
	ID       string
}

// Less reports whether r sorts before o.
func (r SearchRank) Less(o SearchRank) bool {
	if r.Tier != o.Tier {
		return r.Tier < o.Tier
	}
	if r.TitleLen != o.TitleLen {
		return r.TitleLen < o.TitleLen
	}
	return r.ID < o.ID
}

// SubstringSearchRank is SubstringSearchMatches plus a relevance tier. ok is
// false exactly when SubstringSearchMatches is false — it uses the same fields
// and the same normalization (bare strings.ToLower for title and narrator,
// authorNames already util.NormalizeAuthor'd, lowerQuery = strings.ToLower of
// the query), so ranking never changes WHICH books match, only their order.
//
// An empty query matches every book (strings.Contains(x, "") is true). It gets
// one tier and a zero title length so those results stay in plain ID order, as
// they were before ranking.
func SubstringSearchRank(id, title string, narrator *string, authorID *int, authorNames map[int]string, lowerQuery string) (SearchRank, bool) {
	if lowerQuery == "" {
		return SearchRank{Tier: SearchTierExactTitle, ID: id}, true
	}
	lt := strings.ToLower(title)
	r := SearchRank{TitleLen: len(lt), ID: id}
	if strings.Contains(lt, lowerQuery) {
		tq := strings.TrimSpace(lowerQuery)
		tt := strings.TrimSpace(lt)
		switch {
		case lt == lowerQuery || (tq != "" && tt == tq):
			r.Tier = SearchTierExactTitle
		case strings.HasPrefix(lt, lowerQuery) || (tq != "" && strings.HasPrefix(tt, tq)):
			r.Tier = SearchTierTitlePrefix
		case containsWholeWord(lt, lowerQuery):
			r.Tier = SearchTierTitleWord
		default:
			r.Tier = SearchTierTitleSubstr
		}
		return r, true
	}
	if authorID != nil {
		if name, ok := authorNames[*authorID]; ok && strings.Contains(name, lowerQuery) {
			r.Tier = SearchTierAuthor
			return r, true
		}
	}
	if narrator != nil && strings.Contains(strings.ToLower(*narrator), lowerQuery) {
		r.Tier = SearchTierNarrator
		return r, true
	}
	return SearchRank{}, false
}

// containsWholeWord reports whether needle occurs in s with no letter or digit
// immediately before or after it — "roll" in "rock and roll" but not in
// "scrolls". A needle that itself starts or ends with a non-word rune ("roll ")
// is still checked the same way; it simply matches less often.
func containsWholeWord(s, needle string) bool {
	for from := 0; from <= len(s)-len(needle); {
		i := strings.Index(s[from:], needle)
		if i < 0 {
			return false
		}
		start := from + i
		end := start + len(needle)
		before, after := true, true
		if start > 0 {
			r, _ := utf8.DecodeLastRuneInString(s[:start])
			before = !isWordRune(r)
		}
		if end < len(s) {
			r, _ := utf8.DecodeRuneInString(s[end:])
			after = !isWordRune(r)
		}
		if before && after {
			return true
		}
		// Advance one rune past this occurrence's start.
		_, size := utf8.DecodeRuneInString(s[start:])
		if size == 0 {
			size = 1
		}
		from = start + size
	}
	return false
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// searchRanker collects ranked matches and returns the [offset, offset+limit)
// window of them in SearchRank order. It is the single place every SearchBooks
// backend applies offset and limit, so they cannot disagree about the cut.
//
// With a positive limit it keeps only the best offset+limit items in a bounded
// max-heap (worst on top), so a one-letter query that matches most of the
// library costs O(n log k) and holds k items, not every match. limit == 0 means
// "no limit" (every match, fully sorted); a negative limit returns nothing,
// which is what both scans did before ranking existed.
type searchRanker[T any] struct {
	k     int // 0 = unbounded
	items rankedHeap[T]
	none  bool
}

type rankedItem[T any] struct {
	rank SearchRank
	val  T
}

// rankedHeap is a max-heap on SearchRank: the WORST kept item is at index 0.
type rankedHeap[T any] []rankedItem[T]

func (h rankedHeap[T]) Len() int           { return len(h) }
func (h rankedHeap[T]) Less(i, j int) bool { return h[j].rank.Less(h[i].rank) }
func (h rankedHeap[T]) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *rankedHeap[T]) Push(x any)        { *h = append(*h, x.(rankedItem[T])) }
func (h *rankedHeap[T]) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	*h = old[:n-1]
	return it
}

func newSearchRanker[T any](limit, offset int) *searchRanker[T] {
	if limit < 0 {
		return &searchRanker[T]{none: true}
	}
	if offset < 0 {
		offset = 0
	}
	r := &searchRanker[T]{}
	if limit > 0 {
		k := offset + limit
		if k < 0 { // overflow: a hostile offset; keep everything (bounded by the library)
			k = 0
		}
		r.k = k
	}
	return r
}

// Add offers one match. Nothing is preallocated from limit or offset: both come
// from request input (see the CodeQL note in searchBookIDs).
func (r *searchRanker[T]) Add(rank SearchRank, v T) {
	if r.none {
		return
	}
	it := rankedItem[T]{rank: rank, val: v}
	if r.k == 0 {
		r.items = append(r.items, it)
		return
	}
	if len(r.items) < r.k {
		heap.Push(&r.items, it)
		return
	}
	if rank.Less(r.items[0].rank) {
		r.items[0] = it
		heap.Fix(&r.items, 0)
	}
}

// Result returns the kept matches in rank order with the first offset dropped.
func (r *searchRanker[T]) Result(offset int) []T {
	if r.none {
		return []T{}
	}
	items := []rankedItem[T](r.items)
	sort.Slice(items, func(i, j int) bool { return items[i].rank.Less(items[j].rank) })
	if offset < 0 {
		offset = 0
	}
	if offset >= len(items) {
		return []T{}
	}
	items = items[offset:]
	out := make([]T, 0, len(items))
	for _, it := range items {
		out = append(out, it.val)
	}
	return out
}

// RankSubstringMatches filters books by the SearchBooks predicate and returns
// the matches in SearchRank order — the same order the store's SearchBooks
// returns. Used by callers that already hold a candidate set (the audiobooks
// service's scoped search) so their order agrees with the store's.
func RankSubstringMatches(books []Book, authorNames map[int]string, lowerQuery string) []Book {
	r := newSearchRanker[Book](0, 0)
	for i := range books {
		b := &books[i]
		if rank, ok := SubstringSearchRank(b.ID, b.Title, b.Narrator, b.AuthorID, authorNames, lowerQuery); ok {
			r.Add(rank, *b)
		}
	}
	return r.Result(0)
}
