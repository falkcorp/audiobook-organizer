// file: internal/metafetch/question_key.go
// version: 1.1.0
// guid: 5d2b8e41-7c39-4f0a-9e16-3b8a0d6c2f57
// last-edited: 2026-10-11

package metafetch

// The shared provider-answer cache (P1, pcache:q:) is keyed by the QUESTION a
// provider was asked, not by the book that asked it, so two books asking the
// same question share one answer. This file is the one definition of "the
// same question" (spec 02 section 3.4):
//
//   - the key hashes EXACTLY the parameters the call sends, and nothing looser:
//     a key looser than the provider's own matching would serve one question
//     another question's answer;
//   - each text field is NFKC-normalized, case FOLDED (not strings.ToLower:
//     "ß" folds to "ss", final sigma to sigma), has the punctuation no
//     provider tokenizer keeps (quotes, apostrophes, commas, periods, colons,
//     dashes, brackets) and every control character (the \x1e and \x1f
//     separators below among them) turned into a space, and its whitespace
//     collapsed;
//     letters, digits, '&' and every other symbol are kept, because a
//     character that is not on the list might matter to some provider;
//   - author credits are an ORDERED list (owner rule 2026-10-04): each credit
//     is normalized on its own and the list is joined with \x1f in credit
//     order, never sorted, so "A, B" and "B, A" are different questions;
//   - narrator, series, ISBN and ASIN count only for the endpoint that sends
//     them (SearchByContext): a field the call does not send never changes
//     the key;
//   - the provider id, the endpoint and the marketplace are a clear-text key
//     prefix outside the hash (database.PCacheQuestionKey), so a bust by
//     provider is a prefix delete.

import (
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Provider endpoints, the second segment of a P1 key. Each names one call
// shape; two calls with different response types or parameters never share
// an endpoint.
const (
	// EndpointSearchTitle is MetadataSource.SearchByTitle: sends the title.
	EndpointSearchTitle = "search_title"
	// EndpointSearchTitleAuthor is MetadataSource.SearchByTitleAndAuthor:
	// sends the title and the author string.
	EndpointSearchTitleAuthor = "search_title_author"
	// EndpointSearchContext is ContextualSearch.SearchByContext: sends the
	// whole SearchContext (title, author, narrator, series, ISBNs, ASIN).
	EndpointSearchContext = "search_context"
	// EndpointIdentitySearch is the Audible catalog identity search
	// (AudibleClient.SearchIdentities) the ASIN backfill makes: title,
	// author, keywords and the result count, answered as AudibleIdentity.
	EndpointIdentitySearch = "identity_search"
	// EndpointLookupASIN is a product lookup by ASIN (P2). Its key is the
	// provider and the ASIN (database.PCacheASINKey), not a question hash;
	// the name is recorded on the entry.
	EndpointLookupASIN = "lookup_asin"
)

// Question is one provider call's parameters, as sent.
type Question struct {
	Title string
	// Authors is the ordered credit list the call sends. A call that sends a
	// flat author string passes CreditsFromFlat(author).
	Authors    []string
	Narrator   string
	Series     string
	ISBN10     string
	ISBN13     string
	ASIN       string
	Keywords   string
	NumResults int
}

// questionField is one tagged field of the canonical form. Tags keep a value
// in one field from colliding with the same value in another.
type questionField struct {
	tag   string
	value string
}

// NormalizeQuestion returns the P1 key of one provider call: provider,
// endpoint (with any marketplace already appended) and the sha256 of the
// canonical question, built from exactly the fields that endpoint sends.
func NormalizeQuestion(provider, endpoint string, q Question) string {
	return database.PCacheQuestionKey(provider, endpoint, canonicalQuestion(endpoint, q))
}

// canonicalQuestion is the pre-hash form of q for endpoint.
func canonicalQuestion(endpoint string, q Question) string {
	// Built per call: a cases.Caser keeps state and is not safe for
	// concurrent use, and askVariant runs one goroutine per source.
	fold := cases.Fold()
	text := func(s string) string { return normalizeQuestionText(fold, s) }
	credits := func(list []string) string {
		out := make([]string, 0, len(list))
		for _, c := range list {
			if n := text(c); n != "" {
				out = append(out, n)
			}
		}
		return strings.Join(out, "\x1f")
	}
	base := strings.TrimSuffix(endpoint, endpointMarketplaceSuffix(endpoint))
	var fields []questionField
	switch base {
	case EndpointSearchTitle:
		fields = []questionField{{"t", text(q.Title)}}
	case EndpointSearchTitleAuthor:
		fields = []questionField{{"t", text(q.Title)}, {"a", credits(q.Authors)}}
	case EndpointIdentitySearch:
		fields = []questionField{{"t", text(q.Title)}, {"a", credits(q.Authors)}, {"k", text(q.Keywords)},
			{"n", strconv.Itoa(q.NumResults)}}
	default:
		// EndpointSearchContext, and any endpoint not listed: every field. A
		// stricter key costs at most an extra call; a looser one would serve
		// the wrong answer.
		fields = []questionField{{"t", text(q.Title)}, {"a", credits(q.Authors)}, {"r", text(q.Narrator)},
			{"s", text(q.Series)}, {"i10", text(q.ISBN10)}, {"i13", text(q.ISBN13)}, {"asin", text(q.ASIN)},
			{"k", text(q.Keywords)}, {"n", strconv.Itoa(q.NumResults)}}
	}
	var b strings.Builder
	b.WriteString("v1")
	for _, f := range fields {
		b.WriteByte('\x1e')
		b.WriteString(f.tag)
		b.WriteByte('=')
		b.WriteString(f.value)
	}
	return b.String()
}

// normalizeQuestionText is the per-field rule: NFKC, case fold, the listed
// punctuation and every control character to a space, whitespace collapsed
// and trimmed. The collapse runs
// after the strip so "A , B" and "A, B" agree.
func normalizeQuestionText(fold cases.Caser, s string) string {
	if s == "" {
		return ""
	}
	s = fold.String(norm.NFKC.String(s))
	s = strings.Map(func(r rune) rune {
		if isQuestionPunct(r) {
			return ' '
		}
		// Control characters, \x1e and \x1f among them, are separators:
		// \x1e separates the canonical form's fields and \x1f its credits,
		// so a value carrying either could otherwise forge a field or a
		// credit boundary ("A\x1fB" as one credit keyed as the two credits
		// A, B).
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// isQuestionPunct reports the punctuation no provider tokenizer keeps:
// quotes, apostrophes, commas, periods, colons, dashes and brackets. Anything
// not listed here is kept on purpose (see the file header).
func isQuestionPunct(r rune) bool {
	switch r {
	case '\'', '"', '`', ',', '.', ':':
		return true
	}
	// Pd dashes, Ps/Pe brackets, Pi/Pf the typographic quotes.
	return unicode.In(r, unicode.Pd, unicode.Ps, unicode.Pe, unicode.Pi, unicode.Pf)
}

// CreditsFromFlat splits a flat author string as a call sends it ("A, B")
// into its ordered credits. Only the comma separates: '&' is kept as part of
// a credit, and "and" may be part of a name.
func CreditsFromFlat(author string) []string {
	if strings.TrimSpace(author) == "" {
		return nil
	}
	return strings.Split(author, ",")
}

// endpointWithMarketplace appends the provider's configured marketplace to
// endpoint ("search_title@api.audible.co.uk"): a provider pointed at another
// store answers differently, so its answers must not share keys with the
// default store's. The default store (no configured base URL) adds nothing.
func endpointWithMarketplace(provider, endpoint string) string {
	if m := providerMarketplace(provider); m != "" {
		return endpoint + "@" + m
	}
	return endpoint
}

func endpointMarketplaceSuffix(endpoint string) string {
	if i := strings.IndexByte(endpoint, '@'); i >= 0 {
		return endpoint[i:]
	}
	return ""
}

// providerMarketplace is the host of the provider's configured base URL, or
// "" when none is configured (the provider's built-in default store).
func providerMarketplace(provider string) string {
	for _, s := range config.AppConfig.MetadataSources {
		if s.ID != provider || strings.TrimSpace(s.BaseURL) == "" {
			continue
		}
		if u, err := url.Parse(strings.TrimSpace(s.BaseURL)); err == nil && u.Host != "" {
			return strings.ToLower(u.Host)
		}
		return strings.ToLower(strings.TrimSpace(s.BaseURL))
	}
	return ""
}

// questionKey is NormalizeQuestion with the provider's marketplace applied:
// the key every cache read and write uses.
func questionKey(provider, endpoint string, q Question) string {
	return NormalizeQuestion(provider, endpointWithMarketplace(provider, endpoint), q)
}
