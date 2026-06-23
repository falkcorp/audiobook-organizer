// file: internal/audiobooks/service_mutation.go
// version: 1.0.0
// guid: e7b1f6a5-b8c9-0d12-ce3f-4a5b6c7d8e9f
// last-edited: 2026-06-23

package audiobooks

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
)

// UpdateAudiobook updates an audiobook with new metadata and handles overrides
func (svc *AudiobookService) UpdateAudiobook(ctx context.Context, id string, req *UpdateAudiobookRequest) (*database.Book, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	currentBook, err := svc.store.GetBookByID(id)
	if err != nil {
		return nil, err
	}
	if currentBook == nil {
		return nil, fmt.Errorf("audiobook not found")
	}

	now := time.Now()

	// Apply updates from req.Updates to currentBook first
	// This ensures extractors can read the updated fields
	if req.Updates.Title != "" {
		currentBook.Title = req.Updates.Title
	}
	if req.Updates.Format != "" {
		currentBook.Format = req.Updates.Format
	}
	if req.Updates.FilePath != "" && req.Updates.FilePath != currentBook.FilePath {
		// Validate the new path is inside an allowed directory before accepting
		// the change (go/path-injection), then confirm it exists.
		absNewPath, err := filepath.Abs(req.Updates.FilePath)
		if err != nil {
			return nil, fmt.Errorf("invalid new path: %w", err)
		}
		importPaths, err := svc.store.GetAllImportPaths()
		if err != nil {
			return nil, fmt.Errorf("failed to check allowed paths: %w", err)
		}
		if !fileops.IsAllowedPath(absNewPath, importPaths) {
			return nil, fmt.Errorf("new path is not in an allowed directory")
		}
		// Validate new file exists before accepting path change.
		// absNewPath is gated by fileops.IsAllowedPath above; CodeQL does not
		// model that custom allow-list barrier, so suppress the false positive.
		if _, err := os.Stat(absNewPath); err != nil { // lgtm[go/path-injection]
			return nil, fmt.Errorf("file does not exist at new path: %s", absNewPath)
		}
		slog.Info("audiobook_service FilePath changed for →", "id", id, "currentBook", currentBook.FilePath, "value2", absNewPath)
		currentBook.FilePath = absNewPath
	}
	if req.Updates.Narrator != nil {
		currentBook.Narrator = req.Updates.Narrator
	}
	if req.Updates.Publisher != nil {
		currentBook.Publisher = req.Updates.Publisher
	}
	if req.Updates.Language != nil {
		currentBook.Language = req.Updates.Language
	}
	if req.Updates.AudiobookReleaseYear != nil {
		currentBook.AudiobookReleaseYear = req.Updates.AudiobookReleaseYear
	}
	if req.Updates.ISBN10 != nil {
		currentBook.ISBN10 = req.Updates.ISBN10
	}
	if req.Updates.ISBN13 != nil {
		currentBook.ISBN13 = req.Updates.ISBN13
	}
	if req.Updates.AuthorID != nil {
		currentBook.AuthorID = req.Updates.AuthorID
	}
	if req.Updates.SeriesID != nil {
		currentBook.SeriesID = req.Updates.SeriesID
	}

	payload := &AudiobookUpdate{
		Book: currentBook,
	}

	// Load and process metadata state
	state, err := svc.loadMetadataState(id)
	if err != nil {
		slog.Info("[ERROR] UpdateAudiobook failed to load metadata state", "err", err)
		return nil, fmt.Errorf("failed to load metadata state")
	}
	if state == nil {
		state = map[string]metadataFieldState{}
	}

	// Create a MetadataStateService for recording change history.
	mss := newMetadataStateSvc(svc.store)

	// Process overrides
	for field, override := range req.Updates.Overrides {
		entry := state[field]
		oldOverrideValue := entry.OverrideValue
		if override.Clear {
			entry.OverrideValue = nil
			entry.OverrideLocked = false
			entry.UpdatedAt = now
			// Record history for clearing an override.
			if fmt.Sprintf("%v", oldOverrideValue) != fmt.Sprintf("%v", nil) {
				mss.recordChange(id, field, "override", "user_edit", oldOverrideValue, nil)
			}
		} else {
			if len(override.Value) > 0 {
				val := decodeRawValue(override.Value)
				entry.OverrideValue = val
				entry.OverrideLocked = override.Locked == nil || *override.Locked
				entry.UpdatedAt = now
				ApplyOverrideToPayload(payload, field, val)
				// Record history for setting an override.
				if fmt.Sprintf("%v", oldOverrideValue) != fmt.Sprintf("%v", val) {
					mss.recordChange(id, field, "override", "user_edit", oldOverrideValue, val)
				}
			} else if override.Locked != nil {
				entry.OverrideLocked = *override.Locked
				entry.UpdatedAt = now
			}
			if len(override.FetchedValue) > 0 {
				entry.FetchedValue = decodeRawValue(override.FetchedValue)
				if entry.UpdatedAt.IsZero() {
					entry.UpdatedAt = now
				}
			}
		}
		state[field] = entry
	}

	// Resolve author by name or ID — auto-split on " & " for multiple authors
	var resolvedAuthorName string
	if req.Updates.AuthorName != nil {
		name := strings.TrimSpace(*req.Updates.AuthorName)
		if name != "" {
			// Split on " & " to support multiple authors
			authorNames := splitMultipleNames(name)
			var bookAuthors []database.BookAuthor
			var primaryAuthorID int
			for i, aName := range authorNames {
				aName = strings.TrimSpace(aName)
				if aName == "" {
					continue
				}
				normalizedName := dedup.NormalizeAuthorName(aName)
				author, err := svc.store.GetAuthorByName(normalizedName)
				if err != nil {
					return nil, fmt.Errorf("failed to resolve author")
				}
				if author == nil {
					author, err = svc.store.CreateAuthor(normalizedName)
					if err != nil {
						return nil, fmt.Errorf("failed to create author")
					}
				}
				role := "author"
				if i > 0 {
					role = "co-author"
				}
				bookAuthors = append(bookAuthors, database.BookAuthor{
					BookID: id, AuthorID: author.ID, Role: role, Position: i,
				})
				if i == 0 {
					primaryAuthorID = author.ID
				}
			}
			// Set primary author on the book for backward compat
			payload.AuthorID = &primaryAuthorID
			resolvedAuthorName = name // Keep the combined name for display
			// Save multiple authors to join table
			if len(bookAuthors) > 0 {
				if err := svc.store.SetBookAuthors(id, bookAuthors); err != nil {
					slog.Warn("failed to set book authors", "err", err)
				}
			}
		} else {
			payload.AuthorID = nil
		}
	} else if payload.AuthorID != nil {
		if author, err := svc.store.GetAuthorByID(*payload.AuthorID); err == nil && author != nil {
			resolvedAuthorName = author.Name
		}
	}

	// Resolve narrator — auto-split on " & " for multiple narrators
	if req.Updates.Narrator != nil {
		narStr := strings.TrimSpace(*req.Updates.Narrator)
		if narStr != "" {
			narratorNames := splitMultipleNames(narStr)
			var bookNarrators []database.BookNarrator
			for i, nName := range narratorNames {
				nName = strings.TrimSpace(nName)
				if nName == "" {
					continue
				}
				narrator, err := svc.store.GetNarratorByName(nName)
				if err != nil || narrator == nil {
					narrator, err = svc.store.CreateNarrator(nName)
					if err != nil {
						slog.Warn("failed to create narrator", "nName", nName, "err", err)
						continue
					}
				}
				role := "narrator"
				if i > 0 {
					role = "co-narrator"
				}
				bookNarrators = append(bookNarrators, database.BookNarrator{
					BookID: id, NarratorID: narrator.ID, Role: role, Position: i,
				})
			}
			if len(bookNarrators) > 0 {
				if err := svc.store.SetBookNarrators(id, bookNarrators); err != nil {
					slog.Warn("failed to set book narrators", "err", err)
				}
			}
		}
	}

	// Resolve series by name or ID
	var resolvedSeriesName string
	if req.Updates.SeriesName != nil {
		name := strings.TrimSpace(*req.Updates.SeriesName)
		if name != "" {
			series, err := svc.store.GetSeriesByName(name, payload.AuthorID)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve series")
			}
			if series == nil {
				series, err = svc.store.CreateSeries(name, payload.AuthorID)
				if err != nil {
					return nil, fmt.Errorf("failed to create series")
				}
			}
			payload.SeriesID = &series.ID
			resolvedSeriesName = series.Name
		} else {
			payload.SeriesID = nil
		}
	} else if payload.SeriesID != nil {
		if series, err := svc.store.GetSeriesByID(*payload.SeriesID); err == nil && series != nil {
			resolvedSeriesName = series.Name
		}
	}

	// Process direct field updates (non-override)
	fieldExtractors := map[string]func() (any, bool){
		"title": func() (any, bool) {
			return payload.Title, true
		},
		"author_name": func() (any, bool) {
			if resolvedAuthorName == "" {
				return nil, false
			}
			return resolvedAuthorName, true
		},
		"series_name": func() (any, bool) {
			if resolvedSeriesName == "" {
				return nil, false
			}
			return resolvedSeriesName, true
		},
		"narrator": func() (any, bool) {
			if payload.Narrator == nil {
				return nil, false
			}
			return *payload.Narrator, true
		},
		"publisher": func() (any, bool) {
			if payload.Publisher == nil {
				return nil, false
			}
			return *payload.Publisher, true
		},
		"language": func() (any, bool) {
			if payload.Language == nil {
				return nil, false
			}
			return *payload.Language, true
		},
		"audiobook_release_year": func() (any, bool) {
			if payload.AudiobookReleaseYear == nil {
				return nil, false
			}
			return *payload.AudiobookReleaseYear, true
		},
		"isbn10": func() (any, bool) {
			if payload.ISBN10 == nil {
				return nil, false
			}
			return *payload.ISBN10, true
		},
		"isbn13": func() (any, bool) {
			if payload.ISBN13 == nil {
				return nil, false
			}
			return *payload.ISBN13, true
		},
	}

	for field, extractor := range fieldExtractors {
		if _, ok := req.RawPayload[field]; !ok {
			slog.Debug("UpdateAudiobook field not in RawPayload", "field", field)
			continue
		}
		if _, hasOverride := req.Updates.Overrides[field]; hasOverride {
			slog.Debug("UpdateAudiobook field has explicit override", "field", field)
			continue
		}
		if value, ok := extractor(); ok {
			slog.Debug("UpdateAudiobook creating state for field with value", "field", field, "value", value)
			entry := state[field]
			oldValue := entry.OverrideValue

			entry.OverrideValue = value
			entry.OverrideLocked = true
			entry.UpdatedAt = now
			state[field] = entry

			// Record history only when the value actually changed.
			if fmt.Sprintf("%v", oldValue) != fmt.Sprintf("%v", value) {
				mss.recordChange(id, field, "override", "user_edit", oldValue, value)
			}
		} else {
			slog.Debug("UpdateAudiobook extractor for field returned false/nil", "field", field)
		}
	}

	// Process unlock overrides
	for _, field := range req.Updates.UnlockOverrides {
		entry := state[field]
		entry.OverrideLocked = false
		entry.UpdatedAt = now
		state[field] = entry
	}

	// Save to database
	updatedBook, err := svc.store.UpdateBook(id, payload.Book)
	if err != nil {
		return nil, err
	}

	// Save metadata state
	if err := svc.saveMetadataState(id, state); err != nil {
		slog.Info("[ERROR] UpdateAudiobook failed to save metadata state", "err", err)
		return nil, fmt.Errorf("failed to persist metadata state")
	}

	svc.InvalidateBookCaches()

	// Enrich response with resolved names
	if resolvedAuthorName != "" && updatedBook.AuthorID != nil {
		updatedBook.Author = &database.Author{ID: *updatedBook.AuthorID, Name: resolvedAuthorName}
	}
	if resolvedSeriesName != "" && updatedBook.SeriesID != nil {
		updatedBook.Series = &database.Series{ID: *updatedBook.SeriesID, Name: resolvedSeriesName, AuthorID: payload.AuthorID}
	}

	return updatedBook, nil
}

// DeleteAudiobook deletes an audiobook (soft or hard delete)
func (svc *AudiobookService) DeleteAudiobook(ctx context.Context, id string, opts *DeleteAudiobookOptions) (map[string]any, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	if opts == nil {
		opts = &DeleteAudiobookOptions{}
	}

	// Get the book first to access its hash.
	// PebbleStore returns nil, nil for a not-found book; check both.
	book, err := svc.store.GetBookByID(id)
	if err != nil || book == nil {
		return nil, fmt.Errorf("audiobook not found")
	}

	// If soft delete requested, mark for deletion instead of hard delete
	if opts.SoftDelete {
		if (book.MarkedForDeletion != nil && *book.MarkedForDeletion) ||
			(book.LibraryState != nil && strings.EqualFold(*book.LibraryState, "deleted")) {
			return nil, fmt.Errorf("audiobook already soft deleted")
		}

		now := time.Now()
		book.MarkedForDeletion = boolPtr(true)
		book.MarkedForDeletionAt = &now
		book.LibraryState = stringPtr("deleted")

		if _, err := svc.store.UpdateBook(id, book); err != nil {
			return nil, err
		}

		// Optionally block the hash
		blocked := false
		if opts.BlockHash && book.FileHash != nil && *book.FileHash != "" {
			if err := svc.store.AddBlockedHash(*book.FileHash, "User deleted - soft delete"); err != nil {
				slog.Warn("failed to block hash during soft delete", "err", err)
			} else {
				blocked = true
			}
		}

		// Remove the book's tracks from iTunes. Soft-delete is treated
		// as "user no longer wants this in their library" and the
		// iTunes side should reflect that immediately.
		svc.enqueueITunesRemovesForBook(id, book)

		svc.InvalidateBookCaches()
		return map[string]any{
			"message":     "audiobook soft deleted",
			"blocked":     blocked,
			"soft_delete": true,
		}, nil
	}

	// Hard delete path
	// Optionally block the hash before deleting
	blocked := false
	if opts.BlockHash && book.FileHash != nil && *book.FileHash != "" {
		if err := svc.store.AddBlockedHash(*book.FileHash, "User deleted - prevent reimport"); err != nil {
			slog.Warn("failed to block hash before delete", "err", err)
			// Continue with delete even if blocking fails
		} else {
			blocked = true
		}
	}

	// Capture iTunes PIDs BEFORE the DB row vanishes so we can
	// enqueue iTunes removes after the hard delete succeeds.
	var itunesPIDs []string
	if svc.itunesEnqueuer != nil {
		itunesPIDs = svc.collectITunesPIDsForBook(id, book)
	}

	if err := svc.store.DeleteBook(id); err != nil {
		if err.Error() == "book not found" {
			return nil, fmt.Errorf("audiobook not found")
		}
		return nil, err
	}

	if svc.itunesEnqueuer != nil {
		for _, pid := range itunesPIDs {
			svc.itunesEnqueuer.EnqueueRemove(pid)
		}
	}

	svc.InvalidateBookCaches()
	return map[string]any{
		"message": "audiobook deleted",
		"blocked": blocked,
	}, nil
}

// collectITunesPIDsForBook returns every PID stored on the book's
// book_files plus the legacy Book.ITunesPersistentID field. Used by
// hard-delete to pre-capture PIDs before the row is gone, and by the
// orphan-cleanup endpoint.
func (svc *AudiobookService) collectITunesPIDsForBook(bookID string, book *database.Book) []string {
	pids := []string{}
	files, _ := svc.store.GetBookFiles(bookID)
	for _, f := range files {
		if f.ITunesPersistentID != "" {
			pids = append(pids, f.ITunesPersistentID)
		}
	}
	if book != nil && book.ITunesPersistentID != nil && *book.ITunesPersistentID != "" {
		pids = append(pids, *book.ITunesPersistentID)
	}
	return pids
}

// enqueueITunesRemovesForBook is a soft-delete helper: it pulls the
// PIDs and enqueues each via the wired batcher. No-op if the batcher
// isn't wired.
func (svc *AudiobookService) enqueueITunesRemovesForBook(bookID string, book *database.Book) {
	if svc.itunesEnqueuer == nil {
		return
	}
	for _, pid := range svc.collectITunesPIDsForBook(bookID, book) {
		svc.itunesEnqueuer.EnqueueRemove(pid)
	}
}

// ApplyOverrideToPayload applies an override value to the update payload
func ApplyOverrideToPayload(payload *AudiobookUpdate, field string, value any) {
	switch field {
	case "title":
		if v, ok := value.(string); ok {
			payload.Title = v
		}
	case "author_name":
		if v, ok := value.(string); ok {
			payload.AuthorName = &v
		}
	case "series_name":
		if v, ok := value.(string); ok {
			payload.SeriesName = &v
		}
	case "narrator":
		if v, ok := value.(string); ok {
			payload.Narrator = stringPtr(v)
		}
	case "publisher":
		if v, ok := value.(string); ok {
			payload.Publisher = stringPtr(v)
		}
	case "language":
		if v, ok := value.(string); ok {
			payload.Language = stringPtr(v)
		}
	case "audiobook_release_year":
		switch v := value.(type) {
		case float64:
			year := int(v)
			payload.AudiobookReleaseYear = &year
		case int:
			year := v
			payload.AudiobookReleaseYear = &year
		}
	case "isbn10":
		if v, ok := value.(string); ok {
			payload.ISBN10 = stringPtr(v)
		}
	case "isbn13":
		if v, ok := value.(string); ok {
			payload.ISBN13 = stringPtr(v)
		}
	case "asin":
		if v, ok := value.(string); ok {
			payload.ASIN = stringPtr(v)
		}
	}
}

