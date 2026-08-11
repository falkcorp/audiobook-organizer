// file: internal/search/bleve_translator.go
// version: 1.2.1
// guid: 9c2a4f1d-5b3e-4f70-a7d6-2e8c0f1b9a47
// last-edited: 2026-08-11
//
// AST → Bleve query translator (spec DES-1 v1.1). Walks the AST
// produced by ParseQuery and emits a bleve/v2 query.Query suitable
// for passing to BleveIndex.SearchNative.
//
// Per-user fields (read_status, progress_pct, last_played) are NOT
// sent to Bleve — they're split off into a PerUserFilter list for
// Go-side post-filtering per spec 3.6 §5 + DES-1 v1.1 per-user
// deferred section.

package search

import (
	"fmt"
	"strconv"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/analysis"
	"github.com/blevesearch/bleve/v2/analysis/lang/en"
	"github.com/blevesearch/bleve/v2/registry"
	"github.com/blevesearch/bleve/v2/search/query"
)

// PerUserFilter is a FieldNode that references per-user state
// (read_status, progress_pct, last_played). Translator collects
// these into a slice the caller applies after Bleve returns
// candidate book IDs.
type PerUserFilter struct {
	Node *FieldNode
	// Negated is true when this filter sat under a NotNode in the
	// AST. Go post-filter must invert the match.
	Negated bool
}

// perUserFieldSet is the set of field names routed to Go post-filter
// instead of Bleve. Spec 3.6 §5.
var perUserFieldSet = map[string]struct{}{
	"read_status":  {},
	"progress_pct": {},
	"last_played":  {},
}

// Translate converts an AST into a Bleve query plus any per-user
// filters. Returns an error if the AST references unknown operators
// or if a range can't be parsed as numeric.
func Translate(node Node) (query.Query, []PerUserFilter, error) {
	if node == nil {
		return bleve.NewMatchAllQuery(), nil, nil
	}
	var perUser []PerUserFilter
	q, err := translateNode(node, &perUser, false)
	if err != nil {
		return nil, nil, err
	}
	if q == nil {
		// Only per-user filters — let Bleve return everything, Go
		// post-filter narrows.
		return bleve.NewMatchAllQuery(), perUser, nil
	}
	return q, perUser, nil
}

func translateNode(n Node, perUser *[]PerUserFilter, negated bool) (query.Query, error) {
	switch v := n.(type) {
	case *AndNode:
		return translateAnd(v, perUser, negated)
	case *OrNode:
		return translateOr(v, perUser, negated)
	case *NotNode:
		return translateNot(v, perUser, negated)
	case *FieldNode:
		return translateField(v, perUser, negated)
	case *ValueAltNode:
		return translateValueAlt(v, perUser, negated)
	case *FreeTextNode:
		return translateFreeText(v), nil
	default:
		return nil, fmt.Errorf("unknown AST node type %T", n)
	}
}

func translateAnd(n *AndNode, perUser *[]PerUserFilter, negated bool) (query.Query, error) {
	var children []query.Query
	for _, c := range dropStopwordOnlyConjuncts(n.Children) {
		q, err := translateNode(c, perUser, negated)
		if err != nil {
			return nil, err
		}
		if q != nil {
			children = append(children, q)
		}
	}
	if len(children) == 0 {
		return nil, nil
	}
	if len(children) == 1 {
		return children[0], nil
	}
	return bleve.NewConjunctionQuery(children...), nil
}

// freeTextAnalyzer is the analyzer the index applies to free-text fields
// (bleve_index.go sets im.DefaultAnalyzer = en.AnalyzerName). It is resolved
// from the same registry Bleve itself uses, so it cannot drift from the index's
// notion of a stopword the way a hand-maintained word list would.
//
// nil if the registry lookup ever fails; every caller treats nil as "cannot
// tell", which degrades to the old behaviour rather than to a wrong answer.
var freeTextAnalyzer = func() analysis.Analyzer {
	a, err := registry.NewCache().AnalyzerNamed(en.AnalyzerName)
	if err != nil {
		return nil
	}
	return a
}()

// isStopwordOnly reports whether a term survives analysis. The English analyzer
// strips stopwords, so "of" produces zero tokens and therefore appears in no
// document's term dictionary.
func isStopwordOnly(term string) bool {
	if freeTextAnalyzer == nil || term == "" {
		return false
	}
	return len(freeTextAnalyzer.Analyze([]byte(term))) == 0
}

// dropStopwordOnlyConjuncts removes free-text conjuncts that analyze to nothing,
// but ONLY when a real term remains.
//
// THE BUG (reported 2026-08-11, "shards of oblivion" finds nothing): the parser
// treats whitespace as AND, so a three-word query becomes a conjunction of three
// match queries. "of" is an English stopword, so the indexer never wrote it to
// any document's term dictionary — the conjunct can never be satisfied and takes
// the entire query down with it. Every multi-word title containing of/the/a/in
// was unfindable, which is a large share of book titles. Single-word searches
// worked, which is why this read as "spaces break search".
//
// The guard matters as much as the fix. A nil query from Translate becomes
// MatchAll, so unconditionally dropping stopwords would make a search for "of"
// return the entire library. Requiring a surviving non-stopword term
// means this only ever fires on the broken case; an all-stopword query keeps its
// current behaviour exactly.
//
// Conjunction only. In a disjunction a stopword child contributes no hits and
// harms nothing, so OR is deliberately left alone.
func dropStopwordOnlyConjuncts(children []Node) []Node {
	kept := make([]Node, 0, len(children))
	dropped := 0
	for _, c := range children {
		if ft, ok := c.(*FreeTextNode); ok && !ft.Quoted && !ft.Prefix && !ft.Fuzzy &&
			isStopwordOnly(ft.Value) {
			dropped++
			continue
		}
		kept = append(kept, c)
	}
	if dropped == 0 || len(kept) == 0 {
		return children
	}
	return kept
}

func translateOr(n *OrNode, perUser *[]PerUserFilter, negated bool) (query.Query, error) {
	var children []query.Query
	for _, c := range n.Children {
		q, err := translateNode(c, perUser, negated)
		if err != nil {
			return nil, err
		}
		if q != nil {
			children = append(children, q)
		}
	}
	if len(children) == 0 {
		return nil, nil
	}
	if len(children) == 1 {
		return children[0], nil
	}
	return bleve.NewDisjunctionQuery(children...), nil
}

func translateNot(n *NotNode, perUser *[]PerUserFilter, negated bool) (query.Query, error) {
	// Flip the negation flag going into the child. When the child is
	// a FieldNode on a per-user field, we record Negated on the
	// PerUserFilter so Go post-filter can invert. When the child is
	// indexable, we wrap in a BooleanQuery{MustNot}.
	inner, err := translateNode(n.Child, perUser, !negated)
	if err != nil {
		return nil, err
	}
	if inner == nil {
		return nil, nil
	}
	b := bleve.NewBooleanQuery()
	b.AddMustNot(inner)
	// Bleve BooleanQuery requires at least one must/should to return
	// any results; pair with a match-all so "NOT something" returns
	// all docs that aren't `something`.
	b.AddMust(bleve.NewMatchAllQuery())
	return b, nil
}

func translateField(n *FieldNode, perUser *[]PerUserFilter, negated bool) (query.Query, error) {
	// Per-user fields → post-filter, not Bleve.
	if _, ok := perUserFieldSet[n.Field]; ok {
		*perUser = append(*perUser, PerUserFilter{Node: n, Negated: negated})
		return nil, nil
	}

	// Range queries.
	if n.Op == "range" {
		return buildNumericRange(n.Field, n.RangeMin, n.RangeMax, true, true)
	}

	// Comparator queries (>, <, >=, <=).
	if n.Op == ">" || n.Op == "<" || n.Op == ">=" || n.Op == "<=" {
		minInclusive := n.Op == ">="
		maxInclusive := n.Op == "<="
		switch n.Op {
		case ">", ">=":
			return buildNumericRange(n.Field, n.Value, "", minInclusive, true)
		case "<", "<=":
			return buildNumericRange(n.Field, "", n.Value, true, maxInclusive)
		}
	}

	// Fuzzy / prefix / wildcard — take precedence over default match.
	if n.Fuzzy {
		fq := bleve.NewFuzzyQuery(n.Value)
		fq.SetField(n.Field)
		if n.Boost > 0 {
			fq.SetBoost(n.Boost)
		}
		return fq, nil
	}
	if n.Prefix && !n.Wildcard {
		pq := bleve.NewPrefixQuery(n.Value)
		pq.SetField(n.Field)
		if n.Boost > 0 {
			pq.SetBoost(n.Boost)
		}
		return pq, nil
	}
	if n.Wildcard {
		pattern := n.Value
		if n.Prefix {
			pattern = "*" + pattern + "*"
		} else {
			pattern = "*" + pattern
		}
		wq := bleve.NewWildcardQuery(pattern)
		wq.SetField(n.Field)
		if n.Boost > 0 {
			wq.SetBoost(n.Boost)
		}
		return wq, nil
	}

	// Default: phrase match if quoted, otherwise regular match.
	if n.Quoted {
		pq := bleve.NewMatchPhraseQuery(n.Value)
		pq.SetField(n.Field)
		if n.Boost > 0 {
			pq.SetBoost(n.Boost)
		}
		return pq, nil
	}
	mq := bleve.NewMatchQuery(n.Value)
	mq.SetField(n.Field)
	if n.Boost > 0 {
		mq.SetBoost(n.Boost)
	}
	return mq, nil
}

func translateValueAlt(n *ValueAltNode, perUser *[]PerUserFilter, negated bool) (query.Query, error) {
	// Per-user field alternation still goes to post-filter. Rare in
	// practice but handle it for correctness.
	if _, ok := perUserFieldSet[n.Field]; ok {
		// Build a synthetic set of PerUserFilter entries.
		for _, v := range n.Values {
			*perUser = append(*perUser, PerUserFilter{
				Node:    &FieldNode{Field: n.Field, Value: v},
				Negated: negated,
			})
		}
		return nil, nil
	}

	var children []query.Query
	for _, v := range n.Values {
		mq := bleve.NewMatchQuery(v)
		mq.SetField(n.Field)
		children = append(children, mq)
	}
	if len(children) == 0 {
		return nil, nil
	}
	if len(children) == 1 {
		return children[0], nil
	}
	return bleve.NewDisjunctionQuery(children...), nil
}

func translateFreeText(n *FreeTextNode) query.Query {
	if n.Prefix {
		return bleve.NewPrefixQuery(n.Value)
	}
	if n.Fuzzy {
		return bleve.NewFuzzyQuery(n.Value)
	}
	if n.Quoted {
		return bleve.NewMatchPhraseQuery(n.Value)
	}
	children := make([]query.Query, 0, len(textFieldBoosts)+1)
	for _, fb := range textFieldBoosts {
		mq := bleve.NewMatchQuery(n.Value)
		mq.SetField(fb.Field)
		mq.SetBoost(fb.Boost)
		children = append(children, mq)
	}
	// Unfielded child preserves recall: anything that matched via _all
	// before (tags, genre, isbn text) still matches, at neutral weight.
	children = append(children, bleve.NewMatchQuery(n.Value))
	return bleve.NewDisjunctionQuery(children...)
}

// buildNumericRange constructs a NumericRangeQuery for the given
// bounds. Empty-string bounds are treated as unbounded on that side.
func buildNumericRange(field, minS, maxS string, minInclusive, maxInclusive bool) (query.Query, error) {
	var minPtr, maxPtr *float64
	if minS != "" {
		v, err := strconv.ParseFloat(minS, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid numeric bound %q: %w", field, minS, err)
		}
		minPtr = &v
	}
	if maxS != "" {
		v, err := strconv.ParseFloat(maxS, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid numeric bound %q: %w", field, maxS, err)
		}
		maxPtr = &v
	}
	minInc := minInclusive
	maxInc := maxInclusive
	nrq := bleve.NewNumericRangeInclusiveQuery(minPtr, maxPtr, &minInc, &maxInc)
	nrq.SetField(field)
	return nrq, nil
}
