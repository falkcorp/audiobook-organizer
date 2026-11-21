// file: internal/applygate/titlecompare_scan_test.go
// version: 1.3.0
// guid: 0c4e8a71-d395-4b26-9f17-6a8b3e5d2c40
// last-edited: 2026-11-18

package applygate

import (
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// The title tokenizer used to be three regular expressions and a strings.Replacer
// built on every call. They are scanners now (titlecompare.go): a title check made
// hundreds of matches, and under -race the regexp matcher's pooled state is
// allocated again for many of them. These tests keep the old expressions as the reference and
// hold every replacement to them: exhaustively over short strings, and at random
// over strings with the characters the expressions treat specially.

var (
	refTitleTokenRe   = regexp.MustCompile(`\d+\.\d+|[\p{L}\p{N}]+`)
	refRomanRe        = regexp.MustCompile(`^(?:x{0,3})(?:ix|iv|v?i{0,3})$`)
	refOrdinalDigitRe = regexp.MustCompile(`^(\d+)(?:st|nd|rd|th)$`)
	// segSplitRe is the expression splitSegments replaced (it was segSplitRe in
	// evidence.go, behind titleforms, titlecompare and booknum).
	segSplitRe         = regexp.MustCompile(`\s*(?::|\s-\s|\s–\s|\s—\s|[(\[\])])\s*`)
	refSegNormReplacer = func(s string) string {
		return strings.NewReplacer("&", " and ", "#", " ", "'", "", "’", "").Replace(s)
	}
)

// refFold is segTokens' character pass as it was written before the scanners.
func refFold(s string) string {
	s = strings.ToLower(norm.NFKC.String(s))
	s = refSegNormReplacer(s)
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// exhaustiveLen is the longest string an exhaustive walk visits: one shorter under
// -race, where every extra length multiplies the walk by the alphabet's size and
// the walk is the most of these tests' time. The random draws that follow it carry
// the longer strings (scaled).
func exhaustiveLen(full int) int {
	if raceEnabled {
		return full - 1
	}
	return full
}

// everyString calls f with every string of length 0..maxLen over alphabet.
func everyString(alphabet []string, maxLen int, f func(string)) {
	var walk func(prefix string, left int)
	walk = func(prefix string, left int) {
		f(prefix)
		if left == 0 {
			return
		}
		for _, a := range alphabet {
			walk(prefix+a, left-1)
		}
	}
	walk("", maxLen)
}

// scanPieces are the fragments the random strings are built from: what the
// expressions and the tokenizer treat specially, in the spellings a title has.
var scanPieces = []string{
	"a", "b", "z", "x", "v", "i", "l", "c", "I", "V", "X", "xx", "xxx", "xxxx", "ix", "iv", "vi", "vii", "viii", "mix", "civil",
	"0", "1", "2", "7", "10", "22", "199", "2033", "1.5", "2.", ".5", ".", "..", "3.14.15", "1,5",
	" ", "  ", "-", " - ", ":", ": ", "(", ")", "[", "]", "&", "#", "'", "’", "_", "/",
	"st", "nd", "rd", "th", "1st", "2nd", "3rd", "4th", "22nd", "0th", "first", "second", "third", "twelfth",
	"one", "two", "nine", "ten", "twelve", "nineteen", "twenty", "thirty", "ninety", "twenty-one",
	"the", "a", "an", "of", "and", "book", "part", "vol", "volume", "chapter", "no", "number", "abridged",
	"unabridged", "audiobook", "novel", "a novel", "the audiobook", "unknown author", "read by narrator",
	"é", "ë", "ñ", "Noël", "́", "̈", "İ", "ǅ", "ß", "Ⅱ", "Ⅲ", "ⅷ", "²", "½", "٣", "१२", "ª", "日本", "Ωmega", " ", "–", "—",
	"\xff", "\xc3", "\xe2\x80", "\x00", "\n", "\t",
}

func randomScanText(r *rand.Rand, pieces int) string {
	var b strings.Builder
	for n := r.IntN(pieces + 1); n > 0; n-- {
		b.WriteString(scanPieces[r.IntN(len(scanPieces))])
	}
	return b.String()
}

func TestTitleTokens_MatchTheExpression(t *testing.T) {
	check := func(s string) {
		t.Helper()
		got := titleTokens(s)
		want := refTitleTokenRe.FindAllString(s, -1)
		if !slices.Equal(got, want) {
			t.Fatalf("titleTokens(%q) = %q, the expression finds %q", s, got, want)
		}
	}
	everyString([]string{"a", "1", ".", " ", "é", "\xff", "٣", "-"}, exhaustiveLen(6), check)
	r := rand.New(rand.NewPCG(7, 11))
	for i := 0; i < scaled(60000); i++ {
		check(randomScanText(r, 8))
	}
}

func TestIsRomanNumeral_MatchesTheExpression(t *testing.T) {
	everyString([]string{"x", "v", "i", "l", "a", "1"}, exhaustiveLen(8), func(s string) {
		if got, want := isRomanNumeral(s), refRomanRe.MatchString(s); got != want {
			t.Fatalf("isRomanNumeral(%q) = %v, the expression says %v", s, got, want)
		}
	})
}

func TestOrdinalDigits_MatchTheExpression(t *testing.T) {
	check := func(s string) {
		t.Helper()
		m := refOrdinalDigitRe.FindStringSubmatch(s)
		if got := isOrdinalDigits(s); got != (m != nil) {
			t.Fatalf("isOrdinalDigits(%q) = %v, the expression says %v", s, got, m != nil)
		}
		if m != nil {
			if got := ordinalDigitValue(s); got != m[1] {
				t.Fatalf("ordinalDigitValue(%q) = %q, the expression captures %q", s, got, m[1])
			}
		}
	}
	everyString([]string{"1", "2", "s", "t", "n", "d", "r", "h", "a", "٣"}, 5, check)
	for _, s := range []string{"1st", "22nd", "3rd", "4th", "0th", "007th", "1th", "st", "1st ", " 1st", "1st\n", "1ST", "12345678901234567890th"} {
		check(s)
	}
}

// TestFoldTitleText_MatchesTheTwoPassForm holds the ASCII shortcut to the full
// NFKC, replace, NFD, drop-marks pass it skips.
func TestFoldTitleText_MatchesTheTwoPassForm(t *testing.T) {
	check := func(s string) {
		t.Helper()
		if got, want := foldTitleText(s), refFold(s); got != want {
			t.Fatalf("foldTitleText(%q) = %q, the full pass gives %q", s, got, want)
		}
	}
	// Every ASCII byte alone and in pairs: the shortcut is only taken for ASCII.
	for a := range 128 {
		check(string(rune(a)))
		for b := range 128 {
			check(string([]rune{rune(a), rune(b)}))
		}
	}
	r := rand.New(rand.NewPCG(3, 5))
	for i := 0; i < scaled(30000); i++ {
		check(randomScanText(r, 8))
	}
}

func TestSplitSegments_MatchesTheExpression(t *testing.T) {
	check := func(s string) {
		t.Helper()
		got, want := splitSegments(s), segSplitRe.Split(s, -1)
		if !slices.Equal(got, want) {
			t.Fatalf("splitSegments(%q) = %q, the expression gives %q", s, got, want)
		}
	}
	everyString([]string{"a", " ", "\t", "-", ":", "(", "\u2013", "\u00a0", "\v"}, exhaustiveLen(6), check)
	r := rand.New(rand.NewPCG(23, 29))
	pieces := []string{"a", "hush", "Batman", " ", "  ", "\t", "\n", "\v", "\f", "\r", "\u00a0", "\u2003", "-", "--", " - ", "–", " – ", "—", " — ", "-a", "a-", ":", ": ", " :", "(", ")", "[", "]", "()", " (", ") ", "\xff", "\xe2\x80", "é", "1", "2"}
	for i := 0; i < scaled(80000); i++ {
		var b strings.Builder
		for n := r.IntN(9); n > 0; n-- {
			b.WriteString(pieces[r.IntN(len(pieces))])
		}
		check(b.String())
	}
	for _, s := range []string{"", " ", ":", " : ", "a:", ":a", "a: ", " :a", "a - b", "a  -  b", "a -b", "a- b", "a – b", "a — b", "a-b", "a (b) c", "a [b]", "a ( b ) c", "x - ", " - x", "-", " -", "- "} {
		check(s)
	}
}
