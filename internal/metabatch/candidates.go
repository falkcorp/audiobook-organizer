// file: internal/metabatch/candidates.go
// version: 1.1.0
// guid: b2c3d4e5-f6a7-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-07-01
//
// Package metabatch contains pure service types and logic for the
// metadata candidate batch fetch / apply pipeline. HTTP handlers live
// in internal/server and import from here; this package has no
// dependency on *Server or gin.

package metabatch

import (
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
)

// BookFilesGetter is the narrow interface needed by BuildCandidateBookInfo
// to look up file records for a book. Satisfied by database.Store and any
// type that embeds it.
type BookFilesGetter interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
}

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
	// TranscribedTitle is the Whisper-parsed title from the book's audio intro.
	// Non-empty means transcription data was available during candidate scoring.
	// Lets the review UI offer a "has transcription" filter to focus on books
	// where audio-derived metadata could confirm or reject the match.
	TranscribedTitle string `json:"transcribed_title,omitempty"`
}

// CandidateResult holds the metadata candidate search result for a single book.
type CandidateResult struct {
	Book      CandidateBookInfo            `json:"book"`
	Candidate *metafetch.MetadataCandidate `json:"candidate,omitempty"`
	Status    string                       `json:"status"` // "matched", "no_match", "error"
	Error     string                       `json:"error_message,omitempty"`
}

// BatchFetchRequest is the JSON body for the batch candidate fetch handler.
// Either BookIDs or Selection must be provided; OnlyUnmatched can be combined
// with either to exclude books that already have a "matched" candidate.
type BatchFetchRequest struct {
	BookIDs       []string                  `json:"book_ids"`
	Selection     *operations.SelectionSpec `json:"selection"`
	OnlyUnmatched bool                      `json:"only_unmatched"`
}

// BatchApplyRequest is the JSON body for the batch candidate apply handler.
type BatchApplyRequest struct {
	OperationID string   `json:"operation_id" binding:"required"`
	BookIDs     []string `json:"book_ids" binding:"required"`
}

// LatestMatchedBookIDs returns the set of book IDs whose most-recent
// metadata_candidate_fetch result has status "matched". Used to exclude
// already-matched books when OnlyUnmatched is requested.
func LatestMatchedBookIDs(store database.Store) map[string]bool {
	allOps, err := store.GetRecentOperations(5000)
	if err != nil {
		return nil
	}
	type entry struct {
		status    string
		createdAt time.Time
	}
	latest := map[string]entry{}
	for _, op := range allOps {
		if op.Type != "metadata_candidate_fetch" {
			continue
		}
		results, err := store.GetOperationResults(op.ID)
		if err != nil {
			continue
		}
		for _, r := range results {
			ex, ok := latest[r.BookID]
			if !ok || r.CreatedAt.After(ex.createdAt) {
				latest[r.BookID] = entry{status: r.Status, createdAt: r.CreatedAt}
			}
		}
	}
	matched := make(map[string]bool, len(latest))
	for bookID, e := range latest {
		if e.status == "matched" {
			matched[bookID] = true
		}
	}
	return matched
}

// BuildCandidateBookInfo builds a CandidateBookInfo from a database.Book.
// store is used to look up BookFile.ITunesPath (the authoritative field).
func BuildCandidateBookInfo(store BookFilesGetter, book *database.Book) CandidateBookInfo {
	info := CandidateBookInfo{
		ID:       book.ID,
		Title:    book.Title,
		FilePath: book.FilePath,
		Format:   book.Format,
	}
	if book.Author != nil {
		info.Author = book.Author.Name
	}
	if bfs, bfErr := store.GetBookFiles(book.ID); bfErr == nil && len(bfs) > 0 {
		info.ITunesPath = bfs[0].ITunesPath
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
	if book.TranscribedTitle != nil && *book.TranscribedTitle != "" {
		info.TranscribedTitle = *book.TranscribedTitle
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
func LoadRejectedCandidateKeys(store database.RawKVStore, bookID string) map[string]bool {
	keys := make(map[string]bool)
	// Scan only rejection keys for this specific book.
	pairs, err := store.ScanPrefix(fmt.Sprintf("rejected_candidate:%s:", bookID))
	if err != nil {
		return keys
	}
	for _, kv := range pairs {
		// Key format: rejected_candidate:{bookID}:{source}|{title}
		// Value is just "1" — we only need the key.
		keyStr := kv.Key
		prefix := fmt.Sprintf("rejected_candidate:%s:", bookID)
		if len(keyStr) > len(prefix) {
			keys[keyStr[len(prefix):]] = true
		}
	}
	return keys
}
