// file: internal/metafetch/search_variants.go
// version: 1.2.0
// guid: 74a7d36b-024c-4887-a6c3-4ebaf2e61490
// last-edited: 2026-10-01

package metafetch

import (
	"regexp"
	"slices"
	"strings"
	"sync/atomic"

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

// maxAudnexusRegions is how many Audnexus regions one search's single
// Audnexus lookup tries (audnexusSearchRegions). The old ladder's fallback
// tried all of metadata's audnexusRegions, each a separate request against
// Audnexus's 2/s.
const maxAudnexusRegions = 3

// audnexusSearchRegions are the regions a search's Audnexus lookup tries,
// chosen for an English-language library:
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
// library. The lookup is a fallback: Audible's own ASIN lookup and its title
// search are asked first.
var audnexusSearchRegions = []string{"", "uk", "au"}

// strongRuntimeTolerance is the runtime agreement (|delta| / book runtime) a
// title+author match needs to count as strong enough to stop the fan-out.
// 0.15 sits inside durationTier's "close -- probably correct" band.
const strongRuntimeTolerance = 0.15

// parsedTitle is what parseSearchTitle reads out of a stored title: the book's
// own name and the series slot, narrator and author that organizers and
// taggers pack into the title field.
type parsedTitle struct {
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

// parseSearchTitle reads the book's own name out of raw, reusing the
// cleaners the scanner and the variant ladder already trust:
// metadata.StripRipJunk (bitrate/size groups), metadata.NarratorCreditName
// ("read by X (Title)"), authorname.IsPlaceholderAuthor (the organizer's
// " - Unknown Author" suffix, the shape metadata.go's file-name parse already
// clears), splitSeriesDecoration (", Book 5") and stripChapterFromTitle.
// author and narrator are the book's known people; a trailing " - <name>" or
// "(<name>)" is dropped as a person only when it is a placeholder, one of
// them, or person-shaped next to a series slot, so "The Witcher - 4 - The
// Tower of the Swallow" keeps its title.
func parseSearchTitle(raw, author, narrator string) parsedTitle {
	var p parsedTitle
	t, _ := metadata.StripRipJunk(strings.TrimSpace(raw))
	t = strings.TrimSpace(editionQualifierRe.ReplaceAllString(t, ""))

	// "read by Cathfach (Erryn's World)": the credit names the narrator and
	// the parenthesised part is the book.
	if name, ok := metadata.NarratorCreditName(t); ok {
		if m := trailingParenRe.FindStringSubmatch(name); m != nil {
			p.Narrator = strings.TrimSpace(m[1])
			t = strings.TrimSpace(m[2])
		}
	}

	// A trailing " - <person>": the organizer's "<title> - Unknown Author",
	// or a real author appended to a series slot ("Saving Supervillains,
	// Book 5 - Bruce Sentar").
	if locs := dashSegment.FindAllStringIndex(t, -1); len(locs) > 0 {
		last := locs[len(locs)-1]
		head, tail := strings.TrimSpace(t[:last[0]]), strings.TrimSpace(t[last[1]:])
		switch {
		case head == "":
		case authorname.IsPlaceholderAuthor(tail):
			t = head
		case samePerson(tail, author) || samePerson(tail, narrator):
			t = head
			if samePerson(tail, author) {
				p.Author = tail
			}
		case seriesDecoration.MatchString(head) && personShaped(tail):
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
	}

	p.Title = stripChapterFromTitle(t)
	if m := leadingYearRe.FindStringSubmatch(p.Title); m != nil && len(SignificantWords(m[1])) >= 2 {
		p.YearFree = strings.TrimSpace(m[1])
	}
	if s := stripSubtitle(p.Title); s != p.Title && len(anchorWords(s, "")) > 0 && !p.TitleIsSeries {
		p.Short = s
	}
	return p
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

func normPosition(pos string) string {
	n := normalizeSeriesNumber(pos)
	if t := strings.TrimLeft(n, "0"); t != "" && t[0] != '.' {
		return t
	}
	return n
}

// positionAgrees reports whether the provider's explicit series_position of r
// is want. A number in r's title never confirms a position: "Rogue Ascension
// 8" in a title says nothing a sibling's title can't also say.
func positionAgrees(r metadata.BookMetadata, want string) bool {
	return want != "" && normPosition(r.SeriesPosition) == want
}

// titleNumbers returns every number in title, normalized (normPosition).
func titleNumbers(title string) []string {
	var out []string
	for _, n := range titleNumberRe.FindAllString(title, -1) {
		out = append(out, normPosition(n))
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
			if sharesPersonWord(r.Author+" "+r.Narrator, people) {
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
		add(mk(variantShort, p.Short, who, &titleVariant{Query: p.Short, Anchor: a, Exact: true, Allowed: allowed, Strict: true}))
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
	people     string
	bookDur    int
	slotSeries string
	slotPos    string
	// position is the series position read from the title (normPosition);
	// see positionConflicts.
	position string
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
func newStrongCriteria(p parsedTitle, title, literal, asin, people string, bookDur int) strongCriteria {
	c := strongCriteria{asin: asin, people: people, bookDur: bookDur, position: normPosition(p.Position),
		nameIsTagline: &atomic.Bool{}}
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
	for _, s := range []string{literal, p.Title, p.Series, p.SlotHead, p.Name} {
		if strings.TrimSpace(s) == "" {
			continue
		}
		for w := range SignificantWords(s) {
			c.allowed[w] = true
		}
		for _, n := range titleNumbers(s) {
			c.allowedNums[n] = true
		}
	}
	if c.position != "" {
		c.allowedNums[c.position] = true
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
		other = sp != c.position
	} else {
		other = titleOther
	}
	return other && !c.nameVouches(r)
}

// dropConflicts returns rs without the answers that name another position
// (positionConflicts); an answer carrying the book's own ASIN and agreeing
// with it (ownASINAgrees) is never dropped.
func (c strongCriteria) dropConflicts(rs []metadata.BookMetadata) []metadata.BookMetadata {
	if c.position == "" {
		return rs
	}
	out := rs[:0:0]
	for _, r := range rs {
		if c.ownASINAgrees(r) || !c.positionConflicts(r) {
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
func (c strongCriteria) titleSubset(r metadata.BookMetadata) bool {
	for w := range SignificantWords(r.Title) {
		if c.allowed[w] || c.allowedNums[normPosition(w)] {
			continue
		}
		if !omnibusWords[w] && authorjunk.IsGenreTagline(w) {
			continue
		}
		return false
	}
	for _, n := range titleNumbers(r.Title) {
		if !c.allowedNums[n] {
			return false
		}
	}
	return true
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
	return true
}

func (c strongCriteria) runtimeAgrees(r metadata.BookMetadata) bool {
	return c.bookDur > 0 && r.DurationSec > 0 && durationDeltaRatio(c.bookDur, r.DurationSec) <= strongRuntimeTolerance
}

// ownASINAgrees reports whether r carries the book's own ASIN AND agrees with
// the book on something else: its title words, a person, or its runtime. A
// stored ASIN is sometimes wrong (an earlier bad match); an answer that
// carries it but disagrees on everything gets the ASIN multiplier only, not
// the first-place tier or the early stop.
func (c strongCriteria) ownASINAgrees(r metadata.BookMetadata) bool {
	if c.asin == "" || !strings.EqualFold(strings.TrimSpace(r.ASIN), c.asin) {
		return false
	}
	return c.titleAgrees(r) || c.runtimeAgrees(r) ||
		(strings.TrimSpace(c.people) != "" && sharesPersonWord(r.Author+" "+r.Narrator, c.people))
}

// matches reports whether r is strong. Title words alone never are: a
// sibling, a box set or an omnibus carries every word of the book's title.
// So r must carry the book's own ASIN and agree with it (ownASINAgrees), or
// name a person of the book's, identify it (identifies), name no other
// position (positionConflicts), and then
//   - with the book's own runtime: run within strongRuntimeTolerance of it;
//   - without one: name no other explicit series_position, and say nothing in
//     its title the book's own title, series and position do not
//     (titleSubset).
func (c strongCriteria) matches(r metadata.BookMetadata) bool {
	if c.ownASINAgrees(r) {
		return true
	}
	if strings.TrimSpace(c.people) == "" || !sharesPersonWord(r.Author+" "+r.Narrator, c.people) {
		return false
	}
	if c.positionConflicts(r) || !c.identifies(r) {
		return false
	}
	if c.bookDur > 0 {
		return c.runtimeAgrees(r)
	}
	if sp := normPosition(r.SeriesPosition); c.position != "" && sp != "" && sp != c.position {
		return false
	}
	return c.titleSubset(r)
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

// sharesPersonWord reports whether a and b share a significant name word.
func sharesPersonWord(a, b string) bool {
	bw := SignificantWords(b)
	for w := range SignificantWords(a) {
		if bw[w] {
			return true
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
// and the rest 1; Audnexus one ASIN lookup of at most maxAudnexusRegions
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
		return maxAudnexusRegions
	default:
		return 1
	}
}
