// file: internal/dedup/levenshtein_units_test.go
// version: 1.0.1
// guid: 7c4e1a92-3d68-4b05-9f27-8a1c6e0d5b34
// last-edited: 2026-09-02

package dedup

import (
	"math/rand"
	"testing"
)

// TestLevenshteinDistanceIsMeasuredInRunes pins the unit that
// normalizedLevenshteinSimilarity divides by.
//
// This package used to carry a BYTE-indexed Levenshtein while
// normalizedLevenshteinSimilarity divided its result by a RUNE length. A
// multi-byte character therefore cost 2-3 edits instead of 1, the quotient
// could exceed 1.0, and the resulting negative similarity was hidden by an
// `if sim < 0 { sim = 0 }` clamp -- so a pair of near-identical CJK or Cyrillic
// names silently scored as MAXIMALLY different.
//
// The property below is what actually catches that class of mistake: rune edit
// distance can never exceed the longer string's rune length, so if the two
// units ever disagree again this fails immediately. Verified to do so: with
// []rune swapped for []byte in matcher.LevenshteinDistanceCaseSensitive it
// reports d("東東é","Д")=8 > maxRuneLen=3.
func TestLevenshteinDistanceIsMeasuredInRunes(t *testing.T) {
	// Mixed scripts on purpose: an ASCII-only alphabet cannot observe the bug,
	// which is exactly why the original table test passed for so long.
	alphabets := [][]rune{
		[]rune("abc"),
		[]rune("aé東"),
		[]rune("Достй"),
		{}, // the empty-string edges
	}
	r := rand.New(rand.NewSource(1))
	for range 200000 {
		mk := func() string {
			al := alphabets[r.Intn(len(alphabets))]
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
		maxLen := len([]rune(a))
		if lb := len([]rune(b)); lb > maxLen {
			maxLen = lb
		}
		if d := levenshteinDistance(a, b); d > maxLen {
			t.Fatalf("distance is not in runes: d(%q,%q)=%d > maxRuneLen=%d", a, b, d, maxLen)
		}
		// The direct consequence, and the reason the clamp is gone. Redundant
		// with the check above for the distance-INFLATION case (that Fatalf
		// preempts this one), but it is live logic and states the property the
		// clamp used to hide, so it stays.
		if s := normalizedLevenshteinSimilarity(a, b); s < 0 || s > 1 {
			t.Fatalf("similarity %f out of [0,1] for (%q,%q)", s, a, b)
		}
	}
}

// TestNormalizedSimilarityOnNonASCII pins EXACT values for the pairs the
// byte-indexed implementation got wrong. The property test above proves the
// unit is self-consistent; these prove it is also correct.
func TestNormalizedSimilarityOnNonASCII(t *testing.T) {
	cases := []struct {
		a, b string
		want float64 // was, under byte indexing
	}{
		{"José Saramago", "Jose Saramago", 1 - 1.0/13}, // 0.846
		{"Böll", "Boll", 0.75},                         // 0.500
		{"Émile Zola", "Emile Zola", 0.90},             // 0.800
		{"Достоевский", "Достоевскiй", 1 - 1.0/11},     // 0.818
		{"村上春樹", "村上春树", 0.75},                         // 0.500
		{"東京", "東京都", 1 - 1.0/3},                       // 0.000 (byte d=3, maxLen=3)
	}
	for _, c := range cases {
		got := normalizedLevenshteinSimilarity(c.a, c.b)
		if diff := got - c.want; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("normalizedLevenshteinSimilarity(%q, %q) = %.6f, want %.6f", c.a, c.b, got, c.want)
		}
	}
}

// BenchmarkNormalizeAuthorName exists so the figures quoted for the regex
// hoist are REPRODUCIBLE rather than ad-hoc. NormalizeAuthorName runs twice per
// candidate pair in the pairwise metadata-fuzzy scan.
//
// Measured on darwin/arm64, -benchtime=2s -count=3, before the hoist (two
// regexp.MustCompile calls in the function body) vs after:
//
//	before  7976 ns/op  3708 B/op  43 allocs/op
//	after   1608 ns/op   181 B/op  10 allocs/op
func BenchmarkNormalizeAuthorName(b *testing.B) {
	names := []string{"J.R.R. Tolkien", "& Conrad Westmaas", "Ursula K. Le Guin", "José Saramago"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		NormalizeAuthorName(names[i%len(names)])
	}
}
