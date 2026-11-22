// file: internal/applygate/titlecompare_bench_test.go
// version: 1.3.0
// guid: 3b7d9e14-6a2c-4f58-b0e1-95c8d4a7e362
// last-edited: 2026-11-18

package applygate

import (
	"strconv"
	"strings"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// BenchmarkCheckTitle prices one title check per outcome shape. Bulk apply runs
// the check once per candidate, and the generated property tests run it tens of
// thousands of times, so its per-call cost and allocation count are what decide
// how long this package takes under -race (every CI run). Run it with
//
//	go test ./internal/applygate -run '^$' -bench CheckTitle -benchmem
//
// and again with -race, where whatever a check builds per call that sync.Pool
// would have reused is built again. The tokenizer and segment-splitter regexes
// and the per-call strings.Replacer construction are gone from the check; the
// year expressions (yearAnyRe, yearTagRe) and seqnum's guarded readers still run
// on every call, so the benchmark is the place to see what they cost under -race.
func BenchmarkCheckTitle(b *testing.B) {
	series := func(n string) *database.Series { return &database.Series{Name: n} }
	sub := func(s string) *string { return &s }
	for _, c := range []struct {
		name string
		book database.Book
		cand metafetch.MetadataCandidate
	}{
		{"agree whole title",
			database.Book{Title: "Knightfall"},
			metafetch.MetadataCandidate{Title: "Knightfall"}},
		{"agree behind the series name",
			database.Book{Title: "Hush", Subtitle: sub("Part 1"), Series: series("Batman"), FilePath: "/lib/Bruce Wayne/Hush/Hush.m4b"},
			metafetch.MetadataCandidate{Title: "Hush", Subtitle: "Part 1", Series: "Batman", SeriesPosition: "5", Narrator: "Dick Grayson"}},
		{"conflicting volume",
			database.Book{Title: "Hush 1"},
			metafetch.MetadataCandidate{Title: "Hush 2"}},
		{"conflicting segment",
			database.Book{Title: "Batman: Dark Victory"},
			metafetch.MetadataCandidate{Title: "Batman: The Long Halloween"}},
		{"neutral added subtitle",
			database.Book{Title: "Year One", Series: series("Batman")},
			metafetch.MetadataCandidate{Title: "Year One", Subtitle: "Gotham Noir", Series: "Batman", SeriesPosition: "1", Narrator: "Dick Grayson"}},
		{"unrelated titles",
			database.Book{Title: "Knightfall"},
			metafetch.MetadataCandidate{Title: "No Man's Land", Series: "Batman", SeriesPosition: "3"}},
		{"self-titled candidate",
			database.Book{Title: "Gotham Central", Series: series("Gotham Central")},
			metafetch.MetadataCandidate{Title: "Gotham Central", Series: "Gotham Central", SeriesPosition: "2", Narrator: "Dick Grayson"}},
	} {
		book, cand := c.book, c.cand
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				runTitleCheck(book, cand)
			}
		})
	}
}

// BenchmarkCheckTitle_Length runs one agreeing and one conflicting check over
// titles of 4 to 256 words. The check is linear in the length of the title (it
// tokenises each field a fixed number of times); a super-linear step shows here
// as a per-word cost that grows with the length.
func BenchmarkCheckTitle_Length(b *testing.B) {
	title := func(words int, tail string) string {
		parts := make([]string, 0, words+1)
		for i := range words {
			parts = append(parts, "Gotham"+strconv.Itoa(i%7))
		}
		return strings.Join(parts, " ") + tail
	}
	for _, words := range []int{4, 16, 64, 256} {
		for _, c := range []struct{ name, tail string }{{"agree", ""}, {"conflict", " 2"}} {
			book := database.Book{Title: title(words, "")}
			cand := metafetch.MetadataCandidate{Title: title(words, c.tail)}
			b.Run(strconv.Itoa(words)+" words/"+c.name, func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					runTitleCheck(book, cand)
				}
			})
		}
	}
}
