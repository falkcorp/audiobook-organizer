// file: internal/metafetch/search_variants.go
// version: 1.7.0
// guid: 74a7d36b-024c-4887-a6c3-4ebaf2e61490
// last-edited: 2026-10-05

package metafetch

import (
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/falkcorp/audiobook-organizer/internal/authorjunk"
	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// maxQueryVariants caps how many title/author combinations one book asks a
// fan-out source (Audible, Open Library). It multiplies straight into the
// batch op's per-book call count, which the slowest fan-out source divides
// into books/s (MaxSearchCallsPerBook, EnabledSourcesBudget), so variants are
// ordered by expected value and the tail is dropped rather than asked.
const maxQueryVariants = 4

// maxOpenLibraryAsks caps Open Library's variants. It never returns a
// runtime or a narrator, so it can never produce a strong match by itself;
// it is asked its next variant only while it has accepted nothing.
const maxOpenLibraryAsks = 2

// maxAudnexusRequests is the most region requests one search's single
// Audnexus lookup sends: every audnexusSearchRegions region, then every
// audnexusFallbackRegions one, when the ASIN is in none of them. A found
// ASIN costs at most len(audnexusSearchRegions). The old ladder's fallback
// asked all of metadata's audnexusRegions, the default store twice.
const maxAudnexusRequests = 8

// audnexusSearchRegions are the regions a search's Audnexus lookup tries
// first, chosen for an English-language library:
//   - "" is the default store, which IS the US one (the full list's "us"
//     asks it a second time);
//   - "uk" and "au" are the two other English stores with catalogs of their
//     own: UK-only releases (Big Finish, BBC, UK-published editions) and
//     ANZ-only ones;
//   - "ca" and "in" are left out: their English catalogs are drawn from the
//     US and UK ones, so an ASIN missing from "" and "uk" is very rarely
//     there;
//   - "de", "fr" and "jp" are left out: their catalogs are German, French and
//     Japanese releases, which an English library's ASINs do not name.
//
// This is reasoned from the stores' catalogs, not measured against the
// library, so it costs no recall: only when all three say "not here"
// (metadata.IsNotFound) are the rest asked (audnexusFallbackRegions) before
// the ASIN is called not found -- the full list main asked, without the
// default store twice.
var audnexusSearchRegions = []string{"", "uk", "au"}

// audnexusFallbackRegions are asked only after every audnexusSearchRegions
// region answered "not here".
var audnexusFallbackRegions = []string{"ca", "in", "de", "fr", "jp"}

// strongRuntimeTolerance is the runtime agreement (|delta| / book runtime) a
// title+author match needs to count as strong enough to stop the fan-out.
// 0.15 sits inside durationTier's "close -- probably correct" band.
const strongRuntimeTolerance = 0.15

// parsedTitle is what parseSearchTitle reads out of a stored title: the book's
// own name and the series slot, narrator and author that organizers and
// taggers pack into the title field.
type parsedTitle struct {
	// Junk: the title is no title (metadata.ClassifyJunkTitle: "Audiobook 2",
	// "New Recording 4"), so its numbers are not the book's -- no position,
	// and nothing for an answer's numbers to fit or conflict with.
	Junk bool
	// Title is the cleaned title to search by. When TitleIsSeries is set it
	// is the series name: the title named only a series slot.
	Title    string
	Series   string
	Position string
	Narrator string
	// Author is a real author read from a trailing " - <author>"; a
	// placeholder ("Unknown Author") is dropped, never recorded.
	Author string
	// YearFree is Title without a leading release year ("2010 The Stainless
	// Steel Rat Returns"), set only when one was there.
	YearFree string
	// Short is Title before its subtitle (": ..." or " - ...").
	Short string
	// TitleIsSeries: no book name survives, only "<series>, Book N" -- every
	// answer must then name the series AND the position (keepSeriesSlot).
	TitleIsSeries bool
	// NameSplit: Title is a book name read out from behind a series slot
	// ("A Wanted Man" from "Jack Reacher 17: A Wanted Man"), not the title
	// with its slot still in it.
	NameSplit bool
	// SlotHead is the text in front of a series slot's number, whatever its
	// word count ("Witcher" in "Witcher 4: The Tower of the Swallow"), and
	// Name the usable book name behind it (usableSlotName), recorded even
	// when the title is not split there. They anchor the position checks
	// (strongCriteria.positionConflicts), never the queries.
	SlotHead string
	Name     string
	// BareSlot: Position is a bare number trailing the title ("Rogue
	// Ascension 8", "Rogue Ascension #8", "Rogue Ascension VIII"), with no
	// slot word. Such a number may be part of the name ("Fahrenheit 451",
	// "Area 51"), so an answer whose title carries it is not refuted by a
	// provider's other series_position (strongCriteria.positionConflicts).
	BareSlot bool
	// StoredPosition is the book's stored series sequence, set by
	// resolveSearchInputs only when the title names no position ("Overlord"
	// with sequence 8). It gates strength alone (strongCriteria.storedPos),
	// never the pool and never the queries: it may come from an earlier bad
	// match.
	StoredPosition string
	// Year and Suffix are a leading release year and a track/part suffix
	// metadata.ParseBookName removed ("2018", "01"); Cleaned is the title
	// with every shape it read removed but the series slot kept. A removed
	// number is not the book's: newStrongCriteria reads the book's own
	// numbers from Cleaned, not from the literal title. "" when the parser
	// removed nothing.
	Year    string
	Suffix  string
	Cleaned string
	// AuthorIsTitle: the book's author equalled its title and was dropped
	// as junk (resolveSearchInputs). No person is left to vouch for an
	// answer, so every question asked without an author keeps only answers
	// whose title is exactly the book's and that all name one author
	// (buildQueryVariants: an Exact filter, keepVariant) -- the bulk fetch
	// applies the top candidate with no score floor.
	AuthorIsTitle bool
}

var (
	// editionQualifierRe matches "(Unabridged)", "[Abridged]", "(Unabridged
	// Edition)" anywhere in a title.
	editionQualifierRe = regexp.MustCompile(`(?i)\s*[(\[]\s*(?:un)?abridged(?:\s+edition)?\s*[)\]]`)
	// leadingYearRe: a release year in front of the title ("2010 The
	// Stainless Steel Rat Returns"). Whitespace, then a letter, is required,
	// so "2001: A Space Odyssey", "1984" and "11/22/63" never match.
	leadingYearRe = regexp.MustCompile(`^(?:19|20)\d{2}\s+(?:[-–—]\s+)?(\pL.*)$`)
	// trailingParenRe splits "<title> (<inner>)".
	trailingParenRe = regexp.MustCompile(`^(.*\S)\s*\(([^()]+)\)\s*$`)
	// dashPositionRe: "<series> - <n> - <title>" ("The Witcher - 4 - The
	// Tower of the Swallow").
	dashPositionRe = regexp.MustCompile(`^(.+?)\s+[-–—]\s+#?(\d{1,3}(?:\.\d+)?)\s+[-–—]\s+(.+)$`)
	// slotWordRe: a series name that is only a slot word ("Book 3: ...").
	slotWordRe = regexp.MustCompile(`(?i)^(?:book|bk|part|pt|vol(?:ume)?|episode|ep|chapter|disc|disk|track)\.?$`)
	// decorationNumberRe pulls the position out of a seriesDecoration match.
	decorationNumberRe = regexp.MustCompile(`\d+(?:\.\d+)?`)
	// labelledWordNumberRe: a slot word followed by a written-out number
	// ("Book Eight", "Part II", "Vol. Three"); parseSearchTitle reads it as
	// the digits, so it takes the same series-slot path as "Book 8".
	labelledWordNumberRe = regexp.MustCompile(`(?i)\b(book|bk|vol(?:ume)?|part|pt|episode|ep)(\.?\s+)([a-z]+)\b`)
	// trailingSeriesNumberRe: "<series> <n>" with nothing after the number --
	// "Rogue Ascension 8", "Rogue Ascension #8", "Rogue Ascension 08",
	// "Rogue Ascension 8.5", "Rogue Ascension VIII", "Rogue Ascension Eight".
	// A lone "I" is never a position here: "Who Am I".
	trailingSeriesNumberRe = regexp.MustCompile(`(?i)^(.*\pL.*?)[\s,]+#?\s*(\d{1,3}(?:\.\d+)?|xx|xix|xviii|xvii|xvi|xv|xiv|xiii|xii|xi|x|ix|viii|vii|vi|v|iv|iii|ii|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty)$`)
)

// notPersonWords are words a series or title in trailing parentheses carries
// and a person's name does not ("(Erryn's World)", "(Long Earth Saga)").
var notPersonWords = map[string]bool{
	"series": true, "saga": true, "book": true, "chronicles": true, "cycle": true,
	"trilogy": true, "edition": true, "collection": true, "world": true, "novel": true,
	"tales": true, "volume": true, "vol": true, "part": true, "unabridged": true, "abridged": true,
}

// samePerson reports whether two person strings name the same person,
// ignoring case and surrounding space.
func samePerson(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	return a != "" && b != "" && strings.EqualFold(a, b)
}

// personShaped reports whether s reads as a person's name rather than a
// series or edition note: metadata.LooksLikeAuthorSegment's capitalised-words
// gate, no digits, no possessive and no series/edition word.
func personShaped(s string) bool {
	if !metadata.LooksLikeAuthorSegment(s) || strings.ContainsAny(s, "0123456789") {
		return false
	}
	for _, w := range strings.Fields(strings.ToLower(s)) {
		if strings.HasSuffix(w, "'s") || strings.HasSuffix(w, "’s") || notPersonWords[strings.Trim(w, ".,:;")] {
			return false
		}
	}
	return true
}

// parseSearchTitle is parseSearchTitleWith with no evidence beyond the
// book's author and narrator.
func parseSearchTitle(raw, author, narrator string) parsedTitle {
	return parseSearchTitleWith(raw, author, narrator, metadata.NameEvidence{})
}

// parseSearchTitleWith reads the book's own name out of raw.
//
// The shapes file and folder names pack around a title -- rip details, a
// " - Unknown Author" placeholder, a leading release year ("2018 - Blueshift"),
// a leading or trailing author credit ("M.R. Forbes - Starship for Rent 02",
// "Title - Author"), a "read by" credit and a track suffix after a series slot
// ("Discworld 24 - The Fifth Elephant - 01") -- are read by
// metadata.ParseBookName, the one parser the scanner's folder parse uses too.
// ev adds the evidence it may check a segment against (the book's path, the
// authority lists); author and narrator are always added to it. What is left
// is read here for what only a search needs: the narrator credit
// ("read by X (Title)", metadata.NarratorCreditName), a trailing
// "(<narrator>)", the series slot with the gates the strong criteria read
// (splitSeriesDecoration, usableSlotName, BareSlot) and stripChapterFromTitle.
func parseSearchTitleWith(raw, author, narrator string, ev metadata.NameEvidence) parsedTitle {
	var p parsedTitle
	if strings.TrimSpace(author) != "" {
		ev.Authors = append(slices.Clone(ev.Authors), author)
	}
	if strings.TrimSpace(narrator) != "" {
		ev.Narrators = append(slices.Clone(ev.Narrators), narrator)
	}
	shared := metadata.ParseBookName(raw, ev)
	t := strings.TrimSpace(editionQualifierRe.ReplaceAllString(shared.Title, ""))
	p.Year, p.Suffix = shared.Year, shared.Suffix
	if shared.Narrator != "" {
		p.Narrator = shared.Narrator
	}
	// A credit read off a dash segment is recorded only when it is a real
	// person of the book's (or a known author): a placeholder never is.
	if a := strings.TrimSpace(shared.Author); a != "" && !authorname.IsPlaceholderAuthor(a) {
		if !samePerson(a, narrator) {
			p.Author = a
		}
	}

	// "read by Cathfach (Erryn's World)": the credit names the narrator and
	// the parenthesised part is the book.
	if name, ok := metadata.NarratorCreditName(t); ok {
		if m := trailingParenRe.FindStringSubmatch(name); m != nil {
			p.Narrator = strings.TrimSpace(m[1])
			t = strings.TrimSpace(m[2])
		}
	}

	// A real author appended to a series slot that is not one of the book's
	// own people ("Saving Supervillains, Book 5 - Bruce Sentar"): the
	// person-shaped tail is a credit only next to a slot -- "Dune - Frank
	// Herbert" and "The Witcher - Blood of Elves" are the same shape without
	// one.
	if locs := dashSegment.FindAllStringIndex(t, -1); len(locs) > 0 {
		last := locs[len(locs)-1]
		head, tail := strings.TrimSpace(t[:last[0]]), strings.TrimSpace(t[last[1]:])
		if head != "" && seriesDecoration.MatchString(head) && personShaped(tail) {
			t = head
			p.Author = tail
		}
	}

	// A trailing "(<narrator>)": "Jack Reacher 17: A Wanted Man (Jeff
	// Harding)". A known person always; an unknown one only when the book has
	// no narrator yet and the text is person-shaped.
	if m := trailingParenRe.FindStringSubmatch(t); m != nil {
		inner := strings.TrimSpace(m[2])
		switch {
		case samePerson(inner, author):
			t = strings.TrimSpace(m[1])
		case samePerson(inner, narrator) || (strings.TrimSpace(narrator) == "" && p.Narrator == "" && personShaped(inner)):
			t = strings.TrimSpace(m[1])
			if p.Narrator == "" {
				p.Narrator = inner
			}
		}
	}

	// A labelled written-out position reads as its digits ("Rogue Ascension,
	// Book Eight" is "Rogue Ascension, Book 8") -- only in a title with no
	// digits, so it never displaces a digit slot: "Hemlock Hollow 8: Book
	// One" stays book 8 with a tagline, not book 1 of "Hemlock Hollow 8".
	if !titleNumberRe.MatchString(t) {
		t = labelledWordNumberRe.ReplaceAllStringFunc(t, labelledWordNumber)
	}

	// Series slot.
	if m := dashPositionRe.FindStringSubmatch(t); m != nil && !slotWordRe.MatchString(strings.TrimSpace(m[1])) {
		p.Series, p.Position = strings.TrimSpace(m[1]), m[2]
		p.SlotHead = p.Series
		if name := strings.TrimSpace(m[3]); usableSlotName(name, p.Series) {
			t = name
			p.NameSplit = true
			p.Name = name
		}
	} else if loc := seriesDecoration.FindStringIndex(t); loc != nil {
		// A labelled slot (", Book 5", "Vol. 3") is read before a bare number,
		// so "Eternal Dominion, Book 04 - Assertions" names the series
		// "Eternal Dominion", not "Eternal Dominion, Book".
		base, series, bookName, found := splitSeriesDecoration(t)
		if found && series != "" {
			p.Series = series
			p.SlotHead = series
			p.Position = decorationNumberRe.FindString(t[loc[0]:loc[1]])
			if bookName != "" && usableSlotName(bookName, series) {
				t = bookName
				p.NameSplit = true
				p.Name = bookName
			} else if bookName != "" {
				// "Rogue Ascension, Book 8: A Progression LitRPG": the part
				// after the slot is a genre tagline, not the book's name.
				// Only the series and the slot identify the book.
				t = series
				p.TitleIsSeries = p.Position != ""
			} else if b := strings.TrimSpace(base); b != "" {
				t = b
				// Only the series and the slot remain ("Saving
				// Supervillains, Book 5"); a base that still carries more
				// ("Blood of Elves The Witcher, Book 1") is searched as is.
				p.TitleIsSeries = p.Position != "" && strings.EqualFold(b, series)
			}
		}
	} else if m := bareSeriesNumber.FindStringSubmatch(t); m != nil && !slotWordRe.MatchString(strings.TrimSpace(m[1])) {
		// "<series> <n>: <name>". The number is always kept as the position
		// the answers must agree with ("Fahrenheit 451: A Novel" must not
		// accept a book with another number), but the title is split only
		// when the series has two or more words and the rest is a real name:
		// "Fahrenheit 451", "Catch 22", "Area 51" and "Apollo 8" are titles,
		// and "A Novel" or "A Progression LitRPG" is a genre tagline.
		series, name := strings.TrimSpace(m[1]), strings.TrimSpace(m[3])
		p.Position = m[2]
		p.SlotHead = series
		// The name anchors the position checks whatever the series' word
		// count: "Witcher 4: The Tower of the Swallow" must keep "The Tower
		// of the Swallow" (Audible's #6) over "Time of Contempt" (#4).
		if usableSlotName(name, series) {
			p.Name = name
		}
		if len(SignificantWords(series)) >= 2 {
			p.Series = series
			if p.Name != "" {
				t = name
				p.NameSplit = true
			}
		}
	} else if m := trailingSeriesNumberRe.FindStringSubmatch(t); m != nil && !slotWordRe.MatchString(strings.TrimSpace(m[1])) {
		// "<series> <n>" with nothing after it. The number is the position
		// the answers must agree with; the title is searched as written and
		// no series is recorded, so no query variant changes. A written-out
		// number needs a series of two or more words ("Ready Player One" has
		// one; "Malcolm X" does not). The digits always count: "Apollo 13" vs
		// "Apollo 11" is a different book whatever the series' length.
		series := strings.Trim(strings.TrimSpace(m[1]), " ,:-–—")
		num, written := m[2], false
		if v, ok := wordNumber(num); ok {
			num, written = v, true
		}
		if words := len(SignificantWords(series)); words >= 2 || (words == 1 && !written) {
			p.Position = num
			p.SlotHead = series
			p.BareSlot = true
		}
	}

	// The shared parser's slot, where the reads above split nothing:
	//   - a one-word series before a SPACED DASH ("Discworld 24 - The Fifth
	//     Elephant"): bareSeriesNumber keeps a one-word series unsplit
	//     because "Fahrenheit 451: A Novel" and "Catch 22: A Novel" share the
	//     colon shape, but a spaced dash between a numbered head and a usable
	//     name is a filename's slot;
	//   - a hyphenated series word ("Para-Military Recruiter 06 - Soldier"),
	//     which bareSeriesNumber's head refuses;
	//   - book one named for its series ("The Forest Grimm 01 - The Forest
	//     Grimm"): the name has no word the series lacks, so usableSlotName
	//     refuses it, but it IS the book's name.
	// And a sub-series field after the slot ("Legend of Drizzt Book 05 -
	// Icewind Dale Trilogy - Streams of Silver"): the book is the last field.
	if shared.Series != "" && shared.Name != "" && !p.TitleIsSeries {
		name := strings.TrimSpace(shared.Name)
		switch {
		case p.NameSplit && shared.Has(metadata.ShapeSubseries) && strings.HasSuffix(strings.ToLower(t), strings.ToLower(name)) &&
			usableSlotName(name, shared.Series):
			t, p.Name = name, name
		case !p.NameSplit && usableSlotName(name, shared.Series):
			p.Series, p.Position, p.SlotHead = shared.Series, shared.Position, shared.Series
			p.BareSlot = false
			t, p.Name, p.NameSplit = name, name, true
		case !p.NameSplit && strings.EqualFold(name, shared.Series):
			p.Series, p.Position, p.SlotHead = shared.Series, shared.Position, shared.Series
			p.BareSlot = false
			t, p.NameSplit = name, true
		}
	}

	p.Title = stripChapterFromTitle(t)
	if shared.Title != strings.TrimSpace(raw) {
		p.Cleaned = shared.Title
	}
	if m := leadingYearRe.FindStringSubmatch(p.Title); m != nil && len(SignificantWords(m[1])) >= 2 {
		p.YearFree = strings.TrimSpace(m[1])
	}
	// The short title holds a letter: a bare number ("2018" from an uncleaned
	// "2018 - Blueshift") would spend a provider question on nothing.
	if s := stripSubtitle(p.Title); s != p.Title && len(anchorWords(s, "")) > 0 && !p.TitleIsSeries && hasLetter.MatchString(s) {
		p.Short = s
	}
	// A junk title ("Audiobook 2", "New Recording 4") is no title at all, so
	// its number is no series position (metadata.ClassifyJunkTitle, the
	// Repairs lane's classifier). Read as one, every position gate would drop
	// each candidate a provider numbers otherwise -- the whole input of the
	// junk-title fixer, which exists to replace exactly these titles.
	if metadata.ClassifyJunkTitle(strings.TrimSpace(raw)) != metadata.JunkNone {
		p.Junk = true
		p.Position, p.BareSlot, p.SlotHead, p.Name = "", false, "", ""
		if p.TitleIsSeries {
			p.Title, p.TitleIsSeries = t, false
		}
		p.Series = ""
	}
	return p
}

// labelledWordNumber rewrites one labelledWordNumberRe match with the
// number's digits ("Book Eight" -> "Book 8"); a word that is no number is
// left as written ("Book Club").
func labelledWordNumber(m string) string {
	sub := labelledWordNumberRe.FindStringSubmatch(m)
	if v, ok := wordNumber(sub[3]); ok {
		return sub[1] + sub[2] + v
	}
	return m
}

// usableSlotName reports whether name, read from behind a series slot, can
// stand as the book's own name: it has distinguishing words of its own and is
// not a genre tagline (authorjunk.IsGenreTagline: "A Novel", "A Progression
// LitRPG", "A LitRPG Adventure").
func usableSlotName(name, series string) bool {
	return len(anchorWords(name, series)) > 0 && !authorjunk.IsGenreTagline(name)
}

// titleNumberRe finds every number written in title, leading zeros dropped
// ("Catch-22" -> 22, "Book 08" -> 8).
var titleNumberRe = regexp.MustCompile(`\d+(?:\.\d+)?`)

// romanNumerals and numberWords are the written-out positions a title uses
// ("Rogue Ascension VIII", "Book Eight"): I to XX and one to twenty.
var romanNumerals = map[string]string{
	"i": "1", "ii": "2", "iii": "3", "iv": "4", "v": "5", "vi": "6", "vii": "7", "viii": "8", "ix": "9", "x": "10",
	"xi": "11", "xii": "12", "xiii": "13", "xiv": "14", "xv": "15", "xvi": "16", "xvii": "17", "xviii": "18", "xix": "19", "xx": "20",
}

var numberWords = map[string]string{
	"one": "1", "two": "2", "three": "3", "four": "4", "five": "5", "six": "6", "seven": "7", "eight": "8", "nine": "9", "ten": "10",
	"eleven": "11", "twelve": "12", "thirteen": "13", "fourteen": "14", "fifteen": "15", "sixteen": "16", "seventeen": "17",
	"eighteen": "18", "nineteen": "19", "twenty": "20",
}

// wordNumber returns the number a written-out word names ("VIII", "eight"
// -> "8"), case and surrounding punctuation ignored.
func wordNumber(w string) (string, bool) {
	w = strings.ToLower(strings.Trim(w, ".,:;#!?'\"()[]"))
	if v, ok := romanNumerals[w]; ok {
		return v, true
	}
	v, ok := numberWords[w]
	return v, ok
}

// numberKeywords label the number after them as a position ("Book Eight",
// "Part II", "No. 3").
var numberKeywords = map[string]bool{
	"book": true, "bk": true, "vol": true, "volume": true, "part": true, "pt": true,
	"episode": true, "ep": true, "no": true, "number": true,
}

// numberSegmentRe splits a title at its separators, so "last word" means the
// last word before a colon, comma, bracket or spaced dash.
var numberSegmentRe = regexp.MustCompile(`\s*(?:[:;,()\[\]]|\s[-–—]\s)\s*`)

// normPosition is pos's single number with leading zeros dropped ("Book
// 08" -> "8"), or "" for none. A range or a list ("8-10", "8, 9, 10") names
// no single position -- an omnibus is not book 8 -- so it is "" too: it
// neither satisfies a position nor refutes one. (normalizeSeriesNumber reads
// its first number, seqnum.ParsePosition its last.)
func normPosition(pos string) string {
	if len(titleNumberRe.FindAllString(pos, 2)) > 1 {
		return ""
	}
	n := normalizeSeriesNumber(pos)
	if t := strings.TrimLeft(n, "0"); t != "" && t[0] != '.' {
		return t
	}
	return n
}

// titleNumbers returns every number in title, normalized (normPosition):
// every number in digits, and a written-out one (wordNumber) after a slot
// word ("Book Eight", "Part II") or as the last word of a segment after
// another word ("Rogue Ascension VIII", "Rogue Ascension: Book Seven"). A
// written number elsewhere is a word ("Seven Years in Tibet"), and a lone
// trailing "I" is a pronoun ("Who Am I").
func titleNumbers(title string) []string {
	var out []string
	for _, n := range titleNumberRe.FindAllString(title, -1) {
		out = append(out, normPosition(n))
	}
	for _, seg := range numberSegmentRe.Split(title, -1) {
		toks := strings.Fields(seg)
		for i, tok := range toks {
			v, ok := wordNumber(tok)
			if !ok {
				continue
			}
			labelled := i > 0 && numberKeywords[strings.ToLower(strings.Trim(toks[i-1], ".#"))]
			trailing := i > 0 && i == len(toks)-1 && !strings.EqualFold(strings.Trim(tok, ".,:;!?"), "i")
			if labelled || trailing {
				out = append(out, v)
			}
		}
	}
	return out
}

// positionOverrideTolerance is how close r's runtime must be to the book's
// for it to outweigh a conflicting series position. Tighter than
// strongRuntimeTolerance: siblings in one series often run within 15% of
// each other, but rarely within 2%, and a provider's numbering ("The Tower of
// the Swallow" is Audible's #6, the file says "Witcher 4") is exactly the
// case where only the runtime can tell.
const positionOverrideTolerance = 0.02

// dropGenreTagline returns title without a genre-tagline subtitle ("Catch
// 22: A Novel" -> "Catch 22"), else title unchanged.
func dropGenreTagline(title string) string {
	for _, sep := range []string{": ", " - ", " — "} {
		if i := strings.LastIndex(title, sep); i > 0 && authorjunk.IsGenreTagline(title[i+len(sep):]) {
			return strings.TrimSpace(title[:i])
		}
	}
	return title
}

// queryVariant is one provider question: a title, optionally narrowed by a
// person sent as the author, and the rule its answers must pass.
type queryVariant struct {
	Kind   string
	Title  string
	Author string
	// filter, when set, is applied to the answers (live or cached) before
	// they join the pool: every derived (non-literal) title is anchored,
	// because POST /api/v1/metadata/bulk-fetch applies the top candidate with
	// no score floor and must never be handed a sibling of this book.
	filter *titleVariant
	// slotSeries/slotPosition, when set, keep only answers in that series at
	// that position (keepSeriesSlot).
	slotSeries   string
	slotPosition string
	// onlyIfEmpty: asked of a source only while it has accepted nothing (the
	// broad title-only question, for a book with known people).
	onlyIfEmpty bool
	// personRequired: an answer must name a person of the book's (a
	// title-only question answers with every author's book of that title).
	personRequired bool
}

// Variant kinds, for logs and the cache key.
const (
	variantTitleAuthor   = "title+author"
	variantTitleNarrator = "title+narrator" // the narrator sent as the author: taggers swap them
	variantTitleOnly     = "title"
	variantYearFree      = "year-free"
	variantShort         = "short-title"
	variantSeriesAuthor  = "series+author"
	variantLadder        = "ladder" // extraTitleVariants: segment, subtitle head, part stem
	variantLiteral       = "literal"
)

// key identifies the provider question, for skipping a duplicate ask and for
// the per-variant fetch-cache row.
func (v queryVariant) key() string {
	return strings.ToLower(strings.TrimSpace(v.Title)) + "\x00" + strings.ToLower(strings.TrimSpace(v.Author))
}

// accept returns the answers this variant keeps. people is the book's author
// and narrator, the persons a result may be vouched by.
func (v queryVariant) accept(results []metadata.BookMetadata, people string) []metadata.BookMetadata {
	if v.slotSeries != "" {
		if v.Kind == variantSeriesAuthor {
			results = keepSeriesSlot(results, v.slotSeries, v.slotPosition)
		} else {
			results = keepSlotOrNamed(results, v.slotSeries, v.slotPosition)
		}
	}
	if v.filter != nil {
		results = keepVariant(results, *v.filter, people)
	}
	if v.personRequired && strings.TrimSpace(people) != "" {
		var kept []metadata.BookMetadata
		for _, r := range results {
			if sharesPerson(r.Author+"; "+r.Narrator, people) {
				kept = append(kept, r)
			}
		}
		results = kept
	}
	return results
}

// keepSlotOrNamed is keepSeriesSlot for a title that splitSeriesDecoration
// read as "<series>, Book N" with nothing else -- a guess, because "Blood of
// Elves The Witcher, Book 1" has the same shape as "Saving Supervillains,
// Book 5". An answer is kept when it is in that series at that position, or
// when it names no slot of its own and its title is a proper part of ours
// ("Blood of Elves") carrying no other position number. An answer that is
// only the series' name, or names another position, is a sibling.
func keepSlotOrNamed(results []metadata.BookMetadata, series, position string) []metadata.BookMetadata {
	want := normalizeSeriesNumber(position)
	ours := SignificantWords(series)
	var kept []metadata.BookMetadata
	for _, r := range results {
		if len(keepSeriesSlot([]metadata.BookMetadata{r}, series, position)) == 1 {
			kept = append(kept, r)
			continue
		}
		if strings.TrimSpace(r.SeriesPosition) != "" || strings.EqualFold(strings.TrimSpace(r.Title), strings.TrimSpace(series)) {
			continue
		}
		if n := extractTrailingNumber(r.Title); n != "" && n != want {
			continue
		}
		words := SignificantWords(r.Title)
		ok := len(words) >= 2 && len(words) < len(ours)
		for w := range words {
			if !ours[w] {
				ok = false
				break
			}
		}
		if ok {
			kept = append(kept, r)
		}
	}
	return kept
}

// keepSeriesSlot keeps answers in series at position. A query by series name
// answers with the whole series, so a slot-only title ("Saving
// Supervillains, Book 5") is refused anything less than both.
func keepSeriesSlot(results []metadata.BookMetadata, series, position string) []metadata.BookMetadata {
	want := normalizeSeriesNumber(position)
	s := strings.ToLower(strings.TrimSpace(series))
	var kept []metadata.BookMetadata
	for _, r := range results {
		rs := strings.ToLower(strings.TrimSpace(r.Series))
		if rs == "" || want == "" || normalizeSeriesNumber(r.SeriesPosition) != want {
			continue
		}
		if strings.Contains(rs, s) || strings.Contains(s, rs) {
			kept = append(kept, r)
		}
	}
	return kept
}

// buildQueryVariants returns the provider questions for one book, ordered by
// expected value, duplicates removed and capped at maxQueryVariants:
//
//  1. cleaned title + author (title alone when no person is known)
//  2. cleaned title + narrator as the author (taggers swap the two; the
//     MetadataSource interface has no narrator parameter, so "title +
//     narrator" and "author/narrator swapped" are the same request)
//  3. the existing anchored ladder variants (extraTitleVariants)
//  4. the title without a leading year, then the short title, + author
//  5. series + author, kept only at the parsed position
//  6. cleaned title alone, for the broad answers the old ladder always asked
//
// literal is the chapter-stripped title the old ladder searched by; a title
// that differs from it is derived, and its answers are anchored.
func buildQueryVariants(p parsedTitle, literal, rawTitle, author, narrator string) []queryVariant {
	people := strings.TrimSpace(author + " " + narrator)
	var out []queryVariant
	seen := map[string]bool{}
	add := func(v queryVariant) {
		v.Title = strings.TrimSpace(v.Title)
		if len(v.Title) < 2 || len(out) >= maxQueryVariants {
			return
		}
		if seen[v.key()] {
			return
		}
		seen[v.key()] = true
		out = append(out, v)
	}

	base := p.Title
	var baseFilter *titleVariant
	slotSeries, slotPos := "", ""
	if p.TitleIsSeries {
		slotSeries, slotPos = p.Series, p.Position
	} else if !strings.EqualFold(strings.TrimSpace(base), strings.TrimSpace(literal)) {
		if a := anchorWords(base, p.Series); len(a) > 0 {
			baseFilter = &titleVariant{Query: base, Anchor: a}
		}
	}
	mk := func(kind, title, who string, f *titleVariant) queryVariant {
		return queryVariant{Kind: kind, Title: title, Author: who, filter: f, slotSeries: slotSeries, slotPosition: slotPos}
	}

	switch {
	case author != "":
		add(mk(variantTitleAuthor, base, author, baseFilter))
	case narrator == "":
		add(mk(variantTitleOnly, base, "", baseFilter))
	}
	if narrator != "" && !samePerson(narrator, author) {
		add(mk(variantTitleNarrator, base, narrator, baseFilter))
	}
	// The literal title is always asked too when parsing changed it: a parse
	// is a guess, and the literal question is the one the old ladder asked.
	if lit := strings.TrimSpace(literal); lit != "" && !strings.EqualFold(lit, strings.TrimSpace(base)) {
		// Anchored on the parsed title's words, as every derived variant
		// is: "Magma Heart - Unknown Author" asked as written must not let
		// "Frost Heart" in. A title that is only a series slot is already
		// held to its slot (mk).
		var lf *titleVariant
		if !p.TitleIsSeries {
			a := anchorWords(p.Title, p.Series)
			if len(a) == 0 {
				a = anchorWords(lit, "")
			}
			if len(a) > 0 {
				lf = &titleVariant{Query: lit, Anchor: a}
			}
		}
		lv := mk(variantLiteral, lit, author, lf)
		if author == "" {
			lv.Author = narrator
		}
		add(lv)
	}
	// The ladder's anchored variants read series slots, dash segments and
	// subtitle heads out of the raw title; when the parse above already found
	// the series slot it has the book's name, and the ladder would only
	// re-derive it with the narrator or slot still attached.
	if p.Series == "" {
		for _, ev := range extraTitleVariants(rawTitle, literal) {
			ev := ev
			who := author
			if who == "" {
				who = narrator
			}
			if who == "" && !ev.titleOnlyAllowed() {
				continue
			}
			// A genre tagline is not a book name ("A Progression LitRPG").
			if authorjunk.IsGenreTagline(ev.Query) {
				continue
			}
			add(queryVariant{Kind: variantLadder, Title: ev.Query, Author: who, filter: &ev})
		}
	}
	who := author
	if who == "" {
		who = narrator
	}
	if p.YearFree != "" {
		if a := anchorWords(p.YearFree, p.Series); len(a) > 0 && (who != "" || len(a) >= 2) {
			add(mk(variantYearFree, p.YearFree, who, &titleVariant{Query: p.YearFree, Anchor: a}))
		}
	}
	if p.Short != "" && who != "" {
		allowed := SignificantWords(base)
		a := anchorWords(p.Short, "")
		for w := range a {
			allowed[w] = true
		}
		add(mk(variantShort, p.Short, who, &titleVariant{Query: p.Short, Anchor: a, Exact: true, Allowed: allowed, Nums: numberSet(base), Strict: true}))
	}
	if p.Series != "" && p.Position != "" && who != "" {
		v := queryVariant{Kind: variantSeriesAuthor, Title: p.Series, Author: who, slotSeries: p.Series, slotPosition: p.Position}
		if !p.TitleIsSeries {
			if a := anchorWords(base, p.Series); len(a) > 0 {
				v.filter = &titleVariant{Query: base, Anchor: a}
			}
		}
		add(v)
	}
	if p.AuthorIsTitle {
		if a := anchorWords(base, ""); len(a) > 0 {
			for i := range out {
				if out[i].Author == "" && out[i].slotSeries == "" {
					out[i].filter = &titleVariant{Query: base, Anchor: a, Exact: true}
				}
			}
		}
	}
	if people != "" {
		// Title alone, for a book with people: asked of a source only when
		// its person-narrowed questions found nothing, and an answer must
		// still name one of the book's people -- otherwise another author's
		// "Hunted" joins the pool and can outrank nothing at all.
		v := mk(variantTitleOnly, base, "", baseFilter)
		v.onlyIfEmpty, v.personRequired = true, true
		add(v)
	}
	return out
}

// strongCriteria decides when a pooled answer is good enough that further
// variants cannot change the top of the ranking, so the fan-out stops: the
// book's own ASIN, or the cleaned title's words + a person of ours + a
// runtime within strongRuntimeTolerance (any runtime when the book's own is
// unknown).
type strongCriteria struct {
	asin       string
	titleWords map[string]bool
	// ownNums are the numbers of the cleaned title searched by
	// (positionNamed): with no parsed position, an answer must carry them.
	ownNums []string
	// authors is the book's author credit ("; "-separated names). Only an
	// author match satisfies the person leg (authorAgrees); a narrator in
	// common never does on its own.
	authors    string
	bookDur    int
	slotSeries string
	slotPos    string
	// position is the series position read from the title (normPosition);
	// see positionConflicts. bareSlot: it was a bare trailing number
	// (parsedTitle.BareSlot).
	position string
	bareSlot bool
	// storedPos is the book's stored series sequence when the title names no
	// position (parsedTitle.StoredPosition). Only the strong gates read it
	// (positionNamed, explicitPositionConflicts); positionConflicts does not,
	// so it never drops an answer from the pool.
	storedPos string
	// nameAnchor is the distinguishing words of the book name read from
	// behind the title's series slot (parsedTitle.Name), and seriesWords the
	// slot head's (parsedTitle.SlotHead).
	nameAnchor  map[string]bool
	seriesWords map[string]bool
	// allowed and allowedNums are every word and number of the book's own
	// title, series and position: with no runtime of our own, a strong
	// answer's title says nothing else (titleSubset).
	allowed     map[string]bool
	allowedNums map[string]bool
	// nameIsTagline is set, for the rest of the search, once two pooled
	// answers carry the name with different positions: it is the series'
	// tagline ("A Cozy Mystery"), not this book's name, and vouches for
	// nothing (noteNameEvidence). Shared by every copy of the criteria;
	// sources answer concurrently.
	nameIsTagline *atomic.Bool
}

// newStrongCriteria builds the criteria for one search from its parse, the
// title it searches by and literal, the title as written.
func newStrongCriteria(p parsedTitle, title, literal, asin, author string, bookDur int) strongCriteria {
	c := strongCriteria{asin: asin, authors: author, bookDur: bookDur, position: normPosition(p.Position),
		bareSlot: p.BareSlot, nameIsTagline: &atomic.Bool{}}
	if !p.Junk {
		c.ownNums = titleNumbers(title)
	}
	if p.TitleIsSeries {
		c.slotSeries, c.slotPos = p.Series, p.Position
	} else {
		c.titleWords = anchorWords(title, p.Series)
	}
	if p.Name != "" {
		c.nameAnchor = anchorWords(p.Name, p.SlotHead)
	}
	if strings.TrimSpace(p.SlotHead) != "" {
		c.seriesWords = SignificantWords(p.SlotHead)
	}
	c.allowed, c.allowedNums = map[string]bool{}, map[string]bool{}
	// The literal title less what metadata.ParseBookName removed: a leading
	// release year ("2018 - Blueshift"), a track suffix ("- 01"), a credit.
	// Those numbers are not the book's, and an answer is not another book for
	// lacking them -- nor this one for carrying them.
	if strings.TrimSpace(p.Cleaned) != "" {
		literal = p.Cleaned
	}
	for _, s := range []string{literal, p.Title, p.Series, p.SlotHead, p.Name} {
		if strings.TrimSpace(s) == "" {
			continue
		}
		for w := range SignificantWords(s) {
			c.allowed[w] = true
		}
		if p.Junk {
			continue
		}
		for _, n := range titleNumbers(s) {
			c.allowedNums[n] = true
		}
	}
	if c.position != "" {
		c.allowedNums[c.position] = true
	} else if sp := normPosition(p.StoredPosition); sp != "" {
		c.storedPos = sp
		c.allowedNums[sp] = true
	}
	// A written-out number of the book's own ("viii" in "Rogue Ascension
	// VIII") is checked as a number (numbersFit, positionConflicts), not as a
	// word every answer must carry: "Rogue Ascension 8" is the same book.
	for w := range c.titleWords {
		if v, ok := wordNumber(w); ok && c.allowedNums[v] {
			delete(c.titleWords, w)
		}
	}
	return c
}

// nameVouches reports whether r's title carries the book's own name, and the
// name has not turned out to be a shared tagline.
func (c strongCriteria) nameVouches(r metadata.BookMetadata) bool {
	return len(c.nameAnchor) > 0 && (c.nameIsTagline == nil || !c.nameIsTagline.Load()) && coversWords(r.Title, c.nameAnchor)
}

// answerPosition is the position r names: its explicit series_position, else
// the first number in its title; "" for neither.
func answerPosition(r metadata.BookMetadata) string {
	if sp := normPosition(r.SeriesPosition); sp != "" {
		return sp
	}
	if nums := titleNumbers(r.Title); len(nums) > 0 {
		return nums[0]
	}
	return ""
}

// noteNameEvidence marks the name a tagline when two answers in rs carry it
// with different positions ("Hemlock Hollow 7: A Cozy Mystery" and "Hemlock
// Hollow 8: A Cozy Mystery"): a name the series' siblings share is not this
// book's. It catches taglines the genre vocabulary does not know.
func (c strongCriteria) noteNameEvidence(rs []metadata.BookMetadata) {
	if len(c.nameAnchor) == 0 || c.nameIsTagline == nil || c.nameIsTagline.Load() {
		return
	}
	first := ""
	for _, r := range rs {
		if !coversWords(r.Title, c.nameAnchor) {
			continue
		}
		pos := answerPosition(r)
		if pos == "" {
			continue
		}
		if first == "" {
			first = pos
		} else if pos != first {
			c.nameIsTagline.Store(true)
			return
		}
	}
}

// positionConflicts reports whether r is another book of the series than the
// one at c.position. The evidence, strongest first:
//  1. r's title states the series beside another number ("Rogue Ascension 7",
//     "Hemlock Hollow 7: A Cozy Mystery" for book 8): a conflict, whatever
//     else agrees -- the title says which book it is;
//  2. r's runtime within positionOverrideTolerance of the book's: never a
//     conflict (a provider's numbering differs from the file's);
//  3. the provider's explicit series_position: a different one conflicts;
//  4. otherwise a number in r's title: weak, it conflicts only when none of
//     r's title numbers is the position.
//
// 3 and 4 are excused when r's title carries the book's own name
// (nameVouches: "The Tower of the Swallow", Audible's #6, for "Witcher 4: The
// Tower of the Swallow").
func (c strongCriteria) positionConflicts(r metadata.BookMetadata) bool {
	if c.position == "" {
		return false
	}
	nums := titleNumbers(r.Title)
	titleOther := len(nums) > 0 && !slices.Contains(nums, c.position)
	if titleOther && len(c.seriesWords) > 0 && coversWords(r.Title, c.seriesWords) {
		return true
	}
	if c.bookDur > 0 && r.DurationSec > 0 && durationDeltaRatio(c.bookDur, r.DurationSec) <= positionOverrideTolerance {
		return false
	}
	var other bool
	if sp := normPosition(r.SeriesPosition); sp != "" {
		// A bare trailing number may be the name's own ("Area 51"): an answer
		// whose title carries it is not refuted by its series_position.
		other = sp != c.position && !(c.bareSlot && slices.Contains(nums, c.position))
	} else {
		other = titleOther
	}
	// The book's name excuses another position only in a title that carries
	// no number the book's own does not (numbersFit): "Assertions 2" covers
	// the name "Assertions" and is still another book. Word coverage cannot
	// see the "2" -- SignificantWords drops short tokens.
	return other && !(c.nameVouches(r) && c.numbersFit(r.Title))
}

// dropConflicts returns rs without the answers that name another position
// (positionConflicts). An answer carrying the book's own ASIN gets no pass:
// a stored ASIN can be a sibling's (ownASINAgrees).
func (c strongCriteria) dropConflicts(rs []metadata.BookMetadata) []metadata.BookMetadata {
	if c.position == "" {
		return rs
	}
	out := rs[:0:0]
	for _, r := range rs {
		if !c.positionConflicts(r) {
			out = append(out, r)
		}
	}
	return out
}

// titleSubset reports whether every word and number of r's title is the
// book's own (allowed, allowedNums). A genre word ("A Novel") may be added; a
// set word ("Books", "Collection") may not. "Jack Reacher, Books 17-19" and "A
// Wanted Man / Never Go Back / Personal" fail it for "Jack Reacher 17: A
// Wanted Man".
//
// A title may restate the answer's OWN series metadata ("A Wanted Man: Jack
// Reacher, Book 17" with series "Jack Reacher") and edition noise
// ("Unabridged"); "Abridged" is another product and fails. Only when the
// book's title names no series of its own: a book that does already allows
// its series' words, and an answer's other series ("Sword Art Online
// Progressive" for "Sword Art Online, Vol. 8") is a sibling series, not a
// restatement.
func (c strongCriteria) titleSubset(r metadata.BookMetadata) bool {
	own := SignificantWords(r.Series)
	if strings.TrimSpace(r.Series) == "" || len(c.seriesWords) > 0 {
		own = nil
	}
	for w := range SignificantWords(r.Title) {
		if c.allowed[w] || c.allowedNums[normPosition(w)] || own[w] || subsetNoiseWords[w] {
			continue
		}
		if v, ok := wordNumber(w); ok && c.allowedNums[v] {
			continue
		}
		if !omnibusWords[w] && w != "abridged" && authorjunk.IsGenreTagline(w) {
			continue
		}
		return false
	}
	return c.numbersFit(r.Title)
}

// subsetNoiseWords may appear in a strong answer's title beyond the book's
// own words: they say nothing about which book it is.
var subsetNoiseWords = map[string]bool{"unabridged": true, "audiobook": true, "book": true}

// numbersFit reports whether every number in title is one of the book's own
// (allowedNums). SignificantWords drops tokens of two characters or fewer, so
// without this "Rogue Ascension 7" reads as "Rogue Ascension 8".
func (c strongCriteria) numbersFit(title string) bool {
	for _, n := range titleNumbers(title) {
		if !c.allowedNums[n] {
			return false
		}
	}
	return true
}

// numberSet returns the numbers in s (titleNumbers) as a set.
func numberSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, n := range titleNumbers(s) {
		out[n] = true
	}
	return out
}

// titleAgrees reports whether r's title carries every one of the cleaned
// title's distinguishing words.
func (c strongCriteria) titleAgrees(r metadata.BookMetadata) bool {
	if len(c.titleWords) == 0 {
		return false
	}
	words := SignificantWords(r.Title)
	for w := range c.titleWords {
		if !words[w] {
			return false
		}
	}
	// Numbers never vanish from the comparison (numbersFit).
	return c.numbersFit(r.Title)
}

func (c strongCriteria) runtimeAgrees(r metadata.BookMetadata) bool {
	return c.bookDur > 0 && r.DurationSec > 0 && durationDeltaRatio(c.bookDur, r.DurationSec) <= strongRuntimeTolerance
}

// runtimeExact reports whether r runs within positionOverrideTolerance of the
// book: close enough to tell the book from a sibling of its series, which
// strongRuntimeTolerance is not.
func (c strongCriteria) runtimeExact(r metadata.BookMetadata) bool {
	return c.bookDur > 0 && r.DurationSec > 0 && durationDeltaRatio(c.bookDur, r.DurationSec) <= positionOverrideTolerance
}

// ownASINAgrees reports whether r carries the book's own ASIN AND is this
// book by the same evidence any other answer needs: it names no other
// position (positionConflicts, explicitPositionConflicts), it names the
// book's (positionNamed), its title carries no number the book's does not
// (numbersFit), and its title says nothing the book's does not (titleSubset)
// or it runs within positionOverrideTolerance -- not strongRuntimeTolerance:
// siblings of one series run alike ("Sword Art Online Progressive 8" is 4%
// off "Sword Art Online, Vol. 8"). A stored ASIN is
// sometimes wrong -- an earlier bad match stored a sibling's ("Rogue
// Ascension 7" on book 8) -- and an answer that carries it but fails that
// gets no ASIN multiplier, no first-place tier and no early stop.
func (c strongCriteria) ownASINAgrees(r metadata.BookMetadata) bool {
	if c.asin == "" || !strings.EqualFold(strings.TrimSpace(r.ASIN), c.asin) {
		return false
	}
	return !c.positionConflicts(r) && !c.explicitPositionConflicts(r) && c.positionNamed(r) &&
		c.numbersFit(r.Title) && (c.runtimeExact(r) || c.titleSubset(r))
}

// positionNamed reports whether r names the book's number: its position
// (an explicit series_position or a number in its title) when the book has
// one, else its stored sequence (storedPos) or every number of the book's own
// title (ownNums: "Metro 2033", "The Final Four"). An answer carrying the
// book's own name (nameVouches: "The Tower of the Swallow" for "Witcher 4:
// The Tower of the Swallow") needs no position only when it runs within
// positionOverrideTolerance -- the provider-numbering case only the runtime
// can tell. The name alone is not enough: siblings share one ("Hemlock
// Hollow: Midlife Magic" for "Hemlock Hollow 8: Midlife Magic", "Murder at
// the Hollow" for "Hemlock Hollow 8: Murder"). An answer that names no number is any book of the series --
// "Rogue Ascension" (Google Books' and Open Library's shape, with no
// series_position) for "Rogue Ascension VIII", "The Witcher" for "The
// Witcher 4", "The Final" for "The Final Four" -- and no position gate can
// refute what names nothing. It may stay in the pool, but it is never
// strong: not by its author, not by a stored ASIN that may be a sibling's,
// and not by a runtime, which a sibling of the same series can share.
func (c strongCriteria) positionNamed(r metadata.BookMetadata) bool {
	names := func(pos string) bool {
		return normPosition(r.SeriesPosition) == pos || slices.Contains(titleNumbers(r.Title), pos)
	}
	if c.position != "" {
		return names(c.position) || (c.nameVouches(r) && c.runtimeExact(r))
	}
	if c.storedPos != "" {
		// A stored sequence is weaker evidence than the title's own number
		// (it may come from an earlier bad match), so a runtime within
		// positionOverrideTolerance names the book whatever a provider
		// numbers it ("The Tower of the Swallow", stored 4, Audible's #6).
		if names(c.storedPos) || c.runtimeExact(r) {
			return true
		}
		// Only a number of the title's own can still name the book ("Metro
		// 2033", stored 1, is named by 2033). With none, nothing else does:
		// "Overlord" at #1 is another book, and "Overlord" with no
		// series_position names no number at all -- the ownNums loop below
		// would pass it vacuously.
		if len(c.ownNums) == 0 {
			return false
		}
	}
	nums := numberSet(r.Title)
	for _, n := range c.ownNums {
		if !nums[n] {
			return false
		}
	}
	return true
}

// explicitPositionConflicts reports whether r's explicit series_position is
// not the book's and r's runtime is not within positionOverrideTolerance of
// the book's. "Not the book's": not the title's position, or, when the parse
// found none, not one of the title's numbers (allowedNums) while r's title does
// not carry all of them (carriesAllNumbers) -- a title with no number at all
// says nothing either way. A strong answer never has one: the
// name exemption that keeps it in the pool ("The Tower of the Swallow",
// Audible's #6) is not enough to stop the search on, and neither is a
// position the parse could not read.
func (c strongCriteria) explicitPositionConflicts(r metadata.BookMetadata) bool {
	sp := normPosition(r.SeriesPosition)
	if sp == "" {
		return false
	}
	if c.position != "" {
		if sp == c.position || (c.bareSlot && slices.Contains(titleNumbers(r.Title), c.position)) {
			return false
		}
	} else if c.storedPos != "" {
		// The book's stored sequence ("Overlord", sequence 8): "Overlord"
		// at series_position 1 is book 1. As with no stored sequence, a title
		// that carries the book's numbers is excused: every allowed number
		// ("Overlord 8" at #1), or every number of the title's own ("Metro
		// 2033", stored 1, at #3: the provider counts a prequel).
		if sp == c.storedPos || c.carriesAllNumbers(r.Title) || c.carriesOwnNumbers(r.Title) {
			return false
		}
	} else if len(c.allowedNums) == 0 || c.allowedNums[sp] || c.carriesAllNumbers(r.Title) {
		return false
	}
	return !c.runtimeExact(r)
}

// carriesOwnNumbers reports whether title carries every number of the book's
// own title (ownNums), and there is at least one.
func (c strongCriteria) carriesOwnNumbers(title string) bool {
	if len(c.ownNums) == 0 {
		return false
	}
	nums := numberSet(title)
	for _, n := range c.ownNums {
		if !nums[n] {
			return false
		}
	}
	return true
}

// siblingEvidence reports whether r positively names another book than this
// one: another series position (positionConflicts, explicitPositionConflicts)
// or a number in its title the book's does not carry (numbersFit). Unlike
// !ownASINAgrees it is not a mere lack of agreement: an answer about which
// nothing is known is not a sibling.
func (c strongCriteria) siblingEvidence(r metadata.BookMetadata) bool {
	return c.positionConflicts(r) || c.explicitPositionConflicts(r) || !c.numbersFit(r.Title)
}

// carriesAllNumbers reports whether title carries every number of the book's
// own (allowedNums). With no parsed position, those numbers are the name's
// ("Metro 2033", "2001: A Space Odyssey"), and an answer that carries them is
// that book whatever a provider numbers it in its series (Metro 2033 is its
// series' #1).
func (c strongCriteria) carriesAllNumbers(title string) bool {
	nums := numberSet(title)
	for n := range c.allowedNums {
		if !nums[n] {
			return false
		}
	}
	return true
}

// authorAgrees reports whether one of r's authors is one of the book's
// (samePersonName). The person leg of a strong match: a shared first name
// ("Michael Grant" for "Michael Connelly") is not a match, and a narrator in
// common is not one at all.
func (c strongCriteria) authorAgrees(r metadata.BookMetadata) bool {
	return sharesPerson(r.Author, c.authors)
}

// matches reports whether r is strong. Title words alone never are: a
// sibling, a box set or an omnibus carries every word of the book's title,
// and a sequel ("The Fall of Hyperion") runs close to the book. So r must
// carry the book's own ASIN and agree with it (ownASINAgrees), or:
//   - name one of the book's AUTHORS (authorAgrees; a narrator never counts);
//   - identify the book (identifies) and name no other position
//     (positionConflicts), nor another explicit series_position unless its
//     runtime is within positionOverrideTolerance (explicitPositionConflicts);
//   - name the book's position, when it has one (positionNamed);
//   - say nothing in its title the book's own title, series and position do
//     not (titleSubset);
//   - and, when the book has its own runtime, run within
//     strongRuntimeTolerance of it.
func (c strongCriteria) matches(r metadata.BookMetadata) bool {
	if c.ownASINAgrees(r) {
		return true
	}
	if !c.authorAgrees(r) {
		return false
	}
	if c.positionConflicts(r) || c.explicitPositionConflicts(r) || !c.positionNamed(r) || !c.identifies(r) || !c.titleSubset(r) {
		return false
	}
	return c.bookDur <= 0 || c.runtimeAgrees(r)
}

// identifies reports whether r's title names this book: the series slot when
// the title is only that, else the cleaned title's words or the book's own
// name (nameVouches).
func (c strongCriteria) identifies(r metadata.BookMetadata) bool {
	if c.slotSeries != "" {
		return len(keepSeriesSlot([]metadata.BookMetadata{r}, c.slotSeries, c.slotPos)) > 0
	}
	return c.titleAgrees(r) || c.nameVouches(r)
}

// coversWords reports whether title carries every word of words.
func coversWords(title string, words map[string]bool) bool {
	tw := SignificantWords(title)
	for w := range words {
		if !tw[w] {
			return false
		}
	}
	return true
}

// personSepRe splits a credit into groups of names: "A & B", "A and B",
// "A; B", "A / B". A comma is read inside a group (personNames): it may
// separate two people ("Lee Child, Jeff Harding"), a suffix or role ("Michael
// Grant, Jr.", "X, editor") or a sorted name ("Grant, Michael").
var personSepRe = regexp.MustCompile(`(?i)\s*(?:[;/&]|\band\b)\s*`)

// personRoleWords are name suffixes and credit roles: words of a credit that
// are never a name of their own, a surname, or a given name.
var personRoleWords = map[string]bool{
	"jr": true, "sr": true, "ii": true, "iii": true, "iv": true, "phd": true, "md": true,
	"ed": true, "eds": true, "editor": true, "editors": true, "translator": true, "foreword": true,
	"introduction": true, "narrator": true, "illustrator": true,
	// Honorifics: "Sir Arthur C. Clarke" is Arthur C. Clarke.
	"sir": true, "dr": true, "dame": true, "lord": true, "lady": true, "prof": true, "rev": true,
	"mr": true, "mrs": true, "ms": true,
}

// surnameParticles join the word after them into one surname ("le guin",
// "de camp", "van dyke"). A particle that ends a name is that name's surname
// ("Le" in "Thanh Le"), never skipped for the word before it.
var surnameParticles = map[string]bool{
	"le": true, "la": true, "de": true, "del": true, "van": true, "von": true, "der": true,
	"da": true, "du": true, "di": true, "st": true,
}

// isSurnameShape reports whether a comma part is a surname alone: one word,
// or particles and one word ("Le Guin" in "Le Guin, Ursula K.").
func isSurnameShape(words []string) bool {
	for _, w := range words[:len(words)-1] {
		if !surnameParticles[w] {
			return false
		}
	}
	return true
}

// personPlaceholders name nobody: two credits that both say "Various
// Authors" share no person.
var personPlaceholders = map[string]bool{
	"various": true, "various authors": true, "various artists": true, "unknown": true, "unknown author": true,
	"full cast": true, "a full cast": true, "anonymous": true, "anon": true, "uncredited": true, "multiple authors": true,
}

// personNames splits a credit into names, each as its lower-cased words
// without dots ("Ph.D." is "phd", "J.R.R." is "jrr"). A comma part that is
// only a suffix or role is dropped ("Michael Grant, Jr."), a two-part
// "<surname>, <given>" credit is read as one name ("Grant, Michael"), and a
// placeholder ("Various Authors", "Full Cast") is no name at all.
func personNames(s string) [][]string {
	var out [][]string
	for _, group := range personSepRe.Split(s, -1) {
		var parts [][]string
		for _, part := range strings.Split(group, ",") {
			if words := nameWords(part); len(words) > 0 {
				parts = append(parts, words)
			}
		}
		if len(parts) == 2 && isSurnameShape(parts[0]) {
			parts = [][]string{append(parts[1], parts[0]...)}
		}
		for _, words := range parts {
			name := strings.Join(words, " ")
			if !personPlaceholders[name] && !authorname.IsPlaceholderAuthor(name) {
				out = append(out, words)
			}
		}
	}
	return out
}

// nameWords returns part's words, lower-cased, without punctuation or dots
// and with accents folded ("Zoë Brontë" is "zoe bronte": a provider
// credits the accented spelling the book's tags often lack), or nil when
// every word is a suffix or role (personRoleWords).
func nameWords(part string) []string {
	var words []string
	roleOnly := true
	for _, w := range strings.Fields(foldAccents(strings.ToLower(part))) {
		w = strings.ReplaceAll(strings.Trim(w, ".,;:'\"()"), ".", "")
		if w == "" {
			continue
		}
		words = append(words, w)
		if !personRoleWords[w] {
			roleOnly = false
		}
	}
	if roleOnly {
		return nil
	}
	return words
}

// foldAccents removes combining marks after canonical decomposition ("é" ->
// "e"), so two spellings of one name compare equal.
func foldAccents(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return norm.NFC.String(b.String())
}

// surname is a name's last word that is not a suffix, role or initial, with
// the particles in front of it ("le guin"), and the index it starts at; "",
// -1 for none. Every particle directly in front is taken into it, so none is
// ever left before that index for givenInitial to read an initial from.
func surname(words []string) (string, int) {
	for i := len(words) - 1; i >= 0; i-- {
		if w := words[i]; len([]rune(w)) > 1 && !personRoleWords[w] {
			j := i
			for j > 0 && surnameParticles[words[j-1]] {
				j--
			}
			return strings.Join(words[j:i+1], " "), j
		}
	}
	return "", -1
}

// givenInitial is the first letter of a name's first word before its
// surname (at si), skipping roles and honorifics; 0 when the name is a
// surname alone. A particle is never read here: surname takes every particle
// in front of it.
func givenInitial(words []string, si int) rune {
	for _, w := range words[:max(si, 0)] {
		if !personRoleWords[w] {
			return []rune(w)[0]
		}
	}
	return 0
}

// samePersonName reports whether two names are one person: the same full
// name, or the same surname with the same first initial when both give one
// ("J.R.R. Tolkien" is "John Ronald Reuel Tolkien"; "Owen King" is not
// "Stephen King"). A surname alone agrees with any given name. "Michael
// Grant" and "Michael Connelly" share a word, not a person.
func samePersonName(a, b []string) bool {
	if strings.Join(a, " ") == strings.Join(b, " ") {
		return true
	}
	sa, ia := surname(a)
	sb, ib := surname(b)
	if sa == "" || sa != sb {
		return false
	}
	ga, gb := givenInitial(a, ia), givenInitial(b, ib)
	return ga == 0 || gb == 0 || ga == gb
}

// sharesPerson reports whether a credit in a and one in b name the same
// person (samePersonName).
func sharesPerson(a, b string) bool {
	bn := personNames(b)
	for _, x := range personNames(a) {
		for _, y := range bn {
			if samePersonName(x, y) {
				return true
			}
		}
	}
	return false
}

// candidateKeys are the identities a result is deduplicated by: its ASIN,
// its ISBNs and its normalized title+author (always the first key).
func candidateKeys(r metadata.BookMetadata) []string {
	keys := []string{"ta:" + strings.ToLower(strings.Join(strings.Fields(r.Title), " ")) + "|" + strings.ToLower(strings.Join(strings.Fields(r.Author), " "))}
	if a := strings.ToUpper(strings.TrimSpace(r.ASIN)); a != "" {
		keys = append(keys, "asin:"+a)
	}
	for _, isbn := range []string{r.ISBN13, r.ISBN10, r.ISBN} {
		if n := strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(isbn), "-", ""), " ", ""); n != "" {
			keys = append(keys, "isbn:"+n)
		}
	}
	return keys
}

// candidateSeen tracks pooled identities (candidateKeys).
type candidateSeen map[string]bool

// add records r's keys and reports whether r is new. A result carrying an
// ASIN or ISBN is a duplicate only by that ID: two editions of one title
// (abridged and unabridged, two narrators) have different ASINs and must both
// reach the ranking, where runtime and narrator tell them apart. A result
// with no ID is a duplicate by normalized title+author, so an Open Library
// record of a book Audible already returned collapses into it.
func (s candidateSeen) add(r metadata.BookMetadata) bool {
	keys := candidateKeys(r)
	check := keys
	if len(keys) > 1 {
		check = keys[1:]
	}
	for _, k := range check {
		if s[k] {
			return false
		}
	}
	for _, k := range keys {
		s[k] = true
	}
	return true
}

// sourcePolicy is how the fan-out asks one provider.
type sourcePolicy int

const (
	// policyFanOut: every variant, up to maxQueryVariants. Quota-free
	// title-search sources (Audible, Open Library) and sources with no
	// declared id (test stubs).
	policyFanOut sourcePolicy = iota
	// policyBestOnly: the best variant only. Google Books (a 1,000/day key
	// quota), Hardcover (60 requests/minute, which would otherwise become the
	// batch's slowest source) and any other provider.
	policyBestOnly
	// policyASINOnly: never title-searched. Audnexus has no title search:
	// its SearchByTitle returns nothing and its SearchByTitleAndAuthor spends
	// a request on /authors and then returns nothing, so every rung the old
	// ladder sent it was a wasted request against its 2/s budget. It is asked
	// only by ASIN (lookupASIN).
	policyASINOnly
	// policyUntilFound: the next variant only while the source has accepted
	// nothing, at most maxOpenLibraryAsks. Open Library returns no runtime
	// or narrator, so it can never make a strong match: asking it every
	// variant would make it the batch's slowest source (3/s / 4) for answers
	// that cannot stop the fan-out.
	policyUntilFound
)

func sourcePolicyFor(providerID string) sourcePolicy {
	switch providerID {
	case metadata.SourceIDAudible, "":
		return policyFanOut
	case metadata.SourceIDOpenLibrary:
		return policyUntilFound
	case metadata.SourceIDAudnexus:
		return policyASINOnly
	default:
		return policyBestOnly
	}
}

// MaxSearchCallsPerBook is the most requests one book's search sends the
// provider with this config id, ASIN lookups included (cache hits and early
// stop send fewer): Audible maxQueryVariants title searches + 1 lookup of the
// book's own ASIN; Open Library maxOpenLibraryAsks; Google Books, Hardcover
// and the rest 1; Audnexus one ASIN lookup of at most maxAudnexusRequests
// region requests. The candidate op sizes its workers and reports its
// binding source from this.
func MaxSearchCallsPerBook(providerID string) int {
	if providerID == metadata.SourceIDAudible {
		return maxQueryVariants + 1
	}
	switch sourcePolicyFor(providerID) {
	case policyFanOut:
		return maxQueryVariants
	case policyUntilFound:
		return maxOpenLibraryAsks
	case policyASINOnly:
		return maxAudnexusRequests
	default:
		return 1
	}
}
