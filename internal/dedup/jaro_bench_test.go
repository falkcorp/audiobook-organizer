// file: internal/dedup/jaro_bench_test.go
// version: 1.1.0
// guid: 98dd7ffc-fc19-4e12-9ac2-e9b1677e92b2
// last-edited: 2026-10-30

package dedup

import "testing"

var benchJWPairs = [][2]string{
	{"Wayne", "Waynes"},
	{"Gordon", "Gordan"},
	{"Grayson", "Graysen"},
	{"Cobblepot", "Cobblepott"},
	{"Quinzel", "Quinsel"},
}

func BenchmarkJaroWinklerSimilarity(b *testing.B) {
	// Warm the scratch pool: its first use allocates the buffers, which a short
	// run (-benchtime 2x) would average into allocs/op.
	for _, p := range benchJWPairs {
		_ = jaroWinklerSimilarity(p[0], p[1])
	}
	b.ReportAllocs()
	b.ResetTimer()
	n := 0
	for b.Loop() {
		p := benchJWPairs[n%len(benchJWPairs)]
		n++
		_ = jaroWinklerSimilarity(p[0], p[1])
	}
}
