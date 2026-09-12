// file: internal/util/natural.go
// version: 1.0.0
// guid: 3b6a0a81-b5c4-4778-b467-f600e8e0d14e
// last-edited: 2026-09-12

package util

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// CompareNatural orders strings the way a person reads numbered file names:
// "Chapter 2" before "Chapter 10". It returns -1, 0 or +1.
//
// Runs of ASCII digits compare by numeric value; everything else compares rune
// by rune, case-insensitively. Digit runs are never parsed into an integer, so
// a run longer than int64 still compares correctly: leading zeros are trimmed
// and the shorter remaining run is the smaller number, with equal lengths
// compared digit by digit. Only ASCII 0-9 count as digits; other Unicode digits
// compare as ordinary runes.
//
// When the two strings are equal under those rules ("01" vs "1", "a" vs "A"),
// the plain byte-wise strings.Compare decides, so the order is total and
// deterministic and CompareNatural returns 0 only for identical strings.
func CompareNatural(a, b string) int {
	if c := compareNaturalFold(a, b); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

// NaturalLess reports whether a sorts before b under CompareNatural. It is
// shaped for sort.Slice / slices.SortFunc adapters.
func NaturalLess(a, b string) bool { return CompareNatural(a, b) < 0 }

func compareNaturalFold(a, b string) int {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if isASCIIDigit(a[i]) && isASCIIDigit(b[j]) {
			si, sj := i, j
			for i < len(a) && isASCIIDigit(a[i]) {
				i++
			}
			for j < len(b) && isASCIIDigit(b[j]) {
				j++
			}
			if c := compareDigitRuns(a[si:i], b[sj:j]); c != 0 {
				return c
			}
			continue
		}
		ra, na := utf8.DecodeRuneInString(a[i:])
		rb, nb := utf8.DecodeRuneInString(b[j:])
		if la, lb := unicode.ToLower(ra), unicode.ToLower(rb); la != lb {
			if la < lb {
				return -1
			}
			return 1
		}
		i += na
		j += nb
	}
	switch {
	case i < len(a):
		return 1
	case j < len(b):
		return -1
	}
	return 0
}

// compareDigitRuns compares two all-digit strings by numeric value without
// parsing them.
func compareDigitRuns(x, y string) int {
	x = strings.TrimLeft(x, "0")
	y = strings.TrimLeft(y, "0")
	if len(x) != len(y) {
		if len(x) < len(y) {
			return -1
		}
		return 1
	}
	return strings.Compare(x, y)
}

func isASCIIDigit(c byte) bool { return '0' <= c && c <= '9' }
