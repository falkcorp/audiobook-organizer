// file: internal/applygate/evidence.go
// version: 1.12.1
// guid: 4e2b7c19-8a3d-4f60-b5e1-9d7c0a2f6b38
// last-edited: 2026-11-17

package applygate

import (
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"golang.org/x/text/unicode/norm"
)

// Evidence leg. The score, identity and sequence legs let through, on the
// 2026-09-13 prod dry run, "Eldest" -> a 15-minute "C.L.O.W.N. 242", an
// editor credited as the author, and 1,612 of 4,398 rows whose runtime was
// more than 10% off the files. A score is a blend of multipliers (author x1.5,
// transcription x2.24 ...) that can lift a 0.67 title match to 2.33, so no
// score floor can stand in for checking the candidate against what we know
// about the files. This leg does that, following the dedup auto-resolve rule
// (internal/dedup/auto_resolve.go): pass only with NO hard contradiction and
// at least MinAgreements independent positive agreements. Missing evidence is
// "not confirmed", never agreement.

// MinAgreements is how many independent signals must positively agree.
const MinAgreements = 2

// Runtime bands, as |candidate - book| / book.
const (
	RuntimeBlockRatio = 0.10
	RuntimeAgreeRatio = 0.05
)

// Evidence reasons. Stable strings, like the others.
const (
	ReasonRuntimeMismatch         = "runtime_mismatch"
	ReasonRuntimeUnknownOverwrite = "runtime_unknown_on_overwrite"
	ReasonAuthorRoleCredit        = "author_role_credit"
	ReasonAuthorNotInPath         = "author_not_in_path"
	ReasonTitleDisagrees          = "title_disagrees"
	ReasonNarratorMismatch        = "narrator_mismatch"
	ReasonASINConflict            = "asin_conflict"
	ReasonInsufficientEvidence    = "insufficient_evidence"
	// ReasonTitleUnconfirmed: the title only partly matches (the title check
	// is neutral) and the runtime does not confirm it. See tally.
	ReasonTitleUnconfirmed = "title_unconfirmed"
)

// Check outcomes.
const (
	OutcomeAgree   = "agree"   // positive, independent confirmation
	OutcomeBlock   = "block"   // hard contradiction: refuse
	OutcomeNeutral = "neutral" // looked, neither confirms nor contradicts
	OutcomeUnknown = "unknown" // no data to look at
)

// CheckResult is one evidence check's finding, shown per row in the dry run.
type CheckResult struct {
	Name    string `json:"name"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
	Detail  string `json:"detail,omitempty"`

	// fieldConflict marks a title block that comes from the two title fields
	// themselves (the candidate and the stored title share a whole segment and
	// each has one of its own, or use the same words with different numbers).
	// No transcription lifts it (applyTranscribedTitle): a disagreement between
	// the titles is not missing evidence. Not serialised.
	fieldConflict bool
}

// EvidenceVerdict is the evidence leg's result.
type EvidenceVerdict struct {
	Pass       bool          `json:"pass"`
	Reason     string        `json:"reason,omitempty"`
	Detail     string        `json:"detail,omitempty"`
	Agreements int           `json:"agreements"`
	Overwrites []string      `json:"overwrites,omitempty"`
	Checks     []CheckResult `json:"checks"`
}

// roleCreditRe matches a contributor credit that is not an authorship one.
// "John Joseph Adams - editor" was applied as an author on 2026-09-13.
// "(ed)" is matched only inside parentheses: a bare "ed" is a first name
// (Ed Greenwood). "with" is not a role: "Patterson with Paetro" is co-authorship.
var roleCreditRe = regexp.MustCompile(`(?i)(^|[\s,(/\-–—])(editor|editors|ed\.|eds\.|edited by|translator|translated by|translated|translation|trans\.|tr\.|hrsg\.|hrsg|herausgeber|herausgegeben|narrator|narrated by|read by|foreword|introduction|illustrator|illustrated by|contributor|compiler|compiled by|adapted by|adaptation)($|[\s,)/\-–—.])|\(\s*(eds?|trans|tr)\.?\s*\)`)

// titleStop are tokens that carry no identity in a title.
var titleStop = map[string]bool{
	"the": true, "a": true, "an": true, "of": true, "and": true, "book": true,
	"part": true, "volume": true, "vol": true, "unabridged": true, "abridged": true,
	"audiobook": true, "novel": true, "series": true, "edition": true,
}

// CheckEvidenceInBatch runs every evidence check. audioConfirmed is the score
// leg's transcription result, counted as one agreement.
//
// rt is the book's canonical runtime (database.LoadBookRuntime); the runtime
// check compares only a Complete one and reads anything else as unknown.
//
// authors is the book's live author credit (see Authors).
//
// claims is the batch's ClaimIndex, which the partial_book check reads to see
// sibling parts. nil (the single-book case) skips that test.
func CheckEvidenceInBatch(book *database.Book, authors Authors, rt database.BookRuntime, c *metafetch.MetadataCandidate, audioConfirmed bool, claims *ClaimIndex) EvidenceVerdict {
	var v EvidenceVerdict
	if book == nil || c == nil {
		v.Reason, v.Detail = ReasonInsufficientEvidence, "no book or candidate"
		return v
	}
	authors = authors.real()
	v.Overwrites = overwrites(book, authors, c)

	runtime := checkRuntime(rt, c, len(v.Overwrites) > 0)
	narratorArmed := narratorVetoArmed(rt, c, runtime.Outcome)
	v.Checks = append(v.Checks,
		runtime,
		checkAuthorRole(c),
		checkAuthorPath(book, authors, c),
		checkTitle(book, c),
		checkNarrator(book, c, narratorArmed),
		CheckASIN(book, c),
		checkCastInAuthor(&nameSource{author: authors.credit(), narrator: bookNarrator(book)}, c.Author, c.Narrator),
		checkSeriesNumberLost(book, c, v.Overwrites),
		checkPartialBook(book, c, runtime.Outcome, claims),
	)
	audio := CheckResult{Name: "transcription", Outcome: OutcomeUnknown}
	if audioConfirmed {
		audio.Outcome, audio.Detail = OutcomeAgree, "audio intro names this title/author"
	}
	v.Checks = append(v.Checks, audio)
	v.tally()
	return v
}

// tally derives Pass, Reason, Detail and Agreements from Checks: the first
// block refuses; otherwise at least MinAgreements agreements pass, with one
// more condition. A NEUTRAL title (it neither agrees nor conflicts: the
// candidate and the book share part of a title and differ in the rest) applies
// only when the runtime check AGREES, known on both sides and within
// RuntimeAgreeRatio. Author, narrator, ASIN and the transcription are all
// agreements that do not look at the title or the length, so a neutral title
// plus two of them is two agreements about a different book. The condition is
// here, where the agreements are counted, so no caller can count its way around
// it; applyTranscribedTitle re-runs tally after it revises the title check.
func (v *EvidenceVerdict) tally() {
	v.Pass, v.Reason, v.Detail, v.Agreements = false, "", "", 0
	for _, ch := range v.Checks {
		switch ch.Outcome {
		case OutcomeBlock:
			if v.Reason == "" {
				v.Reason, v.Detail = ch.Reason, ch.Detail
			}
		case OutcomeAgree:
			v.Agreements++
		}
	}
	if v.Reason != "" {
		return
	}
	if v.Agreements < MinAgreements {
		v.Reason = ReasonInsufficientEvidence
		v.Detail = strconv.Itoa(v.Agreements) + " independent signal(s) agree, need " + strconv.Itoa(MinAgreements)
		return
	}
	if title, rt := v.check("title"), v.check("runtime"); title != nil && title.Outcome == OutcomeNeutral &&
		(rt == nil || rt.Outcome != OutcomeAgree) {
		v.Reason = ReasonTitleUnconfirmed
		v.Detail = "title only partly matches (" + title.Detail + ") and the runtime does not confirm it"
		if rt != nil && rt.Detail != "" {
			v.Detail += " (" + rt.Detail + ")"
		}
		return
	}
	v.Pass = true
}

// check returns the named check, or nil.
func (v *EvidenceVerdict) check(name string) *CheckResult {
	for i := range v.Checks {
		if v.Checks[i].Name == name {
			return &v.Checks[i]
		}
	}
	return nil
}

// overwrites lists identity fields the candidate would REPLACE (not fill).
// Filling an empty field on thin evidence is recoverable; replacing a title
// or author the owner may have set is not, so unknown runtime blocks only here.
//
// A field counts as replaced only when something the book has would be LOST:
// a title or series token the candidate lacks, or a credited surname the
// candidate drops. "The Hobbit" -> "The Hobbit: Or There and Back Again" and
// "J.R.R. Tolkien" -> "J. R. R. Tolkien" keep everything and are not overwrites;
// "A New Dawn: Star Wars" -> "Star Wars" loses "new" and "dawn" and is one.
//
// The author side is every live credit (authors): a candidate that drops a
// credited co-author replaces the credit as surely as one that drops the
// primary.
func overwrites(book *database.Book, authors Authors, c *metafetch.MetadataCandidate) []string {
	var out []string
	if strings.TrimSpace(c.Title) != "" && loses(tokens(book.Title, true), tokens(c.Title+" "+c.Subtitle, true)) {
		out = append(out, "title")
	}
	if strings.TrimSpace(c.Author) != "" && loses(set(authors.surnames()), set(surnames(c.Author))) {
		out = append(out, "author")
	}
	if strings.TrimSpace(c.Series) != "" && loses(tokens(seriesName(book), true), tokens(c.Series, true)) {
		out = append(out, "series")
	}
	return out
}

// loses reports whether have holds a token that next lacks.
func loses(have, next map[string]bool) bool {
	for t := range have {
		if !next[t] {
			return true
		}
	}
	return false
}

func set(xs []string) map[string]bool {
	out := make(map[string]bool, len(xs))
	for _, x := range xs {
		out[x] = true
	}
	return out
}

func checkRuntime(rt database.BookRuntime, c *metafetch.MetadataCandidate, overwriting bool) CheckResult {
	r := CheckResult{Name: "runtime"}
	// Only a COMPLETE runtime is compared as a total. A partial one (some
	// files' durations never probed, or chapters missing from disk) is a
	// LOWER BOUND: comparing it as a total made a 10 h book with two known
	// 20-minute chapters a "40 min vs 10 h" runtime_mismatch and vetoed the
	// right candidate. A lower bound can still PROVE a mismatch in one
	// direction, and that is kept: see lowerBoundContradicts. The total size
	// of the book's files proves the other direction when it can: a candidate
	// far longer than the bytes on disk could hold is a mismatch too, see
	// upperBoundContradicts.
	bookSec, known := rt.KnownSeconds()
	desc := runtimeDesc(rt)
	if !known && rt.Source == database.RuntimeSourceNone && rt.BookAggregateSec > 0 {
		// A multi-file book with NO file durations (or unreadable rows):
		// compare the stored Book.Duration in both directions, exactly as
		// main did. It may be a stale partial sum or one chapter's probe, so
		// this can block a right candidate; the owner rule is that the gate
		// is never looser than main, and here the files add nothing to
		// improve on it.
		bookSec, known = rt.BookAggregateSec, true
		desc = mins(bookSec) + " (stored book duration; no file durations)"
	}
	if !known || c.DurationSec <= 0 {
		if agg, ratio, ok := storedDurationContradicts(rt, c.DurationSec); ok {
			r.Outcome, r.Reason = OutcomeBlock, ReasonRuntimeMismatch
			r.Detail = "stored book duration " + mins(agg) + " (files " + runtimeDesc(rt) + "), candidate " + mins(c.DurationSec) +
				" (" + strconv.Itoa(int(ratio*100+0.5)) + "% off)"
			return r
		}
		if lb, ratio, ok := lowerBoundContradicts(rt, c.DurationSec); ok {
			r.Outcome, r.Reason = OutcomeBlock, ReasonRuntimeMismatch
			r.Detail = "files at least " + mins(lb) + " (" + runtimeDesc(rt) + "), candidate " + mins(c.DurationSec) +
				" (at least " + strconv.Itoa(int(ratio*100+0.5)) + "% off)"
			return r
		}
		if upper, ok := upperBoundContradicts(rt, c.DurationSec); ok {
			est, _ := rt.EstimatedSeconds()
			r.Outcome, r.Reason = OutcomeBlock, ReasonRuntimeMismatch
			r.Detail = "files " + runtimeDesc(rt) + "; their combined size suggests about " + mins(est) +
				" and no more than " + mins(upper) + " at any plausible bitrate, candidate " + mins(c.DurationSec) +
				" is longer than the files can hold"
			return r
		}
		r.Outcome = OutcomeUnknown
		r.Detail = "files " + runtimeDesc(rt) + ", candidate " + mins(c.DurationSec)
		if overwriting {
			r.Outcome, r.Reason = OutcomeBlock, ReasonRuntimeUnknownOverwrite
			r.Detail += "; would replace existing fields without a runtime to confirm the match"
		}
		return r
	}
	delta := bookSec - c.DurationSec
	if delta < 0 {
		delta = -delta
	}
	ratio := float64(delta) / float64(bookSec)
	r.Detail = "files " + desc + ", candidate " + mins(c.DurationSec) + " (" + strconv.Itoa(int(ratio*100+0.5)) + "% off)"
	switch {
	case ratio > RuntimeBlockRatio:
		r.Outcome, r.Reason = OutcomeBlock, ReasonRuntimeMismatch
	case ratio <= RuntimeAgreeRatio:
		r.Outcome = OutcomeAgree
	default:
		r.Outcome = OutcomeNeutral
	}
	return r
}

func checkAuthorRole(c *metafetch.MetadataCandidate) CheckResult {
	r := CheckResult{Name: "author_role", Outcome: OutcomeNeutral}
	if m := roleCreditRe.FindString(c.Author); m != "" {
		r.Outcome, r.Reason = OutcomeBlock, ReasonAuthorRoleCredit
		r.Detail = "candidate author " + strconv.Quote(c.Author) + " carries a non-author credit"
	}
	return r
}

// checkAuthorPath asks whether anything we hold about the files names the
// candidate's author: the path segments, or the book's current (live)
// authors. Until 2026-09-27 "current author" was the Book.Author snapshot
// embedded in the book row, which nothing refreshes when AuthorID or the
// book_authors join changes: 27/27 prod refusals had a live author equal to
// the candidate's and a nil or placeholder snapshot.
func checkAuthorPath(book *database.Book, authors Authors, c *metafetch.MetadataCandidate) CheckResult {
	r := CheckResult{Name: "author_evidence"}
	names := surnames(c.Author)
	if len(names) == 0 {
		r.Outcome, r.Detail = OutcomeUnknown, "candidate has no author"
		return r
	}
	// Path words plus the current author's surnames. Not every word of the
	// current author: a shared first name ("Stephen" King vs Baxter, Stephen)
	// is not evidence.
	have := tokens(book.FilePath, false)
	for _, n := range authors.surnames() {
		have[n] = true
	}
	for _, n := range names {
		if have[n] {
			r.Outcome, r.Detail = OutcomeAgree, strconv.Quote(n)+" appears in the path or current author"
			return r
		}
	}
	if !authors.equals(c.Author) {
		r.Outcome, r.Reason = OutcomeBlock, ReasonAuthorNotInPath
		r.Detail = "no surname of " + strconv.Quote(c.Author) + " appears in the path or current author"
		if cur := authors.credit(); cur != "" {
			r.Detail += " (" + strconv.Quote(cur) + ")"
		}
		return r
	}
	r.Outcome = OutcomeNeutral
	return r
}

// checkTitle compares the candidate's title with the book's, by the one rule
// in titlecompare.go: AGREE when a whole form of the candidate equals a whole
// form of the book (the declared fields joined, never trimmed; the book's file
// and folder names are forms of the book too), DISAGREE when the two TITLE
// fields conflict (a shared segment with a segment of its own on each side, or
// the same words with different numbers), and NEUTRAL otherwise: one title
// holds words or a number the other lacks. A neutral title confirms nothing, and
// EvidenceVerdict.tally applies it only with a runtime that agrees.
//
// "Hush: Part 1" and "Hush: Part 2", or "Batman: Dark Victory" and "Batman:
// The Long Halloween", share a storyline or franchise name and are different
// books; a series name, a credited name, a path or a year never makes a pair
// agree or disagree (an earlier version excused them, and each excuse was a way
// for two books to agree). A candidate title that equals a SERIES name ("Star
// Wars" for "A New Dawn: Star Wars") is not a title, and neither is a stored
// title that equals one ("Batman" for "Batman: Dark Victory" in the series
// "Batman"): both are the 2026-09-13 "Star Wars" at 233% row, from either side,
// and block unless a whole form agrees.
//
// A conflict between the two title fields blocks even when a file name agrees:
// the stored title and the file name disagreeing with each other is not
// confirmation. A path never causes a conflict. A pair with nothing in common
// blocks only when no form agrees.
func checkTitle(book *database.Book, c *metafetch.MetadataCandidate) CheckResult {
	r := CheckResult{Name: "title"}
	seriesNames := titleSeriesNames(book, c)
	bookForms, pathNames := bookTitleForms(book, seriesNames)
	if len(bookForms) == 0 {
		r.Outcome, r.Detail = OutcomeUnknown, "book has no title or file name"
		return r
	}
	candForms := titleForms(c.Title, c.Subtitle, c.Series)
	block := func(detail string) CheckResult {
		r.Outcome, r.Reason, r.Detail = OutcomeBlock, ReasonTitleDisagrees, detail
		return r
	}
	if len(candForms) == 0 {
		return block("candidate has no title")
	}

	candField := fieldText(c.Title, c.Subtitle)
	storedField := fieldText(book.Title, derefStr(book.Subtitle))
	cmp := titleCmp{class: classNeutral}
	desc := ""
	if strings.TrimSpace(book.Title) != "" {
		cmp = compareTitleFields(candField, storedField)
		desc = strconv.Quote(candField) + " vs " + strconv.Quote(storedField)
		if cmp.note != "" {
			desc += ": " + cmp.note
		}
	}
	if cmp.class == classConflictNumber || cmp.class == classConflictWords {
		r = block("conflicting title: " + desc)
		r.fieldConflict = true
		return r
	}
	for _, cf := range candForms {
		if i := slices.Index(bookForms, cf); i >= 0 {
			r.Outcome, r.Detail = OutcomeAgree, "whole title equals "+strconv.Quote(bookForms[i])
			return r
		}
	}
	switch {
	case candidateTitleIsSeriesName(book, c):
		return block("candidate title " + strconv.Quote(c.Title) + " is the series name; " + desc)
	case storedTitleIsSeriesName(book, c):
		return block("stored title " + strconv.Quote(book.Title) + " is the series name; " + desc)
	case cmp.class == classUnrelated:
		return block("the titles have little in common: " + desc)
	}
	r.Outcome = OutcomeNeutral
	if desc != "" {
		r.Detail = desc
		return r
	}
	// No stored title: the file and folder names are all the book says. They
	// can still show that the candidate is another work altogether (nothing
	// in common with any of them), as before; they never make two titles
	// conflict, because a name on disk carries narrators, publishers and years.
	r.Detail = "no stored title; the file and folder names do not equal " + strconv.Quote(candField)
	unrelated := len(pathNames) > 0
	for _, pn := range pathNames {
		if compareTitleFields(candField, pn).class != classUnrelated {
			unrelated = false
		}
	}
	if unrelated {
		return block("no stored title and the file and folder names have little in common with " + strconv.Quote(candField) +
			": " + strconv.Quote(strings.Join(pathNames, ", ")))
	}
	return r
}

// titleSeriesNames is the series names the title check knows (the candidate's
// and the book's), each as its normalised form (formTokens joined): the same
// normalisation the forms are compared in, so "Batman (Unabridged)" is the
// series "Batman" and "Book 2" is the series "Book Two".
func titleSeriesNames(book *database.Book, c *metafetch.MetadataCandidate) map[string]bool {
	names := map[string]bool{}
	for _, s := range []string{c.Series, seriesName(book)} {
		if n := strings.Join(formTokens(s), " "); n != "" {
			names[n] = true
		}
	}
	return names
}

// candidateTitleIsSeriesName reports whether the candidate's whole title is a
// series name with no subtitle to tell the volume ("Star Wars", "Discworld"):
// the title check's hard block. It is its own function because nothing may
// lift that block -- not even a transcription that says the same word, since
// an intro announcing the series names no volume (applyTranscribedTitle).
func candidateTitleIsSeriesName(book *database.Book, c *metafetch.MetadataCandidate) bool {
	return titleSeriesNames(book, c)[strings.Join(formTokens(c.Title), " ")] && strings.TrimSpace(c.Subtitle) == ""
}

// storedTitleIsSeriesName is candidateTitleIsSeriesName from the book's side:
// the stored title is a series name and nothing else ("Batman", in the series
// "Batman"), so it does not say which volume the book is and cannot confirm
// one. checkTitle reaches it only after a whole form failed to agree.
func storedTitleIsSeriesName(book *database.Book, c *metafetch.MetadataCandidate) bool {
	return titleSeriesNames(book, c)[strings.Join(formTokens(book.Title), " ")] && strings.TrimSpace(derefStr(book.Subtitle)) == ""
}

// checkNarrator compares narrator surnames. A contradiction blocks only when
// vetoArmed (narratorVetoArmed): the runtime did not confirm the match. A
// different narrator with a different runtime is another recording, but with
// no runtime at all the stored narrator is often a tag guess (the author,
// "Unknown") and proves nothing.
func checkNarrator(book *database.Book, c *metafetch.MetadataCandidate, vetoArmed bool) CheckResult {
	r := CheckResult{Name: "narrator"}
	cur := ""
	if book.Narrator != nil {
		cur = *book.Narrator
	}
	a, b := set(surnames(cur)), set(surnames(c.Narrator))
	if len(a) == 0 || len(b) == 0 {
		r.Outcome = OutcomeUnknown
		return r
	}
	for t := range a {
		if b[t] {
			r.Outcome, r.Detail = OutcomeAgree, "narrator matches"
			return r
		}
	}
	r.Detail = strconv.Quote(cur) + " vs " + strconv.Quote(c.Narrator)
	if !vetoArmed {
		r.Outcome = OutcomeNeutral
		return r
	}
	r.Outcome, r.Reason = OutcomeBlock, ReasonNarratorMismatch
	return r
}

// CheckASIN is the evidence leg's ASIN check: agree when the book and the
// candidate carry the same ASIN (case and surrounding space ignored), block as
// asin_conflict when both carry one and they differ, unknown when either has
// none. Exported for the paths outside the bulk gate that must refuse the same
// pair: the transcription auto-apply and the single-book dialog.
func CheckASIN(book *database.Book, c *metafetch.MetadataCandidate) CheckResult {
	r := CheckResult{Name: "asin", Outcome: OutcomeUnknown}
	cur := ""
	if book.ASIN != nil {
		cur = strings.TrimSpace(*book.ASIN)
	}
	cand := strings.TrimSpace(c.ASIN)
	switch {
	case cur == "" || cand == "":
	case strings.EqualFold(cur, cand):
		r.Outcome, r.Detail = OutcomeAgree, "ASIN "+cur+" matches"
	default:
		r.Outcome, r.Reason = OutcomeBlock, ReasonASINConflict
		r.Detail = "book ASIN " + cur + ", candidate " + cand
	}
	return r
}

// bookTitleForms returns the book's whole forms: its stored fields joined (the
// title, or title and subtitle, each also behind the book's series) and the file
// base name and the folder name, each as one form (names returns them as
// written). A folder or file name equal to a series name is skipped: it names
// the series, not this book. The file names are forms only to AGREE with; they
// never cause a conflict.
func bookTitleForms(book *database.Book, seriesNames map[string]bool) (forms, names []string) {
	out := titleForms(book.Title, derefStr(book.Subtitle), seriesName(book))
	addPath := func(s string) {
		f := strings.Join(formTokens(s), " ")
		if seriesNames[f] {
			return
		}
		if f != "" {
			names = append(names, s)
			if !slices.Contains(out, f) {
				out = append(out, f)
			}
		}
	}
	if p := strings.TrimSpace(book.FilePath); p != "" {
		base := filepath.Base(p)
		if ext := filepath.Ext(base); metadata.IsFileExt(ext) {
			addPath(strings.TrimSuffix(base, ext))
			addPath(filepath.Base(filepath.Dir(p)))
		} else {
			addPath(base)
		}
	}
	return out, names
}

var (
	parenRe    = regexp.MustCompile(`[(\[][^)\]]*[)\]]`)
	segSplitRe = regexp.MustCompile(`\s*(?::|\s-\s|\s–\s|\s—\s|[(\[\])])\s*`)
)

// titleSim is |A∩B| / max(|A|,|B|) over identity tokens. Unlike a token-set
// ratio it does NOT score a subset as a full match: "Star Wars" against
// "A New Dawn Star Wars" is 0.5, not 1.0.
func titleSim(a, b string) float64 {
	A, B := tokens(a, true), tokens(b, true)
	if len(A) == 0 || len(B) == 0 {
		return 0
	}
	inter, alpha := 0, false
	for t := range A {
		if B[t] {
			inter++
			if !isNumber(t) {
				alpha = true
			}
		}
	}
	// "Book 1" vs "Part 1" share only "1". A shared number is not a match
	// unless the titles have no words at all ("1984").
	if !alpha && (hasWord(tokens(a, false)) || hasWord(tokens(b, false))) {
		return 0
	}
	den := max(len(B), len(A))
	return float64(inter) / float64(den)
}

func isNumber(t string) bool {
	return strings.IndexFunc(t, func(r rune) bool { return !unicode.IsDigit(r) }) < 0
}

func hasWord(m map[string]bool) bool {
	for t := range m {
		if !isNumber(t) {
			return true
		}
	}
	return false
}

// tokens folds accents, lowercases and splits on non-alphanumerics. With
// dropStop it also drops titleStop words. Single characters are dropped.
func tokens(s string, dropStop bool) map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.FieldsFunc(normText(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len([]rune(f)) < 2 && !unicode.IsDigit([]rune(f)[0]) {
			continue
		}
		if dropStop && titleStop[f] {
			continue
		}
		out[f] = true
	}
	return out
}

// normText lowercases and strips diacritics ("Machmüller" == "Machmuller").
func normText(s string) string {
	d := norm.NFD.String(strings.ToLower(strings.TrimSpace(s)))
	var b strings.Builder
	for _, r := range d {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

var (
	nameSuffix  = map[string]bool{"jr": true, "sr": true, "ii": true, "iii": true, "iv": true, "phd": true, "md": true, "dr": true}
	creditSplit = regexp.MustCompile(`(?i),|&|;|\band\b`)
	andWord     = regexp.MustCompile(`(?i)\band\b`)
)

// surnames returns the last name-token of each credited person. A single
// "Last, First" credit ("Baxter, Stephen") yields the part before the comma,
// not two people.
func surnames(author string) []string {
	if last, _, ok := strings.Cut(author, ","); ok && !strings.ContainsAny(author, "&;") &&
		strings.Count(author, ",") == 1 && !andWord.MatchString(author) {
		if w := nameWords(last); len(w) == 1 {
			rest := nameWords(author[len(last)+1:])
			if len(rest) > 0 && len(rest) <= 3 {
				return w
			}
		}
	}
	var out []string
	for _, part := range creditSplit.Split(author, -1) {
		if words := nameWords(part); len(words) > 0 {
			out = append(out, words[len(words)-1])
		}
	}
	return out
}

// roleWord are credit labels, never a person's name. "Radclyffe - author/editor"
// yielded the "surname" editor, which then matched "read by narrator"-style
// words in a file path as author evidence (2026-09-13 prod preview).
var roleWord = map[string]bool{
	"author": true, "editor": true, "editors": true, "translator": true, "narrator": true,
	"illustrator": true, "contributor": true, "compiler": true, "foreword": true,
	"introduction": true, "adapter": true, "eds": true, "trans": true, "hrsg": true,
	"read": true, "by": true, "edited": true, "translated": true, "narrated": true,
}

// nameWords is the name tokens of one credit, without initials, suffixes or
// role labels.
func nameWords(s string) []string {
	var words []string
	for _, w := range strings.FieldsFunc(normText(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len([]rune(w)) > 1 && !nameSuffix[w] && !roleWord[w] {
			words = append(words, w)
		}
	}
	return words
}

// Authors is a book's LIVE author credit, primary first: the names behind
// Book.AuthorID and the book_authors join, as database.LiveBookAuthorNames
// resolves them. Every caller resolves it; the gate never reads Book.Author,
// the denormalized snapshot persisted inside the book row, because nothing
// refreshes that snapshot when the author changes (the metadata apply itself
// writes AuthorID and the join and leaves it alone), so it is nil on most
// books and stale on others. A nil Authors means the book has no author.
//
// The organizer's "Unknown Author" placeholder is not an author: the gate
// drops it (real), so a book filed under the placeholder has nothing to
// protect, exactly like a book with no author row.
type Authors []string

// real is authors without blanks and the placeholder.
func (a Authors) real() Authors {
	var out Authors
	for _, n := range a {
		if n = strings.TrimSpace(n); n != "" && !authorname.IsPlaceholder(n) {
			out = append(out, n)
		}
	}
	return out
}

// credit is the authors as one display credit ("A & B").
func (a Authors) credit() string { return strings.Join(a, " & ") }

// surnames is the surname of every author, in order.
func (a Authors) surnames() []string {
	var out []string
	for _, n := range a {
		out = append(out, surnames(n)...)
	}
	return out
}

// equals reports whether the candidate credit is, as normalized text, one of
// the authors or all of them joined.
func (a Authors) equals(credit string) bool {
	want := normText(credit)
	if want == "" || len(a) == 0 {
		return false
	}
	if normText(a.credit()) == want {
		return true
	}
	for _, n := range a {
		if normText(n) == want {
			return true
		}
	}
	return false
}

func bookNarrator(book *database.Book) string {
	if book.Narrator != nil {
		return strings.TrimSpace(*book.Narrator)
	}
	return ""
}

func seriesName(book *database.Book) string {
	if book.Series != nil {
		return book.Series.Name
	}
	return ""
}

func mins(sec int) string {
	if sec <= 0 {
		return "unknown"
	}
	return strconv.Itoa((sec+30)/60) + " min"
}

// runtimeDesc renders a book runtime for a check detail, saying when it is not
// the whole book: "600 min", "40 min from 2 of 30 files (partial)", "unknown".
func runtimeDesc(rt database.BookRuntime) string {
	switch {
	case rt.Complete():
		return mins(rt.Seconds)
	case rt.Partial():
		return mins(rt.Seconds) + " from " + strconv.Itoa(rt.FilesKnown) + " of " + strconv.Itoa(rt.FilesCounted) + " files (partial)"
	default:
		return mins(0)
	}
}

// runtimeLowerBound is a value the book's true runtime cannot be below when
// the runtime is PARTIAL: the sum of its counted rows with known durations.
// Complete runtimes are compared as totals, and the no-file-duration case is
// compared on Book.Duration as main did (checkRuntime), so neither needs one.
func runtimeLowerBound(rt database.BookRuntime) int {
	if rt.Partial() {
		return rt.Seconds
	}
	return 0
}

// storedDurationContradicts is main's runtime check, run against the stored
// Book.Duration, for a PARTIAL runtime whose Book.Duration is NOT the partial
// known sum. Such a value came from somewhere other than the rows the
// canonical runtime just summed — an earlier measurement of more files, a
// probe, an apply — and main compared it in both directions, so the gate
// must still block on it to stay never looser than main.
//
// When Book.Duration EQUALS the partial known sum (within the duplicate
// slack), it is exactly the lower bound RecomputeBookAggregates stored, and
// comparing it as a total is the defect this PR fixes: on prod every one of
// the 26 sampled partial books had Book.Duration == its known sum, and main
// vetoed their correct candidates. Only that value is set aside;
// lowerBoundContradicts still blocks on it in the direction it proves.
func storedDurationContradicts(rt database.BookRuntime, candSec int) (agg int, ratio float64, ok bool) {
	agg = rt.BookAggregateSec
	if !rt.Partial() || agg <= 0 || candSec <= 0 || isPartialSum(agg, rt.Seconds) {
		return agg, 0, false
	}
	d := agg - candSec
	if d < 0 {
		d = -d
	}
	ratio = float64(d) / float64(agg)
	return agg, ratio, ratio > RuntimeBlockRatio
}

// isPartialSum reports whether a stored Book.Duration is the partial known
// sum (2 s slack for rounding in how the aggregate was summed).
func isPartialSum(agg, knownSum int) bool {
	d := agg - knownSum
	return d >= -2 && d <= 2
}

// lowerBoundContradicts reports whether a partial runtime still PROVES a
// runtime_mismatch: its lower bound LB exceeds the candidate by more than
// RuntimeBlockRatio. The true runtime T is >= LB, and for T above the
// candidate the gate's ratio (T-cand)/T only grows with T, so
// (LB-cand)/LB > RuntimeBlockRatio implies the complete runtime would block
// too. The opposite direction (LB below the candidate, with some row's
// duration unknown) proves nothing: that is the one case this PR unblocks.
func lowerBoundContradicts(rt database.BookRuntime, candSec int) (lb int, ratio float64, ok bool) {
	lb = runtimeLowerBound(rt)
	if lb <= 0 || candSec <= 0 || lb <= candSec {
		return lb, 0, false
	}
	ratio = float64(lb-candSec) / float64(lb)
	return lb, ratio, ratio > RuntimeBlockRatio
}

// upperBoundContradicts reports whether a partial runtime still PROVES a
// runtime_mismatch from the other direction: the candidate is longer than the
// upper bound the total size of ALL the book's files allows
// (database.BookRuntime.EstimatedUpperSeconds: the larger of the measured
// files' bytes-per-second applied to every file's bytes with 3x headroom, and
// the physical floor of the total size at 1000 B/s).
// A 14 h candidate for two files totalling 8 MB cannot be this book. A
// candidate at or below the bound proves nothing, so a 10 h book with two of
// twenty chapters measured still accepts its 10 h candidate; with no estimate
// (a size missing, nothing measured) this never fires and the outcome stays
// unknown.
func upperBoundContradicts(rt database.BookRuntime, candSec int) (upper int, ok bool) {
	upper, ok = rt.EstimatedUpperSeconds()
	if !ok {
		return 0, false
	}
	return upper, candSec > upper
}

// narratorVetoArmed reports whether a narrator contradiction blocks. main's
// rule is kept: the runtime check looked and did not agree (neutral or
// block) — which covers complete runtimes and the stored-duration comparison.
// A PARTIAL runtime has no confirmation to excuse a different narrator, so
// the veto is armed whenever the candidate has a runtime; main compared the
// partial sum there and armed it unless that sum landed in the agree band, so
// this is never looser. No runtime data, or a candidate without one, keeps
// main's neutral.
func narratorVetoArmed(rt database.BookRuntime, c *metafetch.MetadataCandidate, runtimeOutcome string) bool {
	// main's rule, unchanged: the runtime check looked and did not agree.
	if runtimeOutcome == OutcomeNeutral || runtimeOutcome == OutcomeBlock {
		return true
	}
	if rt.Complete() {
		return false
	}
	return c.DurationSec > 0 && runtimeLowerBound(rt) > 0
}
