// file: core/storage/index/query.go
// version: 1.0.0
// guid: a6ecfb1e-fe1d-4abc-a868-549b6b3bb5cd
// last-edited: 2026-11-11

package index

import (
	"fmt"
	"time"
)

// Query is engine-neutral; engine types never appear in the SDK. An engine
// reads one through Describe.
type Query interface{ isQuery() }

// Op is a query node's operator.
type Op uint8

const (
	OpInvalid Op = iota
	OpMatch      // Field, Text: analysed match
	OpTerm       // Field, Text: one exact term
	OpPrefix     // Field, Text: terms starting with Text
	OpRange      // Field, Min, Max: inclusive bounds, either may be nil
	OpAnd        // Children
	OpOr         // Children
	OpNot        // Children[0]
	OpString     // Text: the engine's user query syntax
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
}

type query struct{ n Node }

func (query) isQuery() {}

// badQuery carries a constructor's complaint to Describe, so constructors
// never panic and compose freely.
type badQuery struct{ err error }

func (badQuery) isQuery() {}

// Match is an analysed match of text against field.
func Match(field, text string) Query { return leaf(OpMatch, field, text) }

// Term is an exact term of field.
func Term(field, value string) Query { return leaf(OpTerm, field, value) }

// Prefix matches terms of field that start with prefix.
func Prefix(field, prefix string) Query { return leaf(OpPrefix, field, prefix) }

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
