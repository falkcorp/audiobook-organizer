// file: internal/plugins/metafetch/asin_match.go
// version: 1.3.0
// guid: 3f7c1b9e-58a2-4d0f-9e61-c2a4b8d07e15
// last-edited: 2026-10-01

package metafetch

// The ASIN match gate for metafetch.asin-backfill.
//
// A wrong ASIN is worse than none: metadata fetch looks a book up by its ASIN
// and dedup's book:asin index groups books that share one, so a wrong ASIN
// pulls the wrong metadata onto the book and merges it with a stranger. The
// gate is therefore conservative by construction. A candidate passes only when
// ALL of the following hold:
//
//  1. Title: normalized equality, or a ONE-SIDED subtitle difference (one side
//     has a ":" / " - " subtitle the other lacks, or Audible's separate
//     subtitle field completes the title). A difference that is only a number
//     designator ("2", "Book 2", "Part II", "#3") is a reject, and so is the
//     old 60%-prefix rule's favourite false positive ("Red Rising" vs "Red
//     Rising 2"), which simply is not equal.
//  2. Author: some product author normalizes equal to some live author of the
//     book (Book.AuthorID or the book_authors join).
//  3. No veto: same series name with a different sequence is a reject; a known
//     complete book runtime more than runtimeVetoRatio away from the product's
//     runtime is a reject (it is a different edition -- abridged, dramatized).
//  4. Corroboration, at least one of: the audiobook ISBN equals a book ISBN;
//     runtime within runtimeCorroborateRatio; series name AND sequence agree.
//     A PARTIAL title match (one side has a subtitle the other lacks) needs
//     the ISBN or two corroborations: a bare franchise title ("Star Wars")
//     heads a dozen of the author's products, and one of them is always within
//     10% of the book's runtime.
//  5. Edition: an abridged product, or one whose title/subtitle says it is a
//     dramatization / adaptation / full-cast production, passes only with
//     runtime corroboration. A box set or range ("Books 1-3", series sequence
//     "1-3", "Omnibus") is a different product and is rejected.
//  6. A volume number in the book's own trailing parenthetical ("Red Rising
//     (Book 2)") must equal a series sequence of the product.
//
// decideASIN then requires EXACTLY ONE distinct passing ASIN; two or more is
// ambiguous and nothing is written.

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

const (
	// runtimeCorroborateRatio: |book - product| / product at or below this is
	// runtime corroboration.
	runtimeCorroborateRatio = 0.10
	// runtimeVetoRatio: above this, with both runtimes known, the product is a
	// different edition and is rejected whatever else agrees.
	runtimeVetoRatio = 0.25
)

// asinBookFacts is what the gate knows about one of our books.
type asinBookFacts struct {
	Title   string
	Authors []string
	// ISBNs are the book's ISBN-10/13 values as stored (normalized inside).
	ISBNs []string
	// RuntimeSec is the book's COMPLETE runtime (database.BookRuntime
	// KnownSeconds); 0 means unknown, and an unknown runtime neither
	// corroborates nor vetoes.
	RuntimeSec int
	// SeriesName / SeriesSeq: SeriesSeq <= 0 means no known position.
	SeriesName string
	SeriesSeq  int
}

// Reject reasons and evidence kinds. They are counted in the op result, so
// keep them stable.
const (
	rejectNoASIN          = "no_asin"
	rejectTitle           = "title"
	rejectAuthor          = "author"
	rejectSeriesConflict  = "series_conflict"
	rejectRuntimeConflict = "runtime_conflict"
	rejectUncorroborated  = "uncorroborated"
	rejectPartialTitle    = "partial_title_weak"
	rejectEdition         = "edition"
	rejectBoxSet          = "box_set"
	rejectVolume          = "volume_mismatch"

	evidenceISBN    = "isbn"
	evidenceRuntime = "runtime"
	evidenceSeries  = "series"
)

// asinVerdict is the gate's answer for one candidate.
type asinVerdict struct {
	Pass     bool
	Reason   string   // reject reason when !Pass
	Evidence []string // corroboration kinds when Pass
}

// evaluateASINCandidate runs the gate on one catalog product.
func evaluateASINCandidate(b asinBookFacts, c metadata.AudibleIdentity) asinVerdict {
	if strings.TrimSpace(c.ASIN) == "" {
		return asinVerdict{Reason: rejectNoASIN}
	}
	if isBoxSet(c.Title) || isBoxSet(c.Subtitle) {
		return asinVerdict{Reason: rejectBoxSet}
	}
	title, ok := asinTitleMatch(b.Title, c.Title, c.Subtitle)
	if !ok {
		return asinVerdict{Reason: rejectTitle}
	}
	if !asinAuthorMatches(b.Authors, c.Authors) {
		return asinVerdict{Reason: rejectAuthor}
	}
	// A volume number the book carries only in its own trailing parenthetical
	// ("Red Rising (Book 2)") was stripped for the title compare; it must
	// still name the product's position.
	if n, has := titleParenVolume(b.Title); has {
		agree := false
		for _, s := range c.Series {
			if seq, ok := parseSeriesSequence(s.Sequence); ok && seq == n {
				agree = true
				break
			}
		}
		if !agree {
			return asinVerdict{Reason: rejectVolume}
		}
	}

	var evidence []string
	seriesAgree := false
	if bName := normSeriesName(b.SeriesName); bName != "" && b.SeriesSeq > 0 {
		for _, s := range c.Series {
			if normSeriesName(s.Title) != bName {
				continue
			}
			if isSequenceRange(s.Sequence) {
				return asinVerdict{Reason: rejectBoxSet}
			}
			seq, ok := parseSeriesSequence(s.Sequence)
			if !ok {
				continue
			}
			if seq == float64(b.SeriesSeq) {
				seriesAgree = true
			} else {
				return asinVerdict{Reason: rejectSeriesConflict}
			}
		}
	}
	for _, s := range c.Series {
		// A range anywhere ("1-3") is a box set, whatever the series name.
		if isSequenceRange(s.Sequence) {
			return asinVerdict{Reason: rejectBoxSet}
		}
	}

	runtimeAgree := false
	if b.RuntimeSec > 0 && c.RuntimeMin > 0 {
		prod := float64(c.RuntimeMin * 60)
		ratio := math.Abs(float64(b.RuntimeSec)-prod) / prod
		if ratio > runtimeVetoRatio {
			return asinVerdict{Reason: rejectRuntimeConflict}
		}
		runtimeAgree = ratio <= runtimeCorroborateRatio
	}

	isbnAgree := false
	if pi := normISBN13(c.ISBN); pi != "" {
		for _, bi := range b.ISBNs {
			if normISBN13(bi) == pi {
				isbnAgree = true
				break
			}
		}
	}
	if isbnAgree {
		evidence = append(evidence, evidenceISBN)
	}
	if runtimeAgree {
		evidence = append(evidence, evidenceRuntime)
	}
	if seriesAgree {
		evidence = append(evidence, evidenceSeries)
	}
	if len(evidence) == 0 {
		return asinVerdict{Reason: rejectUncorroborated}
	}
	if title == titlePartial && !isbnAgree && len(evidence) < 2 {
		return asinVerdict{Reason: rejectPartialTitle}
	}
	if isRiskyEdition(c) && !runtimeAgree {
		return asinVerdict{Reason: rejectEdition}
	}
	return asinVerdict{Pass: true, Evidence: evidence}
}

// Decision outcomes.
const (
	asinOutcomeMatched   = "matched"
	asinOutcomeAmbiguous = "ambiguous"
	asinOutcomeNoMatch   = "no_match"
)

// asinDecision is decideASIN's answer for one book.
type asinDecision struct {
	Outcome  string
	ASIN     string   // set only when Outcome == matched
	Evidence []string // of the matched candidate
	Title    string   // matched product title, for the log line
	// ISBN is the matched product's audiobook ISBN as ISBN-13, "" when Audible
	// gave none or it is malformed.
	ISBN string
	// Passing lists every distinct passing ASIN (len > 1 when ambiguous).
	Passing []string
	// Rejects counts reject reasons over the distinct candidates.
	Rejects map[string]int
}

// decideASIN applies the gate to every candidate (deduplicated by ASIN, first
// occurrence wins) and returns matched only when exactly one distinct ASIN
// passes.
func decideASIN(b asinBookFacts, cands []metadata.AudibleIdentity) asinDecision {
	d := asinDecision{Rejects: map[string]int{}}
	seen := map[string]bool{}
	var match asinVerdict
	var matchTitle, matchISBN string
	for _, c := range cands {
		key := strings.ToUpper(strings.TrimSpace(c.ASIN))
		if key != "" && seen[key] {
			continue
		}
		if key != "" {
			seen[key] = true
		}
		v := evaluateASINCandidate(b, c)
		if !v.Pass {
			d.Rejects[v.Reason]++
			continue
		}
		d.Passing = append(d.Passing, strings.TrimSpace(c.ASIN))
		if len(d.Passing) == 1 {
			match, matchTitle, matchISBN = v, c.Title, normISBN13(c.ISBN)
		}
	}
	switch len(d.Passing) {
	case 0:
		d.Outcome = asinOutcomeNoMatch
	case 1:
		d.Outcome = asinOutcomeMatched
		d.ASIN = d.Passing[0]
		d.Evidence = match.Evidence
		d.Title = matchTitle
		d.ISBN = matchISBN
	default:
		d.Outcome = asinOutcomeAmbiguous
	}
	return d
}

// ---------------------------------------------------------------------------
// Title
// ---------------------------------------------------------------------------

// Title match kinds.
const (
	titleExact   = 1 // the titles agree (an Audible subtitle that is only a series note counts)
	titlePartial = 2 // one side carries a subtitle the other lacks
)

// asinTitleMatch is gate rule 1. See the package comment above. It reports
// whether the titles match and, when they do, how strongly.
func asinTitleMatch(bookTitle, candTitle, candSubtitle string) (int, bool) {
	bRaw := stripSeriesParen(bookTitle)
	cRaw := stripSeriesParen(candTitle)
	bt, ct := normTitle(bRaw), normTitle(cRaw)
	if bt == "" || ct == "" {
		return 0, false
	}
	cSub := normTitle(candSubtitle)

	if bt == ct {
		// Bare titles agree. A separate Audible subtitle that is only a number
		// designator ("Book 2") names a different volume of a same-titled set.
		if cSub != "" && isNumberDesignator(cSub) {
			return 0, false
		}
		// A subtitle the book lacks makes this partial, unless it only
		// names the series ("Red Rising Saga, Book 1").
		if cSub != "" && !seriesParenRe.MatchString(candSubtitle) {
			return titlePartial, true
		}
		return titleExact, true
	}
	// Audible keeps the subtitle in its own field; the book may carry it in
	// the title ("Golden Son: Book II of the Red Rising Trilogy").
	if cSub != "" && bt == normTitle(cRaw+" "+candSubtitle) {
		return titleExact, true
	}

	bHead, bTail, bHas := splitSubtitle(bRaw)
	cHead, cTail, cHas := splitSubtitle(cRaw)
	switch {
	case bHas && !cHas:
		// Book has a subtitle the product title lacks. If Audible reports its
		// own subtitle and it differs from the book's, both sides carry a
		// different subtitle: reject.
		if normTitle(bHead) != ct || isNumberDesignator(normTitle(bTail)) {
			return 0, false
		}
		if cSub == "" {
			return titlePartial, true
		}
		if cSub == normTitle(bTail) {
			return titleExact, true
		}
		return 0, false
	case cHas && !bHas:
		if normTitle(cHead) == bt && !isNumberDesignator(normTitle(cTail)) {
			return titlePartial, true
		}
		return 0, false
	}
	// Both have subtitles that differ, or neither has one and the titles
	// differ: not the same title.
	return 0, false
}

// subtitleSeparators, in priority order. A bare "-" is not one: hyphenated
// words ("Half-Blood") must not split.
var subtitleSeparators = []string{":", " - ", " – ", " — "}

// splitSubtitle splits raw at its first subtitle separator.
func splitSubtitle(raw string) (head, tail string, ok bool) {
	best := -1
	sepLen := 0
	for _, sep := range subtitleSeparators {
		if i := strings.Index(raw, sep); i > 0 && (best < 0 || i < best) {
			best, sepLen = i, len(sep)
		}
	}
	if best < 0 {
		return raw, "", false
	}
	head, tail = strings.TrimSpace(raw[:best]), strings.TrimSpace(raw[best+sepLen:])
	if head == "" || tail == "" {
		return raw, "", false
	}
	return head, tail, true
}

var (
	trailingParenRe = regexp.MustCompile(`\s*[(\[]([^()\[\]]*)[)\]]\s*$`)
	// seriesParenRe: a trailing parenthetical that only names the series or
	// the edition ("(Red Rising Saga, Book 1)", "(Unabridged)").
	seriesParenRe = regexp.MustCompile(`(?i)\b(book|bk|volume|vol|series|saga|trilogy|cycle|chronicles|novel|unabridged)\b|#\s*\d`)
	// partOfRe: "(1 of 3)" is a split release, never noise.
	partOfRe = regexp.MustCompile(`(?i)\d+\s+of\s+\d+`)
)

// stripSeriesParen removes ONE trailing parenthetical when it is a series or
// edition note. Brackets such as "[Dramatized Adaptation]" and split-release
// markers such as "(1 of 3)" are kept: they name a different product.
func stripSeriesParen(raw string) string {
	raw = strings.TrimSpace(raw)
	m := trailingParenRe.FindStringSubmatchIndex(raw)
	if m == nil {
		return raw
	}
	inner := raw[m[2]:m[3]]
	if strings.Contains(raw[m[0]:m[1]], "[") {
		return raw
	}
	if partOfRe.MatchString(inner) || !seriesParenRe.MatchString(inner) {
		return raw
	}
	if out := strings.TrimSpace(raw[:m[0]]); out != "" {
		return out
	}
	return raw
}

// normTitle lowercases, folds "&" to "and", drops apostrophes, turns every
// other non-alphanumeric into a space, collapses spaces and drops a leading
// "the".
func normTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "&", " and ")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\'' || r == '’' || r == '‘':
			// "Ender's" == "Enders"
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	out = strings.TrimPrefix(out, "the ")
	return out
}

// numberWords spells volume numbers out ("book thirteen", "part the second"
// is not handled; it fails toward a miss, never a wrong ASIN).
const numberWords = `one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|` +
	`sixteen|seventeen|eighteen|nineteen|twenty|` +
	`first|second|third|fourth|fifth|sixth|seventh|eighth|ninth|tenth|eleventh|twelfth|thirteenth|` +
	`fourteenth|fifteenth|sixteenth|seventeenth|eighteenth|nineteenth|twentieth`

// numberDesignatorRe: one or more "<word> <number>" groups, each number
// optionally a range or "N of M" ("book 2", "part 1 of 3", "volume 1 part 2",
// "books 1 3", "season 2", "thirteen").
var numberDesignatorRe = regexp.MustCompile(
	`^(?:(?:books?|bk|parts?|pt|volumes?|vol|no|number|episodes?|ep|installment|season|series|chapter)?\s*` +
		`(?:\d+|[ivxlcdm]+|` + numberWords + `)` +
		`(?:\s*(?:of|to|and)?\s*(?:\d+|[ivxlcdm]+|` + numberWords + `))?\s*)+$`)

// isNumberDesignator reports whether a NORMALIZED title fragment is only a
// volume number ("2", "book 2", "part ii", "volume three"). "#3" normalizes to
// "3".
func isNumberDesignator(norm string) bool {
	if norm == "" {
		return false
	}
	return numberDesignatorRe.MatchString(norm)
}

// ---------------------------------------------------------------------------
// Author
// ---------------------------------------------------------------------------

// asinAuthorMatches is gate rule 2.
func asinAuthorMatches(bookAuthors, candAuthors []string) bool {
	want := map[string]bool{}
	for _, a := range bookAuthors {
		for _, n := range normAuthorForms(a) {
			want[n] = true
		}
	}
	if len(want) == 0 {
		return false
	}
	for _, a := range candAuthors {
		for _, n := range normAuthorForms(a) {
			if want[n] {
				return true
			}
		}
	}
	return false
}

// normAuthorForms returns the comparable form of one author name. A name
// with exactly one comma that is not a suffix ("Jr.") is "Last, First" and
// yields ONLY the swap ("Le Guin, Ursula K." -> "ursulakleguin"): offering the
// as-written form too would make "Henry, James" (James Henry) equal "Henry
// James", a different person. A comma-joined list ("Pierce Brown, Tim
// Reynolds") then matches nobody, which fails toward a miss. A " - role"
// suffix ("Ken Liu - translator") is cut first. The form keeps only letters
// and digits, so "J.R.R. Tolkien", "J. R. R. Tolkien" and "Tolkien, J.R.R."
// agree.
func normAuthorForms(s string) []string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, " - "); i > 0 {
		s = s[:i]
	}
	if parts := strings.Split(s, ","); len(parts) == 2 {
		last, first := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if last != "" && first != "" && !nameSuffixes[normTitle(first)] {
			if n := alnumLower(first + " " + last); n != "" {
				return []string{n}
			}
			return nil
		}
	}
	if n := alnumLower(s); n != "" {
		return []string{n}
	}
	return nil
}

func alnumLower(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// nameSuffixes are what may follow a comma in a name without it being
// "Last, First" order.
var nameSuffixes = map[string]bool{"jr": true, "sr": true, "ii": true, "iii": true, "iv": true, "phd": true, "md": true}

// ---------------------------------------------------------------------------
// Series / ISBN
// ---------------------------------------------------------------------------

// normSeriesName is normTitle without a trailing "series"/"saga"/"trilogy".
func normSeriesName(s string) string {
	n := normTitle(s)
	for _, suf := range []string{" series", " saga", " trilogy"} {
		if strings.HasSuffix(n, suf) && len(n) > len(suf) {
			n = strings.TrimSuffix(n, suf)
			break
		}
	}
	return n
}

var seqNumberRe = regexp.MustCompile(`\d+(\.\d+)?`)

// seqRangeRe: a sequence that names several positions ("1-3", "1–7",
// "1, 2, 3", "1 to 3") -- a box set, never one book.
var seqRangeRe = regexp.MustCompile(`\d\s*(?:[-–—,&+]|to|and)\s*\d`)

// isSequenceRange reports whether an Audible series sequence is a range.
func isSequenceRange(s string) bool {
	return seqRangeRe.MatchString(strings.ToLower(s))
}

// boxSetRe names a multi-book product in a title or subtitle.
var boxSetRe = regexp.MustCompile(`(?i)\b(omnibus|box(?:ed)?\s*set|boxset|complete\s+(?:series|collection|trilogy|saga)|` +
	`books?\s+\d+\s*(?:-|–|—|to|through|thru|and|&)\s*\d+|\d+[\s-]*book\s+(?:set|bundle|collection))\b`)

// isBoxSet reports whether a product title or subtitle names a box set.
func isBoxSet(s string) bool {
	return s != "" && boxSetRe.MatchString(s)
}

// riskyEditionRe names a production that is a different listen from the
// book even under the same title.
var riskyEditionRe = regexp.MustCompile(`(?i)\b(dramati[sz](?:ed|ation)|adaptation|adapted|full[\s-]*cast|radio\s+(?:drama|play|production|collection)|bbc\s+radio|abridged)\b`)

// isRiskyEdition reports whether c is abridged or a dramatized/adapted
// production. Such a product passes only with runtime corroboration.
func isRiskyEdition(c metadata.AudibleIdentity) bool {
	if strings.EqualFold(strings.TrimSpace(c.FormatType), "abridged") {
		return true
	}
	return riskyEditionRe.MatchString(c.Title) || riskyEditionRe.MatchString(c.Subtitle)
}

// parenNumberRe finds the number in a stripped parenthetical.
var parenNumberRe = regexp.MustCompile(`#?\s*(\d+(?:\.\d+)?)`)

// titleParenVolume returns the volume number in the trailing parenthetical
// that stripSeriesParen would remove ("Red Rising (Book 2)" -> 2), and
// whether there is one. A parenthetical that is kept, or names no number,
// reports false.
func titleParenVolume(raw string) (float64, bool) {
	raw = strings.TrimSpace(raw)
	stripped := stripSeriesParen(raw)
	if stripped == raw {
		return 0, false
	}
	m := parenNumberRe.FindStringSubmatch(raw[len(stripped):])
	if m == nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// parseSeriesSequence reads the first number in an Audible sequence ("1",
// "1.5", "Book 2"). Callers reject a range (isSequenceRange) first.
func parseSeriesSequence(s string) (float64, bool) {
	m := seqNumberRe.FindString(s)
	if m == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(m, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// normISBN13 returns s as a 13-digit ISBN (ISBN-10 converted), or "" when it is
// not a well-formed ISBN of either length.
func normISBN13(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= '0' && r <= '9') || r == 'X' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	switch len(d) {
	case 13:
		if strings.ContainsRune(d, 'X') {
			return ""
		}
		return d
	case 10:
		if strings.ContainsRune(d[:9], 'X') {
			return ""
		}
		core := "978" + d[:9]
		sum := 0
		for i, r := range core {
			n := int(r - '0')
			if i%2 == 1 {
				n *= 3
			}
			sum += n
		}
		return core + strconv.Itoa((10-sum%10)%10)
	}
	return ""
}
