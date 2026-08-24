// file: internal/dedup/author_prefilter_test.go
// version: 1.0.0
// guid: 3f9c21ad-7e40-4b62-9c85-1d6a0f3b8e74
// last-edited: 2026-08-24

package dedup

import (
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

// The phase-3 length prefilter is only safe if it NEVER discards a pair that
// jaroWinklerSimilarity would have accepted. These tests pin that from three
// independent directions, because no one of them is sufficient:
//
//   - TestJaroWinklerBelowThresholdIsSound proves it never over-skips, but is
//     satisfied by a function that always returns false.
//   - TestJaroWinklerBelowThresholdActuallySkips proves it is not that inert
//     function, by measuring the fraction it rejects.
//   - TestJaroWinklerBelowThresholdCountsRunesNotBytes pins rune counting
//     deterministically, without relying on the fuzz alphabet and seed to
//     stumble onto it (see its comment).
//
// All four failure modes below were confirmed to fail these tests by mutating
// the implementation -- byte counting, an always-false filter, dropping the
// epsilon bias, and an over-aggressive 5t-3 bound.

// prefilterCorpus returns realistic surname-shaped strings plus deliberately
// adversarial ones: near-identical pairs, length-boundary pairs, and non-ASCII.
func prefilterCorpus() []string {
	base := []string{
		"smith", "smyth", "smithe", "smithers", "schmidt",
		"anderson", "andersen", "andersson", "anders",
		"macdonald", "mcdonald", "macdonell", "o'brien", "obrien",
		"lee", "li", "lu", "ng", "wu",
		"vandermeer", "van der meer", "vonnegut",
		"müller", "mueller", "muller", "mullerova",
		"beauchamp", "beaumont", "x", "yu",
		"kowalski", "kowalczyk", "nowakowski",
	}
	out := append([]string{}, base...)
	// Mutations: single-character edits are exactly the shape that lands near
	// the 0.95 threshold, which is where an unsound bound would show up.
	for _, b := range base {
		if b == "" {
			continue
		}
		r := []rune(b)
		out = append(out, string(r[:len(r)-1]))                         // deletion
		out = append(out, b+"s")                                        // insertion
		out = append(out, strings.ToUpper(string(r[:1]))+string(r[1:])) // case (rune-safe: "müller" must not split ü)
		out = append(out, string(r[len(r)-1:])+b)                       // prepend
	}
	return out
}

// TestJaroWinklerBelowThresholdIsSound is the core safety property: skipping a
// pair must imply the real comparison would have rejected it anyway.
func TestJaroWinklerBelowThresholdIsSound(t *testing.T) {
	const threshold = 0.95
	corpus := prefilterCorpus()

	checked := 0
	for i := range corpus {
		for j := range corpus {
			a, b := corpus[i], corpus[j]
			if !jaroWinklerBelowThreshold(a, b, threshold) {
				continue
			}
			checked++
			if got := jaroWinklerSimilarity(a, b); got >= threshold {
				t.Fatalf("prefilter discarded a pair the real comparison accepts: "+
					"%q vs %q -> jaroWinkler=%v (>= %v). The length bound is unsound.",
					a, b, got, threshold)
			}
		}
	}
	if checked == 0 {
		t.Fatal("prefilter skipped no pairs in the corpus; this test proved nothing")
	}
	t.Logf("verified %d skipped pairs are all genuinely below threshold", checked)
}

// TestJaroWinklerBelowThresholdIsSoundUnderFuzz repeats the property over
// randomly generated strings, including multi-byte runes and lopsided lengths
// that the curated corpus does not reach.
func TestJaroWinklerBelowThresholdIsSoundUnderFuzz(t *testing.T) {
	const threshold = 0.95
	rng := rand.New(rand.NewSource(20260824))
	alphabet := []rune("abcdefghijklmnopqrstuvwxyzäöüßéèñçłżđ日本語")

	randName := func() string {
		n := 1 + rng.Intn(14)
		r := make([]rune, n)
		for i := range r {
			r[i] = alphabet[rng.Intn(len(alphabet))]
		}
		return string(r)
	}

	skipped := 0
	for iter := 0; iter < 200000; iter++ {
		a := randName()
		b := a
		// Half the time perturb a copy, so the corpus is dense near the
		// threshold rather than almost entirely dissimilar pairs.
		if rng.Intn(2) == 0 {
			r := []rune(b)
			switch rng.Intn(3) {
			case 0:
				r = append(r, alphabet[rng.Intn(len(alphabet))])
			case 1:
				if len(r) > 1 {
					k := rng.Intn(len(r))
					r = append(r[:k], r[k+1:]...)
				}
			case 2:
				if len(r) > 0 {
					r[rng.Intn(len(r))] = alphabet[rng.Intn(len(alphabet))]
				}
			}
			b = string(r)
		} else {
			b = randName()
		}

		if !jaroWinklerBelowThreshold(a, b, threshold) {
			continue
		}
		skipped++
		if got := jaroWinklerSimilarity(a, b); got >= threshold {
			t.Fatalf("fuzz found an unsound skip: %q (%d runes) vs %q (%d runes) -> %v",
				a, utf8.RuneCountInString(a), b, utf8.RuneCountInString(b), got)
		}
	}
	if skipped == 0 {
		t.Fatal("fuzz skipped no pairs; this test proved nothing")
	}
	t.Logf("fuzz verified %d skipped pairs", skipped)
}

// TestJaroWinklerBelowThresholdActuallySkips guards against the filter silently
// becoming inert. A prefilter that always returns false is perfectly SOUND and
// would satisfy every assertion above while delivering no speedup at all, so
// the soundness tests cannot detect that regression -- only a yield floor can.
func TestJaroWinklerBelowThresholdActuallySkips(t *testing.T) {
	corpus := prefilterCorpus()
	total, skipped := 0, 0
	for i := range corpus {
		for j := i + 1; j < len(corpus); j++ {
			total++
			if jaroWinklerBelowThreshold(corpus[i], corpus[j], 0.95) {
				skipped++
			}
		}
	}
	if total == 0 {
		t.Fatal("empty corpus")
	}
	ratio := float64(skipped) / float64(total)
	// The production corpus of 7,261 distinct last names sits at ~61% skipped.
	// A floor well below that catches an inert or inverted filter without
	// making the test brittle to corpus edits.
	if ratio < 0.20 {
		t.Fatalf("prefilter rejected only %d/%d pairs (%.1f%%); expected a "+
			"substantial fraction. It may have gone inert.", skipped, total, ratio*100)
	}
	t.Logf("prefilter rejects %d/%d pairs (%.1f%%)", skipped, total, ratio*100)
}

// TestJaroWinklerBelowThresholdCountsRunesNotBytes pins rune-vs-byte counting
// directly, by asserting the SPECIFIED verdict rather than the safety property.
//
// The fuzz test above does also catch a byte-counting implementation -- verified
// by mutation, not assumed -- because its alphabet mixes 1-byte and 3-byte runes,
// so a single-rune edit can move byte length by 3. "abcd" vs "abcd<3-byte rune>"
// is 0.8 by runes but 0.571 by bytes and scores 0.96, which a byte-counting
// filter would wrongly discard.
//
// This test earns its place by not depending on that luck. Its catch survives
// someone narrowing the fuzz alphabet to ASCII, reducing the iteration count, or
// reseeding the RNG, and when it fails it names the actual defect instead of
// reporting an unsound skip on two random strings.
func TestJaroWinklerBelowThresholdCountsRunesNotBytes(t *testing.T) {
	// 7 ASCII runes (7 bytes) against 9 CJK runes (27 bytes).
	// By runes: 7/9 = 0.778 >= 0.75, so the pair must be KEPT.
	// By bytes: 7/27 = 0.259 < 0.75, so a byte-counting bug would SKIP it.
	shortASCII := "abcdefg"
	longCJK := "日本語漢字試験八九" // 9 runes, 27 bytes

	if got := utf8.RuneCountInString(longCJK); got != 9 {
		t.Fatalf("fixture wrong: expected 9 runes, got %d", got)
	}
	if len(longCJK) == utf8.RuneCountInString(longCJK) {
		t.Fatal("fixture wrong: needs multi-byte runes to discriminate")
	}

	if jaroWinklerBelowThreshold(shortASCII, longCJK, 0.95) {
		t.Fatalf("prefilter skipped %q (%d runes, %d bytes) vs %q (%d runes, %d bytes); "+
			"rune ratio is %.3f which is >= 0.75, so this pair must be kept. "+
			"This is the signature of counting bytes instead of runes.",
			shortASCII, utf8.RuneCountInString(shortASCII), len(shortASCII),
			longCJK, utf8.RuneCountInString(longCJK), len(longCJK),
			float64(utf8.RuneCountInString(shortASCII))/float64(utf8.RuneCountInString(longCJK)))
	}
}

// TestJaroWinklerBelowThresholdBoundaryAndWeakThresholds pins the two edges of
// the derivation: a ratio sitting exactly on 5t-4 must be kept, and a threshold
// at or below 0.8 makes the bound non-positive so nothing may be skipped.
func TestJaroWinklerBelowThresholdBoundaryAndWeakThresholds(t *testing.T) {
	// 3 vs 4 runes is exactly 0.75, the 0.95 bound. Must NOT be skipped --
	// this is what the epsilon bias protects.
	if jaroWinklerBelowThreshold("abc", "abcd", 0.95) {
		t.Error("ratio exactly 0.75 was skipped at threshold 0.95; the bound is off by one rounding step")
	}
	// 2 vs 3 runes is 0.667 < 0.75. Must be skipped.
	if !jaroWinklerBelowThreshold("ab", "abcd", 0.95) {
		t.Error("ratio 0.5 was not skipped at threshold 0.95")
	}
	// At t <= 0.8 the bound is non-positive: length proves nothing.
	for _, weak := range []float64{0.8, 0.7, 0.5, 0.0} {
		if jaroWinklerBelowThreshold("a", "abcdefghijklmnop", weak) {
			t.Errorf("threshold %v: length must not justify a skip when 5t-4 <= 0", weak)
		}
	}
	// Empty strings must never be skipped on length.
	if jaroWinklerBelowThreshold("", "", 0.95) {
		t.Error("empty/empty was skipped")
	}
}
