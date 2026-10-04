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
//   - a bracketed segment is never an author: it is stripped before the
//     split ("Dante King (Dragon Born)" is a series tag, "Virgil
//     Knightley(Master Class)" a series tag on one of two authors), so the
//     splitter's bracket branch is never used here;
//   - the rest is split with the shared splitter
//     (personname.SplitCompositeAuthorName); every part must pass the
//     publisher/role gate (CleanGate) and the caller's gate, must not be a
//     collective credit ("Full Cast"), must not be the title of a book or the
//     name of a series in the library (nor may the whole credit), and there
//     may be at most MaxSplitParts of them (longer lists are anthologies and
//     cast lists); a credit naming a contributor role ("(translator)") is not
//     split;
//   - when anything refuses the split, the credit is handled as before (the
//     whole string is looked up) with one exception: a whole string that is
//     not an author yet and names several people is never created. That is a
//     combined record, and creating it would add another one; the caller
//     treats ErrCombinedCredit as "no author", the same as a junk name.
//
// Review of #3717 (2026-10-04) found the first version of this helper split
// on the bracket branch and gated parts with PrepareGate only: through the
// real scanner it credited "Dragon Born", "Star Wars", "LitForge Press" and
// "Full Cast" as authors, and minted a new combined record from
// "Annabelle Hawthorne, Virgil Knightley(Master Class)".
package authorcredit

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"
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

// ErrCombinedCredit is returned by Resolve for a credit that names several
// people, is not an author yet, and will not split safely. The caller leaves
// the book without that author, exactly as for a junk name; it must not
// create the whole string.
var ErrCombinedCredit = errors.New("authorcredit: combined credit the splitter will not split safely")

// MaxSplitParts is the most names a credit may be split into at creation:
// longer lists are anthologies and cast lists ("4-name narrator lists"), and
// the combined-credit fixer holds them for the same reason.
const MaxSplitParts = 3

// collectiveCredits are the letters keys of credits that name no person.
var collectiveCredits = map[string]bool{
	"fullcast": true, "cast": true, "fullcastproduction": true, "fullcastdramatization": true,
	"various": true, "variousauthors": true, "variousnarrators": true, "variousartists": true,
	"dramatized": true, "dramatization": true, "others": true, "etal": true, "andothers": true,
	"multipleauthors": true, "multiplenarrators": true, "uncredited": true,
}

// IsCollectiveCredit reports whether a credit names no person ("Full Cast",
// "Various Authors", "et al.").
func IsCollectiveCredit(s string) bool { return collectiveCredits[LettersKey(s)] }

// roleRe is a contributor role written into a credit ("Mikhail Yagupov
// (translator)", "Gardner Dozois - editor"): the credit names people who are
// not authors, so it is never split at creation.
var roleRe = regexp.MustCompile(`(?i)\b(?:translat\w*|editor|edited|illustrat\w*|foreword|introduction|narrat\w*|read by|contributor|adapt\w*)\b`)

// bracketRe matches a bracketed segment, with or without a space before it.
var bracketRe = regexp.MustCompile(`\s*[\(\[][^\)\]]*[\)\]]`)

// StripBrackets removes every bracketed segment ("(Dragon Born)",
// "[Unabridged]") from a credit and tidies the spacing.
func StripBrackets(s string) string {
	return strings.Join(strings.Fields(bracketRe.ReplaceAllString(s, " ")), " ")
}

// TitleSource is what the title check reads. A store that has it (resolved
// through decorators with database.AsCapability) gets the check; one that
// does not (a test fake) does not.
type TitleSource interface {
	GetAllSeries() ([]database.Series, error)
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
}

// titleIndexTTL bounds how long one store's title index is reused.
const titleIndexTTL = 5 * time.Minute

type titleIndex struct {
	keys  map[string]bool
	built time.Time
}

var (
	titleMu    sync.Mutex
	titleCache = map[any]*titleIndex{}
)

// titlesOf returns the letters keys of every live book title and series name
// of ts, cached per store for titleIndexTTL.
func titlesOf(ts TitleSource) (map[string]bool, error) {
	cacheable := reflect.TypeOf(ts).Kind() == reflect.Ptr
	titleMu.Lock()
	defer titleMu.Unlock()
	if cacheable {
		if e := titleCache[ts]; e != nil && time.Since(e.built) < titleIndexTTL {
			return e.keys, nil
		}
	}
	series, err := ts.GetAllSeries()
	if err != nil {
		return nil, fmt.Errorf("list series: %w", err)
	}
	books, err := ts.GetAllBooksCore(0, 0)
	if err != nil {
		return nil, fmt.Errorf("list books: %w", err)
	}
	keys := make(map[string]bool, len(series)+len(books))
	for i := range series {
		if k := LettersKey(series[i].Name); k != "" {
			keys[k] = true
		}
	}
	for i := range books {
		if books[i].IsSoftDeleted() {
			continue
		}
		if k := LettersKey(books[i].Title); k != "" {
			keys[k] = true
		}
	}
	if cacheable {
		titleCache[ts] = &titleIndex{keys: keys, built: time.Now()}
	}
	return keys, nil
}

// ResetTitleCache drops every cached title index (tests that add titles to
// a store after a first resolve).
func ResetTitleCache() {
	titleMu.Lock()
	defer titleMu.Unlock()
	titleCache = map[any]*titleIndex{}
}

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
// when it does not split safely:
//
//   - the shared splitter (personname.SplitCompositeAuthorName) refuses it;
//   - the splitter keeps fewer distinct names than the credit lists (its
//     slash branch drops a piece that is not person-shaped, which may be a
//     single-word pen name: splitting would silently lose that credit);
//   - the credit carries a bracket: the splitter's bracket branch reads
//     "Dante King (Dragon Born)" as two people (callers strip brackets
//     first, StripBrackets);
//   - a part reads as a work title (personname.LooksLikeWorkTitle: it leads
//     with an article or carries a series marker), the "A Dark and Drowning
//     Tide" shape the splitter's " and " branch would otherwise cut into the
//     authors "A Dark" and "Drowning Tide";
//   - a part is a collective credit ("Full Cast"), or fails CleanGate (a
//     publisher, a role credit) or gate;
//   - every part is one name ("A. G. Riddle, A. G. Riddle").
func SplitNames(name string, gate Gate) []string {
	if gate == nil {
		gate = PrepareGate
	}
	if strings.ContainsAny(name, "()[]") {
		return nil
	}
	parts := personname.SplitCompositeAuthorName(name)
	if len(parts) < 2 {
		return nil
	}
	listed := map[string]bool{}
	for _, p := range LooseParts(name) {
		listed[LettersKey(p)] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range parts {
		if personname.LooksLikeWorkTitle(p) || IsCollectiveCredit(p) {
			return nil
		}
		if _, ok := CleanGate(p); !ok {
			return nil
		}
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
	if len(out) < 2 || len(out) < len(listed) {
		return nil
	}
	return out
}

// surnameFirst reports the one shape of a two-piece comma credit that is one
// person: a bare surname, a comma, a given name ("King, Stephen"). Both sides
// one word: "Travis Deverell, Shirtaloon" is two people even though its
// right side is one word, and when both are existing authors it must not be
// created as one.
func surnameFirst(name string, parts []string) bool {
	return len(parts) == 2 && strings.Count(name, ",") == 1 && !strings.ContainsAny(name, ";/&") &&
		len(strings.Fields(parts[0])) == 1 && len(strings.Fields(parts[1])) == 1
}

// LooksCombined reports whether name is a credit of several people: the
// shared splitter splits it, or it loosely splits into two or more pieces of
// which every one is an author (exists reports that for one piece). A
// two-piece "Surname, First" is one person. For a caller that holds its own
// author index and cannot create on the spot (a dry run, a frozen snapshot).
func LooksCombined(name string, exists func(piece string) bool) bool {
	if len(personname.SplitCompositeAuthorName(name)) >= 2 {
		return true
	}
	parts := LooseParts(name)
	if len(parts) < 2 || surnameFirst(name, parts) {
		return false
	}
	for _, p := range parts {
		if !exists(p) {
			return false
		}
	}
	return true
}

// isMultiName reports whether name, its brackets stripped, lists two or more
// people: two or more list pieces, other than a bare "Surname, First".
func isMultiName(name string) bool {
	stripped := StripBrackets(name)
	parts := LooseParts(stripped)
	return len(parts) >= 2 && !surnameFirst(stripped, parts)
}

// splitForCreation returns the names a creation path may credit for name,
// or nil when it must not split it (see the package comment).
func splitForCreation(store Store, name string, gate Gate) ([]string, error) {
	if roleRe.MatchString(name) {
		return nil, nil
	}
	stripped := StripBrackets(name)
	parts := SplitNames(stripped, gate)
	if len(parts) < 2 || len(parts) > MaxSplitParts {
		return nil, nil
	}
	ts, ok := database.AsCapability[TitleSource](store)
	if !ok {
		return parts, nil
	}
	titles, err := titlesOf(ts)
	if err != nil {
		return nil, fmt.Errorf("read titles for the split check: %w", err)
	}
	// The whole credit a title ("Full Dark, No Stars", "David Starr, Space
	// Ranger") or any part a title or series ("Dragon Born"): not a list of
	// authors.
	if titles[LettersKey(stripped)] || titles[LettersKey(name)] {
		return nil, nil
	}
	for _, p := range parts {
		if titles[LettersKey(p)] {
			return nil, nil
		}
	}
	return parts, nil
}

// Resolve returns the authors to credit for name, in credit order. name must
// already have passed the caller's own gate for the whole string (the callers
// log their refusals in their own words); each split part must pass CleanGate
// and gate (PrepareGate when nil).
//
// A part the store refuses as implausible (database.ErrImplausibleAuthorName)
// is dropped; any other store error is returned. Resolve returns
// ErrCombinedCredit (and no authors) for a credit naming several people that
// does not split safely and is not an author already; see the package comment.
func Resolve(store Store, name string, gate Gate) ([]database.Author, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	parts, err := splitForCreation(store, name, gate)
	if err != nil {
		return nil, err
	}
	if len(parts) >= 2 {
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
	if isMultiName(name) {
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
