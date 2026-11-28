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
	// only per-user or tag terms). The caller lists with MatchAllQuery, or from
	// the store.
	MatchAll bool
	// PerUser are the per-user terms (read_status, progress_pct, last_played) the
	// caller applies to each viewer's rows after the index answers.
	PerUser []search.PerUserFilter
	// Tags are the tag: terms, resolved by the caller (HEAD's GetBooksByTag) and
	// intersected with the index answer.
	Tags []string
}

// MatchAllQuery is the index query of a listing with no search term: every
// document (index.All). It costs the engine a walk of every document, so it
// counts in a query's node budget as a Not does.
func MatchAllQuery() index.Query { return index.All() }

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

// weighted wraps leaf in index.Boost when HEAD weighted the node. HEAD's
// weights are carried on the nodes, never declared on the fields (see Fields),
// so an unweighted node stays at 1, as in HEAD. A boost of exactly 1 is the
// default weight and is not emitted.
//
// Only leaves are weighted. HEAD also calls SetBoost on the conjunction
// underscorePatternQuery builds for a multi-part pattern, but bleve never reads
// the weight of a conjunction, disjunction or boolean query (their searchers
// ignore BoostVal), so HEAD's scores carry no weight there. index.Boost on a
// group would multiply every leaf under it and rank differently from HEAD;
// convert therefore drops a group's weight, which is HEAD's effective answer.
func weighted(q index.Query, boost float64) index.Query {
	if boost == 0 {
		return q
	}
	return index.Boost(q, boost)
}

// allFields are the fields HEAD's unfielded "_all" holds: every text-analysed
// field, the "keyword" ones included (HEAD maps those with the standard analyzer
// and bleve feeds _all from every text mapping).
var allFields = append(append([]string(nil), freeTextFields...), keywordFields...)

// convertLeaf maps one non-compound query, with HEAD's weight on it.
func convertLeaf(q query.Query) (index.Query, error) {
	boost := boostOf(q)
	field := ""
	if f, ok := q.(query.FieldableQuery); ok {
		field = f.Field()
	}
	leaf, err := convertLeafUnweighted(q, field)
	if err != nil {
		return nil, err
	}
	if _, err := index.Describe(leaf); err != nil {
		// A constructor recorded a problem: a wildcard with no literal
		// character is the one a translated search can reach.
		return nil, notExpressible("%v", err)
	}
	return weighted(leaf, boost), nil
}

// convertLeafUnweighted maps one non-compound query.
//
// An unfielded analysed match is HEAD's "_all" child of the free-text fan-out
// and is index.MatchAnyField. The other unfielded leaves (a phrase, a prefix, a
// fuzzy term, a pattern part) are an Or over allFields instead of the engine's _all: HEAD
// analyses its _all with its default analyzer (book_en_nostop), while the
// engine's composite is analysed with the standard analyzer, so the engine's _all
// does not match "lucius fox" where the stemmed terms are "luciu fox" (HEAD does;
// pinned by TestUnfieldedPhraseAnswersLikeHead). Each field's own analyzer is the
// one HEAD's composite was built from.
func convertLeafUnweighted(q query.Query, field string) (index.Query, error) {
	each := func(f func(field string) index.Query) index.Query {
		if field != "" {
			return f(field)
		}
		kids := make([]index.Query, 0, len(allFields))
		for _, fl := range allFields {
			kids = append(kids, f(fl))
		}
		return or(kids)
	}
	switch v := q.(type) {
	case *query.MatchQuery:
		if v.Fuzziness != 0 {
			return nil, notExpressible("a fuzzy match")
		}
		if v.Analyzer != "" {
			return nil, notExpressible("a match with analyzer %q", v.Analyzer)
		}
		if field == "" {
			return index.MatchAnyField(v.Match), nil
		}
		return index.Match(field, v.Match), nil
	case *query.MatchPhraseQuery:
		// The engine analyses a phrase with the field's own analyzer (stop
		// words stay in place under book_en_nostop).
		return each(func(f string) index.Query {
			return index.QueryString(f + ":\"" + escapePhrase(v.MatchPhrase) + "\"")
		}), nil
	case *query.PrefixQuery:
		return each(func(f string) index.Query { return index.Prefix(f, v.Prefix) }), nil
	case *query.FuzzyQuery:
		if v.Prefix != 0 {
			return nil, notExpressible("a fuzzy query with a fixed prefix length")
		}
		// A fuzzy term is not analysed, so it is lowercased here, as HEAD does
		// (patternTerm) at its two NewFuzzyQuery call sites.
		term := escapeTerm(strings.ToLower(v.Term))
		return each(func(f string) index.Query {
			return index.QueryString(fmt.Sprintf("%s:%s~%d", f, term, v.Fuzziness))
		}), nil
	case *query.WildcardQuery:
		// A leading wildcard is allowed and bounded: past the engine's term
		// cap the search fails with index.ErrTooBroad (see Dispose).
		return each(func(f string) index.Query { return index.Wildcard(f, v.Wildcard) }), nil
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

// escapeTerm escapes a bare query-string term.
func escapeTerm(s string) string { return escapeWith(s, "+-=&|><!(){}[]^\"~*?:\\/ \t\r\n") }

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
