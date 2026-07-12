// file: internal/matcher/fuzzy_test.go
// version: 1.2.0
// guid: b2c3d4e5-f6a7-8901-bcde-f23456789012
// last-edited: 2026-07-11

package matcher

import "testing"

// scoreMatchGoldenRow captures a (query, target) pair and the ScoreMatch value
// it produced BEFORE the TokenSetRatio raise-only blend was introduced. The
// blend must never DECREASE any pair's score, so golden is treated as a
// MINIMUM floor (see TestScoreMatchGolden). Rows expected to RISE from the
// order-insensitive token-set signal are annotated in the note.
type scoreMatchGoldenRow struct {
	query, target string
	golden        int
	note          string
}

// scoreMatchGolden is the shared fixture table used by TestScoreMatchGolden and
// TestTokenSetRatioNoRegression. Values are the pre-blend ScoreMatch outputs.
var scoreMatchGolden = []scoreMatchGoldenRow{
	{"Harry Potter", "Harry Potter", 100, "exact"},
	{"harry potter", "Harry Potter", 100, "exact case-insensitive"},
	{"Harry", "Harry Potter and the Philosopher's Stone", 90, "prefix; token-set subset -> 70, prefix 90 wins, unchanged"},
	{"Potter", "Harry Potter", 80, "word-start subset; token-set 70, 80 wins, unchanged"},
	{"Hary Poter", "Harry Potter", 41, "typo, no token intersection"},
	{"xyzzy", "Harry Potter", 13, "unrelated"},
	{"Zola", "Émile Zola", 80, "non-ASCII substring subset; unchanged"},
	{"The Hobbit - Tolkien", "Tolkien: The Hobbit", 25, "reordered tokens -> rises to token-set floor"},
	{"Tolkien The Hobbit", "The Hobbit Tolkien", 27, "reordered tokens -> rises to token-set floor"},
	{"Dune", "Dune Messiah", 90, "prefix; token-set 70, 90 wins, unchanged"},
	{"dune", "June", 52, "single-token typo"},
	{"The Lord of the Rings", "Lord of the Rings The", 30, "reordered tokens -> rises to token-set floor"},
	{"Ender's Game - Orson Scott Card", "Orson Scott Card - Ender's Game", 14, "reordered tokens -> rises to token-set floor"},
	{"Neuromancer", "Neuromancer by William Gibson", 90, "prefix; unchanged"},
	{"Foundation Isaac Asimov", "Asimov Foundation", 30, "reordered/appended -> rises to token-set floor"},
	{"1984", "Nineteen Eighty-Four", 0, "unrelated spelled-out number"},
	{"The Great Gatsby", "Great Gatsby", 37, "author/prefix drop -> rises to token-set floor"},
	{"Brandon Sanderson Mistborn", "Mistborn Brandon Sanderson", 24, "reordered tokens -> rises to token-set floor"},
	{"A Game of Thrones", "Game of Thrones A", 38, "reordered tokens -> rises to token-set floor"},
	{"Snow Crash", "Cryptonomicon", 5, "unrelated"},
}

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

// TestScoreMatchGolden locks the current ScoreMatch behavior. In this commit it
// asserts EQUALITY against the captured golden values; a later commit relaxes it
// to a floor (got >= golden) once the raise-only TokenSetRatio blend lands.
func TestScoreMatchGolden(t *testing.T) {
	for _, r := range scoreMatchGolden {
		got := ScoreMatch(r.query, r.target)
		if got != r.golden {
			t.Errorf("ScoreMatch(%q, %q) = %d, golden %d (%s)", r.query, r.target, got, r.golden, r.note)
		}
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
