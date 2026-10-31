// file: internal/database/metadata_bucket_bench_test.go
// version: 1.1.0
// guid: fa3ffdc0-5937-4635-8f29-eb38ef59bccd
// last-edited: 2026-10-30

package database

import (
	"fmt"
	"math/rand"
	"runtime"
	"strings"
	"testing"
)

// metadataBucketStorylines are Batman comic storylines used to synthesise
// bucket members. Every title built from them starts with "Batman", so all
// books of one author land in one (authorKey, titleToken) bucket.
var metadataBucketStorylines = []string{
	"Year One", "The Long Halloween", "Hush", "Knightfall", "Court of Owls",
	"Dark Victory", "The Killing Joke", "A Death in the Family", "Zero Year",
	"Night of the Owls", "Blind Justice", "No Man's Land", "Under the Red Hood",
	"The Black Mirror", "Haunted Knight", "Gothic", "The Cult", "Death of the Family",
}

// metadataBucketSuffixes widen the title space so scores span the threshold.
var metadataBucketSuffixes = []string{
	"", "", "Part 1", "Part 2", "Part 3", "Vol. 2", "Unabridged", "Deluxe Edition", "Book Three",
}

// syntheticMetadataBuckets returns buckets of qualifying entries: nBuckets
// buckets of perBucket members each. Each bucket belongs to its own author id
// (so the buckets are disjoint) and every title starts with "Batman", so the
// total stays below metadataFuzzyBucketCap per bucket and none is skipped.
func syntheticMetadataBuckets(nBuckets, perBucket int) []metadataDupEntry {
	rng := rand.New(rand.NewSource(20261030))
	entries := make([]metadataDupEntry, 0, nBuckets*perBucket)
	for b := 0; b < nBuckets; b++ {
		authorID := 1000 + b
		for i := 0; i < perBucket; i++ {
			parts := []string{"Batman", metadataBucketStorylines[rng.Intn(len(metadataBucketStorylines))]}
			if sfx := metadataBucketSuffixes[rng.Intn(len(metadataBucketSuffixes))]; sfx != "" {
				parts = append(parts, sfx)
			}
			title := strings.Join(parts, " ")
			switch rng.Intn(4) {
			case 0:
				title = strings.ToUpper(title)
			case 1:
				title = strings.ToLower(title) + "."
			case 2:
				title = "The " + title
			}
			id := authorID
			e, ok := newMetadataDupEntry(BookCore{
				ID:       fmt.Sprintf("bm-%d-%d", b, i),
				Title:    title,
				AuthorID: &id,
			})
			if !ok {
				panic("synthetic title unexpectedly empty")
			}
			entries = append(entries, e)
		}
	}
	return entries
}

// BenchmarkGroupMetadataBucket measures one cap-sized (200 member) bucket:
// the 19,900-pair quadratic loop that is the unit of work.
func BenchmarkGroupMetadataBucket(b *testing.B) {
	bucket := syntheticMetadataBuckets(1, metadataFuzzyBucketCap)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = groupMetadataBucket(bucket, 0.85)
	}
}

// BenchmarkBucketMetadataDuplicates2000 measures the whole library pass over a
// synthetic 2,000-item set (20 disjoint buckets of 100), once with the bucket
// pool pinned to a single worker (the serial cost) and once at NumCPU.
func BenchmarkBucketMetadataDuplicates2000(b *testing.B) {
	entries := syntheticMetadataBuckets(20, 100)
	for _, workers := range []int{1, runtime.NumCPU()} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			defer func(old int) { metadataBucketWorkers = old }(metadataBucketWorkers)
			metadataBucketWorkers = workers
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				_ = bucketMetadataDuplicates(entries, 0.85)
			}
		})
	}
}
