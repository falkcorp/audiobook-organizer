// file: internal/search/field_term.go
// version: 1.1.0
// guid: 8787a505-24bd-4ccd-a676-dea643446c7e
// last-edited: 2026-10-26

package search

import (
	"fmt"
	"strings"
	"time"
)

// FieldNodeForTerm builds the FieldNode the query parser builds for field:value,
// for a caller that already holds the value as a separate string (the Library
// list sends its filters as field/value pairs, not as a query string). It runs
// the parser's own value rules - the comparison operator prefix, [a TO b], the
// date-only a..b range, the trailing * - so a term means the same thing here
// as in a dynamic collection or a smart playlist. A quoted value is literal:
// no operator, no wildcard, no range.
func FieldNodeForTerm(field, value string, quoted bool) (*FieldNode, error) {
	n, err := NodeForTerm(field, value, quoted)
	if err != nil {
		return nil, err
	}
	fn, ok := n.(*FieldNode)
	if !ok {
		return nil, fmt.Errorf("%s: %q is not a single value", field, value)
	}
	return fn, nil
}

// NodeForTerm is FieldNodeForTerm for a caller that also accepts the
// alternation field:(a|b), which the parser reads as a ValueAltNode rather than
// as a FieldNode.
func NodeForTerm(field, value string, quoted bool) (Node, error) {
	if quoted {
		return &FieldNode{Field: field, Value: value, Quoted: true}, nil
	}
	p := &parser{input: value}
	n, err := p.parseFieldValue(field)
	if err != nil {
		return nil, err
	}
	p.skipWhitespace()
	if p.pos < len(p.input) {
		return nil, fmt.Errorf("%s: unexpected trailing input in %q", field, value)
	}
	return n, nil
}

// PerUserFilterForTerm builds the PerUserFilter for one per-user term the Library
// list sends as a field/value pair (read_status, progress_pct, last_played), and
// refuses what CheckPerUserValues refuses in a query string. It is the one rule
// for the filters= parameter: the validation at the request boundary and the
// matcher that later judges each book both come through here, so a value the
// list accepts is a value the matcher can evaluate. Before this, filters= was
// checked only for patterns, and -read_status:completed returned the whole
// library while progress_pct:[* TO 50] matched nothing.
//
// The alternation read_status:(a|b) is one filter that ORs inside itself, as the
// translator builds it for a query string.
func PerUserFilterForTerm(field, value string, quoted, negated bool, now time.Time) (PerUserFilter, error) {
	n, err := NodeForTerm(field, value, quoted)
	if err != nil {
		return PerUserFilter{}, err
	}
	if err := CheckPerUserValues(n, now); err != nil {
		return PerUserFilter{}, err
	}
	switch v := n.(type) {
	case *FieldNode:
		return PerUserFilter{Node: v, Negated: negated}, nil
	case *ValueAltNode:
		if f, ok := perUserAltFilter(v, negated); ok {
			return f, nil
		}
	}
	return PerUserFilter{}, fmt.Errorf("%s: %q is not a term the per-user pass can evaluate", field, value)
}

// CheckDateNode reports why a date term on a date field is refused, or nil. It
// is the validation the Bleve translator applies to the indexed date fields,
// for callers that match in Go instead. last_played:* (has been played at all)
// is the one term that is not a date, and only last_played has it.
func CheckDateNode(n *FieldNode, now time.Time) error {
	if isPresenceTerm(n) {
		return nil
	}
	check := func(s string) error {
		if _, err := ParseDateTermErr(s, now); err != nil {
			return fmt.Errorf("%s: %w", n.Field, err)
		}
		return nil
	}
	if n.Op == "range" {
		if err := check(n.RangeMin); err != nil {
			return err
		}
		return check(n.RangeMax)
	}
	return check(n.Value)
}

// MatchTimeNode reports whether got satisfies a date term (a comparison, an
// equality on a day, or a range) resolved against now. A zero got never matches:
// the field is unset. Relative terms resolve against now, which the caller takes
// once per evaluation so every row sees the same instant.
func MatchTimeNode(got time.Time, n *FieldNode, now time.Time) bool {
	if got.IsZero() {
		return false
	}
	if isPresenceTerm(n) {
		return true
	}
	return timeFieldMatches(got, n, now)
}

// isPresenceTerm reports whether n is last_played:* - "has been played". The
// indexed dates (date_added, date_modified) are always set, so a * there is not
// a form: it falls through to the date parse and is refused.
func isPresenceTerm(n *FieldNode) bool {
	return n.Field == "last_played" && n.Op == "" && n.Prefix && strings.TrimSpace(n.Value) == ""
}
