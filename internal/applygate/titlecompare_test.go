// file: internal/applygate/titlecompare_test.go
// version: 2.0.0
// guid: 0b5a7e12-93c4-4d68-a1f7-6e2d9c40b835
// last-edited: 2026-11-17

package applygate

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// The first version of this file tested the comparison with pairs the author
// had just written the comparison to handle, and a pool whose series, credits
// and subtitles never reused the words of the titles. Every hole the review
// found lived in that gap. Here the generator draws titles, subtitles, series
// names, credits and file names from ONE word pool, renders them with the noise
// the rule claims to ignore (case, delimiters, articles, "&", number spellings,
// "(Unabridged)"), and an oracle written in this file from the rule's
// specification (not by calling it) says what the check must answer.

// ---- the oracle -----------------------------------------------------------

// atitle is one side of a pair in the abstract: segments of canonical words
// (numbers as digit strings), an optional subtitle, an optional series.
type atitle struct {
	segs   [][]string
	sub    []string
	series []string
	path   []string // a file or folder name, one segment; empty for none
}

var (
	oracleWords   = []string{"hush", "knightfall", "victory", "dark", "halloween", "long", "robin", "penguin", "riddler", "gotham", "owls", "court", "year", "mask"}
	oracleMarkers = []string{"part", "book"}
	oracleNums    = []string{"1", "2", "3"}
)

func flat(segs [][]string) []string {
	var out []string
	for _, s := range segs {
		out = append(out, s...)
	}
	return out
}

func join(parts ...[]string) string {
	var all []string
	for _, p := range parts {
		all = append(all, p...)
	}
	return strings.Join(all, " ")
}

// oCanon is the rule's one per-segment step: a leading article is dropped from
// each segment (the title's, the subtitle, the series, a file name as one
// segment) unless that leaves nothing.
func oCanon(a atitle) atitle {
	drop := func(seg []string) []string {
		if len(seg) > 1 && (seg[0] == "the" || seg[0] == "a" || seg[0] == "an") {
			return seg[1:]
		}
		return seg
	}
	out := atitle{sub: drop(a.sub), series: drop(a.series), path: drop(a.path)}
	for _, sg := range a.segs {
		out.segs = append(out.segs, drop(sg))
	}
	return out
}

// oForms are the whole forms of one side, per the rule: the title (or title and
// subtitle) joined, each also behind the series; no bare title when there is a
// subtitle.
func oForms(a atitle) []string {
	t := flat(a.segs)
	var out []string
	if len(a.sub) == 0 {
		out = append(out, join(t))
		if len(a.series) > 0 {
			out = append(out, join(a.series, t))
		}
		return out
	}
	out = append(out, join(t, a.sub))
	if len(a.series) > 0 {
		out = append(out, join(a.series, t, a.sub))
	}
	return out
}

// oSegs are the segments of the title field: the title's, then the subtitle.
func oSegs(a atitle) [][]string {
	out := slices.Clone(a.segs)
	if len(a.sub) > 0 {
		out = append(out, a.sub)
	}
	return out
}

func oWordsNums(ws []string) (words map[string]bool, nums []string) {
	words = map[string]bool{}
	for _, w := range ws {
		switch {
		case w == "the" || w == "a" || w == "an" || w == "of" || w == "and":
		case w >= "0" && w <= "9":
			nums = append(nums, w)
		default:
			words[w] = true
		}
	}
	slices.Sort(nums)
	return words, nums
}

func oSubMulti(a, b []string) bool {
	left := slices.Clone(b)
	for _, x := range a {
		i := slices.Index(left, x)
		if i < 0 {
			return false
		}
		left = slices.Delete(left, i, i+1)
	}
	return true
}

func oSubset(a, b map[string]bool) bool {
	for w := range a {
		if !b[w] {
			return false
		}
	}
	return true
}

func oIntersect(a, b map[string]bool) bool {
	for w := range a {
		if b[w] {
			return true
		}
	}
	return false
}

// oStop is titleSim's stop list, restated.
var oStop = map[string]bool{"the": true, "a": true, "an": true, "of": true, "and": true, "book": true,
	"part": true, "volume": true, "vol": true, "unabridged": true, "abridged": true, "audiobook": true,
	"novel": true, "series": true, "edition": true}

// oOverlap is |A∩B| / max(|A|,|B|) over words and numbers without stop words,
// 0 when only numbers are shared and a side has words.
func oOverlap(a, b []string) float64 {
	set := func(ws []string) map[string]bool {
		m := map[string]bool{}
		for _, w := range ws {
			if !oStop[w] {
				m[w] = true
			}
		}
		return m
	}
	A, B := set(a), set(b)
	if len(A) == 0 || len(B) == 0 {
		return 0
	}
	inter, alpha := 0, false
	for w := range A {
		if B[w] {
			inter++
			if !(w >= "0" && w <= "9") {
				alpha = true
			}
		}
	}
	if !alpha {
		for _, ws := range [][]string{a, b} {
			for _, w := range ws {
				if !(w >= "0" && w <= "9") {
					return 0
				}
			}
		}
	}
	return float64(inter) / float64(max(len(A), len(B)))
}

// oConflict: the two title fields conflict.
func oConflict(book, cand atitle) bool {
	bs, cs := oSegs(book), oSegs(cand)
	bw, bn := oWordsNums(flat(bs))
	cw, cn := oWordsNums(flat(cs))
	related := oIntersect(bw, cw) || (len(bw) == 0 && len(cw) == 0)
	if related && len(bn) > 0 && len(cn) > 0 && !slices.Equal(bn, cn) && !oSubMulti(bn, cn) && !oSubMulti(cn, bn) {
		return true
	}
	// a shared whole segment and a segment of its own on each side
	used := make([]bool, len(bs))
	shared, remC := false, 0
	for _, c := range cs {
		hit := false
		for j, b := range bs {
			if !used[j] && slices.Equal(c, b) {
				used[j], hit, shared = true, true, true
				break
			}
		}
		if !hit {
			remC++
		}
	}
	remB := 0
	for _, u := range used {
		if !u {
			remB++
		}
	}
	return shared && remC > 0 && remB > 0
}

// oClass is the rule, restated from its specification: "agree", "neutral" or
// "block".
func oClass(book, cand atitle) string {
	book, cand = oCanon(book), oCanon(cand)
	if oConflict(book, cand) {
		return "block"
	}
	cf := oForms(cand)
	for _, bf := range oForms(book) {
		if slices.Contains(cf, bf) {
			return "agree"
		}
	}
	// a title that is a series name, on either side, is not a title; and a
	// file name that is a series name names the series, not this book
	names := map[string]bool{}
	for _, s := range [][]string{cand.series, book.series} {
		if len(s) > 0 {
			names[join(s)] = true
		}
	}
	if len(book.path) > 0 && !names[join(book.path)] && slices.Contains(cf, join(book.path)) {
		return "agree"
	}
	if len(cand.sub) == 0 && names[join(flat(cand.segs))] {
		return "block"
	}
	if len(book.sub) == 0 && names[join(flat(book.segs))] {
		return "block"
	}
	// nothing in common
	bw, _ := oWordsNums(flat(oSegs(book)))
	cw, _ := oWordsNums(flat(oSegs(cand)))
	shared := false
	for _, c := range oSegs(cand) {
		for _, b := range oSegs(book) {
			shared = shared || slices.Equal(c, b)
		}
	}
	if !shared && !(len(bw) > 0 && len(cw) > 0 && (oSubset(bw, cw) || oSubset(cw, bw))) &&
		oOverlap(flat(oSegs(cand)), flat(oSegs(book))) < 0.5 {
		return "block"
	}
	return "neutral"
}

// ---- the generator --------------------------------------------------------

func genSeg(r *rand.Rand) []string {
	var seg []string
	switch r.IntN(5) {
	case 0: // a numbered part: "part 2"
		seg = []string{oracleMarkers[r.IntN(2)], oracleNums[r.IntN(3)]}
	case 1: // words and a number inside: "year 2"
		seg = []string{oracleWords[r.IntN(len(oracleWords))], oracleNums[r.IntN(3)]}
	default:
		for i := 0; i <= r.IntN(2); i++ {
			seg = append(seg, oracleWords[r.IntN(len(oracleWords))])
		}
		if r.IntN(8) == 0 {
			seg = append([]string{"the"}, seg...)
		}
		if r.IntN(10) == 0 {
			seg = append(seg, "and", oracleWords[r.IntN(len(oracleWords))])
		}
	}
	return seg
}

func genSide(r *rand.Rand) atitle {
	a := atitle{}
	for i := 0; i <= r.IntN(2); i++ {
		a.segs = append(a.segs, genSeg(r))
	}
	if r.IntN(5) == 0 {
		a.sub = genSeg(r)
	}
	switch r.IntN(6) {
	case 0: // a series of its own
		a.series = genSeg(r)
		for len(a.series) > 0 && a.series[0] == "the" {
			a.series = a.series[1:]
		}
	case 1: // a series that IS one of the title's segments
		a.series = slices.Clone(a.segs[r.IntN(len(a.segs))])
		for len(a.series) > 0 && a.series[0] == "the" {
			a.series = a.series[1:]
		}
	}
	if len(a.series) == 0 {
		a.series = nil
	}
	return a
}

func cloneSide(a atitle) atitle {
	out := atitle{sub: slices.Clone(a.sub), series: slices.Clone(a.series), path: slices.Clone(a.path)}
	for _, s := range a.segs {
		out.segs = append(out.segs, slices.Clone(s))
	}
	return out
}

// genMutate derives a second side from the first: the same, or with a segment
// added, dropped, replaced or a number changed, or the series swapped in as the
// title: the near-miss shapes where the rule decides.
func genMutate(r *rand.Rand, a atitle) atitle {
	b := cloneSide(a)
	switch r.IntN(9) {
	case 0: // the same
	case 1:
		b.segs = append(b.segs, genSeg(r))
	case 2:
		b.segs = append([][]string{genSeg(r)}, b.segs...)
	case 3:
		if len(b.segs) > 1 {
			b.segs = b.segs[:len(b.segs)-1]
		}
	case 4:
		b.segs[len(b.segs)-1] = genSeg(r)
	case 5:
		for i, s := range b.segs {
			for j, w := range s {
				if w >= "0" && w <= "9" {
					b.segs[i][j] = oracleNums[(slices.Index(oracleNums, w)+1)%3]
				}
			}
		}
	case 6:
		if len(b.sub) > 0 {
			b.sub = nil
		} else {
			b.sub = genSeg(r)
		}
	case 7: // move the series into the title and back
		if len(b.series) > 0 {
			b.segs = append([][]string{b.series}, b.segs...)
			b.series = nil
		} else {
			b.series = genSeg(r)
		}
	case 8:
		b.series = genSeg(r)
	}
	for len(b.series) > 0 && b.series[0] == "the" {
		b.series = b.series[1:]
	}
	if len(b.series) == 0 {
		b.series = nil
	}
	return b
}

func genPath(r *rand.Rand, a atitle, other atitle) []string {
	switch r.IntN(4) {
	case 0:
		return flat(a.segs)
	case 1:
		return join2(flat(other.segs))
	case 2:
		return genSeg(r)
	}
	return nil
}

func join2(ws []string) []string { return slices.Clone(ws) }

// ---- rendering --------------------------------------------------------------

var (
	delimiters = []string{": ", " - ", " – "}
	spellings  = map[string][]string{"1": {"1", "one"}, "2": {"2", "two", "II"}, "3": {"3", "three", "III"}, "7": {"7", "seven"}}
)

func renderWords(r *rand.Rand, seg []string) string {
	var out []string
	for i, w := range seg {
		switch {
		case w == "and" && r.IntN(2) == 0:
			out = append(out, "&")
			continue
		case w >= "0" && w <= "9":
			opts := spellings[w]
			s := opts[r.IntN(len(opts))]
			if w == "1" && i > 0 && slices.Contains(oracleMarkers, seg[i-1]) && r.IntN(2) == 0 {
				s = "I"
			}
			out = append(out, s)
			continue
		}
		out = append(out, w)
	}
	text := strings.Join(out, " ")
	switch r.IntN(4) {
	case 0:
		text = strings.ToUpper(text)
	case 1:
		text = capWords(text)
	}
	return text
}

// renderTitle joins the segments with delimiters or brackets and may add an
// edition tag.
func renderTitle(r *rand.Rand, segs [][]string) string {
	var b strings.Builder
	for i, s := range segs {
		text := renderWords(r, s)
		switch {
		case i == 0:
		case r.IntN(4) == 0:
			b.WriteString(" (" + text + ")")
			continue
		default:
			b.WriteString(delimiters[r.IntN(len(delimiters))])
		}
		b.WriteString(text)
	}
	if r.IntN(8) == 0 {
		b.WriteString(" (Unabridged)")
	}
	return b.String()
}

// capWords upper-cases the first letter of each word; the pool is ASCII.
func capWords(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

// randomName is a narrator-like name from the word pool, for the credit field.
func randomName(r *rand.Rand) string {
	return capWords(oracleWords[r.IntN(len(oracleWords))] + " " + oracleWords[r.IntN(len(oracleWords))])
}

func renderBook(r *rand.Rand, a atitle) database.Book {
	b := database.Book{Title: renderTitle(r, a.segs)}
	if len(a.sub) > 0 {
		b.Subtitle = new(renderWords(r, a.sub))
	}
	if len(a.series) > 0 {
		b.Series = &database.Series{Name: renderWords(r, a.series)}
	}
	if len(a.path) > 0 {
		b.FilePath = "/lib/Bruce Wayne/" + renderWords(r, a.path) + ".m4b"
	}
	b.Narrator = new(randomName(r))
	return b
}

func renderCand(r *rand.Rand, a atitle) metafetch.MetadataCandidate {
	c := metafetch.MetadataCandidate{Title: renderTitle(r, a.segs), Series: ""}
	if len(a.sub) > 0 {
		c.Subtitle = renderWords(r, a.sub)
	}
	if len(a.series) > 0 {
		c.Series = renderWords(r, a.series)
	}
	c.Narrator = randomName(r)
	return c
}

func outcomeClass(ch CheckResult) string {
	switch ch.Outcome {
	case OutcomeAgree:
		return "agree"
	case OutcomeBlock:
		return "block"
	}
	return string(ch.Outcome)
}

func runTitleCheck(book database.Book, cand metafetch.MetadataCandidate) CheckResult {
	book.Author = &database.Author{Name: "Bruce Wayne"}
	cand.Author = "Bruce Wayne"
	return checkTitle(&book, &cand)
}

func describePair(book database.Book, cand metafetch.MetadataCandidate) string {
	return fmt.Sprintf("book{title %q sub %q series %q path %q} cand{title %q sub %q series %q}",
		book.Title, derefStr(book.Subtitle), seriesName(&book), book.FilePath, cand.Title, cand.Subtitle, cand.Series)
}

// TestCheckTitle_MatchesTheOracle draws pairs from one pool, renders them with
// the noise the rule ignores, and requires the check to answer what the oracle,
// which is written from the rule and not from the code, answers. The pool
// reuses the title words for series names, subtitles and file names, which is
// where the first version's holes were.
func TestCheckTitle_MatchesTheOracle(t *testing.T) {
	r := rand.New(rand.NewPCG(20261117, 4688))
	counts := map[string]int{}
	bad := 0
	const pairs = 25000
	for i := 0; i < pairs; i++ {
		a := genSide(r)
		var b atitle
		if r.IntN(4) == 0 {
			b = genSide(r)
		} else {
			b = genMutate(r, a)
		}
		if r.IntN(3) == 0 {
			b.path = genPath(r, b, a)
		}
		// a is the candidate and b the book, and the other way round
		for _, swap := range []bool{false, true} {
			cand, book := a, b
			if swap {
				cand, book = b, a
				cand.path = nil // a path belongs to the book
			}
			bk, cd := renderBook(r, book), renderCand(r, cand)
			want := oClass(book, cand)
			got := outcomeClass(runTitleCheck(bk, cd))
			counts[want]++
			if got != want {
				bad++
				if bad <= 15 {
					t.Errorf("want %s, got %s: %s", want, got, describePair(bk, cd))
				}
			}
		}
	}
	t.Logf("%d checks: %v", pairs*2, counts)
	if bad > 0 {
		t.Fatalf("%d of %d checks differ from the oracle", bad, pairs*2)
	}
	for _, class := range []string{"agree", "neutral", "block"} {
		if counts[class] < pairs/20 {
			t.Fatalf("the generator reached %q only %d times: it is not exercising the rule", class, counts[class])
		}
	}
}

// ---- properties -------------------------------------------------------------

// TestCheckTitle_IsSymmetric: swapping which side is the book and which the
// candidate never changes the answer, series, subtitles and credits included.
// The first version decided "identity" from its first argument only (MINOR-4 of
// the review: the class changed with which side carried the series spelling).
func TestCheckTitle_IsSymmetric(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 4688))
	for i := 0; i < 15000; i++ {
		a := genSide(r)
		b := genSide(r)
		if r.IntN(2) == 0 {
			b = genMutate(r, a)
		}
		seed := r.Uint64()
		// A side is spelled the same whichever role it plays: its noise comes
		// from the seed and which side it is, not from the order of the calls.
		bookOf := func(side atitle, id uint64) database.Book { return renderBook(rand.New(rand.NewPCG(seed, id)), side) }
		candOf := func(side atitle, id uint64) metafetch.MetadataCandidate {
			return renderCand(rand.New(rand.NewPCG(seed, id)), side)
		}
		x := runTitleCheck(bookOf(a, 1), candOf(b, 2))
		y := runTitleCheck(bookOf(b, 2), candOf(a, 1))
		if outcomeClass(x) != outcomeClass(y) {
			t.Fatalf("book A cand B = %s (%s), book B cand A = %s (%s); A=%+v B=%+v", x.Outcome, x.Detail, y.Outcome, y.Detail, a, b)
		}
	}
}

// TestCheckTitle_SeriesCreditsAndPathsNeverChangeAConflict: for a pair whose
// TITLE fields conflict, no series name (taken from either side's own words),
// no credit and no file name makes the pair anything but a block. This is the
// review's BLOCKER-1 and MAJOR-1 as a property: a series named after the other
// storyline, a credited name as a segment.
func TestCheckTitle_SeriesCreditsAndPathsNeverChangeAConflict(t *testing.T) {
	r := rand.New(rand.NewPCG(11, 4688))
	checked := 0
	for i := 0; i < 40000 && checked < 2500; i++ {
		book, cand := genSide(r), genSide(r)
		book.series, cand.series, book.path = nil, nil, nil
		if !oConflict(oCanon(book), oCanon(cand)) {
			continue
		}
		checked++
		for _, series := range [][]string{nil, cand.segs[0], book.segs[0], flat(book.segs), flat(cand.segs), {"grayson"}} {
			for _, path := range [][]string{nil, flat(cand.segs), flat(book.segs)} {
				bk, cd := renderBook(r, atitle{segs: book.segs, sub: book.sub, series: series, path: path}),
					renderCand(r, atitle{segs: cand.segs, sub: cand.sub, series: series})
				cd.Narrator, cd.Author = "Dick Grayson", "Bruce Wayne"
				if got := outcomeClass(runTitleCheck(bk, cd)); got != "block" {
					t.Fatalf("conflicting titles became %s with series %q path %q: %s", got, series, path, describePair(bk, cd))
				}
			}
		}
	}
	if checked < 1000 {
		t.Fatalf("only %d conflicting pairs generated", checked)
	}
}

// TestCheckTitle_AddedTextNeverAgrees: text added to ONE side's title can
// never make an agreeing pair stay agreeing, and text added to a pair that does
// not agree can never make it agree. It does not depend on the oracle: the
// added words are ones no pool word equals.
func TestCheckTitle_AddedTextNeverAgrees(t *testing.T) {
	r := rand.New(rand.NewPCG(13, 4688))
	fresh := []string{"zephyr", "quartz", "mirage"}
	addTo := func(a atitle, how int) atitle {
		b := cloneSide(a)
		w := []string{fresh[how%len(fresh)]}
		switch how % 6 {
		case 0: // a word in the last segment
			b.segs[len(b.segs)-1] = append(b.segs[len(b.segs)-1], w...)
		case 1: // a word in the first segment
			b.segs[0] = append(w, b.segs[0]...)
		case 2: // a segment after
			b.segs = append(b.segs, w)
		case 3: // a segment before
			b.segs = append([][]string{w}, b.segs...)
		case 4: // a number
			b.segs[len(b.segs)-1] = append(b.segs[len(b.segs)-1], "7") // the pool has no 7
		case 5: // a subtitle
			b.sub = append(slices.Clone(b.sub), w...)
		}
		return b
	}
	agreeing := 0
	for i := 0; i < 6000; i++ {
		a := genSide(r)
		b := genMutate(r, a)
		a.path, b.path = nil, nil
		baseGot := oClass(b, a)
		for how := 0; how < 6; how++ {
			for _, onCand := range []bool{true, false} {
				cand, book := a, b
				if onCand {
					cand = addTo(a, how)
				} else {
					book = addTo(b, how)
				}
				got := outcomeClass(runTitleCheck(renderBook(r, book), renderCand(r, cand)))
				if got == "agree" {
					t.Fatalf("text added (how=%d, onCand=%v) and the pair AGREES: book %+v cand %+v", how, onCand, book, cand)
				}
			}
		}
		if baseGot == "agree" {
			agreeing++
		}
	}
	if agreeing < 300 {
		t.Fatalf("only %d agreeing base pairs: the property was not exercised", agreeing)
	}
}

// ---- the normalisation, by example ------------------------------------------

func TestTitleForms_NormalisationIsClosedAndExact(t *testing.T) {
	eq := func(a, b string) bool { return slices.Equal(formTokens(a), formTokens(b)) }
	for _, c := range []struct{ a, b string }{
		{"Hush: Part I", "Hush: Part 1"},
		{"Hush: Part One", "Hush Part 1"},
		{"Hush Ⅱ", "Hush II"}, // unicode roman numeral
		{"Hush Ⅱ", "Hush 2"},
		{"Gotham Year Twenty-One", "Gotham Year 21"},
		{"The Third Robin", "The 3rd Robin"},
		{"Batman & Robin", "Batman and Robin"},
		{"The Long Halloween", "Long Halloween"},
		{"Hush (Unabridged)", "Hush"},
		{"Hush: A Novel", "Hush"},
		{"Gotham Noël", "Gotham Noel"},
		{"Ra's Al Ghul", "Ras Al Ghul"},
		{"BATMAN: dark victory", "Batman - Dark Victory"},
		{"Batman: The Long Halloween", "Batman (The Long Halloween)"},
	} {
		if !eq(c.a, c.b) {
			t.Errorf("%q = %v, %q = %v, want equal", c.a, formTokens(c.a), c.b, formTokens(c.b))
		}
	}
	for _, c := range []struct{ a, b, why string }{
		{"Hush (Abridged)", "Hush", "an abridged recording is a different edition"},
		{"Who Am I", "Who Am 1", "a lone I after a word is the pronoun"},
		{"Hush: Part 1", "Hush: Part 2", "different numbers"},
		{"Hush Ⅱ", "Hush Ⅲ", "unicode roman numerals are read as values"},
		{"Night of Seven Owls", "Night of Five Owls", "numbers after \"of\" count"},
		{"The Third Robin", "The Three Robin", "an ordinal is not a cardinal"},
		{"Year One", "Year Two", "number words are read as values"},
		{"Batman Hush", "Hush Batman", "word order is part of the title"},
		{"Grayson: Year One", "Year One", "a segment is not dropped for being a name"},
		{"Knightfall: Book 2", "Knightfall", "a number is not dropped"},
	} {
		if eq(c.a, c.b) {
			t.Errorf("%q equals %q (%s)", c.a, c.b, c.why)
		}
	}
	if got := formTokens("Novel"); len(got) != 1 {
		t.Errorf("a title that is only an edition word was emptied: %v", got)
	}
}

func TestTitleForms_ASideWithASubtitleHasNoBareForms(t *testing.T) {
	for _, c := range []struct {
		title, sub, series string
		want               []string
	}{
		{"Hush", "", "", []string{"hush"}},
		{"Hush", "", "Batman", []string{"hush", "batman hush"}},
		{"Hush", "Part 2", "", []string{"hush part #2"}},
		{"Hush", "Part 2", "Batman", []string{"hush part #2", "batman hush part #2"}},
	} {
		if got := titleForms(c.title, c.sub, c.series); !slices.Equal(got, c.want) {
			t.Errorf("titleForms(%q,%q,%q) = %q, want %q", c.title, c.sub, c.series, got, c.want)
		}
	}
}

// ---- the tally rule, by example ---------------------------------------------

// TestEvaluate_NeutralTitleRefusalIsTruthfulAndOverridable: a neutral title
// with two other agreements and no agreeing runtime is refused as
// title_unconfirmed, whose detail says what is missing, and an owner review can
// lift it like the insufficient_evidence it replaces.
func TestEvaluate_NeutralTitleRefusalIsTruthfulAndOverridable(t *testing.T) {
	book := database.Book{Title: "Year One", Narrator: new("Alfred Pennyworth"), Author: &database.Author{Name: "Bruce Wayne"}}
	cand := metafetch.MetadataCandidate{Title: "Grayson: Year One", Author: "Bruce Wayne", Narrator: "Alfred Pennyworth", Score: 0.95}
	v := Evaluate(&book, snap(&book), database.ComputeBookRuntime(&book, nil), &cand, nil)
	if v.Allowed || v.Reason != ReasonTitleUnconfirmed {
		t.Fatalf("allowed=%v reason=%q (%s), want refused as %q", v.Allowed, v.Reason, v.Detail, ReasonTitleUnconfirmed)
	}
	if v.Evidence.Agreements < MinAgreements {
		t.Fatalf("only %d agreements: the case must have enough of them for the count not to be what refuses", v.Evidence.Agreements)
	}
	for _, want := range []string{"title only partly matches", "runtime does not confirm it"} {
		if !strings.Contains(v.Detail, want) {
			t.Errorf("detail %q does not say %q", v.Detail, want)
		}
	}
	if !v.OwnerReviewOverridable() || !v.UnseenOwnerReviewOverridable() {
		t.Error("an owner review must be able to lift title_unconfirmed, as it lifts insufficient_evidence")
	}
	if !StructuralRefusal(ReasonTitleUnconfirmed) {
		t.Error("title_unconfirmed is a judgement about the book's own files and the candidate: structural, like insufficient_evidence")
	}
	if got := v.RefusingReasons(); !slices.Equal(got, []string{ReasonTitleUnconfirmed}) {
		t.Errorf("RefusingReasons = %v", got)
	}

	// With the runtime agreeing the same pair applies.
	book.Duration, cand.DurationSec = new(36000), 36600
	if v := Evaluate(&book, snap(&book), database.ComputeBookRuntime(&book, nil), &cand, nil); !v.Allowed {
		t.Fatalf("an agreeing runtime must let the neutral title through: %s (%s)", v.Reason, v.Detail)
	}
	// 6% off is neutral, not agreeing.
	cand.DurationSec = 38160
	if v := Evaluate(&book, snap(&book), database.ComputeBookRuntime(&book, nil), &cand, nil); v.Allowed || v.Reason != ReasonTitleUnconfirmed {
		t.Fatalf("a runtime 6%% off is not an agreement: allowed=%v reason=%q", v.Allowed, v.Reason)
	}
}

// TestEvaluate_AgreeingTitleKeepsToday: an AGREEING title is unchanged by the
// runtime rule: author plus title applies with no runtime at all.
func TestEvaluate_AgreeingTitleKeepsToday(t *testing.T) {
	book := database.Book{Title: "Hush", Author: &database.Author{Name: "Bruce Wayne"}}
	cand := metafetch.MetadataCandidate{Title: "Hush", Author: "Bruce Wayne", Score: 0.95}
	if v := Evaluate(&book, snap(&book), database.ComputeBookRuntime(&book, nil), &cand, nil); !v.Allowed {
		t.Fatalf("an agreeing title with an agreeing author must apply: %s (%s)", v.Reason, v.Detail)
	}
}

// TestTally_NeutralTitleNeedsAnAgreeingRuntimeCheck holds the rule at the place
// the agreements are counted, whoever built the checks: a verdict with no
// runtime check at all, or one that is unknown or neutral, is refused as
// title_unconfirmed however many other checks agree; only an agreeing runtime
// lets the neutral title through.
func TestTally_NeutralTitleNeedsAnAgreeingRuntimeCheck(t *testing.T) {
	title := CheckResult{Name: "title", Outcome: OutcomeNeutral, Detail: "one title continues past the text both share"}
	others := []CheckResult{
		{Name: "author_role", Outcome: OutcomeAgree},
		{Name: "narrator", Outcome: OutcomeAgree},
		{Name: "asin", Outcome: OutcomeAgree},
	}
	for _, c := range []struct {
		name    string
		runtime *CheckResult
		pass    bool
	}{
		{"no runtime check", nil, false},
		{"runtime unknown", &CheckResult{Name: "runtime", Outcome: OutcomeUnknown}, false},
		{"runtime neutral", &CheckResult{Name: "runtime", Outcome: OutcomeNeutral, Detail: "7% apart"}, false},
		{"runtime agrees", &CheckResult{Name: "runtime", Outcome: OutcomeAgree}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := EvidenceVerdict{Checks: append([]CheckResult{title}, others...)}
			if c.runtime != nil {
				v.Checks = append(v.Checks, *c.runtime)
			}
			v.tally()
			if v.Pass != c.pass {
				t.Fatalf("Pass = %v (reason %q: %s), want %v", v.Pass, v.Reason, v.Detail, c.pass)
			}
			if !c.pass && v.Reason != ReasonTitleUnconfirmed {
				t.Fatalf("reason = %q, want %q", v.Reason, ReasonTitleUnconfirmed)
			}
		})
	}
	// An AGREEING title is not asked for a runtime.
	v := EvidenceVerdict{Checks: []CheckResult{{Name: "title", Outcome: OutcomeAgree}, others[0]}}
	v.tally()
	if !v.Pass {
		t.Fatalf("an agreeing title with an agreeing author must pass: %q (%s)", v.Reason, v.Detail)
	}
}
