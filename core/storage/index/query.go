// file: core/storage/index/query.go
// version: 1.0.0
// guid: a6ecfb1e-fe1d-4abc-a868-549b6b3bb5cd
// last-edited: 2026-11-11

package index

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Query is engine-neutral; engine types never appear in the SDK. An engine
// reads one through Describe.
type Query interface{ isQuery() }

// Op is a query node's operator.
type Op uint8

const (
	OpInvalid  Op = iota
	OpMatch       // Field, Text: analysed match
	OpTerm        // Field, Text: one exact term
	OpPrefix      // Field, Text: terms starting with Text
	OpRange       // Field, Min, Max: inclusive bounds, either may be nil
	OpAnd         // Children
	OpOr          // Children
	OpNot         // Children[0]
	OpString      // Text: the engine's user query syntax
	OpWildcard    // Field, Text: terms matching a * / ? pattern (Wildcard)
	OpAll         // no operands: every document (All)
	OpMatchAny    // Text: analysed match against every Text and Keyword field at once (MatchAnyField)
)

// Node is the engine-facing view of a Query: a plain tree an engine translates.
// Min and Max of an OpRange hold int64, float64 or time.Time, or nil for an
// open end.
type Node struct {
	Op       Op
	Field    string
	Text     string
	Min, Max any
	Children []Node
	// Boost is the node's own score weight, set by index.Boost; 0 = none.
	// On a Match it replaces the field's declared Field.Boost.
	Boost float64
}

type query struct{ n Node }

func (query) isQuery() {}

// badQuery carries a constructor's complaint to Describe, so constructors
// never panic and compose freely.
type badQuery struct{ err error }

func (badQuery) isQuery() {}

// Match is an analysed match of text against field.
func Match(field, text string) Query { return leaf(OpMatch, field, text) }

// Term is an exact term of field: value is matched against the index's terms
// without analysis, except that on a Text or Keyword field whose analyzer
// lowercases, the engine lowercases value first (an uppercase term could
// never match there).
func Term(field, value string) Query { return leaf(OpTerm, field, value) }

// Prefix matches terms of field that start with prefix. As with Term, the
// prefix is not analysed, except that it is lowercased on a field whose
// analyzer lowercases.
func Prefix(field, prefix string) Query { return leaf(OpPrefix, field, prefix) }

// Wildcard matches terms of field against pattern, where * stands for any
// run of characters (none included) and ? for exactly one; every other
// character is literal, and there is no escape for * and ?. Unlike a
// QueryString, it may start with * or ?, so "*man" and "*bat*" are allowed:
// it is the explicit opt-in to a pattern with no literal prefix.
//
// Cost. A pattern with a literal prefix ("bat*") narrows the term dictionary
// like Prefix. One that starts with * or ? cannot: the engine walks every
// term of the field (in every index segment) through the pattern's automaton,
// which is linear in the field's distinct terms, then searches each term that
// matched. That walk is cheap per term but unbounded by the pattern, so the
// engine bounds what follows it: every matched term counts against the
// engine's term-searcher cap for the whole search (1024 by default), and a
// search over the cap fails with ErrTooBroad instead of running; each
// Wildcard node also costs more of the query's node budget than a Term (see
// the engine). A pattern must hold at least one literal character ("*" alone
// enumerates the whole field and is refused).
//
// Case: on a field whose analyzer lowercases, the engine lowercases pattern;
// no other analysis runs (no stemming, no character filter), so a caller
// whose analyzer splits on a character, such as CharUnderscoreToSpace, splits
// the pattern itself and And-s the parts.
func Wildcard(field, pattern string) Query {
	if strings.Trim(pattern, "*?") == "" {
		return badQuery{fmt.Errorf("%w: wildcard %q has no literal character", ErrInvalidQuery, pattern)}
	}
	return leaf(OpWildcard, field, pattern)
}

// All matches every document of the index. It is the query of a listing
// with no search term (sort and page it with Page). It costs the engine a
// walk of every document, so it counts in a query's node budget as a Not
// does.
func All() Query { return query{Node{Op: OpAll}} }

// MatchAnyField is an analysed match of text against every Text and Keyword
// field of the index at once (the engine's composite field), unweighted: the
// unfielded fallback beside an Or of per-field Matches that carry the
// fields' Boosts. text is analysed with the standard analyzer, not with each
// field's own, and is matched against the terms each field's analyzer
// indexed: on a stemming field a word matches only where the standard form
// equals the stored stem ("famili" matches "family"; "families" does not).
func MatchAnyField(text string) Query { return query{Node{Op: OpMatchAny, Text: text}} }

func leaf(op Op, field, text string) Query {
	if field == "" {
		return badQuery{fmt.Errorf("%w: a field is required", ErrInvalidQuery)}
	}
	return query{Node{Op: op, Field: field, Text: text}}
}

// Range matches field values between min and max inclusive; a nil end is open.
func Range[T int64 | float64 | time.Time](field string, min, max *T) Query {
	if field == "" {
		return badQuery{fmt.Errorf("%w: a field is required", ErrInvalidQuery)}
	}
	if min == nil && max == nil {
		return badQuery{fmt.Errorf("%w: range over %q has neither bound", ErrInvalidQuery, field)}
	}
	n := Node{Op: OpRange, Field: field}
	if min != nil {
		n.Min = *min
	}
	if max != nil {
		n.Max = *max
	}
	if min != nil && max != nil && after(any(*min), any(*max)) {
		return badQuery{fmt.Errorf("%w: range over %q has min above max", ErrInvalidQuery, field)}
	}
	return query{n}
}

func after(a, b any) bool {
	switch x := a.(type) {
	case int64:
		return x > b.(int64)
	case float64:
		return x > b.(float64)
	case time.Time:
		return x.After(b.(time.Time))
	}
	return false
}

// And matches documents that match every query.
func And(qs ...Query) Query { return group(OpAnd, qs) }

// Or matches documents that match any query.
func Or(qs ...Query) Query { return group(OpOr, qs) }

// Not matches documents that do not match q.
func Not(q Query) Query { return group(OpNot, []Query{q}) }

func group(op Op, qs []Query) Query {
	if len(qs) == 0 {
		return badQuery{fmt.Errorf("%w: a group needs at least one query", ErrInvalidQuery)}
	}
	n := Node{Op: op}
	for _, q := range qs {
		c, err := Describe(q)
		if err != nil {
			return badQuery{err}
		}
		n.Children = append(n.Children, c)
	}
	return query{n}
}

// Boost weights q's score by weight, a finite number above 0: a user's
// "title:joker^3", or a weight a caller computes per clause. It is the
// query-level weight; Field.Boost is the field-level default. The two do not
// multiply: on a Match node an explicit Boost replaces the field's declared
// one (so "title:joker^2" scores 2 on a field declared 3, as HEAD's search
// does), and on every other leaf it is the only weight. Boosting a node
// twice keeps the last weight. On a group (And, Or, Not) or a QueryString the
// weight multiplies the weight of every scored leaf under it, an inner Boost
// or a ^n written inside the string included (a Not's excluded operand scores
// nothing and is not weighted).
//
// A caller that translates a tree which already carries every weight (HEAD's
// bleve tree, fan-out weights included) declares no Field.Boost and emits a
// Boost for each weighted node, which reproduces HEAD's scores exactly.
func Boost(q Query, weight float64) Query {
	n, err := Describe(q)
	if err != nil {
		return badQuery{err}
	}
	if math.IsNaN(weight) || math.IsInf(weight, 0) || weight <= 0 {
		return badQuery{fmt.Errorf("%w: boost %v must be a finite number above 0", ErrInvalidQuery, weight)}
	}
	n.Boost = weight
	return query{n}
}

// QueryString is the user query syntax, parsed by the engine.
func QueryString(s string) Query {
	return query{Node{Op: OpString, Text: s}}
}

// Describe returns q's tree, or the first problem a constructor recorded
// (ErrInvalidQuery). A nil Query is a problem, not a match-all.
func Describe(q Query) (Node, error) {
	switch v := q.(type) {
	case query:
		return v.n, nil
	case badQuery:
		return Node{}, v.err
	default:
		return Node{}, fmt.Errorf("%w: nil or foreign query %T", ErrInvalidQuery, q)
	}
}
