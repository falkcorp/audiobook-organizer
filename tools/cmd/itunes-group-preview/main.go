// file: tools/cmd/itunes-group-preview/main.go
// version: 1.0.0
// guid: 6f7a8b9c-0d1e-2f3a-4b5c-6d7e8f9a0b1c
// last-edited: 2026-06-20

// Command itunes-group-preview parses an iTunes library XML and reports how many
// BOOKS the (fixed) importer grouping would create from it — a DB-free dry-run
// of the "delete + re-import" heal (Option B). It touches nothing.
//
//	go run ./tools/cmd/itunes-group-preview -xml /path/to/iTunes\ Library.xml [-sample 25]
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
)

func main() {
	xmlPath := flag.String("xml", "", "path to iTunes Library.xml")
	sample := flag.Int("sample", 25, "number of largest multi-file books to print")
	flag.Parse()
	if *xmlPath == "" {
		fmt.Fprintln(os.Stderr, "usage: itunes-group-preview -xml <path>")
		os.Exit(2)
	}

	lib, err := itunes.ParseLibrary(*xmlPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse: %v\n", err)
		os.Exit(1)
	}

	var audiobookTracks int
	for _, t := range lib.Tracks {
		if itunes.IsAudiobook(t) {
			audiobookTracks++
		}
	}

	p := itunesservice.PreviewGroups(lib)

	fmt.Printf("=== Option B dry-run: fixed importer re-grouping of %q ===\n", *xmlPath)
	fmt.Printf("audiobook tracks (current per-file records today): %d\n", audiobookTracks)
	fmt.Printf("books the FIXED importer would create:            %d\n", p.TotalGroups)
	fmt.Printf("  multi-file books (>=2 tracks):  %d\n", p.MultiFile)
	fmt.Printf("  single-file books:              %d\n", p.SingleFile)
	if audiobookTracks > 0 {
		fmt.Printf("net record reduction:             %d -> %d  (%.1f%% fewer records)\n",
			audiobookTracks, p.TotalGroups, 100*float64(audiobookTracks-p.TotalGroups)/float64(audiobookTracks))
	}

	books := append([]itunesservice.PreviewBook(nil), p.Books...)
	sort.Slice(books, func(i, j int) bool { return books[i].NumTracks > books[j].NumTracks })
	fmt.Printf("\ntop %d multi-file books (tracks merged into one book):\n", *sample)
	for i := 0; i < *sample && i < len(books); i++ {
		b := books[i]
		fmt.Printf("  %4d tracks  %-28.28s  %q\n", b.NumTracks, b.Artist, b.Title)
	}

	// Distribution of book sizes.
	buckets := map[string]int{}
	for _, b := range books {
		switch {
		case b.NumTracks == 1:
			buckets["1"]++
		case b.NumTracks <= 5:
			buckets["2-5"]++
		case b.NumTracks <= 20:
			buckets["6-20"]++
		case b.NumTracks <= 100:
			buckets["21-100"]++
		default:
			buckets["100+"]++
		}
	}
	fmt.Printf("\nbook-size distribution: 1=%d  2-5=%d  6-20=%d  21-100=%d  100+=%d\n",
		buckets["1"], buckets["2-5"], buckets["6-20"], buckets["21-100"], buckets["100+"])
}
