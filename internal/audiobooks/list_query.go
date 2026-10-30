// file: internal/audiobooks/list_query.go
// version: 1.0.1
// guid: 68b32fd1-100d-47d9-98bc-8be02ad080b9
// last-edited: 2026-10-30

package audiobooks

import (
	"fmt"
	"sort"
	"strings"
)

// Server-side filter, search, sort and paging for the Authors and Series list
// endpoints (WEB-01 / WEB-02). Both pages used to download the whole table and
// do all of this in the browser; the cached full list is still what the
// handlers read, but the slicing now happens here so the wire carries one page.
//
// Everything works on a COPY of the cached list's index space: the cached
// response is shared by every request and must never be reordered in place.

// Author list filters, in the vocabulary the Authors page already used.
const (
	AuthorFilterAll       = "all"
	AuthorFilterWithBooks = "with_books"
	AuthorFilterZeroBooks = "zero_books"
)

// Series list filters, in the vocabulary the Series page already used.
const (
	SeriesFilterAll       = "all"
	SeriesFilterWithBooks = "with_books"
	SeriesFilterEmpty     = "empty"
)

// ListQuery is the part of a list request both endpoints share.
type ListQuery struct {
	// Search is a case-insensitive substring; empty matches everything.
	Search string
	// Filter is endpoint-specific (see the constants above); empty means all.
	Filter string
	// Sort names a sortable column; empty keeps the cached list's own order.
	Sort string
	// Desc reverses Sort. Ties always break on id ascending so a page boundary
	// never shuffles between two requests.
	Desc bool
	// Offset and Limit window the matches. Limit 0 means "to the end".
	Offset int
	Limit  int
	// Paged is true when the caller sent limit or offset.
	Paged bool
}

// Narrows reports whether the query asks for anything but the whole cached
// list in its cached order, so a bare GET can be served without any work.
func (q ListQuery) Narrows() bool {
	return q.Paged || q.Search != "" || (q.Filter != "" && q.Filter != "all") || q.Sort != ""
}

// Author sort keys.
var authorSortKeys = map[string]bool{"name": true, "book_count": true, "alias_count": true, "id": true}

// Series sort keys. created_at is deliberately absent: the series row carries
// no creation time, so there is nothing to sort by.
var seriesSortKeys = map[string]bool{"name": true, "author_name": true, "book_count": true, "id": true}

// ValidateAuthors rejects a filter or sort the authors list does not know,
// rather than silently serving a different order than the caller asked for.
func (q ListQuery) ValidateAuthors() error {
	switch q.Filter {
	case "", AuthorFilterAll, AuthorFilterWithBooks, AuthorFilterZeroBooks:
	default:
		return fmt.Errorf("invalid filter %q (want all, with_books or zero_books)", q.Filter)
	}
	if q.Sort != "" && !authorSortKeys[q.Sort] {
		return fmt.Errorf("invalid sort %q (want name, book_count, alias_count or id)", q.Sort)
	}
	return nil
}

// ValidateSeries is ValidateAuthors for the series list.
func (q ListQuery) ValidateSeries() error {
	switch q.Filter {
	case "", SeriesFilterAll, SeriesFilterWithBooks, SeriesFilterEmpty:
	default:
		return fmt.Errorf("invalid filter %q (want all, with_books or empty)", q.Filter)
	}
	if q.Sort != "" && !seriesSortKeys[q.Sort] {
		return fmt.Errorf("invalid sort %q (want name, author_name, book_count or id)", q.Sort)
	}
	return nil
}

// Apply returns the requested page of r. r is not modified. Count on the result
// is the number of authors matching the filter and search (all pages); Total is
// the size of r.
func (r *AuthorWithCountListResponse) Apply(q ListQuery) *AuthorWithCountListResponse {
	needle := strings.ToLower(strings.TrimSpace(q.Search))
	idx := make([]int, 0, len(r.Items))
	for i := range r.Items {
		a := &r.Items[i]
		switch q.Filter {
		case AuthorFilterWithBooks:
			if a.BookCount <= 0 {
				continue
			}
		case AuthorFilterZeroBooks:
			if a.BookCount != 0 {
				continue
			}
		}
		if needle != "" && !authorMatches(a, needle) {
			continue
		}
		idx = append(idx, i)
	}

	if q.Sort != "" {
		sortIndex(idx, q.Desc, func(i int) int { return r.Items[i].ID }, authorSortLess(r.Items, q.Sort))
	}

	start, end := window(len(idx), q)
	page := make([]AuthorWithCount, 0, end-start)
	for _, i := range idx[start:end] {
		page = append(page, r.Items[i])
	}
	return &AuthorWithCountListResponse{Items: page, Count: len(idx), Total: len(r.Items)}
}

// Apply is the series twin of AuthorWithCountListResponse.Apply.
func (r *SeriesWithCountsResponse) Apply(q ListQuery) *SeriesWithCountsResponse {
	needle := strings.ToLower(strings.TrimSpace(q.Search))
	idx := make([]int, 0, len(r.Items))
	for i := range r.Items {
		s := &r.Items[i]
		switch q.Filter {
		case SeriesFilterWithBooks:
			if s.BookCount <= 0 {
				continue
			}
		case SeriesFilterEmpty:
			if s.BookCount != 0 {
				continue
			}
		}
		if needle != "" &&
			!strings.Contains(strings.ToLower(s.Name), needle) &&
			!strings.Contains(strings.ToLower(s.AuthorName), needle) {
			continue
		}
		idx = append(idx, i)
	}

	if q.Sort != "" {
		sortIndex(idx, q.Desc, func(i int) int { return r.Items[i].ID }, seriesSortLess(r.Items, q.Sort))
	}

	start, end := window(len(idx), q)
	page := make([]SeriesWithCount, 0, end-start)
	for _, i := range idx[start:end] {
		page = append(page, r.Items[i])
	}
	return &SeriesWithCountsResponse{Items: page, Count: len(idx), Total: len(r.Items)}
}

// authorMatches reports whether the author's name, or any alias, contains the
// already-lowercased needle. Aliases are searched because the Authors page
// always did: "Stan Lee" has to find the row whose alias is "Stanley Lieber".
func authorMatches(a *AuthorWithCount, needle string) bool {
	if strings.Contains(strings.ToLower(a.Name), needle) {
		return true
	}
	for _, al := range a.Aliases {
		if strings.Contains(strings.ToLower(al.AliasName), needle) {
			return true
		}
	}
	return false
}

// window clamps q's offset/limit to n matches and returns [start,end). It is
// total over every int: a negative offset or limit (which the handler rejects,
// but a caller of Apply may still pass) is treated as 0, and a limit near
// MaxInt never reaches start+limit, so the sum cannot overflow into a negative
// slice index.
func window(n int, q ListQuery) (start, end int) {
	if !q.Paged {
		return 0, n
	}
	start = min(max(q.Offset, 0), n)
	end = n
	if q.Limit > 0 && q.Limit < n-start {
		end = start + q.Limit
	}
	return start, end
}

// sortIndex orders idx with less (ascending), reversing for desc and breaking
// ties on id ascending in BOTH directions.
func sortIndex(idx []int, desc bool, id func(int) int, less func(a, b int) (lt, eq bool)) {
	sort.SliceStable(idx, func(x, y int) bool {
		a, b := idx[x], idx[y]
		lt, eq := less(a, b)
		if eq {
			return id(a) < id(b)
		}
		if desc {
			return !lt
		}
		return lt
	})
}

func authorSortLess(items []AuthorWithCount, key string) func(a, b int) (lt, eq bool) {
	switch key {
	case "name":
		// Lower-cased once per author, not once per comparison.
		names := make([]string, len(items))
		for i := range items {
			names[i] = strings.ToLower(items[i].Name)
		}
		return func(a, b int) (bool, bool) { return names[a] < names[b], names[a] == names[b] }
	case "book_count":
		return func(a, b int) (bool, bool) {
			return items[a].BookCount < items[b].BookCount, items[a].BookCount == items[b].BookCount
		}
	case "alias_count":
		return func(a, b int) (bool, bool) {
			la, lb := len(items[a].Aliases), len(items[b].Aliases)
			return la < lb, la == lb
		}
	default: // id
		return func(a, b int) (bool, bool) { return items[a].ID < items[b].ID, items[a].ID == items[b].ID }
	}
}

func seriesSortLess(items []SeriesWithCount, key string) func(a, b int) (lt, eq bool) {
	switch key {
	case "name", "author_name":
		names := make([]string, len(items))
		for i := range items {
			if key == "name" {
				names[i] = strings.ToLower(items[i].Name)
			} else {
				names[i] = strings.ToLower(items[i].AuthorName)
			}
		}
		return func(a, b int) (bool, bool) { return names[a] < names[b], names[a] == names[b] }
	case "book_count":
		return func(a, b int) (bool, bool) {
			return items[a].BookCount < items[b].BookCount, items[a].BookCount == items[b].BookCount
		}
	default: // id
		return func(a, b int) (bool, bool) { return items[a].ID < items[b].ID, items[a].ID == items[b].ID }
	}
}
