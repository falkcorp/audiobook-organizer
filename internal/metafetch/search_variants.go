// file: internal/metafetch/search_variants.go
// version: 1.0.0
// guid: 74a7d36b-024c-4887-a6c3-4ebaf2e61490
// last-edited: 2026-10-01

package metafetch

import (
	"regexp"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// maxQueryVariants caps how many title/author combinations one book asks a
// fan-out source (Audible, Open Library). It multiplies straight into the
// batch op's per-book call count, which the slowest fan-out source divides
// into books/s (MaxSearchCallsPerBook, EnabledSourcesBudget), so variants are
// ordered by expected value and the tail is dropped rather than asked.
const maxQueryVariants = 4

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
		p.Series, p.Position, t = strings.TrimSpace(m[1]), m[2], strings.TrimSpace(m[3])
	} else if m := bareSeriesNumber.FindStringSubmatch(t); m != nil && !slotWordRe.MatchString(strings.TrimSpace(m[1])) {
		p.Series, p.Position, t = strings.TrimSpace(m[1]), m[2], strings.TrimSpace(m[3])
	} else if loc := seriesDecoration.FindStringIndex(t); loc != nil {
		base, series, bookName, found := splitSeriesDecoration(t)
		if found && series != "" {
			p.Series = series
			p.Position = decorationNumberRe.FindString(t[loc[0]:loc[1]])
			if bookName != "" {
				t = bookName
			} else if strings.TrimSpace(base) != "" {
				t = strings.TrimSpace(base)
				p.TitleIsSeries = p.Position != ""
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
		results = keepSeriesSlot(results, v.slotSeries, v.slotPosition)
	}
	if v.filter != nil {
		results = keepVariant(results, *v.filter, people)
	}
	return results
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
	if people != "" && (baseFilter != nil || slotSeries != "" || strings.EqualFold(strings.TrimSpace(base), strings.TrimSpace(literal))) {
		add(mk(variantTitleOnly, base, "", baseFilter))
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
}

func (c strongCriteria) matches(r metadata.BookMetadata) bool {
	if c.asin != "" && strings.EqualFold(strings.TrimSpace(r.ASIN), c.asin) {
		return true
	}
	if strings.TrimSpace(c.people) == "" || !sharesPersonWord(r.Author+" "+r.Narrator, c.people) {
		return false
	}
	if c.slotSeries != "" {
		if len(keepSeriesSlot([]metadata.BookMetadata{r}, c.slotSeries, c.slotPos)) == 0 {
			return false
		}
	} else {
		if len(c.titleWords) == 0 {
			return false
		}
		words := SignificantWords(r.Title)
		for w := range c.titleWords {
			if !words[w] {
				return false
			}
		}
	}
	if c.bookDur > 0 {
		return r.DurationSec > 0 && durationDeltaRatio(c.bookDur, r.DurationSec) <= strongRuntimeTolerance
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
// its ISBNs and its normalized title+author. A result is a duplicate when
// ANY key was already pooled, so an Audible record and an Open Library record
// of the same book collapse to the first one even when only one carries an ID.
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

// add records r's keys and reports whether r is new.
func (s candidateSeen) add(r metadata.BookMetadata) bool {
	keys := candidateKeys(r)
	for _, k := range keys {
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
)

func sourcePolicyFor(providerID string) sourcePolicy {
	switch providerID {
	case metadata.SourceIDAudible, metadata.SourceIDOpenLibrary, "":
		return policyFanOut
	case metadata.SourceIDAudnexus:
		return policyASINOnly
	default:
		return policyBestOnly
	}
}

// MaxSearchCallsPerBook is the most title-search requests one book's search
// sends the provider with this config id (cache hits and early stop send
// fewer). ASIN lookups are outside it: Audible gets at most one more (the
// book's own ASIN) and Audnexus, which gets no title search, at most
// 1+maxASINEnrich lookups, each up to len(audnexusRegions) requests. The
// candidate op sizes its workers and reports its binding source from this.
func MaxSearchCallsPerBook(providerID string) int {
	switch sourcePolicyFor(providerID) {
	case policyFanOut:
		return maxQueryVariants
	case policyBestOnly:
		return 1
	default:
		return 0
	}
}
