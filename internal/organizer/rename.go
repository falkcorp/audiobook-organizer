// file: internal/organizer/rename.go
// version: 2.0.0
// guid: e5f6a7b8-c9d0-e1f2-a3b4-c5d6e7f8a9b0
// last-edited: 2026-10-10

package organizer

import (
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The rename service, its preview and apply entry points and the file-move
// helpers only they used were deleted with the /audiobooks/:id/rename/*
// routes (owner decision D13): organize and write-back cover renames. What
// remains is the tag-metadata and narrator helpers the organize preview
// still builds on.

// resolveNarratorNames resolves narrator names from the database.
func resolveNarratorNames(db Store, bookID string, book *database.Book) string {
	var narratorNames []string
	bookNarrators, err := db.GetBookNarrators(bookID)
	if err == nil && len(bookNarrators) > 0 {
		for _, bn := range bookNarrators {
			if narrator, nerr := db.GetNarratorByID(bn.NarratorID); nerr == nil && narrator != nil {
				narratorNames = append(narratorNames, narrator.Name)
			}
		}
	} else if book.Narrator != nil && *book.Narrator != "" {
		narratorNames = append(narratorNames, *book.Narrator)
	}
	return strings.Join(narratorNames, " & ")
}

// BuildTagMetadata constructs the metadata map for WriteMetadataToFile.
func BuildTagMetadata(book *database.Book, authorName, narratorStr string) map[string]any {
	meta := map[string]any{
		"title": book.Title,
		"album": book.Title,
		"genre": "Audiobook",
	}
	// The artist key is written to ARTIST and ALBUMARTIST
	// (metadata.WriteTagProperties). Album Artist is the author (owner
	// decision 2026-09-14), and every reader takes ALBUMARTIST first, so an
	// organize that left a stale ALBUMARTIST had the next scan read the old
	// author back. That reverses the ALBUMARTIST half of c81b39801, which
	// wrote ARTIST only.
	if authorName != "" {
		meta["artist"] = authorName
	}
	// The narrator goes to NARRATOR and PERFORMER only. Never ALBUMARTIST or
	// COMPOSER: the file reader takes ALBUMARTIST first as the book's author,
	// so a narrator there is read back as the author on the next scan, and
	// COMPOSER may hold a value the owner set.
	if narratorStr != "" {
		meta["narrator"] = narratorStr
	}
	year := 0
	if book.AudiobookReleaseYear != nil && *book.AudiobookReleaseYear > 0 {
		year = *book.AudiobookReleaseYear
	} else if book.PrintYear != nil && *book.PrintYear > 0 {
		year = *book.PrintYear
	}
	if year > 0 {
		meta["year"] = fmt.Sprintf("%d", year)
	}
	return meta
}
