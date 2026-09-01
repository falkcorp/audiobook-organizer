// file: internal/metadata/folder_parser_placeholder_test.go
// version: 1.0.0
// guid: 6d9c4a13-72e8-4b05-8f61-2a37e0b95cd4
// last-edited: 2026-09-01

package metadata

import (
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
)

// TestFolderParserNeverReturnsThePlaceholderAsAnAuthor covers the THIRD
// path->author parser.
//
// internal/authorname exists to stop the organizer's own placeholder directory
// being read back as a real author. That guard was added to the two parsers now
// collapsed into internal/authorname -- and this one, in the same package as one
// of them, was missed. Three copies, not two.
//
// The paths below are the layout the organizer ITSELF writes,
// <root>/<author>/<title>/, so this is not a hypothetical shape: every book the
// organizer files without a resolvable author lands under one of them.
//
// WHY IT MATTERED, traced rather than assumed:
//
//	folder_parser  -> Authors = ["Unknown Author"], AuthorConf = ConfidenceHigh
//	scanner.go     -> book.Author = bm.PrimaryAuthor()
//	scanner.go     -> recovery guard is `Author == ""`, so a NON-EMPTY
//	                  placeholder SKIPS the guard whose defer would clear it
//	scanner.go     -> resolveAuthorID creates or attaches a real
//	                  "Unknown Author" author row
//
// The book still reaches AI nomination (placeholderAuthors recognises the row),
// so this is not permanent loss -- it mints author rows and writes the
// placeholder as book.Author.
func TestFolderParserNeverReturnsThePlaceholderAsAnAuthor(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{
			"organizer layout with a series-decorated title",
			"/books/" + authorname.Placeholder + "/(Discworld 04) Mort/Mort - read by Nigel Planer",
		},
		{
			"organizer layout, plain title",
			"/books/" + authorname.Placeholder + "/Mort/Disc 1",
		},
		{
			"lowercased by a filesystem round trip",
			"/books/" + strings.ToLower(authorname.Placeholder) + "/Mort/Disc 1",
		},
		{
			"deep real-world prefix",
			"/mnt/bigdata/books/" + authorname.Placeholder + "/(Long Earth 05) The Long Cosmos/The Long Cosmos - read by X",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fm, err := ExtractMetadataFromFolder(tc.path)
			if err != nil {
				t.Fatalf("ExtractMetadataFromFolder(%q) error = %v", tc.path, err)
			}
			for _, a := range fm.Authors {
				if authorname.IsPlaceholder(a) {
					t.Errorf("ExtractMetadataFromFolder(%q) returned the placeholder as an author "+
						"(Authors=%v, conf=%d).\nscanner.go assigns this to book.Author, whose recovery "+
						"guard is `Author == \"\"` -- so a non-empty placeholder skips the guard that "+
						"would have cleared it, and resolveAuthorID then mints an \"Unknown Author\" row",
						tc.path, fm.Authors, fm.AuthorConf)
				}
			}
		})
	}
}

// TestFolderParserStillReadsARealAuthor is the known-good twin.
//
// The test above only ever asserts that something is ABSENT, so on its own it
// passes just as happily against a parser broken to return no authors at all --
// which is precisely what over-broad skip-map entries would cause. This one
// fails in that case.
func TestFolderParserStillReadsARealAuthor(t *testing.T) {
	const path = "/books/Terry Pratchett/(Discworld 04) Mort/Mort - read by Nigel Planer"

	fm, err := ExtractMetadataFromFolder(path)
	if err != nil {
		t.Fatalf("ExtractMetadataFromFolder(%q) error = %v", path, err)
	}
	found := false
	for _, a := range fm.Authors {
		if a == "Terry Pratchett" {
			found = true
		}
	}
	if !found {
		t.Errorf("ExtractMetadataFromFolder(%q) Authors = %v, want it to contain %q -- "+
			"the parser is not reaching real authors, which would make the placeholder test above vacuous",
			path, fm.Authors, "Terry Pratchett")
	}
}
