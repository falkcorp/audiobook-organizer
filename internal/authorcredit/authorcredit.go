// file: internal/authorcredit/authorcredit.go
// version: 1.4.4
// guid: 7000d1fc-e16c-47e1-bb94-6180fe3ec1de
// last-edited: 2026-10-05

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
// between them. A creation path splits a combined credit ONLY into authors
// that already exist; it never creates an author from a split (new authors
// from a split come only through the owner-reviewed rows of
// maintenance.repair-combined-author-credits, class split_new_authors):
//
//   - a bracketed segment is never an author: it is stripped before the
//     split ("Dante King (Dragon Born)" is a series tag), so the splitter's
//     bracket branch is never used here;
//   - the rest is split with the shared splitter
//     (personname.SplitCompositeAuthorName); every part must pass the
//     publisher/role gate (CleanGate) and the caller's gate, must not be a
//     collective credit ("Full Cast"), and a credit naming a contributor
//     role ("(translator)") is not split; any number of parts is linked;
//   - a part whose name is also a book title or series name in the library
//     ("Dragon Born, Dante King", "Mistborn, Brandon Sanderson") is linked
//     ONLY on non-name evidence that it is a person (PersonEvidence: the
//     author is credited on a book in a DIFFERENT named series, a metadata
//     provider credited exactly that name to the book, or the authority
//     lists know the name as an author); otherwise it is dropped with a
//     logged reason and the other parts are linked. A title-named part is
//     never the first credit (the primary) unless it is the only one or its
//     evidence is STRONG: the authority lists hold it as an author from the
//     owner's own library (tier O) or from an author credit that carried
//     an Audible contributor ASIN (tier A) (owner decisions 2026-10-04 "usage check + never first" and
//     2026-10-05 "strong evidence may go first");
//   - and EVERY other part must already be an author record (by name, or by
//     alias when the store has aliases). Then those authors are credited in
//     order.
//
// Otherwise the credit is handled exactly as before this package existed: the
// whole string is looked up and created when missing. The one exception is
// ErrCombinedCredit: a whole string that is not an author yet, will not split
// safely, and whose every loosely split piece already IS an author is never
// created (it would be one more combined record); the caller treats it as "no
// author", the same as a junk name.
//
// Review of #3717 (2026-10-04) found the first version of this helper split
// on the bracket branch, gated parts with PrepareGate only and created every
// missing part: through the real scanner it credited "Dragon Born", "Star
// Wars", "LitForge Press" and "Full Cast" as authors, and an offline run over
// the 8,808 production author folders still showed cast lists, an
// illustrator and a label becoming new authors once those were gated. Hence
// the existing-authors-only rule.
package authorcredit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/falkcorp/audiobook-organizer/internal/authority"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
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

// ErrCombinedCredit is returned by Resolve for a credit that does not split
// safely, is not an author yet, and whose every loosely split piece already
// is a DIFFERENT existing author (a doubled name or an alias pair resolves to
// its one author instead). The caller leaves the book without that author, exactly as for a
// junk name; it must not create the whole string.
var ErrCombinedCredit = errors.New("authorcredit: combined credit of existing authors the splitter will not split")

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

// titleIndex is one store's library titles: the letters keys of every live
// book title and series name, and each series' name key by series ID (the
// outside-series evidence reads it).
type titleIndex struct {
	keys      map[string]bool
	seriesKey map[int]string
	built     time.Time
}

var (
	titleMu    sync.Mutex
	titleCache = map[any]*titleIndex{}
)

// titlesOf returns the title index of ts (every live book title and series
// name), cached per store for titleIndexTTL.
func titlesOf(ts TitleSource) (*titleIndex, error) {
	cacheable := reflect.TypeOf(ts).Kind() == reflect.Ptr
	if cacheable {
		titleMu.Lock()
		e := titleCache[ts]
		titleMu.Unlock()
		if e != nil && time.Since(e.built) < titleIndexTTL {
			return e, nil
		}
	}
	// Built outside the lock: a full series and book listing must not hold
	// up every other resolve. Two concurrent rebuilds both read; the later
	// swap wins, which is harmless.
	series, err := ts.GetAllSeries()
	if err != nil {
		return nil, fmt.Errorf("list series: %w", err)
	}
	books, err := ts.GetAllBooksCore(0, 0)
	if err != nil {
		return nil, fmt.Errorf("list books: %w", err)
	}
	keys := make(map[string]bool, len(series)+len(books))
	seriesKey := make(map[int]string, len(series))
	for i := range series {
		if k := LettersKey(series[i].Name); k != "" {
			keys[k] = true
			seriesKey[series[i].ID] = k
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
	idx := &titleIndex{keys: keys, seriesKey: seriesKey, built: time.Now()}
	if cacheable {
		titleMu.Lock()
		titleCache[ts] = idx
		titleMu.Unlock()
	}
	return idx, nil
}

// ResetTitleCache drops every cached title index (tests that add titles to
// a store after a first resolve).
func ResetTitleCache() {
	titleMu.Lock()
	defer titleMu.Unlock()
	titleCache = map[any]*titleIndex{}
}

// LettersKey is a name's letters and digits, lower-cased and NFC-normalized:
// "J.N. Chaney" and "J N Chaney" share one key. It is personname.LettersKey,
// kept here so no caller moves.
func LettersKey(s string) string { return personname.LettersKey(s) }

// looseSepRe separates the pieces of a credit list for the existence test:
// commas, semicolons, slashes, ampersands and a whole-word "and".
var looseSepRe = regexp.MustCompile(`(?i)\s*(?:[,;/&]|\band\b)\s*`)

// HasSeparator reports whether s still holds a list separator (",", ";",
// "/", "&" or a whole-word "and"): a name that does is not one person.
func HasSeparator(s string) bool { return looseSepRe.MatchString(s) }

// FlattenParts re-splits every part that still holds a list separator. The
// shared splitter tries one separator per credit, so "Travis Baldree, Sarah
// Lin/Travis Baldree" comes back as "Travis Baldree" and "Sarah Lin/Travis
// Baldree", and the second piece would be created as one author (prod dry
// run 2026-10-04). The pieces are normalized and kept in order; the callers
// apply their shape checks and de-duplication to the result.
func FlattenParts(parts []string) []string {
	var out []string
	for _, p := range parts {
		if !HasSeparator(p) {
			out = append(out, p)
			continue
		}
		for _, q := range LooseParts(p) {
			out = append(out, personname.NormalizeAuthorName(q))
		}
	}
	return out
}

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
	parts := FlattenParts(personname.SplitCompositeAuthorName(name))
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

// namesLibraryTitle reports whether name is the title of a book or series in
// the library. A store without the title listing, or a listing error, reads
// as false.
func namesLibraryTitle(store Store, name string) bool {
	ts, ok := database.AsCapability[TitleSource](store)
	if !ok {
		return false
	}
	titles, err := titlesOf(ts)
	if err != nil {
		return false
	}
	return titles.keys[LettersKey(name)]
}

// SingleWordParts is the split SplitNames refuses only because a piece is a
// single word ("Shirtaloon, Travis Deverell": the shared splitter wants every
// piece person-shaped, and a one-word pen name is not). It returns the loose
// pieces, cleaned by gate and de-duplicated by LettersKey, when:
//
//   - at least one piece is a single word, and every other piece is
//     person-shaped (personPiece, the test piecesVerdict uses);
//   - the credit is not one person written surname first ("Deverell,
//     Travis", OnePersonShape) and carries no bracket or role word;
//   - no piece reads as a title, is a collective credit, or fails CleanGate
//     or gate.
//
// It is a SHAPE test only. A single word is a name only when it already is an
// author or alias, or a provider credited it to the book (owner decision
// 2026-10-04): Resolve requires every piece to exist, and the combined-credit
// fixer asks for the provider credit. Nil when the credit does not qualify.
func SingleWordParts(name string, gate Gate) []string {
	if gate == nil {
		gate = PrepareGate
	}
	if strings.ContainsAny(name, "()[]") || roleRe.MatchString(name) || OnePersonShape(name) {
		return nil
	}
	loose := LooseParts(name)
	if len(loose) < 2 {
		return nil
	}
	single := false
	seen := map[string]bool{}
	var out []string
	for _, p := range loose {
		if IsNameSuffix(p) || personname.LooksLikeWorkTitle(p) || IsCollectiveCredit(p) || !personPiece(p) {
			return nil
		}
		if _, ok := CleanGate(p); !ok {
			return nil
		}
		clean, ok := gate(p)
		if !ok {
			return nil
		}
		if len(strings.Fields(clean)) == 1 {
			single = true
		}
		if k := LettersKey(clean); k != "" && !seen[k] {
			seen[k] = true
			out = append(out, clean)
		}
	}
	if !single || len(out) < 2 {
		return nil
	}
	return out
}

// IsMultiName reports whether a credit lists several names: it loosely splits
// into two or more pieces (brackets aside) and is not one person written
// surname first ("Le Guin, Ursula K.").
func IsMultiName(name string) bool {
	return len(LooseParts(StripBrackets(name))) >= 2 && !OnePersonShape(name)
}

// IsSingleWord reports whether a name is one word ("Shirtaloon").
func IsSingleWord(name string) bool { return len(strings.Fields(name)) == 1 }

// surnameParticles lead a surname written first ("Le Guin, Ursula K.", "De
// La Cruz, Maria").
var surnameParticles = map[string]bool{
	"le": true, "la": true, "von": true, "de": true, "della": true, "di": true, "du": true, "des": true,
	"da": true, "dos": true, "das": true, "der": true, "den": true, "ter": true, "ten": true, "o'": true,
}

// weakSurnameParticles are particles that are also common given names ("Ben
// Wolf, Luke Messa", "Al Gore", "Van Morrison"): they lead a surname only
// when another particle follows ("Van Der Berg, Jan Willem").
var weakSurnameParticles = map[string]bool{
	"van": true, "ben": true, "bin": true, "al": true, "el": true, "del": true, "mac": true, "mc": true,
	"st": true, "st.": true,
}

// particleLed reports whether a surname written first opens with a particle.
func particleLed(left []string) bool {
	if len(left) < 2 {
		return false
	}
	first := strings.ToLower(left[0])
	if surnameParticles[first] {
		return true
	}
	second := strings.ToLower(left[1])
	return weakSurnameParticles[first] && (surnameParticles[second] || weakSurnameParticles[second]) && len(left) >= 3
}

// suffixRe is a name suffix or degree written as its own comma piece ("Jr.",
// "Sr.", "III", "PhD", "M.A."): part of the name before it, not a person.
var suffixRe = regexp.MustCompile(`(?i)^(?:jr|sr|ii|iii|iv|phd|ph\.?\s?d|md|m\.?\s?a|m\.?\s?d|b\.?\s?a|m\.?\s?s|esq|dds|rn|ret)\.?$`)

// IsNameSuffix reports whether a credit piece is a suffix or degree.
func IsNameSuffix(s string) bool { return suffixRe.MatchString(strings.TrimSpace(s)) }

// initialRe is one initial ("A", "A.") or run of initials ("J.R.R.").
var initialRe = regexp.MustCompile(`^(?:\p{Lu}\.?)+$`)

// givenNameShaped reports whether a piece reads as given names written after
// a surname: initials only ("A. E.", "J. R. R."), or one name followed by
// initials ("Ursula K.", "George R. R.").
func givenNameShaped(s string) bool {
	f := strings.Fields(s)
	if len(f) == 0 {
		return false
	}
	start := 0
	if !initialRe.MatchString(f[0]) {
		if len(f) == 1 {
			return false
		}
		start = 1
	}
	for _, w := range f[start:] {
		if !initialRe.MatchString(w) {
			return false
		}
	}
	return true
}

// surnameFirst reports whether a two-piece comma credit is ONE person written
// surname first, or a name with its suffix:
//   - both sides one word ("King, Stephen");
//   - the right side is a suffix or degree ("Martin Luther King, Jr.", "Rob J.
//     Hayes, M.A.");
//   - the right side is given-name shaped ("Tolkien, J. R. R.", "Martin,
//     George R. R.", "Le Guin, Ursula K.");
//   - the left side starts with a surname particle ("De La Cruz, Maria"; a
//     particle that is also a given name, "Van", "Ben", "Al", only when
//     another particle follows: "Van Der Berg, Jan Willem").
//
// A left side that carries an initial is a full name, never a surname.
//
// "Travis Deverell, Shirtaloon" is two people: a two-word left side and a
// right side that is neither initials nor a particle-led surname.
func surnameFirst(name string, parts []string) bool {
	if len(parts) != 2 || strings.Count(name, ",") != 1 || strings.ContainsAny(name, ";/&") ||
		looseAndRe.MatchString(name) {
		return false
	}
	left, right := strings.Fields(parts[0]), strings.Fields(parts[1])
	if len(left) == 0 || len(right) == 0 {
		return false
	}
	// A surname never carries an initial: "Mashton XX, Mashton XY" and
	// "J. N. Chaney, A. B." have a full name on the left.
	leftInitial := false
	for _, w := range left {
		if initialRe.MatchString(w) {
			leftInitial = true
		}
	}
	switch {
	case len(left) == 1 && len(right) == 1:
		return true
	case IsNameSuffix(parts[1]):
		return true
	case leftInitial:
		return false
	case givenNameShaped(parts[1]):
		return true
	case particleLed(left):
		return true
	}
	return false
}

// looseAndRe is a whole-word "and" (a list, never a surname-first name).
var looseAndRe = regexp.MustCompile(`(?i)\band\b`)

// OnePersonShape reports whether a credit is one person written surname
// first or with a suffix (surnameFirst), so it must never be split.
func OnePersonShape(name string) bool {
	stripped := StripBrackets(name)
	return surnameFirst(stripped, LooseParts(stripped))
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

// FindByAlias returns the author whose alias is name, through the store's
// optional alias lookup (nil when the store has none or no alias matches).
func FindByAlias(store any, name string) (*database.Author, error) {
	af, ok := database.AsCapability[aliasFinder](store)
	if !ok {
		return nil, nil
	}
	a, err := af.FindAuthorByAlias(name)
	if err != nil {
		return nil, fmt.Errorf("look up author alias %q: %w", name, err)
	}
	return a, nil
}

// aliasFinder is the optional alias lookup a part may resolve through.
type aliasFinder interface {
	FindAuthorByAlias(aliasName string) (*database.Author, error)
}

// lookupExisting returns the existing author a part names: by name (the
// store's lookup ignores case and spacing), else by alias when the store has
// aliases; nil when there is none.
func lookupExisting(store Store, name string) (*database.Author, error) {
	a, err := store.GetAuthorByName(name)
	if err != nil {
		return nil, fmt.Errorf("look up author %q: %w", name, err)
	}
	if a != nil {
		return a, nil
	}
	if af, ok := database.AsCapability[aliasFinder](store); ok {
		a, err = af.FindAuthorByAlias(name)
		if err != nil {
			return nil, fmt.Errorf("look up author alias %q: %w", name, err)
		}
	}
	return a, nil
}

// piecesVerdict looks every loosely split piece of name up. A suffix or
// degree piece ("Jr.", "PhD") is dropped. It reports:
//   - same: every remaining piece resolves to ONE author (a doubled name, "A.
//     G. Riddle, A. G. Riddle", or a pen name with its alias, "Robert
//     Galbraith, J. K. Rowling"): that author is the credit;
//   - combined: two or more person-shaped pieces, every one an existing
//     author, and not all the same one: a combined record that must not be
//     created.
//
// A piece that is not person-shaped (personPiece) means the string is not a
// list of people: neither verdict.
// personPiece reports whether a credit piece can be one person: person-shaped
// (personname.LooksLikePersonName), or a single-word pen name of four or more
// letters ("Shirtaloon", "Zogarth"). Initials alone ("J. R. R.") are not.
func personPiece(p string) bool {
	n := personname.NormalizeAuthorName(p)
	if personname.LooksLikePersonName(n) {
		return true
	}
	f := strings.Fields(n)
	if len(f) != 1 || initialRe.MatchString(f[0]) {
		return false
	}
	letters := 0
	for _, r := range f[0] {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	// Case is not asked: a lowercase pen name ("pirateaba") is a name, and
	// every caller also requires an existing author (or, in the combined-credit
	// fixer, a provider credit) for the piece.
	return letters >= 4
}

func piecesVerdict(store Store, name string) (same *database.Author, combined bool, err error) {
	var pieces []string
	for _, p := range LooseParts(name) {
		if !IsNameSuffix(p) {
			pieces = append(pieces, p)
		}
	}
	if len(pieces) < 2 || OnePersonShape(name) {
		return nil, false, nil
	}
	for _, p := range pieces {
		if !personPiece(p) {
			return nil, false, nil
		}
	}
	var first *database.Author
	allSame := true
	for _, p := range pieces {
		a, err := lookupExisting(store, p)
		if err != nil {
			return nil, false, err
		}
		if a == nil {
			return nil, false, nil
		}
		if first == nil {
			first = a
		} else if a.ID != first.ID {
			allSame = false
		}
	}
	if allSame {
		return first, false, nil
	}
	return nil, true, nil
}

// PersonQuery is one credit part whose name is also a book title or series
// name in the library, put to a PersonEvidence source.
type PersonQuery struct {
	// Name is the part as credited ("Michael Anderle").
	Name string
	// Author is the existing author record the part names.
	Author database.Author
	// BookID is the book the credit is for; "" when it is not stored yet
	// (an import through Resolve or Lookup).
	BookID string
}

// EvidenceStrength is how much a piece of person evidence proves (owner
// decision 2026-10-05). Weak evidence links a title-named part but never as
// the primary; strong evidence may make it the primary.
type EvidenceStrength int

const (
	// EvidenceNone: the source has no evidence.
	EvidenceNone EvidenceStrength = iota
	// EvidenceWeak: an outside-series credit, a provider credit of the exact
	// name, or an authority-list author entry below tier O / A.
	EvidenceWeak
	// EvidenceStrong: the authority lists hold the name as an author from the
	// owner's own library (tier O) or from an author credit that carried an
	// Audible contributor ASIN (tier A).
	EvidenceStrong
)

// String names the strength for logs.
func (s EvidenceStrength) String() string {
	switch s {
	case EvidenceWeak:
		return "weak"
	case EvidenceStrong:
		return "strong"
	}
	return "none"
}

// Evidence is one PersonEvidence answer.
type Evidence struct {
	Strength EvidenceStrength
	// Detail describes the evidence for logs and reports.
	Detail string
}

// PersonEvidence is a source of non-name evidence that a credit part named
// like a book or series is a person (owner decision 2026-10-04, "usage check
// + never first"). The name alone proves nothing: "Dragon Born" and
// "Mistborn" are author records too, minted from junk credits.
//
// splitExisting asks the built-in sources (authorityEvidence,
// outsideSeriesEvidence, providerCreditEvidence) and, when the store offers
// it (resolved through decorators with database.AsCapability), the store
// itself, so a further source plugs in as a store capability without
// changing any caller.
type PersonEvidence interface {
	// PersonEvidence returns the evidence that q's part is a person, or an
	// Evidence with Strength EvidenceNone when this source has none. An
	// error means the source could not be read; unless another source has
	// evidence, the part is then not linked (fail closed).
	PersonEvidence(q PersonQuery) (Evidence, error)
}

// AuthoritySource is the store capability that hands authorcredit the
// authority lists (internal/authority). The server's store decorator offers
// it and answers authority.Empty() while authority_evidence_enabled is off or
// no snapshot is loaded, so production behaviour changes only with the flag
// on. It must not read the store per call: it returns an in-memory Snapshot.
type AuthoritySource interface {
	AuthorityLookup() authority.Lookup
}

// authorityEvidence is evidence from the authority lists. Strong when the
// name qualifies as an author (Entry.Qualifies: an owner override blocking
// the role, or cast_author-only credits, never count) AND that author role
// was observed at tier O (owner library or override) or tier A (an author
// credit with a contributor ASIN). The role's own tiers are read, never
// Entry.Tier (the best tier over every role) and never the entry's ASINs
// (Lookup.AuthorASINs): an entry carries the ASINs of every role it was
// credited in, so a narrator or cast member seen with an ASIN who is an
// author only at tier B would read as strong and go first (#3741 review
// B1). An author entry that qualifies on tier B alone is weak.
type authorityEvidence struct{ lookup authority.Lookup }

func (e authorityEvidence) PersonEvidence(q PersonQuery) (Evidence, error) {
	return gradeAuthorityAuthor(e.lookup, q.Name), nil
}

// gradeAuthorityAuthor grades the authority-list evidence that name is an
// author (see authorityEvidence). It cannot fail: the lists are in memory.
func gradeAuthorityAuthor(lookup authority.Lookup, name string) Evidence {
	if lookup == nil || !lookup.IsKnownPerson(name, authority.RoleAuthor) {
		return Evidence{}
	}
	p := lookup.Person(name)
	if p == nil {
		// A Lookup that answers IsKnownPerson without an entry (not Snapshot,
		// whose answer comes from the entry): known, tiers unknown, so weak.
		return Evidence{Strength: EvidenceWeak, Detail: fmt.Sprintf("the authority lists know %q as an author (no entry to read tiers from)", name)}
	}
	st := p.Roles[authority.RoleAuthor]
	switch {
	case st.ByTier[authority.TierO] > 0:
		return Evidence{Strength: EvidenceStrong, Detail: fmt.Sprintf("the authority lists hold %q as an author from the owner's library (tier O)", name)}
	case st.ByTier[authority.TierA] > 0:
		return Evidence{Strength: EvidenceStrong, Detail: fmt.Sprintf("the authority lists hold %q as an author credited with a contributor ASIN (tier A)", name)}
	}
	return Evidence{Strength: EvidenceWeak, Detail: fmt.Sprintf("the authority lists hold %q as an author (no tier O / A credit)", name)}
}

// authorBooksSource is the by-author book listing the outside-series
// evidence reads: one lookup per title-named part (a memdb index in
// production).
type authorBooksSource interface {
	GetBooksByAuthorIDWithRoleCore(authorID int) ([]database.BookCore, error)
}

// outsideSeriesEvidence is evidence (a), weak: the author is credited on a
// book whose series is a DIFFERENT named series than the one named like the
// part, the book is not titled like the part, and it is not the book being
// credited (a credit is not evidence for itself). A book with no series, or
// whose series has no name in the index, is NOT evidence (owner decision
// 2026-10-05): a junk record left on books whose series was never set would
// otherwise link.
type outsideSeriesEvidence struct {
	books  authorBooksSource
	series map[int]string // series ID -> name letters key (titleIndex)
}

func (e outsideSeriesEvidence) PersonEvidence(q PersonQuery) (Evidence, error) {
	books, err := e.books.GetBooksByAuthorIDWithRoleCore(q.Author.ID)
	if err != nil {
		return Evidence{}, fmt.Errorf("list the books of author %d: %w", q.Author.ID, err)
	}
	k := LettersKey(q.Name)
	for i := range books {
		b := &books[i]
		if b.ID == q.BookID || b.IsSoftDeleted() || LettersKey(b.Title) == k || b.SeriesID == nil {
			continue
		}
		if sk := e.series[*b.SeriesID]; sk == "" || sk == k {
			continue
		}
		return Evidence{Strength: EvidenceWeak,
			Detail: fmt.Sprintf("credited on book %s, in a different series than the one named %q", b.ID, q.Name)}, nil
	}
	return Evidence{}, nil
}

// HistorySource is the metadata change history a provider credit is read
// from.
type HistorySource interface {
	GetMetadataChangeHistory(bookID string, field string, limit int) ([]database.MetadataChangeRecord, error)
}

// providerHistoryLimit bounds the author history read per book.
const providerHistoryLimit = 200

// ProviderCredited returns the source of a metadata fetch that wrote exactly
// name (by letters key) as book bookID's author, or "" when none did. Only a
// whole fetched value counts: a provider's joined credit ("Shirtaloon,
// Travis Deverell") is the string the record came from, so reading a piece of
// it as evidence would prove nothing. Manual and AI-parse rows are not
// provider credits. A store without the history reads as no credit; a read
// error is returned. The combined-credit fixer's single-word rule and the
// title-named part rule here share it, so the two cannot drift.
func ProviderCredited(store any, bookID, name string) (string, error) {
	hs, ok := database.AsCapability[HistorySource](store)
	if !ok || bookID == "" {
		return "", nil
	}
	recs, err := hs.GetMetadataChangeHistory(bookID, database.HistoryFieldAuthor, providerHistoryLimit)
	if err != nil {
		return "", fmt.Errorf("read author history of %s: %w", bookID, err)
	}
	want := LettersKey(name)
	for _, rec := range recs {
		src := strings.ToLower(strings.TrimSpace(rec.Source))
		if rec.ChangeType != "fetched" || rec.NewValue == nil || src == "" || src == "manual" || strings.HasPrefix(src, "ai") {
			continue
		}
		var v string
		if json.Unmarshal([]byte(*rec.NewValue), &v) != nil {
			v = *rec.NewValue
		}
		if LettersKey(personname.StripByPrefix(v)) == want {
			return rec.Source, nil
		}
	}
	return "", nil
}

// providerCreditEvidence is evidence (b), weak: a metadata provider credited
// exactly the part's name to the book (ProviderCredited). A credit for a book
// not stored yet has no history.
type providerCreditEvidence struct{ store any }

func (e providerCreditEvidence) PersonEvidence(q PersonQuery) (Evidence, error) {
	src, err := ProviderCredited(e.store, q.BookID, q.Name)
	if err != nil || src == "" {
		return Evidence{}, err
	}
	return Evidence{Strength: EvidenceWeak, Detail: fmt.Sprintf("metadata provider %s credited %q to this book", src, q.Name)}, nil
}

// AuthorityWaiter is the store capability a plan uses to read the authority
// lists: unlike AuthoritySource.AuthorityLookup, which never blocks and
// answers authority.Empty() until a background load lands, it waits (bounded
// by ctx) for a load in flight or due, so a plan run right after
// authority_evidence_enabled is turned on reads the lists instead of an empty
// answer indistinguishable from "no list knows this name". ready is false
// when no snapshot is in hand: the flag is off, the load failed, or ctx ended
// first.
type AuthorityWaiter interface {
	AwaitAuthorityLookup(ctx context.Context) (lookup authority.Lookup, ready bool)
}

// AwaitAuthority returns the authority lists a plan reads and whether a real
// snapshot is in hand. A store offering AuthorityWaiter is waited on; one
// offering only AuthoritySource is read as is (ready when it answers anything
// but authority.Empty()); any other store answers authority.Empty(), false.
// A caller must not treat a name missing from a not-ready Lookup as evidence
// of anything.
func AwaitAuthority(ctx context.Context, store any) (authority.Lookup, bool) {
	if w, ok := database.AsCapability[AuthorityWaiter](store); ok {
		if l, ready := w.AwaitAuthorityLookup(ctx); !isNilLookup(l) {
			return l, ready
		}
		return authority.Empty(), false
	}
	return CurrentAuthority(store)
}

// CurrentAuthority is AwaitAuthority without the wait, for a path that must
// not block (a repairs apply holding the scan stand-down lease): the lists
// the store answers right now through AuthoritySource, and whether that is a
// real snapshot rather than authority.Empty(). A nil or typed-nil Lookup (a
// custom AuthoritySource returning a nil *authority.Snapshot) is not ready:
// authorityOf answers authority.Empty() for it.
func CurrentAuthority(store any) (authority.Lookup, bool) {
	l := authorityOf(store)
	return l, l != authority.Empty()
}

// isNilLookup reports whether l is nil or an interface holding a nil pointer
// (a typed-nil *authority.Snapshot). Such a Lookup compares unequal to both
// nil and authority.Empty(), yet calling it dereferences nil, so it must be
// read as "no lists" rather than as a real snapshot.
//
// The check is by kind, not by type: a Lookup implemented on any nilable
// kind (pointer, map, slice, func or chan) whose value is nil is read as
// "no lists" too. That includes a nil pointer to a type whose methods are
// nil-safe and would have answered: such a value is never treated as a real
// snapshot, so a custom Lookup must be non-nil to be read. Interface is not
// among the kinds: reflect.ValueOf takes the dynamic value out of l, so its
// Kind is never Interface.
func isNilLookup(l authority.Lookup) bool {
	if l == nil {
		return true
	}
	if s, ok := l.(*authority.Snapshot); ok {
		return s == nil
	}
	v := reflect.ValueOf(l)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return v.IsNil()
	}
	return false
}

// AuthorityPersonEvidence is the authority-list evidence that name is an
// author, graded exactly as credit resolution grades it: strong only for an
// author role observed at tier O or tier A; weak for an author entry that
// qualifies without either (tier B, owner decision 2026-10-05: it helps
// ranking but never puts a name first alone); none otherwise, which includes
// an author role seen only at tier C (authority.AuthorEvidenceRule never
// counts tier C) and a single tier-B observation.
func AuthorityPersonEvidence(lookup authority.Lookup, name string) Evidence {
	return gradeAuthorityAuthor(lookup, name)
}

// authorityOf returns the authority lists the store offers, or
// authority.Empty() when it offers none (or a nil one).
func authorityOf(store any) authority.Lookup {
	if as, ok := database.AsCapability[AuthoritySource](store); ok {
		if l := as.AuthorityLookup(); !isNilLookup(l) {
			return l
		}
	}
	return authority.Empty()
}

// evidenceSource is one PersonEvidence source and whether it can ever answer
// strong.
type evidenceSource struct {
	src         PersonEvidence
	canBeStrong bool
}

// evidenceSources returns the PersonEvidence sources for store: the
// authority lists first (an in-memory read, and the only built-in strong
// source), then the built-in weak ones it can serve, then the store's own
// when it has one (which may answer strong).
func evidenceSources(store Store, ti *titleIndex) []evidenceSource {
	out := []evidenceSource{{src: authorityEvidence{lookup: authorityOf(store)}, canBeStrong: true}}
	if bs, ok := database.AsCapability[authorBooksSource](store); ok {
		out = append(out, evidenceSource{src: outsideSeriesEvidence{books: bs, series: ti.seriesKey}})
	}
	out = append(out, evidenceSource{src: providerCreditEvidence{store: store}})
	if pe, ok := database.AsCapability[PersonEvidence](store); ok {
		out = append(out, evidenceSource{src: pe, canBeStrong: true})
	}
	return out
}

// personEvidence asks the sources about q and returns the strongest evidence
// found: it stops at the first strong answer, so a weak outside-series hit
// never hides a strong authority hit. Once it holds weak evidence it skips
// every source that can only answer weak (a by-author listing and a history
// read that could not change the outcome) but still asks one that can answer
// strong. With no evidence it returns the reason the part is not linked.
func personEvidence(sources []evidenceSource, q PersonQuery) (best Evidence, why string) {
	var errs []string
	for _, s := range sources {
		if best.Strength >= EvidenceWeak && !s.canBeStrong {
			continue
		}
		ev, err := s.src.PersonEvidence(q)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if ev.Strength > best.Strength {
			best = ev
		}
		if best.Strength == EvidenceStrong {
			break
		}
	}
	if best.Strength > EvidenceNone {
		return best, ""
	}
	why = "it is a book or series title in the library and nothing shows it is a person " +
		"(no book in a different named series, no provider credit of that name, no authority-list author entry)"
	if len(errs) > 0 {
		why += "; evidence could not be read: " + strings.Join(errs, "; ")
	}
	return Evidence{}, why
}

// droppedPart is a credit part splitExisting did not link, and why.
type droppedPart struct {
	Name   string
	Reason string
}

// splitExisting returns the existing authors a creation path credits for
// name, in order, or nil when it must not split it: a gate refuses (see the
// package comment) or a part that is not a library title would need a new
// author record.
//
// A part whose name is a book title or series name in the library is linked
// only on PersonEvidence; without it the part is dropped (returned in
// dropped) and the other parts are the credit, even a single one. A
// title-named part with only weak evidence is never first unless every
// linked part is such a part; one with strong evidence keeps its credit
// position, first included (strong evidence permits first, it does not
// promote).
// The title index is read once per resolve (cached, built outside locks) and
// the evidence costs one lookup per title-named part. A title index read
// error is returned (the caller takes the whole-string path); an evidence
// read error drops only that part.
//
// No part-count cap: a long credit list of existing authors is still its
// authors, in order (#3729 review, B1). The cap guards the creation of NEW
// authors, which only the combined-credit fixer's reviewed rows do.
func splitExisting(store Store, bookID, name string, gate Gate) (authors []database.Author, dropped []droppedPart, err error) {
	if roleRe.MatchString(name) || OnePersonShape(name) {
		return nil, nil, nil
	}
	parts := SplitNames(StripBrackets(name), gate)
	if parts == nil {
		parts = SingleWordParts(StripBrackets(name), gate)
	}
	if len(parts) < 2 {
		return nil, nil, nil
	}
	var ti *titleIndex
	if ts, ok := database.AsCapability[TitleSource](store); ok {
		ti, err = titlesOf(ts)
		if err != nil {
			return nil, nil, fmt.Errorf("read titles for the split check: %w", err)
		}
	}
	type linked struct {
		author database.Author
		// demote: a title-named part without strong evidence, never first
		// while a part without that flag is linked.
		demote bool
	}
	out := make([]linked, 0, len(parts))
	seen := map[int]bool{}
	var sources []evidenceSource
	for _, p := range parts {
		title := ti != nil && ti.keys[LettersKey(p)]
		a, err := lookupExisting(store, p)
		if err != nil {
			return nil, nil, err
		}
		if a == nil {
			if title {
				dropped = append(dropped, droppedPart{Name: p, Reason: "it is a book or series title in the library and no author record"})
				continue
			}
			return nil, nil, nil // a new author would be needed: never at import
		}
		demote := false
		if title {
			if sources == nil {
				sources = evidenceSources(store, ti)
			}
			ev, why := personEvidence(sources, PersonQuery{Name: p, Author: *a, BookID: bookID})
			if why != "" {
				dropped = append(dropped, droppedPart{Name: p, Reason: why})
				continue
			}
			demote = ev.Strength < EvidenceStrong
		}
		if !seen[a.ID] {
			seen[a.ID] = true
			out = append(out, linked{author: *a, demote: demote})
		}
	}
	if len(out) == 0 || (len(dropped) == 0 && len(out) < 2) {
		return nil, dropped, nil
	}
	// Never first on weak evidence: the first part that is not demoted
	// becomes the primary; the others keep their credit order.
	if out[0].demote {
		for i := 1; i < len(out); i++ {
			if !out[i].demote {
				first := out[i]
				copy(out[1:i+1], out[:i])
				out[0] = first
				break
			}
		}
	}
	authors = make([]database.Author, len(out))
	for i := range out {
		authors[i] = out[i].author
	}
	return authors, dropped, nil
}

// Resolve returns the authors to credit for name, in credit order. name must
// already have passed the caller's own gate for the whole string (the callers
// log their refusals in their own words); each split part must pass CleanGate
// and gate (PrepareGate when nil).
//
// Order:
//  1. the whole string, when it is already an author ("Le Guin, Ursula K."),
//     unless it is a combined record whose parts all exist (then 2 links
//     them instead of the combined row);
//  2. a split, only into existing authors (splitExisting); a part named
//     like a book or series is linked only on PersonEvidence and never
//     first unless that evidence is strong, and when such a part is dropped
//     the remaining authors are the credit even if only one is left;
//  3. every piece the same author (a doubled name, a pen name and its
//     alias): that author;
//  4. every piece a different existing author: ErrCombinedCredit, never
//     created;
//  5. otherwise the whole string is created, as before this package.
//
// A failure of the optional checks (the title index, an alias lookup) fails
// OPEN to the whole-string path: it never fails the caller's save. A failure
// to read person evidence drops only the title-named part. A failure of the
// whole-string lookup or create is returned.
//
// Resolve is for a credit whose book is not stored yet (an import); a path
// that holds the book's ID uses ResolveBook, so a provider credit recorded
// for that book counts as person evidence.
func Resolve(store Store, name string, gate Gate) ([]database.Author, error) {
	return resolve(store, "", name, gate, true)
}

// ResolveBook is Resolve for the credit of the stored book bookID.
func ResolveBook(store Store, bookID, name string, gate Gate) ([]database.Author, error) {
	return resolve(store, bookID, name, gate, true)
}

// Lookup is Resolve without step 5: it never creates an author, and returns
// no authors when the credit names no existing one. For a path that did not
// create authors on a miss before this package (the file importer).
func Lookup(store Store, name string, gate Gate) ([]database.Author, error) {
	return resolve(store, "", name, gate, false)
}

func resolve(store Store, bookID, name string, gate Gate, create bool) ([]database.Author, error) {
	name = strings.TrimSpace(name)
	// "By: Brandon Sanderson" and "by Brandon Sanderson" name Brandon
	// Sanderson. The bare form is kept whole when "By ..." is a book or
	// series title in the library ("By Schism Rent Asunder"); the title
	// listing is read only for a name that opens with a byline. What a
	// byline leaves as one bare word ("By: Zork") is linked when it is an
	// author or alias already and never created: the creation gates refuse
	// that residue the same way (PrepareAuthorNameForCreation).
	if personname.HasByPrefix(name) && (personname.HasMarkedByPrefix(name) || !namesLibraryTitle(store, name)) {
		name = personname.NormalizeAuthorName(personname.StripByPrefix(name))
		if !strings.ContainsAny(name, " \t") {
			create = false
		}
	}
	if name == "" {
		return nil, nil
	}
	existing, err := store.GetAuthorByName(name)
	if err != nil {
		return nil, fmt.Errorf("look up author %q: %w", name, err)
	}
	split, dropped, serr := splitExisting(store, bookID, name, gate)
	if serr != nil {
		split, dropped = nil, nil // fail open: the whole-string path
	}
	// Debug, not Info: every rescan resolves the same credits and would
	// repeat the line. There is no batch resolve API to summarize at Info.
	for _, d := range dropped {
		logger.New("authorcredit").Debug("not crediting %q from the credit %q: %s",
			logger.SanitizeLogValue(d.Name), logger.SanitizeLogValue(name), logger.SanitizeLogValue(d.Reason))
	}
	// A dropped title-named part decides the split even when one author is
	// left: the whole-string path would read "Dragon Born, Dante King" as a
	// combined credit of two existing authors and credit no one.
	if len(split) >= 2 || (len(split) == 1 && len(dropped) > 0) {
		return split, nil
	}
	if existing != nil {
		return []database.Author{*existing}, nil
	}
	same, combined, verr := piecesVerdict(store, name)
	switch {
	case verr != nil:
		// fail open: treat as not a list of existing authors
	case same != nil:
		return []database.Author{*same}, nil
	case combined:
		return nil, ErrCombinedCredit
	}
	if !create {
		return nil, nil
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
