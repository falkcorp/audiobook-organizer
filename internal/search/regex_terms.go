// file: internal/search/regex_terms.go
// version: 1.0.1
// guid: da97759e-e2f6-4dd3-b387-5920768c88ee
// last-edited: 2026-10-26

package search

import (
	"errors"
	"regexp"

	"github.com/falkcorp/audiobook-organizer/internal/querygrammar"
)

// RegexValueError is the refusal for a /regex/ field value that reached the search
// index. Reason is shown to the person who typed the query.
//
// The index matches analyzed words and has no regular expressions. Regex values
// (title:/^the /, description:/(?i)gotham/) are evaluated only by the library filter
// layer (internal/querygrammar), which is where the web search box sends a field term
// that stands alone. A /regex/ that reached the index anyway was answered as something
// else without a word: when the text still parsed, as a match on the words inside the
// slashes (title:/hush|knight/ found "Hush" only); when it did not, as a substring
// search over the whole raw text, which finds nothing.
type RegexValueError struct {
	Reason string
}

func (e *RegexValueError) Error() string { return e.Reason }

const regexValueReason = "a /regex/ value cannot be used in the search parameter: the search index " +
	"matches whole words and has no regular expressions, so it would be read as the words " +
	"between the slashes. A regex works only as a field filter that stands on its own " +
	"(title:/^the /), not inside this search text; write plain words instead"

// IsRegexValueError reports whether err is the refusal of a /regex/ field value in
// a search.
func IsRegexValueError(err error) bool {
	var re *RegexValueError
	return errors.As(err, &re)
}

// CheckRegexTerms refuses a query holding a /regex/ value on a field the index holds:
// an unquoted value that starts with a slash, by the same rule the filter layer routes
// on (querygrammar.IsRegexForm). A nil node and a query with no regex are accepted.
//
// Only an indexed field is refused (IsIndexedField), because that is where the index
// answers the term as something else: as words. The parser reads any `name:` as a
// field, so a URL (https://x/y), a drive path (c:/lib/Gotham/x) or a title with a colon
// (Batman: /Year One/) is a "field" with a slash value too, and is no regex; it is
// searched as it always was. A field the filter layer knows but the index does not
// (codec, ident, metadata) is refused by its own check in the list handler
// (searchNamesUnindexedFilterField), with a message that names the field.
//
// A value inside a field's own alternation (title:(/a/|b)) is a regex too; a quoted
// one is not, but an alternation does not record quoting, so a quoted value there
// that starts with a slash is refused as well, which is the safe side.
func CheckRegexTerms(n Node) error {
	switch v := n.(type) {
	case *AndNode:
		for _, c := range v.Children {
			if err := CheckRegexTerms(c); err != nil {
				return err
			}
		}
	case *OrNode:
		for _, c := range v.Children {
			if err := CheckRegexTerms(c); err != nil {
				return err
			}
		}
	case *NotNode:
		return CheckRegexTerms(v.Child)
	case *FieldNode:
		if IsIndexedField(v.Field) && querygrammar.IsRegexForm(v.Value, v.Quoted) {
			return &RegexValueError{Reason: regexValueReason}
		}
	case *ValueAltNode:
		if !IsIndexedField(v.Field) {
			return nil
		}
		for _, val := range v.Values {
			if querygrammar.IsRegexForm(val, false) {
				return &RegexValueError{Reason: regexValueReason}
			}
		}
	}
	return nil
}

var (
	quotedTextInQuery = regexp.MustCompile(`"[^"]*"?`)
	// regexFieldValueInText finds field:/ at the start of a term, after a space, a
	// parenthesis, a NOT bang or a minus.
	regexFieldValueInText = regexp.MustCompile(`(?:^|[\s(!-])([A-Za-z_][A-Za-z0-9_]*):/`)
)

// UnparsableRegexQuery is CheckRegexTerms for text the parser rejected. A /regex/
// holds parentheses, pipes and spaces the search grammar cannot read
// (title:/^(the|a) /), so such text used to fall to the whole-text substring match,
// which shows no rows and says nothing. It is nil when parseErr is nil or no known
// field is written with a regex value, so an ordinary punctuation-heavy title keeps
// its substring fallback. isField says which names are fields; a URL such as
// http://x is not a regex term. Quoted text is skipped.
func UnparsableRegexQuery(q string, parseErr error, isField func(string) bool) error {
	if parseErr == nil {
		return nil
	}
	bare := quotedTextInQuery.ReplaceAllString(q, " ")
	for _, m := range regexFieldValueInText.FindAllStringSubmatch(bare, -1) {
		if isField(m[1]) {
			return &RegexValueError{Reason: regexValueReason}
		}
	}
	return nil
}
