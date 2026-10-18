// file: internal/audiobooks/filter_date.go
// version: 1.0.0
// guid: 953a0b92-fcf9-4757-8f95-5c6463dfc016
// last-edited: 2026-10-18

package audiobooks

import (
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// Date filters on the Library list (owner decision D76).
//
// The Library page does not send a typed query as one string: its search box is
// split client-side and the field terms travel as a filters JSON array of
// field/value pairs, so this path, not the search index, is what answers
// date_added:>-1y typed into the Library. It reads the term with the search
// package's own parser and matcher (search.FieldNodeForTerm, MatchTimeNode), so
// a term means here exactly what it means in a dynamic collection or a smart
// playlist.

// dateFilterFields are the book-column date filters: when a book entered the
// library and when its record last changed. The short names are aliases.
// created_at / updated_at stay what they were, RFC 3339 substring filters.
var dateFilterFields = map[string]bool{
	"date_added": true, "added": true,
	"date_modified": true, "modified": true,
}

// dateFilter is a date term parsed ONCE per scan. now is fixed at that moment
// so every row in the scan is judged against the same instant: a relative
// window must not move between the first book and the last.
type dateFilter struct {
	node *search.FieldNode
	now  time.Time
}

// compileDateFilter parses and validates a date term on a date field (a book
// column or last_played). The error is what the list handler returns as its 400.
func compileDateFilter(f FieldFilter, now time.Time) (*dateFilter, error) {
	node, err := search.FieldNodeForTerm(f.Field, f.Value, f.Quoted)
	if err != nil {
		return nil, err
	}
	if err := search.CheckDateNode(node, now); err != nil {
		return nil, err
	}
	return &dateFilter{node: node, now: now}, nil
}

// matchesBook reports whether the book's date for field satisfies the term. An
// unset timestamp (nil) never matches, so a negated term keeps such a book.
func (d *dateFilter) matchesBook(book *database.Book, field string) bool {
	var t *time.Time
	switch field {
	case "date_added", "added":
		t = book.CreatedAt
	case "date_modified", "modified":
		t = book.UpdatedAt
	}
	if t == nil {
		return false
	}
	return search.MatchTimeNode(*t, d.node, d.now)
}

// HasRelativeDateFilter reports whether any filter holds a date term whose
// answer depends on the clock (now, or a signed offset such as -30d). A cache
// keyed on the filters alone must not store such a result: the same key means
// a different set tomorrow.
func HasRelativeDateFilter(filtersets ...[]FieldFilter) bool {
	for _, set := range filtersets {
		for _, f := range set {
			if !dateFilterFields[f.Field] && f.Field != "last_played" {
				continue
			}
			node, err := search.FieldNodeForTerm(f.Field, f.Value, f.Quoted)
			if err != nil {
				continue
			}
			if search.HasRelativeDate(node) {
				return true
			}
		}
	}
	return false
}

// perUserMatcher is a request's per-user filters (read_status, progress_pct,
// last_played) built once, before the row walk, with the instant relative dates
// resolve against. It judges a row with search.MatchPerUserFilters, the same
// matcher a smart playlist and a dynamic collection use, so "unstarted" and
// "played in the last 30 days" mean one thing everywhere.
type perUserMatcher struct {
	filters []search.PerUserFilter
	now     time.Time
}

// newPerUserMatcher builds the matcher. A filter that does not parse is an
// error, not a silent no-match: the entry points validate first, so reaching one
// here is a bypassed boundary.
func newPerUserMatcher(filters []FieldFilter) (*perUserMatcher, error) {
	m := &perUserMatcher{now: time.Now(), filters: make([]search.PerUserFilter, 0, len(filters))}
	for _, f := range filters {
		node, err := search.FieldNodeForTerm(f.Field, f.Value, f.Quoted)
		if err != nil {
			return nil, err
		}
		m.filters = append(m.filters, search.PerUserFilter{Node: node, Negated: f.Negated})
	}
	return m, nil
}

// matches reports whether state satisfies every per-user filter. A nil state is
// a book the user has no record for; the matcher reads it as a book not started.
func (m *perUserMatcher) matches(state *database.UserBookState) bool {
	return search.MatchPerUserFilters(state, m.filters, m.now)
}
