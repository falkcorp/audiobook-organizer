// file: internal/metadata/custom_tags.go
// version: 1.0.1
// guid: a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d

package metadata

// Custom tag key constants for embedding audiobook organizer metadata in audio files.
// All keys use the AUDIOBOOK_ORGANIZER_ prefix to avoid collisions with standard tags.
const (
	TagBookID      = "AUDIOBOOK_ORGANIZER_ID"
	TagVersion     = "AUDIOBOOK_ORGANIZER_VERSION"
	TagISBN10      = "AUDIOBOOK_ORGANIZER_ISBN10"
	TagISBN13      = "AUDIOBOOK_ORGANIZER_ISBN13"
	TagASIN        = "AUDIOBOOK_ORGANIZER_ASIN"
	TagOpenLibrary = "AUDIOBOOK_ORGANIZER_OPENLIBRARY"
	TagHardcover   = "AUDIOBOOK_ORGANIZER_HARDCOVER"
	TagGoogleBooks = "AUDIOBOOK_ORGANIZER_GOOGLEBOOKS"
	TagEdition     = "AUDIOBOOK_ORGANIZER_EDITION"
	TagPrintYear   = "AUDIOBOOK_ORGANIZER_PRINT_YEAR"

	// CustomTagVersion is the current schema version for custom tags.
	CustomTagVersion = "1"
)

// CustomTags holds the custom audiobook organizer tag values extracted from or
// destined for audio file metadata.
type CustomTags struct {
	BookID        string
	Version       string
	ISBN10        string
	ISBN13        string
	ASIN          string
	OpenLibraryID string
	HardcoverID   string
	GoogleBooksID string
	Edition       string
	PrintYear     string
}
