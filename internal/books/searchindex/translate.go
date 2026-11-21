// file: internal/books/searchindex/translate.go
// version: 0.1.0
// guid: 3b6ab51d-e24d-4f1b-a779-a79257f1b044
// last-edited: 2026-11-20

package searchindex

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/blevesearch/bleve/v2/search/query"
	"github.com/falkcorp/mediacore/storage/index"

	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// ErrNotExpressible marks a search the contract's index.Query cannot express
// with HEAD's meaning. Each refusal is typed and never silently downgraded: a
// downgraded query answers something else with no signal. The caller falls back
// to the live path, as it does while the index is rebuilding.
var ErrNotExpressible = errors.New("searchindex: query not expressible over the index contract")

func notExpressible(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrNotExpressible, fmt.Sprintf(format, args...))
}

// Translation is a parsed search split the way HEAD's search.Translation is.
type Translation struct {
	// Query is the index query for the indexed part of the search. It is nil
	// when MatchAll is set.
	Query index.Query
	// MatchAll: the indexed part constrains nothing (an empty search, or one of
	// only per-user or tag terms). The contract has no match-all operation, so
	// the caller lists books from the store, or uses MatchAllQuery.
	MatchAll bool
	// PerUser are the per-user terms (read_status, progress_pct, last_played) the
	// caller applies to each viewer's rows after the index answers.
	PerUser []search.PerUserFilter
	// Tags are the tag: terms, resolved by the caller (HEAD's GetBooksByTag) and
	// intersected with the index answer.
	Tags []string
}

// matchNoneTerm is a term no document holds: no book has a format of it.
const matchNoneTerm = "\x00no-such-term"

// MatchAllQuery is an index query every document matches: the contract's Not is
// "match all minus the operand", so negating a term nothing holds is match-all.
// The contract has no match-all operation of its own (a seam, switch at the
// fold).
func MatchAllQuery() index.Query { return index.Not(index.Term(FieldFormat, matchNoneTerm)) }

// Translate parses text with HEAD's search grammar and compiles it to an index
// query. now anchors relative dates (date_added:>-7d).
//
// It is built on search.TranslateQuery so every rule of HEAD's translation (the
// date and duration bounds, underscore patterns, stop-word handling, tag and
// per-user splitting, regex refusal) is HEAD's own, then maps the resulting
// query tree onto the contract. A search whose per-user terms sit under an OR or
// a NOT (HEAD's Mixed shape, evaluated row by row against the viewer's state) is
// refused with ErrNotExpressible. An error from the parser or from HEAD's term
// checks is returned unwrapped, so callers keep using search.IsQueryTermError.
func Translate(text string, now time.Time) (Translation, error) {
	if strings.TrimSpace(text) == "" {
		return Translation{MatchAll: true}, nil
	}
	node, err := search.ParseQuery(text)
	if err != nil {
		return Translation{}, err
	}
	tr, err := search.TranslateQuery(node, now)
	if err != nil {
		return Translation{}, err
	}
	if tr.Mixed != nil {
		return Translation{}, notExpressible("per-user terms under OR or NOT need the viewer's rows per book")
	}
	out := Translation{PerUser: tr.PerUser, Tags: tr.Tags}
	if tr.Query == nil {
		out.MatchAll = true
		return out, nil
	}
	q, matchAll, err := convert(tr.Query)
	if err != nil {
		return Translation{}, err
	}
	out.Query, out.MatchAll = q, matchAll
	return out, nil
}

// convert maps a bleve query tree, as HEAD builds it, onto index.Query.
// matchAll reports a tree that is a bare match-all.
func convert(q query.Query) (res index.Query, matchAll bool, err error) {
	switch v := q.(type) {
	case *query.MatchAllQuery:
		return nil, true, nil
	case *query.ConjunctionQuery:
		kids, all, err := convertAll(v.Conjuncts)
		if err != nil {
			return nil, false, err
		}
		if len(kids) == 0 {
			return nil, all, nil
		}
		return and(kids), false, nil
	case *query.DisjunctionQuery:
		if v.Min > 1 {
			return nil, false, notExpressible("a disjunction needing %v matches", v.Min)
		}
		kids, all, err := convertAll(v.Disjuncts)
		if err != nil {
			return nil, false, err
		}
		if all {
			// OR with a match-all is match-all.
			return nil, true, nil
		}
		return or(kids), false, nil
	case *query.BooleanQuery:
		return convertBoolean(v)
	}
	q2, err := convertLeaf(q)
	return q2, false, err
}

func convertAll(qs []query.Query) (kids []index.Query, anyAll bool, err error) {
	for _, c := range qs {
		k, all, err := convert(c)
		if err != nil {
			return nil, false, err
		}
		if all {
			anyAll = true
			continue
		}
		kids = append(kids, k)
	}
	return kids, anyAll, nil
}

func and(kids []index.Query) index.Query {
	if len(kids) == 1 {
		return kids[0]
	}
	return index.And(kids...)
}

func or(kids []index.Query) index.Query {
	if len(kids) == 1 {
		return kids[0]
	}
	return index.Or(kids...)
}

// convertBoolean handles the one shape HEAD builds: must(match-all), mustNot(x)
// for a negation, i.e. NOT x, with other must clauses as an AND. Should clauses
// and minimum-should counts are not built by HEAD and are refused.
func convertBoolean(b *query.BooleanQuery) (index.Query, bool, error) {
	if b.Should != nil {
		if sq, ok := b.Should.(*query.DisjunctionQuery); !ok || len(sq.Disjuncts) > 0 {
			return nil, false, notExpressible("a boolean query with should clauses")
		}
	}
	var must []index.Query
	if b.Must != nil {
		cq, ok := b.Must.(*query.ConjunctionQuery)
		if !ok {
			return nil, false, notExpressible("a boolean query whose must is a %T", b.Must)
		}
		kids, _, err := convertAll(cq.Conjuncts)
		if err != nil {
			return nil, false, err
		}
		must = kids
	}
	var mustNot []index.Query
	if b.MustNot != nil {
		dq, ok := b.MustNot.(*query.DisjunctionQuery)
		if !ok {
			return nil, false, notExpressible("a boolean query whose must-not is a %T", b.MustNot)
		}
		for _, c := range dq.Disjuncts {
			k, all, err := convert(c)
			if err != nil {
				return nil, false, err
			}
			if all {
				return nil, false, notExpressible("a negated match-all")
			}
			mustNot = append(mustNot, k)
		}
	}
	if len(mustNot) > 0 {
		must = append(must, index.Not(or(mustNot)))
	}
	if len(must) == 0 {
		return nil, true, nil
	}
	return and(must), false, nil
}

func boostOf(q query.Query) float64 {
	if b, ok := q.(query.BoostableQuery); ok {
		if v := b.Boost(); v > 0 && v != 1 {
			return v
		}
	}
	return 0
}

// convertLeaf maps one non-compound query. An unfielded term (HEAD's "_all")
// fans out as an OR over the text fields, since the contract has no _all field.
func convertLeaf(q query.Query) (index.Query, error) {
	boost := boostOf(q)
	field := ""
	if f, ok := q.(query.FieldableQuery); ok {
		field = f.Field()
	}
	fields := []string{field}
	if field == "" {
		fields = textFields
	}
	each := func(f func(field string) (index.Query, error)) (index.Query, error) {
		kids := make([]index.Query, 0, len(fields))
		for _, fl := range fields {
			k, err := f(fl)
			if err != nil {
				return nil, err
			}
			kids = append(kids, k)
		}
		return or(kids), nil
	}

	switch v := q.(type) {
	case *query.MatchQuery:
		if v.Fuzziness != 0 {
			return nil, notExpressible("a fuzzy match")
		}
		if v.Analyzer != "" {
			return nil, notExpressible("a match with analyzer %q", v.Analyzer)
		}
		if boost == 0 {
			return each(func(f string) (index.Query, error) { return index.Match(f, v.Match), nil })
		}
		return each(func(f string) (index.Query, error) { return termsString(f, v.Match, "", boost), nil })
	case *query.MatchPhraseQuery:
		return each(func(f string) (index.Query, error) {
			return index.QueryString(fieldClause(f, `"`+escapePhrase(v.MatchPhrase)+`"`, boost)), nil
		})
	case *query.PrefixQuery:
		if boost == 0 {
			return each(func(f string) (index.Query, error) { return index.Prefix(f, v.Prefix), nil })
		}
		return each(func(f string) (index.Query, error) {
			return index.QueryString(fieldClause(f, escapeTerm(v.Prefix)+"*", boost)), nil
		})
	case *query.FuzzyQuery:
		if v.Prefix != 0 {
			return nil, notExpressible("a fuzzy query with a fixed prefix length")
		}
		return each(func(f string) (index.Query, error) {
			return index.QueryString(fieldClause(f, fmt.Sprintf("%s~%d", escapeTerm(v.Term), v.Fuzziness), boost)), nil
		})
	case *query.WildcardQuery:
		if strings.HasPrefix(v.Wildcard, "*") || strings.HasPrefix(v.Wildcard, "?") {
			return nil, notExpressible("a leading wildcard %q (the engine refuses a scan of the whole term dictionary)", v.Wildcard)
		}
		return each(func(f string) (index.Query, error) {
			return index.QueryString(fieldClause(f, escapeWildcard(v.Wildcard), boost)), nil
		})
	case *query.NumericRangeQuery:
		return numericRange(v)
	case *query.DateRangeQuery:
		return dateRange(v)
	}
	return nil, notExpressible("a %T query", q)
}

func numericRange(v *query.NumericRangeQuery) (index.Query, error) {
	lo, hi := v.Min, v.Max
	// bleve's defaults: the minimum is inclusive, the maximum exclusive.
	incLo, incHi := true, false
	if v.InclusiveMin != nil {
		incLo = *v.InclusiveMin
	}
	if v.InclusiveMax != nil {
		incHi = *v.InclusiveMax
	}
	if lo != nil && !incLo {
		n := math.Nextafter(*lo, math.Inf(1))
		lo = &n
	}
	if hi != nil && !incHi {
		n := math.Nextafter(*hi, math.Inf(-1))
		hi = &n
	}
	if lo == nil && hi == nil {
		return nil, notExpressible("an unbounded numeric range on %q", v.Field())
	}
	return index.Range(v.Field(), lo, hi), nil
}

func dateRange(v *query.DateRangeQuery) (index.Query, error) {
	var lo, hi *time.Time
	if !v.Start.IsZero() {
		t := v.Start.Time
		if v.InclusiveStart != nil && !*v.InclusiveStart {
			t = t.Add(time.Nanosecond)
		}
		lo = &t
	}
	if !v.End.IsZero() {
		t := v.End.Time
		// bleve's default end is exclusive; the contract's is inclusive.
		if v.InclusiveEnd == nil || !*v.InclusiveEnd {
			t = t.Add(-time.Nanosecond)
		}
		hi = &t
	}
	if lo == nil && hi == nil {
		return nil, notExpressible("an unbounded date range on %q", v.Field())
	}
	return index.Range(v.Field(), lo, hi), nil
}

// termsString renders text as an OR of its boosted terms on field: the
// boosted form of an analysed match, which index.Match cannot carry (per-field
// boosts are a seam, switch at the fold).
func termsString(field, text, _ string, boost float64) index.Query {
	toks := strings.Fields(text)
	parts := make([]string, 0, len(toks))
	for _, t := range toks {
		parts = append(parts, fieldClause(field, escapeTerm(t), boost))
	}
	if len(parts) == 0 {
		return index.Match(field, text)
	}
	return index.QueryString(strings.Join(parts, " "))
}

func fieldClause(field, body string, boost float64) string {
	s := field + ":" + body
	if boost > 0 {
		s += fmt.Sprintf("^%g", boost)
	}
	return s
}

// escapeTerm escapes a bare query-string term.
func escapeTerm(s string) string { return escapeWith(s, "+-=&|><!(){}[]^\"~*?:\\/ \t\r\n") }

// escapeWildcard escapes a pattern but keeps its own * and ?.
func escapeWildcard(s string) string { return escapeWith(s, "+-=&|><!(){}[]^\"~:\\/ \t\r\n") }

// escapePhrase escapes the inside of a quoted phrase.
func escapePhrase(s string) string { return escapeWith(s, "\"\\") }

func escapeWith(s, special string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(special, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
