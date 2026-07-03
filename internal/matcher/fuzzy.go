// file: internal/matcher/fuzzy.go
// version: 1.1.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-07-03

package matcher

import (
	"strings"
	"unicode"
)

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

	return score
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
