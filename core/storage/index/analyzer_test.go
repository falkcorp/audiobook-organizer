// file: core/storage/index/analyzer_test.go
// version: 0.1.0
// guid: 5e9a1c37-8b24-4f0d-b6e3-2c7f9d4a1b86
// last-edited: 2026-11-20

package index_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/storage/index"
)

func analyzerSpec() index.Spec {
	return index.Spec{
		Name: "batcomputer", MappingVersion: "1",
		Fields: []index.Field{
			{Name: "title", Kind: index.Text, Analyzer: "casefile", Boost: 3},
			{Name: "format", Kind: index.Keyword, Analyzer: index.AnalyzerKeywordLowercase, Boost: 1.5},
			{Name: "alias", Kind: index.Keyword, Analyzer: index.AnalyzerKeyword},
			{Name: "blurb", Kind: index.Text, Analyzer: index.AnalyzerStandard},
		},
		Analyzers: []index.Analyzer{{
			Name:         "casefile",
			CharFilters:  []index.CharFilter{index.CharUnderscoreToSpace},
			Tokenizer:    index.TokenizeUnicodeWords,
			TokenFilters: []index.TokenFilter{index.FilterEnglishPossessive, index.FilterLowercase, index.FilterPorterStem},
		}},
		Rebuild:  func(context.Context, index.RebuildWriter) error { return nil },
		Document: func(context.Context, string) (index.Doc, bool, error) { return nil, false, nil },
	}
}

func TestSpecValidateAnalyzersAndBoosts(t *testing.T) {
	if err := analyzerSpec().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		mut  func(*index.Spec)
		want string
	}{
		"unknown analyzer":     {func(s *index.Spec) { s.Fields[0].Analyzer = "nope" }, `analyzer "nope" is neither a built-in`},
		"undeclared custom":    {func(s *index.Spec) { s.Analyzers = nil }, `analyzer "casefile" is neither a built-in`},
		"shadows a built-in":   {func(s *index.Spec) { s.Analyzers[0].Name = index.AnalyzerStandard }, "the name is a built-in's"},
		"declared twice":       {func(s *index.Spec) { s.Analyzers = append(s.Analyzers, s.Analyzers[0]) }, "declared twice"},
		"bad analyzer name":    {func(s *index.Spec) { s.Analyzers[0].Name = "Case File" }, "a name of a-z"},
		"no tokenizer":         {func(s *index.Spec) { s.Analyzers[0].Tokenizer = index.TokenizerInvalid }, "tokenizer invalid(0)"},
		"unknown tokenizer":    {func(s *index.Spec) { s.Analyzers[0].Tokenizer = 99 }, "tokenizer invalid(99)"},
		"unknown token filter": {func(s *index.Spec) { s.Analyzers[0].TokenFilters = []index.TokenFilter{0} }, "token filter invalid(0)"},
		"unknown char filter":  {func(s *index.Spec) { s.Analyzers[0].CharFilters = []index.CharFilter{7} }, "char filter invalid(7)"},
		"negative boost":       {func(s *index.Spec) { s.Fields[0].Boost = -1 }, "boost -1"},
		"NaN boost":            {func(s *index.Spec) { s.Fields[0].Boost = math.NaN() }, "boost NaN"},
		"infinite boost":       {func(s *index.Spec) { s.Fields[0].Boost = math.Inf(1) }, "boost +Inf"},
		"boost on a number": {func(s *index.Spec) {
			s.Fields = append(s.Fields, index.Field{Name: "year", Kind: index.Number, Boost: 2})
		}, "a boost applies to Text and Keyword"},
		"analyzer on a boolean": {func(s *index.Spec) {
			s.Fields = append(s.Fields, index.Field{Name: "free", Kind: index.Bool, Analyzer: "casefile"})
		}, "an analyzer applies to Text and Keyword"},
	} {
		t.Run(name, func(t *testing.T) {
			s := analyzerSpec()
			tc.mut(&s)
			err := s.Validate()
			if !errors.Is(err, index.ErrInvalidSpec) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate: %v, want ErrInvalidSpec naming %q", err, tc.want)
			}
		})
	}
}

func TestAnalyzerComponentNames(t *testing.T) {
	for got, want := range map[string]string{
		index.CharUnderscoreToSpace.String():   "underscore_to_space",
		index.CharFilterInvalid.String():       "invalid(0)",
		index.TokenizeUnicodeWords.String():    "unicode_words",
		index.TokenizeWholeValue.String():      "whole_value",
		index.TokenizerInvalid.String():        "invalid(0)",
		index.FilterLowercase.String():         "lowercase",
		index.FilterEnglishPossessive.String(): "english_possessive",
		index.FilterPorterStem.String():        "porter_stem",
		index.TokenFilterInvalid.String():      "invalid(0)",
	} {
		if got != want {
			t.Errorf("name %q, want %q", got, want)
		}
	}
	a := analyzerSpec().Analyzers[0]
	if !a.Lowercases() {
		t.Error("casefile lowercases")
	}
	a.TokenFilters = []index.TokenFilter{index.FilterPorterStem}
	if a.Lowercases() {
		t.Error("a porter-only analyzer does not lowercase")
	}
}

func TestWildcardConstructor(t *testing.T) {
	n, err := index.Describe(index.Wildcard("title", "*bat?"))
	if err != nil || n.Op != index.OpWildcard || n.Field != "title" || n.Text != "*bat?" {
		t.Fatalf("Describe = %+v, %v", n, err)
	}
	for _, p := range []string{"", "*", "?", "*?*"} {
		if _, err := index.Describe(index.Wildcard("title", p)); !errors.Is(err, index.ErrInvalidQuery) {
			t.Errorf("Wildcard(%q): %v, want ErrInvalidQuery (no literal character)", p, err)
		}
	}
	if _, err := index.Describe(index.Wildcard("", "*bat")); !errors.Is(err, index.ErrInvalidQuery) {
		t.Errorf("Wildcard with no field: %v, want ErrInvalidQuery", err)
	}
}

func TestAllAndMatchAnyFieldConstructors(t *testing.T) {
	if n, err := index.Describe(index.All()); err != nil || n.Op != index.OpAll || n.Field != "" {
		t.Fatalf("All = %+v, %v", n, err)
	}
	if n, err := index.Describe(index.MatchAnyField("dark knight")); err != nil || n.Op != index.OpMatchAny || n.Field != "" || n.Text != "dark knight" {
		t.Fatalf("MatchAnyField = %+v, %v", n, err)
	}
}

func TestBoostConstructor(t *testing.T) {
	n, err := index.Describe(index.Boost(index.Match("title", "joker"), 3))
	if err != nil || n.Op != index.OpMatch || n.Boost != 3 {
		t.Fatalf("Boost = %+v, %v", n, err)
	}
	n, err = index.Describe(index.Boost(index.Boost(index.Term("alias", "clown"), 3), 0.5))
	if err != nil || n.Boost != 0.5 {
		t.Fatalf("re-Boost = %+v, %v; want the last weight", n, err)
	}
	for name, q := range map[string]index.Query{
		"zero":     index.Boost(index.Match("title", "joker"), 0),
		"negative": index.Boost(index.Match("title", "joker"), -2),
		"NaN":      index.Boost(index.Match("title", "joker"), math.NaN()),
		"infinite": index.Boost(index.Match("title", "joker"), math.Inf(1)),
		"bad q":    index.Boost(index.Match("", "joker"), 2),
		"nil q":    index.Boost(nil, 2),
	} {
		if _, err := index.Describe(q); !errors.Is(err, index.ErrInvalidQuery) {
			t.Errorf("%s: %v, want ErrInvalidQuery", name, err)
		}
	}
}
