// file: core/storage/index/analyzer.go
// version: 0.1.0
// guid: 3f0c9a57-6b1e-4d2a-9e84-c1d7b25a6f30
// last-edited: 2026-11-20

package index

import (
	"fmt"
	"slices"

	"github.com/falkcorp/mediacore/storage/keys"
)

// The built-in analyzers a Field may name without declaring them.
const (
	// AnalyzerStandard splits on Unicode word boundaries, lowercases and drops
	// English stop words. The default of a Text field.
	AnalyzerStandard = "standard"
	// AnalyzerKeyword indexes the whole value as one term, exactly as given
	// (case-sensitive). The default of a Keyword field.
	AnalyzerKeyword = "keyword"
	// AnalyzerKeywordLowercase indexes the whole value as one lowercased term:
	// a case-insensitive exact match. Term, Prefix and Wildcard on a field
	// that uses it are lowercased by the engine too, and its facet terms come
	// back lowercased.
	AnalyzerKeywordLowercase = "keyword_lowercase"
)

func builtinAnalyzer(name string) bool {
	switch name {
	case AnalyzerStandard, AnalyzerKeyword, AnalyzerKeywordLowercase:
		return true
	}
	return false
}

// Analyzer is an index's own analyzer, built from core's closed set of
// components: optional character filters (run on the raw text, in order), one
// tokenizer, then optional token filters (run on each token, in order). A
// Field uses it by naming it in Field.Analyzer. The components are core's,
// not an engine's, so a Spec never names an engine type (I5), and an engine
// refuses a component it cannot build instead of indexing with a default.
//
// The definition is part of the mapping fingerprint: editing an analyzer
// recreates the index (a full rebuild) even if MappingVersion was not bumped.
type Analyzer struct {
	Name         string // a-z, 0-9 and _; not a built-in's name
	CharFilters  []CharFilter
	Tokenizer    Tokenizer // required
	TokenFilters []TokenFilter
}

// CharFilter rewrites the raw text before it is tokenized.
type CharFilter uint8

const (
	CharFilterInvalid CharFilter = iota // the zero value is rejected
	// CharUnderscoreToSpace maps every '_' to a space, so "Batman_Year_One"
	// is three words. Unicode word breaking keeps '_' inside a word (it is a
	// word joiner), so without it a filename-style value is one token.
	CharUnderscoreToSpace
)

// String names the filter.
func (c CharFilter) String() string {
	switch c {
	case CharUnderscoreToSpace:
		return "underscore_to_space"
	}
	return fmt.Sprintf("invalid(%d)", uint8(c))
}

// Tokenizer splits text into tokens.
type Tokenizer uint8

const (
	TokenizerInvalid Tokenizer = iota // the zero value is rejected
	// TokenizeUnicodeWords splits on Unicode word boundaries (UAX #29).
	TokenizeUnicodeWords
	// TokenizeWholeValue keeps the whole value as one token.
	TokenizeWholeValue
)

// String names the tokenizer.
func (t Tokenizer) String() string {
	switch t {
	case TokenizeUnicodeWords:
		return "unicode_words"
	case TokenizeWholeValue:
		return "whole_value"
	}
	return fmt.Sprintf("invalid(%d)", uint8(t))
}

// TokenFilter rewrites or drops tokens.
type TokenFilter uint8

const (
	TokenFilterInvalid TokenFilter = iota // the zero value is rejected
	// FilterLowercase lowercases each token. An analyzer that has it makes
	// the engine lowercase the text of Term, Prefix and Wildcard on its
	// fields, since those match index terms without analysis.
	FilterLowercase
	// FilterEnglishPossessive strips a trailing 's ("Joker's" -> "Joker").
	FilterEnglishPossessive
	// FilterPorterStem reduces each token to its Porter stem ("families" ->
	// "famili"), so a Match finds other forms of a word. Prefix and Wildcard
	// text is not stemmed: a prefix of a word is not a word.
	FilterPorterStem
)

// String names the filter.
func (f TokenFilter) String() string {
	switch f {
	case FilterLowercase:
		return "lowercase"
	case FilterEnglishPossessive:
		return "english_possessive"
	case FilterPorterStem:
		return "porter_stem"
	}
	return fmt.Sprintf("invalid(%d)", uint8(f))
}

// Lowercases reports whether a uses FilterLowercase.
func (a Analyzer) Lowercases() bool { return slices.Contains(a.TokenFilters, FilterLowercase) }

// analyzerNames validates s.Analyzers and returns the names they declare.
func (s Spec) analyzerNames() (map[string]bool, []string) {
	var probs []string
	names := make(map[string]bool, len(s.Analyzers))
	for _, a := range s.Analyzers {
		switch {
		case keys.ValidName(a.Name) != nil:
			probs = append(probs, fmt.Sprintf("analyzer %q: a name of a-z, 0-9 and _ is required", a.Name))
		case builtinAnalyzer(a.Name):
			probs = append(probs, fmt.Sprintf("analyzer %q: the name is a built-in's", a.Name))
		case names[a.Name]:
			probs = append(probs, fmt.Sprintf("analyzer %q declared twice", a.Name))
		}
		names[a.Name] = true
		for _, c := range a.CharFilters {
			if c != CharUnderscoreToSpace {
				probs = append(probs, fmt.Sprintf("analyzer %q: char filter %s", a.Name, c))
			}
		}
		if a.Tokenizer != TokenizeUnicodeWords && a.Tokenizer != TokenizeWholeValue {
			probs = append(probs, fmt.Sprintf("analyzer %q: tokenizer %s (one is required)", a.Name, a.Tokenizer))
		}
		for _, f := range a.TokenFilters {
			switch f {
			case FilterLowercase, FilterEnglishPossessive, FilterPorterStem:
			default:
				probs = append(probs, fmt.Sprintf("analyzer %q: token filter %s", a.Name, f))
			}
		}
	}
	return names, probs
}
