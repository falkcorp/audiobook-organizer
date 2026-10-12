// file: internal/audiobooks/author_filter_credits_bench_test.go
// version: 1.0.0
// guid: c53e5018-1c53-4ecd-82fc-426c49954ae4
// last-edited: 2026-10-12

package audiobooks

import (
	"context"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// BenchmarkAuthorFilterCredits_10k times the per-query credit load on its own
// (credit-load), and one author: query end to end (CountAudiobooksFiltered:
// the load plus the per-book match), over a Pebble store of 10k synthetic
// books. 75% carry only their primary credit, 20% a primary plus a co-author,
// 5% a primary, a co-author and a narrator-role row. Synthetic rows only; the
// fixture build sits before the timer.
//
//	go test -run '^$' -bench AuthorFilterCredits_10k -benchtime 20x -benchmem ./internal/audiobooks/
func BenchmarkAuthorFilterCredits_10k(b *testing.B) {
	const books = 10_000
	ps, err := database.NewPebbleStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()

	const people = 400
	ids := make([]int, people)
	for i := range ids {
		a, err := ps.CreateAuthor(fmt.Sprintf("Benchperson %04d", i))
		if err != nil {
			b.Fatal(err)
		}
		ids[i] = a.ID
	}
	for i := range books {
		primary := ids[i%people]
		bk, err := ps.CreateBook(&database.Book{Title: fmt.Sprintf("Benchtitle %05d", i), AuthorID: &primary})
		if err != nil {
			b.Fatal(err)
		}
		rows := []database.BookAuthor{{AuthorID: primary, Role: "author", Position: 0}}
		switch r := i % 20; {
		case r < 4:
			rows = append(rows, database.BookAuthor{AuthorID: ids[(i+7)%people], Role: "co-author", Position: 1})
		case r == 4:
			rows = append(rows,
				database.BookAuthor{AuthorID: ids[(i+7)%people], Role: "co-author", Position: 1},
				database.BookAuthor{AuthorID: ids[(i+13)%people], Role: "narrator", Position: 2})
		}
		if err := ps.SetBookAuthors(bk.ID, rows); err != nil {
			b.Fatal(err)
		}
	}
	svc := NewAudiobookService(ps)
	ctx := context.Background()
	// The per-query credit load alone (buildAuthorSeriesNameMaps, which also
	// loads the author names), without the per-book walk.
	b.Run("credit-load", func(b *testing.B) {
		ff := []FieldFilter{{Field: "author", Value: "Benchperson"}}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, _, credits := svc.buildAuthorSeriesNameMaps(ff); credits == nil {
				b.Fatal("no credit lookup")
			}
		}
	})
	for _, c := range []struct {
		name string
		ff   FieldFilter
	}{
		// Matches one person's books, as primary and as co-author.
		{"one-person", FieldFilter{Field: "author", Value: "Benchperson 0007"}},
		// Matches every book: the per-book cost with no early rejection.
		{"everyone", FieldFilter{Field: "author", Value: "Benchperson"}},
	} {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			n := 0
			for i := 0; i < b.N; i++ {
				if n, err = svc.CountAudiobooksFiltered(ctx, ListFilters{FieldFilters: []FieldFilter{c.ff}}); err != nil {
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
