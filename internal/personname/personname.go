// file: internal/personname/personname.go
// version: 1.3.0
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
//
// # Known limit: Georgian, and Armenian written lowercase
//
// The formulation above is right for CASELESS scripts and wrong for a CASED
// script whose DEFAULT written form is the lowercase one. Georgian Mkhedruli
// letters are Unicode Ll -- unicode.IsLower('გ') is true, because Unicode 11
// added Mtavruli capitals -- yet Mkhedruli is how Georgian is normally written.
// So LooksLikePersonName("გიორგი ბაქრაძე") is FALSE and every Georgian author is
// dropped at all five call sites. Not a regression (the ASCII test this package
// replaced dropped them too), but not fixed either.
//
// Do not "fix" it by accepting runes with no uppercase mapping: Go DOES map
// Mkhedruli to Mtavruli (unicode.ToUpper('გ') == 'Გ'), so that test rejects
// Georgian exactly as today. Measured 2026-09-01; see
// todo.d/20260901_georgian_dropped_by_person_name_predicate.md for the disproof
// and for why a per-script exception is needed instead.
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

// structuralWords mark a volume/part token rather than a person. Without this
// guard "Book 3" and "Chapter 1" parse as two capitalised words and are
// indistinguishable from a name -- which is exactly what the dedup copy did.
//
// These are matched as WHOLE FIRST WORDS, never as bare prefixes. A
// strings.HasPrefix test here rejects real authors whose names merely begin with
// the same letters -- Booker T. Washington, Volker Kutscher, Volney Beckner,
// Volodymyr Zelensky, Voltaire, Partha Chatterjee, Partridge -- and the damage
// does not stop at a refusal. SplitCompositeAuthorName's comma branch falls
// THROUGH on refusal (internal/dedup/author.go:270) to a weaker semicolon gate
// with no shape check, so refusing "Volker Kutscher, Niall Sellar" does not
// merely drop a split, it can mint the whole composite as one author name.
// Measured against the real splitter: 886 distinct author strings that a bare
// prefix test would newly mint, and 33,580 of 195,245 realistic composites
// silently losing their split.
//
// Plurals are listed explicitly so widening the match does not also start
// admitting "Parts Unknown", which the prefix test caught by accident.
var structuralWords = map[string]bool{
	"book": true, "books": true,
	"chapter": true, "chapters": true,
	"part": true, "parts": true,
	"vol": true, "vols": true,
	"volume": true, "volumes": true,
	"disc": true, "discs": true,
}

// IsValidAuthor rejects strings that cannot be an author at all: empty, purely
// numeric, or led by a structural marker word.
func IsValidAuthor(author string) bool {
	if author == "" {
		return false
	}
	// Purely numeric ("01", "1984") is a disc or a year, not a person.
	if _, err := strconv.Atoi(author); err == nil {
		return false
	}
	// Test the first WORD, not a prefix. Trailing punctuation and digits are
	// stripped so the label forms that actually occur -- "Vol. 2", "Book3",
	// "Disc 1" -- still match, while "Volker" and "Booker" do not.
	fields := strings.Fields(strings.ToLower(author))
	if len(fields) > 0 && structuralWords[strings.TrimRight(fields[0], ".,-_0123456789")] {
		return false
	}
	return true
}

// IsNameParticle reports whether w is a name particle ("de", "van", "Le") in any
// casing. Exported because internal/dedup needs the SAME set to decide that a
// trailing particle is not a surname -- a second copy of this list in dedup is
// precisely the duplication this package exists to remove.
func IsNameParticle(w string) bool {
	return nameParticles[strings.ToLower(strings.TrimSpace(w))]
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
		// strings.Fields never yields an empty field, so r[0] is always safe here
		// and a len(r)==0 guard would be unreachable code that no test can kill.
		r := []rune(w)
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
