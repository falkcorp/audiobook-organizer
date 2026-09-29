// file: internal/metafetch/search_author.go
// version: 1.1.0
// guid: b2296132-2b4b-426c-9f36-b3031543cec6
// last-edited: 2026-09-28

package metafetch

import (
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/database"
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
// Every place that sends or hashes a batch author hint uses this one
// function, so the hint the providers see, the hint the candidate cache is
// hashed with (hashSearchInputs) and the hint the apply gate re-checks
// (ValidateCachedIdentityForBook) cannot drift apart.
func SearchAuthorHint(name string) string {
	n := strings.TrimSpace(name)
	if authorname.IsPlaceholderAuthor(n) || IsGarbageValue(n) {
		return ""
	}
	return n
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
