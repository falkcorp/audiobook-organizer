// file: internal/seqnum/range.go
// version: 1.2.0
// guid: 3e8a1d57-4c20-4b9f-a6d3-91f5b0c7e248
// last-edited: 2026-11-17

package seqnum

import (
	"regexp"
	"strconv"
	"strings"
)

// Range syntax. The keyword is what makes a span in free text a span: "Books
// 1-3" is a collected edition, "Catch-22"-style hyphenated names are not.
//
//	keyword: book(s), bk(s), volume(s), vol(s) with an optional dot, or "#"
//	digits:  N-M, N–M, N—M (a tight dash), N to|through|thru M, and, with a
//	         PLURAL keyword only, N - M with spaces
//	words:   a roman numeral or number word as the SECOND end ("Books I-III",
//	         "Books one to three") only when nothing but a terminator follows
//	         it: "Book 2 - Three Jokers", "Book 2 - Ten Nights of the Beast"
//	         and "Book 1 and Two-Face" are single volumes, because the second
//	         "number" is the first word of the title
//	list:    N, M, ... & P / N and M / N + M, with the same word rule
//	tail:    a range followed by "& N", ", N" or "and N" is a LIST ("Books 1-3
//	         & 5" is the volumes 1, 2, 3 and 5), never equal to the bare range
//
// A terminator is the end of the text, a closing bracket, a colon, a semicolon,
// a comma, an opening bracket, or a spaced dash. A tight dash is not one:
// "Two-Face" continues the word.
const (
	kwWord   = `(?:books?|bks?|volumes?|vols?)`
	kwPlural = `(?:books|bks|volumes|vols)`
	digitsN  = `\d+(?:\.\d+)?`
	wordNum  = `(?:one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty|[ivxlc]+)`
	anyNum   = `(?:` + digitsN + `|` + wordNum + `)`
	sepList  = `(?:\s*(?:,|&|\+)\s*|\s+and\s+)`
	terminal = `(?:\s*$|\s*[)\]:;,(]|\s+[-–—]\s)`
	connTo   = `\s+(?:to|through|thru)\s+`
)

var (
	rangeKeyword   = `(?:\b` + kwWord + `\.?|#)\s*#?\s*`
	rangeKeywordPl = `\b` + kwPlural + `\.?\s*#?\s*`
	secondKw       = `(?:` + kwWord + `\.?\s*|#\s*)?`

	// Spans, tried in this order of generality; the earliest match wins.
	reSpanDigits = regexp.MustCompile(rangeKeyword + `(` + anyNum + `)(?:[-–—]|` + connTo + `)` + secondKw + `(` + digitsN + `)\b`)
	reSpanSpaced = regexp.MustCompile(rangeKeywordPl + `(` + anyNum + `)\s+[-–—]\s+` + secondKw + `(` + digitsN + `)\b`)
	reSpanWords  = regexp.MustCompile(rangeKeyword + `(` + anyNum + `)(?:[-–—]|` + connTo + `)` + secondKw + `(` + wordNum + `)` + terminal)

	// A span's continuation: ", 5", "& 5", "and 4-6".
	reSpanTail = regexp.MustCompile(`^` + sepList + `(` + digitsN + `)(?:\s*[-–—]\s*(` + digitsN + `))?\b`)

	reListDigits = regexp.MustCompile(rangeKeyword + `(` + digitsN + `(?:` + sepList + digitsN + `)+)` + terminal)
	reListWords  = regexp.MustCompile(rangeKeyword + `(` + anyNum + `(?:` + sepList + anyNum + `)+)` + terminal)
	reListItem   = regexp.MustCompile(anyNum)

	// A position FIELD is only a position, so its span needs no keyword:
	// "1-3", "1 – 3", "1 to 3", "1.5-2", "1 & 2", "1, 2, 3", "1-3 & 5".
	posNum    = digitsN
	posSeg    = posNum + `(?:\s*[-–—]\s*` + posNum + `)?`
	rePosAll  = regexp.MustCompile(`^` + posSeg + `(?:` + sepList + posSeg + `)*$`)
	rePosTo   = regexp.MustCompile(`^(` + posNum + `)` + connTo + `(` + posNum + `)$`)
	rePosSegs = regexp.MustCompile(`(` + posNum + `)(?:\s*[-–—]\s*(` + posNum + `))?`)
)

// seg is one run of volumes in a span or list: lo..hi, lo == hi for a single.
type seg struct{ lo, hi float64 }

// ParseRange reads a collected edition's range out of free text (a title, a
// subtitle, a file or folder name): "Books 1-3", "Vols 1–3", "Book 1 to 3",
// "Books 1 & 2", "Books 1, 2 & 3", "Books 1-3 & 5". It returns false for text
// with no keyworded range, for a reversed or one-volume span ("Books 3-1",
// "Books 2-2"), for a second end that is the start of a title ("Book 2 -
// Three Jokers", "Book 1 and Two-Face"), and for a bare "1-3" (see the
// package doc).
//
// A list that skips a volume ("Books 1, 3", "Books 1-3 & 5") is a Number too,
// with the ends in Value and Last and a Text that names its members ("1,3",
// "1-3,5"): it equals only that same list, never the bare "1-3".
func ParseRange(s string) (Number, bool) {
	c := clean(s)
	if c == "" {
		return Number{}, false
	}
	best := -1
	var segs []seg
	try := func(loc []int, parts []string, tail bool) {
		if loc == nil || (best >= 0 && loc[0] >= best) {
			return
		}
		a, aok := toNumber(parts[0], "range")
		b, bok := toNumber(parts[1], "range")
		if !aok || !bok || b.Value <= a.Value {
			return
		}
		best = loc[0]
		segs = []seg{{a.Value, b.Value}}
		if tail {
			segs = appendTail(segs, c[loc[1]:])
		}
	}
	for _, re := range []*regexp.Regexp{reSpanDigits, reSpanSpaced} {
		if loc := re.FindStringSubmatchIndex(c); loc != nil {
			try(loc[:2], []string{c[loc[2]:loc[3]], c[loc[4]:loc[5]]}, true)
		}
	}
	if loc := reSpanWords.FindStringSubmatchIndex(c); loc != nil {
		try(loc[:2], []string{c[loc[2]:loc[3]], c[loc[4]:loc[5]]}, false)
	}
	if best >= 0 {
		return segmentsNumber(segs)
	}
	for _, re := range []*regexp.Regexp{reListDigits, reListWords} {
		m := re.FindStringSubmatch(c)
		if m == nil {
			continue
		}
		var vals []seg
		for _, tok := range reListItem.FindAllString(m[1], -1) {
			n, ok := toNumber(tok, "range")
			if !ok {
				return Number{}, false
			}
			vals = append(vals, seg{n.Value, n.Value})
		}
		return segmentsNumber(vals)
	}
	return Number{}, false
}

// appendTail adds the "& 5" / ", 7-9" continuations that follow a span.
func appendTail(segs []seg, rest string) []seg {
	for {
		m := reSpanTail.FindStringSubmatchIndex(rest)
		if m == nil {
			return segs
		}
		a, aok := toNumber(rest[m[2]:m[3]], "range")
		if !aok {
			return segs
		}
		sg := seg{a.Value, a.Value}
		if m[4] >= 0 {
			b, bok := toNumber(rest[m[4]:m[5]], "range")
			if !bok {
				return segs
			}
			sg.hi = b.Value
		}
		segs = append(segs, sg)
		rest = rest[m[1]:]
	}
}

// parsePositionRange reads a bare span or list that is the whole position
// field: "1-3", "1 to 3", "1 & 2", "1, 2, 3", "1-3 & 5".
func parsePositionRange(t string) (Number, bool) {
	var segs []seg
	switch {
	case rePosTo.MatchString(t):
		m := rePosTo.FindStringSubmatch(t)
		a, aok := toNumber(m[1], "position")
		b, bok := toNumber(m[2], "position")
		if !aok || !bok {
			return Number{}, false
		}
		segs = []seg{{a.Value, b.Value}}
	case rePosAll.MatchString(t):
		for _, m := range rePosSegs.FindAllStringSubmatch(t, -1) {
			a, aok := toNumber(m[1], "position")
			if !aok {
				return Number{}, false
			}
			sg := seg{a.Value, a.Value}
			if m[2] != "" {
				b, bok := toNumber(m[2], "position")
				if !bok {
					return Number{}, false
				}
				sg.hi = b.Value
			}
			segs = append(segs, sg)
		}
	default:
		return Number{}, false
	}
	n, ok := segmentsNumber(segs)
	if ok {
		n.Form = "position"
	}
	return n, ok
}

// segmentsNumber is the range the segments cover. A single segment is a range
// only when it is a real span (lo < hi: "3-1" and "2-2" are not). Several
// segments must be in strictly increasing order; when every step is exactly
// one volume they are the contiguous range ("1, 2, 3" and "1-3 & 4" are
// "1-3"), otherwise a gappy list that equals only itself, its Text naming
// the members ("1,3", "1-3,5").
func segmentsNumber(segs []seg) (Number, bool) {
	if len(segs) == 0 {
		return Number{}, false
	}
	if len(segs) == 1 {
		return spanNumber(segs[0].lo, segs[0].hi)
	}
	contiguous := true
	parts := make([]string, len(segs))
	for i, sg := range segs {
		if sg.hi < sg.lo {
			return Number{}, false
		}
		parts[i] = ftoa(sg.lo)
		if sg.hi > sg.lo {
			parts[i] += "-" + ftoa(sg.hi)
		}
		if i == 0 {
			continue
		}
		if sg.lo <= segs[i-1].hi {
			return Number{}, false
		}
		if sg.lo-segs[i-1].hi != 1 {
			contiguous = false
		}
	}
	first, last := segs[0].lo, segs[len(segs)-1].hi
	if contiguous {
		return Number{Value: first, Last: last, Text: ftoa(first) + "-" + ftoa(last), Form: "range"}, true
	}
	return Number{Value: first, Last: last, Text: strings.Join(parts, ","), Form: "range"}, true
}

// spanNumber is the range first..last. A reversed or one-volume span is not a
// range ("3-1", "2-2"), so it never reads as one.
func spanNumber(first, last float64) (Number, bool) {
	if last <= first {
		return Number{}, false
	}
	return Number{Value: first, Last: last, Text: ftoa(first) + "-" + ftoa(last), Form: "range"}, true
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
