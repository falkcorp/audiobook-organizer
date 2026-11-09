// file: internal/scanner/dedup.go
// version: 1.0.1
// guid: match-4-dedup-001

package scanner

import (
	"crypto/sha256"
	"fmt"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// computeMetadataSourceHash computes the metadata_source_hash for a book
// based on its metadata source and ID. Returns empty string if not enough
// metadata is available.
func computeMetadataSourceHash(book *database.Book) string {
	src, id := bookMetadataSourceAndID(book)
	if src == "" || id == "" {
		return ""
	}
	raw := fmt.Sprintf("%s:%s", src, id)
	sum := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", sum)
}

// bookMetadataSourceAndID extracts the metadata source and ID from a book.
// Returns (source, id) tuple where source is one of: "audible", "openlibrary",
// "google_books", or "hardcover".
func bookMetadataSourceAndID(book *database.Book) (string, string) {
	if book.MetadataSource == nil || *book.MetadataSource == "" {
		return "", ""
	}
	src := *book.MetadataSource
	switch src {
	case "audible":
		if book.ASIN != nil && *book.ASIN != "" {
			return src, *book.ASIN
		}
	case "openlibrary":
		if book.OpenLibraryID != nil && *book.OpenLibraryID != "" {
			return src, *book.OpenLibraryID
		}
	case "google_books":
		if book.GoogleBooksID != nil && *book.GoogleBooksID != "" {
			return src, *book.GoogleBooksID
		}
	case "hardcover":
		if book.HardcoverID != nil && *book.HardcoverID != "" {
			return src, *book.HardcoverID
		}
	}
	return "", ""
}
