// file: internal/audiobooks/service_tag_facets_bench_test.go
// version: 1.0.0
// guid: 9b1c5e7a-3f2d-4a68-8c94-e0d6b2f8a417
// last-edited: 2026-10-06

package audiobooks

import (
	"context"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// BenchmarkScopedTagFacets_Cold measures one UNCACHED scoped-facets
// computation (no result cache wired) over a synthetic library: the default
// Library view (primary only) and a narrowing filter. Synthetic rows only.
//
//	go test -run '^$' -bench ScopedTagFacets -benchtime 5x ./internal/audiobooks/
func BenchmarkScopedTagFacets_Cold(b *testing.B) {
	const books, tagsPerBook, tagPool = 5000, 6, 400
	ps, err := database.NewPebbleStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()
	for i := range books {
		primary := i%5 != 0
		state := []string{"organized", "imported"}[i%2]
		bk, err := ps.CreateBook(&database.Book{Title: fmt.Sprintf("synthetic %d", i), IsPrimaryVersion: &primary, LibraryState: &state})
		if err != nil {
			b.Fatal(err)
		}
		for j := range tagsPerBook {
			if err := ps.AddBookTag(bk.ID, fmt.Sprintf("tag-%d", (i*7+j*31)%tagPool)); err != nil {
				b.Fatal(err)
			}
		}
	}
	svc := NewAudiobookService(ps)
	yes := true
	for name, f := range map[string]ListFilters{
		"primary-only":       {IsPrimaryVersion: &yes, ExcludeQuarantined: true},
		"primary+organized":  {IsPrimaryVersion: &yes, ExcludeQuarantined: true, LibraryState: "organized"},
		"primary+tag-narrow": {IsPrimaryVersion: &yes, ExcludeQuarantined: true, Tags: []string{"tag-7"}},
	} {
		b.Run(name, func(b *testing.B) {
			for b.Loop() {
				if _, err := svc.ScopedTagFacets(context.Background(), "", nil, nil, f); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
