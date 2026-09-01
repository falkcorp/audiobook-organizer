// file: internal/matcher/fuzzy.go
// version: 1.4.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-09-01

package matcher

import (
	"sort"
	"strings"
	"unicode"
)

// tokenSetScale maps a TokenSetRatio (0..1) onto the int ScoreMatch scale. It
// mirrors the strongest existing Levenshtein-based term (the per-word match at
// *70, see ScoreMatch) so the order-insensitive signal stays a strong hint
// while remaining below the exact/prefix heuristic tier (90-100). Because the
// blend is raise-only, this constant only bounds how much a score can RISE.
const tokenSetScale = 70

// FuzzyResult holds a scored search result.
type FuzzyResult struct {
	Index int // index into the original slice
	Score int // 0-100, higher is better
}

// LevenshteinDistance computes the case-insensitive edit distance between two
// strings, in RUNES. It lowercases both sides and defers to
// LevenshteinDistanceCaseSensitive.
//
// Callers that must NOT fold case want that function directly — see its doc
// comment for why the two are separate.
func LevenshteinDistance(a, b string) int {
	return LevenshteinDistanceCaseSensitive(strings.ToLower(a), strings.ToLower(b))
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
	ra := []rune(a)
	rb := []rune(b)
	la, lb := len(ra), len(rb)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	// Two-row DP with the rows SWAPPED rather than reallocated per outer
	// iteration: this runs pairwise over title/author forms inside
	// full-library dedup scans, so an allocation per row of the matrix is an
	// allocation per candidate pair per form pair.
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
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

// ScoreMatch scores how well query matches target. Returns 0-100.
func ScoreMatch(query, target string) int {
	if query == "" || target == "" {
		return 0
	}
	q := normalize(query)
	t := normalize(target)

	if q == "" || t == "" {
		return 0
	}

	// Exact match
	if q == t {
		return 100
	}

	score := 0

	// Prefix match: target starts with query
	if strings.HasPrefix(t, q) {
		score = max(score, 90)
	}

	// Substring match
	if strings.Contains(t, q) {
		// Score higher for shorter targets (more specific match)
		ratio := float64(len([]rune(q))) / float64(len([]rune(t)))
		substringScore := 60 + int(ratio*25)
		score = max(score, substringScore)
	}

	// Word-start match: query matches start of any word in target
	words := strings.Fields(t)
	for _, w := range words {
		if strings.HasPrefix(w, q) {
			score = max(score, 80)
			break
		}
	}

	// Fuzzy distance on the whole string
	dist := LevenshteinDistance(q, t)
	maxLen := max(len([]rune(q)), len([]rune(t)))
	if maxLen > 0 {
		similarity := 1.0 - float64(dist)/float64(maxLen)
		fuzzyScore := int(similarity * 50)
		if fuzzyScore < 0 {
			fuzzyScore = 0
		}
		score = max(score, fuzzyScore)
	}

	// Fuzzy distance on individual words (find best matching word)
	for _, w := range words {
		dist := LevenshteinDistance(q, w)
		wLen := max(len([]rune(q)), len([]rune(w)))
		if wLen > 0 {
			similarity := 1.0 - float64(dist)/float64(wLen)
			wordScore := int(similarity * 70)
			if wordScore < 0 {
				wordScore = 0
			}
			score = max(score, wordScore)
		}
	}

	// Order-insensitive token-set signal, blended RAISE-ONLY: it can lift a
	// score (e.g. "Author: Title" vs "Title - Author", which the lexical terms
	// above punish) but never lower one, so no caller threshold can newly
	// reject a previously-accepted match.
	if tokenScore := int(TokenSetRatio(query, target) * tokenSetScale); tokenScore > score {
		score = tokenScore
	}

	return score
}

// TokenSetRatio computes an order-insensitive similarity in [0,1] between two
// strings, following fuzzywuzzy's token_set_ratio construction. It tokenizes
// with the shared normalize helper (so punctuation/case are ignored), then
// compares the sorted intersection against the sorted intersection-plus-remainder
// of each side and returns the best Levenshtein-based similarity of those three
// comparisons. The result is symmetric (TokenSetRatio(a,b) == TokenSetRatio(b,a));
// empty or whitespace-only input on either side returns 0 (unknown, not a match).
func TokenSetRatio(a, b string) float64 {
	fieldsA := strings.Fields(normalize(a))
	fieldsB := strings.Fields(normalize(b))
	if len(fieldsA) == 0 || len(fieldsB) == 0 {
		return 0
	}

	setA := make(map[string]struct{}, len(fieldsA))
	for _, tok := range fieldsA {
		setA[tok] = struct{}{}
	}
	setB := make(map[string]struct{}, len(fieldsB))
	for _, tok := range fieldsB {
		setB[tok] = struct{}{}
	}

	var inter, onlyA, onlyB []string
	for tok := range setA {
		if _, ok := setB[tok]; ok {
			inter = append(inter, tok)
		} else {
			onlyA = append(onlyA, tok)
		}
	}
	for tok := range setB {
		if _, ok := setA[tok]; !ok {
			onlyB = append(onlyB, tok)
		}
	}
	sort.Strings(inter)
	sort.Strings(onlyA)
	sort.Strings(onlyB)

	t0 := strings.Join(inter, " ")                               // shared tokens
	t1 := strings.TrimSpace(t0 + " " + strings.Join(onlyA, " ")) // shared + a-only
	t2 := strings.TrimSpace(t0 + " " + strings.Join(onlyB, " ")) // shared + b-only

	return max(tokenSimilarity(t0, t1), tokenSimilarity(t0, t2), tokenSimilarity(t1, t2))
}

// tokenSimilarity returns a Levenshtein-based similarity in [0,1] for two
// already-normalized token strings (1.0 == identical). Two empty strings are
// treated as identical; anything vs empty is 0.
func tokenSimilarity(a, b string) float64 {
	if a == "" && b == "" {
		return 1.0
	}
	maxLen := max(len([]rune(a)), len([]rune(b)))
	if maxLen == 0 {
		return 0
	}
	// No `if sim < 0` clamp, for the same reason it was removed from
	// internal/dedup: the only way this goes negative is the numerator and
	// denominator disagreeing on their unit, and flooring that to 0 turns a
	// detectable defect into a plausible-looking score. Both are runes here.
	// LevenshteinDistance lowercases internally while maxLen is taken from the
	// originals, which is safe because Go's strings.ToLower maps rune-by-rune:
	// verified across all 1,112,064 valid code points that ToLower never
	// changes a string's rune count.
	return 1.0 - float64(LevenshteinDistance(a, b))/float64(maxLen)
}

// RankResults scores each candidate against the query and returns results
// sorted by score descending. Only results with score >= minScore are returned.
func RankResults(query string, candidates []string, minScore int) []FuzzyResult {
	var results []FuzzyResult
	for i, c := range candidates {
		s := ScoreMatch(query, c)
		if s >= minScore {
			results = append(results, FuzzyResult{Index: i, Score: s})
		}
	}
	// Sort descending by score
	for i := 1; i < len(results); i++ {
		for j := i; j > 0 && results[j].Score > results[j-1].Score; j-- {
			results[j], results[j-1] = results[j-1], results[j]
		}
	}
	return results
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
