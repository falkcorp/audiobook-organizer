// file: internal/matcher/fuzzy_test.go
// version: 1.3.0
// guid: b2c3d4e5-f6a7-8901-bcde-f23456789012
// last-edited: 2026-10-30

package matcher

import (
	"math/rand"
	"testing"
)

// scoreMatchGoldenRow captures a (query, target) pair and the directional
// string-API ScoreMatch value it produced BEFORE the token-set raise-only blend
// was introduced (ScoreMatch itself was replaced by SymmetricScore, which is
// the larger of its two directions, so these stay valid as floors). The
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

// scoreMatch scores a pair through the shipped Prepare + SymmetricScore path,
// the only scorer left in the package. It is symmetric, and at least as large
// as the directional score the old ScoreMatch gave either way round, so the
// golden floors and ranges below carry over unchanged.
func scoreMatch(query, target string) int {
	pa, pb := Prepare(query), Prepare(target)
	var sc Scratch
	return SymmetricScore(&pa, &pb, &sc)
}

// tokenSetRatio runs the shipped token-set ratio over freshly prepared inputs.
func tokenSetRatio(a, b string) float64 {
	pa, pb := Prepare(a), Prepare(b)
	var sc Scratch
	return tokenSetRatioPrepared(&pa, &pb, &sc)
}

// tokenSimilarity runs the shipped rune-level similarity over lower-cased
// decodings of a and b.
func tokenSimilarity(a, b string) float64 {
	var ls levScratch
	return tokenSimilarityRunes(appendLowerRunes(nil, a), appendLowerRunes(nil, b), &ls)
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
		{"Café", "Cafe", 1},             // one accented character substitution
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
		score := scoreMatch(tt.query, tt.target)
		if score < tt.minExpected || score > tt.maxExpected {
			t.Errorf("scoreMatch(%q, %q) = %d, want [%d, %d]",
				tt.query, tt.target, score, tt.minExpected, tt.maxExpected)
		}
	}
}

func TestScoreMatch_Ranking(t *testing.T) {
	query := "dune"
	// Exact should beat substring which should beat fuzzy
	exact := scoreMatch(query, "Dune")
	substring := scoreMatch(query, "Dune Messiah")
	fuzzy := scoreMatch(query, "June")

	if exact <= substring {
		t.Errorf("exact (%d) should beat substring (%d)", exact, substring)
	}
	if substring <= fuzzy {
		t.Errorf("substring (%d) should beat fuzzy (%d)", substring, fuzzy)
	}
}

// TestScoreMatchRanksExactFirst: the exact title outscores near-misses and
// unrelated text, and only the exact match clears a 90 floor.
func TestScoreMatchRanksExactFirst(t *testing.T) {
	query := "Lamp of the Rivers"
	exact := scoreMatch(query, "The Lamp of the Rivers")
	for _, other := range []string{"Lamp of the Fires", "Lump of the Rivers", "Something Completely Different"} {
		if got := scoreMatch(query, other); got > exact {
			t.Errorf("scoreMatch(%q, %q)=%d outranks the exact match (%d)", query, other, got, exact)
		}
	}
	if got := scoreMatch("Exact Match", "Exact Match"); got < 90 {
		t.Errorf("exact match scored %d, want >= 90", got)
	}
	if got := scoreMatch("Exact Match", "something else entirely"); got >= 90 {
		t.Errorf("unrelated text scored %d, want < 90", got)
	}
}

// TestScoreMatchGolden enforces the raise-only contract of the TokenSetRatio
// blend: every pre-blend golden value is a MINIMUM floor, so no pair may score
// LOWER than it did before the blend (got >= golden). Reordered-token rows are
// expected to rise; that is allowed and annotated in each row's note.
func TestScoreMatchGolden(t *testing.T) {
	for _, r := range scoreMatchGolden {
		got := scoreMatch(r.query, r.target)
		if got < r.golden {
			t.Errorf("scoreMatch(%q, %q) = %d regressed below golden floor %d (%s)",
				r.query, r.target, got, r.golden, r.note)
		}
	}
}

// TestTokenSetRatioNoRegression is the anti-over-suppression guard: every
// known-good pair that previously scored at least a given minScore must
// still pass it after the blend (the happy path still matches).
func TestTokenSetRatioNoRegression(t *testing.T) {
	// Known-good matches and the minScore threshold they cleared pre-blend.
	goodMatches := []struct {
		query, target string
		minScore      int
	}{
		{"Harry Potter", "Harry Potter", 100},
		{"Harry", "Harry Potter and the Philosopher's Stone", 80},
		{"Potter", "Harry Potter", 60},
		{"Dune", "Dune Messiah", 80},
		{"Neuromancer", "Neuromancer by William Gibson", 80},
		// Reordered-token pairs that scored poorly before now clear a real bar.
		{"The Hobbit - Tolkien", "Tolkien: The Hobbit", 50},
		{"Brandon Sanderson Mistborn", "Mistborn Brandon Sanderson", 50},
	}
	for _, m := range goodMatches {
		if got := scoreMatch(m.query, m.target); got < m.minScore {
			t.Errorf("scoreMatch(%q, %q)=%d fell below the known-good floor %d",
				m.query, m.target, got, m.minScore)
		}
	}
}

// TestTokenSetRatio covers symmetry, order-insensitivity, empty/whitespace input,
// and single-token degradation.
func TestTokenSetRatio(t *testing.T) {
	// Symmetry: tokenSetRatio(a,b) == tokenSetRatio(b,a).
	symPairs := [][2]string{
		{"The Hobbit - Tolkien", "Tolkien: The Hobbit"},
		{"Dune", "Dune Messiah"},
		{"Foundation Isaac Asimov", "Asimov Foundation"},
		{"totally different", "unrelated words here"},
	}
	for _, p := range symPairs {
		ab := tokenSetRatio(p[0], p[1])
		ba := tokenSetRatio(p[1], p[0])
		if ab != ba {
			t.Errorf("TokenSetRatio not symmetric: (%q,%q)=%g vs (%q,%q)=%g", p[0], p[1], ab, p[1], p[0], ba)
		}
	}

	// Empty / whitespace-only input -> 0 (unknown, not a match), no crash.
	empties := [][2]string{
		{"", "Harry Potter"},
		{"Harry Potter", ""},
		{"   ", "Harry Potter"},
		{"!!! ---", "Harry Potter"}, // normalize strips to empty
		{"", ""},
	}
	for _, p := range empties {
		if got := tokenSetRatio(p[0], p[1]); got != 0 {
			t.Errorf("tokenSetRatio(%q, %q) = %g, want 0 for empty input", p[0], p[1], got)
		}
	}

	// Order-insensitivity: identical token sets in different order -> 1.0.
	if got := tokenSetRatio("The Hobbit Tolkien", "Tolkien The Hobbit"); got != 1.0 {
		t.Errorf("TokenSetRatio for reordered identical tokens = %g, want 1.0", got)
	}

	// Single-token exact -> 1.0; single-token mismatch degrades gracefully to (0,1).
	if got := tokenSetRatio("dune", "Dune"); got != 1.0 {
		t.Errorf("tokenSetRatio(dune, Dune) = %g, want 1.0", got)
	}
	if got := tokenSetRatio("dune", "june"); got <= 0 || got >= 1 {
		t.Errorf("tokenSetRatio(dune, june) = %g, want in (0,1)", got)
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

// TestSimilarityStaysInUnitRange guards the clamp's removal: rune edit distance
// cannot exceed the longer rune length, so similarity is in [0,1] by
// construction. This fails immediately if the distance unit and the length unit
// ever diverge again (the defect that motivated removing the clamp).
func TestSimilarityStaysInUnitRange(t *testing.T) {
	alphabets := []string{"abc", "aé東", "ΣİǄ", "Достй", ""}
	r := rand.New(rand.NewSource(7))
	for range 100000 {
		mk := func() string {
			al := []rune(alphabets[r.Intn(len(alphabets))])
			if len(al) == 0 {
				return ""
			}
			out := make([]rune, r.Intn(6))
			for j := range out {
				out[j] = al[r.Intn(len(al))]
			}
			return string(out)
		}
		a, b := mk(), mk()
		if s := tokenSimilarity(a, b); s < 0 || s > 1 {
			t.Fatalf("tokenSimilarity(%q, %q) = %f, outside [0,1]", a, b, s)
		}
	}
}

// TestLevenshteinDistanceCaseSensitiveDoesNotFold pins the DIFFERENCE between
// the core and the wrapper, which is the entire reason they are separate
// functions.
//
// internal/dedup routes through the core precisely because NormalizeAuthorName
// preserves case (it expands collapsed initials with an [A-Z]-anchored
// pattern), so folding here would silently make author comparison
// case-insensitive. Until this test existed, adding strings.ToLower to the core
// left the whole dedup + matcher suite GREEN -- the design argument had no
// enforcement at all.
func TestLevenshteinDistanceCaseSensitiveDoesNotFold(t *testing.T) {
	if got := LevenshteinDistanceCaseSensitive("Boll", "boll"); got != 1 {
		t.Errorf("LevenshteinDistanceCaseSensitive(%q, %q) = %d, want 1: the core must NOT fold case", "Boll", "boll", got)
	}
	if got := LevenshteinDistanceCaseSensitive("ABC", "abc"); got != 3 {
		t.Errorf("LevenshteinDistanceCaseSensitive(%q, %q) = %d, want 3: the core must NOT fold case", "ABC", "abc", got)
	}
	// The wrapper is the one that folds. Both halves stated together, so the
	// pair cannot drift into agreeing.
	if got := LevenshteinDistance("ABC", "abc"); got != 0 {
		t.Errorf("LevenshteinDistance(%q, %q) = %d, want 0: the wrapper MUST fold case", "ABC", "abc", got)
	}
}

// TestTokenSimilarityPinsNonASCIIValues is the twin of dedup's
// TestNormalizedSimilarityOnNonASCII, and exists for the reason that test
// turned out to matter: the [0,1] property test above is ONE-SIDED. A
// denominator that is too LARGE pushes similarity toward 1, which no [0,1]
// range check can observe -- and too-high similarity is the direction that
// causes false merges. Changing maxLen to len(a)/len(b) (bytes) survived
// everything until these pinned values existed.
func TestTokenSimilarityPinsNonASCIIValues(t *testing.T) {
	cases := []struct {
		a, b string
		want float64
	}{
		{"Böll", "Boll", 0.75},
		{"村上春樹", "村上春树", 0.75},
		{"Émile Zola", "Emile Zola", 0.90},
		{"東京", "東京都", 1 - 1.0/3},
	}
	for _, c := range cases {
		got := tokenSimilarity(c.a, c.b)
		if diff := got - c.want; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("tokenSimilarity(%q, %q) = %.6f, want %.6f", c.a, c.b, got, c.want)
		}
	}
}
