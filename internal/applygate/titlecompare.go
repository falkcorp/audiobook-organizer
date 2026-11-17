// file: internal/applygate/titlecompare.go
// version: 1.0.0
// guid: 8e41c7a2-5d09-4b3f-a6e8-2f9c0d71b5a3
// last-edited: 2026-11-17

package applygate

import (
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Title comparison for the evidence leg's title check.
//
// The check used to score agreement 1.0 when the candidate's main title (the
// part before ":") equalled ANY segment of the stored title. Two volumes of one
// storyline share that segment ("Hush: Part 1" and "Hush: Part 2", "Batman:
// Dark Victory" and "Batman: The Long Halloween"), so with the runtime inside
// 5% and the author agreeing the pair applied unattended. This file compares
// the WHOLE identifying title on both sides:
//
//   - words and numbers are kept apart. A number is a VALUE (digit, decimal,
//     number word, roman numeral) and is never dropped, so "Part I" is not
//     "Part II" and "Year One" is "Year 1";
//   - numbers carry a kind: a part / disc / track / chapter number (a position
//     in the files) or a series number (a book, a volume, a number inside the
//     title). A kind both sides give with different values is a CONFLICT;
//   - a segment shared by two titles confirms nothing while each title has an
//     unaccounted remainder of its own: that is a CONFLICT too.
//
// The comparison is symmetric in its two texts.

// titleClass is the verdict on one pair of titles, ordered by rank.
type titleClass int

const (
	// classConflictNumber: the same words but a number both sides give differs
	// ("Part 1" / "Part 2", "Year One" / "Year Two").
	classConflictNumber titleClass = iota
	// classConflictWords: a segment is shared and each title has a remainder
	// the other does not account for ("Batman: Dark Victory" /
	// "Batman: The Long Halloween").
	classConflictWords
	// classUnrelated: little in common, no structure to compare.
	classUnrelated
	// classNeutral: related, but one side says something the other cannot
	// confirm (a volume number only one side has, a prefix nobody accounts for).
	classNeutral
	// classAgree: the whole identifying titles match.
	classAgree
)

// titleCmp is one pair's verdict with a short reason for the check detail.
type titleCmp struct {
	class titleClass
	note  string
}

type numKind int

const (
	// numSeries is a book / volume number, or a number inside the title
	// ("Knightfall 2", "Year One").
	numSeries numKind = iota
	// numFile is a part / disc / track / chapter number: where the files sit
	// inside one work, not which work it is.
	numFile
)

type tnum struct {
	kind numKind
	val  float64
}

type segKind int

const (
	segWords  segKind = iota // carries identity words
	segMarker                // no words; numbers behind a marker word ("Part 2", "Book 3")
	segBare                  // no words; a bare number ("08", "001")
)

// tseg is one ":" / dash / bracket separated piece of a title.
type tseg struct {
	norm  string
	words map[string]bool
	nums  []tnum
	kind  segKind
}

var (
	// titleTokenRe keeps "1.5" whole and splits everything else on
	// non-alphanumerics.
	titleTokenRe = regexp.MustCompile(`\d+\.\d+|[\p{L}\p{N}]+`)
	// romanRe is I..XXXIX over the letters I, V, X only: "mix" and "civil" are
	// words, "vi" and "xii" are numerals.
	romanRe = regexp.MustCompile(`^(?:x{0,3})(?:ix|iv|v?i{0,3})$`)

	titleNumberWords = map[string]float64{
		"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8,
		"nine": 9, "ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13, "fourteen": 14,
		"fifteen": 15, "sixteen": 16, "seventeen": 17, "eighteen": 18, "nineteen": 19, "twenty": 20,
	}
	// fileMarkers number a position in the files of one work.
	fileMarkers = map[string]bool{
		"part": true, "pt": true, "disc": true, "disk": true, "cd": true,
		"track": true, "trk": true, "chapter": true, "ch": true,
	}
	// volumeMarkers number a book in a series.
	volumeMarkers = map[string]bool{
		"book": true, "bk": true, "volume": true, "vol": true, "no": true,
		"number": true, "episode": true, "season": true, "act": true,
	}
)

func isMarkerWord(s string) bool { return fileMarkers[s] || volumeMarkers[s] }

// titleTok is a token with its numeric reading.
type titleTok struct {
	s     string
	val   float64
	isNum bool
}

// parseTitle splits s into segments with their identity words and numbers.
// Blank segments (and ones that are only stop words, "Unabridged") are dropped.
func parseTitle(s string) []tseg {
	s = strings.ReplaceAll(s, "#", " no ")
	var out []tseg
	for _, part := range segSplitRe.Split(s, -1) {
		if seg, ok := parseSegment(part); ok {
			out = append(out, seg)
		}
	}
	return out
}

func parseSegment(part string) (tseg, bool) {
	n := normText(part)
	raw := titleTokenRe.FindAllString(n, -1)
	if len(raw) == 0 {
		return tseg{}, false
	}
	toks := make([]titleTok, len(raw))
	for i, s := range raw {
		toks[i].s = s
		switch {
		case isDecimalOrDigits(s):
			if v, err := strconv.ParseFloat(s, 64); err == nil {
				toks[i].val, toks[i].isNum = v, true
			}
		case titleNumberWords[s] > 0:
			toks[i].val, toks[i].isNum = titleNumberWords[s], true
		case len(s) >= 2 && romanRe.MatchString(s):
			toks[i].val, toks[i].isNum = romanValue(s), true
		}
	}
	// A single I, V or X is a numeral only behind a marker word ("Part I") or
	// when nothing but numbering follows it in a multi-token segment ("Detective Comics V",
	// "Gazette V Part 7"); elsewhere it is an initial and, like every one-letter
	// token, carries nothing.
	for i := range toks {
		s := toks[i].s
		if toks[i].isNum || (s != "i" && s != "v" && s != "x") {
			continue
		}
		if (i > 0 && isMarkerWord(toks[i-1].s)) || (len(toks) >= 2 && onlyNumberingFollows(toks[i+1:])) {
			toks[i].val, toks[i].isNum = romanValue(s), true
		}
	}
	seg := tseg{norm: n, words: map[string]bool{}}
	marker := false
	for i, t := range toks {
		if t.isNum {
			if i > 0 && toks[i-1].s == "of" {
				continue // "Part 1 of 2": the total is not an identity
			}
			k := numSeries
			if i > 0 && fileMarkers[toks[i-1].s] {
				k = numFile
			}
			if i > 0 && isMarkerWord(toks[i-1].s) {
				marker = true
			}
			seg.nums = append(seg.nums, tnum{k, t.val})
			continue
		}
		if isMarkerWord(t.s) && i+1 < len(toks) && toks[i+1].isNum {
			marker = true
			continue
		}
		if titleStop[t.s] || (len([]rune(t.s)) < 2 && !unicode.IsDigit([]rune(t.s)[0])) {
			continue
		}
		seg.words[t.s] = true
	}
	sort.Slice(seg.nums, func(i, j int) bool {
		if seg.nums[i].kind != seg.nums[j].kind {
			return seg.nums[i].kind < seg.nums[j].kind
		}
		return seg.nums[i].val < seg.nums[j].val
	})
	switch {
	case len(seg.words) > 0:
		seg.kind = segWords
	case len(seg.nums) > 0 && (marker || !listOrdinal(toks)):
		seg.kind = segMarker
	case len(seg.nums) > 0:
		seg.kind = segBare
	default:
		return tseg{}, false
	}
	return seg, true
}

// onlyNumberingFollows reports whether every token is a number, a marker word
// or a stop word ("of"): the tail of a title that numbers it.
func onlyNumberingFollows(toks []titleTok) bool {
	for _, t := range toks {
		if !t.isNum && !isMarkerWord(t.s) && !titleStop[t.s] {
			return false
		}
	}
	return true
}

// listOrdinal reports whether a numbers-only segment is a list or track number
// ("08", "001", "1.5"): at most three integer digits. A longer number ("1939")
// is a title or a year.
func listOrdinal(toks []titleTok) bool {
	for _, t := range toks {
		if !t.isNum {
			return false
		}
		if len(strings.SplitN(t.s, ".", 2)[0]) > 3 {
			return false
		}
	}
	return true
}

func isDecimalOrDigits(s string) bool {
	dot := false
	for i, r := range s {
		switch {
		case unicode.IsDigit(r):
		case r == '.' && !dot && i > 0:
			dot = true
		default:
			return false
		}
	}
	return s != ""
}

func romanValue(s string) float64 {
	vals := map[byte]int{'i': 1, 'v': 5, 'x': 10}
	total := 0
	for i := 0; i < len(s); i++ {
		v := vals[s[i]]
		if i+1 < len(s) && v < vals[s[i+1]] {
			total -= v
		} else {
			total += v
		}
	}
	return float64(total)
}

// numRel is how two number sets relate.
type numRel int

const (
	numsEqual numRel = iota
	numsOneSided
	numsConflict
)

// compareNums compares per kind, as multisets: "Two-Face: Year Two" has a 2 in
// its name and a 2 in its volume, "Two-Face: Year One" a 2 and a 1. A kind only
// one side gives is one-sided (the other side says nothing about it); a kind
// both give must be equal, or one a subset of the other ("Books 1-3" and "Book
// 1"), else it is a conflict.
func compareNums(a, b []tnum) numRel {
	rel := numsEqual
	for _, k := range []numKind{numSeries, numFile} {
		va, vb := kindVals(a, k), kindVals(b, k)
		switch {
		case len(va) == 0 && len(vb) == 0:
		case len(va) == 0 || len(vb) == 0:
			rel = numsOneSided
		case slices.Equal(va, vb):
		case subsetVals(va, vb) || subsetVals(vb, va):
			rel = numsOneSided
		default:
			return numsConflict
		}
	}
	return rel
}

func kindVals(ns []tnum, k numKind) []float64 {
	var out []float64
	for _, n := range ns {
		if n.kind == k {
			out = append(out, n.val)
		}
	}
	return out // ns is sorted
}

// subsetVals reports whether every value of a is in b, counting repeats.
func subsetVals(a, b []float64) bool {
	count := map[float64]int{}
	for _, x := range b {
		count[x]++
	}
	for _, x := range a {
		if count[x] == 0 {
			return false
		}
		count[x]--
	}
	return true
}

// wordSim is |A∩B| / max(|A|,|B|) over identity words: a subset is not a match.
func wordSim(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for w := range a {
		if b[w] {
			inter++
		}
	}
	return float64(inter) / float64(max(len(a), len(b)))
}

type matchLevel int

const (
	lvlNone matchLevel = iota
	lvlConflict
	lvlSoft  // same words, a number only one side has
	lvlExact // same words, same numbers
)

// compareParts compares two word sets with their numbers.
func compareParts(wa map[string]bool, na []tnum, wb map[string]bool, nb []tnum) matchLevel {
	rel := compareNums(na, nb)
	if len(wa) == 0 && len(wb) == 0 {
		// Numbers-only titles ("1939"): they need a kind of number in common,
		// else a part number and a year would be compared.
		if len(na) == 0 || len(nb) == 0 || !shareKind(na, nb) {
			return lvlNone
		}
		switch rel {
		case numsEqual:
			return lvlExact
		case numsConflict:
			return lvlConflict
		}
		return lvlSoft
	}
	ws := wordSim(wa, wb)
	switch {
	case ws >= 0.85:
		switch rel {
		case numsConflict:
			return lvlConflict
		case numsEqual:
			return lvlExact
		}
		return lvlSoft
	case ws >= 0.5 && rel == numsConflict:
		return lvlConflict
	}
	return lvlNone
}

func shareKind(a, b []tnum) bool {
	for _, k := range []numKind{numSeries, numFile} {
		if len(kindVals(a, k)) > 0 && len(kindVals(b, k)) > 0 {
			return true
		}
	}
	return false
}

// aggregate is the words and numbers of a whole title. A bare number segment
// ("08 - Title") is a list ordinal, not part of the title, unless it is all
// the title has ("1939").
func aggregate(segs []tseg) (map[string]bool, []tnum) {
	hasMore := false
	for _, s := range segs {
		if s.kind != segBare {
			hasMore = true
		}
	}
	words := map[string]bool{}
	var nums []tnum
	for _, s := range segs {
		if s.kind == segBare && hasMore {
			continue
		}
		for w := range s.words {
			words[w] = true
		}
		nums = append(nums, s.nums...)
	}
	sort.Slice(nums, func(i, j int) bool {
		if nums[i].kind != nums[j].kind {
			return nums[i].kind < nums[j].kind
		}
		return nums[i].val < nums[j].val
	})
	return words, nums
}

// titleCtx is what the title check knows that is not title text: the series
// names and the people credited on the book or the candidate. A segment that is
// one of those names the series or the credit, not this book.
type titleCtx struct {
	seriesNorm  map[string]bool
	seriesWords []map[string]bool
	names       map[string]bool
}

func newTitleCtx(seriesNames map[string]bool, credits ...string) titleCtx {
	x := titleCtx{seriesNorm: seriesNames, names: map[string]bool{}}
	for name := range seriesNames {
		var ws map[string]bool
		for _, seg := range parseTitle(name) {
			if ws == nil {
				ws = map[string]bool{}
			}
			for w := range seg.words {
				ws[w] = true
			}
		}
		if len(ws) > 0 {
			x.seriesWords = append(x.seriesWords, ws)
		}
	}
	for _, c := range credits {
		for w := range tokens(c, false) {
			x.names[w] = true
		}
	}
	return x
}

// accounted reports whether a segment is not part of the book's identity: a
// series name (with or without a volume number behind it), or a credited
// person.
func (x titleCtx) accounted(s tseg) bool {
	if s.kind != segWords {
		return false
	}
	if x.seriesNorm[s.norm] {
		return true
	}
	for _, ws := range x.seriesWords {
		if sameWordSet(s.words, ws) {
			return true
		}
	}
	for w := range s.words {
		if !x.names[w] {
			return false
		}
	}
	return true
}

func sameWordSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for w := range a {
		if !b[w] {
			return false
		}
	}
	return true
}

// segRel relates two segments.
func segRel(a, b tseg) matchLevel {
	if (a.kind == segWords) != (b.kind == segWords) {
		return lvlNone
	}
	if a.kind != segWords {
		// Numbers only: a part number and a bare list number are different
		// things, and so are a file position and a book number.
		if a.kind != b.kind || (a.nums[0].kind != b.nums[0].kind) {
			return lvlNone
		}
	}
	return compareParts(a.words, a.nums, b.words, b.nums)
}

// compareTitles classifies one pair of titles. a and b are the raw texts; the
// result is the same with the two swapped.
func compareTitles(a, b string, x titleCtx) titleCmp {
	sa, sb := parseTitle(a), parseTitle(b)
	if len(sa) == 0 || len(sb) == 0 {
		return titleCmp{class: classUnrelated}
	}
	wa, na := aggregate(sa)
	wb, nb := aggregate(sb)
	lvl := compareParts(wa, na, wb, nb)
	switch lvl {
	case lvlExact:
		return titleCmp{class: classAgree}
	case lvlConflict:
		return titleCmp{class: classConflictNumber, note: "same words, different number (" + numsText(na) + " vs " + numsText(nb) + ")"}
	}
	if len(sa) > 1 || len(sb) > 1 {
		if r, ok := compareSegments(sa, sb, x); ok {
			return r
		}
	}
	if lvl == lvlSoft {
		return titleCmp{class: classNeutral, note: "a number only one side has"}
	}
	if titleSim(a, b) < 0.5 {
		return titleCmp{class: classUnrelated}
	}
	return titleCmp{class: classNeutral}
}

// compareSegments is the segment-level comparison. ok is false when the titles
// share no segment, so there is no structure to judge.
func compareSegments(a, b []tseg, x titleCtx) (titleCmp, bool) {
	matchedA, matchedB := make([]bool, len(a)), make([]bool, len(b))
	conflictA, conflictB := make([]bool, len(a)), make([]bool, len(b))
	shared, soft, identity := false, false, false
	sharedText := ""
	for i, sa := range a {
		for j, sb := range b {
			rel := segRel(sa, sb)
			switch rel {
			case lvlExact, lvlSoft:
				matchedA[i], matchedB[j] = true, true
				if sa.kind != segWords {
					break // a shared list number or part number shares no title
				}
				shared = true
				if rel == lvlSoft {
					soft = true
				}
				if !x.accounted(sa) {
					identity = true
				}
				if sharedText == "" {
					sharedText = sa.norm
				}
			case lvlConflict:
				conflictA[i], conflictB[j] = true, true
			}
		}
	}
	if !shared {
		return titleCmp{}, false
	}
	// Remainders: unmatched segments that are not accounted for. A lone file
	// marker or list number ("Part 2", "08") says where the files sit, and is
	// set aside unless the other side has a conflicting one.
	remainder := func(segs []tseg, matched, conflict []bool) (rest []int, last int) {
		last = -1
		for i := range segs {
			if matched[i] {
				last = i
			}
		}
		for i, s := range segs {
			switch {
			case matched[i], x.accounted(s):
			case s.kind == segBare && !conflict[i]:
			case s.kind == segMarker && s.nums[0].kind == numFile && !conflict[i]:
			default:
				rest = append(rest, i)
			}
		}
		return rest, last
	}
	restA, lastA := remainder(a, matchedA, conflictA)
	restB, lastB := remainder(b, matchedB, conflictB)
	if len(restA) > 0 && len(restB) > 0 {
		return titleCmp{class: classConflictWords, note: "both titles share " + strconv.Quote(sharedText) +
			" but each continues with something the other does not: " + strconv.Quote(a[restA[0]].norm) + " vs " + strconv.Quote(b[restB[0]].norm)}, true
	}
	if !identity {
		return titleCmp{class: classNeutral, note: "only a series or credit name is shared"}, true
	}
	capped := soft
	check := func(segs []tseg, rest []int, last int) {
		for _, i := range rest {
			// A trailing subtitle of words ("The Killing Joke: A Gotham
			// Story") is extra detail. A prefix nobody accounts for, or a
			// volume number only one side has, is not confirmation.
			if i < last || len(segs[i].nums) > 0 || segs[i].kind != segWords {
				capped = true
			}
		}
	}
	check(a, restA, lastA)
	check(b, restB, lastB)
	if capped {
		return titleCmp{class: classNeutral, note: "one title has a prefix or number the other does not"}, true
	}
	return titleCmp{class: classAgree}, true
}

func numsText(ns []tnum) string {
	var parts []string
	for _, n := range ns {
		p := ""
		if n.kind == numFile {
			p = "part "
		}
		parts = append(parts, p+strconv.FormatFloat(n.val, 'f', -1, 64))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ",")
}
