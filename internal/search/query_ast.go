// file: internal/search/query_ast.go
// version: 1.0.1
// guid: 8a2c4f1d-5b9e-4f60-a7c8-1d6e0f2b9a47
//
// AST for the library search DSL (spec 3.4 / DES-1 v1.1). Each
// concrete type is both the parser output and the input the
// Bleve translator walks to produce a query.Query.
//
// Operator syntax (user-facing):
//   AND:   whitespace | && | AND
//   OR:    ||  |  OR
//   NOT:   - prefix  |  NOT
//   Group: (…)
//   Within-field alternation: field:(a|b|c)
//   Numeric: field:>N  field:<N  field:>=N  field:<=N  field:[A TO B]
//   Prefix / wildcard: field:vamp*
//   Fuzzy: field:smith~
//   Boost: field:vampire^3
// last-edited: 2026-09-02

package search

import "strings"

import "fmt"

// Node is any AST node. The Kind() method exists so callers can
// type-switch by an enum rather than a reflect cast.
type Node interface {
	Kind() NodeKind
	// String returns a human-readable form of the node, mostly for
	// tests and for surfacing parse errors.
	String() string
}

// NodeKind enumerates the AST node types.
type NodeKind int

const (
	NodeAnd NodeKind = iota
	NodeOr
	NodeNot
	NodeField
	NodeFreeText
	NodeValueAlt
)

// AndNode represents conjunction. Arity ≥ 2. An AND of one child is
// collapsed by the parser into that child.
type AndNode struct {
	Children []Node
}

func (n *AndNode) Kind() NodeKind { return NodeAnd }
func (n *AndNode) String() string {
	return groupString("AND", n.Children)
}

// OrNode represents disjunction. Arity ≥ 2.
type OrNode struct {
	Children []Node
}

func (n *OrNode) Kind() NodeKind { return NodeOr }
func (n *OrNode) String() string {
	return groupString("OR", n.Children)
}

// NotNode wraps a single child. Both `-field:value` and `NOT …`
// produce this.
type NotNode struct {
	Child Node
}

func (n *NotNode) Kind() NodeKind { return NodeNot }
func (n *NotNode) String() string {
	if n.Child == nil {
		return "NOT(<nil>)"
	}
	return "NOT(" + n.Child.String() + ")"
}

// FieldNode is a single field:value clause, optionally with an
// operator inside the value expression (range, prefix, fuzzy,
// boost). Boost applies multiplicatively to this clause's score
// contribution.
type FieldNode struct {
	Field string
	// Value is the literal token after the colon (still includes
	// any trailing * / ~ / ^boost suffixes until the translator
	// unpacks them).
	Value string
	// Quoted marks whether the raw token was quoted — translator
	// uses this to pick MatchQuery vs MatchPhraseQuery.
	Quoted bool
	// Op is the operator distinguishing exact equality from
	// comparators. Empty string means default (match / contains).
	// Known values: "", ">", "<", ">=", "<=", "=", "range"
	Op string
	// Range (RangeMin, RangeMax) populated when Op == "range" —
	// parsed from field:[A TO B].
	RangeMin string
	RangeMax string
	// Fuzzy is set when the token had a ~ suffix. Edit distance
	// defaults to 2 in Bleve.
	Fuzzy bool
	// Prefix and Wildcard indicate * suffix / prefix / both.
	Prefix   bool
	Wildcard bool
	// Boost is a multiplicative boost (e.g. `^3`). Zero means no
	// user-specified boost; translator applies default 1.0.
	Boost float64
}

func (n *FieldNode) Kind() NodeKind { return NodeField }
func (n *FieldNode) String() string {
	if n.Op == "range" {
		return fmt.Sprintf("%s:[%s TO %s]", n.Field, n.RangeMin, n.RangeMax)
	}
	suffix := ""
	if n.Fuzzy {
		suffix += "~"
	}
	if n.Boost > 0 {
		suffix += fmt.Sprintf("^%g", n.Boost)
	}
	op := n.Op
	if op == "" || op == "=" {
		op = ""
	}
	return fmt.Sprintf("%s:%s%s%s", n.Field, op, n.Value, suffix)
}

// FreeTextNode is a bare token that wasn't scoped to a field.
// Translator uses this against an "all fields" query.
type FreeTextNode struct {
	Value  string
	Quoted bool
	Fuzzy  bool
	Prefix bool
}

func (n *FreeTextNode) Kind() NodeKind { return NodeFreeText }
func (n *FreeTextNode) String() string {
	q := n.Value
	if n.Quoted {
		q = `"` + q + `"`
	}
	if n.Prefix {
		q += "*"
	}
	if n.Fuzzy {
		q += "~"
	}
	return q
}

// ValueAltNode represents the `field:(a|b|c)` within-field
// alternation shortcut. Translator unfolds to a DisjunctionQuery
// of MatchQuery per value.
type ValueAltNode struct {
	Field  string
	Values []string
}

func (n *ValueAltNode) Kind() NodeKind { return NodeValueAlt }
func (n *ValueAltNode) String() string {
	var out strings.Builder
	out.WriteString(n.Field + ":(")
	for i, v := range n.Values {
		if i > 0 {
			out.WriteString("|")
		}
		out.WriteString(v)
	}
	return out.String() + ")"
}

func groupString(op string, children []Node) string {
	var out strings.Builder
	out.WriteString("(" + op)
	for _, c := range children {
		out.WriteString(" " + c.String())
	}
	return out.String() + ")"
}
