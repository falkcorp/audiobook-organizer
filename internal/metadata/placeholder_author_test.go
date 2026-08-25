// file: internal/metadata/placeholder_author_test.go
// version: 1.1.0
// guid: 6b0d24e9-8f13-4a57-b2c6-0d94e1738ac5
// last-edited: 2026-08-25

package metadata

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// internal/metadata carries its OWN copy of the filename parser, and it runs
// first: the scanner only calls its own extractInfoFromPath when Author is still
// empty. So a fix applied solely in internal/scanner never executes on the path
// that produces the bug -- the author is already "Unknown Author" by then, and
// runAIBatchPhase only fills EMPTY fields, so the AI's answer is discarded on
// arrival. Measured 2026-08-25: this returned Artist="Unknown Author".
func TestExtractFromFilenameDoesNotLaunderThePlaceholder(t *testing.T) {
	m := extractFromFilename("/mnt/bigdata/books/audiobook-organizer/Unknown Author/Pratchett 036/Pratchett 036 - Unknown Author.mp3")

	require.Empty(t, m.Artist,
		"the placeholder was recorded as a real author; the AI answer is then discarded "+
			"because the field is no longer empty")
	require.Equal(t, "Pratchett 036", m.Title,
		"clearing the placeholder author must not discard the title from the same filename")

	// The scanner's copy of this parser regressed here during this change:
	// clearing the placeholder re-opened the directory fallback, and under the
	// organizer's <root>/<author>/<title>/<file> layout the immediate parent is
	// the TITLE, so the book was attributed to itself. This copy validates the
	// directory name and does not. Pinned so it stays that way.
	require.NotEqual(t, "Pratchett 036", m.Artist,
		"the book's own title was recorded as its author via the directory fallback")
}

// The directory fallback must not reintroduce what the filename parse just
// dropped, so the placeholder is filtered on that path too.
//
// The path here puts the placeholder in the IMMEDIATE parent on purpose. In the
// organizer's real layout (<root>/<author>/<title>/<file>) the immediate parent
// is the TITLE, so this fallback returns the title as the author -- a separate
// defect, filed as todo.d/20260825-directory-fallback-reads-title-as-author.md
// and deliberately not fixed in this change.
func TestExtractFromFilenameDoesNotTakeThePlaceholderFromTheDirectory(t *testing.T) {
	m := extractFromFilename("/mnt/bigdata/books/audiobook-organizer/Unknown Author/Some Book.mp3")
	require.Empty(t, m.Artist, "the placeholder came back via the directory fallback")
}

// The point of clearing BEFORE the directory fallback rather than after: a real
// author in the directory must still be recovered. A post-hoc clear would blank
// this and lose a recoverable author.
func TestExtractFromFilenameRecoversARealAuthorFromTheDirectory(t *testing.T) {
	m := extractFromFilename("/mnt/bigdata/books/audiobook-organizer/Terry Pratchett/Mort - Unknown Author.mp3")
	require.Equal(t, "Terry Pratchett", m.Artist,
		"a recoverable author was discarded: the placeholder must be cleared BEFORE the "+
			"directory fallback, not after it")
}

// The converse, so none of the above can pass by breaking extraction outright.
func TestExtractFromFilenameKeepsARealAuthor(t *testing.T) {
	m := extractFromFilename("/mnt/bigdata/books/audiobook-organizer/Terry Pratchett/Mort/Mort - Terry Pratchett.mp3")
	require.Equal(t, "Terry Pratchett", m.Artist, "a real author was dropped")
}
