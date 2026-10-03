// file: internal/server/server_metadata.go
// version: 1.4.0
// guid: 588350bc-83db-47ed-9590-2b6513aadcda
// last-edited: 2026-10-03

package server

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// These helpers are methods on *Server so they use the server's resolved
// store (SERVER-GLOBAL-STORE-AUDIT phase 3b). A read is loadMetadataState; a
// change goes through metafetch.WithStateSnapshot, which holds the book's
// field-state stripe from the read through the save.

// loadMetadataState is the read-only load (metafetch.LoadStateSnapshot).
func (s *Server) loadMetadataState(bookID string) (map[string]metafetch.MetadataFieldState, error) {
	store := s.Ops()
	if store == nil {
		return map[string]metafetch.MetadataFieldState{}, fmt.Errorf("database not initialized")
	}
	return metafetch.LoadStateSnapshot(store, bookID)
}

func decodeRawValue(raw json.RawMessage) any {
	if raw == nil {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	return value
}

func (s *Server) updateFetchedMetadataState(bookID string, values map[string]any) error {
	store := s.Ops()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	return metafetch.WithStateSnapshot(store, bookID, func(state map[string]metafetch.MetadataFieldState) error {
		now := time.Now()
		for field, value := range values {
			entry := state[field]
			entry.FetchedValue = value
			entry.UpdatedAt = now
			state[field] = entry
		}
		return nil
	})
}

func (s *Server) resolveAuthorAndSeriesNames(book *database.Book) (string, string) {
	authorName := ""
	store := s.Ops()
	if book.Author != nil {
		authorName = book.Author.Name
	} else if book.AuthorID != nil && store != nil {
		if author, err := store.GetAuthorByID(*book.AuthorID); err == nil && author != nil {
			authorName = author.Name
		}
	}

	seriesName := ""
	if book.Series != nil {
		seriesName = book.Series.Name
	} else if book.SeriesID != nil && store != nil {
		if series, err := store.GetSeriesByID(*book.SeriesID); err == nil && series != nil {
			seriesName = series.Name
		}
	}

	return authorName, seriesName
}

// batchFetchBookAuthorsAndNarrators pre-fetches author and narrator join table
// entries plus their full details for all given books. Returns maps keyed by
// book ID for join entries, plus maps keyed by author/narrator ID for details.
// Nil maps are returned if the server's store is not available.
func (s *Server) batchFetchBookAuthorsAndNarrators(bookIDs []string) (map[string][]database.BookAuthor, map[int]*database.Author, map[string][]database.BookNarrator, map[int]*database.Narrator) {
	store := s.Ops()
	if len(bookIDs) == 0 || store == nil {
		return nil, nil, nil, nil
	}

	// Collect all book authors and extract author IDs
	bookAuthorsMap := make(map[string][]database.BookAuthor)
	authorIDs := make(map[int]bool)
	for _, bookID := range bookIDs {
		if bas, err := store.GetBookAuthors(bookID); err == nil {
			bookAuthorsMap[bookID] = bas
			for _, ba := range bas {
				authorIDs[ba.AuthorID] = true
			}
		}
	}

	// Fetch all authors in bulk
	authorsByID := make(map[int]*database.Author)
	for authorID := range authorIDs {
		if author, err := store.GetAuthorByID(authorID); err == nil && author != nil {
			authorsByID[authorID] = author
		}
	}

	// Collect all book narrators and extract narrator IDs
	bookNarratorsMap := make(map[string][]database.BookNarrator)
	narratorIDs := make(map[int]bool)
	for _, bookID := range bookIDs {
		if bns, err := store.GetBookNarrators(bookID); err == nil {
			bookNarratorsMap[bookID] = bns
			for _, bn := range bns {
				narratorIDs[bn.NarratorID] = true
			}
		}
	}

	// Fetch all narrators in bulk
	narratorsByID := make(map[int]*database.Narrator)
	for narratorID := range narratorIDs {
		if narrator, err := store.GetNarratorByID(narratorID); err == nil && narrator != nil {
			narratorsByID[narratorID] = narrator
		}
	}

	return bookAuthorsMap, authorsByID, bookNarratorsMap, narratorsByID
}

// enrichBookForResponseSingle enriches a single book by pre-fetching its
// author and narrator data. Convenience wrapper for single-book endpoints.
func (s *Server) enrichBookForResponseSingle(book *database.Book) enrichedBookResponse {
	bookAuthorsMap, authorsByID, bookNarratorsMap, narratorsByID := s.batchFetchBookAuthorsAndNarrators([]string{book.ID})
	return s.enrichBookForResponse(book, bookAuthorsMap, authorsByID, bookNarratorsMap, narratorsByID)
}

// enrichBookForResponse resolves author, series, and narrator names from join
// tables so the JSON response contains all the fields the frontend expects.
// Pre-fetched maps of authors and narrators (by book ID) are used instead of
// per-book DB calls to eliminate N+1 queries.
func (s *Server) enrichBookForResponse(book *database.Book, bookAuthorsMap map[string][]database.BookAuthor, authorsByID map[int]*database.Author, bookNarratorsMap map[string][]database.BookNarrator, narratorsByID map[int]*database.Narrator) enrichedBookResponse {
	authorName, seriesName := s.resolveAuthorAndSeriesNames(book)
	resp := enrichedBookResponse{Book: book}
	if authorName != "" {
		resp.AuthorName = &authorName
	}
	if seriesName != "" {
		resp.SeriesName = &seriesName
	}

	// Check if the book's file exists on disk (single-file books only).
	if book.FilePath != "" {
		_, statErr := os.Stat(book.FilePath)
		exists := statErr == nil
		resp.FileExists = &exists
	}

	if bookAuthorsMap != nil && authorsByID != nil {
		if bookAuthors, ok := bookAuthorsMap[book.ID]; ok && len(bookAuthors) > 0 {
			for _, ba := range bookAuthors {
				if author, ok := authorsByID[ba.AuthorID]; ok && author != nil {
					resp.Authors = append(resp.Authors, authorEntry{
						ID: author.ID, Name: author.Name, Role: ba.Role, Position: ba.Position,
					})
				}
			}
			if resp.AuthorName == nil && len(resp.Authors) > 0 {
				names := make([]string, len(resp.Authors))
				for i, a := range resp.Authors {
					names[i] = a.Name
				}
				combined := strings.Join(names, " & ")
				resp.AuthorName = &combined
			}
		}
	}

	if bookNarratorsMap != nil && narratorsByID != nil {
		if bookNarrators, ok := bookNarratorsMap[book.ID]; ok && len(bookNarrators) > 0 {
			for _, bn := range bookNarrators {
				if narrator, ok := narratorsByID[bn.NarratorID]; ok && narrator != nil {
					resp.Narrators = append(resp.Narrators, narratorEntry{
						ID: narrator.ID, Name: narrator.Name, Role: bn.Role, Position: bn.Position,
					})
				}
			}
			if (book.Narrator == nil || *book.Narrator == "") && len(resp.Narrators) > 0 {
				names := make([]string, len(resp.Narrators))
				for i, n := range resp.Narrators {
					names[i] = n.Name
				}
				combined := strings.Join(names, " & ")
				book.Narrator = &combined
			}
		}
	}

	// Populate metadata_source_hash_duplicate_count if this book has a hash.
	// This lets the BookDetail UI warn about possible duplicates without an
	// extra round-trip.
	if hashStore := s.Ops(); book.MetadataSourceHash != nil && *book.MetadataSourceHash != "" && hashStore != nil {
		if matches, err := hashStore.GetBooksByMetadataSourceHash(*book.MetadataSourceHash); err == nil {
			count := 0
			for _, m := range matches {
				if m.ID != book.ID {
					count++
				}
			}
			if count > 0 {
				resp.MetadataSourceHashDuplicateCount = &count
			}
		}
	}

	return resp
}
