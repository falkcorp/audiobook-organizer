// file: internal/database/credits.go
// version: 1.0.0
// guid: 3cb12a4a-40b2-48b2-ad10-67e6e1ccdf9d
// last-edited: 2026-10-04

package database

import (
	"sort"
	"strings"
)

// Credit lists: the owner decision of 2026-10-04 makes a book's authors and
// narrators ordered lists of individual records (book_authors:<id>,
// book_narrators:<id>), position 0..n-1, even when there is one name. A joined
// string ("A, B and C") is built from the list only to write file tags or to
// display it; it is never stored as the truth. See
// docs/plans/2026-10-04-author-narrator-credit-lists-audit.md.
//
// This file holds the pure helpers: the joiners, the resolved-credit types
// and the position normalisation every credit write goes through. The store
// side (GetBookCredits, ModifyBookCredits, ModifyBookNarrators) is in
// credits_store.go.

// CreditedAuthor is one resolved row of a book's author credit list.
type CreditedAuthor struct {
	Author   Author `json:"author"`
	Role     string `json:"role"`
	Position int    `json:"position"`
}

// CreditedNarrator is one resolved row of a book's narrator credit list.
type CreditedNarrator struct {
	Narrator Narrator `json:"narrator"`
	Role     string   `json:"role"`
	Position int      `json:"position"`
}

// BookCredits is a book's resolved credit lists, each in position order
// (Position 0 first, contiguous). Rows whose author or narrator no longer
// resolves are left out.
type BookCredits struct {
	Authors   []CreditedAuthor   `json:"authors"`
	Narrators []CreditedNarrator `json:"narrators"`
}

// PrimaryAuthor returns the author at position 0, and false when the book
// credits no author.
func (c BookCredits) PrimaryAuthor() (Author, bool) {
	if len(c.Authors) == 0 {
		return Author{}, false
	}
	return c.Authors[0].Author, true
}

// PrimaryNarrator returns the narrator at position 0, and false when the book
// credits no narrator.
func (c BookCredits) PrimaryNarrator() (Narrator, bool) {
	if len(c.Narrators) == 0 {
		return Narrator{}, false
	}
	return c.Narrators[0].Narrator, true
}

// AuthorNames returns the credited authors' names in position order.
func (c BookCredits) AuthorNames() []string {
	out := make([]string, 0, len(c.Authors))
	for _, a := range c.Authors {
		out = append(out, a.Author.Name)
	}
	return out
}

// NarratorNames returns the credited narrators' names in position order.
func (c BookCredits) NarratorNames() []string {
	out := make([]string, 0, len(c.Narrators))
	for _, n := range c.Narrators {
		out = append(out, n.Narrator.Name)
	}
	return out
}

// cleanNames trims every name and drops the empty ones, keeping order.
func cleanNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// JoinCreditNames joins credited names into the form written to file tags
// and shown in the app: "A", "A and B", "A, B and C" (no serial comma).
// Names are trimmed and empty names dropped; no names gives "".
//
// The joined string is an OUTPUT. Nothing may split it back into a list:
// the ordered list travels beside it (the AUDIOBOOK_ORGANIZER_* custom tag,
// owner decision 2026-10-04) precisely because a name can itself contain
// "and" or a comma.
func JoinCreditNames(names []string) string {
	names = cleanNames(names)
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// JoinCreditNamesABS joins credited names with ", ", the form the ABS wire
// contract uses for authorName and narratorName. Names are trimmed and empty
// names dropped.
func JoinCreditNamesABS(names []string) string {
	return strings.Join(cleanNames(names), ", ")
}

// NormalizeBookAuthors returns rows in canonical form: ordered by Position
// (ties keep their stored order), one row per (author, role) pair (the
// first, i.e. the lowest position, wins), and Position renumbered 0..n-1.
// Rows with a non-positive AuthorID are dropped. The input slice is not
// modified.
//
// Ties keep stored order because production holds rows that an old copy
// path wrote all at position 0, followed by rows a later add-only apply
// appended at max+1 (the "A @0, B @0, A+B @1" shape found on 2026-10-04).
// Stored order is the order they were credited in, so A, B, A+B becomes
// positions 0, 1, 2.
//
// The same author may legitimately appear twice with DIFFERENT roles: an
// author who also narrates keeps an "author" row and a "narrator" row, and
// the swapped-title and combined-author fixers rely on that. Only an exact
// (author, role) repeat is dropped. memdb's book_authors primary index is
// {BookID, AuthorID}, so memdb holds one of such a pair; that existing gap
// is not changed here.
func NormalizeBookAuthors(rows []BookAuthor) []BookAuthor {
	sorted := make([]BookAuthor, 0, len(rows))
	for _, r := range rows {
		if r.AuthorID > 0 {
			sorted = append(sorted, r)
		}
	}
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Position < sorted[j].Position })
	type key struct {
		id   int
		role string
	}
	seen := make(map[key]bool, len(sorted))
	out := sorted[:0]
	for _, r := range sorted {
		k := key{r.AuthorID, r.Role}
		if seen[k] {
			continue
		}
		seen[k] = true
		r.Position = len(out)
		out = append(out, r)
	}
	return out
}

// NormalizeBookNarrators is NormalizeBookAuthors for narrator credits: one row
// per (narrator, role) pair.
func NormalizeBookNarrators(rows []BookNarrator) []BookNarrator {
	sorted := make([]BookNarrator, 0, len(rows))
	for _, r := range rows {
		if r.NarratorID > 0 {
			sorted = append(sorted, r)
		}
	}
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Position < sorted[j].Position })
	type key struct {
		id   int
		role string
	}
	seen := make(map[key]bool, len(sorted))
	out := sorted[:0]
	for _, r := range sorted {
		k := key{r.NarratorID, r.Role}
		if seen[k] {
			continue
		}
		seen[k] = true
		r.Position = len(out)
		out = append(out, r)
	}
	return out
}
