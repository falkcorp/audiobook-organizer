// file: internal/metadata/junk_title.go
// version: 1.5.0
// guid: 4d7a2c91-3e6b-4f08-a1d5-8c2e9b7f4a13
// last-edited: 2026-10-01

package metadata

import (
	"regexp"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// JunkTitleKind says why a stored book title is not a title. The empty kind
// means the title is a real one.
type JunkTitleKind string

// Junk title kinds, in the order ClassifyJunkTitle tests them.
const (
	JunkNone JunkTitleKind = ""
	// JunkChapterOnly: only a chapter / part / disc / track position, in
	// arabic or roman numerals ("01", "Chapter 12", "Disc 2", "Part IV").
	JunkChapterOnly JunkTitleKind = "chapter_only"
	// JunkNarratorCredit: a narrator credit that replaced the title
	// ("read by narrator", "Read by Kate Reading", "Narrated by ...").
	JunkNarratorCredit JunkTitleKind = "narrator_credit"
	// JunkTrackTag: track 1's tag promoted to the book title ("Opening",
	// "Intro", "Opening Credits 1", "Big Finish Ident", "End Credits").
	JunkTrackTag JunkTitleKind = "track_tag"
	// JunkPlaceholder: the system's own or a tagger's placeholder ("Unknown
	// Title", "Unknown", "Unknown Album", "Untitled", "Unknown Author").
	JunkPlaceholder JunkTitleKind = "placeholder"
	// JunkRomanNumeral: a bare roman numeral ("IV", "XII") and nothing else.
	JunkRomanNumeral JunkTitleKind = "roman_numeral"
	// JunkNumberPrefix: a real title behind a track-number prefix
	// ("01 - Eldest", "003. The Hobbit", "07_Dune").
	JunkNumberPrefix JunkTitleKind = "number_prefix"
	// JunkPunctuationPrefix: a real title behind leading junk punctuation
	// ("- Eldest", "_Dune", "~ The Hobbit").
	JunkPunctuationPrefix JunkTitleKind = "punctuation_prefix"
)

// IsChapterKind reports whether the kind says "this is a chapter position":
// such a book is most likely one file of a bigger book.
func (k JunkTitleKind) IsChapterKind() bool {
	return k == JunkChapterOnly || k == JunkRomanNumeral
}

// HasRealTitleInside reports whether the stored title still contains the
// real title behind a junk prefix (StripJunkTitlePrefix recovers it).
func (k JunkTitleKind) HasRealTitleInside() bool {
	return k == JunkNumberPrefix || k == JunkPunctuationPrefix
}

// romanNumeralRe is a well-formed roman numeral from 1 to 3999. The empty
// match is excluded by the caller.
const romanNumeralPattern = `M{0,3}(?:CM|CD|D?C{0,3})(?:XC|XL|L?X{0,3})(?:IX|IV|V?I{0,3})`

var (
	romanNumeralRe = regexp.MustCompile(`^` + romanNumeralPattern + `$`)
	// romanChapterRe is a chapter position spelled in roman numerals:
	// "Chapter IV", "Part II", "Book III" is NOT here (a "Book 3" title is a
	// real title of a series entry and stays).
	romanChapterRe = regexp.MustCompile(`(?i)^(?:chapter|chap|ch|part|pt|disc|disk|cd|track)\.?[\s_\-]*([ivxlcdm]+)$`)
	// spelledChapterRe is a chapter position spelled out: "Chapter One",
	// "Part Twelve", "Disc Two".
	spelledChapterRe = regexp.MustCompile(`(?i)^(?:chapter|part|disc|disk|track)\s+(?:one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty)$`)
	// bareCreditRe: the title is a narrator credit with no name in it
	// ("read by narrator", "Narrated by the author"). A credit naming someone
	// is junk only when that someone is the book's narrator
	// (ClassifyJunkTitleFor): "Read by Moonlight" is a title.
	bareCreditRe = regexp.MustCompile(`(?i)^(?:read|narrated|performed)\s+by(?:\s+(?:the\s+)?(?:narrator|author|unknown(?:\s+narrator)?))?$`)
	// namedCreditRe splits "Read by <name>" for the narrator comparison.
	namedCreditRe = regexp.MustCompile(`(?i)^(?:read|narrated|performed)\s+by\s+(.+)$`)
	// trackTagRe: a track tag that names a segment of an audiobook, never a
	// book. "Introduction" is NOT here: it is the real title of some books.
	trackTagRe = regexp.MustCompile(`(?i)^(?:intro|opening|opening\s+(?:credits|titles|music)|(?:end|closing)\s+credits|credits|big\s+finish\s+ident)(?:\s*\d+)?$`)
	// placeholderRe: tagger and system placeholders, and the default names a
	// recorder or tagger gives a file it knows nothing about ("Audiobook 2",
	// "New Recording 4"): never a book's title, and their number is no
	// series position (metafetch.parseSearchTitle).
	placeholderRe = regexp.MustCompile(`(?i)^(?:unknown(?:\s+(?:title|book|album|audiobook|artist|author|narrator))?|untitled|no\s+title|\[?untitled\]?|\[?unknown\]?|audiobook(?:\s+\d+)?|new\s+recording(?:\s+\d+)?)$`)
	// numberPrefixRe: 1-3 digits then a separator and the real title. The
	// separator is required ("1 Corinthians" and "7 Habits" are titles). A
	// hyphen run counts only with whitespace on both sides, so "10-Minute
	// Toughness", "1-2-3 Magic", "4-3-2-1" and "9-11" keep their number; a
	// colon is not a separator ("3:10 to Yuma"); a dot counts only before a
	// space ("1.5"); four digits never match ("2001: A Space Odyssey", "1984").
	numberPrefixRe = regexp.MustCompile(`^#?\d{1,3}(?:\s+-+\s+|\s*[)\]]\s*|\.\s+)(\S.*)$`)
	// underscorePrefixRe: a ZERO-PADDED number, an underscore run, then a
	// letter ("07_Dune"). Unpadded, the number may be the title's ("3_Body").
	underscorePrefixRe = regexp.MustCompile(`^0\d{1,2}_+(\pL.*)$`)
	// zeroPaddedPrefixRe: a zero-padded track number then a space or a bare
	// hyphen run ("01 Eldest", "01-Eldest"). A leading zero is never part of
	// a real title's number, which is what keeps "10-Minute Toughness" and
	// "21-Day Sugar Detox" titles.
	zeroPaddedPrefixRe = regexp.MustCompile(`^0\d{1,2}(?:\s+|-+)(\S.*)$`)
	// punctuationPrefixRe: leading punctuation that no title starts with.
	// Quotes, apostrophes, brackets and "¿¡" are excluded ("'Salem's Lot"),
	// and so are dots touching a letter ("...And Justice for All").
	punctuationPrefixRe = regexp.MustCompile(`^(?:[-_~=+|/\\,;:*•·]+|\.+\s)\s*(\S.*)$`)
)

// romanWords are well-formed roman numerals that are also English words or
// common abbreviations; as a whole title they are words.
var romanWords = map[string]bool{"MIX": true, "DIV": true, "LIV": true, "MD": true, "DC": true, "CD": true,
	"DIM": true, "MI": true, "CM": true, "MC": true, "XL": true, "CC": true, "CV": true}

// isBareRoman reports whether t is only a roman numeral. It must be upper
// case, or lower case made only of i, v and x: "Mix", "Dim", "Liv", "Civil"
// and "mid" are words, not numerals.
func isBareRoman(t string) bool {
	if t == "" {
		return false
	}
	if t == strings.ToUpper(t) {
		return romanNumeralRe.MatchString(t)
	}
	if t == strings.ToLower(t) && strings.Trim(t, "ivx") == "" {
		return romanNumeralRe.MatchString(strings.ToUpper(t))
	}
	return false
}

// ClassifyJunkTitle decides whether a stored book title is really a title.
// It is the one classifier the Repairs lane's junk-title fixer detects with
// and checks every proposed replacement against, so a replacement can never
// be junk of another kind.
//
// Negatives that must stay titles are pinned by the test table: "Discworld",
// "1984", "11/22/63", "2001: A Space Odyssey", "Book 3", "I, Robot",
// "l'Étranger", "V for Vendetta", "'Salem's Lot", "...And Justice for All",
// "1 Corinthians", "Mix".
func ClassifyJunkTitle(title string) JunkTitleKind {
	t := strings.TrimSpace(title)
	switch {
	case IsChapterOnlyTitle(t):
		// The empty title is chapter-only too: a parser that stripped a
		// leading number handed "98.mp3" over as "".
		return JunkChapterOnly
	case romanChapterRe.MatchString(t) && isBareRoman(romanChapterRe.FindStringSubmatch(t)[1]):
		return JunkChapterOnly
	case spelledChapterRe.MatchString(t):
		return JunkChapterOnly
	case bareCreditRe.MatchString(t):
		return JunkNarratorCredit
	case trackTagRe.MatchString(t):
		return JunkTrackTag
	case placeholderRe.MatchString(t) || authorname.IsPlaceholderTitle(t):
		return JunkPlaceholder
	case len(t) >= 2 && !romanWords[strings.ToUpper(t)] && isBareRoman(t):
		// One letter ("I", "V", "X") is a title too often: Grafton's "X".
		return JunkRomanNumeral
	}
	// "1. John", "2 - Kings", "2-Peter": the number is part of a numbered
	// book's name, not a track prefix.
	if _, ok := util.NumberedBookSortForm(t); ok {
		return JunkNone
	}
	if _, ok := stripNumberPrefix(t); ok {
		return JunkNumberPrefix
	}
	if m := punctuationPrefixRe.FindStringSubmatch(t); m != nil {
		return JunkPunctuationPrefix
	}
	// A title that is nothing but punctuation and spaces.
	if strings.Trim(t, " -_~=+|/\\,;:*.•·#!?") == "" {
		return JunkPunctuationPrefix
	}
	return JunkNone
}

func stripNumberPrefix(t string) (string, bool) {
	if m := numberPrefixRe.FindStringSubmatch(t); m != nil {
		return strings.TrimSpace(m[1]), true
	}
	if m := underscorePrefixRe.FindStringSubmatch(t); m != nil {
		return strings.TrimSpace(m[1]), true
	}
	if m := zeroPaddedPrefixRe.FindStringSubmatch(t); m != nil {
		return strings.TrimSpace(m[1]), true
	}
	return "", false
}

// StripJunkTitlePrefix removes a track-number or punctuation prefix and
// returns what is left, when that is itself a real title. ok is false when
// the title has no such prefix or the remainder is junk too ("01 - 02").
func StripJunkTitlePrefix(title string) (string, bool) {
	t := strings.TrimSpace(title)
	if _, ok := util.NumberedBookSortForm(t); ok {
		return "", false
	}
	for range 3 { // "01 - - Eldest": at most a few stacked prefixes
		rest, ok := stripNumberPrefix(t)
		if !ok {
			if m := punctuationPrefixRe.FindStringSubmatch(t); m != nil {
				rest, ok = strings.TrimSpace(m[1]), true
			}
		}
		if !ok {
			break
		}
		t = rest
	}
	if t == strings.TrimSpace(title) || len([]rune(t)) < 2 || ClassifyJunkTitle(t) != JunkNone {
		return "", false
	}
	return t, true
}

// NarratorCreditName returns the name a "Read by <name>" / "Narrated by
// <name>" / "Performed by <name>" title credits, and whether the title has
// that shape. A bare credit with no name ("read by narrator") is not one: it
// names nobody. The search query builder reads it so "read by Cathfach
// (Erryn's World)" is searched as "Erryn's World" narrated by Cathfach; it
// shares namedCreditRe with ClassifyJunkTitleFor so the two cannot disagree on
// what a credit is.
func NarratorCreditName(title string) (string, bool) {
	t := strings.TrimSpace(title)
	if bareCreditRe.MatchString(t) {
		return "", false
	}
	m := namedCreditRe.FindStringSubmatch(t)
	if m == nil {
		return "", false
	}
	name := strings.TrimSpace(m[1])
	return name, name != ""
}

// ClassifyJunkTitleFor is ClassifyJunkTitle with the book's narrator names:
// "Read by Kate Reading" is a credit only on a book Kate Reading narrates.
func ClassifyJunkTitleFor(title string, narrators []string) JunkTitleKind {
	if k := ClassifyJunkTitle(title); k != JunkNone {
		return k
	}
	m := namedCreditRe.FindStringSubmatch(strings.TrimSpace(title))
	if m == nil {
		return JunkNone
	}
	for _, n := range narrators {
		if n = strings.TrimSpace(n); n != "" && strings.EqualFold(n, strings.TrimSpace(m[1])) {
			return JunkNarratorCredit
		}
	}
	return JunkNone
}
