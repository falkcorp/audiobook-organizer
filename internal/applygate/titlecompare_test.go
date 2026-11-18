// file: internal/applygate/titlecompare_test.go
// version: 2.4.0
// guid: 0b5a7e12-93c4-4d68-a1f7-6e2d9c40b835
// last-edited: 2026-11-18

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
//
// The oracle restates the title rule from its one-page specification
// (E_rule.md in the review notes; the rule is also stated in the doc comments of
// titleforms.go and titlecompare.go) and calls NONE of the production helpers:
// no formTokens, titleForms, titleSegments, seqnum or partial.go. Where the
// production code normalises text, the oracle works on words the generator
// already holds in canonical form, so a disagreement is a disagreement about
// the rule, not about a shared helper.
//
// What the oracle models, with the rule-page line each part restates:
//   - forms of a side: title, or title and subtitle, each also behind the
//     series; a side with a subtitle has no bare-title form (Forms, book side).
//   - candidate-only forms: "S N T", "T S N", "S N T U", "U N T" with the
//     position bare or behind Book/Books/Volume/Vol; "T A", "A T", "S N T A",
//     "A S N T" for the author and the narrator, skipped when the credit equals
//     the title or the series name (Forms, b-pos and an).
//   - a subtitle that is only an edition phrase, or exactly "S, Book N" /
//     "Book N of S" for the declared S and N on a candidate that is not
//     self-titled, is not a subtitle for the candidate forms (Forms, e).
//   - the forms that leave a subtitle out are offered only by a candidate with
//     no real subtitle (ruling 2; the subtitle is never left out).
//   - a credit never equals a series (ruling 3).
//   - segment-set equality of the two title fields agrees (Forms, p).
//   - agree is decided before any conflict test (Order of decisions, 1).
//   - numbers are compared by kind: a volume against a volume and a part
//     against a part; "Part 1 of 3" is part 1 (Numbers); the raw-token rule is
//     the fallback after the kind checks (Numbers, last bullet).
//   - a file or folder name is a form of the book only when the stored title is
//     blank (Order of decisions, 3). The generator has no placeholder titles
//     ("Chapter 3"); those are covered by the by-example tests.
//
// Two kinds of pair are skipped by the callers, because the oracle does not
// model them and a guess would only copy the code:
//   - a side that states more than one number (oStated.ok): which of two volume
//     numbers the reader takes is the reader's business, not the rule's;
//   - a book that has a file name and a title that mentions a number or a
//     part/book heading (oMaybePlaceholder): whether such a title is one that
//     "says nothing" (a chapter heading, "Gotham 3", "Book Two") is the
//     placeholder predicate's business (metadata.NeedsFolderEvidence,
//     MayBeUnsearchableTitle), which decides whether the file name may be a
//     form of the book. Path forms are covered by the by-example tests.

// atitle is one side of a pair in the abstract: segments of canonical words
// (numbers as digit strings), an optional subtitle, an optional series, and the
// fields only the candidate or only the book declares.
type atitle struct {
	segs   [][]string
	sub    []string
	series []string
	path   []string // a file or folder name, one segment; empty for none (book only)
	// Candidate-only declared fields.
	narr    []string // the narrator's name
	pos     string   // the declared series position, "" for none
	edition int      // 1: the subtitle is "A Novel", 2: "Unabridged" (sub is then empty)
	stmt    int      // 1: the subtitle is "S, Book N", 2: "Book N of S" (sub holds its words)
	// Book only.
	blank bool // the stored title is blank (segs is then empty)
}

var (
	oracleWords   = []string{"hush", "knightfall", "victory", "dark", "halloween", "long", "robin", "penguin", "riddler", "gotham", "owls", "court", "year", "mask"}
	oracleMarkers = []string{"part", "book"}
	oracleNums    = []string{"1", "2", "3"}
	// oraclePositionMarkers are the spellings the rule page lists for a
	// position: bare, or behind one of four marker words.
	oraclePositionMarkers = [][]string{nil, {"book"}, {"books"}, {"volume"}, {"vol"}}
	oracleAuthor          = []string{"bruce", "wayne"}
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
	out := a
	out.sub, out.series, out.path, out.narr = drop(a.sub), drop(a.series), drop(a.path), drop(a.narr)
	out.segs = nil
	for _, sg := range a.segs {
		out.segs = append(out.segs, drop(sg))
	}
	return out
}

// oForm is a whole form with the roles it is built from.
type oForm struct {
	text           string
	series, credit bool
}

// oBookForms are the whole forms of the book: the title (or title and
// subtitle) joined, each also behind the series; no bare title when there is a
// subtitle.
func oBookForms(a atitle) []oForm {
	if len(a.segs) == 0 {
		return nil
	}
	t := flat(a.segs)
	if len(a.sub) == 0 {
		out := []oForm{{text: join(t)}}
		if len(a.series) > 0 {
			out = append(out, oForm{text: join(a.series, t), series: true})
		}
		return out
	}
	out := []oForm{{text: join(t, a.sub)}}
	if len(a.series) > 0 {
		out = append(out, oForm{text: join(a.series, t, a.sub), series: true})
	}
	return out
}

// oSelfTitled: the title is the series name.
func oSelfTitled(a atitle) bool {
	return len(a.series) > 0 && join(flat(a.segs)) == join(a.series)
}

// oDroppable: the subtitle of the candidate is exactly its own declared series
// and position in a closed spelling ("S, Book N", "Book N of S"), on a
// candidate that is not self-titled. (An edition subtitle never reaches sub.)
func oDroppable(a atitle) bool {
	return a.stmt != 0 && len(a.series) > 0 && a.pos != "" && !oSelfTitled(a)
}

// oEffectiveSub is the subtitle the candidate says once the closed
// non-subtitles are dropped.
func oEffectiveSub(a atitle) []string {
	if oDroppable(a) {
		return nil
	}
	return a.sub
}

// oCandForms are the whole forms of the candidate.
func oCandForms(a atitle) []oForm {
	if len(a.segs) == 0 {
		return nil
	}
	T, S := flat(a.segs), a.series
	out := oBookForms(a) // the declared fields as they stand
	if oDroppable(a) {
		b := a
		b.sub = nil
		out = append(out, oBookForms(b)...)
	}
	U := oEffectiveSub(a)
	var marked [][]string
	if a.pos != "" {
		for _, m := range oraclePositionMarkers {
			marked = append(marked, append(slices.Clone(m), a.pos))
		}
	}
	if len(U) > 0 {
		// a candidate with a real subtitle offers only the forms that say it
		for _, mn := range marked {
			if len(S) > 0 {
				out = append(out, oForm{text: join(S, mn, T, U), series: true})
			}
			out = append(out, oForm{text: join(U, mn, T)})
		}
		return out
	}
	for _, mn := range marked {
		if len(S) > 0 {
			out = append(out, oForm{text: join(S, mn, T), series: true}, oForm{text: join(T, S, mn), series: true})
		}
	}
	for _, X := range [][]string{oracleAuthor, a.narr} {
		if len(X) == 0 || join(X) == join(T) || join(X) == join(S) {
			continue
		}
		out = append(out, oForm{text: join(T, X), credit: true}, oForm{text: join(X, T), credit: true})
		for _, mn := range marked {
			if len(S) > 0 {
				out = append(out, oForm{text: join(S, mn, T, X), series: true, credit: true},
					oForm{text: join(X, S, mn, T), series: true, credit: true})
			}
		}
	}
	return out
}

// oMatch: the same text, and no credit standing for a series.
func oMatch(c, b oForm) bool {
	return c.text == b.text && !(c.credit && b.series)
}

func oAnyMatch(cands, books []oForm) bool {
	for _, c := range cands {
		for _, b := range books {
			if oMatch(c, b) {
				return true
			}
		}
	}
	return false
}

// oSegs are the segments of the title field: the title's, then the subtitle.
func oSegs(a atitle) [][]string {
	out := slices.Clone(a.segs)
	if len(a.sub) > 0 {
		out = append(out, a.sub)
	}
	return out
}

// oSegKey is the segment-set key of the title field as a candidate states it
// for agreement: the effective subtitle, and the book's declared one.
func oSegKey(segs [][]string, sub []string) string {
	var js []string
	for _, s := range segs {
		js = append(js, join(s))
	}
	if len(sub) > 0 {
		js = append(js, join(sub))
	}
	slices.Sort(js)
	return strings.Join(js, "|")
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

// oStated is what one side states about its numbers, read off the shapes the
// generator builds (one numbered segment at most):
//
//	"part N" and "part N of M": part N
//	"book N" and "<word> N":    volume N
//
// ok is false for a side that states more than one number, which the oracle
// does not model.
type oStated struct {
	volume, part string // "" when the side states none
	ok           bool
}

func oRead(a atitle) oStated {
	st := oStated{ok: true}
	count := 0
	for _, seg := range oSegs(a) {
		switch {
		case len(seg) == 2 && seg[0] == "part" && seg[1] >= "0" && seg[1] <= "9":
			st.part, count = seg[1], count+1
		case len(seg) == 4 && seg[0] == "part" && seg[2] == "of":
			st.part, count = seg[1], count+1
		case len(seg) == 2 && seg[1] >= "0" && seg[1] <= "9":
			st.volume, count = seg[1], count+1
		}
	}
	if a.stmt != 0 { // "S, Book N" states volume N
		st.volume, count = a.pos, count+1
	}
	st.ok = count <= 1
	return st
}

// oMaybePlaceholder: a stored title the oracle cannot say carries evidence.
func oMaybePlaceholder(a atitle) bool {
	for _, seg := range oSegs(a) {
		for _, w := range seg {
			if (w >= "0" && w <= "9") || w == "part" || w == "book" {
				return true
			}
		}
	}
	return false
}

// oConflict: the two title fields conflict, with each side's own numbers (the
// stated ones by kind, then the raw numbers) and segments.
func oConflict(book, cand atitle) bool {
	bs, cs := oSegs(book), oSegs(cand)
	bw, bn := oWordsNums(flat(bs))
	cw, cn := oWordsNums(flat(cs))
	related := oIntersect(bw, cw) || (len(bw) == 0 && len(cw) == 0)
	if related {
		b, c := oRead(book), oRead(cand)
		if b.volume != "" && c.volume != "" && b.volume != c.volume {
			return true
		}
		if b.part != "" && c.part != "" && b.part != c.part {
			return true
		}
		// the raw-token fallback, for numbers the kind checks do not decide
		if len(bn) > 0 && len(cn) > 0 && !oSubMulti(bn, cn) && !oSubMulti(cn, bn) {
			return true
		}
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

// oUnrelated: nothing in common between two title fields given as segments.
func oUnrelated(bs, cs [][]string) bool {
	bw, _ := oWordsNums(flat(bs))
	cw, _ := oWordsNums(flat(cs))
	for _, c := range cs {
		for _, b := range bs {
			if slices.Equal(c, b) {
				return false
			}
		}
	}
	// bs is the stored title: a candidate that adds words to it is related at
	// any size; one that is a few words OF a longer stored title must share
	// half the words (oOverlap), as the rule's "bound" says.
	if len(bw) > 0 && len(cw) > 0 && oSubset(bw, cw) {
		return false
	}
	return oOverlap(flat(cs), flat(bs)) < 0.5
}

// oClass is the rule, restated from its specification, in its order of
// decisions: "agree", "neutral" or "block".
func oClass(book, cand atitle) string {
	book, cand = oCanon(book), oCanon(cand)
	// 1. a whole declared form is equal, or the two title fields say the same
	//    segments in another order
	//    One exception: a candidate that is its own series name with no real
	//    subtitle says no volume by its bare title, by the series name in
	//    front of it, or by its title with a credit beside it, so those forms
	//    and the segment key are NEUTRAL, not agree. A form that states the
	//    position agrees as ever.
	selfBare := oSelfTitled(cand) && len(oEffectiveSub(cand)) == 0
	forms := oCandForms(cand)
	var bare []oForm
	if selfBare {
		t := join(flat(cand.segs))
		bare = []oForm{{text: t}, {text: t + " " + t, series: true}}
		for _, f := range forms {
			if f.credit && !f.series {
				bare = append(bare, f)
			}
		}
		forms = slices.DeleteFunc(slices.Clone(forms), func(f oForm) bool { return slices.Contains(bare, f) })
	}
	if oAnyMatch(forms, oBookForms(book)) {
		return "agree"
	}
	k := oSegKey(book.segs, book.sub)
	sameSegs := k != "" && (k == oSegKey(cand.segs, oEffectiveSub(cand)) || k == oSegKey(cand.segs, cand.sub))
	if selfBare {
		if oAnyMatch(bare, oBookForms(book)) || sameSegs {
			return "neutral"
		}
	} else if sameSegs {
		return "agree"
	}
	// 2. the two title fields conflict (a blank stored title has none)
	if !book.blank && oConflict(book, cand) {
		return "block"
	}
	// 3. a file or folder name is a form of the book only for a blank stored
	//    title; a name equal to a series name names the series
	names := map[string]bool{}
	for _, s := range [][]string{cand.series, book.series} {
		if len(s) > 0 {
			names[join(s)] = true
		}
	}
	if book.blank && len(book.path) > 0 && !names[join(book.path)] &&
		oAnyMatch(forms, []oForm{{text: join(book.path)}}) {
		return "agree"
	}
	// 4. a title that is a series name, on either side, is not a title
	// (the rule says "no subtitle": the text as written, so an edition phrase
	// such as "A Novel" counts as one here, unlike in the forms)
	if len(cand.sub) == 0 && cand.edition == 0 && names[join(flat(cand.segs))] {
		return "block"
	}
	if !book.blank && len(book.sub) == 0 && names[join(flat(book.segs))] {
		return "block"
	}
	if book.blank {
		// All the book says is its file and folder names. The folder here is the
		// author's, unrelated to any title; another work altogether blocks, and
		// a file name that names the series is skipped, leaving only the folder.
		// A name that conflicts with the candidate (another part, another
		// number) is not "nothing in common" either, and a path never causes a
		// conflict: that case stays neutral.
		pseudo := atitle{segs: [][]string{book.path}}
		if names[join(book.path)] || (len(book.path) > 0 && !oConflict(pseudo, cand) && oUnrelated(oSegs(pseudo), oSegs(cand))) {
			return "block"
		}
		return "neutral"
	}
	if oUnrelated(oSegs(book), oSegs(cand)) {
		return "block"
	}
	return "neutral"
}

// ---- the generator --------------------------------------------------------

func genSeg(r *rand.Rand) []string {
	var seg []string
	switch r.IntN(6) {
	case 5: // a part of a set: "part 1 of 3"
		seg = []string{"part", oracleNums[r.IntN(3)], "of", oracleNums[r.IntN(3)]}
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
	out := a
	out.sub, out.series, out.path, out.narr = slices.Clone(a.sub), slices.Clone(a.series), slices.Clone(a.path), slices.Clone(a.narr)
	out.segs = nil
	for _, s := range a.segs {
		out.segs = append(out.segs, slices.Clone(s))
	}
	return out
}

// withCandidateFields gives a side the fields only a candidate declares: a
// narrator from the title word pool (so it can collide with a series or a
// title), a series position, and a subtitle that is an edition phrase or the
// candidate's own series statement.
func withCandidateFields(r *rand.Rand, a atitle) atitle {
	b := cloneSide(a)
	b.narr = nil
	for i := 0; i <= r.IntN(2); i++ {
		b.narr = append(b.narr, oracleWords[r.IntN(len(oracleWords))])
	}
	if len(b.series) > 0 && r.IntN(2) == 0 {
		b.pos = oracleNums[r.IntN(3)]
	}
	switch r.IntN(8) {
	case 0: // an edition phrase in place of a subtitle
		b.sub, b.edition = nil, 1+r.IntN(2)
	case 1, 2: // "S, Book N" or "Book N of S"
		if len(b.series) > 0 && b.pos != "" {
			b.stmt = 1 + r.IntN(2)
			if b.stmt == 1 {
				b.sub = append(slices.Clone(b.series), "book", b.pos)
			} else {
				b.sub = append([]string{"book", b.pos, "of"}, b.series...)
			}
		}
	}
	return b
}

// withBlankTitle makes a book whose stored title is blank, with a file name.
func withBlankTitle(a atitle, name []string) atitle {
	b := cloneSide(a)
	b.segs, b.sub, b.blank, b.path = nil, nil, true, slices.Clone(name)
	return b
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

// genStatementPair draws a book whose title states the candidate's series and
// position ("Book 2 of Batman: Hush", "Batman, Book 2: Hush") and a candidate
// with the same title and series whose subtitle is that statement.
func genStatementPair(r *rand.Rand) (book, cand atitle) {
	T := genSeg(r)
	S := genSeg(r)
	for len(S) > 0 && S[0] == "the" {
		S = S[1:]
	}
	if len(S) == 0 || join(S) == join(T) {
		S = []string{"mask", "penguin"}
		if join(T) == join(S) {
			T = []string{"robin"}
		}
	}
	n := oracleNums[r.IntN(3)]
	stmt := 1 + r.IntN(2)
	var words []string
	if stmt == 1 {
		words = append(slices.Clone(S), "book", n)
	} else {
		words = append([]string{"book", n, "of"}, S...)
	}
	book = atitle{segs: [][]string{slices.Clone(words), slices.Clone(T)}}
	cand = atitle{segs: [][]string{slices.Clone(T)}, series: slices.Clone(S), pos: n, stmt: stmt, sub: slices.Clone(words)}
	return book, cand
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
	if a.blank {
		b.Title = ""
	}
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

// renderCand renders a candidate. A narrator the side declares is used;
// otherwise one is drawn from the word pool, so a credit can collide with a
// title or a series.
func renderCand(r *rand.Rand, a atitle) metafetch.MetadataCandidate {
	c := metafetch.MetadataCandidate{Title: renderTitle(r, a.segs), Series: ""}
	switch {
	case a.stmt == 1:
		c.Subtitle = renderWords(r, a.series) + ", Book " + a.pos
	case a.stmt == 2:
		c.Subtitle = "Book " + a.pos + " of " + renderWords(r, a.series)
	case a.edition == 1:
		c.Subtitle = "A Novel"
	case a.edition == 2:
		c.Subtitle = "Unabridged"
	case len(a.sub) > 0:
		c.Subtitle = renderWords(r, a.sub)
	}
	if len(a.series) > 0 {
		c.Series = renderWords(r, a.series)
	}
	c.SeriesPosition = a.pos
	if len(a.narr) > 0 {
		c.Narrator = renderWords(r, a.narr)
	} else {
		c.Narrator = randomName(r)
	}
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

func describeFull(book, cand atitle, bk database.Book, cd metafetch.MetadataCandidate) string {
	return fmt.Sprintf("%s | abstract book %+v cand %+v | cand narrator %q position %q", describePair(bk, cd), book, cand, cd.Narrator, cd.SeriesPosition)
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
	skipped, bad := 0, 0
	feature := map[string]int{}
	pairs := scaled(25000)
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
			cand = withCandidateFields(r, cand)
			if r.IntN(10) == 0 && len(book.path) > 0 {
				book = withBlankTitle(book, book.path)
			}
			if !oRead(book).ok || !oRead(cand).ok || (len(book.path) > 0 && !book.blank && oMaybePlaceholder(book)) {
				skipped++
				continue
			}
			bk, cd := renderBook(r, book), renderCand(r, cand)
			want := oClass(book, cand)
			got := outcomeClass(runTitleCheck(bk, cd))
			counts[want]++
			if book.blank {
				feature["blank stored title, "+want]++
			}
			if cand.pos != "" || cand.stmt != 0 || cand.edition != 0 {
				feature["candidate position/closed subtitle, "+want]++
			}
			if got != want {
				bad++
				if bad <= 15 {
					t.Errorf("want %s, got %s: %s", want, got, describeFull(book, cand, bk, cd))
				}
			}
		}
	}
	t.Logf("%d checks (%d skipped, see the oracle's header): %v; features %v", pairs*2-skipped, skipped, counts, feature)
	if bad > 0 {
		t.Fatalf("%d checks differ from the oracle", bad)
	}
	for _, class := range []string{"agree", "neutral", "block"} {
		if counts[class] < pairs/20 {
			t.Fatalf("the generator reached %q only %d times: it is not exercising the rule", class, counts[class])
		}
	}
	for _, f := range []string{"blank stored title, agree", "blank stored title, block", "candidate position/closed subtitle, agree"} {
		if feature[f] < scaled(100) {
			t.Fatalf("the generator reached %q only %d times", f, feature[f])
		}
	}
}

// ---- properties -------------------------------------------------------------

// TestCheckTitle_IsSymmetric: swapping which side is the book and which the
// candidate never changes the answer, series and subtitles included, EXCEPT for
// a pair whose words are a proper subset one way round (the subset bound
// compareTitleFields applies in one direction only; see the rule examples
// "a few words of a longer stored title" and its mirror) and for a pair with a
// side whose title is its own series name (checkTitle's self-titled exception,
// the candidate's side only). The first
// version decided "identity" from its first argument only (MINOR-4 of the
// review: the class changed with which side carried the series spelling).
//
// The property is stated for the DECLARED title fields, and that is the whole
// of what it can say. The candidate-only forms (the candidate's author or
// narrator beside its title, its series position, a closed non-subtitle) are
// one-sided on purpose: a stored title is the book's own text, and "title plus
// the narrator's name" is a thing a tagger writes into a stored title, while a
// stored credit is never a candidate's title. So the candidate here has no
// credit (its narrator is blanked; the author is the same on both sides) and no
// position, and the two orders must agree. What the one-sided forms may do is
// the next two tests: they only move a pair toward AGREE, and the same fields
// on the book change nothing.
func TestCheckTitle_IsSymmetric(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 4688))
	compared, asymmetric, selfTitled := 0, 0, 0
	exempt := map[string]int{}
	for i := 0; i < scaled(15000); i++ {
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
			c := renderCand(rand.New(rand.NewPCG(seed, id)), side)
			c.Narrator = ""
			return c
		}
		x := runTitleCheck(bookOf(a, 1), candOf(b, 2))
		y := runTitleCheck(bookOf(b, 2), candOf(a, 1))
		cx, cy := outcomeClass(x), outcomeClass(y)
		if cx == cy {
			compared++
			continue
		}
		// The pair differs: x is book A with candidate B, y is book B with
		// candidate A. Each exemption names the DIRECTION of the difference, so
		// a pair that differs the wrong way round, or in a class the rule does
		// not produce, still fails.
		aw, _ := oWordsNums(flat(oSegs(oCanon(a))))
		bw, _ := oWordsNums(flat(oSegs(oCanon(b))))
		switch {
		// The subset bound: a candidate that ADDS words to the stored title is
		// neutral; one that is a few words OF a longer stored title must share
		// half of them (compareTitleFields) and is a block when it does not.
		// Pinned by example. So the order whose candidate is the proper subset
		// of the stored words is the block and the other order is neutral.
		case len(aw) > 0 && len(bw) > 0 && oSubset(aw, bw) != oSubset(bw, aw):
			asymmetric++
			candIsSubsetInX := oSubset(bw, aw) // candidate B is a subset of book A
			want := [2]string{"neutral", "block"}
			if candIsSubsetInX {
				want = [2]string{"block", "neutral"}
			}
			if [2]string{cx, cy} != want {
				t.Fatalf("subset pair differs the wrong way round: book A cand B = %s (%s), book B cand A = %s (%s), want %v; A=%+v B=%+v",
					cx, x.Detail, cy, y.Detail, want, a, b)
			}
			exempt["subset "+cx+"/"+cy]++
		// A candidate whose title is its own series name says no volume by it
		// (neutral or a series-name block); a stored title that is its series
		// name is an ordinary stored title. The side that is the self-titled
		// candidate never comes out higher (block, neutral, agree) than the
		// same side as the book. (Pinned by the rule examples.)
		case oSelfTitled(oCanon(a)) || oSelfTitled(oCanon(b)):
			selfTitled++
			rank := map[string]int{"block": 0, "neutral": 1, "agree": 2}
			// y has A as the candidate, x has B as the candidate
			selfCandHigher := (oSelfTitled(oCanon(a)) && !oSelfTitled(oCanon(b)) && rank[cy] > rank[cx]) ||
				(oSelfTitled(oCanon(b)) && !oSelfTitled(oCanon(a)) && rank[cx] > rank[cy])
			// and the difference is always agree against neutral: a self-titled
			// candidate's bare forms are neutral where the stored side's agree
			if selfCandHigher || !((cx == "agree" && cy == "neutral") || (cx == "neutral" && cy == "agree")) {
				t.Fatalf("a self-titled pair differs in the wrong direction or in classes other than agree and neutral: book A cand B = %s (%s), book B cand A = %s (%s); A=%+v B=%+v",
					cx, x.Detail, cy, y.Detail, a, b)
			}
			exempt["self-titled "+cx+"/"+cy]++
		default:
			t.Fatalf("book A cand B = %s (%s), book B cand A = %s (%s); A=%+v B=%+v", x.Outcome, x.Detail, y.Outcome, y.Detail, a, b)
		}
	}
	// The exemptions must not swallow the property: most pairs are compared and
	// agree in both orders, and the floor fails the test if a change makes the
	// two exemptions (or a skip) take over.
	if compared < scaled(13500) {
		t.Fatalf("only %d pairs were compared in both orders (floor %d): the exemptions are taking over the property", compared, scaled(13500))
	}
	t.Logf("%d pairs compared, %d differ only by the subset bound, %d only by a self-titled side; exempt classes %v", compared, asymmetric, selfTitled, exempt)
}

// A note on what is NOT in the property: declaring a SERIES on a candidate that
// is titled with it ("Batman" in the series "Batman") turns an equal bare title
// from AGREE into NEUTRAL (checkTitle's self-titled rule, pinned by the rule
// examples). The property adds a position and a narrator to a candidate that
// already has its series, never the series itself, so it does not draw that
// move.
//
// TestCheckTitle_CandidateOnlyFieldsOnlyMoveTowardAgree: the fields only a
// candidate declares, its narrator and its series position, add forms and
// nothing else. Adding them to a candidate leaves the class as it was or moves
// it up the order block, neutral, agree, never down (agree is decided first, so
// a form that now matches wins over a conflict the plain fields had; for a
// candidate whose title is its own series name the credit forms are neutral,
// so a credit that completes the stored title lifts a block to neutral and no
// further). The same fields on the
// BOOK are not read: a stored credit or position is never part of a form.
func TestCheckTitle_CandidateOnlyFieldsOnlyMoveTowardAgree(t *testing.T) {
	r := rand.New(rand.NewPCG(17, 4688))
	moved, same, statements := 0, 0, 0
	for i := 0; i < scaled(15000); i++ {
		a := genSide(r)
		b := genMutate(r, a)
		if r.IntN(2) == 0 {
			b.series = slices.Clone(a.series)
		}
		// A quarter of the draws: the book writes the series statement into its
		// title ("Book 2 of Batman: Hush") and the candidate into its subtitle,
		// where declaring the position turns that subtitle into a statement the
		// candidate's effective subtitle drops.
		stmtShape := r.IntN(4) == 0
		if stmtShape {
			a, b = genStatementPair(r)
		}
		// Half the draws: the candidate is the book's title minus its last
		// segment and the candidate's narrator is that segment, so the credit
		// form completes the title and the pair can move.
		completing := ""
		if len(a.segs) > 1 && r.IntN(2) == 0 {
			b = cloneSide(a)
			b.segs, b.sub = b.segs[:len(b.segs)-1], nil
			completing = strings.Join(a.segs[len(a.segs)-1], " ")
		}
		seed := r.Uint64()
		book := renderBook(rand.New(rand.NewPCG(seed, 1)), a)
		cand := renderCand(rand.New(rand.NewPCG(seed, 2)), b)
		plain := cand
		plain.Narrator, plain.Author, plain.SeriesPosition = "", "", ""
		with := cand
		with.Narrator = randomName(rand.New(rand.NewPCG(seed, 3)))
		if completing != "" {
			with.Narrator = completing
		}
		if stmtShape {
			with.SeriesPosition = b.pos
			statements++
		} else if len(b.series) > 0 {
			with.SeriesPosition = oracleNums[int(seed%3)]
		}
		before := outcomeClass(checkTitle(&book, &plain))
		after := outcomeClass(checkTitle(&book, &with))
		switch rank := map[string]int{"block": 0, "neutral": 1, "agree": 2}; {
		case before == after:
			same++
		case rank[after] > rank[before]:
			moved++
		default:
			t.Fatalf("candidate credit/position moved %s to %s: %s", before, after, describePair(book, with))
		}
		// the book's own narrator, author and position change nothing
		other := book
		other.Narrator, other.Author, other.SeriesPositionRaw = new(randomName(rand.New(rand.NewPCG(seed, 4)))), &database.Author{Name: "Alfred Pennyworth"}, new("2")
		if got := outcomeClass(checkTitle(&other, &plain)); got != before {
			t.Fatalf("the book's own credit or position changed %s to %s: %s", before, got, describePair(other, plain))
		}
	}
	if moved < scaled(50) || same < scaled(5000) || statements < scaled(2000) {
		t.Fatalf("the property was not exercised: %d moved toward agree, %d unchanged, %d statement pairs", moved, same, statements)
	}
	t.Logf("%d moved toward agree, %d unchanged, %d statement pairs", moved, same, statements)
}

// TestCheckTitle_SeriesAndPathsNeverChangeAConflict: for a pair whose TITLE
// fields conflict, no series name (taken from either side's own words) and no
// file name makes the pair anything but a block. This is the review's BLOCKER-1
// as a property: a series named after the other storyline. It says nothing
// about a credit: the narrator here is one fixed name that no pool title
// contains, so it can never be a segment of a title. A credit that IS in a title
// is TestCheckTitle_ACreditNeverMatchesASeries's, by example.
func TestCheckTitle_SeriesAndPathsNeverChangeAConflict(t *testing.T) {
	r := rand.New(rand.NewPCG(11, 4688))
	checked := 0
	for i := 0; i < scaled(40000) && checked < scaled(2500); i++ {
		book, cand := genSide(r), genSide(r)
		book.series, cand.series, book.path = nil, nil, nil
		if !oRead(book).ok || !oRead(cand).ok || !oConflict(oCanon(book), oCanon(cand)) {
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
	if checked < scaled(1000) {
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
	for i := 0; i < scaled(6000); i++ {
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
	if agreeing < scaled(300) {
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
		var got []string
		for _, f := range titleForms(c.title, c.sub, c.series) {
			got = append(got, f.text)
		}
		if !slices.Equal(got, c.want) {
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
