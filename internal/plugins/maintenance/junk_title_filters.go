// file: internal/plugins/maintenance/junk_title_filters.go
// version: 1.0.0
// guid: 642f415f-8f15-4d76-83b6-8290d9c7029f
// last-edited: 2026-10-23

package maintenance

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/falkcorp/audiobook-organizer/internal/authorjunk"
	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// This file holds the string-level evidence rules of the junk-title fixer:
// what a proposed title may not look like, and when two titles name the same
// book. They exist because an Opus review of a prod plan (1,138 applicable
// rows, 2026-10-23) found that of the 624 rows proposed from a transcription,
// a filename, a folder or a series and number, 513 were wrong. The shapes
// below are the reviewer's, with the titles left out.

// ---- series and edition tails ----

var (
	// seriesBookTailRe: "…, Book 2 of the Y series", "… Book Two of Y",
	// "…, Book 6 in the Y Series": the transcription window read on into the
	// publisher's series line. Everything from "Book N of/in" on is dropped.
	// A bare ", Book 9" is NOT matched: reviewers accepted titles that end so
	// ("The Dark Healer, Book 9"), and nothing in a bare number says it is not
	// part of the title.
	seriesBookTailRe = regexp.MustCompile(`(?i)[\s,;:.\-–]*\(?\bbook\s+(?:\d+|[ivx]+|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve)\s+(?:of|in)\b.*$`)
	// genreTailRe: ", a crunchy LitRPG adventure", ", a Starships Mage story",
	// ", a novel": a comma, an article, and a descriptor ending in a genre word.
	genreTailRe = regexp.MustCompile(`(?i),\s+an?\s+[^,]{0,80}?\b(?:story|saga|novel|novelization|novella|adventure|tale|trilogy|series|romance|fantasy|epic|thriller|mystery|litrpg|lit\s?rpg|progression)\b.*$`)
	// seriesNameTailRe: ", The Seaborn Cycle", ", The Foo Series": a comma and
	// the series' name.
	seriesNameTailRe = regexp.MustCompile(`(?i),\s+the\s+[^,]+?\s+(?:cycle|series|saga|trilogy|chronicles|sequence)\s*$`)
)

// stripSeriesTail drops a series or genre tail a transcription (or any other
// free-text source) appended to the title: "Sword of Mars, Book Two of
// Unarcana Rebellions, a Starships Mage story" is "Sword of Mars". It returns
// "" when nothing is left (a string that is only "Book One of X"). A string
// with none of the tails is returned trimmed and otherwise as it was.
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
// subtitle ("Stormborn, The Seaborn Cycle", "Eldest, Book 2 of the Inheritance
// cycle"), and counting it let a transcription of another book of the series
// agree with the folder of this one.
var junkSubtitleSeps = []string{":", " (", " - "}

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
		if c == k || hasSubtitleSeps(lc, le, junkSubtitleSeps) || hasSubtitleSeps(le, lc, junkSubtitleSeps) {
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
		`|book\s+` + spelledNumberAlt + `\s+(?:of|in)\b` +
		`|(?:episode|ep|volume|vol)\.?\s*` + spelledNumberAlt + `\b` +
		`)`)

	// sentenceLeadRe: a pronoun-led opening followed by a lowercase word
	// ("He turned toward the door"). Title Case ("She Walks in Beauty") and a
	// lone pronoun ("It") are not matched.
	sentenceLeadRe = regexp.MustCompile(`^(?:[Hh]e|[Ss]he|[Tt]hey|[Ii]t|[Ww]e|[Yy]ou|I|[Hh]is|[Hh]er|[Tt]heir)\s+\p{Ll}+`)
	// nameVerbRe: a capitalised word and a past-tense or auxiliary verb
	// ("Kate grabbed Bobby"). The article subjects are left out.
	nameVerbRe = regexp.MustCompile(`^\p{Lu}\p{Ll}+\s+(?:\p{Ll}+ed|was|were|had|said|felt|saw|knew|came|went|took|made|got|looked|turned|stood|sat|ran|fell|began|thought|heard|told|found|gave)\b`)
	// trailingStopRe: a full stop, an ellipsis or a comma at the end. A title
	// ends in "?" or "!" now and then; it does not end in a full stop.
	trailingStopRe = regexp.MustCompile(`(?:[.…,;]|\.\.\.)\s*$`)
	quoteRe        = regexp.MustCompile("[\"“”]")
)

// transcribedSentence reports whether c is a sentence of the text rather than
// a spoken title: it ends in a full stop or comma, carries a quotation mark,
// opens with a pronoun and a lowercase word, or is a name followed by a past
// tense verb. transcribedProse (length and sentence breaks) is the other half.
// "Going Postal", "It", "Cell" and "Misery" are titles and pass.
func transcribedSentence(c string) bool {
	s := strings.TrimSpace(c)
	if trailingStopRe.MatchString(s) && !abbreviationEndRe.MatchString(s) {
		return true
	}
	return quoteRe.MatchString(s) || sentenceLeadRe.MatchString(s) || nameVerbRe.MatchString(s)
}

// abbreviationEndRe: a title that ends in an abbreviation's full stop
// ("Dr.", "Vol.", an initial: "Bound to the Throne, by J.").
var abbreviationEndRe = regexp.MustCompile(`(?i)(?:\b(?:dr|mr|mrs|ms|st|vol|no|jr|sr)|\b\p{L})\.\s*$`)

// ---- folder and filename filters ----

var (
	// trackTokenRe: a chapter or track position, a credit, a placeholder
	// author or album, or a leading track name, ANYWHERE in a folder or
	// filename title: "35.Chapter 27 - 08) Villain - read by narrator",
	// "Volume 1 0 - Prologue + Chapter 1", "Unknown Album (3 13 2012 …)",
	// "Intro - Metro 2033". chapterHeadingRe only sees a heading at the start.
	// A bitrate ("64kbps") is not here: a rip's stem is judged by
	// isWeakFileStem / weakStemVeto, which must still see it to veto evidence.
	trackTokenRe = regexp.MustCompile(`(?i)\b(?:chapter|chap|ch|track|trk)[\s._\-]*` + spelledNumberAlt + `\b` +
		`|\bread\s+by\b|\bnarrated\s+by\b` +
		`|\bunknown\s+(?:author|album|artist|narrator)\b` +
		`|^\s*(?:intro|introduction|recap|outro|prologue|epilogue|opening|closing|credits|preface|foreword|afterword)\s*[-–—:]`)

	// readByCreditRe: a trailing narrator credit, with or without brackets:
	// "The Green Mile (read by X)", "Afterword - 2006 Foo - read by narrator".
	readByCreditRe = regexp.MustCompile(`(?i)\s*(?:[-–—]\s*read\s+by\b[^()\[\]]*|[(\[]\s*read\s+by\b[^()\[\]]*[)\]])\s*$`)

	// readByLeadRe: a stem that is only an author credit: "read by X".
	readByLeadRe = regexp.MustCompile(`(?i)^\s*(?:read|narrated)\s+by\b`)
)

// stripReadByCredit drops a trailing narrator credit from a folder or
// filename title, so the title it leaves is judged on its own: "The Green
// Mile (read by Frank Muller)" is "The Green Mile".
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

// looksLikePersonName reports whether s has the shape of a person's name: two
// to four words, each capitalised (or an initial such as "L." or "J.R.R."),
// no digits, none of them a word a title has and a name does not.
func looksLikePersonName(s string) bool {
	words := strings.Fields(s)
	if len(words) < 2 || len(words) > 4 {
		return false
	}
	for _, w := range words {
		if namePartStopWords[strings.ToLower(w)] {
			return false
		}
		r := []rune(w)
		if !unicode.IsUpper(r[0]) {
			return false
		}
		for _, c := range r {
			if unicode.IsDigit(c) {
				return false
			}
		}
	}
	return true
}

// endsWithAuthorCredit returns the trailing " - <segment>" of c when that
// segment is a person's name or a placeholder author: a known person of the
// book, an "Unknown Author", or a segment shaped like a name. The last is a
// refusal, not a strip: nothing proves "Time Lord" in "Dark Gallifrey - Time
// Lord" is not a subtitle, and a skipped row costs less than a wrong title.
func endsWithAuthorCredit(c string, isPerson func(string) bool) (string, bool) {
	i := strings.LastIndex(c, " - ")
	if i < 0 {
		return "", false
	}
	seg := strings.TrimSpace(c[i+3:])
	if seg == "" || positionLeadRe.MatchString(seg) {
		return "", false
	}
	if authorname.IsPlaceholderAuthor(seg) || (isPerson != nil && isPerson(seg)) || looksLikePersonName(seg) {
		return seg, true
	}
	return "", false
}

// ---- author identity ----

// junkStoredAuthor reports whether the stored author carries no identity: it
// is empty, a placeholder ("Unknown Author", "read by narrator"), or a name
// the author classifier flags as junk ("- Some Title (read by X)"). The
// fixer then cannot compare a provider's recorded author with it and cannot
// key a duplicate check on it.
func junkStoredAuthor(a string) bool {
	a = strings.TrimSpace(a)
	if a == "" || authorname.IsPlaceholderAuthor(a) {
		return true
	}
	return authorjunk.ClassifyName(a).Junk()
}

// ---- series and number ----

// positionLeadRe: a segment that opens with a position word ("Volume II",
// "Part 3", "Book Two"): title text, never a person.
var positionLeadRe = regexp.MustCompile(`(?i)^(?:volume|vol|part|pt|book|disc|disk|cd|chapter|episode|season|series)\b`)

// positionWordRe: a word that places a file inside a book (disc, CD, track,
// part, chapter, "N of M"). A number beside one is a position in the book,
// never the book's position in a series.
var positionWordRe = regexp.MustCompile(`(?i)\b(?:(?:disc|disk|cd|dvd|tape|side|track|part|pt|chapter|chap|ch)(?:\b|\d)|(?:volume|vol|of)\b)`)
