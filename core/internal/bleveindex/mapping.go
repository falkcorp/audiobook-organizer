// file: core/internal/bleveindex/mapping.go
// version: 1.0.3
// guid: 4a2f8d21-9b4c-4b9f-8c64-7e1d3a6c8b22
// last-edited: 2026-11-11

package bleveindex

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/keyword"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/standard"
	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/search/query"

	"github.com/falkcorp/mediacore/storage/index"
)

// buildMapping turns a Spec's fields into a bleve mapping. The mapping is
// closed (Dynamic false): a document field the Spec did not declare is not
// indexed, so the on-disk index is exactly what MappingVersion names.
func buildMapping(s index.Spec) (mapping.IndexMapping, error) {
	im := bleve.NewIndexMapping()
	doc := bleve.NewDocumentMapping()
	doc.Dynamic = false
	for _, f := range s.Fields {
		var fm *mapping.FieldMapping
		switch f.Kind {
		case index.Text:
			fm = bleve.NewTextFieldMapping()
			a, err := analyzerName(f.Analyzer, standard.Name)
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", f.Name, err)
			}
			fm.Analyzer = a
		case index.Keyword:
			fm = bleve.NewTextFieldMapping()
			a, err := analyzerName(f.Analyzer, keyword.Name)
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", f.Name, err)
			}
			fm.Analyzer = a
		case index.Number:
			fm = bleve.NewNumericFieldMapping()
		case index.Date:
			fm = bleve.NewDateTimeFieldMapping()
		case index.Bool:
			fm = bleve.NewBooleanFieldMapping()
		default:
			return nil, fmt.Errorf("field %q: kind %s", f.Name, f.Kind)
		}
		fm.Store = f.Store
		fm.DocValues = f.Facet
		doc.AddFieldMappingsAt(f.Name, fm)
	}
	im.DefaultMapping = doc
	return im, nil
}

// mappingStamp is the content of the mapping marker: the Spec's MappingVersion
// and a fingerprint of its field declarations, so a Fields change that forgot
// the version bump still recreates the index instead of serving documents under
// a mapping the Spec no longer describes. The fingerprint hashes the
// declaration (name, kind, analyzer, store, facet, in order), not bleve's
// marshalled mapping, so a bleve upgrade does not by itself force a
// library-scale rebuild.
func mappingStamp(s index.Spec) string {
	var decl []byte
	for _, f := range s.Fields {
		decl = fmt.Appendf(decl, "%q %d %q %t %t\n", f.Name, f.Kind, f.Analyzer, f.Store, f.Facet)
	}
	sum := sha256.Sum256(decl)
	return s.MappingVersion + "\n" + hex.EncodeToString(sum[:])
}

// analyzerName resolves a Field.Analyzer: "" is the kind's default, and
// "standard" and "keyword" are bleve's stock analyzers. A module-registered
// analyzer needs a registration surface the contract does not have yet, so any
// other name is refused rather than silently indexed with the default.
func analyzerName(name, def string) (string, error) {
	switch name {
	case "":
		return def, nil
	case "standard":
		return standard.Name, nil
	case "keyword":
		return keyword.Name, nil
	}
	return "", fmt.Errorf("%w: analyzer %q is not registered", index.ErrInvalidSpec, name)
}

// translate turns an engine-neutral query into a bleve query over this index's
// fields. A field the Spec does not declare is ErrInvalidQuery, never a silent
// match-nothing: a typo in a field name is a bug, not an empty result. The tree
// is bounded first (nodes, depth, text size), so a hostile query cannot cost
// more than a fixed amount before it is refused. The query strings inside the
// tree are parsed during the translation, and what each costs is added to the
// same node budget checkShape started (nodes), not to a fresh one per string: a
// tree of a thousand OpString leaves must not get a thousand budgets.
func translate(kinds map[string]index.FieldKind, n index.Node) (query.Query, error) {
	nodes := 0
	if err := checkShape(n, 1, &nodes); err != nil {
		return nil, err
	}
	return translateNode(kinds, n, 1, &nodes)
}

// notCost is what a Not node counts in the node budget: it is translated to
// "match all, except", so each one scans every document (1.6s over 20k documents
// for an Or of 510 of them), where a Term or a Match costs a dictionary lookup.
const notCost = 64

func nodeCost(op index.Op) int {
	if op == index.OpNot {
		return notCost
	}
	return 1
}

// checkShape bounds a query tree: at most maxQueryNodes nodes, maxQueryDepth
// levels, and maxQueryStringBytes of text in any one node. It is iterative in
// its depth only up to the cap, so a deeply nested query is refused before the
// translation recurses.
func checkShape(n index.Node, depth int, nodes *int) error {
	if depth > maxQueryDepth {
		return fmt.Errorf("%w: query nests deeper than %d levels", index.ErrInvalidQuery, maxQueryDepth)
	}
	*nodes += nodeCost(n.Op)
	if *nodes > maxQueryNodes {
		return fmt.Errorf("%w: query is over its budget of %d nodes (a Not counts as %d: it matches every document first)", index.ErrInvalidQuery, maxQueryNodes, notCost)
	}
	if len(n.Text) > maxQueryStringBytes {
		return fmt.Errorf("%w: query text is %d bytes, over %d", index.ErrInvalidQuery, len(n.Text), maxQueryStringBytes)
	}
	for _, c := range n.Children {
		if err := checkShape(c, depth+1, nodes); err != nil {
			return err
		}
	}
	return nil
}

// translateNode translates n, which sits at depth in the tree. nodes is the
// budget shared by the whole query: it holds what checkShape counted and grows
// by what each parsed query string costs.
func translateNode(kinds map[string]index.FieldKind, n index.Node, depth int, nodes *int) (query.Query, error) {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", index.ErrInvalidQuery, fmt.Sprintf(format, a...))
	}
	field := func() (index.FieldKind, error) {
		k, ok := kinds[n.Field]
		if !ok {
			return 0, bad("unknown field %q", n.Field)
		}
		return k, nil
	}
	switch n.Op {
	case index.OpMatch:
		k, err := field()
		if err != nil {
			return nil, err
		}
		if k != index.Text && k != index.Keyword {
			return nil, bad("match on %s field %q", k, n.Field)
		}
		q := bleve.NewMatchQuery(n.Text)
		q.SetField(n.Field)
		return q, nil
	case index.OpTerm:
		k, err := field()
		if err != nil {
			return nil, err
		}
		switch k {
		case index.Text, index.Keyword:
			q := bleve.NewTermQuery(n.Text)
			q.SetField(n.Field)
			return q, nil
		case index.Bool:
			v, err := strconv.ParseBool(n.Text)
			if err != nil {
				return nil, bad("term %q on bool field %q", n.Text, n.Field)
			}
			q := bleve.NewBoolFieldQuery(v)
			q.SetField(n.Field)
			return q, nil
		case index.Number:
			v, err := strconv.ParseFloat(n.Text, 64)
			if err != nil {
				return nil, bad("term %q on number field %q", n.Text, n.Field)
			}
			incl := true
			q := bleve.NewNumericRangeInclusiveQuery(&v, &v, &incl, &incl)
			q.SetField(n.Field)
			return q, nil
		}
		return nil, bad("term on %s field %q", k, n.Field)
	case index.OpPrefix:
		k, err := field()
		if err != nil {
			return nil, err
		}
		if k != index.Text && k != index.Keyword {
			return nil, bad("prefix on %s field %q", k, n.Field)
		}
		q := bleve.NewPrefixQuery(n.Text)
		q.SetField(n.Field)
		return q, nil
	case index.OpRange:
		k, err := field()
		if err != nil {
			return nil, err
		}
		return translateRange(k, n, bad)
	case index.OpAnd, index.OpOr:
		qs := make([]query.Query, 0, len(n.Children))
		for _, c := range n.Children {
			q, err := translateNode(kinds, c, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			qs = append(qs, q)
		}
		if n.Op == index.OpAnd {
			return bleve.NewConjunctionQuery(qs...), nil
		}
		return bleve.NewDisjunctionQuery(qs...), nil
	case index.OpNot:
		if len(n.Children) != 1 {
			return nil, bad("not takes one query")
		}
		c, err := translateNode(kinds, n.Children[0], depth+1, nodes)
		if err != nil {
			return nil, err
		}
		// bleve's boolean query needs a positive clause: "everything except c".
		q := bleve.NewBooleanQuery()
		q.AddMust(bleve.NewMatchAllQuery())
		q.AddMustNot(c)
		return q, nil
	case index.OpString:
		return translateString(n.Text, depth, nodes)
	}
	return nil, bad("operator %d", n.Op)
}

// translateRange builds a numeric or date range. An open end is a nil number
// pointer or a zero time. An int64 bound is held as a float64 (bleve's numeric
// fields are), exact up to 2^53.
func translateRange(k index.FieldKind, n index.Node, bad func(string, ...any) error) (query.Query, error) {
	incl := true
	switch k {
	case index.Number:
		lo, err := num(n.Min)
		if err != nil {
			return nil, bad("range on number field %q: %v", n.Field, err)
		}
		hi, err := num(n.Max)
		if err != nil {
			return nil, bad("range on number field %q: %v", n.Field, err)
		}
		q := bleve.NewNumericRangeInclusiveQuery(lo, hi, &incl, &incl)
		q.SetField(n.Field)
		return q, nil
	case index.Date:
		lo, err := when(n.Min)
		if err != nil {
			return nil, bad("range on date field %q: %v", n.Field, err)
		}
		hi, err := when(n.Max)
		if err != nil {
			return nil, bad("range on date field %q: %v", n.Field, err)
		}
		q := bleve.NewDateRangeInclusiveQuery(lo, hi, &incl, &incl)
		q.SetField(n.Field)
		return q, nil
	}
	return nil, bad("range on %s field %q", k, n.Field)
}

func num(v any) (*float64, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case int64:
		f := float64(x)
		return &f, nil
	case float64:
		return &x, nil
	}
	return nil, fmt.Errorf("bound %T is not a number", v)
}

func when(v any) (time.Time, error) {
	switch x := v.(type) {
	case nil:
		return time.Time{}, nil
	case time.Time:
		return x, nil
	}
	return time.Time{}, fmt.Errorf("bound %T is not a time", v)
}

// translateString parses a user query string now, not at search time, and
// refuses the shapes whose cost the term-searcher cap alone cannot make cheap
// to refuse: regular expressions, and a wildcard that starts with * or ? (it
// has no literal prefix to narrow the term dictionary scan, so it expands to
// every term of the field). A trailing wildcard (cap*) is allowed and counted
// against the term-searcher cap like a Prefix. What the parsed query costs is
// added to nodes, the budget of the whole query (a string at depth levels deep
// starts its own depth count there), so the strings of one query share one cap.
func translateString(s string, depth int, nodes *int) (query.Query, error) {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", index.ErrInvalidQuery, fmt.Sprintf(format, a...))
	}
	if len(s) > maxQueryStringBytes {
		return nil, bad("query string is %d bytes, over %d", len(s), maxQueryStringBytes)
	}
	parsed, err := query.NewQueryStringQuery(s).Parse()
	if err != nil {
		return nil, bad("query string: %v", err)
	}
	if err := checkParsed(parsed, depth+1, nodes); err != nil {
		return nil, err
	}
	return parsed, nil
}

func checkParsed(q query.Query, depth int, nodes *int) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", index.ErrInvalidQuery, fmt.Sprintf(format, a...))
	}
	if depth > maxQueryDepth {
		return bad("query string nests deeper than %d levels", maxQueryDepth)
	}
	*nodes++
	if b, ok := q.(*query.BooleanQuery); ok && b != nil && b.MustNot != nil {
		*nodes += notCost - 1 // a must-not clause is a match-all, like Not
	}
	if *nodes > maxQueryNodes {
		return bad("query is over its budget of %d nodes (a negation in a query string counts as %d)", maxQueryNodes, notCost)
	}
	switch v := q.(type) {
	case *query.RegexpQuery:
		return bad("regular expressions are not allowed in a query string")
	case *query.WildcardQuery:
		if v != nil && (strings.HasPrefix(v.Wildcard, "*") || strings.HasPrefix(v.Wildcard, "?")) {
			return bad("a wildcard may not start with * or ?")
		}
	case *query.ConjunctionQuery:
		if v == nil { // a typed nil inside BooleanQuery's interface fields
			return nil
		}
		for _, c := range v.Conjuncts {
			if err := checkParsed(c, depth+1, nodes); err != nil {
				return err
			}
		}
	case *query.DisjunctionQuery:
		if v == nil {
			return nil
		}
		for _, c := range v.Disjuncts {
			if err := checkParsed(c, depth+1, nodes); err != nil {
				return err
			}
		}
	case *query.BooleanQuery:
		if v == nil {
			return nil
		}
		for _, c := range []query.Query{v.Must, v.Should, v.MustNot} {
			if c == nil {
				continue
			}
			if err := checkParsed(c, depth+1, nodes); err != nil {
				return err
			}
		}
	}
	return nil
}
