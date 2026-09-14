// file: internal/metadata/narrator_precedence_test.go
// version: 1.0.0
// guid: 2f7b9d41-6c3e-4a58-9e1d-0b5a8c7f3d62
// last-edited: 2026-09-14

package metadata

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/stretchr/testify/assert"
)

// Both file-tag readers take the explicit NARRATOR tag before PERFORMER, so a
// file whose two tags differ gets the same narrator from either reader.
func TestNarratorPrecedence_BothReadersPreferNarratorTag(t *testing.T) {
	log := logger.New("metadata-test")

	fromTag := BuildMetadataFromTag(&mockTagMetadata{
		title:  "Book",
		artist: "Author Name",
		raw: map[string]any{
			"ALBUMARTIST": "Author Name",
			"PERFORMER":   "Other Reader",
			"NARRATOR":    "Real Narrator",
		},
	}, "/books/book.m4b", log)
	assert.Equal(t, "Real Narrator", fromTag.Narrator, "BuildMetadataFromTag")

	fromTaglib := BuildMetadataFromTaglibMap(map[string][]string{
		"TITLE":       {"Book"},
		"ALBUMARTIST": {"Author Name"},
		"PERFORMER":   {"Other Reader"},
		"NARRATOR":    {"Real Narrator"},
	}, "/books/book.m4b", log)
	assert.Equal(t, "Real Narrator", fromTaglib.Narrator, "BuildMetadataFromTaglibMap")
}

// An author reading their own book with a co-author: ALBUMARTIST and NARRATOR
// both name the author and ARTIST names the co-author. ALBUMARTIST is still the
// author; nothing in the tags tells this apart from a narrator written into
// ALBUMARTIST, so there is no guard.
func TestAlbumArtistIsAuthor_WhenAuthorNarratesOwnBook(t *testing.T) {
	log := logger.New("metadata-test")
	fromTag := BuildMetadataFromTag(&mockTagMetadata{
		title:  "Book",
		artist: "Co Author",
		raw: map[string]any{
			"ALBUMARTIST": "Main Author",
			"NARRATOR":    "Main Author",
		},
	}, "/books/book.m4b", log)
	assert.Equal(t, "Main Author", fromTag.Artist)
	assert.Equal(t, "Main Author", fromTag.Narrator)

	fromTaglib := BuildMetadataFromTaglibMap(map[string][]string{
		"TITLE":       {"Book"},
		"ALBUMARTIST": {"Main Author"},
		"ARTIST":      {"Co Author"},
		"NARRATOR":    {"Main Author"},
	}, "/books/book.m4b", log)
	assert.Equal(t, "Main Author", fromTaglib.Artist)
	assert.Equal(t, "Main Author", fromTaglib.Narrator)
}
