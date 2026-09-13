// file: internal/applygate/evidence.go
// version: 1.3.0
// guid: 4e2b7c19-8a3d-4f60-b5e1-9d7c0a2f6b38
// last-edited: 2026-09-13

package applygate

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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

// CheckEvidence runs every evidence check. audioConfirmed is the score leg's
// transcription result, counted as one agreement.
func CheckEvidence(book *database.Book, c *metafetch.MetadataCandidate, audioConfirmed bool) EvidenceVerdict {
	var v EvidenceVerdict
	if book == nil || c == nil {
		v.Reason, v.Detail = ReasonInsufficientEvidence, "no book or candidate"
		return v
	}
	v.Overwrites = overwrites(book, c)

	runtime := checkRuntime(book, c, len(v.Overwrites) > 0)
	v.Checks = append(v.Checks,
		runtime,
		checkAuthorRole(c),
		checkAuthorPath(book, c),
		checkTitle(book, c),
		checkNarrator(book, c, runtime.Outcome),
		checkASIN(book, c),
		checkCastInAuthor(&nameSource{author: bookAuthor(book), narrator: bookNarrator(book)}, c.Author, c.Narrator),
	)
	audio := CheckResult{Name: "transcription", Outcome: OutcomeUnknown}
	if audioConfirmed {
		audio.Outcome, audio.Detail = OutcomeAgree, "audio intro names this title/author"
	}
	v.Checks = append(v.Checks, audio)

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
		return v
	}
	if v.Agreements < MinAgreements {
		v.Reason = ReasonInsufficientEvidence
		v.Detail = strconv.Itoa(v.Agreements) + " independent signal(s) agree, need " + strconv.Itoa(MinAgreements)
		return v
	}
	v.Pass = true
	return v
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
func overwrites(book *database.Book, c *metafetch.MetadataCandidate) []string {
	var out []string
	if strings.TrimSpace(c.Title) != "" && loses(tokens(book.Title, true), tokens(c.Title+" "+c.Subtitle, true)) {
		out = append(out, "title")
	}
	if strings.TrimSpace(c.Author) != "" && loses(set(surnames(bookAuthor(book))), set(surnames(c.Author))) {
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

func checkRuntime(book *database.Book, c *metafetch.MetadataCandidate, overwriting bool) CheckResult {
	r := CheckResult{Name: "runtime"}
	bookSec := 0
	if book.Duration != nil {
		bookSec = *book.Duration
	}
	if bookSec <= 0 || c.DurationSec <= 0 {
		r.Outcome = OutcomeUnknown
		r.Detail = "files " + mins(bookSec) + ", candidate " + mins(c.DurationSec)
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
	r.Detail = "files " + mins(bookSec) + ", candidate " + mins(c.DurationSec) + " (" + strconv.Itoa(int(ratio*100+0.5)) + "% off)"
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
// candidate's author: the path segments, or the book's current author.
func checkAuthorPath(book *database.Book, c *metafetch.MetadataCandidate) CheckResult {
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
	for _, n := range surnames(bookAuthor(book)) {
		have[n] = true
	}
	for _, n := range names {
		if have[n] {
			r.Outcome, r.Detail = OutcomeAgree, strconv.Quote(n)+" appears in the path or current author"
			return r
		}
	}
	cur := bookAuthor(book)
	if cur == "" || normText(cur) != normText(c.Author) {
		r.Outcome, r.Reason = OutcomeBlock, ReasonAuthorNotInPath
		r.Detail = "no surname of " + strconv.Quote(c.Author) + " appears in the path or current author"
		return r
	}
	r.Outcome = OutcomeNeutral
	return r
}

// checkTitle compares the candidate title with every place the book's title
// lives: the stored title and the file and folder names. A candidate title
// that equals a SERIES name ("Star Wars" for "A New Dawn: Star Wars") is not
// allowed to agree through a split segment, and a folder named after the
// series ("/Star Wars/A New Dawn.m4b") is not a title at all. A candidate whose
// title IS the series name and that nothing confirms is a hard block: that is
// the 2026-09-13 "Star Wars" at 233% row.
func checkTitle(book *database.Book, c *metafetch.MetadataCandidate) CheckResult {
	r := CheckResult{Name: "title"}
	candVariants := []string{c.Title}
	if strings.TrimSpace(c.Subtitle) != "" {
		candVariants = append(candVariants, c.Title+" "+c.Subtitle)
	}
	seriesNames := map[string]bool{}
	for _, s := range []string{c.Series, seriesName(book)} {
		if n := normText(s); n != "" {
			seriesNames[n] = true
		}
	}
	full, segs := bookTitleVariants(book, seriesNames)
	// The candidate's own main title ("The Hobbit" of "The Hobbit: Or There
	// and Back Again") may agree with a whole book title, unless it is the
	// series name ("Star Wars" of "Star Wars: A New Dawn").
	if main := strings.TrimSpace(segSplitRe.Split(c.Title, 2)[0]); main != "" &&
		main != strings.TrimSpace(c.Title) && !seriesNames[normText(main)] {
		candVariants = append(candVariants, main)
	}

	best, bestAgainst := 0.0, ""
	for _, cv := range candVariants {
		for _, bv := range full {
			if s := titleSim(cv, bv); s > best {
				best, bestAgainst = s, bv
			}
		}
		if seriesNames[normText(cv)] {
			continue
		}
		for _, bv := range segs {
			if s := titleSim(cv, bv); s > best {
				best, bestAgainst = s, bv
			}
		}
	}
	if best == 0 && len(full) == 0 {
		r.Outcome, r.Detail = OutcomeUnknown, "book has no title or file name"
		return r
	}
	r.Detail = "best " + strconv.Itoa(int(best*100+0.5)) + "% vs " + strconv.Quote(bestAgainst)
	switch {
	case best >= 0.85:
		r.Outcome = OutcomeAgree
	case seriesNames[normText(c.Title)] && strings.TrimSpace(c.Subtitle) == "":
		r.Outcome, r.Reason = OutcomeBlock, ReasonTitleDisagrees
		r.Detail = "candidate title " + strconv.Quote(c.Title) + " is the series name; " + r.Detail
	case best < 0.5:
		r.Outcome, r.Reason = OutcomeBlock, ReasonTitleDisagrees
	default:
		r.Outcome = OutcomeNeutral
	}
	return r
}

// checkNarrator compares narrator surnames. A contradiction blocks only when
// the runtime is KNOWN and outside the agree band: a different narrator with
// a different runtime is another recording, but with no runtime the stored
// narrator is often a tag guess (the author, "Unknown") and proves nothing.
func checkNarrator(book *database.Book, c *metafetch.MetadataCandidate, runtimeOutcome string) CheckResult {
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
	if runtimeOutcome != OutcomeNeutral && runtimeOutcome != OutcomeBlock {
		r.Outcome = OutcomeNeutral
		return r
	}
	r.Outcome, r.Reason = OutcomeBlock, ReasonNarratorMismatch
	return r
}

func checkASIN(book *database.Book, c *metafetch.MetadataCandidate) CheckResult {
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

// bookTitleVariants returns whole-title forms (stored title, its
// parenthetical-stripped form, file base name, folder name) and the segments
// those split into at ":" / " - " separators. A folder or file name equal to a
// series name is skipped: it names the series, not this book.
func bookTitleVariants(book *database.Book, seriesNames map[string]bool) (full, segs []string) {
	add := func(s string) {
		if strings.TrimSpace(s) != "" {
			full = append(full, s)
		}
	}
	addPath := func(s string) {
		if !seriesNames[normText(s)] {
			add(s)
		}
	}
	add(book.Title)
	add(parenRe.ReplaceAllString(book.Title, " "))
	if p := strings.TrimSpace(book.FilePath); p != "" {
		base := filepath.Base(p)
		if ext := filepath.Ext(base); isFileExt(ext) {
			addPath(strings.TrimSuffix(base, ext))
			addPath(filepath.Base(filepath.Dir(p)))
		} else {
			addPath(base)
		}
	}
	for _, f := range full {
		for _, s := range segSplitRe.Split(f, -1) {
			if strings.TrimSpace(s) != "" && strings.TrimSpace(s) != strings.TrimSpace(f) {
				segs = append(segs, s)
			}
		}
	}
	return full, segs
}

// isFileExt tells a real extension (".m4b", ".mp3") from a folder name that
// merely contains a dot ("Book 1.5" -> ".5").
func isFileExt(ext string) bool {
	if len(ext) < 2 || len(ext) > 5 {
		return false
	}
	return strings.IndexFunc(ext[1:], unicode.IsLetter) >= 0
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
	den := len(A)
	if len(B) > den {
		den = len(B)
	}
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

func bookAuthor(book *database.Book) string {
	if book.Author != nil {
		return strings.TrimSpace(book.Author.Name)
	}
	return ""
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
