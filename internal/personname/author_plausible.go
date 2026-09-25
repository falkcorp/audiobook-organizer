// file: internal/personname/author_plausible.go
// version: 1.0.0
// guid: 7b634b40-1fc8-4fcd-bf81-98dd562e7ebe
// last-edited: 2026-09-25

package personname

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// AuthorNameRejection names the rule that refused a candidate author name. The
// empty value means the name was accepted.
type AuthorNameRejection string

// Rejection reasons returned by IsPlausibleAuthorName. They are stable strings so
// callers can log them and tests can pin which rule fired.
const (
	RejectEmpty         AuthorNameRejection = "empty"
	RejectTooLong       AuthorNameRejection = "too_long"
	RejectHTMLEntity    AuthorNameRejection = "html_entity"
	RejectCopyright     AuthorNameRejection = "copyright"
	RejectStoryCount    AuthorNameRejection = "story_count"
	RejectEraTag        AuthorNameRejection = "era_tag"
	RejectLeadingPunct  AuthorNameRejection = "leading_punctuation"
	RejectEditionMarker AuthorNameRejection = "edition_marker"
	RejectStructural    AuthorNameRejection = "structural_word"
	RejectReadBy        AuthorNameRejection = "read_by"
	RejectTimecode      AuthorNameRejection = "timecode_or_track"
	RejectPureNumber    AuthorNameRejection = "pure_number"
	RejectPlaceholder   AuthorNameRejection = "placeholder"
	RejectPositional    AuthorNameRejection = "positional_artifact"
)

// MaxAuthorNameRunes is the longest author name IsPlausibleAuthorName accepts.
// The longest real credit in the production author table on 2026-09-25 was well
// under this; everything above it was a whole filename or a tag dump.
const MaxAuthorNameRunes = 80

var (
	// "&#169", "&#169;", "&#xA9;", "&amp;" -- entity shrapnel from tags and HTML.
	htmlEntityRe = regexp.MustCompile(`(?i)&#x?[0-9a-f]+;?|&[a-z]{2,8};`)
	// "(c) 2001 Stephen Hawking", "©2013 by …", "℗ 2019", "(P)2019", "Copyright".
	copyrightRe = regexp.MustCompile(`(?i)\(c\)|©|℗|\(p\)\s*\d{4}|\bcopyright\b|all rights reserved`)
	// "13 short stories", "4 novellas", "3 Books", "Five Novels".
	storyCountRe = regexp.MustCompile(`(?i)^(\d+|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|twenty)\s+(short\s+)?(stories|novellas|novelettes|novels|books|tales|volumes)\b`)
	// Star Wars era tags: "14 BBY", "0BBY", "45 ABY", "Lords of the Sith (14 BBY)".
	eraTagRe = regexp.MustCompile(`(?i)\b\d+\s*(bby|aby)\b`)
	// "(Unabridged)", "Unabridged", "Abridged" anywhere in the string.
	editionMarkerRe = regexp.MustCompile(`(?i)\b(un)?abridged\b`)
	// "read by narrator", "Read by Robin Sachs", "narrated by …", "performed by …".
	readByRe = regexp.MustCompile(`(?i)\b(read|narrated|performed)\s+by\b`)
	// Timecodes and track numbering embedded anywhere in the string:
	// "Lords of the Sith_418m_07s_99h", "000m_00s", "12:34", "Track01", "Disc 2 Track 3".
	timecodeAnyRe = regexp.MustCompile(`(?i)\d+m_?\d+s\b|_\d+m(_|\b)|\b\d{1,3}:\d{2}\b|^(track|trk|disc|disk|cd)\s*\d+`)
)

// structuralLabels are book-structure words that are never an author on their
// own, and never an author when followed only by a number ("Chapter 3",
// "Part One", "Book IV", "Vol. 01").
var structuralLabels = map[string]bool{
	"epigraph": true, "prologue": true, "epilogue": true, "introduction": true,
	"intro": true, "preface": true, "foreword": true, "forward": true,
	"afterword": true, "appendix": true, "acknowledgments": true,
	"acknowledgements": true, "dedication": true, "contents": true,
	"chapter": true, "part": true, "book": true, "volume": true, "vol": true,
	"vol.": true, "disc": true, "disk": true, "cd": true, "track": true,
	"episode": true, "section": true, "side": true, "interlude": true,
	"opening credits": true, "end credits": true, "credits": true,
}

// numberWords lets "Part One" and "Book Twelve" count as numbered labels.
var numberWords = map[string]bool{
	"one": true, "two": true, "three": true, "four": true, "five": true,
	"six": true, "seven": true, "eight": true, "nine": true, "ten": true,
	"eleven": true, "twelve": true, "thirteen": true, "fourteen": true,
	"fifteen": true, "sixteen": true, "seventeen": true, "eighteen": true,
	"nineteen": true, "twenty": true,
}

var romanNumeralRe = regexp.MustCompile(`(?i)^[ivxlc]+$`)

// placeholderNames are literal non-answers. "Unknown Author" is in this list on
// purpose: a PARSED "Unknown Author" is not an author. The canonical fallback row
// (database.UnknownAuthorName) is exempted by the store itself, which is the only
// place that is allowed to mint it.
var placeholderNames = map[string]bool{
	"unknown": true, "unknown author": true, "unknown artist": true,
	"various": true, "various artists": true, "various authors": true,
	"n/a": true, "na": true, "none": true, "null": true, "nil": true,
	"undefined": true, "audiobook": true, "audiobooks": true, "narrator": true,
	"author": true, "no author": true, "tbd": true, "untitled": true,
}

// IsPlausibleAuthorName is THE shared creation gate for author rows. It reports
// whether name may be stored as an author and, when it may not, which rule
// refused it.
//
// It judges the string as given; it does not salvage. Callers that hold a raw
// tag or parse result should run PrepareAuthorNameForCreation (or the stricter
// CleanAuthorNameForCreation), which strips positional numbering and then
// applies this gate to the residue. database.PebbleStore.CreateAuthor calls this directly as the
// last line of defence, so every creation path is covered even when a caller
// forgets the clean step.
//
// Every rule below was checked against the full production author table
// (15,055 names, 2026-09-25) and tuned until the only names it refused were
// junk. Real single-word pen names (Zogarth, pirateaba, RavensDagger, Radclyffe,
// Jae) and initial-heavy names ("J. N. Chaney", "M.E. Thorne") pass: the gate
// rejects known junk SHAPES rather than requiring a person-shaped name.
func IsPlausibleAuthorName(name string) (bool, AuthorNameRejection) {
	s := strings.TrimSpace(name)
	if s == "" {
		return false, RejectEmpty
	}
	if utf8.RuneCountInString(s) > MaxAuthorNameRunes && !isShortPartCreditList(s) {
		return false, RejectTooLong
	}
	first, _ := utf8.DecodeRuneInString(s)
	if !unicode.IsLetter(first) && !unicode.IsDigit(first) {
		// "- Epigraph", "+Brandon Sanderson", "[PZG]", "_static", "(c) …".
		// Checked before the copyright rule only for ordering; either refuses.
		if copyrightRe.MatchString(s) {
			return false, RejectCopyright
		}
		if htmlEntityRe.MatchString(s) {
			return false, RejectHTMLEntity
		}
		return false, RejectLeadingPunct
	}
	if htmlEntityRe.MatchString(s) {
		return false, RejectHTMLEntity
	}
	if copyrightRe.MatchString(s) {
		return false, RejectCopyright
	}
	if storyCountRe.MatchString(s) {
		return false, RejectStoryCount
	}
	if eraTagRe.MatchString(s) {
		return false, RejectEraTag
	}
	if editionMarkerRe.MatchString(s) {
		return false, RejectEditionMarker
	}
	if readByRe.MatchString(s) {
		return false, RejectReadBy
	}
	if timecodeAnyRe.MatchString(s) {
		return false, RejectTimecode
	}
	if isPureNumber(s) {
		return false, RejectPureNumber
	}
	lower := strings.ToLower(s)
	if placeholderNames[lower] {
		return false, RejectPlaceholder
	}
	if isStructuralLabel(lower) {
		return false, RejectStructural
	}
	if IsPositionalArtifactName(s) {
		return false, RejectPositional
	}
	return true, ""
}

// isShortPartCreditList reports whether a long string is a comma or semicolon
// list of credits in which every part would itself pass the gate. Anthology
// credits ("Greg Bear, Gregory Benford, Ben Bova, David Brin, ...") run past
// MaxAuthorNameRunes but are a composite of real people, which the split tooling
// can repair; refusing them outright turns a fixable composite into a book with
// no author at all. The cap still applies to each part.
func isShortPartCreditList(s string) bool {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' })
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if ok, _ := IsPlausibleAuthorName(p); !ok {
			return false
		}
	}
	return true
}

// isPureNumber reports whether s is nothing but digits and numbering
// separators: "2", "02-25", "1-14", "0.5".
func isPureNumber(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool {
		return !strings.ContainsRune("0123456789.,-_/\\() \t#", r)
	}) < 0
}

// isStructuralLabel reports whether lower is a structural label alone, or a
// structural label followed only by a number, number word or roman numeral.
func isStructuralLabel(lower string) bool {
	if structuralLabels[lower] {
		return true
	}
	fields := strings.Fields(lower)
	if len(fields) < 2 || len(fields) > 3 {
		return false
	}
	if !structuralLabels[fields[0]] {
		return false
	}
	for _, f := range fields[1:] {
		f = strings.Trim(f, ".,:")
		if f == "" {
			continue
		}
		if !(isPureNumber(f) || numberWords[f] || romanNumeralRe.MatchString(f)) {
			return false
		}
	}
	return true
}
