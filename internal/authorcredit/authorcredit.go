// file: internal/authorcredit/authorcredit.go
// version: 1.0.0
// guid: 7000d1fc-e16c-47e1-bb94-6180fe3ec1de
// last-edited: 2026-10-04

// Package authorcredit turns one author credit string ("J.N. Chaney, Jonathan
// P. Brazee") into the author rows a book should be credited to, in order.
//
// WHY. Authors are stored one person per row and linked to a book through its
// book_authors credits (positions 0, 1, 2...); the combined "A, B" string is
// produced only when tags are written. Every creation path used to look the
// WHOLE credit string up and create it when missing, so a provider or tag
// credit naming two people minted one author row named after both of them. A
// census of production on 2026-10-04 found 1,597 such rows, each joining names
// that also exist as their own authors, credited on about 2,800 books.
//
// One helper, used by every creation path, so the split rule cannot drift
// between them:
//
//   - the credit is split with the shared splitter
//     (personname.SplitCompositeAuthorName) and every part must pass the
//     caller's creation gate; the parts are then resolved or created in order;
//   - when the splitter refuses, the credit is handled as before (the whole
//     string is looked up, and created when missing) with one exception: a
//     whole string that is not an author yet and whose every loosely split
//     part already IS an author is never created. That is a combined record
//     the splitter could not prove safe to split, and creating it would add
//     another one; the caller treats ErrCombinedCredit as "no author", the
//     same as a junk name.
package authorcredit

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/personname"
)

// Store is what resolution reads and writes.
type Store interface {
	GetAuthorByName(name string) (*database.Author, error)
	CreateAuthor(name string) (*database.Author, error)
}

// Gate cleans one name for creation and reports whether it may be stored.
type Gate func(raw string) (string, bool)

// PrepareGate is personname.PrepareAuthorNameForCreation as a Gate: the gate
// the scanner, metadata and provider paths use.
func PrepareGate(raw string) (string, bool) {
	s, why := personname.PrepareAuthorNameForCreation(raw)
	return s, why == ""
}

// CleanGate is personname.CleanAuthorNameForCreation (PrepareGate plus the
// publisher and role-credit refusal): the gate the iTunes importer and the
// file importer use.
func CleanGate(raw string) (string, bool) {
	return personname.CleanAuthorNameForCreation(raw)
}

// ErrCombinedCredit is returned by Resolve for a credit the splitter will not
// split, that is not an author yet, and whose every loosely split part already
// is one. The caller leaves the book without that author, exactly as for a
// junk name; it must not create the whole string.
var ErrCombinedCredit = errors.New("authorcredit: combined credit of existing authors the splitter will not split")

// LettersKey is a name's letters and digits, lower-cased and NFC-normalized:
// "J.N. Chaney" and "J N Chaney" share one key.
func LettersKey(s string) string {
	var b strings.Builder
	for _, r := range norm.NFC.String(strings.ToLower(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// looseSepRe separates the pieces of a credit list for the existence test:
// commas, semicolons, slashes, ampersands and a whole-word "and".
var looseSepRe = regexp.MustCompile(`(?i)\s*(?:[,;/&]|\band\b)\s*`)

// LooseParts splits a credit on every list separator, with no shape test and
// no de-duplication, in order: "A, B, A" is three parts. It is NOT a splitter
// to credit from (it cuts titles and "Surname, First" names alike); it only
// asks "is this string made of other authors' names".
func LooseParts(name string) []string {
	var out []string
	for _, p := range looseSepRe.Split(name, -1) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// SplitNames returns the names a credit splits into, each cleaned by gate and
// de-duplicated by LettersKey ("A, B, A, B" is A and B), in credit order; nil
// when the shared splitter will not split it or any part fails the gate. A
// credit whose parts are all one name ("A. G. Riddle, A. G. Riddle") is not a
// split: it returns nil.
func SplitNames(name string, gate Gate) []string {
	if gate == nil {
		gate = PrepareGate
	}
	parts := personname.SplitCompositeAuthorName(name)
	if len(parts) < 2 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range parts {
		clean, ok := gate(p)
		if !ok {
			return nil
		}
		k := LettersKey(clean)
		if k == "" {
			return nil
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, clean)
		}
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// surnameFirst reports the one shape of a two-piece comma credit that is one
// person: "King, Stephen" (a single word after the comma).
func surnameFirst(name string, parts []string) bool {
	return len(parts) == 2 && strings.Count(name, ",") == 1 && !strings.ContainsAny(name, ";/&") &&
		len(strings.Fields(parts[1])) == 1
}

// allPartsAreAuthors reports whether name loosely splits into two or more
// pieces of which every one is already an author row.
func allPartsAreAuthors(store Store, name string) (bool, error) {
	parts := LooseParts(name)
	if len(parts) < 2 || surnameFirst(name, parts) {
		return false, nil
	}
	for _, p := range parts {
		a, err := store.GetAuthorByName(p)
		if err != nil {
			return false, fmt.Errorf("look up author %q: %w", p, err)
		}
		if a == nil {
			return false, nil
		}
	}
	return true, nil
}

// Resolve returns the authors to credit for name, in credit order. name must
// already have passed the caller's own gate for the whole string (the callers
// log their refusals in their own words); each split part is cleaned by gate
// (PrepareGate when nil).
//
// A part the store refuses as implausible (database.ErrImplausibleAuthorName)
// is dropped; any other store error is returned. Resolve returns
// ErrCombinedCredit (and no authors) for a combined record the splitter will
// not split; see the package comment.
func Resolve(store Store, name string, gate Gate) ([]database.Author, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	if parts := SplitNames(name, gate); len(parts) >= 2 {
		out := make([]database.Author, 0, len(parts))
		seen := map[int]bool{}
		for _, p := range parts {
			a, err := getOrCreate(store, p)
			if errors.Is(err, database.ErrImplausibleAuthorName) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if a != nil && !seen[a.ID] {
				seen[a.ID] = true
				out = append(out, *a)
			}
		}
		return out, nil
	}
	existing, err := store.GetAuthorByName(name)
	if err != nil {
		return nil, fmt.Errorf("look up author %q: %w", name, err)
	}
	if existing != nil {
		return []database.Author{*existing}, nil
	}
	combined, err := allPartsAreAuthors(store, name)
	if err != nil {
		return nil, err
	}
	if combined {
		return nil, ErrCombinedCredit
	}
	a, err := store.CreateAuthor(name)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, fmt.Errorf("create author %q: no row", name)
	}
	return []database.Author{*a}, nil
}

func getOrCreate(store Store, name string) (*database.Author, error) {
	a, err := store.GetAuthorByName(name)
	if err != nil {
		return nil, fmt.Errorf("look up author %q: %w", name, err)
	}
	if a != nil {
		return a, nil
	}
	a, err = store.CreateAuthor(name)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, fmt.Errorf("create author %q: no row", name)
	}
	return a, nil
}

// Credits returns the book_authors rows for authors in order: the "author"
// role, positions 0..n-1.
func Credits(bookID string, authors []database.Author) []database.BookAuthor {
	out := make([]database.BookAuthor, 0, len(authors))
	for i, a := range authors {
		out = append(out, database.BookAuthor{BookID: bookID, AuthorID: a.ID, Role: "author", Position: i})
	}
	return out
}

// AddCredits returns cur with every author in authors that cur does not
// already credit appended, in order, in the "author" role, at positions after
// the highest one cur holds. Existing rows are never removed or reordered
// (author apply is add-only, owner decision 2026-09-14). A book whose primary
// lives only in its AuthorID (an empty junction) keeps it as the first
// credit, so the append cannot orphan it. added reports whether the result
// differs from cur.
func AddCredits(cur []database.BookAuthor, bookID string, primary *int, authors []database.Author) (next []database.BookAuthor, added bool) {
	next = append([]database.BookAuthor(nil), cur...)
	if len(next) == 0 && primary != nil && *primary > 0 && len(authors) > 0 {
		next = append(next, database.BookAuthor{BookID: bookID, AuthorID: *primary, Role: "author", Position: 0})
	}
	have := map[int]bool{}
	nextPos := 0
	for _, ba := range next {
		have[ba.AuthorID] = true
		if ba.Position >= nextPos {
			nextPos = ba.Position + 1
		}
	}
	for _, a := range authors {
		if have[a.ID] {
			continue
		}
		have[a.ID] = true
		next = append(next, database.BookAuthor{BookID: bookID, AuthorID: a.ID, Role: "author", Position: nextPos})
		nextPos++
	}
	return next, len(next) != len(cur)
}
