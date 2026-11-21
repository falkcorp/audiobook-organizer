// file: core/internal/bleveindex/mapping.go
// version: 1.0.3
// guid: 4a2f8d21-9b4c-4b9f-8c64-7e1d3a6c8b22
// last-edited: 2026-11-11

package bleveindex

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/custom"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/keyword"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/standard"
	regexpchar "github.com/blevesearch/bleve/v2/analysis/char/regexp"
	"github.com/blevesearch/bleve/v2/analysis/lang/en"
	"github.com/blevesearch/bleve/v2/analysis/token/lowercase"
	"github.com/blevesearch/bleve/v2/analysis/token/porter"
	"github.com/blevesearch/bleve/v2/analysis/tokenizer/single"
	"github.com/blevesearch/bleve/v2/analysis/tokenizer/unicode"
	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/search/query"

	"github.com/falkcorp/mediacore/storage/index"
)

// buildMapping turns a Spec's fields into a bleve mapping. The mapping is
// closed (Dynamic false): a document field the Spec did not declare is not
// indexed, so the on-disk index is exactly what MappingVersion names. The
// Spec's own analyzers, and the built-in keyword_lowercase when a field uses
// it, are registered in the mapping (bleve persists them inside the index, so
// a reopen analyses with the definition the index was built with).
func buildMapping(s index.Spec) (mapping.IndexMapping, error) {
	im := bleve.NewIndexMapping()
	if err := registerAnalyzers(im, s); err != nil {
		return nil, err
	}
	doc := bleve.NewDocumentMapping()
	doc.Dynamic = false
	for _, f := range s.Fields {
		var fm *mapping.FieldMapping
		switch f.Kind {
		case index.Text:
			fm = bleve.NewTextFieldMapping()
			a, err := analyzerName(s, f.Analyzer, standard.Name)
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", f.Name, err)
			}
			fm.Analyzer = a
		case index.Keyword:
			fm = bleve.NewTextFieldMapping()
			a, err := analyzerName(s, f.Analyzer, keyword.Name)
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

// The bleve names of what registerAnalyzers adds to a mapping. A Spec's
// analyzer is registered under specAnalyzerPrefix + its name, so no module
// name can collide with an analyzer bleve registers globally ("en",
// "standard", ...), and the built-in keyword_lowercase gets a name of its own
// for the same reason.
const (
	specAnalyzerPrefix         = "core_spec_"
	keywordLowercaseBleveName  = "core_keyword_lowercase"
	underscoreToSpaceBleveName = "core_underscore_to_space"
)

// registerAnalyzers adds the Spec's analyzers, and the built-in
// keyword_lowercase when a field names it, to im. A component this engine
// cannot build is an ErrInvalidSpec, never a silent default.
func registerAnalyzers(im *mapping.IndexMappingImpl, s index.Spec) error {
	needUnderscore := false
	for _, a := range s.Analyzers {
		if slices.Contains(a.CharFilters, index.CharUnderscoreToSpace) {
			needUnderscore = true
		}
	}
	if needUnderscore {
		if err := im.AddCustomCharFilter(underscoreToSpaceBleveName, map[string]any{
			"type": regexpchar.Name, "regexp": "_", "replace": " ",
		}); err != nil {
			return fmt.Errorf("%w: char filter %s: %v", index.ErrInvalidSpec, index.CharUnderscoreToSpace, err)
		}
	}
	if slices.ContainsFunc(s.Fields, func(f index.Field) bool { return f.Analyzer == index.AnalyzerKeywordLowercase }) {
		if err := im.AddCustomAnalyzer(keywordLowercaseBleveName, map[string]any{
			"type": custom.Name, "tokenizer": single.Name, "token_filters": []string{lowercase.Name},
		}); err != nil {
			return fmt.Errorf("%w: analyzer %s: %v", index.ErrInvalidSpec, index.AnalyzerKeywordLowercase, err)
		}
	}
	for _, a := range s.Analyzers {
		def, err := bleveAnalyzer(a)
		if err != nil {
			return err
		}
		if err := im.AddCustomAnalyzer(specAnalyzerPrefix+a.Name, def); err != nil {
			return fmt.Errorf("%w: analyzer %q: %v", index.ErrInvalidSpec, a.Name, err)
		}
	}
	return nil
}

// bleveAnalyzer translates one of core's analyzer declarations into bleve's
// custom-analyzer config.
func bleveAnalyzer(a index.Analyzer) (map[string]any, error) {
	bad := func(what string, v fmt.Stringer) error {
		return fmt.Errorf("%w: analyzer %q: %s %s", index.ErrInvalidSpec, a.Name, what, v)
	}
	var chars []string
	for _, c := range a.CharFilters {
		switch c {
		case index.CharUnderscoreToSpace:
			chars = append(chars, underscoreToSpaceBleveName)
		default:
			return nil, bad("char filter", c)
		}
	}
	var tok string
	switch a.Tokenizer {
	case index.TokenizeUnicodeWords:
		tok = unicode.Name
	case index.TokenizeWholeValue:
		tok = single.Name
	default:
		return nil, bad("tokenizer", a.Tokenizer)
	}
	var filters []string
	for _, f := range a.TokenFilters {
		switch f {
		case index.FilterLowercase:
			filters = append(filters, lowercase.Name)
		case index.FilterEnglishPossessive:
			filters = append(filters, en.PossessiveName)
		case index.FilterPorterStem:
			filters = append(filters, porter.Name)
		default:
			return nil, bad("token filter", f)
		}
	}
	def := map[string]any{"type": custom.Name, "tokenizer": tok}
	if len(chars) > 0 {
		def["char_filters"] = chars
	}
	if len(filters) > 0 {
		def["token_filters"] = filters
	}
	return def, nil
}

// mappingStamp is the content of the mapping marker: the Spec's MappingVersion
// and a fingerprint of its field declarations, so a Fields change that forgot
// the version bump still recreates the index instead of serving documents under
// a mapping the Spec no longer describes. The fingerprint hashes the
// declaration (name, kind, analyzer, store, facet, in order, then each of the
// Spec's analyzers, in order), not bleve's marshalled mapping, so a bleve
// upgrade does not by itself force a library-scale rebuild. Field.Boost is
// left out on purpose: it is a query-time weight and changes no indexed term.
// A Spec that declares no analyzer hashes exactly what it did before
// analyzers existed, so adding the feature recreated no index.
func mappingStamp(s index.Spec) string {
	var decl []byte
	for _, f := range s.Fields {
		decl = fmt.Appendf(decl, "%q %d %q %t %t\n", f.Name, f.Kind, f.Analyzer, f.Store, f.Facet)
	}
	for _, a := range s.Analyzers {
		decl = fmt.Appendf(decl, "analyzer %q %v %d %v\n", a.Name, a.CharFilters, a.Tokenizer, a.TokenFilters)
	}
	sum := sha256.Sum256(decl)
	return s.MappingVersion + "\n" + hex.EncodeToString(sum[:])
}

// analyzerName resolves a Field.Analyzer to the bleve name it is registered
// under: "" is the kind's default, the three built-ins are bleve's standard
// and keyword analyzers and registerAnalyzers' keyword_lowercase, and any
// other name must be one of the Spec's analyzers (Validate refuses the rest;
// this refuses them again, rather than silently indexing with the default).
func analyzerName(s index.Spec, name, def string) (string, error) {
	switch name {
	case "":
		return def, nil
	case index.AnalyzerStandard:
		return standard.Name, nil
	case index.AnalyzerKeyword:
		return keyword.Name, nil
	case index.AnalyzerKeywordLowercase:
		return keywordLowercaseBleveName, nil
	}
	if slices.ContainsFunc(s.Analyzers, func(a index.Analyzer) bool { return a.Name == name }) {
		return specAnalyzerPrefix + name, nil
	}
	return "", fmt.Errorf("%w: analyzer %q is not registered", index.ErrInvalidSpec, name)
}

// fieldInfo is what the translation needs to know about one declared field.
type fieldInfo struct {
	kind  index.FieldKind
	fold  bool    // its analyzer lowercases: Term, Prefix and Wildcard text is lowercased
	boost float64 // Field.Boost; 0 = none
}

// fieldInfos indexes s's fields for translate.
func fieldInfos(s index.Spec) map[string]fieldInfo {
	out := make(map[string]fieldInfo, len(s.Fields))
	for _, f := range s.Fields {
		out[f.Name] = fieldInfo{kind: f.Kind, fold: foldsCase(s, f), boost: f.Boost}
	}
	return out
}

// foldsCase reports whether f's analyzer lowercases its tokens: standard and
// keyword_lowercase do, keyword does not, and a Spec analyzer does when it has
// FilterLowercase.
func foldsCase(s index.Spec, f index.Field) bool {
	if f.Kind != index.Text && f.Kind != index.Keyword {
		return false
	}
	switch f.Analyzer {
	case "":
		return f.Kind == index.Text // standard for Text, keyword for Keyword
	case index.AnalyzerStandard, index.AnalyzerKeywordLowercase:
		return true
	case index.AnalyzerKeyword:
		return false
	}
	for _, a := range s.Analyzers {
		if a.Name == f.Analyzer {
			return a.Lowercases()
		}
	}
	return false
}

// foldText lowercases a Term, Prefix or Wildcard text for a field whose
// analyzer lowercases; those queries match index terms without analysis, so
// an uppercase letter would make them match nothing. strings.ToLower and
// bleve's lowercase filter agree on everything but a final Greek sigma.
func foldText(fi fieldInfo, text string) string {
	if fi.fold {
		return strings.ToLower(text)
	}
	return text
}

// translate turns an engine-neutral query into a bleve query over this index's
// fields. A field the Spec does not declare is ErrInvalidQuery, never a silent
// match-nothing: a typo in a field name is a bug, not an empty result. The tree
// is bounded first (nodes, depth, text size), so a hostile query cannot cost
// more than a fixed amount before it is refused. The query strings inside the
// tree are parsed during the translation, and what each costs is added to the
// same node budget checkShape started (nodes), not to a fresh one per string: a
// tree of a thousand OpString leaves must not get a thousand budgets.
func translate(fields map[string]fieldInfo, n index.Node) (query.Query, error) {
	nodes := 0
	if err := checkShape(n, 1, &nodes); err != nil {
		return nil, err
	}
	return translateNode(fields, n, 1, &nodes)
}

// translateNode translates n and applies its own Boost (index.Boost), which
// on a Match replaces the field's declared weight (translateOp applies that
// one only to an unboosted Match).
func translateNode(fields map[string]fieldInfo, n index.Node, depth int, nodes *int) (query.Query, error) {
	if n.Boost != 0 && (math.IsNaN(n.Boost) || math.IsInf(n.Boost, 0) || n.Boost < 0) { // a Node built by hand
		return nil, fmt.Errorf("%w: boost %v must be a finite number above 0", index.ErrInvalidQuery, n.Boost)
	}
	q, err := translateOp(fields, n, depth, nodes)
	if err != nil || n.Boost == 0 {
		return q, err
	}
	b, ok := q.(query.BoostableQuery)
	if !ok {
		return nil, fmt.Errorf("%w: operator %d takes no boost", index.ErrInvalidQuery, n.Op)
	}
	b.SetBoost(n.Boost)
	return q, nil
}

// notCost is what a Not node, and an All node, counts in the node budget: a
// Not is translated to "match all, except", so each one scans every document (1.6s over 20k documents
// for an Or of 510 of them), where a Term or a Match costs a dictionary lookup.
const notCost = 64

// wildcardCost is what a Wildcard node counts in the node budget: a pattern
// that starts with * or ? walks the field's whole term dictionary through its
// automaton before it searches anything, where a Term costs one lookup. At 32
// a query holds at most 31 Wildcards under one group node (a free-text
// pattern fanned out over seven fields, four times over, fits). What each one then expands to is
// bounded separately, by the term-searcher cap (ErrTooBroad).
const wildcardCost = 32

// allField is bleve's composite field, the target of index.MatchAnyField.
const allField = "_all"

func nodeCost(op index.Op) int {
	switch op {
	case index.OpNot, index.OpAll:
		return notCost
	case index.OpWildcard:
		return wildcardCost
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
		return fmt.Errorf("%w: query is over its budget of %d nodes (a Not counts as %d: it matches every document first; a Wildcard counts as %d: it walks the field's terms first)", index.ErrInvalidQuery, maxQueryNodes, notCost, wildcardCost)
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

// translateOp translates n, which sits at depth in the tree, without its own
// Boost. nodes is the budget shared by the whole query: it holds what
// checkShape counted and grows by what each parsed query string costs.
func translateOp(fields map[string]fieldInfo, n index.Node, depth int, nodes *int) (query.Query, error) {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", index.ErrInvalidQuery, fmt.Sprintf(format, a...))
	}
	var fi fieldInfo
	field := func() (index.FieldKind, error) {
		f, ok := fields[n.Field]
		if !ok {
			return 0, bad("unknown field %q", n.Field)
		}
		fi = f
		return f.kind, nil
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
		if fi.boost > 0 && n.Boost == 0 { // an explicit index.Boost replaces it (translateNode)
			q.SetBoost(fi.boost)
		}
		return q, nil
	case index.OpTerm:
		k, err := field()
		if err != nil {
			return nil, err
		}
		switch k {
		case index.Text, index.Keyword:
			q := bleve.NewTermQuery(foldText(fi, n.Text))
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
		q := bleve.NewPrefixQuery(foldText(fi, n.Text))
		q.SetField(n.Field)
		return q, nil
	case index.OpWildcard:
		k, err := field()
		if err != nil {
			return nil, err
		}
		if k != index.Text && k != index.Keyword {
			return nil, bad("wildcard on %s field %q", k, n.Field)
		}
		if strings.Trim(n.Text, "*?") == "" { // a Node built by hand, not by index.Wildcard
			return nil, bad("wildcard %q has no literal character", n.Text)
		}
		q := bleve.NewWildcardQuery(foldText(fi, n.Text))
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
			q, err := translateNode(fields, c, depth+1, nodes)
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
		c, err := translateNode(fields, n.Children[0], depth+1, nodes)
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
	case index.OpAll:
		return bleve.NewMatchAllQuery(), nil
	case index.OpMatchAny:
		// bleve's composite field: every field mapped with IncludeInAll (a
		// text field mapping's default) adds its analysed terms to it, and a
		// match on it is analysed with the mapping's default analyzer
		// (standard).
		q := bleve.NewMatchQuery(n.Text)
		q.SetField(allField)
		return q, nil
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
