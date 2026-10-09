// file: internal/audiobooks/filter_compiled_bench_test.go
// version: 1.0.0
// guid: 8d90d532-6d88-49e0-b5b8-a4bba9dc1ace
// last-edited: 2026-10-09

package audiobooks

import (
	"context"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// BenchmarkCompiledPredicate_100k measures the production field-filter
// predicate: filters compiled ONCE per request, then matchesCompiledFilters
// per row over 100k synthetic books with the runtime index warm. Compare the
// owner-query sub-benchmark with BenchmarkOwnerQuery_100k_ConveniencePath,
// which recompiles per row. Synthetic rows only.
//
//	go test -run '^$' -bench CompiledPredicate_100k -benchtime 5x -benchmem ./internal/audiobooks/
func BenchmarkCompiledPredicate_100k(b *testing.B) {
	books, files := syntheticLibrary(100_000)
	// syntheticLibrary sets only ID, Duration and MetadataReviewStatus; give
	// the title and library_state filter sets something to match.
	for i := range books {
		if i%3 == 0 {
			books[i].Title = fmt.Sprintf("%d Title", i)
		}
		if i%2 == 0 {
			books[i].LibraryState = strp("organized")
		}
	}
	ri := newRuntimeIndex()
	if !ri.ensure(files) {
		b.Fatal("index build failed")
	}
	sets := []struct {
		name      string
		filters   []FieldFilter
		wantMatch bool
	}{
		{"title-regex", []FieldFilter{{Field: "title", Value: `/^\s*\d/`}}, false},
		{"duration-gt-20m", []FieldFilter{{Field: "duration", Value: ">20m"}}, true},
		{"owner-query", []FieldFilter{{Field: "metadata", Value: "applied", Negated: true}, {Field: "duration", Value: ">20m"}}, true},
		{"library-state-organized", []FieldFilter{{Field: "library_state", Value: "organized"}}, false},
	}
	for _, s := range sets {
		b.Run(s.name, func(b *testing.B) {
			cfs, err := compileFieldFilters(s.filters)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			n := 0
			for i := 0; i < b.N; i++ {
				n = 0
				for j := range books {
					if matchesCompiledFilters(books[j], cfs, ri.runtimeOf) {
						n++
					}
				}
				if s.wantMatch && n == 0 {
					b.Fatal("no matches")
				}
			}
			b.ReportMetric(float64(n), "matches")
		})
	}
}

// BenchmarkHeavyPushdownWalk_40kPrimary times CountAudiobooksFiltered (the
// handler's total) over a Pebble store of 100k synthetic books, 40k of them
// primary: the heavy-pushdown branch (primary + not quarantined + library
// state) and primary-only. The fixture build is slow and sits before the
// timer. Synthetic rows only.
//
//	go test -run '^$' -bench HeavyPushdownWalk_40kPrimary -benchtime 5x -benchmem ./internal/audiobooks/
func BenchmarkHeavyPushdownWalk_40kPrimary(b *testing.B) {
	const books = 100_000
	ps, err := database.NewPebbleStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()
	for i := range books {
		primary := i%5 < 2
		state := []string{"organized", "imported"}[i%2]
		if _, err := ps.CreateBook(&database.Book{Title: fmt.Sprintf("Title %06d", i), FilePath: fmt.Sprintf("/synthetic/Title %06d.m4b", i), IsPrimaryVersion: &primary, LibraryState: &state}); err != nil {
			b.Fatal(err)
		}
	}
	svc := NewAudiobookService(ps)
	ctx := context.Background()
	yes := true
	for _, c := range []struct {
		name string
		f    ListFilters
	}{
		{"primary+organized", ListFilters{IsPrimaryVersion: &yes, ExcludeQuarantined: true, LibraryState: "organized"}},
		{"primary-only", ListFilters{IsPrimaryVersion: &yes, ExcludeQuarantined: true}},
	} {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			n := 0
			for i := 0; i < b.N; i++ {
				if n, err = svc.CountAudiobooksFiltered(ctx, c.f); err != nil {
					b.Fatal(err)
				}
				if n == 0 {
					b.Fatal("no matches")
				}
			}
			b.ReportMetric(float64(n), "matches")
		})
	}
}
