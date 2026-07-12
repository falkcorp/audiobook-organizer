// file: internal/matcher/fuzzy.go
// version: 1.2.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-07-11

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

// LevenshteinDistance computes the edit distance between two strings.
func LevenshteinDistance(a, b string) int {
	a = strings.ToLower(a)
	b = strings.ToLower(b)
	ra := []rune(a)
	rb := []rune(b)
	la, lb := len(ra), len(rb)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	// Single-row DP
	prev := make([]int, lb+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr := make([]int, lb+1)
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min(curr[j-1]+1, min(prev[j]+1, prev[j-1]+cost))
		}
		prev = curr
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

	t0 := strings.Join(inter, " ")                                        // shared tokens
	t1 := strings.TrimSpace(t0 + " " + strings.Join(onlyA, " "))          // shared + a-only
	t2 := strings.TrimSpace(t0 + " " + strings.Join(onlyB, " "))          // shared + b-only

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
	sim := 1.0 - float64(LevenshteinDistance(a, b))/float64(maxLen)
	if sim < 0 {
		sim = 0
	}
	return sim
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
