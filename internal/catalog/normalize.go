// file: internal/catalog/normalize.go
// version: 1.0.0
// guid: 2c8e5a17-9f43-4b6d-8e20-7d1a3c5f9b42
// last-edited: 2026-10-01

// Package catalog is the author catalog (design
// .claude/notes/catalog-wanted-requests-design-2026-10-01.md, phase P1): the
// rules that turn provider products into catalog entries (edition grouping,
// sequence parsing, edition kind, author disambiguation) and the
// catalog.harvest-authors run that fills the store.
//
// Nothing in this package writes a book row, a book file, or anything under
// books/itunes/**. Its only writes go through database.CatalogStore.
package catalog

import (
	"regexp"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/authorjunk"
)

// HarvestKey is the per-author state key: the folded author NAME. It has to
// be computable before any request (it decides whether to make one), so it
// cannot be the author ASIN, which is only known after a lookup. Folding
// (authorjunk.FoldKey) makes "J.N. Chaney" and "J. N. Chaney" one author.
func HarvestKey(name string) string {
	return authorjunk.FoldKey(name)
}

// AuthorNameKey and AuthorASINKey are the two index keys an entry is filed
// under for each of its authors, so the read API can find an author by name
// even when the provider gave no ASIN, and by ASIN when names differ.
func AuthorNameKey(name string) string {
	k := authorjunk.FoldKey(name)
	if k == "" {
		return ""
	}
	return "name:" + k
}

// AuthorASINKey is the index key for an author ASIN.
func AuthorASINKey(asin string) string {
	a := strings.ToUpper(strings.TrimSpace(asin))
	if a == "" {
		return ""
	}
	return "asin:" + a
}

// SeriesKey is the index key for a series name.
func SeriesKey(name string) string {
	return authorjunk.FoldKey(name)
}

// editionMarkerPatterns is the FIXED list of edition markers stripped from a
// title before grouping (R6). Each must be an edition or packaging note,
// never part of a work's name, and each is anchored to a bracket or a
// trailing ": " / " - " clause so a title like "A Novel Idea" or "Full Cast
// of Characters" is never touched.
var editionMarkerPatterns = []*regexp.Regexp{
	// Bracketed notes: "(Unabridged)", "[Dramatized Adaptation]", "(A Novel)",
	// "(Full-Cast Edition)", "(BBC Radio 4 Full-Cast Dramatisation)".
	regexp.MustCompile(`(?i)\s*[\(\[]\s*(?:un)?abridged(?:\s+edition)?\s*[\)\]]`),
	regexp.MustCompile(`(?i)\s*[\(\[][^\)\]]*\bdramati[sz](?:ed|ation)\b[^\)\]]*[\)\]]`),
	regexp.MustCompile(`(?i)\s*[\(\[][^\)\]]*\bfull[\s-]+cast\b[^\)\]]*[\)\]]`),
	regexp.MustCompile(`(?i)\s*[\(\[]\s*a\s+novel\s*[\)\]]`),
	regexp.MustCompile(`(?i)\s*[\(\[]\s*(?:narrated|read|performed)\s+by\b[^\)\]]*[\)\]]`),
	// Trailing clauses after ": " or " - ".
	regexp.MustCompile(`(?i)\s*(?::|\s-|\s–|\s—)\s*(?:a\s+)?(?:bbc\s+(?:radio\s+\d*\s*)?)?(?:full[\s-]+cast\s+)?dramati[sz](?:ed\s+adaptation|ation)\s*$`),
	regexp.MustCompile(`(?i)\s*(?::|\s-|\s–|\s—)\s*(?:a\s+)?full[\s-]+cast(?:\s+(?:audio\s+)?(?:drama|production|recording|edition))?\s*$`),
	regexp.MustCompile(`(?i)\s*(?::|\s-|\s–|\s—)\s*a\s+novel\s*$`),
	regexp.MustCompile(`(?i)\s*(?::|\s-|\s–|\s—)\s*(?:un)?abridged(?:\s+edition)?\s*$`),
	regexp.MustCompile(`(?i)\s*(?::|\s-|\s–|\s—)\s*(?:narrated|read|performed)\s+by\s+.+$`),
}

// StripEditionMarkers removes the edition markers above, repeatedly, so a
// title carrying two ("X: A Novel (Unabridged)") loses both.
func StripEditionMarkers(title string) string {
	t := strings.TrimSpace(title)
	for range 4 {
		before := t
		for _, re := range editionMarkerPatterns {
			t = strings.TrimSpace(re.ReplaceAllString(t, ""))
		}
		if t == before {
			break
		}
	}
	return t
}

// NormalizeTitle is the grouping form of a title: edition markers stripped,
// then folded (case, accents, punctuation and spacing removed).
func NormalizeTitle(title string) string {
	return authorjunk.FoldKey(StripEditionMarkers(title))
}

// PrimaryAuthorIdentity is the first credited author's identity: the author
// ASIN when the provider gave one, else the folded name, else "".
func PrimaryAuthorIdentity(authorName, authorASIN string) string {
	if k := AuthorASINKey(authorASIN); k != "" {
		return k
	}
	return AuthorNameKey(authorName)
}

// EditionGroupKey is the R6 grouping key: primary author identity + the
// normalized title. Series and sequence are NOT part of it. It returns ""
// when either half is missing: an entry with no author identity is never
// grouped on its title alone, it gets a singleton group.
//
// Known gap (reported, not fixed in P1): an edition whose first author
// carries an ASIN and another edition of the same title whose author does not
// produce different keys ("asin:X|t" vs "name:n|t") and so different groups.
func EditionGroupKey(primaryAuthorName, primaryAuthorASIN, title string) string {
	id := PrimaryAuthorIdentity(primaryAuthorName, primaryAuthorASIN)
	t := NormalizeTitle(title)
	if id == "" || t == "" {
		return ""
	}
	return id + "|" + t
}
