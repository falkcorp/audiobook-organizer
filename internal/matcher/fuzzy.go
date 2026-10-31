// file: internal/matcher/fuzzy.go
// version: 1.5.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-10-30

package matcher

import (
	"strings"
	"sync"
	"unicode"
)

// tokenSetScale maps a token-set ratio (0..1) onto the 0..100 SymmetricScore
// scale. It mirrors the strongest Levenshtein-based term (the per-word match at
// *70, see SymmetricScore) so the order-insensitive signal stays a strong hint
// while remaining below the exact/prefix heuristic tier (90-100). Because the
// blend is raise-only, this constant only bounds how much a score can RISE.
const tokenSetScale = 70

// LevenshteinDistance computes the case-insensitive edit distance between two
// strings, in RUNES. It lower-cases both sides as it decodes them and otherwise
// computes exactly what LevenshteinDistanceCaseSensitive does.
//
// Callers that must NOT fold case want that function directly — see its doc
// comment for why the two are separate.
func LevenshteinDistance(a, b string) int {
	return levenshteinPooled(a, b, true)
}

// LevenshteinDistanceCaseSensitive computes the edit distance between two
// strings in RUNES, comparing them exactly as given.
//
// # Runes, not bytes
//
// Distance is measured in runes because these strings are book titles and
// author names. In UTF-8 an accented, Cyrillic or CJK character occupies two
// or three bytes, so a byte-indexed implementation charges several edits for a
// single character substitution: "José"/"Jose" scores 2 instead of 1, and
// inserting one character into "東京" scores 3 instead of 1. Anything dividing
// that distance by a rune length then produces a similarity that is not merely
// imprecise but can fall out of [0,1] entirely. internal/dedup carried exactly
// such a copy until it was folded into this function.
//
// # Why this is separate from LevenshteinDistance
//
// The case-folding wrapper above is what the search/scan callers want. The
// dedup collectors are NOT among them: titles reach them already lowercased by
// normalizeTitle, but NormalizeAuthorName deliberately PRESERVES case (it
// expands collapsed initials with an [A-Z]-anchored pattern). Routing dedup
// through the folding wrapper would quietly make author comparison
// case-insensitive — a behaviour change to the population that already works.
// Hence a case-preserving core with a folding wrapper, rather than one
// function with an opinion.
func LevenshteinDistanceCaseSensitive(a, b string) int {
	return levenshteinPooled(a, b, false)
}

// levScratch is the working memory of one edit-distance computation: the two
// decoded rune slices and the two DP rows. Everything in it is reused, so a
// warm call allocates nothing.
type levScratch struct {
	a, b       []rune
	prev, curr []int
}

// levScratchPool lends levScratch values to the string-typed entry points. The
// pool holds pointers: putting a slice or struct value into a sync.Pool boxes
// it into an interface and allocates on every Put.
var levScratchPool = sync.Pool{New: func() any { return new(levScratch) }}

// levenshteinPooled is the shared body of LevenshteinDistance (fold=true) and
// LevenshteinDistanceCaseSensitive (fold=false). It decodes both strings into
// pooled rune buffers (one pass each, no []rune(s) conversion and no
// strings.ToLower copy) and runs the DP on the pooled rows.
//
// Folding per rune with unicode.ToLower is what strings.ToLower does for
// non-ASCII input (it is strings.Map(unicode.ToLower, s)), and its ASCII fast
// path agrees rune for rune. Ranging over a string yields U+FFFD for each
// invalid byte, exactly as []rune(s) and as strings.Map's replacement do, so
// invalid UTF-8 compares the same either way. The property test in
// fuzzy_equivalence_test.go checks all of that against the previous implementation.
func levenshteinPooled(a, b string, fold bool) int {
	s := levScratchPool.Get().(*levScratch)
	if fold {
		s.a = appendLowerRunes(s.a[:0], a)
		s.b = appendLowerRunes(s.b[:0], b)
	} else {
		s.a = appendRunes(s.a[:0], a)
		s.b = appendRunes(s.b[:0], b)
	}
	d := levenshteinRunes(s.a, s.b, s)
	levScratchPool.Put(s)
	return d
}

// appendRunes appends the runes of str to dst, with U+FFFD for invalid bytes.
func appendRunes(dst []rune, str string) []rune {
	for _, r := range str {
		dst = append(dst, r)
	}
	return dst
}

// appendLowerRunes is appendRunes with each rune lower-cased.
func appendLowerRunes(dst []rune, str string) []rune {
	for _, r := range str {
		dst = append(dst, unicode.ToLower(r))
	}
	return dst
}

// levenshteinRunes is the edit-distance DP over already-decoded runes, using
// (and growing) the DP rows in s. It is the one implementation behind every
// Levenshtein entry point and the prepared scorer.
//
// Two-row DP with the rows SWAPPED rather than reallocated per outer
// iteration: this runs pairwise over title/author forms inside full-library
// dedup scans, so an allocation per row of the matrix is an allocation per
// candidate pair per form pair.
func levenshteinRunes(ra, rb []rune, s *levScratch) int {
	la, lb := len(ra), len(rb)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	if cap(s.prev) < lb+1 {
		s.prev = make([]int, lb+1)
		s.curr = make([]int, lb+1)
	}
	prev, curr := s.prev[:lb+1], s.curr[:lb+1]
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

// normalize lowercases and strips non-alphanumeric characters except spaces.
func normalize(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}
