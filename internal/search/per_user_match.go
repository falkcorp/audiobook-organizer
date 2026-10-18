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
//
// now is the instant relative date terms (last_played:>-30d) resolve against.
// The caller takes it ONCE per evaluation and passes the same value for every
// row, so a window cannot move between the first book and the last one; the
// clock is not read here. A caller with no relative terms still passes it.
func MatchPerUserFilters(state *database.UserBookState, filters []PerUserFilter, now time.Time) bool {
	for _, f := range filters {
		ok := perUserFilterMatches(state, f.Node, now)
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
func perUserFilterMatches(state *database.UserBookState, node *FieldNode, now time.Time) bool {
	if state == nil {
		// Treat absence as a zero-value state: status="" + progress=0.
		state = &database.UserBookState{}
	}
	switch node.Field {
	case "read_status":
		// A book the user has no row for, or whose row carries no status yet,
		// has not been started: it is "unstarted", the same word the status
		// machine writes for an auto row that has not moved. Comparing the raw
		// empty string meant read_status:unstarted matched only rows that said
		// so and missed every book the user never opened, which is most of the
		// library.
		status := state.Status
		if status == "" {
			status = database.UserBookStatusUnstarted
		}
		return strings.EqualFold(status, node.Value)
	case "progress_pct":
		return numericFieldMatches(float64(state.ProgressPct), node)
	case "last_played":
		if state.LastActivityAt.IsZero() {
			return false
		}
		return timeFieldMatches(state.LastActivityAt, node, now)
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

// timeFieldMatches evaluates a date comparison. Terms are absolute (RFC 3339,
// YYYY-MM-DD) or relative to now (see ParseDateTerm); a term that does not parse
// matches nothing. An equality on a bare YYYY-MM-DD matches the whole UTC day.
func timeFieldMatches(got time.Time, node *FieldNode, now time.Time) bool {
	switch node.Op {
	case "range":
		lo, ok1 := ParseDateTerm(node.RangeMin, now)
		hi, ok2 := ParseDateTerm(node.RangeMax, now)
		if !ok1 || !ok2 {
			return false
		}
		return !got.Before(lo) && !got.After(hi)
	default:
		want, ok := ParseDateTerm(node.Value, now)
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
			if isDateOnly(node.Value) {
				lo, hi := dateDayBounds(want)
				return !got.Before(lo) && got.Before(hi)
			}
			return got.Equal(want)
		}
	}
}
