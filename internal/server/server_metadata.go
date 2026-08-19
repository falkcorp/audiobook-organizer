// file: internal/server/server_metadata.go
// version: 1.2.0
// guid: 588350bc-83db-47ed-9590-2b6513aadcda
// last-edited: 2026-05-13

package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

func metadataStateKey(bookID string) string {
	return fmt.Sprintf("metadata_state_%s", bookID)
}

func decodeMetadataValue(raw *string) any {
	if raw == nil || *raw == "" {
		return nil
	}
	var value any
	if err := json.Unmarshal([]byte(*raw), &value); err != nil {
		return *raw
	}
	return value
}

func encodeMetadataValue(value any) (*string, error) {
	if value == nil {
		return nil, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	encoded := string(data)
	return &encoded, nil
}

// These helpers are now methods on *Server so they use the server's
// resolved store (SERVER-GLOBAL-STORE-AUDIT phase 3b). Callers in this
// package update to s.loadMetadataState(...) / s.saveMetadataState(...).

func (s *Server) loadLegacyMetadataState(bookID string) (map[string]metafetch.MetadataFieldState, error) {
	state := map[string]metafetch.MetadataFieldState{}
	store := s.Ops()
	if store == nil {
		return state, fmt.Errorf("database not initialized")
	}

	pref, err := store.GetUserPreference(metadataStateKey(bookID))
	if err != nil {
		return state, err
	}
	if pref == nil || pref.Value == nil || *pref.Value == "" {
		return state, nil
	}

	if err := json.Unmarshal([]byte(*pref.Value), &state); err != nil {
		return state, fmt.Errorf("failed to parse metadata state: %w", err)
	}
	return state, nil
}

func (s *Server) loadMetadataState(bookID string) (map[string]metafetch.MetadataFieldState, error) {
	state := map[string]metafetch.MetadataFieldState{}
	store := s.Ops()
	if store == nil {
		return state, fmt.Errorf("database not initialized")
	}

	stored, err := store.GetMetadataFieldStates(bookID)
	if err != nil {
		return state, err
	}
	for _, entry := range stored {
		state[entry.Field] = metafetch.MetadataFieldState{
			FetchedValue:   decodeMetadataValue(entry.FetchedValue),
			OverrideValue:  decodeMetadataValue(entry.OverrideValue),
			OverrideLocked: entry.OverrideLocked,
			UpdatedAt:      entry.UpdatedAt,
		}
	}
	if len(state) > 0 {
		return state, nil
	}

	legacy, err := s.loadLegacyMetadataState(bookID)
	if err != nil {
		return state, err
	}
	if len(legacy) == 0 {
		return state, nil
	}

	if err := s.saveMetadataState(bookID, legacy); err != nil {
		slog.Warn("failed to migrate legacy metadata state for", "bookID", bookID, "err", err)
	}
	return legacy, nil
}

func (s *Server) saveMetadataState(bookID string, state map[string]metafetch.MetadataFieldState) error {
	store := s.Ops()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	existing, err := store.GetMetadataFieldStates(bookID)
	if err != nil {
		return err
	}
	existingFields := map[string]struct{}{}
	for _, entry := range existing {
		existingFields[entry.Field] = struct{}{}
	}

	now := time.Now()
	for field, entry := range state {
		fetched, err := encodeMetadataValue(entry.FetchedValue)
		if err != nil {
			return fmt.Errorf("failed to encode fetched metadata for %s: %w", field, err)
		}
		override, err := encodeMetadataValue(entry.OverrideValue)
		if err != nil {
			return fmt.Errorf("failed to encode override metadata for %s: %w", field, err)
		}
		if entry.UpdatedAt.IsZero() {
			entry.UpdatedAt = now
		}

		dbState := database.MetadataFieldState{
			BookID:         bookID,
			Field:          field,
			FetchedValue:   fetched,
			OverrideValue:  override,
			OverrideLocked: entry.OverrideLocked,
			UpdatedAt:      entry.UpdatedAt,
		}

		if err := store.UpsertMetadataFieldState(&dbState); err != nil {
			return fmt.Errorf("failed to persist metadata state for %s: %w", field, err)
		}
		delete(existingFields, field)
	}

	for field := range existingFields {
		if err := store.DeleteMetadataFieldState(bookID, field); err != nil {
			return fmt.Errorf("failed to clean up metadata state for %s: %w", field, err)
		}
	}

	return nil
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
	state, err := s.loadMetadataState(bookID)
	if err != nil {
		return err
	}
	if state == nil {
		state = map[string]metafetch.MetadataFieldState{}
	}
	for field, value := range values {
		entry := state[field]
		entry.FetchedValue = value
		entry.UpdatedAt = time.Now()
		state[field] = entry
	}
	return s.saveMetadataState(bookID, state)
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

func buildMetadataProvenance(book *database.Book, state map[string]metafetch.MetadataFieldState, meta metadata.Metadata, authorName, seriesName string, comparisonValues map[string]any) map[string]database.MetadataProvenanceEntry {
	if state == nil {
		state = map[string]metafetch.MetadataFieldState{}
	}

	provenance := map[string]database.MetadataProvenanceEntry{}

	addEntry := func(field string, fileValue any, storedValue any) {
		entryState := state[field]
		effectiveSource := ""
		var effectiveValue any
		switch {
		case entryState.OverrideValue != nil:
			effectiveSource = "override"
			effectiveValue = entryState.OverrideValue
		case storedValue != nil:
			effectiveSource = "stored"
			effectiveValue = storedValue
		case entryState.FetchedValue != nil:
			effectiveSource = "fetched"
			effectiveValue = entryState.FetchedValue
		case fileValue != nil:
			effectiveSource = "file"
			effectiveValue = fileValue
		}

		var updatedAt *time.Time
		if !entryState.UpdatedAt.IsZero() {
			ts := entryState.UpdatedAt.UTC()
			updatedAt = &ts
		}

		entry := database.MetadataProvenanceEntry{
			FileValue:       fileValue,
			FetchedValue:    entryState.FetchedValue,
			StoredValue:     storedValue,
			OverrideValue:   entryState.OverrideValue,
			OverrideLocked:  entryState.OverrideLocked,
			EffectiveValue:  effectiveValue,
			EffectiveSource: effectiveSource,
			UpdatedAt:       updatedAt,
		}

		if comparisonValues != nil {
			if cv, ok := comparisonValues[field]; ok {
				entry.ComparisonValue = cv
			}
		}

		provenance[field] = entry
	}

	addEntry("title", meta.Title, book.Title)
	addEntry("author_name", meta.Artist, authorName)
	addEntry("narrator", meta.Narrator, stringVal(book.Narrator))
	addEntry("series_name", meta.Series, seriesName)
	addEntry("publisher", meta.Publisher, stringVal(book.Publisher))
	addEntry("language", meta.Language, stringVal(book.Language))
	addEntry("audiobook_release_year", meta.Year, intVal(book.AudiobookReleaseYear))
	addEntry("isbn10", meta.ISBN10, stringVal(book.ISBN10))
	addEntry("isbn13", meta.ISBN13, stringVal(book.ISBN13))
	addEntry("genre", meta.Genre, stringVal(book.Genre))
	addEntry("album", meta.Album, book.Title)
	addEntry("asin", nonEmpty(meta.ASIN), stringVal(book.ASIN))
	var seriesIdx any
	if meta.SeriesIndex > 0 {
		seriesIdx = meta.SeriesIndex
	}
	addEntry("series_index", seriesIdx, intVal(book.SeriesSequence))
	addEntry("print_year", nonEmpty(meta.PrintYear), intVal(book.PrintYear))
	addEntry("edition", nonEmpty(meta.Edition), stringVal(book.Edition))
	addEntry("description", nonEmpty(meta.Comments), stringVal(book.Description))
	addEntry("book_id", nonEmpty(meta.BookOrganizerID), book.ID)
	addEntry("open_library_id", nonEmpty(meta.OpenLibraryID), stringVal(book.OpenLibraryID))
	addEntry("hardcover_id", nonEmpty(meta.HardcoverID), stringVal(book.HardcoverID))
	addEntry("google_books_id", nonEmpty(meta.GoogleBooksID), stringVal(book.GoogleBooksID))

	return provenance
}
