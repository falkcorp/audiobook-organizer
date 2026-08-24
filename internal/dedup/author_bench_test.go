// file: internal/dedup/author_bench_test.go
// version: 1.0.0
// guid: c0d4e918-6b57-42fa-8e13-9a72b5f04c8d
// last-edited: 2026-08-24

package dedup

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// benchAuthorCorpus generates an author set shaped like the production library:
// ~14,400 authors resolving to ~7,200 distinct last names, with surname lengths
// following the real distribution (measured 2026-08-24: mode 6 runes, tail out
// past 14). Phase 3's cost is quadratic in the DISTINCT last-name count, so
// reproducing that count is what makes this benchmark representative.
func benchAuthorCorpus(distinctLastNames int) []database.Author {
	rng := rand.New(rand.NewSource(20260824))
	letters := []rune("abcdefghijklmnopqrstuvwxyz")

	// Surname length distribution measured on the production corpus.
	lengthWeights := []struct {
		length, weight int
	}{
		{3, 295}, {4, 666}, {5, 1058}, {6, 1304}, {7, 1150},
		{8, 702}, {9, 560}, {10, 251}, {11, 149}, {12, 188}, {13, 65}, {14, 167},
	}
	total := 0
	for _, lw := range lengthWeights {
		total += lw.weight
	}
	pickLength := func() int {
		r := rng.Intn(total)
		for _, lw := range lengthWeights {
			if r < lw.weight {
				return lw.length
			}
			r -= lw.weight
		}
		return 6
	}

	seen := make(map[string]bool, distinctLastNames)
	lasts := make([]string, 0, distinctLastNames)
	for len(lasts) < distinctLastNames {
		n := pickLength()
		r := make([]rune, n)
		for i := range r {
			r[i] = letters[rng.Intn(len(letters))]
		}
		s := string(r)
		if seen[s] {
			continue
		}
		seen[s] = true
		lasts = append(lasts, s)
	}

	firsts := []string{"john", "maria", "erik", "anna", "lars", "sofia", "peter", "elena"}
	authors := make([]database.Author, 0, distinctLastNames*2)
	id := 1
	for i, last := range lasts {
		// Two authors per surname on average, matching 14,435 / 7,261.
		reps := 2
		if i%3 == 0 {
			reps = 1
		}
		for k := 0; k < reps; k++ {
			authors = append(authors, database.Author{
				ID:   id,
				Name: fmt.Sprintf("%s %s", firsts[id%len(firsts)], last),
			})
			id++
		}
	}
	return authors
}

// BenchmarkFindDuplicateAuthorsProdScale measures the whole grouping pass at
// the production library's shape. Phase 3 dominates it: 7,261 distinct last
// names means 26,357,430 candidate pairs.
func BenchmarkFindDuplicateAuthorsProdScale(b *testing.B) {
	authors := benchAuthorCorpus(7261)
	bookCount := func(id int) int { return id%7 + 1 }
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		FindDuplicateAuthors(authors, 0.85, bookCount)
	}
}

// BenchmarkJaroWinklerPrefilterVsFull isolates the per-pair saving the length
// screen buys, independent of how many cores the scan runs on.
func BenchmarkJaroWinklerPrefilterVsFull(b *testing.B) {
	pairs := [][2]string{
		{"anderson", "bakhtiyarov"}, // very different lengths: prefilter wins
		{"lee", "kowalczyk"},
		{"smith", "smyth"}, // similar lengths: prefilter cannot help
		{"andersen", "andersson"},
	}
	b.Run("full", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			p := pairs[i%len(pairs)]
			_ = jaroWinklerSimilarity(p[0], p[1])
		}
	})
	b.Run("prefilter_then_full", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			p := pairs[i%len(pairs)]
			if jaroWinklerBelowThreshold(p[0], p[1], 0.95) {
				continue
			}
			_ = jaroWinklerSimilarity(p[0], p[1])
		}
	})
}
