// file: internal/search/regex_terms_test.go
// version: 1.0.0
// guid: 148c6695-1e57-4f30-82d5-738fbd5a4a76
// last-edited: 2026-10-26

package search

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A /regex/ field value is evaluated only by the library filter layer. In a search
// the index would read title:/hush|knight/ as the words "hush" and "knight" and find
// one book of two, and text it cannot parse (title:/^(the|a) /) fell to a substring
// match that found nothing, both without a word. The search refuses them.
func TestCheckRegexTerms_RefusesAnUnquotedRegexValue(t *testing.T) {
	refused := []string{
		"title:/hush|knight/",
		"-title:/^z/",
		"NOT title:/^z/",
		"description:/gotham/",
		"title:/hush/ OR title:knight",
		"(title:Hush title:/^k/)",
		"read_status:finished title:/hush|knight/",
		"title:(/hush/|knight)",
		"title:/unterminated",
	}
	for _, q := range refused {
		t.Run(q, func(t *testing.T) {
			ast, err := ParseQuery(q)
			// the shapes whose own parentheses defeat the parser are in
			// TestUnparsableRegexQuery
			require.NoError(t, err)
			err = CheckRegexTerms(ast)
			require.Error(t, err)
			assert.True(t, IsRegexValueError(err))
			assert.True(t, IsQueryTermError(err), "handlers answer it with the other term refusals")
			assert.Contains(t, QueryTermReason(fmt.Errorf("search: %w", err)), "/regex/ value")
			// and so is the whole term check and the translation, so a caller that
			// skips one still cannot answer it as words
			assert.Error(t, CheckQueryTerms(ast, time.Now()))
			_, terr := TranslateQuery(ast, time.Now())
			require.Error(t, terr)
			assert.True(t, IsRegexValueError(terr))
		})
	}
}

func TestCheckRegexTerms_AcceptsEverythingThatIsNotARegexValue(t *testing.T) {
	for _, q := range []string{
		"Hush",
		"title:Hush",
		`title:"/hush/"`,
		`title:"a/b"`,
		"title:a/b",
		"read_status:(finished|in_progress)",
		"title:(Hush|Knightfall)",
		"path/to the Batman",
		"tag:noir",
	} {
		t.Run(q, func(t *testing.T) {
			ast, err := ParseQuery(q)
			require.NoError(t, err)
			assert.NoError(t, CheckRegexTerms(ast))
		})
	}
	assert.NoError(t, CheckRegexTerms(nil))
}

func TestUnparsableRegexQuery(t *testing.T) {
	known := func(f string) bool {
		switch f {
		case "title", "description", "metadata", "read_status":
			return true
		}
		return false
	}
	parseErr := fmt.Errorf("does not parse")
	for _, tc := range []struct {
		q       string
		refused bool
	}{
		{"title:/^(the|a) /", true},
		{"-title:/^(?:\\d)/", true},
		{"description:/(?i)gotham/", true},
		{"metadata:/(audible|google)/", true},
		{"read_status:finished title:/^(the|a) /", true},
		{"(title:/^(a|b)/", true},
		{"NOT title:/^(a|b)/", true},
		// not regex terms: the substring fallback keeps these
		{"The (Long Halloween", false},
		{"http://example.invalid/Batman (1989", false},
		{`title:"/^(the|a) /"`, false},
		{"title:Batman(1989", false},
		{"unknownfield:/x(/", false},
		{"", false},
	} {
		t.Run(tc.q, func(t *testing.T) {
			err := UnparsableRegexQuery(tc.q, parseErr, known)
			assert.Equal(t, tc.refused, err != nil, "got %v", err)
			if err != nil {
				assert.True(t, IsRegexValueError(err))
				assert.True(t, IsQueryTermError(err))
			}
		})
	}
	// text that parsed is not this function's business
	assert.NoError(t, UnparsableRegexQuery("title:/x/", nil, known))
}
