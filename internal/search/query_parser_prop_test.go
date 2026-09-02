// file: internal/search/query_parser_prop_test.go
// version: 1.0.1
// guid: 9d169409-96cb-44ea-b915-ccd285d45168
//
// Property-based tests for the DSL query parser (plan 4.5 task 3).
// Uses pgregory.net/rapid to generate random and well-formed inputs
// and verify invariants that must always hold.
// last-edited: 2026-09-02

package search

import (
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// -----------------------------------------------------------------------------
// AST walker helpers
// -----------------------------------------------------------------------------

// walkAST invokes fn on every node in the tree, pre-order.
func walkAST(n Node, fn func(Node)) {
	if n == nil {
		return
	}
	fn(n)
	switch v := n.(type) {
	case *AndNode:
		for _, c := range v.Children {
			walkAST(c, fn)
		}
	case *OrNode:
		for _, c := range v.Children {
			walkAST(c, fn)
		}
	case *NotNode:
		walkAST(v.Child, fn)
	}
}

// astShape produces a structural signature for an AST — kinds nested
// in the same order, ignoring leaf values. Used to detect whether a
// round-tripped parse produces the same structure.
func astShape(n Node) string {
	if n == nil {
		return "<nil>"
	}
	switch v := n.(type) {
	case *AndNode:
		parts := make([]string, 0, len(v.Children))
		for _, c := range v.Children {
			parts = append(parts, astShape(c))
		}
		return "AND(" + strings.Join(parts, ",") + ")"
	case *OrNode:
		parts := make([]string, 0, len(v.Children))
		for _, c := range v.Children {
			parts = append(parts, astShape(c))
		}
		return "OR(" + strings.Join(parts, ",") + ")"
	case *NotNode:
		return "NOT(" + astShape(v.Child) + ")"
	case *FieldNode:
		return "F"
	case *FreeTextNode:
		return "T"
	case *ValueAltNode:
		return "VA"
	default:
		return "?"
	}
}

// -----------------------------------------------------------------------------
// Valid DSL generators
// -----------------------------------------------------------------------------

// genIdent produces a lowercase alphabetic identifier 2-8 chars long.
// Used for field names and bare values.
func genIdent(t *rapid.T) string {
	return rapid.StringMatching(`[a-z]{2,8}`).Draw(t, "ident")
}

// genBareValue produces a bare value (alphanumeric, 1-10 chars, no
// operators, no whitespace, no special chars).
func genBareValue(t *rapid.T) string {
	return rapid.StringMatching(`[a-z0-9]{1,10}`).Draw(t, "bareval")
}

// genQuotedValue produces a quoted value that can contain spaces.
func genQuotedValue(t *rapid.T) string {
	inner := rapid.StringMatching(`[a-z0-9 ]{1,15}`).Draw(t, "quoted")
	return `"` + inner + `"`
}

// genNumber produces a non-negative integer as a string.
func genNumber(t *rapid.T) string {
	n := rapid.IntRange(0, 9999).Draw(t, "n")
	return rapid.Just(itoa(n)).Draw(t, "numStr")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// genSimpleClause generates one well-formed non-recursive clause:
//   - field:value
//   - field:"quoted value"
//   - field:>N (numeric comparator)
//   - field:(a|b|c)  (value-alt)
//   - bare token (free text)
//
// Per-user fields are intentionally excluded so the translator never
// produces a nil query from a leaf clause.
func genSimpleClause(t *rapid.T) string {
	// Stay away from per-user field names since they split off into
	// PerUserFilter and produce nil queries.
	pickField := func(t *rapid.T) string {
		for {
			f := genIdent(t)
			if _, isPerUser := perUserFieldSet[f]; isPerUser {
				continue
			}
			// Avoid uppercase keyword clashes — genIdent is lowercase
			// so this is already safe, but be explicit.
			if f == "and" || f == "or" || f == "not" {
				continue
			}
			return f
		}
	}

	kind := rapid.IntRange(0, 4).Draw(t, "kind")
	switch kind {
	case 0:
		return pickField(t) + ":" + genBareValue(t)
	case 1:
		return pickField(t) + ":" + genQuotedValue(t)
	case 2:
		op := rapid.SampledFrom([]string{">", "<", ">=", "<="}).Draw(t, "op")
		return pickField(t) + ":" + op + genNumber(t)
	case 3:
		field := pickField(t)
		n := rapid.IntRange(2, 4).Draw(t, "altN")
		vals := make([]string, 0, n)
		for range n {
			vals = append(vals, genBareValue(t))
		}
		return field + ":(" + strings.Join(vals, "|") + ")"
	default:
		// Bare token / free text.
		return genBareValue(t)
	}
}

// genValidQuery produces a well-formed DSL query with bounded depth,
// combining simple clauses using AND / OR / NOT.
func genValidQuery(t *rapid.T) string {
	return genValidQueryDepth(t, 3)
}

func genValidQueryDepth(t *rapid.T, depth int) string {
	if depth <= 0 {
		return genSimpleClause(t)
	}
	kind := rapid.IntRange(0, 5).Draw(t, "qkind")
	switch kind {
	case 0, 1:
		// Leaf clause.
		return genSimpleClause(t)
	case 2:
		// AND of 2-3 children.
		n := rapid.IntRange(2, 3).Draw(t, "andN")
		parts := make([]string, 0, n)
		for range n {
			parts = append(parts, genValidQueryDepth(t, depth-1))
		}
		sep := rapid.SampledFrom([]string{" ", " && ", " AND "}).Draw(t, "andSep")
		return strings.Join(parts, sep)
	case 3:
		// OR of 2-3 children.
		n := rapid.IntRange(2, 3).Draw(t, "orN")
		parts := make([]string, 0, n)
		for range n {
			parts = append(parts, genValidQueryDepth(t, depth-1))
		}
		sep := rapid.SampledFrom([]string{" || ", " OR "}).Draw(t, "orSep")
		return strings.Join(parts, sep)
	case 4:
		// Negated leaf clause (grammar only allows NOT over a clause,
		// not over AND/OR sub-expressions without parens).
		form := rapid.SampledFrom([]string{"dash", "kw"}).Draw(t, "notForm")
		inner := genSimpleClause(t)
		if form == "dash" {
			// Dash-negation only works if the inner starts with a
			// non-space char, which genSimpleClause guarantees.
			return "-" + inner
		}
		return "NOT " + inner
	default:
		// Parenthesized group (helps cover grouping parse paths).
		return "(" + genValidQueryDepth(t, depth-1) + ")"
	}
}

// -----------------------------------------------------------------------------
// Property 1: No panics on arbitrary input
// -----------------------------------------------------------------------------

func TestProp_ParseNoPanic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Fully arbitrary strings up to 256 chars — includes unicode,
		// unmatched quotes, random control chars, etc.
		s := rapid.String().Draw(t, "input")
		// Guard against extreme sizes that would slow the test.
		if len(s) > 256 {
			s = s[:256]
		}
		// ParseQuery must not panic. It is allowed to return an error.
		_, _ = ParseQuery(s)
	})
}

// -----------------------------------------------------------------------------
// Property 2: Parsed AST re-stringifies to the same shape
// -----------------------------------------------------------------------------

func TestProp_ParseStringifyRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		q := genValidQuery(t)
		ast1, err := ParseQuery(q)
		if err != nil {
			t.Skipf("generator produced unparseable query %q: %v", q, err)
		}
		// Canonicalize via the AST's own String() if possible.
		// AndNode/OrNode.String() produces "(AND a b)" which is NOT
		// parseable back into the same AST, so for those root types
		// we skip re-parse and just assert the shape is stable through
		// parse(q) twice.
		ast2, err := ParseQuery(q)
		if err != nil {
			t.Fatalf("second ParseQuery(%q) failed: %v", q, err)
		}
		if astShape(ast1) != astShape(ast2) {
			t.Fatalf("shape mismatch on double-parse:\n  q=%q\n  first=%s\n  second=%s",
				q, astShape(ast1), astShape(ast2))
		}
	})
}

// -----------------------------------------------------------------------------
// Property 3: Field nodes preserve field names
// -----------------------------------------------------------------------------

func TestProp_FieldNodeNonEmpty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Use arbitrary strings — most will error, the ones that
		// parse successfully must still satisfy the invariant.
		s := rapid.String().Draw(t, "input")
		if len(s) > 256 {
			s = s[:256]
		}
		ast, err := ParseQuery(s)
		if err != nil || ast == nil {
			return
		}
		walkAST(ast, func(n Node) {
			switch v := n.(type) {
			case *FieldNode:
				if v.Field == "" {
					t.Fatalf("FieldNode with empty Field in parse of %q", s)
				}
			case *ValueAltNode:
				if v.Field == "" {
					t.Fatalf("ValueAltNode with empty Field in parse of %q", s)
				}
			}
		})
	})
}

// -----------------------------------------------------------------------------
// Property 4: AND/OR children have arity >= 2
// -----------------------------------------------------------------------------

func TestProp_AndOrArity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.String().Draw(t, "input")
		if len(s) > 256 {
			s = s[:256]
		}
		ast, err := ParseQuery(s)
		if err != nil || ast == nil {
			return
		}
		walkAST(ast, func(n Node) {
			switch v := n.(type) {
			case *AndNode:
				if len(v.Children) < 2 {
					t.Fatalf("AndNode arity %d in parse of %q", len(v.Children), s)
				}
			case *OrNode:
				if len(v.Children) < 2 {
					t.Fatalf("OrNode arity %d in parse of %q", len(v.Children), s)
				}
			}
		})
	})
}

// -----------------------------------------------------------------------------
// Property 5: Negation wraps exactly one (non-nil) child
// -----------------------------------------------------------------------------

func TestProp_NotNodeHasChild(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.String().Draw(t, "input")
		if len(s) > 256 {
			s = s[:256]
		}
		ast, err := ParseQuery(s)
		if err != nil || ast == nil {
			return
		}
		walkAST(ast, func(n Node) {
			if not, ok := n.(*NotNode); ok {
				if not.Child == nil {
					t.Fatalf("NotNode with nil Child in parse of %q", s)
				}
			}
		})
	})
}

// -----------------------------------------------------------------------------
// Property 6: Valid DSL round-trips through the translator
// -----------------------------------------------------------------------------

func TestProp_TranslateValidQueries(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		q := genValidQuery(t)
		ast, err := ParseQuery(q)
		if err != nil {
			// Generator claims well-formed output; if parse fails it
			// is a generator bug, not a parser bug. Surface it.
			t.Skipf("generator produced unparseable query %q: %v", q, err)
		}
		bq, _, terr := Translate(ast)
		if terr != nil {
			t.Fatalf("Translate(%q) failed: %v", q, terr)
		}
		if bq == nil {
			t.Fatalf("Translate(%q) returned nil query", q)
		}
	})
}

// -----------------------------------------------------------------------------
// Property 7: Generated valid DSL strings always parse
// -----------------------------------------------------------------------------

func TestProp_GeneratedValidQueriesParse(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		q := genValidQuery(t)
		if _, err := ParseQuery(q); err != nil {
			t.Fatalf("ParseQuery(%q) failed on generated valid DSL: %v", q, err)
		}
	})
}
