// file: internal/plugins/metafetch/asin_match.go
// version: 1.7.0
// guid: 3f7c1b9e-58a2-4d0f-9e61-c2a4b8d07e15
// last-edited: 2026-10-02

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
	"fmt"
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
	// rejectUnpositionedNote is a partial match whose subtitle numbers a
	// series volume on a book with no recorded position.
	rejectUnpositionedNote = "series_note_unpositioned"
	rejectEdition          = "edition"
	rejectBoxSet           = "box_set"
	rejectVolume           = "volume_mismatch"

	evidenceISBN    = "isbn"
	evidenceRuntime = "runtime"
	evidenceSeries  = "series"
)

// asinVerdict is the gate's answer for one candidate.
type asinVerdict struct {
	Pass     bool
	Reason   string   // reject reason when !Pass (a stable code, counted)
	Evidence []string // corroboration kinds when Pass
	// Detail is the reject reason with the concrete values that fired it
	// ("runtime_conflict: book 612m vs audible 1043m"), for the owner to
	// read on a listed row. Never used as a count key: the values vary.
	Detail string
}

// reject builds a failing verdict whose Detail is the reason code followed by
// the formatted values (or the bare code when format is empty).
func reject(reason, format string, args ...any) asinVerdict {
	d := reason
	if format != "" {
		d = reason + ": " + fmt.Sprintf(format, args...)
	}
	return asinVerdict{Reason: reason, Detail: d}
}

// evaluateASINCandidate runs the gate on one catalog product.
func evaluateASINCandidate(b asinBookFacts, c metadata.AudibleIdentity) asinVerdict {
	if strings.TrimSpace(c.ASIN) == "" {
		return reject(rejectNoASIN, "")
	}
	if isBoxSet(c.Title) || isBoxSet(c.Subtitle) {
		return reject(rejectBoxSet, "audible title %q subtitle %q names a multi-book set", c.Title, c.Subtitle)
	}
	title, ok := asinTitleMatch(b.Title, c.Title, c.Subtitle)
	if !ok {
		return reject(rejectTitle, "book %q vs audible %q subtitle %q", b.Title, c.Title, c.Subtitle)
	}
	if !asinAuthorMatches(b.Authors, c.Authors) {
		return reject(rejectAuthor, "book %q vs audible %q", strings.Join(b.Authors, "; "), strings.Join(c.Authors, "; "))
	}
	// A volume number the book carries only in its own trailing parenthetical
	// ("Red Rising (Book 2)") was stripped for the title compare; it must
	// still name the product's position, in the series that is the book's
	// (by name) when the product lists it, so a universe series ("Cosmere
	// 2") cannot stand in for the book's own ("Mistborn 1").
	parenVol, hasParenVol := titleParenVolume(b.Title)
	if hasParenVol {
		agree := false
		rel := relevantSeries(b, c)
		for _, s := range rel {
			if seq, ok := parseSeriesSequence(s.Sequence); ok && seq == parenVol {
				agree = true
				break
			}
		}
		if !agree {
			return reject(rejectVolume, "book title %q names volume %g; audible series %s", b.Title, parenVol, seriesRefsLabel(rel))
		}
	}
	// A series-note subtitle is exact only when its number (if any) is the
	// book's known position. "Overlord, Vol. 2" against a book at #1 is a
	// different volume; against a book with no known position it is only a
	// partial match (it needs the ISBN or two corroborations).
	unpositionedNote := false
	if title == titleSeriesNote {
		known := float64(0)
		switch {
		case b.SeriesSeq > 0:
			known = float64(b.SeriesSeq)
		case hasParenVol:
			known = parenVol
		}
		note := readSeriesNote(c.Subtitle, seriesNameIn(c.Subtitle, b))
		switch {
		case note.split:
			// "Volume 1, Part 2": parts of one volume run to similar
			// lengths, so only the ISBN may confirm one.
			title = titleSplit
		case note.unreadable:
			// A number-like token we cannot place: never exact.
			title = titlePartial
		case len(note.nums) == 0:
			title = titleExact
		case len(note.nums) > 1:
			title = titlePartial
		case known > 0 && note.nums[0] != known:
			return reject(rejectVolume, "book position %g vs audible subtitle %q (volume %g)", known, c.Subtitle, note.nums[0])
		case known > 0 && !subtitleNamesSeries(c.Subtitle, b):
			// The number agrees but the note names another series ("The
			// First Law, Book 1" on a book in some other series at #1).
			title = titlePartial
		case known > 0:
			title = titleExact
		default:
			// The book records no position, so a numbered note cannot be
			// checked. Counted apart so a dry run sizes what this costs.
			title = titlePartial
			unpositionedNote = true
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
				return reject(rejectBoxSet, "audible series %q sequence %q is a range", s.Title, s.Sequence)
			}
			seq, ok := parseSeriesSequence(s.Sequence)
			if !ok {
				continue
			}
			if seq == float64(b.SeriesSeq) {
				seriesAgree = true
			} else {
				return reject(rejectSeriesConflict, "book %q #%d vs audible %q #%s", b.SeriesName, b.SeriesSeq, s.Title, s.Sequence)
			}
		}
	}
	for _, s := range c.Series {
		// A range anywhere ("1-3") is a box set, whatever the series name.
		if isSequenceRange(s.Sequence) {
			return reject(rejectBoxSet, "audible series %q sequence %q is a range", s.Title, s.Sequence)
		}
	}

	runtimeAgree := false
	if b.RuntimeSec > 0 && c.RuntimeMin > 0 {
		prod := float64(c.RuntimeMin * 60)
		ratio := math.Abs(float64(b.RuntimeSec)-prod) / prod
		if ratio > runtimeVetoRatio {
			return reject(rejectRuntimeConflict, "book %dm vs audible %dm (off %.0f%%, limit %.0f%%)",
				b.RuntimeSec/60, c.RuntimeMin, ratio*100, runtimeVetoRatio*100)
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
	// Vetoes before corroboration: checkKnownIdentity accepts an
	// uncorroborated or weak-partial product (the stored ASIN is the claim),
	// so every veto must have run by the time those two can be returned.
	if isRiskyEdition(c) && !runtimeAgree {
		return reject(rejectEdition, "audible format %q title %q subtitle %q is abridged/dramatized and the runtime does not corroborate it (%s)",
			c.FormatType, c.Title, c.Subtitle, runtimeLabel(b.RuntimeSec, c.RuntimeMin))
	}
	if len(evidence) == 0 {
		return reject(rejectUncorroborated, "no isbn, runtime or series agreement (%s)", runtimeLabel(b.RuntimeSec, c.RuntimeMin))
	}
	if title == titlePartial && !isbnAgree && len(evidence) < 2 {
		if unpositionedNote {
			return reject(rejectUnpositionedNote, "audible subtitle %q numbers a volume; book has no series position", c.Subtitle)
		}
		return reject(rejectPartialTitle, "partial title match with only %s", strings.Join(evidence, "+"))
	}
	if title == titleSplit && !isbnAgree {
		return reject(rejectPartialTitle, "audible subtitle %q is part of a split volume; only an isbn can confirm it", c.Subtitle)
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
	titleExact   = 1 // the titles agree
	titlePartial = 2 // one side carries a subtitle the other lacks
	// titleSeriesNote: the titles agree and Audible's subtitle is a series
	// note ("Red Rising Saga, Book 1"). Exact only when a number in that
	// note equals the book's known position; see evaluateASINCandidate.
	titleSeriesNote = 3
	// titleSplit: a series note that names a part of a volume ("Volume 1,
	// Part 2"). Only an ISBN match may confirm it.
	titleSplit = 4
)

// seriesNoteRe: an Audible subtitle that only places the book in a series.
var seriesNoteRe = regexp.MustCompile(`(?i)\b(book|bk|volume|vol|series|saga|trilogy|cycle|chronicles|sequence)\b|#\s*\d`)

// companionRe: a subtitle naming a related but different work. Never a
// series note, whatever else it says.
var companionRe = regexp.MustCompile(`(?i)\b(companion|prequel|sequel|novella|novelette|short\s+stor(?:y|ies)|side\s+story|spin[\s-]*off|tie[\s-]*in|anthology|collection|guide|world\s+of)\b`)

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
		if cSub == "" {
			return titleExact, true
		}
		if seriesNoteRe.MatchString(candSubtitle) && !companionRe.MatchString(candSubtitle) {
			return titleSeriesNote, true
		}
		return titlePartial, true
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

// formatParenRe: a series parenthetical that names a format or edition, not
// a sub-series. "(Light Novel)" is stripped; "(City Watch)" is a different
// sub-series of Discworld and is kept.
var formatParenRe = regexp.MustCompile(`(?i)^\s*(light\s+novels?|novels?|manga|audio\s*drama|dramati[sz]ed|unabridged|abridged|audiobooks?|books?|collection|series|graphic\s+audio)\s*$`)

// normSeriesName is normTitle without a trailing FORMAT parenthetical ("(Light
// Novel)", "(Novels)") and without trailing qualifiers ("series", "saga",
// "trilogy", "cycle", "chronicles", "sequence"), so the same series under two
// spellings compares equal and the series veto is not skipped by a qualifier.
// Sub-series parentheticals ("Discworld (City Watch)") and "Universe" are
// kept: they name a different series.
func normSeriesName(s string) string {
	s = strings.TrimSpace(s)
	for {
		m := trailingParenRe.FindStringSubmatchIndex(s)
		if m == nil || m[0] == 0 || !formatParenRe.MatchString(s[m[2]:m[3]]) {
			break
		}
		s = strings.TrimSpace(s[:m[0]])
	}
	n := normTitle(s)
	for changed := true; changed; {
		changed = false
		for _, suf := range []string{" series", " saga", " trilogy", " cycle", " chronicles", " sequence"} {
			if strings.HasSuffix(n, suf) && len(n) > len(suf) {
				n = strings.TrimSuffix(n, suf)
				changed = true
			}
		}
	}
	return n
}

// relevantSeries is the product's series that name the book's series (or,
// with none recorded, the book's title); all of them when none does.
func relevantSeries(b asinBookFacts, c metadata.AudibleIdentity) []metadata.AudibleSeriesRef {
	want := normSeriesName(b.SeriesName)
	if want == "" {
		want = normSeriesName(stripSeriesParen(b.Title))
	}
	var out []metadata.AudibleSeriesRef
	for _, s := range c.Series {
		if want != "" && normSeriesName(s.Title) == want {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return c.Series
	}
	return out
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

// volumeNumberRe finds a number named as a volume ("Book 2", "Vol. 3",
// "#4", "Part 2").
var volumeNumberRe = regexp.MustCompile(`(?i)(?:\bbooks?|\bbk|\bvol(?:ume)?|\bpart|\bno|#)\.?\s*(\d+(?:\.\d+)?)`)

// bareNumberRe finds any number.
var bareNumberRe = regexp.MustCompile(`\d+(?:\.\d+)?`)

// volumeIn returns the volume a note names: the number after a volume word,
// or, with none, the note's only number when it is not a year.
func volumeIn(note string) (float64, bool) {
	if m := volumeNumberRe.FindStringSubmatch(note); m != nil {
		if f, err := strconv.ParseFloat(m[1], 64); err == nil {
			return f, true
		}
	}
	nums := bareNumberRe.FindAllString(note, -1)
	if len(nums) != 1 {
		return 0, false
	}
	f, err := strconv.ParseFloat(nums[0], 64)
	if err != nil || (f >= 1800 && f <= 2100) {
		return 0, false
	}
	return f, true
}

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
	return volumeIn(raw[len(stripped):])
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

// Known-identity verdicts (checkKnownIdentity).
const (
	identityOK         = "ok"
	identityUnverified = "unverified"
	identitySuspect    = "suspect"
)

// checkKnownIdentity judges a product looked up BY the book's own stored
// ASIN. The ASIN is the identity claim, so no corroboration is needed, but
// every veto of the gate still applies: a product that is a box set, another
// volume, a different edition (abridged/dramatized without a runtime match)
// or far off in runtime is not this book, and its ISBN must not be written.
// A product that fails only the title or author rule is "unverified": a
// differently formatted title or credit causes that on a correct ASIN.
//
// It also returns why a non-OK verdict was reached: the stable reason code
// (the key the run tallies by, e.g. "runtime_conflict") and the detail with
// the concrete values ("runtime_conflict: book 612m vs audible 1043m ..."),
// so a listed row says which rule fired. The gate stops at the first rule
// that fails, so there is exactly one reason; an OK verdict has none.
func checkKnownIdentity(b asinBookFacts, c metadata.AudibleIdentity) (verdict, reason, detail string) {
	v := evaluateASINCandidate(b, c)
	switch {
	case v.Pass:
		return identityOK, "", ""
	case v.Reason == rejectUncorroborated || v.Reason == rejectPartialTitle || v.Reason == rejectUnpositionedNote:
		return identityOK, "", ""
	case v.Reason == rejectTitle || v.Reason == rejectAuthor:
		return identityUnverified, v.Reason, v.Detail
	default:
		return identitySuspect, v.Reason, v.Detail
	}
}

// seriesRefsLabel renders a product's series refs for a reject detail.
func seriesRefsLabel(refs []metadata.AudibleSeriesRef) string {
	if len(refs) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(refs))
	for _, r := range refs {
		parts = append(parts, fmt.Sprintf("%q #%s", r.Title, r.Sequence))
	}
	return strings.Join(parts, ", ")
}

// runtimeLabel renders the two runtimes for a reject detail; an unknown side
// says so, since an unknown runtime neither corroborates nor vetoes.
func runtimeLabel(bookSec, audibleMin int) string {
	b, a := "unknown", "unknown"
	if bookSec > 0 {
		b = fmt.Sprintf("%dm", bookSec/60)
	}
	if audibleMin > 0 {
		a = fmt.Sprintf("%dm", audibleMin)
	}
	return "runtime book " + b + " vs audible " + a
}

// romanValues are the roman numerals read as a volume, only directly after a
// volume word ("Book II", "Volume IV"): alone, "i", "v", "mix" and "civil"
// are words.
var romanValues = map[string]float64{
	"i": 1, "ii": 2, "iii": 3, "iv": 4, "v": 5, "vi": 6, "vii": 7, "viii": 8, "ix": 9, "x": 10,
	"xi": 11, "xii": 12, "xiii": 13, "xiv": 14, "xv": 15, "xvi": 16, "xvii": 17, "xviii": 18, "xix": 19, "xx": 20,
}

// wordValues are spelled-out cardinals and ordinals.
var wordValues = func() map[string]float64 {
	m := map[string]float64{}
	for i, w := range strings.Split("one two three four five six seven eight nine ten eleven twelve thirteen fourteen fifteen sixteen seventeen eighteen nineteen twenty", " ") {
		m[w] = float64(i + 1)
	}
	for i, w := range strings.Split("first second third fourth fifth sixth seventh eighth ninth tenth eleventh twelfth thirteenth fourteenth fifteenth sixteenth seventeenth eighteenth nineteenth twentieth", " ") {
		m[w] = float64(i + 1)
	}
	return m
}()

var volumeWords = map[string]bool{"book": true, "books": true, "bk": true, "volume": true, "vol": true, "no": true, "number": true, "episode": true, "ep": true, "installment": true, "season": true}

var ordinalDigitsRe = regexp.MustCompile(`^(\d+)(st|nd|rd|th)$`)

// seriesNote is what readSeriesNote found in a series-note subtitle.
type seriesNote struct {
	nums       []float64 // distinct volume numbers named
	split      bool      // names a part ("part", "pt")
	unreadable bool      // a number-like token that could not be placed
}

// readSeriesNote reads every volume number a series-note subtitle names:
// digits ("Vol. 2"), ordinal digits ("2nd"), words ("Book Two", "The Second
// Book") and roman numerals after a volume word ("Volume II"). Years and
// editions ("2nd Edition") are ignored. A "part" marks a split release.
// series, when the note names it, is removed first so a number word in the
// series' own name ("Seven Realms, Book 1") is not read as a volume.
//
// A note that places the book without a readable number ("Final Volume",
// "Last Book", "Series IV") is unreadable: it names a volume we cannot check.
func readSeriesNote(sub, series string) seriesNote {
	var out seriesNote
	norm := " " + normTitle(sub) + " "
	if series != "" {
		norm = strings.Replace(norm, " "+series+" ", " ", 1)
	}
	toks := strings.Fields(norm)
	seen := map[float64]bool{}
	add := func(f float64) {
		if !seen[f] {
			seen[f] = true
			out.nums = append(out.nums, f)
		}
	}
	for i, t := range toks {
		prevVolume := i > 0 && volumeWords[toks[i-1]]
		nextEdition := i+1 < len(toks) && (toks[i+1] == "edition" || toks[i+1] == "ed")
		switch {
		case t == "part" || t == "pt" || t == "parts":
			out.split = true
		case nextEdition:
			// "2nd Edition", "Second Edition": not a volume.
		case ordinalDigitsRe.MatchString(t):
			f, err := strconv.ParseFloat(ordinalDigitsRe.FindStringSubmatch(t)[1], 64)
			if err != nil {
				out.unreadable = true
				continue
			}
			add(f)
		case isDigits(t):
			f, err := strconv.ParseFloat(t, 64)
			if err != nil {
				out.unreadable = true
				continue
			}
			if f >= 1800 && f <= 2100 && !prevVolume {
				continue // a year
			}
			add(f)
		case wordValues[t] > 0:
			add(wordValues[t])
		case prevVolume && romanValues[t] > 0:
			add(romanValues[t])
		case prevVolume:
			// "Book Something": a volume word followed by a token we do
			// not read as a number.
			out.unreadable = true
		case placeWords[t]:
			out.unreadable = true
		case romanValues[t] > 0 && t != "i":
			// "Series IV" with no volume word ("i" alone is a word).
			out.unreadable = true
		case volumeWords[t] && i == len(toks)-1 && t != "no":
			// "Final Volume": a trailing volume word with no number.
			out.unreadable = true
		}
	}
	return out
}

// placeWords place a volume in a series without a number.
var placeWords = map[string]bool{"final": true, "last": true, "concluding": true, "closing": true, "penultimate": true, "finale": true}

// seriesNameIn returns the normalized series name (or, with none recorded,
// the book's title) when the subtitle names it, else "".
func seriesNameIn(sub string, b asinBookFacts) string {
	if !subtitleNamesSeries(sub, b) {
		return ""
	}
	if want := normSeriesName(b.SeriesName); want != "" {
		return want
	}
	return normSeriesName(stripSeriesParen(b.Title))
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// subtitleNamesSeries reports whether a series-note subtitle names the book's
// series (or, with none recorded, the book's title), so its number speaks to
// the book's position and not another series'.
func subtitleNamesSeries(sub string, b asinBookFacts) bool {
	want := normSeriesName(b.SeriesName)
	if want == "" {
		want = normSeriesName(stripSeriesParen(b.Title))
	}
	if want == "" {
		return false
	}
	return strings.Contains(" "+normTitle(sub)+" ", " "+want+" ")
}
