// file: internal/search/relative_date.go
// version: 1.2.0
// guid: c3a96e18-5d47-4b02-8f1e-a7d2b04c6e93
// last-edited: 2026-10-18

package search

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/querygrammar"
)

// Date terms in the one search syntax (owner decision D76).
//
// A date value is one of
//
//	2026-09-01             a calendar day, midnight UTC
//	2026-09-01T10:30:00Z   an RFC 3339 instant
//	now                    the instant the search runs
//	-30d  +2w  -6m  -1y    an offset from now: a sign, a non-negative integer,
//	                       and a unit (d days, w weeks of 7 days, m calendar
//	                       months, y calendar years)
//
// so last_played:>-30d means "played after thirty days before this search ran"
// and last_played:-30d..now is the same window as a range. Offsets are applied
// with time.Time.AddDate in UTC, so a month or year offset follows Go's
// normalisation: the day of month is kept and an overflow rolls into the next
// month (2026-03-31 minus 1m is 2026-03-03, because "February 31" is March 3).
// A date-only value is a day, not a point, so an equality on one matches the
// whole UTC day (dateDayBounds).
//
// The sign is required on an offset. A bare "30d" is refused with a message
// showing "-30d" rather than guessed at: it reads as "30 days" in either
// direction, and a guess would silently shift every saved query.
//
// "Resolved when the search runs": the offset is turned into an instant by the
// caller's `now`, passed in so every row of one evaluation sees the same instant
// and so tests can fix it. Nothing here reads the clock.

// dateTermLayouts are the absolute forms, tried in order.
var dateTermLayouts = []string{time.RFC3339, "2006-01-02"}

// ParseDateTerm resolves a date term to an instant relative to now. It reports
// ok=false for anything that is not a valid term; use ParseDateTermErr when the
// reason is wanted (the index translator surfaces it to the person searching).
func ParseDateTerm(s string, now time.Time) (time.Time, bool) {
	t, err := ParseDateTermErr(s, now)
	return t, err == nil
}

// ParseDateTermErr is ParseDateTerm with the reason a term was refused.
func ParseDateTermErr(s string, now time.Time) (time.Time, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return time.Time{}, fmt.Errorf("empty date")
	}
	if strings.EqualFold(raw, "now") {
		return now.UTC(), nil
	}
	for _, layout := range dateTermLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, nil
		}
	}

	// Offset: [+-]N<unit>.
	if t, handled, err := parseDateOffset(raw, now); handled {
		return t, err
	}
	return time.Time{}, fmt.Errorf("invalid date %q: use YYYY-MM-DD, now, or an offset like -30d (units d, w, m, y)",
		querygrammar.ShortToken(raw))
}

// maxDateOffset bounds N so AddDate cannot overflow into a nonsense date.
const maxDateOffset = 100000

// parseDateOffset handles the [+-]N<unit> form. handled is false when raw does
// not look like an offset at all (no unit letter, or no digits before it), so
// the caller reports the generic message.
func parseDateOffset(raw string, now time.Time) (t time.Time, handled bool, err error) {
	if len(raw) < 2 {
		return time.Time{}, false, nil
	}
	unit := raw[len(raw)-1] | 0x20
	if unit != 'd' && unit != 'w' && unit != 'm' && unit != 'y' {
		return time.Time{}, false, nil
	}
	body := raw[:len(raw)-1]
	signed := body[0] == '+' || body[0] == '-'
	digits := body
	if signed {
		digits = body[1:]
	}
	if digits == "" || strings.Trim(digits, "0123456789") != "" {
		return time.Time{}, false, nil
	}
	if !signed {
		return time.Time{}, true, fmt.Errorf("date offset %q needs a sign: write -%s%c for %s ago, or +%s%c for %s from now",
			querygrammar.ShortToken(raw), digits, unit, dateUnitName(unit, digits), digits, unit, dateUnitName(unit, digits))
	}
	n, aerr := strconv.Atoi(digits)
	if aerr != nil || n > maxDateOffset {
		return time.Time{}, true, fmt.Errorf("date offset %q is too large (at most %d)", querygrammar.ShortToken(raw), maxDateOffset)
	}
	if body[0] == '-' {
		n = -n
	}
	base := now.UTC()
	switch unit {
	case 'd':
		return base.AddDate(0, 0, n), true, nil
	case 'w':
		return base.AddDate(0, 0, 7*n), true, nil
	case 'm':
		return base.AddDate(0, n, 0), true, nil
	default:
		return base.AddDate(n, 0, 0), true, nil
	}
}

func dateUnitName(unit byte, digits string) string {
	name := map[byte]string{'d': "day", 'w': "week", 'm': "month", 'y': "year"}[unit]
	if digits != "1" {
		name += "s"
	}
	return digits + " " + name
}

// isDateOnly reports whether s is a bare YYYY-MM-DD, the one absolute form that
// names a whole day rather than an instant.
func isDateOnly(s string) bool {
	_, err := time.Parse("2006-01-02", strings.TrimSpace(s))
	return err == nil
}

// dateDayBounds returns the half-open UTC day [lo, hi) a date-only term names.
func dateDayBounds(day time.Time) (lo, hi time.Time) {
	lo = day.UTC().Truncate(24 * time.Hour)
	return lo, lo.AddDate(0, 0, 1)
}

// dateInterval is the span of time a date term selects: from lo (inclusive)
// until hi (exclusive); a missing side is unbounded. Matching and translating
// both go through it, so the Go matcher and the Bleve date range read the same
// bounds for a single term. (A query that combines terms is a separate matter:
// see MatchPerUserFilters for how per-user terms combine.)
type dateInterval struct {
	lo, hi       time.Time
	hasLo, hasHi bool
}

func (iv dateInterval) contains(t time.Time) bool {
	if iv.hasLo && t.Before(iv.lo) {
		return false
	}
	if iv.hasHi && !t.Before(iv.hi) {
		return false
	}
	return true
}

// dateTermInterval resolves a date term (comparison, equality or range) against
// now. A bare YYYY-MM-DD names a whole UTC day, so it is read by what the
// operator says about that day: ">d" is after the whole day, ">=d" from its
// start, "<d" before it, "<=d" through its end, "=d" the day itself, and in
// "a..b" or "[a TO b]" the upper bound b includes all of day b. A full instant
// (RFC 3339, "now" or an offset like -30d) is exact: "<=t" includes t itself and
// ">t" starts just after it.
func dateTermInterval(n *FieldNode, now time.Time) (dateInterval, error) {
	var iv dateInterval
	// start is the first instant a bound reaches; end is the first instant
	// after it. For a day they are the day's two edges, for an instant t and t+1ns.
	bound := func(s string) (start, end time.Time, err error) {
		t, perr := ParseDateTermErr(s, now)
		if perr != nil {
			return start, end, fmt.Errorf("%s: %w", n.Field, perr)
		}
		if isDateOnly(s) {
			lo, hi := dateDayBounds(t)
			return lo, hi, nil
		}
		return t, t.Add(time.Nanosecond), nil
	}
	if n.Op == "range" {
		lo, _, err := bound(n.RangeMin)
		if err != nil {
			return iv, err
		}
		_, hi, err := bound(n.RangeMax)
		if err != nil {
			return iv, err
		}
		iv.lo, iv.hasLo, iv.hi, iv.hasHi = lo, true, hi, true
		return iv, nil
	}
	start, end, err := bound(n.Value)
	if err != nil {
		return iv, err
	}
	switch n.Op {
	case ">":
		iv.lo, iv.hasLo = end, true
	case ">=":
		iv.lo, iv.hasLo = start, true
	case "<":
		iv.hi, iv.hasHi = start, true
	case "<=":
		iv.hi, iv.hasHi = end, true
	default:
		iv.lo, iv.hasLo, iv.hi, iv.hasHi = start, true, end, true
	}
	return iv, nil
}

// dateFields is every query field that takes a date term, with its aliases.
// last_played is per-user and answered by the Go matcher; the others are
// indexed (datetime fields of BookDocument) and answered by the Bleve
// translator. itunes_last_played is NOT here: it lives on Book, is not indexed,
// and stays a sort-only field.
var dateFields = map[string]struct{}{
	"last_played":   {},
	"date_added":    {},
	"added":         {},
	"date_modified": {},
	"modified":      {},
}

// IsDateField reports whether name takes a date term.
func IsDateField(name string) bool {
	_, ok := dateFields[name]
	return ok
}

// QueryHasRelativeDate reports whether the query text has a relative date term.
// Text that does not parse reports false: it cannot be evaluated at all.
func QueryHasRelativeDate(query string) bool {
	ast, err := ParseQuery(query)
	if err != nil {
		return false
	}
	return HasRelativeDate(ast)
}

// isRelativeDateTerm reports whether v is a date term whose instant depends on
// the clock: "now" or a signed offset. Absolute dates and anything unparseable
// are not (an unparseable term matches nothing, whatever the time).
func isRelativeDateTerm(v string) bool {
	probe := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	raw := strings.TrimSpace(v)
	if strings.EqualFold(raw, "now") {
		return true
	}
	if _, ok := ParseDateTerm(raw, probe); !ok {
		return false
	}
	return !isDateOnly(raw) && !looksAbsolute(raw)
}

func looksAbsolute(raw string) bool {
	_, err := time.Parse(time.RFC3339, raw)
	return err == nil
}

// HasRelativeDate reports whether the query holds a relative date term on a date
// field. Caches use it: such a query's answer changes with the clock while
// nothing in a cache key or the store change log does.
func HasRelativeDate(n Node) bool {
	switch v := n.(type) {
	case *AndNode:
		for _, c := range v.Children {
			if HasRelativeDate(c) {
				return true
			}
		}
	case *OrNode:
		for _, c := range v.Children {
			if HasRelativeDate(c) {
				return true
			}
		}
	case *NotNode:
		return HasRelativeDate(v.Child)
	case *FieldNode:
		if !IsDateField(v.Field) {
			return false
		}
		if v.Op == "range" {
			return isRelativeDateTerm(v.RangeMin) || isRelativeDateTerm(v.RangeMax)
		}
		return isRelativeDateTerm(v.Value)
	case *ValueAltNode:
		// date_added:(-30d|-1y): each alternative is a date term of its own.
		if !IsDateField(v.Field) {
			return false
		}
		for _, alt := range v.Values {
			if isRelativeDateTerm(alt) {
				return true
			}
		}
	}
	return false
}
