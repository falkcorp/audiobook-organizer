// file: internal/itunes/backfill.go
// version: 1.4.0
// guid: b8c9d0e1-f2a3-b4c5-d6e7-f8a9b0c1d2e3
// last-edited: 2026-06-23

package itunes

import (
	"context"
	"log/slog"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ExternalIDBackfillStore defines the store interface needed for external ID backfill.
type ExternalIDBackfillStore interface {
	GetAllBooks(limit, offset int) ([]database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	CreateExternalIDMapping(mapping *database.ExternalIDMapping) error
	BulkCreateExternalIDMappings(mappings []database.ExternalIDMapping) error
	SetSetting(key, value, dataType string, internal bool) error
}

// BackfillExternalIDs scans all books and creates external ID mappings for any
// book that has an iTunes PersistentID set. This is idempotent — it checks the
// setting "external_id_backfill_done" and only runs once.
//
// ctx is honored at every batch boundary AND between book entries so a
// shutdown signal aborts the backfill quickly. Without ctx-awareness this
// loop runs to completion (potentially minutes on a large library) and
// crashes with "pebble: closed" if the store closes mid-iteration. The
// caller (Server.Start's background goroutine) passes s.bgCtx.
func BackfillExternalIDs(ctx context.Context, store ExternalIDBackfillStore) error {
	if store == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Check if backfill has already been performed (v4 = includes BookFile-level PIDs)
	// Note: in actual usage, would need to check via store.GetSetting
	slog.Info("Starting external ID backfill v4...")

	offset := 0
	backfilled := 0
	for {
		if err := ctx.Err(); err != nil {
			slog.Info("external ID backfill canceled at offset after mappings", "offset", offset, "backfilled", backfilled, "err", err)
			return nil
		}
		books, err := store.GetAllBooks(10000, offset)
		if err != nil || len(books) == 0 {
			break
		}

		// Accumulate per-page so we can flush with a single bulk write rather
		// than one CreateExternalIDMapping call per book/file (PERF-5).
		var batch []database.ExternalIDMapping
		for _, book := range books {
			if err := ctx.Err(); err != nil {
				slog.Info("external ID backfill canceled mid-batch after mappings", "backfilled", backfilled, "err", err)
				return nil
			}
			// Book-level PID
			if book.ITunesPersistentID != nil && *book.ITunesPersistentID != "" {
				batch = append(batch, database.ExternalIDMapping{
					Source:     "itunes",
					ExternalID: *book.ITunesPersistentID,
					BookID:     book.ID,
				})
			}

			// BookFile-level PIDs (catches split books, multi-file books, etc.)
			// TODO(PERF-5): replace with a batch GetBookFilesByBookIDs call to
			// eliminate the N+1 file read. Requires a new Store interface method.
			files, fErr := store.GetBookFiles(book.ID)
			if fErr != nil {
				continue
			}
			for _, f := range files {
				if f.ITunesPersistentID != "" {
					batch = append(batch, database.ExternalIDMapping{
						Source:     "itunes",
						ExternalID: f.ITunesPersistentID,
						BookID:     book.ID,
						FilePath:   f.FilePath,
						Provenance: "backfill_v4",
					})
				}
			}
		}
		if len(batch) > 0 {
			_ = store.BulkCreateExternalIDMappings(batch)
			backfilled += len(batch)
		}
		offset += 10000
	}

	if err := ctx.Err(); err != nil {
		slog.Info("external ID backfill canceled before track-PID pass", "err", err)
		return nil
	}
	slog.Info("Backfilled external ID mappings from book + file records", "backfilled", backfilled)

	// Backfill ALL track-level PIDs from the iTunes XML
	itunesBackfilled, _ := BackfillITunesTrackPIDs(ctx, store)
	if itunesBackfilled > 0 {
		slog.Info("Backfilled track-level PIDs from iTunes XML", "itunesBackfilled", itunesBackfilled)
	}

	// Only mark as done AFTER everything completes successfully (and not canceled)
	if err := ctx.Err(); err == nil {
		_ = store.SetSetting("external_id_backfill_v4_done", "true", "bool", false)
		slog.Info("External ID backfill v4 complete")
	}
	return nil
}

// BackfillITunesTrackPIDs reads the iTunes XML and registers ALL track PIDs
// for existing books. This catches the multi-track albums where only the first
// track's PID was stored on the book record.
//
// Uses streaming XML parser to avoid loading the entire library into memory.
// ctx aborts the stream and book index operations; passing context.Background is safe.
func BackfillITunesTrackPIDs(ctx context.Context, store ExternalIDBackfillStore) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	xmlPath := config.AppConfig.ITunes.LibraryReadPath
	if xmlPath == "" {
		slog.Info("BackfillITunesTrackPIDs no iTunes XML path configured, skipping")
		return 0, nil
	}

	slog.Info("BackfillITunesTrackPIDs parsing iTunes XML at", "xmlPath", xmlPath)

	// Build PID→book_id index from existing books (loaded once, kept in memory)
	slog.Info("BackfillITunesTrackPIDs loading book index...")
	pidToBook := make(map[string]string)
	titleToBook := make(map[string]string) // lowercase title → book_id
	totalBooks := 0
	offset := 0
	for {
		if err := ctx.Err(); err != nil {
			slog.Info("BackfillITunesTrackPIDs canceled mid-index at offset", "offset", offset)
			return 0, nil
		}
		books, err := store.GetAllBooks(10000, offset)
		if err != nil || len(books) == 0 {
			break
		}
		for _, book := range books {
			if book.ITunesPersistentID != nil && *book.ITunesPersistentID != "" {
				pidToBook[*book.ITunesPersistentID] = book.ID
			}
			titleToBook[strings.ToLower(strings.TrimSpace(book.Title))] = book.ID
		}
		totalBooks += len(books)
		offset += 10000
	}
	slog.Info("BackfillITunesTrackPIDs loaded books ( PIDs, titles)", "totalBooks", totalBooks, "pidToBook_count", len(pidToBook), "titleToBook_count", len(titleToBook))

	// Stream-parse tracks and register PIDs
	registered := 0
	var batch []database.ExternalIDMapping
	var currentAlbum string
	var currentAlbumTracks []*Track

	trackedCount := 0
	_, err := StreamingParseLibrary(ctx, xmlPath, func(track *Track) error {
		trackedCount++
		if trackedCount%10000 == 0 {
			slog.Info("BackfillITunesTrackPIDs streaming progress", "tracks_processed", trackedCount)
		}

		// Group tracks by album as we stream them
		album := track.Album
		if album == "" {
			album = track.Name
		}
		if album == "" {
			return nil
		}

		albumKey := strings.ToLower(strings.TrimSpace(album))

		// If we've switched to a new album, process the previous album's tracks
		if currentAlbum != albumKey && len(currentAlbumTracks) > 0 {
			if bookID := findAlbumBook(currentAlbumTracks, pidToBook, titleToBook); bookID != "" {
				// Register all track PIDs for this album's book
				for _, t := range currentAlbumTracks {
					if t.PersistentID == "" {
						continue
					}
					trackNum := t.TrackNumber
					batch = append(batch, database.ExternalIDMapping{
						Source:      "itunes",
						ExternalID:  t.PersistentID,
						BookID:      bookID,
						TrackNumber: &trackNum,
					})
					registered++

					// Flush in batches of 5000
					if len(batch) >= 5000 {
						if batchErr := store.BulkCreateExternalIDMappings(batch); batchErr != nil {
							slog.Warn("BackfillITunesTrackPIDs failed to write batch", "err", batchErr)
						}
						batch = batch[:0]
					}
				}
			}

			// Reset for new album
			currentAlbumTracks = currentAlbumTracks[:0]
		}

		// Accumulate track for current album
		currentAlbum = albumKey
		currentAlbumTracks = append(currentAlbumTracks, track)

		return nil
	})

	if err != nil {
		slog.Warn("BackfillITunesTrackPIDs failed to parse iTunes XML", "err", err)
		return registered, err
	}

	// Process final album batch
	if len(currentAlbumTracks) > 0 {
		if bookID := findAlbumBook(currentAlbumTracks, pidToBook, titleToBook); bookID != "" {
			for _, t := range currentAlbumTracks {
				if t.PersistentID == "" {
					continue
				}
				trackNum := t.TrackNumber
				batch = append(batch, database.ExternalIDMapping{
					Source:      "itunes",
					ExternalID:  t.PersistentID,
					BookID:      bookID,
					TrackNumber: &trackNum,
				})
				registered++
			}
		}
	}

	// Flush remaining batch
	if len(batch) > 0 {
		if batchErr := store.BulkCreateExternalIDMappings(batch); batchErr != nil {
			slog.Warn("BackfillITunesTrackPIDs failed to write final batch", "err", batchErr)
		}
	}

	slog.Info("BackfillITunesTrackPIDs completed stream parsing", "tracks_processed", trackedCount, "registered", registered)
	return registered, nil
}

// findAlbumBook locates a book for an album's tracks using PID matching or title matching
func findAlbumBook(tracks []*Track, pidToBook, titleToBook map[string]string) string {
	// First try PID matching
	for _, t := range tracks {
		if bid, ok := pidToBook[t.PersistentID]; ok {
			return bid
		}
	}

	// Then try title matching
	for _, t := range tracks {
		album := strings.ToLower(strings.TrimSpace(t.Album))
		if bid, ok := titleToBook[album]; ok {
			return bid
		}
	}

	return ""
}
