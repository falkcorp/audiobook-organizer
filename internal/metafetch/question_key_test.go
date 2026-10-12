// file: internal/metafetch/question_key_test.go
// version: 1.2.0
// guid: 8c41f0d7-2a6e-4b93-b5d8-61e7a9c3f2b4
// last-edited: 2026-10-11

package metafetch

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// Every case is a pair of calls and whether they are the SAME question (one
// shared key) or not. Fixtures are synthetic.
func TestNormalizeQuestion_Table(t *testing.T) {
	const aud = metadata.SourceIDAudible
	ta := func(title string, authors ...string) Question { return Question{Title: title, Authors: authors} }
	type side struct {
		provider, endpoint string
		q                  Question
	}
	cases := []struct {
		name string
		a, b side
		same bool
	}{
		{"colon and dash are the same separator",
			side{aud, EndpointSearchTitle, ta("Title 000123: Part 2")}, side{aud, EndpointSearchTitle, ta("Title 000123 - Part 2")}, true},
		{"credit order is part of the question",
			side{aud, EndpointSearchTitleAuthor, ta("Title 000123", "Author 07", "Author 12")},
			side{aud, EndpointSearchTitleAuthor, ta("Title 000123", "Author 12", "Author 07")}, false},
		{"flat credits: A, B differs from B, A",
			side{aud, EndpointSearchTitleAuthor, ta("Title 000123", CreditsFromFlat("Author 07, Author 12")...)},
			side{aud, EndpointSearchTitleAuthor, ta("Title 000123", CreditsFromFlat("Author 12, Author 07")...)}, false},
		{"flat credits: A , B equals A, B",
			side{aud, EndpointSearchTitleAuthor, ta("Title 000123", CreditsFromFlat("Author 07 , Author 12")...)},
			side{aud, EndpointSearchTitleAuthor, ta("Title 000123", CreditsFromFlat("Author 07, Author 12")...)}, true},
		{"one credit 'A B' is not two credits 'A, B'",
			side{aud, EndpointSearchTitleAuthor, ta("Title 000123", "Author07 Author12")},
			side{aud, EndpointSearchTitleAuthor, ta("Title 000123", "Author07", "Author12")}, false},
		{"case fold: sharp s folds to ss",
			side{aud, EndpointSearchTitle, ta("STRASSE 000123")}, side{aud, EndpointSearchTitle, ta("straße 000123")}, true},
		{"case fold: capital sharp s folds to ss",
			side{aud, EndpointSearchTitle, ta("ẞ 000123")}, side{aud, EndpointSearchTitle, ta("ss 000123")}, true},
		// U+0130 full-folds to "i" + U+0307; strings.ToLower maps it to a
		// bare "i", so these pairs are equal (or not) only under a real case
		// fold. The decomposed spelling "I" + U+0307 composes to U+0130 under
		// NFKC first.
		{"case fold: dotted capital I folds to i with a combining dot",
			side{aud, EndpointSearchTitle, ta("\u0130TEM 000123")}, side{aud, EndpointSearchTitle, ta("i\u0307tem 000123")}, true},
		{"case fold: dotted capital I, precomposed or not",
			side{aud, EndpointSearchTitle, ta("\u0130tem 000123")}, side{aud, EndpointSearchTitle, ta("I\u0307TEM 000123")}, true},
		{"case fold: dotted capital I is not a plain i",
			side{aud, EndpointSearchTitle, ta("\u0130tem 000123")}, side{aud, EndpointSearchTitle, ta("item 000123")}, false},
		{"a credit carrying the credit separator is not two credits",
			side{aud, EndpointSearchTitleAuthor, ta("Title 000123", "Author 07\x1fAuthor 12")},
			side{aud, EndpointSearchTitleAuthor, ta("Title 000123", "Author 07", "Author 12")}, false},
		{"a value carrying the field separator cannot forge a field",
			side{aud, EndpointIdentitySearch, Question{Title: "Title 000123\x1ek=kw 01", NumResults: 10}},
			side{aud, EndpointIdentitySearch, Question{Title: "Title 000123", Keywords: "kw 01", NumResults: 10}}, false},
		{"case fold: final sigma",
			side{aud, EndpointSearchTitle, ta("ΛΟΓΟΣ 000123")}, side{aud, EndpointSearchTitle, ta("λογος 000123")}, true},
		{"NFKC: fullwidth letters and the fi ligature",
			side{aud, EndpointSearchTitle, ta("Ｔｉｔｌｅ ﬁle 000123")}, side{aud, EndpointSearchTitle, ta("title file 000123")}, true},
		{"whitespace collapses",
			side{aud, EndpointSearchTitle, ta("  Title \t 000123  ")}, side{aud, EndpointSearchTitle, ta("Title 000123")}, true},
		{"typographic quotes and apostrophes are stripped",
			side{aud, EndpointSearchTitle, ta("“Title” Owner’s 000123")}, side{aud, EndpointSearchTitle, ta("Title Owner s 000123")}, true},
		{"brackets and periods are stripped",
			side{aud, EndpointSearchTitle, ta("Title 000123 (Book 1.)")}, side{aud, EndpointSearchTitle, ta("Title 000123 Book 1")}, true},
		{"unlisted symbols are kept",
			side{aud, EndpointSearchTitle, ta("Title++ 000123")}, side{aud, EndpointSearchTitle, ta("Title 000123")}, false},
		{"ampersand is kept",
			side{aud, EndpointSearchTitle, ta("Title & 000123")}, side{aud, EndpointSearchTitle, ta("Title 000123")}, false},
		{"a field the call does not send never changes the key (author, title-only call)",
			side{aud, EndpointSearchTitle, ta("Title 000123", "Author 07")}, side{aud, EndpointSearchTitle, ta("Title 000123")}, true},
		{"a field the call does not send never changes the key (narrator, series, ISBN)",
			side{aud, EndpointSearchTitleAuthor, Question{Title: "Title 000123", Authors: []string{"Author 07"}, Narrator: "Narrator 03", Series: "Series 09", ISBN13: "9780000000001"}},
			side{aud, EndpointSearchTitleAuthor, ta("Title 000123", "Author 07")}, true},
		{"context search: series is sent, so it counts",
			side{aud, EndpointSearchContext, Question{Title: "Title 000123", Series: "Series 09"}},
			side{aud, EndpointSearchContext, Question{Title: "Title 000123", Series: "Series 10"}}, false},
		{"context search: narrator is sent, so it counts",
			side{aud, EndpointSearchContext, Question{Title: "Title 000123", Narrator: "Narrator 03"}},
			side{aud, EndpointSearchContext, Question{Title: "Title 000123"}}, false},
		{"tagged fields: a title value is not an author value",
			side{aud, EndpointSearchContext, Question{Title: "Word 000123"}},
			side{aud, EndpointSearchContext, Question{Authors: []string{"Word 000123"}}}, false},
		{"provider is part of the key",
			side{aud, EndpointSearchTitle, ta("Title 000123")}, side{metadata.SourceIDOpenLibrary, EndpointSearchTitle, ta("Title 000123")}, false},
		{"endpoint is part of the key",
			side{aud, EndpointSearchTitle, ta("Title 000123")}, side{aud, EndpointSearchTitleAuthor, ta("Title 000123")}, false},
		{"identity search: the result count is sent, so it counts",
			side{aud, EndpointIdentitySearch, Question{Title: "Title 000123", NumResults: 10}},
			side{aud, EndpointIdentitySearch, Question{Title: "Title 000123", NumResults: 20}}, false},
	}
	require.GreaterOrEqual(t, len(cases), 15)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ka := NormalizeQuestion(c.a.provider, c.a.endpoint, c.a.q)
			kb := NormalizeQuestion(c.b.provider, c.b.endpoint, c.b.q)
			if c.same {
				assert.Equal(t, ka, kb)
			} else {
				assert.NotEqual(t, ka, kb)
			}
		})
	}
}

// The key shape: provider and endpoint in clear text, then a full sha256.
func TestNormalizeQuestion_KeyShape(t *testing.T) {
	k := NormalizeQuestion(metadata.SourceIDAudible, EndpointSearchTitleAuthor, Question{Title: "Title 000123", Authors: []string{"Author 07"}})
	require.True(t, strings.HasPrefix(k, "pcache:q:audible:search_title_author:"), k)
	assert.Len(t, strings.TrimPrefix(k, "pcache:q:audible:search_title_author:"), 64)
}

// A provider pointed at another store keys apart from the default store; the
// default store adds nothing to the endpoint.
func TestNormalizeQuestion_MarketplaceIsAPrefix(t *testing.T) {
	prev := config.AppConfig.MetadataSources
	t.Cleanup(func() { config.AppConfig.MetadataSources = prev })

	q := Question{Title: "Title 000123"}
	config.AppConfig.MetadataSources = nil
	def := questionKey(metadata.SourceIDAudible, EndpointSearchTitle, q)
	require.True(t, strings.HasPrefix(def, "pcache:q:audible:search_title:"), def)

	config.AppConfig.MetadataSources = []config.MetadataSource{{ID: metadata.SourceIDAudible, BaseURL: "https://api.example.invalid/1.0"}}
	other := questionKey(metadata.SourceIDAudible, EndpointSearchTitle, q)
	assert.True(t, strings.HasPrefix(other, "pcache:q:audible:search_title@api.example.invalid:"), other)
	assert.Equal(t, def[strings.LastIndex(def, ":"):], other[strings.LastIndex(other, ":"):],
		"the marketplace is outside the hash")
}

// questionKey is NormalizeQuestion with the provider's marketplace applied.
// Production call sites need the marketplace-qualified endpoint as well, so
// they apply endpointWithMarketplace themselves; this is the test shorthand.
func questionKey(provider, endpoint string, q Question) string {
	return NormalizeQuestion(provider, endpointWithMarketplace(provider, endpoint), q)
}
