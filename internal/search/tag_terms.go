// file: internal/search/tag_terms.go
// version: 1.0.1
// guid: 0e6b3a91-52c8-4d7f-9a14-b8f2d5c7e613
// last-edited: 2026-10-26

package search

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/search/query"
)

// tag: in a query is an EXACT tag filter, applied outside the index.
//
// WHY NOT THE INDEX: the "tags" field is analysed as words (the standard analyzer,
// lower-cased, English stop words removed), so an index term on it matches by word:
// tag:"sci-fi" also found "hard sci-fi", tag:"to-read" ("to" is a stop word) found
// every book tagged "read", and -tag:"it" ("it" is a stop word, so the term is
// empty) returned the whole library. The tags[] parameter, GetBooksByTag and the tag
// chips are exact (whole tag, case folded), and a chip that turns into a different
// match the moment the box gains an AND or a parenthesis is a wrong list with no
// message. So a tag: term never reaches the index: SplitTagTerms pulls it out of the
// query and the caller ANDs the rest with the books GetBooksByTag returns for each
// tag (ResolveTagIDs).
//
// That is possible only where the term is a plain conjunct. Inside an OR group, an
// alternation or under NOT the term's meaning depends on the rest of the boolean
// tree and cannot be lifted out, so those shapes are refused (TagTermError) rather
// than answered as a word match. Making them work needs the tags field mapped as a
// keyword and the index rebuilt; it is tracked in todo.d.

// TagField is the query field name of an exact tag filter.
const TagField = "tag"

// TagTermError is returned for a tag: term the exact tag filter cannot apply as
// written. Reason is shown to the person who typed the query, so it says what to
// write instead.
type TagTermError struct {
	Reason string
}

func (e *TagTermError) Error() string { return e.Reason }

const tagShapeReason = "tag: cannot be used inside an OR group or an alternation, or under NOT or -, yet: " +
	"a tag filter is exact, so it has to apply to the whole search. " +
	"Write it at the top level, ANDed with the rest (read_status:finished tag:\"noir\"), or pick the tag chip"

const tagValueReason = "tag: takes one whole tag name, with no *, ~, ^ or comparison: " +
	"it matches that tag exactly. Put the name in quotes if it contains one of those characters"

// tagModifierText matches the text the parser leaves behind when a quoted tag is
// followed by a boost or a fuzzy marker: tag:"noir"^2 parses as the tag term noir
// ANDed with the free text ^2, and tag:"noir"~ with the free text ~.
var tagModifierText = regexp.MustCompile(`^[\^~]\d*(\.\d+)?$`)

// isTagModifier reports whether n is the leftover of a boost or fuzzy marker
// written straight after a tag: term.
func isTagModifier(n Node) bool {
	ft, ok := n.(*FreeTextNode)
	return ok && !ft.Quoted && tagModifierText.MatchString(ft.String())
}

// unquotedTagWithOperatorChars reports a tag: value that was not quoted and still
// holds *, ~ or ^: the parser only recognises these at the ends of a value
// (no*ir and noir~1 come through as part of the value), so the check is on the text.
func unquotedTagWithOperatorChars(v *FieldNode) bool {
	return !v.Quoted && strings.ContainsAny(v.Value, "*~^")
}

// IsTagTermError reports whether err is a refusal of a tag: term. Its text is safe
// to show to the person who owns the query.
func IsTagTermError(err error) bool {
	var te *TagTermError
	return errors.As(err, &te)
}

// IsQueryTermError reports whether err says that a query's per-user or tag: terms
// are the problem and why (IsPerUserQueryError, IsTagTermError). Handlers that
// answer a refused query with a 400 use this, so a refusal added here reaches every
// one of them.
func IsQueryTermError(err error) bool {
	return IsPerUserQueryError(err) || IsTagTermError(err)
}

// QueryTermReason is the text to show for an error IsQueryTermError accepted,
// without the wrappers callers put around it.
func QueryTermReason(err error) string {
	if te, ok := errors.AsType[*TagTermError](err); ok {
		return te.Reason
	}
	return PerUserQueryReason(err)
}

// CheckQueryTerms is the whole term check for a query that is about to be saved or
// evaluated: the per-user terms (CheckPerUserTerms) and the tag: terms
// (CheckTagTerms). now is the instant relative dates resolve against.
func CheckQueryTerms(n Node, now time.Time) error {
	if err := CheckPerUserTerms(n, now); err != nil {
		return err
	}
	return CheckTagTerms(n)
}

// CheckTagTerms refuses a query whose tag: terms cannot be applied as an exact
// filter (see SplitTagTerms). A nil node and a query with no tag: term are accepted.
func CheckTagTerms(n Node) error {
	_, _, err := SplitTagTerms(n)
	return err
}

// SplitTagTerms removes the plain tag: conjuncts from n and returns what is left
// (nil when nothing is) with the tag names, in the order written. A conjunct is a
// term on the top level of the query, or inside a parenthesised group that is only
// ANDed: the whole query holds only when it does, so it can be applied on its own.
//
// It returns a *TagTermError for a tag: term anywhere else (under OR, under NOT or
// -, in an alternation) and for a value that is not one whole tag name (a prefix,
// wildcard, fuzzy term, boost, comparison, range or empty value).
func SplitTagTerms(n Node) (Node, []string, error) {
	if n == nil {
		return nil, nil, nil
	}
	switch v := n.(type) {
	case *FieldNode:
		if v.Field != TagField {
			return n, nil, nil
		}
		if v.Op != "" && v.Op != "=" || v.Fuzzy || v.Prefix || v.Wildcard || v.Boost != 0 ||
			strings.TrimSpace(v.Value) == "" || unquotedTagWithOperatorChars(v) {
			return nil, nil, &TagTermError{Reason: tagValueReason}
		}
		return nil, []string{v.Value}, nil
	case *AndNode:
		var kept []Node
		var tags []string
		for i, c := range v.Children {
			// tag:"noir"^2 and tag:"noir"~ leave the modifier behind as free text; a
			// tag filter takes no boost and no fuzziness, so it is refused here rather
			// than left to search for the word "^2".
			if i > 0 && isTagModifier(c) {
				if prev, ok := v.Children[i-1].(*FieldNode); ok && prev.Field == TagField {
					return nil, nil, &TagTermError{Reason: tagValueReason}
				}
			}
			rest, t, err := SplitTagTerms(c)
			if err != nil {
				return nil, nil, err
			}
			tags = append(tags, t...)
			if rest != nil {
				kept = append(kept, rest)
			}
		}
		switch len(kept) {
		case 0:
			return nil, tags, nil
		case 1:
			return kept[0], tags, nil
		}
		if len(tags) == 0 {
			return n, nil, nil
		}
		return &AndNode{Children: kept}, tags, nil
	case *OrNode, *NotNode, *ValueAltNode:
		if namesTagTerm(n) {
			return nil, nil, &TagTermError{Reason: tagShapeReason}
		}
	}
	return n, nil, nil
}

// namesTagTerm reports whether any term under n is a tag: term.
func namesTagTerm(n Node) bool {
	switch v := n.(type) {
	case *AndNode:
		for _, c := range v.Children {
			if namesTagTerm(c) {
				return true
			}
		}
	case *OrNode:
		for _, c := range v.Children {
			if namesTagTerm(c) {
				return true
			}
		}
	case *NotNode:
		return namesTagTerm(v.Child)
	case *FieldNode:
		return v.Field == TagField
	case *ValueAltNode:
		return v.Field == TagField
	}
	return false
}

// errTagUnsplit is what the index translator returns for a tag: term it is handed:
// it has none to give (the index cannot answer one exactly), so a caller that
// translates without splitting gets a refusal, never a word match or, negated,
// the whole library.
var errTagUnsplit = &TagTermError{Reason: tagShapeReason}

// Translation is a query translated for a search: the index query for everything
// but the tag: terms, the per-user filters the caller applies afterwards, and the
// tag names the caller applies exactly (ResolveTagIDs), ANDed with the rest.
type Translation struct {
	Query   query.Query
	PerUser []PerUserFilter
	Tags    []string
}

// TranslateQuery is TranslateAt for a caller that can apply tag: terms: it splits
// them off (SplitTagTerms) and translates the rest. A caller that cannot must use
// TranslateAt, which refuses a tag: term.
func TranslateQuery(node Node, now time.Time) (Translation, error) {
	rest, tags, err := SplitTagTerms(node)
	if err != nil {
		return Translation{}, err
	}
	if len(tags) > 0 {
		rest = dropStopwordRemainder(rest)
	}
	q, perUser, err := TranslateAt(rest, now)
	if err != nil {
		return Translation{}, err
	}
	return Translation{Query: q, PerUser: perUser, Tags: tags}, nil
}

// dropStopwordRemainder drops the free-text conjuncts that analyze to nothing (a
// stop word such as "the" or "or", or bare punctuation such as "-") from what is
// left of a query after its tag: terms were split off. dropStopwordOnlyConjuncts
// drops them only when a real term remains next to them, and the tag term was that
// real term until it was lifted out of the tree, so "the tag:noir", "- tag:noir"
// and "(tag:noir OR)" must be judged as if it were still there. When nothing is
// left the result is nil: the tags alone are the filter. It reaches into AND-only
// groups, the only place SplitTagTerms leaves a conjunct.
func dropStopwordRemainder(n Node) Node {
	switch v := n.(type) {
	case *FreeTextNode:
		if !v.Quoted && !v.Prefix && !v.Fuzzy && isStopwordOnly(v.Value) {
			return nil
		}
	case *AndNode:
		kept := make([]Node, 0, len(v.Children))
		for _, c := range v.Children {
			if c = dropStopwordRemainder(c); c != nil {
				kept = append(kept, c)
			}
		}
		switch len(kept) {
		case 0:
			return nil
		case 1:
			return kept[0]
		}
		return &AndNode{Children: kept}
	}
	return n
}

// TagBookLister is the one store method an exact tag filter needs.
type TagBookLister interface {
	GetBooksByTag(tag string) ([]string, error)
}

// ResolveTagIDs returns the ids of the books that carry EVERY one of tags, exactly
// as the tags[] parameter does: GetBooksByTag per tag (the store folds case and
// trims), intersected. The result is sorted and never nil; an empty result means no
// book matches. tags must not be empty.
func ResolveTagIDs(store TagBookLister, tags []string) ([]string, error) {
	if len(tags) == 0 {
		return nil, errors.New("no tags to resolve")
	}
	var set map[string]struct{}
	for _, tag := range tags {
		ids, err := store.GetBooksByTag(tag)
		if err != nil {
			return nil, fmt.Errorf("resolve tag %q: %w", tag, err)
		}
		cur := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			if set == nil {
				cur[id] = struct{}{}
				continue
			}
			if _, ok := set[id]; ok {
				cur[id] = struct{}{}
			}
		}
		set = cur
		if len(set) == 0 {
			break
		}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// RestrictToTags confines q to the books in ids (Bleve document ids are book ids).
func RestrictToTags(q query.Query, ids []string) query.Query {
	return bleve.NewConjunctionQuery(q, bleve.NewDocIDQuery(ids))
}
