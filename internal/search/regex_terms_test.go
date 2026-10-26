// file: internal/search/regex_terms_test.go
// version: 1.0.2
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
		// N8: tag: is an exact tag filter, not an index field, and the parser keeps a
		// field name's capitals; neither may let a regex through to match nothing
		"tag:/noir/",
		"tags:/noir/",
		"TAGS:/x/",
		"Tag:/noir/",
		"Title:/x/",
		"-tag:/noir/",
		"title:Hush OR tag:/noir/",
		"tag:(/noir/|gotham)",
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

// N1: the parser reads any `name:` as a field, so a pasted URL, a drive path and a title
// with a colon are "fields" with a slash value. They are no regex, and no field the
// index holds: they are searched as they always were and get no regex message.
func TestCheckRegexTerms_AFieldTheIndexDoesNotHoldIsNotARegexTerm(t *testing.T) {
	for _, q := range []string{
		"https://www.audible.com/pd/B0123",
		"http://example.invalid/",
		"https://www.audible.com/pd/B0123/",
		`c:/lib/Gotham/Hush`,
		"Batman: /Year One/",
		"mailto:/x/",
		"foo:/bar",
		"title:Hush OR https://example.invalid/knightfall",
		"foo:(a|/x/)",
	} {
		t.Run(q, func(t *testing.T) {
			ast, err := ParseQuery(q)
			require.NoError(t, err)
			assert.NoError(t, CheckRegexTerms(ast))
			assert.NoError(t, CheckQueryTerms(ast, time.Now()))
		})
	}
	// the capitals of a URL scheme or a title word make no difference either way
	for _, q := range []string{"HTTPS://www.audible.com/pd/B0123", "Batman: /Year One/", "File:/lib/Gotham/Hush"} {
		ast, err := ParseQuery(q)
		require.NoError(t, err, q)
		assert.NoError(t, CheckRegexTerms(ast), q)
	}
	// the same slash on a field the index holds is still refused
	ast, err := ParseQuery("title:/Year One/ OR https://example.invalid/")
	require.NoError(t, err)
	assert.True(t, IsRegexValueError(CheckRegexTerms(ast)))
}

func TestUnparsableRegexQuery(t *testing.T) {
	known := func(f string) bool {
		switch f {
		case "title", "description", "metadata", "read_status":
			return true
		}
		return IsRegexField(f)
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
		{"tag:/(a|b)/", true},
		{"Title:/^(a|b)/", true},
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
