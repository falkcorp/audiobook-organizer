// file: internal/transcribe/numbers.go
// version: 1.0.0
// guid: 3b8e5c17-92d4-4a60-b1f3-6e07d2a94c58
// last-edited: 2026-11-17

package transcribe

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// This file is the number grammar of a structural announcement ("Chapter 12",
// "Part Two", "Book IV", "Chapter the Twenty-First"). It exists so the chapter
// marker, the part marker and the "Books 1 through 3" collection wording all
// read numbers the same way: before it, each regex carried its own copy of a
// fifteen-word list and none of them knew roman numerals, compound numbers
// ("twenty one") or ordinals.
//
// The grammar is ANCHORED to a keyword by its callers. A bare "I", "one" or "12"
// is never a number here; scanNumber is only ever asked about the text right
// after a "chapter"/"part"/"book" word. That is what keeps "I wanted", "one of
// the" and a year in running prose from becoming chapter numbers.

var (
	// numUnits are the cardinals zero..nineteen.
	numUnits = map[string]int{
		"zero": 0, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6,
		"seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12,
		"thirteen": 13, "fourteen": 14, "fifteen": 15, "sixteen": 16,
		"seventeen": 17, "eighteen": 18, "nineteen": 19,
	}
	// numTens are the cardinal tens twenty..ninety.
	numTens = map[string]int{
		"twenty": 20, "thirty": 30, "forty": 40, "fifty": 50,
		"sixty": 60, "seventy": 70, "eighty": 80, "ninety": 90,
	}
	// ordUnits are the ordinals first..nineteenth.
	ordUnits = map[string]int{
		"first": 1, "second": 2, "third": 3, "fourth": 4, "fifth": 5, "sixth": 6,
		"seventh": 7, "eighth": 8, "ninth": 9, "tenth": 10, "eleventh": 11,
		"twelfth": 12, "thirteenth": 13, "fourteenth": 14, "fifteenth": 15,
		"sixteenth": 16, "seventeenth": 17, "eighteenth": 18, "nineteenth": 19,
	}
	// ordTens are the ordinal tens twentieth..ninetieth.
	ordTens = map[string]int{
		"twentieth": 20, "thirtieth": 30, "fortieth": 40, "fiftieth": 50,
		"sixtieth": 60, "seventieth": 70, "eightieth": 80, "ninetieth": 90,
	}

	numDigitsRe = regexp.MustCompile(`^[0-9]+`)
	numThe      = regexp.MustCompile(`(?i)^the\s+`)
	numWordRe   = regexp.MustCompile(`^[A-Za-z]+`)
	numSepRe    = regexp.MustCompile(`^[\s-]+`)
	// romanRe is a canonical roman numeral up to 199. Non-canonical runs ("IIII",
	// "VX") are not numerals, which also rejects most all-caps words.
	romanRe = regexp.MustCompile(`^C?(?:XC|XL|L?X{0,3})(?:IX|IV|V?I{0,3})$`)
)

// maxDigitChapter caps a digit chapter number at three digits. A four-digit
// figure after "Part" or "Book" is a year ("Book 1939"), not a position.
const maxDigitChapter = 3

// scanNumber reads one structural number from the START of s (the text right
// after the keyword and its whitespace) and returns the value, how many bytes
// it used, and whether there was one.
//
// Forms, in the order they are tried:
//
//   - digits, one to three of them, not glued to a following letter: "12", "0"
//   - an UPPERCASE canonical roman numeral that ends the clause: "IV" followed
//     by end of text, punctuation or a Capitalised/digit word. "Book I wanted"
//     is a pronoun, so a numeral followed by a lowercase word is refused.
//   - spelled-out cardinals with hyphen/space compounds and "hundred": "twelve",
//     "twenty one", "twenty-one", "one hundred and five"
//   - after "the", an ordinal: "the first", "the twenty-first". An ordinal
//     without "the" is not accepted ("Chapter first" is not announced that way)
//     and "the" before a cardinal is refused ("Chapter the one").
func scanNumber(s string) (n int, used int, ok bool) {
	if the := numThe.FindString(s); the != "" {
		n, used, ok = scanSpelled(s[len(the):], true)
		if !ok {
			return 0, 0, false
		}
		return n, len(the) + used, true
	}
	if d := numDigitsRe.FindString(s); d != "" {
		if len(d) > maxDigitChapter {
			return 0, 0, false
		}
		// Glued to a letter ("12th", "3rd") is not the bare announcement form.
		if len(s) > len(d) && unicode.IsLetter(rune(s[len(d)])) {
			return 0, 0, false
		}
		v, err := strconv.Atoi(d)
		if err != nil {
			return 0, 0, false
		}
		return v, len(d), true
	}
	if v, u, ok := scanRoman(s); ok {
		return v, u, true
	}
	return scanSpelled(s, false)
}

// scanRoman reads an uppercase roman numeral that ends its clause.
func scanRoman(s string) (int, int, bool) {
	end := 0
	for end < len(s) && strings.IndexByte("IVXLC", s[end]) >= 0 {
		end++
	}
	if end == 0 || !romanRe.MatchString(s[:end]) {
		return 0, 0, false
	}
	// A numeral glued to more letters is a word ("Chapter Vivid").
	if end < len(s) && unicode.IsLetter(rune(s[end])) {
		return 0, 0, false
	}
	if !romanTerminated(s[end:]) {
		return 0, 0, false
	}
	return romanValue(s[:end]), end, true
}

// romanTerminated reports whether what follows a numeral ends the clause: end
// of text, sentence punctuation, or whitespace then an uppercase letter or
// digit. A lowercase continuation means the "numeral" was a pronoun or a letter.
func romanTerminated(after string) bool {
	if after == "" {
		return true
	}
	switch after[0] {
	case '.', ',', ':', ';', '!', '?', ')', '-':
		return true
	}
	rest := strings.TrimLeft(after, " \t")
	if rest == after || rest == "" {
		return rest == ""
	}
	r := rune(rest[0])
	return unicode.IsUpper(r) || unicode.IsDigit(r)
}

func romanValue(s string) int {
	vals := map[byte]int{'I': 1, 'V': 5, 'X': 10, 'L': 50, 'C': 100}
	total := 0
	for i := 0; i < len(s); i++ {
		v := vals[s[i]]
		if i+1 < len(s) && vals[s[i+1]] > v {
			total -= v
		} else {
			total += v
		}
	}
	return total
}

// numPhase is where the spelled-number state machine stands.
type numPhase int

const (
	phaseStart   numPhase = iota // nothing read
	phaseUnit                    // a unit 1..9 was read, "hundred" or the end may follow
	phaseTeen                    // 10..19, only the end may follow
	phaseTens                    // 20..90, a unit 1..9 or the end may follow
	phaseHundred                 // "hundred" read, "and"/tens/units may follow
	phaseDone                    // terminal (zero, a unit after tens, an ordinal)
)

// scanSpelled reads a spelled-out number. With allowOrdinal it reads an ordinal
// ("first", "twenty first") and refuses a number that does not END on an
// ordinal word; without it, it reads cardinals only.
func scanSpelled(s string, allowOrdinal bool) (int, int, bool) {
	var (
		cur        int
		phase      = phaseStart
		used       int // bytes consumed, up to the last accepted word
		read       int // words accepted
		pos        int // scan cursor
		sawHundred bool
		lastWord   string
	)
	for pos < len(s) && phase != phaseDone {
		if read > 0 {
			sep := numSepRe.FindString(s[pos:])
			if sep == "" {
				break
			}
			pos += len(sep)
		}
		w := numWordRe.FindString(s[pos:])
		if w == "" {
			break
		}
		lw := strings.ToLower(w)
		wordEnd := pos + len(w)

		if lw == "and" {
			// "one hundred and five": only between "hundred" and a number word.
			if phase != phaseHundred {
				break
			}
			nxt := ""
			if sep := numSepRe.FindString(s[wordEnd:]); sep != "" {
				nxt = strings.ToLower(numWordRe.FindString(s[wordEnd+len(sep):]))
			}
			if !isNumberWord(nxt) {
				break
			}
			pos = wordEnd
			continue
		}

		next, add, ok := stepSpelled(phase, lw, allowOrdinal, sawHundred)
		if !ok {
			break
		}
		if lw == "hundred" {
			cur *= 100
			sawHundred = true
		} else if lw == "hundredth" {
			cur *= 100
		} else {
			cur += add
		}
		phase = next
		read++
		used = wordEnd
		pos = wordEnd
		lastWord = lw
	}
	if read == 0 {
		return 0, 0, false
	}
	if allowOrdinal {
		// "the twenty" is a cardinal after "the", which is not announced that way.
		if ordUnits[lastWord] == 0 && ordTens[lastWord] == 0 && lastWord != "hundredth" {
			return 0, 0, false
		}
	}
	return cur, used, true
}

// stepSpelled is the transition function of the spelled-number reader: given
// the phase and the next lowercase word it returns the next phase, the value to
// add, and whether the word continues the number.
func stepSpelled(phase numPhase, w string, allowOrdinal, sawHundred bool) (numPhase, int, bool) {
	atBoundary := phase == phaseStart || phase == phaseHundred
	if allowOrdinal {
		if v := ordUnits[w]; v > 0 && (atBoundary || (phase == phaseTens && v < 10)) {
			return phaseDone, v, true
		}
		if v := ordTens[w]; v > 0 && atBoundary {
			return phaseDone, v, true
		}
		if w == "hundredth" && phase == phaseUnit && !sawHundred {
			return phaseDone, 0, true
		}
	}
	switch {
	case w == "zero":
		if !allowOrdinal && phase == phaseStart {
			return phaseDone, 0, true
		}
	case w == "hundred":
		// Only after a unit 1..9, and only once: "one hundred", never "ten hundred".
		if phase == phaseUnit && !sawHundred {
			return phaseHundred, 0, true
		}
	case numTens[w] > 0:
		if atBoundary {
			return phaseTens, numTens[w], true
		}
	case numUnits[w] > 0:
		v := numUnits[w]
		switch {
		case v >= 10 && atBoundary:
			return phaseTeen, v, true
		case v < 10 && atBoundary:
			return phaseUnit, v, true
		case v < 10 && phase == phaseTens:
			return phaseDone, v, true
		}
	}
	return phase, 0, false
}

func isNumberWord(w string) bool {
	if w == "" {
		return false
	}
	_, u := numUnits[w]
	_, t := numTens[w]
	return u || t
}

// parseSpokenNumber converts "12", "twelve" or "twenty-one" to its value.
// Returns 0 when s is not wholly a number. It keeps the contract the classifier
// had before the grammar was shared (it is used on already-isolated tokens).
func parseSpokenNumber(s string) int {
	s = strings.TrimSpace(s)
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	n, used, ok := scanSpelled(strings.ToLower(s), false)
	if !ok || used != len(s) {
		return 0
	}
	return n
}
