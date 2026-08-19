// file: internal/metafetch/batch.go
// version: 1.1.0
// guid: a1b2c3d4-e5f6-7a8b-9c0d-e1f2a3b4c5d6
// last-edited: 2026-08-19

package metafetch

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// CandidateBookInfo contains summary info about a book used in candidate results.
type CandidateBookInfo struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Author     string `json:"author"`
	FilePath   string `json:"file_path"`
	ITunesPath string `json:"itunes_path,omitempty"`
	CoverURL   string `json:"cover_url,omitempty"`
	Format     string `json:"format,omitempty"`
	Duration   int    `json:"duration_seconds,omitempty"`
	FileSize   int64  `json:"file_size_bytes,omitempty"`
	// Language is the book's current language as stored on the
	// Book row (ISO code or full name, whatever was last applied).
	// Used by the review dialog's language filter to hide
	// candidates whose language disagrees with the book's — the
	// motivating Spanish/English "Ancillary Sword" screenshot fix.
	// Empty when the book has no language set, in which case the
	// filter is a no-op for that row.
	Language string `json:"language,omitempty"`
}

// CandidateResult holds the metadata candidate search result for a single book.
type CandidateResult struct {
	Book      CandidateBookInfo  `json:"book"`
	Candidate *MetadataCandidate `json:"candidate,omitempty"`
	Status    string             `json:"status"` // "matched", "no_match", "error"
	Error     string             `json:"error_message,omitempty"`
}

// BuildCandidateBookInfo builds a CandidateBookInfo from a database.Book.
// store is used to look up the first BookFile for ITunesPath; pass nil to skip.
func BuildCandidateBookInfo(book *database.Book, store bookFileLister) CandidateBookInfo {
	info := CandidateBookInfo{
		ID:       book.ID,
		Title:    book.Title,
		FilePath: book.FilePath,
		Format:   book.Format,
	}
	if book.Author != nil {
		info.Author = book.Author.Name
	}
	if store != nil {
		if bfs, bfErr := store.GetBookFiles(book.ID); bfErr == nil && len(bfs) > 0 {
			info.ITunesPath = bfs[0].ITunesPath
		}
	}
	if book.CoverURL != nil {
		info.CoverURL = *book.CoverURL
	}
	if book.Duration != nil {
		info.Duration = *book.Duration
	}
	if book.FileSize != nil {
		info.FileSize = *book.FileSize
	}
	if book.Language != nil {
		info.Language = *book.Language
	}
	return info
}

// CountByStatus counts CandidateResults with the given status.
func CountByStatus(results []CandidateResult, status string) int {
	n := 0
	for _, r := range results {
		if r.Status == status {
			n++
		}
	}
	return n
}

// LoadRejectedCandidateKeys finds previously rejected candidates for a book.
// Uses a dedicated rejection key prefix for fast lookup instead of scanning
// all operation results.
func LoadRejectedCandidateKeys(store rejectedKeyScanner, bookID string) map[string]bool {
	keys := make(map[string]bool)
	// Scan only rejection keys for this specific book
	pairs, err := store.ScanPrefix(fmt.Sprintf("rejected_candidate:%s:", bookID))
	if err != nil {
		return keys
	}
	for _, kv := range pairs {
		// Key format: rejected_candidate:{bookID}:{source}|{title}
		// Value is just "1" — we only need the key
		keyStr := string(kv.Key)
		prefix := fmt.Sprintf("rejected_candidate:%s:", bookID)
		if len(keyStr) > len(prefix) {
			keys[keyStr[len(prefix):]] = true
		}
	}
	return keys
}
