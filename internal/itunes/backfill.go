// file: internal/itunes/backfill.go
// version: 1.9.0
// guid: b8c9d0e1-f2a3-b4c5-d6e7-f8a9b0c1d2e3
// last-edited: 2026-09-12

package itunes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ExternalIDBackfillStore defines the store interface needed for external ID backfill.
type ExternalIDBackfillStore interface {
	// GetAllBooksCore returns the memdb-slim projection — the backfill loops
	// only read Core-safe fields (ITunesPersistentID, ID) off each book.
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	// GetAllBookFilesCore is the one batch read of book_file rows the
	// backfill makes (PERF-5). It replaces a GetBookFiles(bookID) point read
	// per book — under Pebble-direct that was one prefix iterator per book,
	// i.e. ~100k random reads on a production-sized library at every boot.
	// The Core projection carries the only three fields needed here
	// (BookID, FilePath, ITunesPersistentID).
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	CreateExternalIDMapping(mapping *database.ExternalIDMapping) error
	BulkCreateExternalIDMappings(mappings []database.ExternalIDMapping) error
	GetSetting(key string) (*database.Setting, error)
	SetSetting(key, value, dataType string, internal bool) error
}

// ExternalIDBackfillDoneKey is the settings key that records a completed
// external-ID backfill. BackfillExternalIDsOnce reads it; BackfillExternalIDs
// writes it, and ONLY after every pass has finished without an error or a
// cancellation — a partial, cancelled, or errored run never sets it, so a set
// flag guarantees that at least one full v4 pass (book-level PIDs, file-level
// PIDs, and — when an iTunes XML path is configured — track-level PIDs from
// the XML) ran to completion against the library as it stood at that time.
//
// The flag says nothing about books created AFTER that pass. Those do not
// need it: the live import path (internal/itunes/service/importer.go,
// Execute) writes a mapping for every track PID at the moment it creates a
// book, and generated file PIDs go through TrackProvisioner, which writes
// its mapping alongside the PID. The backfill exists to catch up rows that
// pre-date those paths, which is exactly the one-time job the flag records.
// Merges and dedups move mappings via ReassignExternalIDs, not via a rerun.
//
// Note that a rerun would not repair drift either: BulkCreateExternalIDMappings
// has ignore-existing semantics, so before this flag was read the every-boot
// rerun cost a full library scan and wrote nothing new.
const ExternalIDBackfillDoneKey = "external_id_backfill_v4_done"

const (
	// externalIDBackfillDoneFull: every pass ran, including the iTunes XML
	// track-PID pass.
	externalIDBackfillDoneFull = "true"
	// externalIDBackfillDoneBooksOnly: the book + file passes ran, but the
	// track-PID pass was skipped because no iTunes XML path was configured.
	// BackfillExternalIDsOnce treats this as done only while that is still
	// the case — configuring an XML path later makes the next boot run the
	// whole backfill (idempotent) so the XML pass finally happens.
	externalIDBackfillDoneBooksOnly = "books_only"
)

// backfillChunkSize bounds each BulkCreateExternalIDMappings write in the
// book pass and sets how often progress is reported. It is a slice stride
// over an in-memory snapshot, not a store page size.
const backfillChunkSize = 10000

// loadBackfillBooks enumerates the whole library in ONE store call (limit 0
// is unbounded on both store paths) instead of offset pages (PERF-5). Offset
// pages are each served from whichever memdb snapshot is current, and the
// reconciler swaps snapshots asynchronously: a swap, insert, or soft-delete
// between page N and N+1 shifts every position, silently skipping or
// repeating rows — and BackfillExternalIDs would then set its done flag over
// a library it never fully read. One call reads one consistent snapshot, so
// there is no cross-page window. This is the same fix reconcile's
// loadAllBooksCore applied to AssignOrphanVGs, which skipped 1 of 40 books in
// CI and reported success.
//
// It deliberately does not use GetAllBooksFullFrom's ID cursor, although
// that is the pager the PERF-5 TODO item names:
//   - Its memdb branch (the production default) walks ListBookIDs on every
//     page and then point-reads each book from Pebble with sidecar hydration
//     — ~one Pebble read per book, the exact per-book read pattern PERF-5
//     removed from this file, to fetch heavy fields nothing here reads (only
//     ID, Title and ITunesPersistentID are used, all Core-safe).
//   - On that branch an afterID that has vanished from the ID list (the
//     cursor book soft-deleted between pages) ends iteration with no error,
//     which would turn "skip a row" into "stop early and mark done".
//
// The cost is that the whole Core projection is resident at once rather
// than 10,000 rows at a time. BackfillITunesTrackPIDs already keeps a
// per-book index for the whole library, so the peak is the same order.
func loadBackfillBooks(store ExternalIDBackfillStore) ([]database.BookCore, error) {
	return store.GetAllBooksCore(0, 0)
}

// itunesXMLConfigured reports whether the iTunes XML track-PID pass can run.
func itunesXMLConfigured() bool {
	return config.AppConfig.ITunes.LibraryReadPath != ""
}

// externalIDBackfillDone reports whether a previous BackfillExternalIDs run
// left the done flag in a state that makes another run redundant. A missing
// flag, an unreadable flag, or an unrecognised value all answer false: the
// safe direction is to run (the writes are idempotent), never to skip.
func externalIDBackfillDone(store ExternalIDBackfillStore) (bool, string) {
	setting, err := store.GetSetting(ExternalIDBackfillDoneKey)
	if err != nil {
		if !errors.Is(err, database.ErrSettingNotFound) {
			slog.Warn("external ID backfill: could not read done flag; running the backfill", "key", ExternalIDBackfillDoneKey, "err", err)
		}
		return false, ""
	}
	if setting == nil {
		return false, ""
	}
	switch setting.Value {
	case externalIDBackfillDoneFull:
		return true, setting.Value
	case externalIDBackfillDoneBooksOnly:
		return !itunesXMLConfigured(), setting.Value
	}
	return false, setting.Value
}

// BackfillExternalIDsOnce is the boot-time entry point: it runs
// BackfillExternalIDs only if no earlier run has recorded completion under
// ExternalIDBackfillDoneKey (see that constant for what "completion" means).
// Before this gate existed the full-library scan ran unconditionally at every
// server start (SQ-04). Operator-triggered reruns should call
// BackfillExternalIDs directly, which ignores the flag.
func BackfillExternalIDsOnce(ctx context.Context, store ExternalIDBackfillStore, progress func(processed, total int, msg string)) error {
	if store == nil {
		return nil
	}
	if done, value := externalIDBackfillDone(store); done {
		slog.Info("External ID backfill v4 already complete; skipping", "key", ExternalIDBackfillDoneKey, "value", value)
		if progress != nil {
			progress(0, 0, "External ID backfill already complete; skipped")
		}
		return nil
	}
	return BackfillExternalIDs(ctx, store, progress)
}

// BackfillExternalIDs scans all books and creates external ID mappings for any
// book that has an iTunes PersistentID set. It ALWAYS runs — the done flag
// under ExternalIDBackfillDoneKey is written here after a fully successful
// pass but is only consulted by BackfillExternalIDsOnce (the boot path). The
// manual maintenance op calls this function directly so an operator's
// explicit request is never silently skipped. The writes are idempotent
// (BulkCreateExternalIDMappings ignores existing keys), so a rerun is safe.
//
// ctx is honored at every batch boundary AND between book entries so a
// shutdown signal aborts the backfill quickly. Without ctx-awareness this
// loop runs to completion (potentially minutes on a large library) and
// crashes with "pebble: closed" if the store closes mid-iteration. The
// caller (Server.Start's background goroutine) passes s.bgCtx.
//
// progress is called after every page of the book-pagination pass (total is
// unknown ahead of a pass over a paginated store, so 0 is reported for it —
// the message still conveys real counts) and every 10000 tracks during the
// iTunes-XML streaming pass, so a whole-library run surfaces progress instead
// of one log line at the end (H7). progress may be nil.
//
// Persistence errors (bulk-write failures, the track-PID pass) are now
// returned instead of silently discarded — previously the op always reported
// success even when nothing was actually written (H7).
func BackfillExternalIDs(ctx context.Context, store ExternalIDBackfillStore, progress func(processed, total int, msg string)) error {
	if store == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	slog.Info("Starting external ID backfill v4...")

	// PERF-5: one batch read of every book_file row, reduced to the
	// PID-bearing subset keyed by book ID, replaces the per-book
	// GetBookFiles point read the page loop below used to make. The Core
	// projection is memdb-slim, and only files that actually carry a PID are
	// retained, so the resident set is bounded by the iTunes-linked file
	// count rather than the whole table. The book pagination (pages of
	// 10,000) is unchanged.
	if err := ctx.Err(); err != nil {
		slog.Info("external ID backfill canceled before file read", "err", err)
		return nil
	}
	allFiles, err := store.GetAllBookFilesCore()
	if err != nil {
		return fmt.Errorf("external ID backfill: read book files: %w", err)
	}
	// allFiles is dead after this loop; only the PID subset stays resident.
	filesByBook := make(map[string][]database.BookFileCore)
	for _, f := range allFiles {
		if f.ITunesPersistentID == "" {
			continue
		}
		filesByBook[f.BookID] = append(filesByBook[f.BookID], f)
	}

	if err := ctx.Err(); err != nil {
		slog.Info("external ID backfill canceled before book read", "err", err)
		return nil
	}
	allBooks, err := loadBackfillBooks(store)
	if err != nil {
		// H7: a read failure here used to `break` silently, letting the
		// loop fall through to the track-PID pass and mark the whole
		// backfill "done" despite skipping the rest of the library.
		return fmt.Errorf("external ID backfill: read books: %w", err)
	}

	// The snapshot is walked in chunks only to bound each bulk write and to
	// report progress per chunk; there is no store read between chunks, so no
	// cross-page window. Per-book work is a map lookup and slice appends (no
	// I/O), so the walk stays sequential — the one write per chunk is the
	// only store call, and BulkCreateExternalIDMappings is a single batch.
	backfilled := 0
	booksScanned := 0
	for start := 0; start < len(allBooks); start += backfillChunkSize {
		if err := ctx.Err(); err != nil {
			slog.Info("external ID backfill canceled between chunks after mappings", "books_scanned", booksScanned, "backfilled", backfilled, "err", err)
			return nil
		}
		books := allBooks[start:min(start+backfillChunkSize, len(allBooks))]

		// Accumulate per-chunk so we can flush with a single bulk write rather
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
			// — served from the single batch read above (PERF-5); only
			// PID-bearing files were retained, so no per-file filter is needed.
			for _, f := range filesByBook[book.ID] {
				batch = append(batch, database.ExternalIDMapping{
					Source:     "itunes",
					ExternalID: f.ITunesPersistentID,
					BookID:     book.ID,
					FilePath:   f.FilePath,
					Provenance: "backfill_v4",
				})
			}
		}
		if len(batch) > 0 {
			if err := store.BulkCreateExternalIDMappings(batch); err != nil {
				return fmt.Errorf("external ID backfill: bulk write after %d books: %w", booksScanned, err)
			}
			backfilled += len(batch)
		}
		booksScanned += len(books)
		if progress != nil {
			progress(booksScanned, 0, fmt.Sprintf("Backfilling external IDs: %d books scanned (%d mappings written)", booksScanned, backfilled))
		}
	}

	if err := ctx.Err(); err != nil {
		slog.Info("external ID backfill canceled before track-PID pass", "err", err)
		return nil
	}
	slog.Info("Backfilled external ID mappings from book + file records", "backfilled", backfilled)

	// Backfill ALL track-level PIDs from the iTunes XML
	itunesBackfilled, err := BackfillITunesTrackPIDs(ctx, store, progress)
	if err != nil {
		return fmt.Errorf("external ID backfill: track-PID pass: %w", err)
	}
	if itunesBackfilled > 0 {
		slog.Info("Backfilled track-level PIDs from iTunes XML", "itunesBackfilled", itunesBackfilled)
	}

	// Only mark as done AFTER everything completes successfully (and not
	// canceled). Every error path above has already returned, and every
	// cancellation path returned nil without reaching here, so this write is
	// the one thing that can make BackfillExternalIDsOnce skip on the next
	// boot — see ExternalIDBackfillDoneKey for what it guarantees. The value
	// records whether the XML track-PID pass actually ran, so a later
	// configuration of the XML path re-arms the boot-time run.
	if err := ctx.Err(); err != nil {
		slog.Info("external ID backfill canceled after track-PID pass; not marking done", "err", err)
		return nil
	}
	doneValue := externalIDBackfillDoneFull
	if !itunesXMLConfigured() {
		doneValue = externalIDBackfillDoneBooksOnly
	}
	if err := store.SetSetting(ExternalIDBackfillDoneKey, doneValue, "string", false); err != nil {
		// The data is complete; only the marker failed. The next boot will
		// rerun (safe, idempotent) — say so instead of dropping the error.
		slog.Warn("external ID backfill complete but could not record done flag; next boot will rerun", "key", ExternalIDBackfillDoneKey, "err", err)
		return nil
	}
	slog.Info("External ID backfill v4 complete", "done_value", doneValue)
	return nil
}

// BackfillITunesTrackPIDs reads the iTunes XML and registers ALL track PIDs
// for existing books. This catches the multi-track albums where only the first
// track's PID was stored on the book record.
//
// Uses streaming XML parser to avoid loading the entire library into memory.
// ctx aborts the stream and book index operations; passing context.Background is safe.
// progress is called every 10000 tracks streamed (may be nil). Bulk-write
// failures now abort the stream and return an error instead of being logged
// and discarded (H7) — a caller relying on the returned count/error to decide
// whether the backfill "done" flag can be set needs it to be accurate.
func BackfillITunesTrackPIDs(ctx context.Context, store ExternalIDBackfillStore, progress func(processed, total int, msg string)) (int, error) {
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
	if err := ctx.Err(); err != nil {
		slog.Info("BackfillITunesTrackPIDs canceled before index build", "err", err)
		return 0, nil
	}
	// One snapshot read, not offset pages — see loadBackfillBooks (PERF-5).
	books, err := loadBackfillBooks(store)
	if err != nil {
		// H7: a read failure here used to `break` silently and fall through
		// to a stream parse over a partial (or empty) PID/title index.
		return 0, fmt.Errorf("BackfillITunesTrackPIDs: read books: %w", err)
	}
	// Sequential on purpose: per-book work is two map writes with no I/O,
	// and the maps are shared, so a worker pool would only add locking.
	for _, book := range books {
		if book.ITunesPersistentID != nil && *book.ITunesPersistentID != "" {
			pidToBook[*book.ITunesPersistentID] = book.ID
		}
		titleToBook[strings.ToLower(strings.TrimSpace(book.Title))] = book.ID
	}
	totalBooks := len(books)
	slog.Info("BackfillITunesTrackPIDs loaded books ( PIDs, titles)", "totalBooks", totalBooks, "pidToBook_count", len(pidToBook), "titleToBook_count", len(titleToBook))

	// Stream-parse tracks and register PIDs
	registered := 0
	var batch []database.ExternalIDMapping
	var currentAlbum string
	var currentAlbumTracks []*Track

	trackedCount := 0
	_, err = StreamingParseLibrary(ctx, xmlPath, func(track *Track) error {
		trackedCount++
		if trackedCount%10000 == 0 {
			slog.Info("BackfillITunesTrackPIDs streaming progress", "tracks_processed", trackedCount)
			if progress != nil {
				progress(trackedCount, 0, fmt.Sprintf("Backfilling iTunes track PIDs: %d tracks scanned (%d registered)", trackedCount, registered))
			}
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
							return fmt.Errorf("BackfillITunesTrackPIDs: bulk write mid-stream: %w", batchErr)
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
			return registered, fmt.Errorf("BackfillITunesTrackPIDs: bulk write final batch: %w", batchErr)
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
