// file: internal/plugins/maintenance/junk_title_filters.go
// version: 1.0.3
// guid: 642f415f-8f15-4d76-83b6-8290d9c7029f
// last-edited: 2026-10-25

package maintenance

import (
	"regexp"
	"strings"
	"sync"
	"unicode"

	"github.com/falkcorp/audiobook-organizer/internal/authorjunk"
	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/personname"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// This file holds the string-level evidence rules of the junk-title fixer:
// what a proposed title may not look like, and when two titles name the same
// book. They exist because an Opus review of a prod plan (1,138 applicable
// rows, 2026-10-23) found that of the 624 rows proposed from a transcription,
// a filename, a folder or a series and number, 513 were wrong. The shapes
// below are the reviewer's, with the titles left out: every title in a comment
// or a test is invented.

// ---- series and edition tails ----

var (
	// seriesBookTailRe: "…, Book 2 of the Y series", "… Book Two of Y",
	// "…, Book 6 in the Y Series": the transcription window read on into the
	// publisher's series line. Everything from "Book N of/in" on is dropped.
	// A bare ", Book 9" is NOT matched: reviewers accepted titles that end so
	// ("Gorselby Lamps, Book 9"), and nothing in a bare number says it is not
	// part of the title.
	seriesBookTailRe = regexp.MustCompile(`(?i)[\s,;:.\-–]*\(?\bbook\s+(?:\d+|[ivx]+|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve)\s+(?:of|in)\b.*$`)
	// genreTailRe: ", a drowsy Quillfeather tale", ", a Tinker Spindle saga",
	// ", a novel": a comma, an article, and a descriptor ending in a genre word.
	genreTailRe = regexp.MustCompile(`(?i),\s+an?\s+[^,]{0,80}?\b(?:story|saga|novel|novelization|novella|adventure|tale|trilogy|series|romance|fantasy|epic|thriller|mystery|litrpg|lit\s?rpg|progression)\b.*$`)
	// seriesNameTailRe: ", The Brackle Cycle", ", The Foo Series": a comma and
	// the series' name.
	seriesNameTailRe = regexp.MustCompile(`(?i),\s+the\s+[^,]+?\s+(?:cycle|series|saga|trilogy|chronicles|sequence)\s*$`)
	// catalogTaglineRe: a marketing tagline a catalog appends as a subtitle,
	// ": A Brindlecombe Romance", " - A Drowsy Quillfeather Tale": a colon
	// or dash, an article, and a descriptor ending in a genre word. A subtitle
	// that is not shaped so (": Volume 1", ": The Salt Wars") is not matched.
	catalogTaglineRe = regexp.MustCompile(`(?i)\s*(?::|\s[-–—])\s+an?\s+[^:]{0,80}?\b(?:story|saga|novel|novelization|novella|adventure|tale|trilogy|series|romance|fantasy|epic|thriller|mystery|litrpg|lit\s?rpg|progression)\b.*$`)
)

// stripSeriesTail drops a series or genre tail a transcription (or any other
// free-text source) appended to the title: "Orchard Keep, Book Nine in Brackle
// Wars, a Tinker Spindle saga" is "Orchard Keep". It returns "" when nothing
// is left (a string that is only "Book One of X"). A string with none of the
// tails is returned trimmed and otherwise as it was.
func stripSeriesTail(s string) string {
	t := strings.TrimSpace(s)
	for range 3 {
		before := t
		for _, re := range []*regexp.Regexp{seriesBookTailRe, genreTailRe, seriesNameTailRe} {
			t = strings.TrimSpace(re.ReplaceAllString(t, ""))
		}
		t = strings.TrimRight(t, " ,;:-–")
		if t == before {
			break
		}
	}
	return t
}

// stripCatalogTagline drops a marketing tagline subtitle from a catalog
// title: "Orchard Keep 2: A Brindlecombe Romance" is "Orchard Keep 2". A
// title without one is returned trimmed and otherwise as it was.
func stripCatalogTagline(s string) string {
	t := strings.TrimSpace(s)
	if core := strings.TrimSpace(catalogTaglineRe.ReplaceAllString(t, "")); core != "" {
		return core
	}
	return t
}

// bookMarkerAlt is the N of a "Book N" marker: digits, a roman numeral, or a
// spelled number up to twelve.
const bookMarkerAlt = `(?:\d+|[ivx]+|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve)`

// seriesNamePattern is the regexp source for a series name: spaces are any
// whitespace, an apostrophe is optional (a transcription has none), a leading
// "The" is optional.
func seriesNamePattern(series string) string {
	name := strings.TrimSpace(series)
	if len(name) > 4 && strings.EqualFold(name[:4], "the ") {
		name = strings.TrimSpace(name[4:])
	}
	var sb strings.Builder
	sb.WriteString(`(?:the\s+)?`)
	for _, r := range name {
		switch {
		case unicode.IsSpace(r):
			sb.WriteString(`\s+`)
		case r == '\'' || r == '’' || r == '‘' || r == '`':
			sb.WriteString("['’‘`]?")
		default:
			sb.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	return sb.String()
}

// articleEndRe: a string that ends in a word no title ends in.
var articleEndRe = regexp.MustCompile(`(?i)\b(?:the|a|an|of|and|in|to|for|on|at|with|from|by|or)\s*$`)

// stripRecordedSeries drops the book's own recorded series from a free-text
// title, in the two shapes the 2026-10-23 review found slipping:
//
//   - a tail: "Gorselby Lamps Brackle Wars Book 3", "Gorselby Lamps, the Brackle
//     Wars, Book 3" is "Gorselby Lamps";
//   - a prefix: "Brackle Wars, Book 3, Gorselby Lamps", "Brackle Wars Book 3 Gorselby
//     Lamps" is "Gorselby Lamps".
//
// It needs the series' name, so it strips only the series the book is
// recorded in: a bare ", Book 9" with nothing to compare it against stays
// (reviewers accepted titles that end so), and so does "<Series>, Book 3"
// with nothing after the number, which is the book's whole title. What is
// left must be a title of its own: it has a letter, does not start with a
// digit, does not end in an article or a preposition (a series that merely
// ends the title: "Wrath of the" + "Marsh"), and is not the series again.
//
// A series can also END a real title: with series "Brackle Wars", "Dark Brackle
// Wars Book 3" is the title "Dark Brackle Wars" and book 3 of its series, not
// "Dark" followed by the series; with series "Quillmoor", "Second Quillmoor
// Book 3" is "Second Quillmoor", not "Second". Nothing in the text tells the
// two readings apart when the series follows the title after a plain space, and
// the book being judged is junk-titled, so no live title can vouch for its own
// real one. The tail form therefore needs an explicit separator ("," "." ";"
// ":" "-") between the title and the series name; a series that simply
// follows the title after a space is left in the text (the proposal check then
// refuses a title that still ends in "Book N"). Past that, isTitle (nil: none)
// says whether a string is a title: a live book's, or one the book's own other
// evidence (transcription, file name) spells. The tail form leaves the string
// alone when the title plus the series is one.
func stripRecordedSeries(s, series string, isTitle func(string) bool) string {
	t := strings.TrimSpace(s)
	if t == "" || strings.TrimSpace(series) == "" {
		return t
	}
	res := recordedSeriesRegexps(series)
	if res == nil {
		return t
	}
	usable := func(rest string) (string, bool) {
		rest = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(rest), " ,;:.-–"))
		if rest == "" || !strings.ContainsFunc(rest, unicode.IsLetter) || unicode.IsDigit([]rune(rest)[0]) ||
			articleEndRe.MatchString(rest) || junkLettersKey(rest) == junkLettersKey(series) ||
			bareBookTailRe.MatchString(rest) {
			return "", false
		}
		return rest, true
	}
	// tail: m[1] is everything before "Book N", m[2] the title it leaves.
	if m := res.tail.FindStringSubmatch(t); m != nil {
		if isTitle != nil && (isTitle(strings.TrimRight(strings.TrimSpace(m[1]), " ,;:.-–")) ||
			isTitle(strings.TrimSpace(m[2])+" "+strings.TrimSpace(series))) {
			return t
		}
		if rest, ok := usable(m[2]); ok {
			return rest
		}
	}
	if m := res.prefix.FindStringSubmatch(t); m != nil {
		if rest, ok := usable(m[1]); ok {
			return rest
		}
	}
	return t
}

// seriesEndsText reports whether s is text followed by the book's own series
// after a plain space and a book number ("Second Quillmoor Book 3" in series
// "Quillmoor"). It is the shape stripRecordedSeries cannot cut: the series may
// be the end of the title ("Second Quillmoor") or a tail on one ("Second"), and
// nothing in the text says which, so a proposal of the text as it stands would
// carry the series in the title and one that cut it might cut a real title.
// The book's whole title ("Quillmoor Book 3") is not this shape.
func seriesEndsText(s, series string) bool {
	if strings.TrimSpace(series) == "" {
		return false
	}
	res := recordedSeriesRegexps(series)
	return res != nil && res.bare.MatchString(strings.TrimSpace(s))
}

// recordedSeriesRes are the three regexps stripRecordedSeries and
// seriesEndsText build for one series name.
type recordedSeriesRes struct{ tail, prefix, bare *regexp.Regexp }

// recordedSeriesCache memoizes recordedSeriesRegexps by series name. The
// regexps depend on nothing but the name, stripRecordedSeries runs twice per
// book with a series on the Plan worker pool, and there are as many entries as
// the library has series. A name that does not compile is cached as nil.
var recordedSeriesCache sync.Map // string -> *recordedSeriesRes

// recordedSeriesRegexps returns the regexps for series, nil when they do not
// compile (the strip then leaves the text alone; nothing here panics on data).
func recordedSeriesRegexps(series string) *recordedSeriesRes {
	key := strings.TrimSpace(series)
	if v, ok := recordedSeriesCache.Load(key); ok {
		return v.(*recordedSeriesRes)
	}
	name := seriesNamePattern(key)
	tail, err1 := regexp.Compile(`(?i)^((.+?)\s*[,.;:\-–]\s*` + name + `)\s*[,.;:\-–]?\s*\bbook\s+` + bookMarkerAlt + `\b[\s.]*$`)
	prefix, err2 := regexp.Compile(`(?i)^` + name + `\s*[,.;:\-–]?\s*\bbook\s+` + bookMarkerAlt + `\b\s*(?:[,.;:\-–]+\s*|\s+)(.+)$`)
	// bare: text, then the series after a plain space (no separator), then
	// "Book N" and nothing else: the shape stripRecordedSeries leaves alone.
	bare, err3 := regexp.Compile(`(?i)^.*[^\s,.;:\-–]\s+` + name + `\s*[,.;:\-–]?\s*\bbook\s+` + bookMarkerAlt + `\b[\s.]*$`)
	var res *recordedSeriesRes
	if err1 == nil && err2 == nil && err3 == nil {
		res = &recordedSeriesRes{tail: tail, prefix: prefix, bare: bare}
	}
	v, _ := recordedSeriesCache.LoadOrStore(key, res)
	return v.(*recordedSeriesRes)
}

var (
	bookNumberRe    = regexp.MustCompile(`(?i)\bbook\s+(\d+|[ivx]+|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve)\b`)
	spelledNumbers  = map[string]int{"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12}
	romanNumeralVal = map[string]int{"i": 1, "ii": 2, "iii": 3, "iv": 4, "v": 5, "vi": 6, "vii": 7, "viii": 8, "ix": 9, "x": 10}
)

// bookNumberOf is the N of the first "Book N" in s (digits, a spelled number
// up to twelve, or a roman numeral up to X), 0 when there is none.
func bookNumberOf(s string) int {
	m := bookNumberRe.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	tok := strings.ToLower(m[1])
	if n, ok := spelledNumbers[tok]; ok {
		return n
	}
	if n, ok := romanNumeralVal[tok]; ok {
		return n
	}
	n := 0
	for _, r := range tok {
		if r < '0' || r > '9' || n > 100000 {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// ---- agreement ----

// hasSubtitleSeps: s is p followed by one of seps.
func hasSubtitleSeps(s, p string, seps []string) bool {
	if p == "" || len(s) <= len(p) || !strings.HasPrefix(s, p) {
		return false
	}
	rest := s[len(p):]
	for _, sep := range seps {
		if strings.HasPrefix(rest, sep) {
			return true
		}
	}
	return false
}

// junkSubtitleSeps are the separators that make "X<sep>…" a subtitle of X
// when the junk-title fixer weighs evidence. "," is not one of them: after a
// title it is, in this library, a series or book marker far more often than a
// subtitle ("Gorselby Lamps, The Brackle Cycle", "Gorselby Lamps, Book 2 of the
// Brackle cycle"), and counting it let a transcription of another book of the
// series agree with the folder of this one.
var junkSubtitleSeps = []string{":", " (", " - "}

// junkTranscriptionSeps are junkSubtitleSeps plus ",": what the conflict check
// between the path and a transcription accepts as "the same title, read on".
// A transcription window runs into whatever follows the title, a comma and
// the publisher's next words ("Gorselby Lamps, The Second Tide"), and a title
// the path names exactly is then the right one; only a book number that
// differs (junkEvidenceAgreesSeps) says it is another book of the series.
var junkTranscriptionSeps = []string{":", " (", " - ", ","}

// junkEvidenceAgrees reports whether cand names the same book as any of the
// evidence titles. It is titleAgreesWithAny with three differences, all from
// the 2026-10-23 review:
//
//   - series and genre tails ("…, Book 2 of the Y series", "…, a … story") are
//     dropped from both sides before they are compared;
//   - "," does not open a subtitle (junkSubtitleSeps);
//   - when both sides name a book number ("Book 2" against "Book 3") they
//     name different books, whatever else they share, and one such clash
//     among the evidence rules out the number-less titles too.
//
// titleAgreesWithAny is left as it was: the swapped title/author fixer weighs
// an author against a provider's title with it.
func junkEvidenceAgrees(cand string, evidence []string) bool {
	return junkEvidenceAgreesSeps(cand, evidence, junkSubtitleSeps)
}

// junkEvidenceAgreesSeps is junkEvidenceAgrees with the separators that open a
// subtitle chosen by the caller.
func junkEvidenceAgreesSeps(cand string, evidence []string, seps []string) bool {
	core := stripSeriesTail(cand)
	if util.TitleSortKey(dropApostrophes(core)) == "" {
		return false
	}
	cNum := bookNumberOf(cand)
	// An evidence title that names another book of the series than cand does
	// is a veto on every number-less agreement: "X, Book 2 of Y" does not
	// agree with a folder "X" when the stripped title says "X, Book 3".
	numberClash := false
	if cNum != 0 {
		for _, e := range evidence {
			if eNum := bookNumberOf(e); eNum != 0 && eNum != cNum {
				numberClash = true
			}
		}
	}
	for _, e := range evidence {
		ecore := stripSeriesTail(e)
		if util.TitleSortKey(dropApostrophes(ecore)) == "" {
			continue
		}
		c2, e2 := core, ecore
		if eNum := bookNumberOf(e); cNum != 0 && eNum != 0 {
			if cNum != eNum {
				continue
			}
			// Both name the same book of the series: the number is not what
			// tells them apart, so a bare ", Book 2" on one side ("X, Book 2
			// of the Y series" against "X, Book 2") is dropped from both.
			c2, e2 = stripBareBookTail(core), stripBareBookTail(ecore)
		} else if numberClash {
			continue
		}
		c := util.TitleSortKey(dropApostrophes(c2))
		k := util.TitleSortKey(dropApostrophes(e2))
		if c == "" || k == "" {
			continue
		}
		lc := strings.ToLower(strings.TrimSpace(dropApostrophes(c2)))
		le := strings.ToLower(strings.TrimSpace(dropApostrophes(e2)))
		if c == k || hasSubtitleSeps(lc, le, seps) || hasSubtitleSeps(le, lc, seps) {
			return true
		}
	}
	return false
}

// bareBookTailRe: a trailing ", Book 2" / " (Book Two)".
var bareBookTailRe = regexp.MustCompile(`(?i)[\s,;:.\-–]*\(?\bbook\s+(?:\d+|[ivx]+|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve)\)?\s*$`)

// stripBareBookTail drops a trailing "Book N" from s. Unlike stripSeriesTail it
// is only used where both sides being compared carry the same number.
func stripBareBookTail(s string) string {
	return strings.TrimSpace(bareBookTailRe.ReplaceAllString(s, ""))
}

// ---- transcription filters ----

const spelledNumberAlt = `(?:\d+|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve)`

var (
	// announcerRe: what a narrator or a studio says over the first seconds of
	// a recording, which a transcription takes for the title. Anchored at the
	// start, except "<X> presents, …" and "an unabridged recording of …",
	// which an ident places after a name.
	announcerRe = regexp.MustCompile(`(?i)^(?:` +
		`(?:an?|the)\s+(?:un)?abridged\s+(?:recording|edition|version|production|reading)\b` +
		`|.{1,80}?\bpresents\b[,:.]?\s+\S` +
		`|.*\bis\s+pleased\s+to\s+present\b` +
		`|you(?:'|\x{2019})?re\s+listening\s+to\b|you\s+are\s+listening\s+to\b|now\s+listening\s+to\b` +
		`|and\s+now\b` +
		`|this\s+(?:concludes|has\s+been|is\s+the\s+end)\b` +
		`|welcome\s+to\b` +
		`|side\s+(?:[a-d]|` + spelledNumberAlt + `)\b` +
		`|book\s+` + spelledNumberAlt + `(?:\s+(?:of|in)\b|\s*[.,]?\s*$)` +
		`|(?:episode|ep|volume|vol)\.?\s*` + spelledNumberAlt + `\b` +
		`)`)

	// sentenceLeadRe: a pronoun-led opening followed by a lowercase word
	// ("He turned toward the door"). Title Case ("She Wandered Quillow") and a
	// lone pronoun ("It") are not matched.
	sentenceLeadRe = regexp.MustCompile(`^(?:[Hh]e|[Ss]he|[Tt]hey|[Ii]t|[Ww]e|[Yy]ou|I|[Hh]is|[Hh]er|[Tt]heir)\s+\p{Ll}+`)
	// nameVerbRe: a capitalised word and a past-tense or auxiliary verb
	// ("Marta grabbed Oliver"). The article subjects are left out.
	nameVerbRe = regexp.MustCompile(`^\p{Lu}\p{Ll}+\s+(?:\p{Ll}+ed|was|were|had|said|felt|saw|knew|came|went|took|made|got|looked|turned|stood|sat|ran|fell|began|thought|heard|told|found|gave)\b`)
	// trailingStopRe: a full stop, an ellipsis or a comma at the end. A title
	// ends in "?" or "!" now and then; it does not end in a full stop.
	trailingStopRe = regexp.MustCompile(`(?:[.…,;]|\.\.\.)\s*$`)
	quoteRe        = regexp.MustCompile("[\"“”]")
)

// transcribedSentence reports whether c is a sentence of the text rather than
// a spoken title: it ends in a full stop or comma, carries a quotation mark,
// opens with a pronoun and a lowercase word, starts with a lowercase letter
// (the middle of a sentence), or is a name followed by a past tense verb.
// transcribedProse (length and sentence breaks) is the other half. "Brindle
// Quoxx", "Ox", "Kreel" and "Dorvins" are titles and pass.
func transcribedSentence(c string) bool {
	s := strings.TrimSpace(c)
	if trailingStopRe.MatchString(s) && !abbreviationEndRe.MatchString(s) {
		return true
	}
	return quoteRe.MatchString(s) || sentenceLeadRe.MatchString(s) || nameVerbRe.MatchString(s) || startsLowercase(s)
}

// startsLowercase: the first letter of s is lowercase. A spoken title is
// transcribed with a capital; one that is not is the middle of a sentence or
// a run of words with no title casing ("iron salt white lantern book five").
// The 2026-10-23 review: 14 rejected rows, no accepted one.
func startsLowercase(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return unicode.IsLower(r)
		}
		if unicode.IsDigit(r) {
			return false
		}
	}
	return false
}

// abbreviationEndRe: a title that ends in an abbreviation's full stop
// ("Dr.", "Vol.", an initial: "Bound to the Orchard, by J.").
var abbreviationEndRe = regexp.MustCompile(`(?i)(?:\b(?:dr|mr|mrs|ms|st|vol|no|jr|sr)|\b\p{L})\.\s*$`)

// ---- folder and filename filters ----

var (
	// trackTokenRe: a chapter or track position, a credit, a placeholder
	// author or album, a parenthesised part or "N of M", a release-group tag,
	// a trailing year in brackets, or a leading track name, ANYWHERE in a
	// folder or filename title: "41.Chapter 9 - 12) Tides - read by
	// narrator", "Tide Log 1 0 - Prologue + Chapter 1", "Unknown Album (3 12
	// 2014 …)", "Intro - Gorselby Lamps 2033", "Gorselby Lamps-Part (16)",
	// "Gorselby Lamps (1 of 5)", "Gorselby Lamps [XYZ]". chapterHeadingRe only
	// sees a heading at the start. A bare "Part 2" is not here: "The Salt Wind
	// Part 2" is a title. A bitrate ("64kbps") is not here either: a rip's
	// stem is judged by isWeakFileStem / weakStemVeto, which must still see it
	// to veto evidence.
	trackTokenRe = regexp.MustCompile(`(?i)\b(?:chapter|chap|ch|track|trk)[\s._\-]*` + spelledNumberAlt + `\b` +
		`|\bread\s+by\b|\bnarrated\s+by\b` +
		`|\bunknown\s+(?:author|album|artist|narrator)\b` +
		`|\b(?:part|pt)[\s._\-]*\(\s*\d+\s*\)` +
		`|[(\[]\s*\d+\s+of\s+\d+\s*[)\]]` +
		`|[(\[]\s*(?:19|20)\d{2}\s*[)\]]\s*$` +
		`|\[[A-Za-z0-9_.\- ]{2,24}\]` +
		`|^\s*(?:intro|introduction|recap|outro|prologue|epilogue|opening|closing|credits|preface|foreword|afterword)\s*[-–—:]`)

	// unknownPlaceholderRe: the placeholder a tagger writes when it has no
	// value ("Unknown Author", "Unknown Album").
	unknownPlaceholderRe = regexp.MustCompile(`(?i)\bunknown\s+(?:author|album|artist|narrator)\b`)

	// romanTrackRe: "Chapter XIII", "Track IV": a roman-numeral position.
	// Case-sensitive, so the words "civil" or "mix" after "Track" are not one.
	romanTrackRe = regexp.MustCompile(`\b(?:Chapter|CHAPTER|Track|TRACK)[\s._\-]*[IVXLC]{1,6}\b`)

	// readByCreditRe: a trailing narrator credit, with or without brackets:
	// "Gorselby Lamps (read by Some Narrator)", "Foreword - 1987 Foo - read by
	// narrator".
	readByCreditRe = regexp.MustCompile(`(?i)\s*(?:[-–—]\s*read\s+by\b[^()\[\]]*|[(\[]\s*read\s+by\b[^()\[\]]*[)\]])\s*$`)

	// readByLeadRe: a stem that is only an author credit: "read by X".
	readByLeadRe = regexp.MustCompile(`(?i)^\s*(?:read|narrated)\s+by\b`)

	// readBySepRe: the " - read by " that starts a filename's narrator credit.
	readBySepRe = regexp.MustCompile(`(?i) - read by `)

	// bareSectionRe: a title that is only the name of a section of a book.
	bareSectionRe = regexp.MustCompile(`(?i)^(?:prologue|epilogue|preface|foreword|afterword|afterward|introduction|intro|outro|interlude)$`)
)

// stripReadByCredit drops a trailing narrator credit from a folder or
// filename title, so the title it leaves is judged on its own: "Gorselby Lamps
// (read by Some Narrator)" is "Gorselby Lamps".
func stripReadByCredit(s string) string {
	t := strings.TrimSpace(s)
	for range 2 {
		next := strings.TrimSpace(readByCreditRe.ReplaceAllString(t, ""))
		if next == t {
			break
		}
		t = next
	}
	return t
}

// namePartStopWords end a "looks like a person" reading: a title has them
// where a name does not.
var namePartStopWords = map[string]bool{"the": true, "a": true, "an": true, "of": true, "and": true, "in": true,
	"to": true, "for": true, "on": true, "at": true, "with": true, "from": true, "by": true, "or": true,
	// Position words: "Part One", "Volume II", "Book Three" are title text, not a person.
	"part": true, "book": true, "volume": true, "vol": true, "episode": true, "chapter": true, "disc": true,
	"cd": true, "season": true, "series": true, "edition": true, "saga": true, "trilogy": true}

// looksLikePersonSegment reports whether a " - " segment has the shape of a
// person's name: personname.LooksLikePersonName, behind the title words
// (namePartStopWords) it has no stoplist for.
func looksLikePersonSegment(s string) bool {
	for _, w := range strings.Fields(s) {
		if namePartStopWords[strings.ToLower(w)] {
			return false
		}
	}
	return personname.LooksLikePersonName(s)
}

// endsWithAuthorCredit returns the trailing " - <segment>" of c when that
// segment is a person's name or a placeholder author: a known person of the
// book, an "Unknown Author", or a segment shaped like a name. The last is a
// refusal, not a strip: nothing proves "Bram Wickle" in "Dim Wharf - Bram
// Wickle" is not a subtitle, and a skipped row costs less than a wrong title.
func endsWithAuthorCredit(c string, isPerson func(string) bool) (string, bool) {
	i := strings.LastIndex(c, " - ")
	if i < 0 {
		return "", false
	}
	seg := strings.TrimSpace(c[i+3:])
	if seg == "" || positionLeadRe.MatchString(seg) {
		return "", false
	}
	if authorname.IsPlaceholderAuthor(seg) || (isPerson != nil && isPerson(seg)) || looksLikePersonSegment(seg) {
		return seg, true
	}
	return "", false
}

// ---- author identity ----

// junkStoredAuthor reports whether the stored author carries no identity: it
// is empty, a placeholder ("Unknown Author", "read by narrator"), or a name
// the author classifier flags as junk ("- Some Title (read by X)"). The
// fixer then cannot compare a provider's recorded author with it, cannot key
// a duplicate check on it, and must not strip it from a filename (a chapter
// label that landed in the author field is title text there).
func junkStoredAuthor(a string) bool {
	a = strings.TrimSpace(a)
	if a == "" || authorname.IsPlaceholderAuthor(a) {
		return true
	}
	return authorjunk.ClassifyName(a).Junk()
}

// personNameForms are the normalized spellings of a person's name: as given,
// and "Last, First" turned into "First Last".
func personNameForms(n string) []string {
	out := []string{util.NormalizeAuthor(n)}
	if last, first, ok := strings.Cut(n, ","); ok && !strings.Contains(first, ",") &&
		strings.TrimSpace(first) != "" && strings.TrimSpace(last) != "" {
		out = append(out, util.NormalizeAuthor(strings.TrimSpace(first)+" "+strings.TrimSpace(last)))
	}
	return out
}

// ---- series and number ----

// positionLeadRe: a segment that opens with a position word ("Volume II",
// "Part 3", "Book Two"): title text, never a person.
var positionLeadRe = regexp.MustCompile(`(?i)^(?:volume|vol|part|pt|book|disc|disk|cd|chapter|episode|season|series)\b`)

// positionWordRe: a word that places a file inside a book (disc, CD, track,
// part, chapter, volume, "N of M"). A number beside one is a position in the
// book, never the book's position in a series. "of" alone is no position: a
// series named "The Orb of Quallow" is a series. An underscore follows a
// position word in a file name ("CD_01"), where \b would not see the end of
// the word.
var positionWordRe = regexp.MustCompile(`(?i)\b(?:(?:disc|disk|cd|dvd|tape|side|track|part|pt|chapter|chap|ch)(?:\b|\d|_)|(?:volume|vol)\b|\d+\s*of\s*\d+)`)

// positionMarkerRe: a position word WITH its number ("Disc 04", "CD5", "CD-2",
// "Disc-02", "Part II", "Side_2", "Vol.III", "Side A", "3 of 12").
// positionWordRe also fires on the bare word, which is right for a stored title
// ("Part" alone is no title) but refuses real series that contain one ("Side
// Effects", "Track Marks", "Tape Deck Tales"). A series is refused only when
// the word carries a number: digits may follow the word directly or after a
// space, underscore, hyphen or dot ("CD-2", "Vol.3"); a roman numeral or a
// spelled number needs one of those separators ("Chi Alpha" is no "Ch I",
// "Disc-Cover" no "Disc C"); and a side is a single letter A to D standing
// alone ("Side A", "Side-B"; "Side Dish", "Side Effects" are not).
var positionMarkerRe = regexp.MustCompile(`(?i)\b(?:(?:disc|disk|cd|dvd|tape|side|track|part|pt|chapter|chap|ch|volume|vol)(?:[\s_.\-]*\d+|[\s_.\-]+(?:[ivxlc]+|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve))\b|side[\s_.\-]*[a-d]\b|\d+\s*of\s*\d+)`)
