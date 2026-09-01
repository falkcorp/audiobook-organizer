// file: internal/personname/personname.go
// version: 1.0.0
// guid: 8c3f6a15-2e94-4d78-b1a0-5f7e2c9d3b48
// last-edited: 2026-09-01

// Package personname answers "does this string look like a human author's
// name, rather than a title or a structural marker?" -- the heuristic that
// decides which half of "X - Y" is the author when a file or folder has to be
// parsed for one.
//
// # Why this is its own package
//
// It existed THREE times -- internal/scanner, internal/metadata and
// internal/dedup -- and the copies had DIVERGED. Measured over a corpus of real
// author names, titles and structural markers, the three disagreed on 13 of 40
// inputs, in BOTH directions:
//
//   - scanner and metadata compared capitalisation with an ASCII byte test
//     (word[0] < 'A' || word[0] > 'Z'). Any name whose FIRST letter is
//     non-ASCII was rejected: Émile Zola, Åsa Larsson, Ítalo Calvino, Øyvind
//     Torseter, Александр Пушкин, 村上 春樹. (Names like "José Saramago" passed,
//     because 'J' is ASCII -- the bug is narrower than "all accented names".)
//     Both also rejected lowercase particles, losing "Simone de Beauvoir" and
//     "Ludwig van Beethoven".
//   - dedup handled all of the above correctly, but had no validity guard at
//     all, so it answered TRUE for "Book 3", "Chapter 1", "Volume 2" and
//     "Disc 1" -- structural markers, filed as people.
//
// So no single copy was the good one. This implementation is the UNION of the
// three sets of checks, which is why it is a merge rather than a promotion of a
// winner.
//
// # The rule that matters most
//
// Capitalisation is expressed as "the first rune must not be LOWERCASE", never
// as "must be UPPERCASE". unicode.IsUpper is false for every caseless script --
// CJK, Hebrew, Arabic, Thai -- so requiring positive uppercase excludes them
// entirely. That distinction is not stylistic; it is the difference between
// supporting Japanese authors and silently dropping them.
package personname

import (
	"strconv"
	"strings"
	"unicode"
)

// nameParticles are lowercase words that legitimately appear INSIDE a person's
// name ("Simone de Beauvoir", "Ludwig van Beethoven"). Interior lowercase words
// otherwise mark a title clause ("A Game of Thrones"), so the particle list is
// what separates the two.
var nameParticles = map[string]bool{
	"de": true, "la": true, "le": true, "van": true, "von": true,
	"del": true, "della": true, "di": true, "da": true, "dos": true,
	"du": true, "den": true, "ter": true, "bin": true, "ibn": true,
	"al": true, "el": true, "st.": true, "mac": true,
}

// structuralPrefixes mark a volume/part token rather than a person. Without
// this guard "Book 3" and "Chapter 1" parse as two capitalised words and are
// indistinguishable from a name -- which is exactly what the dedup copy did.
var structuralPrefixes = []string{"book", "chapter", "part", "vol", "volume", "disc"}

// IsValidAuthor rejects strings that cannot be an author at all: empty, purely
// numeric, or beginning with a structural marker.
func IsValidAuthor(author string) bool {
	if author == "" {
		return false
	}
	lower := strings.ToLower(author)
	for _, p := range structuralPrefixes {
		if strings.HasPrefix(lower, p) {
			return false
		}
	}
	// Purely numeric ("01", "1984") is a disc or a year, not a person.
	if _, err := strconv.Atoi(author); err == nil {
		return false
	}
	return true
}

// LooksLikePersonName reports whether s reads as a human name: two to four
// words, none of them beginning with a lowercase letter unless it is a name
// particle, carrying no sentence punctuation and no trailing parenthetical.
func LooksLikePersonName(s string) bool {
	if !IsValidAuthor(s) {
		return false
	}
	// Sentence punctuation belongs to titles ("Do Androids Dream?",
	// "Fear and Loathing!"), never to names.
	if strings.ContainsAny(s, ":!?") {
		return false
	}
	// A trailing parenthetical is an edition marker ("... (Unabridged)").
	if strings.HasSuffix(strings.TrimSpace(s), ")") {
		return false
	}

	fields := strings.Fields(s)
	if len(fields) < 2 || len(fields) > 4 {
		return false
	}

	for i, w := range fields {
		r := []rune(w)
		if len(r) == 0 {
			return false
		}
		// Every word must START WITH A LETTER. Checking only "is not lowercase"
		// is not enough: digits and punctuation are neither upper nor lower, so
		// "Pratchett 036" would pass as a name and get filed as a real author.
		// (That placeholder laundering is what
		// TestExtractFromFilenameDoesNotLaunderThePlaceholder guards.)
		if !unicode.IsLetter(r[0]) {
			return false
		}
		// And it must not be LOWERCASE -- expressed that way, never as "must be
		// uppercase". unicode.IsUpper is false for every caseless script (CJK,
		// Hebrew, Arabic, Thai), so requiring positive uppercase drops them all.
		// Interior lowercase is allowed only for name particles.
		if unicode.IsLower(r[0]) {
			if i == 0 || !nameParticles[strings.ToLower(w)] {
				return false
			}
		}
	}
	return true
}
