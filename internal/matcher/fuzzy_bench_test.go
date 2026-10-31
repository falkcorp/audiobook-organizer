// file: internal/matcher/fuzzy_bench_test.go
// version: 1.1.0
// guid: 1c64c110-695f-4d52-a68a-205265a1d482
// last-edited: 2026-10-30

package matcher

import "testing"

// Pairs are Batman storyline titles of realistic length; the second of each is
// a near-miss so the DP runs the full matrix rather than an early-out.
var benchLevPairs = [][2]string{
	{"Batman: The Long Halloween", "Batman - The Long Hallowe'en Deluxe"},
	{"Batman Year One", "Batman: Year Two"},
	{"Batman: A Death in the Family", "Batman Death of the Family"},
	{"Batman: Under the Red Hood", "Batman: Under the Red Hoods"},
}

// The first call on a fresh pool allocates its buffers, which a short run
// (-benchtime 2x) would average into allocs/op; warm the pool and reset the
// counters so the figure is the steady-state per-call cost.

func BenchmarkLevenshteinDistance(b *testing.B) {
	for _, p := range benchLevPairs {
		_ = LevenshteinDistance(p[0], p[1])
	}
	b.ReportAllocs()
	b.ResetTimer()
	n := 0
	for b.Loop() {
		p := benchLevPairs[n%len(benchLevPairs)]
		n++
		_ = LevenshteinDistance(p[0], p[1])
	}
}

func BenchmarkLevenshteinDistanceCaseSensitive(b *testing.B) {
	for _, p := range benchLevPairs {
		_ = LevenshteinDistanceCaseSensitive(p[0], p[1])
	}
	b.ReportAllocs()
	b.ResetTimer()
	n := 0
	for b.Loop() {
		p := benchLevPairs[n%len(benchLevPairs)]
		n++
		_ = LevenshteinDistanceCaseSensitive(p[0], p[1])
	}
}
