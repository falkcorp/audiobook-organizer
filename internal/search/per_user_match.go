// file: internal/search/per_user_match.go
// version: 1.0.0
// guid: a82911d7-d9dc-4f47-9863-1a7638e47194
// last-edited: 2026-07-10

// Per-user DSL filter evaluation (INIT-4 T2). Moved here — verbatim —
// from internal/playlist/evaluator.go so internal/search is the single
// exported source of truth for matching a database.UserBookState
// against the PerUserFilter slice Translate peels off the DSL AST.
// Both the playlist evaluator and searchWithBleve delegate to
// MatchPerUserFilters instead of each carrying their own copy.

package search

import (
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// MatchPerUserFilters reports whether state satisfies EVERY filter
// (AND semantics; Negated inverts per filter). state==nil is evaluated
// as the zero-value UserBookState — read_status:finished rejects an
// unstarted book, negated filters can still succeed.
func MatchPerUserFilters(state *database.UserBookState, filters []PerUserFilter) bool {
	for _, f := range filters {
		ok := perUserFilterMatches(state, f.Node)
		if f.Negated {
			ok = !ok
		}
		if !ok {
			return false
		}
	}
	return true
}

// perUserFilterMatches evaluates a single FieldNode against a
// UserBookState. A nil state means the user has no record — only
// negated filters can succeed against nil.
func perUserFilterMatches(state *database.UserBookState, node *FieldNode) bool {
	if state == nil {
		// Treat absence as a zero-value state: status="" + progress=0.
		// That way `read_status:unstarted` (if the caller maps
		// "unstarted"→"") matches and `read_status:finished` rejects.
		state = &database.UserBookState{}
	}
	switch node.Field {
	case "read_status":
		return strings.EqualFold(state.Status, node.Value)
	case "progress_pct":
		return numericFieldMatches(float64(state.ProgressPct), node)
	case "last_played":
		if state.LastActivityAt.IsZero() {
			return false
		}
		return timeFieldMatches(state.LastActivityAt, node)
	default:
		return false
	}
}

func numericFieldMatches(got float64, node *FieldNode) bool {
	switch node.Op {
	case "range":
		lo, err1 := strconv.ParseFloat(node.RangeMin, 64)
		hi, err2 := strconv.ParseFloat(node.RangeMax, 64)
		if err1 != nil || err2 != nil {
			return false
		}
		return got >= lo && got <= hi
	case ">", "<", ">=", "<=", "=", "":
		want, err := strconv.ParseFloat(node.Value, 64)
		if err != nil {
			return false
		}
		switch node.Op {
		case ">":
			return got > want
		case "<":
			return got < want
		case ">=":
			return got >= want
		case "<=":
			return got <= want
		default:
			return got == want
		}
	}
	return false
}

func timeFieldMatches(got time.Time, node *FieldNode) bool {
	parse := func(s string) (time.Time, bool) {
		// Accept RFC3339 + plain YYYY-MM-DD.
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t, true
		}
		if t, err := time.Parse("2006-01-02", s); err == nil {
			return t, true
		}
		return time.Time{}, false
	}
	switch node.Op {
	case "range":
		lo, ok1 := parse(node.RangeMin)
		hi, ok2 := parse(node.RangeMax)
		if !ok1 || !ok2 {
			return false
		}
		return !got.Before(lo) && !got.After(hi)
	default:
		want, ok := parse(node.Value)
		if !ok {
			return false
		}
		switch node.Op {
		case ">":
			return got.After(want)
		case "<":
			return got.Before(want)
		case ">=":
			return got.After(want) || got.Equal(want)
		case "<=":
			return got.Before(want) || got.Equal(want)
		default:
			return got.Equal(want)
		}
	}
}
