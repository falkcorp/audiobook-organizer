// file: internal/scanner/unknown_author_gate_test.go
// version: 1.0.0
// guid: 3d9a5f71-2e84-4c06-b1f3-6a05e97c2db4
// last-edited: 2026-08-25

package scanner

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/stretchr/testify/require"
)

// TestExtractInfoFromPathDoesNotLaunderThePlaceholder uses the exact shape of a
// real production path, because the loop only closes on organized files: the
// organizer writes ".../Unknown Author/<title>/<title> - Unknown Author.mp3",
// and the next scan parses the author back out of that filename.
//
// Measured against production on 2026-08-25, before the fix, this returned
// Author="Unknown Author" for the path below.
func TestExtractInfoFromPathDoesNotLaunderThePlaceholder(t *testing.T) {
	b := &Book{FilePath: "/mnt/bigdata/books/audiobook-organizer/Unknown Author/Pratchett 036/Pratchett 036 - Unknown Author.mp3"}
	extractInfoFromPath(b)

	require.Empty(t, b.Author,
		"the scan read the organizer's own placeholder back out of the filename and kept it "+
			"as an author; that non-nil AuthorID then closes the AI nomination gate forever")

	// The converse, so the fix cannot pass by simply breaking extraction: the
	// title half of the same parse is still wanted.
	require.Equal(t, "Pratchett 036", b.Title,
		"clearing the placeholder author must not discard the title parsed from the same filename")
}

// A real author in the same filename position must survive untouched -- without
// this, clearing the author unconditionally would also pass the test above.
func TestExtractInfoFromPathKeepsARealAuthor(t *testing.T) {
	b := &Book{FilePath: "/mnt/bigdata/books/audiobook-organizer/Terry Pratchett/Mort/Mort - Terry Pratchett.mp3"}
	extractInfoFromPath(b)

	require.Equal(t, "Terry Pratchett", b.Author,
		"a real author was dropped: the placeholder guard is matching too broadly")
}

func TestRowHasRealAuthor(t *testing.T) {
	id := func(i int) *int { return &i }
	placeholder := id(54846)

	for _, tc := range []struct {
		name        string
		authorID    *int
		placeholder *int
		want        bool
	}{
		{"nil author is not a real author", nil, placeholder, false},
		{"zero author is not a real author", id(0), placeholder, false},
		{"the placeholder is not a real author", id(54846), placeholder, false},
		{"a different author is real", id(38566), placeholder, true},
		// A database that has never filed an authorless book has no placeholder
		// row; every non-zero ID there is genuinely real.
		{"no placeholder row exists", id(38566), nil, true},
		{"nil author with no placeholder row", nil, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, rowHasRealAuthor(tc.authorID, tc.placeholder))
		})
	}
}

// lookupPlaceholderAuthorID must not CREATE the row it looks for. Creating it
// would mint, on any database that had so far avoided it, the exact row the
// gate is trying to treat as absent.
func TestLookupPlaceholderAuthorIDDoesNotCreateTheRow(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	t.Cleanup(func() { SetStore(nil) })

	require.Nil(t, lookupPlaceholderAuthorID(), "a fresh store has no placeholder author")

	got, err := store.GetAuthorByName(authorname.Placeholder)
	require.NoError(t, err)
	require.Nil(t, got, "the lookup created the placeholder author row it was only meant to read")
}

// And once the row does exist it must be found, so the test above cannot pass
// by the lookup being broken outright.
func TestLookupPlaceholderAuthorIDFindsAnExistingRow(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	t.Cleanup(func() { SetStore(nil) })

	created, err := store.CreateAuthor(authorname.Placeholder)
	require.NoError(t, err)
	require.NotNil(t, created)

	got := lookupPlaceholderAuthorID()
	require.NotNil(t, got, "the placeholder author exists but the lookup did not find it")
	require.Equal(t, created.ID, *got)
}
