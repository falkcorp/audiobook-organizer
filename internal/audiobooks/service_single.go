// file: internal/audiobooks/service_single.go
// version: 1.1.1
// guid: d6a0e5f4-a7b8-9c01-bd2e-3f4a5b6c7d8e
// last-edited: 2026-07-03

package audiobooks

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// GetAudiobook retrieves a single audiobook by ID with full metadata provenance
func (svc *AudiobookService) GetAudiobook(ctx context.Context, id string) (*database.Book, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	if cached, ok := svc.bookCache.Get(id); ok {
		return cached, nil
	}

	book, err := svc.store.GetBookByID(id)
	if err != nil {
		return nil, err
	}
	if book == nil {
		return nil, fmt.Errorf("audiobook not found")
	}

	// Load metadata state and extract file metadata
	state, err := svc.loadMetadataState(book.ID)
	if err != nil {
		slog.Info("[ERROR] GetAudiobook failed to load metadata state for", "book", book.ID, "err", err)
		// Don't fail the entire request, just use empty state
		state = map[string]metadataFieldState{}
	}

	authorName, seriesName := resolveAuthorAndSeriesNames(svc.store, book)

	meta := svc.extractBookFileMetadata(book, authorName)

	// Backfill duration (and other media info) from file if DB fields are missing
	if book.FilePath != "" && book.Duration == nil {
		if mi, miErr := mediainfo.Extract(book.FilePath); miErr == nil && mi.Duration > 0 {
			book.Duration = &mi.Duration
			if _, updErr := svc.store.UpdateBook(book.ID, book); updErr != nil {
				slog.Warn("GetAudiobook failed to backfill duration for", "book", book.ID, "updErr", updErr)
			}
		}
	}

	// Build metadata provenance
	book.MetadataProvenance = buildMetadataProvenance(book, state, meta, authorName, seriesName, nil)
	nowUTC := time.Now().UTC()
	book.MetadataProvenanceAt = &nowUTC

	svc.bookCache.Set(id, book)
	return book, nil
}

// GetAudiobookTags retrieves metadata tags and media info for an audiobook
func (svc *AudiobookService) GetAudiobookTags(ctx context.Context, id string, compareID string, snapshotTS string) (map[string]any, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	book, err := svc.store.GetBookByID(id)
	if err != nil {
		return nil, err
	}
	if book == nil {
		return nil, fmt.Errorf("audiobook not found")
	}

	state, err := svc.loadMetadataState(book.ID)
	if err != nil {
		slog.Info("[ERROR] GetAudiobookTags failed to load metadata state for", "book", book.ID, "err", err)
		state = map[string]metadataFieldState{}
	}

	authorName, seriesName := resolveAuthorAndSeriesNames(svc.store, book)

	response := map[string]any{
		"media_info": map[string]any{
			"codec":       stringVal(book.Codec),
			"bitrate":     intVal(book.Bitrate),
			"sample_rate": intVal(book.SampleRate),
			"channels":    intVal(book.Channels),
			"bit_depth":   intVal(book.BitDepth),
			"quality":     stringVal(book.Quality),
			"duration":    intVal(book.Duration),
		},
		"tags": map[string]database.MetadataProvenanceEntry{},
	}

	meta := svc.extractBookFileMetadata(book, authorName)

	// Backfill empty media_info from file if DB fields are missing
	if book.FilePath != "" && (book.Codec == nil || book.Bitrate == nil || book.SampleRate == nil) {
		if mi, err := mediainfo.Extract(book.FilePath); err == nil {
			needsUpdate := false
			if book.Codec == nil && mi.Codec != "" {
				book.Codec = &mi.Codec
				needsUpdate = true
			}
			if book.Bitrate == nil && mi.Bitrate > 0 {
				book.Bitrate = &mi.Bitrate
				needsUpdate = true
			}
			if book.SampleRate == nil && mi.SampleRate > 0 {
				book.SampleRate = &mi.SampleRate
				needsUpdate = true
			}
			if book.Channels == nil && mi.Channels > 0 {
				book.Channels = &mi.Channels
				needsUpdate = true
			}
			if book.Duration == nil && mi.Duration > 0 {
				book.Duration = &mi.Duration
				needsUpdate = true
			}
			if needsUpdate {
				if _, err := svc.store.UpdateBook(book.ID, book); err != nil {
					slog.Warn("GetAudiobookTags failed to backfill media info for", "book", book.ID, "err", err)
				}
				response["media_info"] = map[string]any{
					"codec":       stringVal(book.Codec),
					"bitrate":     intVal(book.Bitrate),
					"sample_rate": intVal(book.SampleRate),
					"channels":    intVal(book.Channels),
					"bit_depth":   intVal(book.BitDepth),
					"quality":     stringVal(book.Quality),
					"duration":    intVal(book.Duration),
				}
			}
		}
	}

	// Load comparison metadata if compare_id is provided
	var comparisonValues map[string]any
	if snapshotTS != "" {
		ts, err := time.Parse(time.RFC3339Nano, snapshotTS)
		if err != nil {
			return nil, fmt.Errorf("invalid snapshot timestamp: %w", err)
		}
		snapshotBook, verErr := svc.store.GetBookAtVersion(id, ts)
		if verErr == nil && snapshotBook != nil {
			snapshotAuthorName, snapshotSeriesName := resolveAuthorAndSeriesNames(svc.store, snapshotBook)
			comparisonValues = buildComparisonValuesFromBook(snapshotBook, snapshotAuthorName, snapshotSeriesName)
		} else {
			// Fallback: reconstruct "before" state from activity log old_values
			slog.Debug("GetAudiobookTags GetBookAtVersion failed (), falling back to activity log for snapshot at", "verErr", verErr, "snapshotTS", snapshotTS)
			if svc.activityService != nil {
				comparisonValues = buildComparisonValuesFromActivityLog(svc.activityService, id, ts)
			}
		}
	} else if compareID != "" {
		compBook, err := svc.store.GetBookByID(compareID)
		if err != nil {
			slog.Warn("GetAudiobookTags failed to load comparison book", "compareID", compareID, "err", err)
		} else if compBook != nil && compBook.FilePath != "" {
			if cm, err := metadata.ExtractMetadata(compBook.FilePath, nil); err == nil {
				comparisonValues = buildComparisonValuesFromMetadata(&cm)
			} else {
				slog.Warn("GetAudiobookTags failed to extract comparison metadata for", "compBook", compBook.FilePath, "err", err)
			}
		}
	}

	tags := buildMetadataProvenance(book, state, meta, authorName, seriesName, comparisonValues)
	response["tags"] = tags

	return response, nil
}

func (svc *AudiobookService) extractBookFileMetadata(book *database.Book, authorName string) metadata.Metadata {
	var meta metadata.Metadata
	if book == nil || book.FilePath == "" {
		return meta
	}

	m, err := metadata.ExtractMetadata(book.FilePath, nil)
	if err != nil {
		slog.Warn("audiobook_service failed to extract metadata for", "book", book.FilePath, "err", err)
		return meta
	}

	if m.OrganizerTagVersion == "" &&
		strings.TrimSpace(authorName) != "" &&
		strings.TrimSpace(m.Narrator) != "" &&
		strings.EqualFold(strings.TrimSpace(m.Artist), strings.TrimSpace(m.Narrator)) {
		m.Artist = authorName
		m.AuthorSource = "database author fallback"
	}

	return m
}

// GetDuplicateBooks retrieves all duplicate book groups
func (svc *AudiobookService) GetDuplicateBooks(ctx context.Context) (*DuplicatesResult, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	// Get hash-based duplicates
	duplicateGroups, err := svc.store.GetDuplicateBooks()
	if err != nil {
		return nil, err
	}
	if duplicateGroups == nil {
		duplicateGroups = [][]database.Book{}
	}

	// Get folder-based duplicates (same title in same folder, e.g. M4B + MP3)
	folderGroups, err := svc.store.GetFolderDuplicates()
	if err != nil {
		slog.Warn("folder duplicate detection failed", "err", err)
	} else {
		// Merge folder groups, avoiding duplicate groups already found by hash
		seenBookIDs := map[string]bool{}
		for _, group := range duplicateGroups {
			for _, b := range group {
				seenBookIDs[b.ID] = true
			}
		}
		for _, group := range folderGroups {
			// Skip if all books in this group are already in hash-based groups
			allSeen := true
			for _, b := range group {
				if !seenBookIDs[b.ID] {
					allSeen = false
					break
				}
			}
			if !allSeen {
				duplicateGroups = append(duplicateGroups, group)
				for _, b := range group {
					seenBookIDs[b.ID] = true
				}
			}
		}
	}

	// Calculate total duplicates count
	totalDuplicates := 0
	for _, group := range duplicateGroups {
		totalDuplicates += len(group) - 1
	}

	return &DuplicatesResult{
		Groups:         duplicateGroups,
		GroupCount:     len(duplicateGroups),
		DuplicateCount: totalDuplicates,
	}, nil
}

// GetSoftDeletedBooks retrieves soft-deleted audiobooks with optional age filter
func (svc *AudiobookService) GetSoftDeletedBooks(ctx context.Context, limit int, offset int, olderThanDays *int) ([]database.Book, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	// Normalize limit and offset
	if limit <= 0 || limit > 10000 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var cutoff *time.Time
	if olderThanDays != nil && *olderThanDays > 0 {
		ts := time.Now().AddDate(0, 0, -*olderThanDays)
		cutoff = &ts
	}

	books, err := svc.store.ListSoftDeletedBooks(limit, offset, cutoff)
	if err != nil {
		return nil, err
	}

	if books == nil {
		books = []database.Book{}
	}

	return books, nil
}

// PurgeSoftDeletedBooks permanently deletes soft-deleted audiobooks
func (svc *AudiobookService) PurgeSoftDeletedBooks(ctx context.Context, deleteFiles bool, olderThanDays *int) (*PurgeResult, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	var cutoff *time.Time
	if olderThanDays != nil && *olderThanDays > 0 {
		ts := time.Now().AddDate(0, 0, -*olderThanDays)
		cutoff = &ts
	}

	books, err := svc.store.ListSoftDeletedBooks(1_000_000, 0, cutoff)
	if err != nil {
		return nil, err
	}

	result := &PurgeResult{
		Attempted: len(books),
		Errors:    []string{},
	}

	for _, book := range books {
		// Tombstone external IDs so reimport is blocked
		if eidStore := asExternalIDStore(svc.store); eidStore != nil {
			extIDs, _ := eidStore.GetExternalIDsForBook(book.ID)
			for _, ext := range extIDs {
				_ = eidStore.TombstoneExternalID(ext.Source, ext.ExternalID)
			}
		}

		// Defense-in-depth: enqueue iTunes removes for any PIDs still
		// on this book. Soft-delete already enqueues these but if the
		// book was soft-deleted before that hook existed, this is the
		// last chance to clean iTunes before the row vanishes.
		bookCopy := book
		svc.enqueueITunesRemovesForBook(book.ID, &bookCopy)

		// Step 1: Create tombstone (snapshot of book for rollback)
		if err := svc.store.CreateBookTombstone(&book); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: failed to create tombstone: %v", book.ID, err))
			continue
		}

		// Step 2: Delete from database (book record gone, tombstone preserved)
		if err := svc.store.DeleteBook(book.ID); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: failed to delete DB record: %v", book.ID, err))
			// Tombstone exists but book still exists — sweeper will clean up tombstone
			continue
		}

		// Step 3: Delete file if requested (only from organizer root, never from protected/import paths)
		if deleteFiles && book.FilePath != "" {
			if isProtectedPath(svc.store, book.FilePath) {
				slog.Debug("purge skipping file deletion for — protected path", "bookID", book.ID, "filePath", book.FilePath)
			} else {
				info, statErr := os.Stat(book.FilePath)
				if statErr == nil && info.IsDir() {
					// Directory-based book: remove all book files then the directory
					if bookFiles, bfErr := svc.store.GetBookFiles(book.ID); bfErr == nil {
						for _, bf := range bookFiles {
							if bf.FilePath != "" && !isProtectedPath(svc.store, bf.FilePath) {
								if rmErr := os.Remove(bf.FilePath); rmErr != nil && !os.IsNotExist(rmErr) {
									result.Errors = append(result.Errors, fmt.Sprintf("%s: failed to delete book file %s: %v", book.ID, bf.FilePath, rmErr))
								}
							}
						}
					}
					// Remove the directory if it is now empty
					if entries, rdErr := os.ReadDir(book.FilePath); rdErr == nil && len(entries) == 0 {
						if rmErr := os.Remove(book.FilePath); rmErr != nil && !os.IsNotExist(rmErr) {
							result.Errors = append(result.Errors, fmt.Sprintf("%s: failed to remove empty dir %s: %v", book.ID, book.FilePath, rmErr))
						} else if rmErr == nil {
							result.FilesDeleted++
							// Also clean up empty parent dirs up to RootDir
							if config.AppConfig.RootDir != "" {
								parentDir := filepath.Dir(book.FilePath)
								for parentDir != config.AppConfig.RootDir &&
									strings.HasPrefix(parentDir, config.AppConfig.RootDir) &&
									parentDir != "/" {
									pe, peErr := os.ReadDir(parentDir)
									if peErr != nil || len(pe) > 0 {
										break
									}
									if os.Remove(parentDir) != nil {
										break
									}
									parentDir = filepath.Dir(parentDir)
								}
							}
						}
					}
				} else if statErr == nil {
					// Single-file book
					if err := os.Remove(book.FilePath); err != nil && !os.IsNotExist(err) {
						result.Errors = append(result.Errors, fmt.Sprintf("%s: failed to delete file (tombstone preserved): %v", book.ID, err))
						// DB record gone, file still exists, tombstone preserved for sweeper
					} else if err == nil {
						result.FilesDeleted++
						// Clean up empty parent dirs up to RootDir
						if config.AppConfig.RootDir != "" {
							parentDir := filepath.Dir(book.FilePath)
							for parentDir != config.AppConfig.RootDir &&
								strings.HasPrefix(parentDir, config.AppConfig.RootDir) &&
								parentDir != "/" {
								pe, peErr := os.ReadDir(parentDir)
								if peErr != nil || len(pe) > 0 {
									break
								}
								if os.Remove(parentDir) != nil {
									break
								}
								parentDir = filepath.Dir(parentDir)
							}
						}
					}
				}
				// If statErr is os.IsNotExist, file is already gone — that's fine
			}
		}

		// Step 4: Clean up tombstone (best-effort — sweeper handles failures)
		_ = svc.store.DeleteBookTombstone(book.ID)

		result.Purged++
	}

	if result.Purged > 0 {
		svc.InvalidateBookCaches()
	}

	return result, nil
}

// RestoreAudiobook restores a soft-deleted audiobook
func (svc *AudiobookService) RestoreAudiobook(ctx context.Context, id string) (*database.Book, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	book, err := svc.store.GetBookByID(id)
	if err != nil || book == nil {
		return nil, fmt.Errorf("audiobook not found")
	}

	// Restore to imported state so the UI can re-process if needed
	book.MarkedForDeletion = boolPtr(false)
	book.MarkedForDeletionAt = nil
	book.LibraryState = stringPtr("imported")

	updated, err := svc.store.UpdateBook(id, book)
	if err != nil {
		return nil, err
	}

	svc.InvalidateBookCaches()
	return updated, nil
}

// CountAudiobooks returns the total count of audiobooks
func (svc *AudiobookService) CountAudiobooks(ctx context.Context) (int, error) {
	if svc.store == nil {
		return 0, fmt.Errorf("database not initialized")
	}

	count, err := svc.store.CountPrimaryBooks()
	if err != nil {
		return 0, err
	}
	return count, nil
}
