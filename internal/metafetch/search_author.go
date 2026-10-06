// file: internal/metafetch/search_author.go
// version: 1.2.0
// guid: b2296132-2b4b-426c-9f36-b3031543cec6
// last-edited: 2026-10-06

package metafetch

import (
	"regexp"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// SearchAuthorHint returns name as the author a provider search is narrowed
// by, or "" when name carries no identity and must not be sent at all.
//
// "Unknown Author", "read by narrator", "Unknown Title" and the other
// placeholders (authorname.IsPlaceholderAuthor) are the system's own markers
// that leaked into author rows through path parsing. Sent as an author hint
// they ask a catalog for "Planet Hulk by Unknown Author", which either finds
// nothing or finds a book that happens to credit a real "Unknown"; searching
// by the title alone (plus the narrator and ASIN, when known) is strictly
// better. IsGarbageValue's list ("various", "n/a", ...) is dropped for the
// same reason.
//
// The text is cleaned first (cleanAuthorText): HTML entities, a "zz" sort
// prefix, a file-copy suffix ("_copy1", "_10-02"), an "(Unabridged)" note
// and bracketed release-group tags ("[XYZ]") are no part of any credit a
// catalog carries. The 2026-10-05 no-match census found 81 queries sent
// with a bracketed release tag and 19 with a "zz"-prefixed name as their
// author.
//
// Every place that sends or hashes a batch author hint uses this one
// function. The identity checks recompute the hash from the book's author
// FORMS (CurrentAuthorForms), which are raw names, so they compare against
// both the raw form and this cleaned one (cachedQueryMatches): a row the
// batch fetch hashed with the cleaned hint, and a row written before the
// cleaning with the raw name, both match the book's current author.
func SearchAuthorHint(name string) string {
	n := cleanAuthorText(name)
	if authorname.IsPlaceholderAuthor(n) || IsGarbageValue(n) {
		return ""
	}
	return n
}

var (
	// zzSortPrefixRe is a "zz" a library manager put in front of a name to
	// sort it last ("zzJane Example").
	zzSortPrefixRe = regexp.MustCompile(`^zz(\p{Lu})`)
	// copySuffixRe is a file-copy tool's suffix ("_copy1", " copy2") or a
	// chapter copy's disc-track one ("Some Long Book_10-02").
	copySuffixRe = regexp.MustCompile(`(?i)(?:[\s_]*copy\d+|_\d{1,2}-\d{1,2})$`)
	// bracketGroupRe is one square-bracket group.
	bracketGroupRe = regexp.MustCompile(`\[([^\[\]]*)\]`)
)

// cleanAuthorText strips what a credit carries that no catalog does (see
// SearchAuthorHint). Bracketed groups are release tags ("[XYZ]") unless one
// holds the only person-shaped name ("AudioHouse [Jane Example]"):
// the first person-shaped text, outside the brackets then inside them, is
// the author, and none is no author.
func cleanAuthorText(name string) string {
	n := metadata.NormalizeNameText(name)
	n = strings.TrimSpace(editionQualifierRe.ReplaceAllString(n, ""))
	n = zzSortPrefixRe.ReplaceAllString(n, "$1")
	n = strings.TrimSpace(copySuffixRe.ReplaceAllString(n, ""))
	if groups := bracketGroupRe.FindAllStringSubmatch(n, -1); groups != nil {
		rest := strings.Join(strings.Fields(bracketGroupRe.ReplaceAllString(n, " ")), " ")
		cands := []string{rest}
		for _, g := range groups {
			cands = append(cands, strings.TrimSpace(g[1]))
		}
		n = ""
		for _, c := range cands {
			if personShaped(c) {
				n = c
				break
			}
		}
	}
	return strings.TrimSpace(n)
}

// FetchCacheIdentity is database.MetadataSearchIdentity -- the stamp on a
// provider fetch-cache row -- keyed on the author a search actually sends
// (SearchAuthorHint), not the stored one. A row fetched while a placeholder
// author was still sent ("Planet Hulk by Unknown Author") then no longer
// matches the book's identity and is refetched instead of replayed until it
// expires; a real author's identity is unchanged (MetadataSearchIdentity
// normalizes the same trimmed name). Every fetch-cache read and write --
// the per-book search, the single-book fetch and the bulk fetch -- goes
// through this, so they agree on the stamp.
func FetchCacheIdentity(title, author string, asin, isbn13, isbn10 *string) string {
	return database.MetadataSearchIdentity(title, SearchAuthorHint(author), asin, isbn13, isbn10)
}
