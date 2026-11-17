// file: internal/seqnum/range.go
// version: 1.0.0
// guid: 3e8a1d57-4c20-4b9f-a6d3-91f5b0c7e248
// last-edited: 2026-11-16

package seqnum

import (
	"regexp"
	"strconv"
	"strings"
)

// Range syntax. The keyword is what makes a span in free text a span: "Books
// 1-3" is a collected edition, "Catch-22" and "9-1-1" are names.
//
//	keyword: book(s), bk(s), volume(s), vol(s) with an optional dot, or "#"
//	span:    N - M, N – M, N — M (no spaces needed), or N to|through|thru|and M
//	         (spaces needed), the second end may repeat the keyword
//	list:    N, M, ... & P / N and M / N + M
var (
	rangeKeyword = `(?:\b(?:books?|bks?|volumes?|vols?)\.?|#)\s*#?\s*`
	reRangeText  = regexp.MustCompile(rangeKeyword + numAlt +
		`(?:\s*[-–—]\s*|\s+(?:to|through|thru)\s+)(?:(?:books?|bks?|volumes?|vols?)\.?\s*|#\s*)?` + numAlt + `\b`)
	reListText = regexp.MustCompile(rangeKeyword + `(` + numAlt + `(?:\s*(?:,|&|\+|\band\b)\s*` + numAlt + `)+)\b`)
	reListItem = regexp.MustCompile(numAlt)

	// A position FIELD is only a position, so its span needs no keyword:
	// "1-3", "1 – 3", "1 to 3", "1.5-2", "1 & 2", "1, 2, 3".
	posNum       = `\d+(?:\.\d+)?`
	rePosSpan    = regexp.MustCompile(`^(` + posNum + `)(?:\s*[-–—]\s*|\s+(?:to|through|thru)\s+)(` + posNum + `)$`)
	rePosList    = regexp.MustCompile(`^` + posNum + `(?:\s*(?:,|&|\+|\band\b)\s*` + posNum + `)+$`)
	rePosListNum = regexp.MustCompile(posNum)
)

// ParseRange reads a collected edition's range out of free text (a title, a
// subtitle, a file or folder name): "Books 1-3", "Vols 1–3", "Book 1 to 3",
// "Books 1 & 2", "Books 1, 2 & 3". It returns false for text with no keyworded
// range, for a reversed or one-volume span ("Books 3-1", "Books 2-2"), and for
// a bare "1-3" (see the package doc).
//
// A list that skips a volume ("Books 1, 3") is a Number too, with the ends in
// Value and Last and Text "1,3": it equals only that same list, never "1-3".
func ParseRange(s string) (Number, bool) {
	c := clean(s)
	if c == "" {
		return Number{}, false
	}
	if m := reRangeText.FindStringSubmatch(c); m != nil {
		a, aok := toNumber(m[1], "range")
		b, bok := toNumber(m[2], "range")
		if aok && bok {
			return spanNumber(a.Value, b.Value)
		}
	}
	if m := reListText.FindStringSubmatch(c); m != nil {
		var vals []float64
		for _, tok := range reListItem.FindAllString(m[1], -1) {
			n, ok := toNumber(tok, "range")
			if !ok {
				return Number{}, false
			}
			vals = append(vals, n.Value)
		}
		return listNumber(vals)
	}
	return Number{}, false
}

// parsePositionRange reads a bare span or list that is the whole position
// field: "1-3", "1 to 3", "1 & 2", "1, 2, 3".
func parsePositionRange(t string) (Number, bool) {
	if m := rePosSpan.FindStringSubmatch(t); m != nil {
		a, aok := toNumber(m[1], "position")
		b, bok := toNumber(m[2], "position")
		if aok && bok {
			return spanNumber(a.Value, b.Value)
		}
		return Number{}, false
	}
	if rePosList.MatchString(t) {
		var vals []float64
		for _, tok := range rePosListNum.FindAllString(t, -1) {
			n, ok := toNumber(tok, "position")
			if !ok {
				return Number{}, false
			}
			vals = append(vals, n.Value)
		}
		n, ok := listNumber(vals)
		if ok {
			n.Form = "position"
		}
		return n, ok
	}
	return Number{}, false
}

// spanNumber is the range first..last. A reversed or one-volume span is not a
// range ("3-1", "2-2"), so it never reads as one.
func spanNumber(first, last float64) (Number, bool) {
	if last <= first {
		return Number{}, false
	}
	return Number{Value: first, Last: last, Text: ftoa(first) + "-" + ftoa(last), Form: "range"}, true
}

// listNumber is a list of volumes: a range when each step is exactly one
// volume, otherwise a gappy list that equals only itself. Out-of-order lists
// are not read.
func listNumber(vals []float64) (Number, bool) {
	if len(vals) < 2 {
		return Number{}, false
	}
	contiguous := true
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = ftoa(v)
		if i == 0 {
			continue
		}
		if v <= vals[i-1] {
			return Number{}, false
		}
		if v-vals[i-1] != 1 {
			contiguous = false
		}
	}
	first, last := vals[0], vals[len(vals)-1]
	if contiguous {
		return Number{Value: first, Last: last, Text: ftoa(first) + "-" + ftoa(last), Form: "range"}, true
	}
	return Number{Value: first, Last: last, Text: strings.Join(parts, ","), Form: "range"}, true
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
