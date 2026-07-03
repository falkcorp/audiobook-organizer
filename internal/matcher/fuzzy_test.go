// file: internal/matcher/fuzzy_test.go
// version: 1.1.0
// guid: b2c3d4e5-f6a7-8901-bcde-f23456789012
// last-edited: 2026-07-03

package matcher

import "testing"

func TestLevenshteinDistance(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"kitten", "sitting", 3},
		{"saturday", "sunday", 3},
		{"abc", "abc", 0},
		{"ABC", "abc", 0}, // case insensitive
		// Non-ASCII cases: rune-based distance
		{"Émile Zola", "Emile Zola", 1}, // one accented character substitution
		{"東京", "東京都", 1},                // one CJK character insertion
		{"Café", "Cafe", 1},                // one accented character substitution
	}
	for _, tt := range tests {
		got := LevenshteinDistance(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("LevenshteinDistance(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestScoreMatch(t *testing.T) {
	tests := []struct {
		query, target string
		minExpected   int
		maxExpected   int
	}{
		// Exact match
		{"Harry Potter", "Harry Potter", 100, 100},
		// Case insensitive exact
		{"harry potter", "Harry Potter", 100, 100},
		// Prefix
		{"Harry", "Harry Potter and the Philosopher's Stone", 80, 95},
		// Substring
		{"Potter", "Harry Potter", 60, 90},
		// Fuzzy (typo)
		{"Hary Poter", "Harry Potter", 30, 75},
		// No match
		{"xyzzy", "Harry Potter", 0, 20},
		// Empty
		{"", "Harry Potter", 0, 0},
		{"Harry", "", 0, 0},
		// Non-ASCII substring match (rune-based ratios)
		{"Zola", "Émile Zola", 60, 90},
	}
	for _, tt := range tests {
		score := ScoreMatch(tt.query, tt.target)
		if score < tt.minExpected || score > tt.maxExpected {
			t.Errorf("ScoreMatch(%q, %q) = %d, want [%d, %d]",
				tt.query, tt.target, score, tt.minExpected, tt.maxExpected)
		}
	}
}

func TestScoreMatch_Ranking(t *testing.T) {
	query := "dune"
	// Exact should beat substring which should beat fuzzy
	exact := ScoreMatch(query, "Dune")
	substring := ScoreMatch(query, "Dune Messiah")
	fuzzy := ScoreMatch(query, "June")

	if exact <= substring {
		t.Errorf("exact (%d) should beat substring (%d)", exact, substring)
	}
	if substring <= fuzzy {
		t.Errorf("substring (%d) should beat fuzzy (%d)", substring, fuzzy)
	}
}

func TestRankResults(t *testing.T) {
	candidates := []string{
		"The Lord of the Rings",
		"Lord of the Flies",
		"Lard of the Rings",
		"Something Completely Different",
	}
	results := RankResults("Lord of the Rings", candidates, 10)

	if len(results) == 0 {
		t.Fatal("expected results")
	}
	// First result should be exact match
	if results[0].Index != 0 {
		t.Errorf("expected index 0 first, got %d (score %d)", results[0].Index, results[0].Score)
	}
	// Scores should be descending
	for i := 1; i < len(results); i++ {
		if results[i].Score > results[i-1].Score {
			t.Errorf("results not sorted: score[%d]=%d > score[%d]=%d",
				i, results[i].Score, i-1, results[i-1].Score)
		}
	}
}

func TestRankResults_MinScore(t *testing.T) {
	candidates := []string{"Exact Match", "something else entirely"}
	results := RankResults("Exact Match", candidates, 90)
	// Only the exact match should pass
	if len(results) != 1 {
		t.Errorf("expected 1 result with minScore 90, got %d", len(results))
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"Hello, World!", "hello world"},
		{"  spaces  ", "spaces"},
		{"it's a test", "its a test"},
	}
	for _, tt := range tests {
		got := normalize(tt.input)
		if got != tt.want {
			t.Errorf("normalize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
