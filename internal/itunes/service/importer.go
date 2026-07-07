// file: internal/itunes/service/importer.go
// version: 1.10.0
// guid: 2b8e5f1a-4c7d-4e9f-b3a0-6d8c2e7a4f1b
// last-edited: 2026-07-07

package itunesservice

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/oklog/ulid/v2"
)

// itlState guards the last ITL read time for conflict detection.
var itlState struct {
	mu       sync.Mutex
	lastRead time.Time
}

// RecordITLReadTime stamps now as the last ITL read.
func RecordITLReadTime() {
	itlState.mu.Lock()
	itlState.lastRead = time.Now()
	itlState.mu.Unlock()
}

// CheckITLConflict returns an error if the ITL file at path has been
// externally modified since the last recorded read.
func CheckITLConflict(itlPath string) error {
	itlState.mu.Lock()
	lastRead := itlState.lastRead
	itlState.mu.Unlock()

	if lastRead.IsZero() {
		return nil
	}
	stat, err := os.Stat(itlPath)
	if err != nil {
		return nil
	}
	if stat.ModTime().After(lastRead.Add(2 * time.Second)) {
		return fmt.Errorf("ITL conflict: file modified at %v (our last read: %v) — re-sync before writing",
			stat.ModTime(), lastRead)
	}
	return nil
}

// albumGroup holds tracks belonging to the same album (book).
type albumGroup struct {
	key    string
	tracks []*itunes.Track
}

// trackDurationSeconds converts an iTunes track's TotalTime (milliseconds) to the
// seconds unit used by BookFile.Duration everywhere else in the codebase (see
// track_provisioner.go: `bookFile.Duration * 1000 // seconds to ms`). CONS-16:
// storing the raw ms value here let RecomputeBookAggregates sum inflated per-file
// durations and clobber the correct seconds-valued Book.Duration.
func trackDurationSeconds(t *itunes.Track) int {
	return int(t.TotalTime / 1000)
}

// Importer runs the iTunes import pipeline and incremental sync.
type Importer struct {
	store            Store
	activityFn       func(database.ActivityEntry)
	eventBus         plugin.EventPublisher // may be nil; replaces OnBookCreated
	cfg              Config
	log              logger.Logger
	mfs              metadataFetcher
	organizerFactory func() BookOrganizer
	statusMap        importStatusMap

	// organizeConcurrencyOverride, when > 0, overrides the worker-pool size
	// organizeImportedBooks passes to registry.RunItems. Zero (the default
	// for every real Importer) means "use runtime.NumCPU()". This exists
	// purely as a test seam so a test can force the sequential path (1)
	// and diff it against the parallel path (see organizeConcurrency).
	organizeConcurrencyOverride int

	// enrichConcurrencyOverride, when > 0, overrides the worker-pool size
	// enrichImportedBooks passes to registry.RunItems. Zero (the default
	// for every real Importer) means "use enrichConcurrency". Same test
	// seam purpose as organizeConcurrencyOverride: lets a test force the
	// sequential path (1) and diff it against a parallel path.
	enrichConcurrencyOverride int
}

// metadataFetcher is the narrow, single-method subset of *metafetch.Service
// that enrichImportedBooks depends on. enrichImportedBooks calls this
// interface (rather than *metafetch.Service directly) purely as a test seam:
// it lets tests fake FetchMetadataForBook's success/failure/timing to
// exercise registry.RunItems' parallel fan-out and the CONC-11 circuit
// breaker below without wiring a real metadata.MetadataSource +
// database.Store pair through a live metafetch.Service. *metafetch.Service
// satisfies this interface unmodified — newImporter's wiring is unaffected.
type metadataFetcher interface {
	FetchMetadataForBook(id string) (*metafetch.FetchMetadataResponse, error)
}

func newImporter(deps Deps) *Importer {
	return &Importer{
		store:            deps.Store,
		activityFn:       deps.ActivityFn,
		eventBus:         deps.EventBus,
		cfg:              deps.Config,
		log:              deps.Logger,
		mfs:              deps.Metafetch,
		organizerFactory: deps.OrganizerFactory,
	}
}

// publishBookCreated emits a plugin.EventBookImported for bookID via the
// event bus, if configured. Replaces the pre-PLUGIN-DECOUPLE OnBookCreated
// callback. Subscribers (dedup engine, webhook plugin, etc.) receive the
// event asynchronously and handle it with their own background contexts.
func (imp *Importer) publishBookCreated(ctx context.Context, bookID string) {
	if imp.eventBus == nil || bookID == "" {
		return
	}
	imp.eventBus.Publish(ctx, plugin.NewEvent(plugin.EventBookImported, bookID, map[string]any{
		"source": "itunes",
	}))
}

// GetStatus returns an exported counter snapshot for opID.
func (imp *Importer) GetStatus(opID string) *ImportStatusSnapshot {
	s := imp.statusMap.snapshot(opID)
	if s == nil {
		return &ImportStatusSnapshot{}
	}
	return &ImportStatusSnapshot{
		Total: s.Total, Processed: s.Processed, Imported: s.Imported,
		Skipped: s.Skipped, Linked: s.Linked, Failed: s.Failed, Errors: s.Errors,
	}
}

// GetStatusBulk returns exported counter snapshots for multiple operation IDs.
func (imp *Importer) GetStatusBulk(ids []string) map[string]*ImportStatusSnapshot {
	out := make(map[string]*ImportStatusSnapshot, len(ids))
	for _, id := range ids {
		out[id] = imp.GetStatus(id)
	}
	return out
}

// Execute runs a full iTunes import. opID is the tracked operation; req is
// the user-supplied parameters; log is a progress-aware logger.
func (imp *Importer) Execute(ctx context.Context, opID string, req ImportRequest, log logger.Logger) error {
	pathMappings := make(map[string]string)
	for _, pm := range req.PathMappings {
		pathMappings[pm.From] = pm.To
	}
	_ = operations.SaveParams(imp.store, opID, operations.ITunesImportParams{
		LibraryXMLPath: req.LibraryPath,
		LibraryPath:    req.LibraryPath,
		ImportMode:     req.ImportMode,
		PathMappings:   pathMappings,
		SkipDuplicates: req.SkipDuplicates,
		EnrichMetadata: req.FetchMetadata,
		AutoOrganize:   !req.PreserveLocation,
	})

	checkpoint, _ := operations.LoadCheckpoint(imp.store, opID)
	resumeIndex := 0
	if checkpoint != nil && checkpoint.Phase == "importing" {
		resumeIndex = checkpoint.PhaseIndex
		log.Info("Resuming import from album %d/%d", resumeIndex, checkpoint.PhaseTotal)
	}

	status := imp.statusMap.load(opID)
	log.UpdateProgress(0, 0, "Parsing iTunes XML library...")
	log.Info("Parsing iTunes XML library: %s", req.LibraryPath)

	library, err := itunes.ParseLibrary(req.LibraryPath)
	if err != nil {
		recordImportError(status, fmt.Sprintf("failed to parse library: %v", err))
		operations.ClearState(imp.store, opID)
		return fmt.Errorf("failed to parse library: %w", err)
	}

	log.UpdateProgress(0, 0, fmt.Sprintf("Parsed %d tracks, grouping into albums...", len(library.Tracks)))
	log.Info("Parsed %d tracks, grouping into albums...", len(library.Tracks))

	groups := imp.groupTracksByAlbum(library)
	totalGroups := len(groups)
	setImportTotal(status, totalGroups)

	log.UpdateProgress(0, totalGroups, fmt.Sprintf("Found %d audiobook albums, starting import...", totalGroups))
	log.Info("Found %d audiobook albums to import (from grouped tracks)", totalGroups)
	if totalGroups == 0 {
		log.UpdateProgress(0, 0, "No audiobooks found in library")
		operations.ClearState(imp.store, opID)
		return nil
	}

	importMode := imp.resolveImportMode(req.ImportMode)
	importOpts := itunes.ImportOptions{
		LibraryPath:  req.LibraryPath,
		PathMappings: toITunesPathMappings(req.PathMappings),
	}

	var newBookIDs []string
	processed := 0
	for i, group := range groups {
		if i < resumeIndex {
			processed++
			continue
		}
		if log.IsCanceled() {
			log.Info("iTunes import canceled")
			return nil
		}

		processed++
		incImportProcessed(status, processed)

		book, err := imp.buildBookFromAlbumGroup(group, req.LibraryPath, importOpts)
		if err != nil {
			recordImportFailure(status, err.Error())
			log.Error("%s", err.Error())
			updateImportProgress(log, status, processed, totalGroups, group.key)
			continue
		}

		imp.assignAuthorAndSeries(book, group.tracks[0])

		firstTrackPath := book.FilePath
		if len(group.tracks) > 0 {
			loc := importOpts.RemapPath(group.tracks[0].Location)
			if decoded, decErr := itunes.DecodeLocation(loc); decErr == nil {
				firstTrackPath = decoded
			}
		}

		// External ID map check: tombstone + already-mapped
		if len(group.tracks) > 0 {
			firstPID := group.tracks[0].PersistentID
			if firstPID != "" {
				if tombstoned, _ := imp.store.IsExternalIDTombstoned("itunes", firstPID); tombstoned {
					updateImportProgress(log, status, processed, totalGroups, book.Title)
					continue
				}
				if bookID, err := imp.store.GetBookByExternalID("itunes", firstPID); err == nil && bookID != "" {
					if existing, err := imp.store.GetBookByID(bookID); err == nil && existing != nil {
						imp.linkITunesMetadata(existing, book, group.tracks[0], log)
						incImportLinked(status)
						updateImportProgress(log, status, processed, totalGroups, book.Title)
						continue
					}
				}
			}
		}

		if req.SkipDuplicates {
			if existing, err := imp.store.GetBookByFilePath(book.FilePath); err == nil && existing != nil {
				imp.linkITunesMetadata(existing, book, group.tracks[0], log)
				incImportLinked(status)
				updateImportProgress(log, status, processed, totalGroups, book.Title)
				continue
			}
		}

		book.LibraryState = strPtr(imp.importLibraryState(importMode))

		vgID := fmt.Sprintf("vg-%s", ulid.Make().String())
		book.VersionGroupID = strPtr(vgID)
		isPrimary := false
		book.IsPrimaryVersion = &isPrimary

		coverPath, coverErr := metadata.ExtractCoverArt(firstTrackPath)
		if coverErr == nil && coverPath != "" {
			book.CoverURL = strPtr("/api/v1/covers/local/" + filepath.Base(coverPath))
		}

		created, err := imp.store.CreateBook(book)
		if err != nil {
			recordImportFailure(status, fmt.Sprintf("Failed to save '%s': %v", book.Title, err))
			log.Error("Failed to save '%s': %v", book.Title, err)
			updateImportProgress(log, status, processed, totalGroups)
			continue
		}

		imp.publishBookCreated(ctx, created.ID)

		incImportImported(status)
		newBookIDs = append(newBookIDs, created.ID)

		for _, albumTrack := range group.tracks {
			if albumTrack.PersistentID == "" {
				continue
			}
			trackNum := albumTrack.TrackNumber
			trackLoc := importOpts.RemapPath(albumTrack.Location)
			trackPath, _ := itunes.DecodeLocation(trackLoc)
			_ = imp.store.CreateExternalIDMapping(&database.ExternalIDMapping{
				Source:      "itunes",
				ExternalID:  albumTrack.PersistentID,
				BookID:      created.ID,
				TrackNumber: &trackNum,
				FilePath:    trackPath,
			})
		}

		if len(group.tracks) > 1 {
			totalTracks := len(group.tracks)
			for _, track := range group.tracks {
				trackLoc := importOpts.RemapPath(track.Location)
				trackPath, decErr := itunes.DecodeLocation(trackLoc)
				if decErr != nil {
					continue
				}
				trackFormat := strings.TrimPrefix(strings.ToLower(filepath.Ext(trackPath)), ".")
				bf := &database.BookFile{
					ID:                 ulid.Make().String(),
					BookID:             created.ID,
					FilePath:           trackPath,
					ITunesPersistentID: track.PersistentID,
					Format:             trackFormat,
					FileSize:           track.Size,
					Duration:           trackDurationSeconds(track),
					TrackNumber:        track.TrackNumber,
					TrackCount:         totalTracks,
				}
				if segHash, hashErr := scanner.ComputeSegmentFileHash(trackPath); hashErr == nil {
					bf.FileHash = segHash
				}
				if createErr := imp.store.CreateBookFile(bf); createErr != nil {
					log.Warn("Failed to create book file for track %d of '%s': %v", track.TrackNumber, book.Title, createErr)
				}
			}
		}

		if created.AuthorID != nil && len(book.Authors) > 0 {
			for i := range book.Authors {
				book.Authors[i].BookID = created.ID
			}
			_ = imp.store.SetBookAuthors(created.ID, book.Authors)
		} else if created.AuthorID != nil {
			_ = imp.store.SetBookAuthors(created.ID, []database.BookAuthor{
				{BookID: created.ID, AuthorID: *created.AuthorID, Role: "author", Position: 0},
			})
		}

		if req.ImportPlaylists {
			tags := itunes.ExtractPlaylistTags(group.tracks[0].TrackID, library.Playlists)
			if len(tags) > 0 {
				log.Info("Playlist tags for '%s': %s", book.Title, strings.Join(tags, ", "))
			}
		}

		updateImportProgress(log, status, processed, totalGroups, book.Title)

		if processed%10 == 0 {
			_ = operations.SaveCheckpoint(imp.store, opID, "itunes_import", "importing", processed, totalGroups)
		}
	}

	quickSummary := buildImportSummary(status)
	log.UpdateProgress(totalGroups, totalGroups, "Quick import done: "+quickSummary)
	log.Info("Quick import completed: %s", quickSummary)

	// Phase 3: Hash validation
	if len(newBookIDs) > 0 && req.SkipDuplicates {
		_ = operations.SaveCheckpoint(imp.store, opID, "itunes_import", "hash_validation", 0, len(newBookIDs))
		log.UpdateProgress(totalGroups, totalGroups, fmt.Sprintf("Hash validation: checking %d new books...", len(newBookIDs)))
		log.Info("Starting hash validation for %d new books...", len(newBookIDs))

		hashLinked := 0
		hashBlocked := 0
		for hi, bookID := range newBookIDs {
			if log.IsCanceled() {
				log.Info("Hash validation canceled")
				break
			}

			book, err := imp.store.GetBookByID(bookID)
			if err != nil || book == nil {
				continue
			}

			hash, err := scanner.ComputeFileHash(book.FilePath)
			if err != nil {
				log.Warn("Hash validation: failed to hash %s: %v", book.FilePath, err)
				continue
			}
			if hash == "" {
				continue
			}

			book.FileHash = strPtr(hash)
			book.OriginalFileHash = strPtr(hash)
			if importMode == itunes.ImportModeOrganized {
				book.OrganizedFileHash = strPtr(hash)
			}

			if blocked, err := imp.store.IsHashBlocked(hash); err == nil && blocked {
				log.Warn("Hash validation: blocked hash for %s, soft-deleting", book.Title)
				marked := true
				now := time.Now()
				book.MarkedForDeletion = &marked
				book.MarkedForDeletionAt = &now
				imp.store.UpdateBook(book.ID, book)
				hashBlocked++
				continue
			}

			if existing, err := imp.store.GetBookByFileHash(hash); err == nil && existing != nil && existing.ID != book.ID {
				if existing.VersionGroupID != nil && *existing.VersionGroupID != "" {
					book.VersionGroupID = existing.VersionGroupID
					isPrimary := false
					book.IsPrimaryVersion = &isPrimary
				}
				hashLinked++
				log.Info("Hash validation: linked %s → %s via hash", book.Title, existing.ID)
			}

			if _, err := imp.store.UpdateBook(book.ID, book); err != nil {
				log.Warn("Hash validation: failed to update %s: %v", book.ID, err)
			}

			if (hi+1)%100 == 0 || hi+1 == len(newBookIDs) {
				msg := fmt.Sprintf("Hash validation: %d/%d checked (%d linked, %d blocked)",
					hi+1, len(newBookIDs), hashLinked, hashBlocked)
				log.UpdateProgress(totalGroups, totalGroups, msg)
			}
		}
		log.Info("Hash validation completed: %d linked, %d blocked out of %d new books", hashLinked, hashBlocked, len(newBookIDs))
	}

	// Phase 4: Metadata enrichment
	if req.FetchMetadata {
		_ = operations.SaveCheckpoint(imp.store, opID, "itunes_import", "enriching", 0, 0)
		log.Info("Starting metadata enrichment phase...")
		imp.enrichImportedBooks(ctx, status, log)
	}

	// Phase 5: Organize
	if importMode == itunes.ImportModeOrganize && !req.PreserveLocation {
		_ = operations.SaveCheckpoint(imp.store, opID, "itunes_import", "organizing", 0, 0)
		log.Info("Starting organize phase...")
		imp.organizeImportedBooks(ctx, status, log)
	}

	_ = operations.ClearState(imp.store, opID)

	if fp, err := itunes.ComputeFingerprint(req.LibraryPath); err == nil {
		_ = imp.store.SaveLibraryFingerprint(fp.Path, fp.Size, fp.ModTime, fp.CRC32)
	}

	summary := buildImportSummary(status)
	log.UpdateProgress(totalGroups, totalGroups, summary)
	log.Info("%s", summary)
	_ = ctx
	return nil
}

// Sync performs an incremental sync from the iTunes library XML.
func (imp *Importer) Sync(ctx context.Context, libraryPath string, pathMappings []itunes.PathMapping, activityFn func(database.ActivityEntry), log logger.Logger) error {
	log.UpdateProgress(0, 0, "Parsing iTunes library XML...")
	log.Info("Starting iTunes sync from %s", libraryPath)

	library, err := itunes.ParseLibrary(libraryPath)
	if err != nil {
		return fmt.Errorf("failed to parse library: %w", err)
	}
	trackCount := len(library.Tracks)
	log.Info("Parsed %d tracks from iTunes library", trackCount)
	log.UpdateProgress(0, 0, fmt.Sprintf("Grouping %d tracks by album...", trackCount))

	groups := imp.groupTracksByAlbum(library)
	totalGroups := len(groups)
	log.Info("Found %d audiobook groups from %d tracks", totalGroups, trackCount)
	if totalGroups == 0 {
		log.UpdateProgress(0, 0, "No audiobooks found in library")
		log.Warn("No audiobooks found in library")
		return nil
	}

	// Apply deferred iTunes updates before sync
	if imp.cfg.ITLWriteBackEnabled && imp.cfg.LibraryWritePath != "" {
		pending, _ := imp.store.GetPendingDeferredITunesUpdates()
		if len(pending) > 0 {
			// TASK-006: normalize each deferred NewPath into the canonical WinPath
			// (the LE writer derives the 0x0B URL). Unmappable values are skipped
			// with a WARN + metric, never written raw (CRIT-2).
			updates := make([]itunes.ITLLocationUpdate, 0, len(pending))
			for _, p := range pending {
				if winPath, ok := normalizeITunesLocation(p.PersistentID, p.NewPath); ok {
					updates = append(updates, itunes.ITLLocationUpdate{PersistentID: p.PersistentID, NewLocation: winPath})
				}
			}
			itlPath := imp.cfg.LibraryWritePath
			tmpPath := itlPath + ".deferred-update.tmp"
			result, itlErr := itunes.UpdateITLLocations(itlPath, tmpPath, updates)
			if itlErr == nil && result.UpdatedCount > 0 {
				_ = itunes.RenameITLFile(tmpPath, itlPath)
				for _, p := range pending {
					_ = imp.store.MarkDeferredITunesUpdateApplied(p.ID)
				}
				log.Info("Applied %d deferred iTunes updates", result.UpdatedCount)
			} else if itlErr != nil {
				log.Warn("Failed to apply deferred iTunes updates: %v", itlErr)
				_ = os.Remove(tmpPath)
			}
		}
	}

	importOpts := itunes.ImportOptions{
		LibraryPath:  libraryPath,
		PathMappings: pathMappings,
	}

	log.UpdateProgress(0, 0, "Building persistent ID index...")
	// Core-typed: the matching indices below only ever read/write Core-safe
	// fields (ITunesPersistentID, FilePath, Title, ITunesPlayCount,
	// ITunesRating, ITunesBookmark, ITunesLastPlayed). The actual writeback
	// hydrates a full row (see below) so it never wipes Author/Series.
	allBooks, err := imp.store.GetAllBooksCore(100000, 0)
	if err != nil {
		return fmt.Errorf("failed to load books for index: %w", err)
	}
	pidIndex := make(map[string]*database.BookCore, len(allBooks))
	pathIndex := make(map[string]*database.BookCore, len(allBooks))
	titleIndex := make(map[string]*database.BookCore, len(allBooks))
	for i := range allBooks {
		if allBooks[i].ITunesPersistentID != nil && *allBooks[i].ITunesPersistentID != "" {
			pidIndex[*allBooks[i].ITunesPersistentID] = &allBooks[i]
		}
		pathIndex[allBooks[i].FilePath] = &allBooks[i]
		titleIndex[strings.ToLower(allBooks[i].Title)] = &allBooks[i]
	}
	log.Info("Indexed %d books (%d with iTunes persistent IDs)", len(allBooks), len(pidIndex))

	const batchFlushSize = 500
	var pendingFiles []*database.BookFile

	flushPendingFiles := func() {
		if len(pendingFiles) == 0 {
			return
		}
		if err := imp.store.BatchUpsertBookFiles(pendingFiles); err != nil {
			log.Error("BatchUpsertBookFiles failed (continuing): %v", err)
		}
		pendingFiles = pendingFiles[:0]
	}

	var updated, newBooks, unchanged int
	for i, group := range groups {
		if log.IsCanceled() {
			log.Info("iTunes sync canceled")
			return nil
		}
		if len(group.tracks) == 0 {
			continue
		}

		firstTrack := group.tracks[0]
		persistentID := firstTrack.PersistentID
		if persistentID == "" {
			continue
		}

		existing := pidIndex[persistentID]

		if existing == nil {
			title := strings.TrimSpace(firstTrack.Album)
			if title == "" {
				title = strings.TrimSpace(firstTrack.Name)
			}
			if title != "" {
				existing = titleIndex[strings.ToLower(title)]
			}
		}

		if existing == nil {
			if book, err := imp.buildBookFromAlbumGroup(group, libraryPath, importOpts); err == nil {
				if match := pathIndex[book.FilePath]; match != nil {
					existing = match
				}
			}
		}

		if existing != nil && (existing.ITunesPersistentID == nil || *existing.ITunesPersistentID == "") {
			existing.ITunesPersistentID = strPtr(persistentID)
			pidIndex[persistentID] = existing
		}

		if existing != nil {
			changed := false

			newPlayCount := intPtrLocal(firstTrack.PlayCount)
			if existing.ITunesPlayCount == nil || *existing.ITunesPlayCount != *newPlayCount {
				existing.ITunesPlayCount = newPlayCount
				changed = true
			}

			newRating := intPtrLocal(firstTrack.Rating)
			if existing.ITunesRating == nil || *existing.ITunesRating != *newRating {
				existing.ITunesRating = newRating
				changed = true
			}

			newBookmark := int64PtrLocal(firstTrack.Bookmark)
			if existing.ITunesBookmark == nil || *existing.ITunesBookmark != *newBookmark {
				existing.ITunesBookmark = newBookmark
				changed = true
			}

			if firstTrack.PlayDate > 0 {
				lastPlayed := time.Unix(firstTrack.PlayDate, 0)
				if existing.ITunesLastPlayed == nil || !existing.ITunesLastPlayed.Equal(lastPlayed) {
					existing.ITunesLastPlayed = &lastPlayed
					changed = true
				}
			}

			if firstTrack.Location == "" {
				log.Debug("No Location for PID %s (%s)", persistentID, existing.Title)
			}

			if changed {
				// Hydrate full row before writeback — existing is Core (slim);
				// writing it straight through UpdateBook would wipe Author/Series.
				full, hydrateErr := imp.store.GetBookByID(existing.ID)
				if hydrateErr != nil || full == nil {
					log.Error("Failed to hydrate '%s' for update: %v", existing.Title, hydrateErr)
				} else {
					full.ITunesPersistentID = existing.ITunesPersistentID
					full.ITunesPlayCount = existing.ITunesPlayCount
					full.ITunesRating = existing.ITunesRating
					full.ITunesBookmark = existing.ITunesBookmark
					full.ITunesLastPlayed = existing.ITunesLastPlayed
					if _, err := imp.store.UpdateBook(full.ID, full); err != nil {
						log.Error("Failed to update '%s': %v", full.Title, err)
					} else {
						updated++
						if activityFn != nil {
							activityFn(database.ActivityEntry{
								Tier:    "change",
								Type:    "itunes_sync",
								Level:   "info",
								Source:  "scheduler",
								BookID:  full.ID,
								Summary: fmt.Sprintf("iTunes sync updated: %s", full.Title),
								Tags:    []string{"itunes"},
							})
						}
					}
				}
			} else {
				unchanged++
			}

			for _, track := range group.tracks {
				if track.PersistentID == "" {
					continue
				}
				existingFile, _ := imp.store.GetBookFileByPID(track.PersistentID)
				if existingFile != nil && existingFile.ITunesPath == track.Location {
					continue
				}

				remappedPath := importOpts.RemapPath(track.Location)
				decodedPath, _ := itunes.DecodeLocation(remappedPath)
				if decodedPath == "" {
					decodedPath = remappedPath
				}
				decodedPath = remapWindowsPath(decodedPath, importOpts)
				pendingFiles = append(pendingFiles, &database.BookFile{
					BookID:             existing.ID,
					FilePath:           decodedPath,
					ITunesPath:         track.Location,
					ITunesPersistentID: track.PersistentID,
					TrackNumber:        track.TrackNumber,
					TrackCount:         track.TrackCount,
					DiscNumber:         track.DiscNumber,
					DiscCount:          track.DiscCount,
					Title:              track.Name,
					Format:             strings.TrimPrefix(filepath.Ext(decodedPath), "."),
					Duration:           trackDurationSeconds(track),
					FileSize:           track.Size,
				})
			}
		} else {
			book, err := imp.buildBookFromAlbumGroup(group, libraryPath, importOpts)
			if err != nil {
				log.Warn("Failed to build book from group '%s': %v", group.key, err)
				continue
			}
			imp.assignAuthorAndSeries(book, firstTrack)
			book.LibraryState = strPtr("imported")

			created, err := imp.store.CreateBook(book)
			if err != nil {
				log.Error("Failed to create '%s': %v", book.Title, err)
			} else {
				newBooks++
				imp.publishBookCreated(ctx, created.ID)
				if created.AuthorID != nil && len(book.Authors) > 0 {
					for i := range book.Authors {
						book.Authors[i].BookID = created.ID
					}
					_ = imp.store.SetBookAuthors(created.ID, book.Authors)
				} else if created.AuthorID != nil {
					_ = imp.store.SetBookAuthors(created.ID, []database.BookAuthor{
						{BookID: created.ID, AuthorID: *created.AuthorID, Role: "author", Position: 0},
					})
				}

				for _, track := range group.tracks {
					remappedPath := importOpts.RemapPath(track.Location)
					decodedPath, _ := itunes.DecodeLocation(remappedPath)
					if decodedPath == "" {
						decodedPath = remappedPath
					}
					decodedPath = remapWindowsPath(decodedPath, importOpts)
					pendingFiles = append(pendingFiles, &database.BookFile{
						BookID:             created.ID,
						FilePath:           decodedPath,
						ITunesPath:         track.Location,
						ITunesPersistentID: track.PersistentID,
						TrackNumber:        track.TrackNumber,
						TrackCount:         track.TrackCount,
						DiscNumber:         track.DiscNumber,
						DiscCount:          track.DiscCount,
						Title:              track.Name,
						Format:             strings.TrimPrefix(filepath.Ext(decodedPath), "."),
						Duration:           trackDurationSeconds(track),
						FileSize:           track.Size,
					})
				}
			}
		}

		if len(pendingFiles) >= batchFlushSize {
			flushPendingFiles()
		}

		processed := i + 1
		if processed%importProgressBatch == 0 || processed == totalGroups {
			message := fmt.Sprintf("Syncing book %d of %d (updated %d, new %d, unchanged %d)",
				processed, totalGroups, updated, newBooks, unchanged)
			log.UpdateProgress(processed, totalGroups, message)
		}
	}

	flushPendingFiles()

	if fp, err := itunes.ComputeFingerprint(libraryPath); err == nil {
		_ = imp.store.SaveLibraryFingerprint(fp.Path, fp.Size, fp.ModTime, fp.CRC32)
	}

	summary := fmt.Sprintf("Sync completed: %d updated, %d new, %d unchanged (from %d tracks, %d groups)",
		updated, newBooks, unchanged, trackCount, totalGroups)
	log.UpdateProgress(totalGroups, totalGroups, summary)
	log.Info("%s", summary)
	_ = ctx
	return nil
}

// DiscoverLibraryPath finds the library path from the most recently imported book.
func (imp *Importer) DiscoverLibraryPath() string {
	books, err := imp.store.GetAllBooksCore(100, 0)
	if err != nil {
		return ""
	}
	for _, book := range books {
		if book.ITunesImportSource != nil && *book.ITunesImportSource != "" {
			return *book.ITunesImportSource
		}
	}
	return ""
}

// CollectITLUpdates builds location updates for all primary-version books with iTunes PIDs.
func (imp *Importer) CollectITLUpdates() []itunes.ITLLocationUpdate {
	const (
		pageSize   = 10000
		numWorkers = 4
	)

	pageCh := make(chan int, 256)
	go func() {
		offset := 0
		for {
			pageCh <- offset
			offset += pageSize
			if offset > 50_000_000 {
				break
			}
		}
		close(pageCh)
	}()

	type result struct{ updates []itunes.ITLLocationUpdate }
	resultCh := make(chan result, numWorkers)

	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var local []itunes.ITLLocationUpdate
			for offset := range pageCh {
				books, err := imp.store.GetAllBooksCore(pageSize, offset)
				if err != nil || len(books) == 0 {
					break
				}
				for i := range books {
					if books[i].IsPrimaryVersion != nil && !*books[i].IsPrimaryVersion {
						continue
					}
					files, _ := imp.store.GetBookFiles(books[i].ID)
					if len(files) > 0 {
						for _, f := range files {
							if f.ITunesPersistentID != "" && f.ITunesPath != "" {
								// TASK-006: normalize to canonical WinPath; skip
								// unmappable (WARN + metric), never write raw (CRIT-2).
								if winPath, ok := normalizeITunesLocation(f.ITunesPersistentID, f.ITunesPath); ok {
									local = append(local, itunes.ITLLocationUpdate{
										PersistentID: f.ITunesPersistentID,
										NewLocation:  winPath,
									})
								}
							}
						}
					}
				}
				if len(books) < pageSize {
					break
				}
			}
			resultCh <- result{updates: local}
		}()
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	var updates []itunes.ITLLocationUpdate
	for r := range resultCh {
		updates = append(updates, r.updates...)
	}
	return updates
}

// CollectITLUpdatesWithBookIDs returns updates and the book IDs that contributed them.
func (imp *Importer) CollectITLUpdatesWithBookIDs() ([]itunes.ITLLocationUpdate, []string) {
	allBooks, err := imp.store.GetAllBooksCore(100000, 0)
	if err != nil {
		return nil, nil
	}

	var updates []itunes.ITLLocationUpdate
	bookIDSet := make(map[string]bool)

	for i := range allBooks {
		b := &allBooks[i]
		if b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion {
			continue
		}
		files, _ := imp.store.GetBookFiles(b.ID)
		if len(files) > 0 {
			for _, f := range files {
				if f.ITunesPersistentID != "" && f.ITunesPath != "" {
					// TASK-006: normalize to canonical WinPath; skip unmappable
					// (WARN + metric), never write raw (CRIT-2).
					if winPath, ok := normalizeITunesLocation(f.ITunesPersistentID, f.ITunesPath); ok {
						updates = append(updates, itunes.ITLLocationUpdate{
							PersistentID: f.ITunesPersistentID,
							NewLocation:  winPath,
						})
						bookIDSet[b.ID] = true
					}
				}
			}
		}
	}

	bookIDs := make([]string, 0, len(bookIDSet))
	for id := range bookIDSet {
		bookIDs = append(bookIDs, id)
	}
	return updates, bookIDs
}

// --- private helpers ---

func (imp *Importer) groupTracksByAlbum(library *itunes.Library) []albumGroup {
	groupMap := make(map[string]*albumGroup)
	var groupOrder []string

	for _, track := range library.Tracks {
		if !itunes.IsAudiobook(track) {
			continue
		}
		key := albumGroupKey(track)
		if _, exists := groupMap[key]; !exists {
			groupMap[key] = &albumGroup{key: key}
			groupOrder = append(groupOrder, key)
		}
		groupMap[key].tracks = append(groupMap[key].tracks, track)
	}

	result := make([]albumGroup, 0, len(groupOrder))
	for _, key := range groupOrder {
		// The over-merge guard may split a group that mis-shares one album tag
		// across several books back into per-book sub-groups.
		for _, sub := range splitOverMergedGroup(*groupMap[key]) {
			sortTracksByDiscTrack(sub.tracks)
			result = append(result, sub)
		}
	}
	return result
}

// normalizeGroupKey lowercases and collapses whitespace so trivial tag
// formatting differences ("Wild Cards I" vs "Wild Cards  I") share one key.
func normalizeGroupKey(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// albumGroupKey derives the book-grouping key for one iTunes track.
//
// CONS-FRAG: the previous key was artist+"|"+album, which fragmented two ways:
//   - a multi-author anthology ("Wild Cards I" with a different Artist per
//     story) split into one book per artist even though the album was constant;
//   - an empty-album chapter file whose " - Part NN" suffix would not strip
//     split into one book per chapter.
//
// When an album tag is present we key on the album ALONE (artist-agnostic) so
// the anthology collapses. When it is absent we derive the key from the
// fully chapter-stripped track name and KEEP the artist — the derived name is a
// weaker signal and the artist guards against unrelated books colliding.
func albumGroupKey(track *itunes.Track) string {
	if album := strings.TrimSpace(track.Album); album != "" {
		return "album:" + normalizeGroupKey(album)
	}
	name := strings.TrimSpace(track.Name)
	stripped := stripChapterSuffix(stripChapterPrefix(name))
	if stripped == "" {
		stripped = name
	}
	return "name:" + strings.TrimSpace(track.Artist) + "|" + normalizeGroupKey(stripped)
}

// trackNumbersCleanDistinct reports whether EVERY track carries a positive,
// distinct track number — strong evidence the group is ONE book (or an anthology
// numbered globally 1..N). A group with a missing/zero number, or a repeated
// number, fails this test: it may span several books each numbered 1..N, or
// simply be untagged, so a cross-artist album merge cannot be trusted.
func trackNumbersCleanDistinct(tracks []*itunes.Track) bool {
	seen := make(map[int]bool, len(tracks))
	for _, t := range tracks {
		if t.TrackNumber <= 0 || seen[t.TrackNumber] {
			return false
		}
		seen[t.TrackNumber] = true
	}
	return len(seen) == len(tracks)
}

// splitOverMergedGroup is the over-merge guard for the artist-agnostic album
// key. A cross-artist merge under one album tag is only trusted when the track
// numbers form a clean distinct sequence (a real multi-author anthology like
// "Wild Cards I", numbered globally). Otherwise — repeated or missing/zero track
// numbers, the signature of several distinct books sharing a generic album
// ("Audiobook", an author name) — the group is split back apart by artist,
// restoring the pre-CONS-FRAG granularity. If every track shares one artist
// there is no safe split signal, so the group is left intact (never worse than
// the old artist+album key). Empty-album ("name:") groups are never split.
func splitOverMergedGroup(g albumGroup) []albumGroup {
	if !strings.HasPrefix(g.key, "album:") || len(g.tracks) < 2 || trackNumbersCleanDistinct(g.tracks) {
		return []albumGroup{g}
	}
	byArtist := make(map[string]*albumGroup)
	var order []string
	for _, t := range g.tracks {
		ak := strings.TrimSpace(t.Artist)
		sub, ok := byArtist[ak]
		if !ok {
			sub = &albumGroup{key: g.key + "|artist:" + ak}
			byArtist[ak] = sub
			order = append(order, ak)
		}
		sub.tracks = append(sub.tracks, t)
	}
	if len(order) < 2 {
		return []albumGroup{g}
	}
	slog.Info("itunes import: split over-merged album group",
		"album_key", g.key, "tracks", len(g.tracks), "into_books", len(order))
	out := make([]albumGroup, 0, len(order))
	for _, ak := range order {
		out = append(out, *byArtist[ak])
	}
	return out
}

// agreedStrippedTitle returns the chapter-stripped track name when EVERY track
// in the group strips to the same non-empty title (e.g. "Aces Abroad - Part 1"
// … "Aces Abroad - Part 14" all → "Aces Abroad"). It returns "" when the
// stripped names disagree (generic chapter names like "Opening Credits" /
// "Big Finish Ident"), letting the caller fall back to the folder name.
//
// CONS-17b: without this, an empty-album multi-file book whose chapter files sit
// flat in the AUTHOR folder would be titled after that folder (every GRRM book
// → "George R. R. Martin"), creating fresh dedup collisions.
func agreedStrippedTitle(tracks []*itunes.Track) string {
	agreed := ""
	for _, t := range tracks {
		s := stripChapterSuffix(stripChapterPrefix(strings.TrimSpace(t.Name)))
		if s == "" {
			return ""
		}
		if agreed == "" {
			agreed = s
		} else if !strings.EqualFold(agreed, s) {
			return ""
		}
	}
	return agreed
}

func sortTracksByDiscTrack(tracks []*itunes.Track) {
	sort.Slice(tracks, func(i, j int) bool {
		if tracks[i].DiscNumber != tracks[j].DiscNumber {
			return tracks[i].DiscNumber < tracks[j].DiscNumber
		}
		return tracks[i].TrackNumber < tracks[j].TrackNumber
	})
}

// enrichConcurrency is the FIXED registry.RunItems worker-pool size for
// enrichImportedBooks's FetchMetadataForBook calls (CONC-11). Unlike
// organizeConcurrency (CPU/local-disk-bound work, sized to
// runtime.NumCPU()), FetchMetadataForBook is a network call against an
// external, rate-limited metadata source — adding more workers would just
// let more goroutines hammer the same rate limit at once, so this is a
// small named constant instead of scaling with core count.
const enrichConcurrency = 4

// enrichBreakerThreshold is the shared-aggregate-failure trip point for the
// circuit breaker below, carried over from the pre-parallelization
// `consecutiveErrors >= 5` check (see enrichImportedBooks's doc comment for
// how its meaning changes under concurrency).
const enrichBreakerThreshold = 5

// enrichImportedBooks runs the per-book metadata-fetch (FetchMetadataForBook)
// step over every LibraryState=="imported" book via registry.RunItems
// (CONC-11), fanning out over a small FIXED enrichConcurrency worker pool
// (see its doc comment for why fixed-vs-NumCPU).
//
// Circuit-breaker semantic change under parallelization (read carefully —
// this is the correctness-critical part of CONC-11):
//   - The original sequential loop tracked a *consecutive* failure counter
//     (consecutiveErrors) that reset to 0 on any success and, upon
//     reaching 5, slept 10s and reset back to 0 so the caller would keep
//     going once the rate limit had presumably recovered. "Consecutive"
//     only has a well-defined meaning when exactly one FetchMetadataForBook
//     call is in flight at a time.
//   - Under a worker pool, several calls run concurrently, so there is no
//     single consecutive sequence to track. This reframes the breaker as a
//     SHARED AGGREGATE failure counter (breakerFails, an atomic int32):
//     every failure increments it, every success resets it to 0. When it
//     reaches enrichBreakerThreshold, the tripping worker — exactly once,
//     via breakerTripped (sync.Once), so concurrent trippers don't each
//     log/cancel — cancels a context derived from the caller's ctx.
//     registry.RunItems' worker pool stops launching new items once that
//     context is canceled (workers already in flight still finish, mirror-
//     ing the "let the current call complete" behavior of the original
//     loop), which preserves the "stop hammering a rate-limited source"
//     intent without a "sleep 10s and silently resume" step that has no
//     race-free meaning across concurrent workers. This is a deliberate,
//     documented semantic change: tripping the breaker now aborts the REST
//     of this enrichment pass instead of pausing then continuing it.
//   - itunesImportStatus counters: enrichImportedBooks does not update
//     `status` today — the pre-existing sequential loop didn't either — so
//     no new locking is needed there; the mutex-guarded helpers in
//     status.go remain available if a future change starts using it.
//   - The local `enriched` tally is guarded by its own mutex, same pattern
//     as organizeImportedBooks' `organized` counter.
func (imp *Importer) enrichImportedBooks(ctx context.Context, status *itunesImportStatus, log logger.Logger) {
	if imp.mfs == nil {
		log.Warn("Metadata enrichment skipped: no metafetch service wired")
		return
	}

	books, err := imp.store.GetAllBooksCore(10000, 0)
	if err != nil {
		log.Error("Failed to list books for enrichment: %v", err)
		return
	}

	// Pre-filter to the books this phase actually enriches, same filter the
	// original sequential loop applied inline per iteration. Keeps
	// RunItems' Concurrency fan-out and progress denominator scoped to real
	// work only (mirrors organizeImportedBooks' toOrganize pre-filter).
	toEnrich := make([]*database.BookCore, 0, len(books))
	for i := range books {
		book := &books[i]
		if book.LibraryState == nil || *book.LibraryState != "imported" {
			continue
		}
		if book.ITunesImportSource == nil {
			continue
		}
		toEnrich = append(toEnrich, book)
	}

	var mu sync.Mutex
	enriched := 0

	// breakerFails / breakerTripped implement the shared-aggregate circuit
	// breaker described in the doc comment above.
	var breakerFails int32
	var breakerTripped sync.Once

	enrichCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	reporter := &loggerReporterAdapter{log: log}

	runErr := registry.RunItems(enrichCtx, reporter, toEnrich, func(_ context.Context, book *database.BookCore) error {
		resp, err := imp.mfs.FetchMetadataForBook(book.ID)
		if err != nil {
			log.Debug("No metadata found for '%s': %v", book.Title, err)
			if atomic.AddInt32(&breakerFails, 1) >= enrichBreakerThreshold {
				breakerTripped.Do(func() {
					log.Info("Rate limit detected (%d aggregate failures), aborting remaining enrichment", enrichBreakerThreshold)
					cancel()
				})
			}
			return nil
		}

		atomic.StoreInt32(&breakerFails, 0)

		if resp.Book != nil && resp.Book.AuthorID != nil {
			existing, _ := imp.store.GetBookAuthors(book.ID)
			if len(existing) <= 1 {
				_ = imp.store.SetBookAuthors(book.ID, []database.BookAuthor{
					{BookID: book.ID, AuthorID: *resp.Book.AuthorID, Role: "author", Position: 0},
				})
			}
		}

		mu.Lock()
		enriched++
		n := enriched
		mu.Unlock()
		if n%10 == 0 {
			log.Info("Enriched %d books so far", n)
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: imp.enrichConcurrency(),
	})
	if runErr != nil {
		// The per-book fn above always returns nil (errors are handled
		// in-line via the breaker, matching the serial loop's "keep going"
		// behavior for a single failure), so this only fires on ctx
		// cancellation — including the breaker's own cancel() above.
		log.Warn("Enrichment phase interrupted: %v", runErr)
	}

	log.Info("Metadata enrichment complete: %d books enriched", enriched)
}

// enrichConcurrency returns the registry.RunItems worker-pool size for
// enrichImportedBooks: the fixed enrichConcurrency const by default (see the
// package-level const's doc comment for why fixed-vs-NumCPU), or
// enrichConcurrencyOverride when a test has set it to force a specific
// pool size (e.g. 1 for the sequential path).
func (imp *Importer) enrichConcurrency() int {
	if imp.enrichConcurrencyOverride > 0 {
		return imp.enrichConcurrencyOverride
	}
	return enrichConcurrency
}

// organizeImportedBooks runs the per-book organize (file-move) + UpdateBook
// step over every LibraryState=="imported" book via registry.RunItems
// (CONC-10). File I/O + a DB write per book is embarrassingly parallel
// across DISTINCT books, so this fans out over a worker pool instead of a
// plain sequential for-loop.
//
// Shared-state correctness:
//   - itunesImportStatus counters: already guarded by status.mu via the
//     recordImportFailure/incImport* helpers in status.go — reused as-is,
//     no new locking needed there.
//   - the local `organized` tally: guarded by its own mutex (mirrors the
//     resultMu pattern in path_reconcile.go's CONC-15 fix).
//   - destination path collisions: per-book target directories are NOT
//     guaranteed disjoint — organizer.generateTargetPath derives the
//     target from book metadata (title/author/narrator/series), not book
//     ID, so two DIFFERENT imported books with identical metadata CAN
//     resolve to the same destination. That's exactly the case
//     organizer.OrganizeBook's stat-then-write + ErrTargetOccupied check
//     exists to catch for a single caller — but that check is NOT atomic,
//     so two goroutines racing on the same destination could both pass
//     the "target doesn't exist yet" check and corrupt/overwrite each
//     other's file. organizeDestKey + destLocks below serialize only the
//     colliding pair (via a per-destination mutex), so the common case
//     (distinct destinations) still runs fully in parallel while the rare
//     colliding case gets the same one-wins/one-fails outcome the serial
//     loop always produced (order-independent, same result set).
func (imp *Importer) organizeImportedBooks(ctx context.Context, status *itunesImportStatus, log logger.Logger) {
	core, err := imp.store.GetAllBooksCore(100000, 0)
	if err != nil {
		log.Error("Failed to list books for organize: %v", err)
		return
	}

	// Pre-filter to the books this phase actually organizes, same filter
	// the original sequential loop applied inline per iteration. Keeps
	// RunItems' Concurrency fan-out and progress denominator scoped to
	// real work only. The filter itself only needs Core-safe fields
	// (LibraryState, ITunesImportSource), but organizeOneBook/organizeDestKey
	// generate the destination path from Author/Series (heavy fields), and
	// the writeback below must never persist a slim struct — so every
	// surviving candidate is hydrated via GetBookByID before being queued.
	toOrganize := make([]*database.Book, 0, len(core))
	for i := range core {
		c := &core[i]
		if c.LibraryState == nil || *c.LibraryState != "imported" {
			continue
		}
		if c.ITunesImportSource == nil {
			continue
		}
		full, hydrateErr := imp.store.GetBookByID(c.ID)
		if hydrateErr != nil || full == nil {
			log.Warn("Failed to hydrate book %s for organize: %v", c.ID, hydrateErr)
			continue
		}
		toOrganize = append(toOrganize, full)
	}

	var mu sync.Mutex
	organized := 0

	// destLocks serializes concurrent workers whose books resolve to the
	// same destination path — see the doc comment above for why this is
	// a correctness requirement, not just an optimization.
	var destLocks sync.Map // map[string]*sync.Mutex

	reporter := &loggerReporterAdapter{log: log}

	runErr := registry.RunItems(ctx, reporter, toOrganize, func(_ context.Context, book *database.Book) error {
		var files []database.BookFile
		if imp.store != nil {
			files, _ = imp.store.GetBookFiles(book.ID)
		}

		destKey := imp.organizeDestKey(book, files)
		lockAny, _ := destLocks.LoadOrStore(destKey, &sync.Mutex{})
		destMu := lockAny.(*sync.Mutex)
		destMu.Lock()
		defer destMu.Unlock()

		oldPath := book.FilePath
		if err := imp.organizeOneBook(book, log); err != nil {
			recordImportFailure(status, fmt.Sprintf("Failed to organize '%s': %v", book.Title, err))
			log.Warn("Failed to organize '%s': %v", book.Title, err)
			return nil
		}

		book.LibraryState = strPtr("organized")
		if _, err := imp.store.UpdateBook(book.ID, book); err != nil {
			log.Error("Failed to update organized path for '%s': %v — rolling back", book.Title, err)
			if book.FilePath != oldPath {
				if rbErr := os.Rename(book.FilePath, oldPath); rbErr != nil {
					log.Error("CRITICAL: rollback failed for %s: file at %s, DB expects %s", book.ID, book.FilePath, oldPath)
				} else {
					book.FilePath = oldPath
				}
			}
			return nil
		}

		mu.Lock()
		organized++
		mu.Unlock()
		return nil
	}, registry.RunItemsOptions{
		Concurrency: imp.organizeConcurrency(),
	})
	if runErr != nil {
		// The per-book fn above always returns nil (errors are recorded via
		// recordImportFailure, matching the serial loop's "keep going"
		// behavior), so this only fires on ctx cancellation.
		log.Warn("Organize phase interrupted: %v", runErr)
	}

	log.Info("Organize phase complete: %d books organized", organized)
}

// organizeConcurrency returns the registry.RunItems worker-pool size for
// organizeImportedBooks: runtime.NumCPU() by default (CPU/local-disk-bound
// work — file copy/hardlink/reflink + a DB write, same shape as the
// CONC-15/CONC-14 fixes), or organizeConcurrencyOverride when a test has
// set it to force the sequential path.
func (imp *Importer) organizeConcurrency() int {
	if imp.organizeConcurrencyOverride > 0 {
		return imp.organizeConcurrencyOverride
	}
	return runtime.NumCPU()
}

// organizeDestKey returns the destination path organizeOneBook will
// resolve for book via imp.organizerFactory: the full target file path for
// single-file books, or the target directory for merged multi-file books.
// Used as the destLocks key in organizeImportedBooks so two books that
// would collide on the same destination are never run through
// organizeOneBook concurrently (see organizeImportedBooks' doc comment).
//
// If organizerFactory is nil or the constructed organizer doesn't expose
// the path-preview API (e.g. a test double implementing only the
// BookOrganizer interface), falls back to book.ID — a unique key that
// serializes nothing, which is safe because organizeOneBook itself will
// hit the same "not configured" / whatever-the-fake-does error path with
// no destination-lock protection needed for those cases.
func (imp *Importer) organizeDestKey(book *database.Book, files []database.BookFile) string {
	if imp.organizerFactory == nil {
		return book.ID
	}
	org := imp.organizerFactory()

	type targetPather interface {
		GenerateTargetPath(book *database.Book) (string, error)
		GenerateTargetDirPath(book *database.Book) (string, error)
	}
	tp, ok := org.(targetPather)
	if !ok {
		return book.ID
	}

	if len(files) > 1 {
		if dir, err := tp.GenerateTargetDirPath(book); err == nil && dir != "" {
			return dir
		}
		return book.ID
	}
	if path, err := tp.GenerateTargetPath(book); err == nil && path != "" {
		return path
	}
	return book.ID
}

// loggerReporterAdapter implements registry.Reporter by delegating to an
// existing logger.Logger, so organizeImportedBooks can drive
// registry.RunItems without changing its logger.Logger-based signature.
// Checkpoint/RunPhase/Trigger/SetCurrentItem are inert no-ops:
// organizeImportedBooks doesn't do per-item checkpointing (the caller
// already calls operations.SaveCheckpoint once for the whole "organizing"
// phase — see Execute) or emit registry-level trigger events.
type loggerReporterAdapter struct {
	log logger.Logger

	// mu serializes UpdateProgress calls from concurrent RunItems workers;
	// logger.Logger implementations are not documented as goroutine-safe.
	mu sync.Mutex
}

func (a *loggerReporterAdapter) UpdateProgress(current, total int, message string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.log.UpdateProgress(current, total, message)
	return nil
}

func (a *loggerReporterAdapter) Log(level slog.Level, message string, attrs ...slog.Attr) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case level >= slog.LevelError:
		a.log.Error("%s", message)
	case level >= slog.LevelWarn:
		a.log.Warn("%s", message)
	case level >= slog.LevelInfo:
		a.log.Info("%s", message)
	default:
		a.log.Debug("%s", message)
	}
	return nil
}

func (a *loggerReporterAdapter) Logger() *slog.Logger {
	// Inert: registry.RunItems never calls Logger() itself; it's only
	// here to satisfy the Reporter interface.
	return slog.Default()
}

func (a *loggerReporterAdapter) Checkpoint(state any) error {
	return nil
}

func (a *loggerReporterAdapter) IsCanceled() bool {
	return a.log.IsCanceled()
}

func (a *loggerReporterAdapter) RunPhase(ctx context.Context, name string, fn func(context.Context, registry.Reporter) error) error {
	return fn(ctx, a)
}

func (a *loggerReporterAdapter) Trigger(ctx context.Context, eventName string, payload any) error {
	return nil
}

func (a *loggerReporterAdapter) SetCurrentItem(label string) {
	// Inert: RunItems calls this before every item, but organizeImportedBooks
	// had no per-item "current item" concept before this change and adding
	// one is out of scope here.
}

func (imp *Importer) organizeOneBook(book *database.Book, log logger.Logger) error {
	if book == nil {
		return fmt.Errorf("book is nil")
	}
	if imp.organizerFactory == nil {
		return fmt.Errorf("organizer not configured")
	}

	org := imp.organizerFactory()

	// Merged multi-file iTunes books have their tracks scattered across
	// multiple book_files rows; FilePath on the book itself resolves to a
	// directory, and OrganizeBook safely (but silently) refuses to touch
	// directories. Route those books through OrganizeBookDirectory instead
	// so they don't get stuck at library_state=imported forever (CONS-FRAG-2).
	var files []database.BookFile
	if imp.store != nil {
		files, _ = imp.store.GetBookFiles(book.ID)
	}
	if len(files) > 1 {
		return imp.organizeMultiFileBook(org, book, files, log)
	}

	newPath, _, err := org.OrganizeBook(book)
	if err != nil {
		return err
	}
	if newPath != "" && newPath != book.FilePath {
		book.FilePath = newPath
		imp.applyOrganizedFileMetadata(book, newPath)
		log.Info("Organized '%s' to %s", book.Title, newPath)
	}
	return nil
}

// organizeMultiFileBook routes a merged, multi-file book through
// OrganizeBookDirectory, then updates the book's FilePath and each
// BookFile's FilePath to reflect the new organized locations.
func (imp *Importer) organizeMultiFileBook(org BookOrganizer, book *database.Book, files []database.BookFile, log logger.Logger) error {
	segmentPaths := make([]string, 0, len(files))
	for _, f := range files {
		if f.FilePath == "" {
			continue
		}
		segmentPaths = append(segmentPaths, f.FilePath)
	}

	targetDir, pathMap, err := org.OrganizeBookDirectory(book, segmentPaths)
	if err != nil {
		return err
	}
	if targetDir != "" && targetDir != book.FilePath {
		book.FilePath = targetDir
		log.Info("Organized '%s' to %s", book.Title, targetDir)
	}

	if imp.store != nil {
		for _, f := range files {
			newPath, ok := pathMap[f.FilePath]
			if !ok || newPath == "" || newPath == f.FilePath {
				continue
			}
			updated := f
			updated.FilePath = newPath
			if uErr := imp.store.UpdateBookFile(f.ID, &updated); uErr != nil {
				log.Warn("failed to update book file path for '%s' (file=%s): %v", book.Title, f.ID, uErr)
			}
		}
	}
	return nil
}

func (imp *Importer) applyOrganizedFileMetadata(book *database.Book, newPath string) {
	hash, err := scanner.ComputeFileHash(newPath)
	if err != nil {
		slog.Warn("failed to compute organized hash", "path", newPath, "error", err)
	} else if hash != "" {
		book.FileHash = strPtr(hash)
		book.OrganizedFileHash = strPtr(hash)
		if book.OriginalFileHash == nil {
			book.OriginalFileHash = strPtr(hash)
		}
	}
	if info, err := os.Stat(newPath); err == nil {
		size := info.Size()
		book.FileSize = &size
	}
}

func (imp *Importer) linkITunesMetadata(existing *database.Book, importBook *database.Book, track *itunes.Track, log logger.Logger) {
	changed := false
	if existing.ITunesPersistentID == nil && importBook.ITunesPersistentID != nil {
		existing.ITunesPersistentID = importBook.ITunesPersistentID
		changed = true
	}
	if existing.ITunesPlayCount == nil && importBook.ITunesPlayCount != nil {
		existing.ITunesPlayCount = importBook.ITunesPlayCount
		changed = true
	}
	if existing.ITunesRating == nil && importBook.ITunesRating != nil {
		existing.ITunesRating = importBook.ITunesRating
		changed = true
	}
	if existing.ITunesBookmark == nil && importBook.ITunesBookmark != nil {
		existing.ITunesBookmark = importBook.ITunesBookmark
		changed = true
	}
	if existing.ITunesDateAdded == nil && importBook.ITunesDateAdded != nil {
		existing.ITunesDateAdded = importBook.ITunesDateAdded
		changed = true
	}
	if existing.ITunesImportSource == nil && importBook.ITunesImportSource != nil {
		existing.ITunesImportSource = importBook.ITunesImportSource
		changed = true
	}
	if existing.VersionGroupID == nil || *existing.VersionGroupID == "" {
		vgID := fmt.Sprintf("vg-%s", ulid.Make().String())
		existing.VersionGroupID = &vgID
		changed = true
	}
	if existing.IsPrimaryVersion == nil || !*existing.IsPrimaryVersion {
		isPrimary := true
		existing.IsPrimaryVersion = &isPrimary
		changed = true
	}
	if changed {
		if _, err := imp.store.UpdateBook(existing.ID, existing); err != nil {
			log.Warn("Failed to link iTunes metadata to %s: %v", existing.ID, err)
		}
	}
}

func (imp *Importer) linkAsVersion(existing *database.Book, importBook *database.Book, track *itunes.Track, log logger.Logger) {
	if existing.VersionGroupID == nil || *existing.VersionGroupID == "" {
		vgID := fmt.Sprintf("vg-%s", ulid.Make().String())
		existing.VersionGroupID = &vgID
		isPrimary := true
		existing.IsPrimaryVersion = &isPrimary
		if _, err := imp.store.UpdateBook(existing.ID, existing); err != nil {
			log.Warn("Failed to set VG on existing book %s: %v", existing.ID, err)
			return
		}
	}

	importBook.VersionGroupID = existing.VersionGroupID
	isPrimary := false
	importBook.IsPrimaryVersion = &isPrimary
	importBook.LibraryState = strPtr("imported")

	created, err := imp.store.CreateBook(importBook)
	if err != nil {
		log.Warn("Failed to create version link for %s: %v", importBook.Title, err)
		return
	}
	imp.linkITunesMetadata(existing, importBook, track, log)
	log.Info("Created version link: %s (iTunes) → %s (primary) in %s", created.ID, existing.ID, *existing.VersionGroupID)
}

func (imp *Importer) buildBookFromAlbumGroup(group albumGroup, libraryPath string, opts itunes.ImportOptions) (*database.Book, error) {
	if len(group.tracks) == 0 {
		return nil, fmt.Errorf("album group has no tracks")
	}

	firstTrack := group.tracks[0]
	location := opts.RemapPath(firstTrack.Location)
	filePath, err := itunes.DecodeLocation(location)
	if err != nil {
		return nil, fmt.Errorf("failed to decode location: %w", err)
	}
	if _, err := os.Stat(filePath); err != nil {
		return nil, fmt.Errorf("file does not exist: %s", filePath)
	}

	title := strings.TrimSpace(firstTrack.Album)
	bookFilePath := filePath
	if len(group.tracks) > 1 && title != "" {
		bookFilePath = imp.commonParentDir(group.tracks, opts)
	}
	if title == "" && len(group.tracks) > 1 {
		// CONS-17b: when every chapter track strips to the SAME title (e.g.
		// "Aces Abroad - Part NN" → "Aces Abroad"), that agreed title IS the
		// book title. Prefer it over the folder name — these chapter files often
		// sit flat in the AUTHOR folder, so the folder base would mistitle every
		// book after its author and spawn fresh dedup collisions.
		if agreed := agreedStrippedTitle(group.tracks); agreed != "" {
			title = agreed
			bookFilePath = imp.commonParentDir(group.tracks, opts)
		}
	}
	if title == "" && len(group.tracks) > 1 {
		// CONS-17 (Path A): album tag empty and the chapter names disagree
		// (generic "Opening Credits" / "Big Finish Ident"). Derive the title from
		// the common parent FOLDER (the book/album directory), more reliable than
		// a per-chapter track Name. Only fall through to the track-Name heuristic
		// below if the folder name is unusable.
		if dir := imp.commonParentDir(group.tracks, opts); dir != "" {
			if base := strings.TrimSpace(filepath.Base(dir)); base != "" && base != "." && base != string(filepath.Separator) {
				title = base
				bookFilePath = dir
			}
		}
	}
	if title == "" {
		// Album tag was empty/missing — falling back to the per-chapter track
		// Name. iTunes audiobook tracks frequently have Names shaped like
		// "(76/85) Tarkin: Star Wars (Unabridged)" or "At All Costs – 11/23" —
		// strip both leading and trailing chapter markers so Book.Title doesn't
		// carry a chapter number.
		// See docs/perf-audit-2026-05-29-g5-title-mismatch.md (MAYDEPLOY-G5a).
		title = stripChapterSuffix(stripChapterPrefix(strings.TrimSpace(firstTrack.Name)))
	}
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	}

	var totalDurationMs int64
	var totalSize int64
	for _, t := range group.tracks {
		totalDurationMs += t.TotalTime
		totalSize += t.Size
	}

	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(filePath)), ".")
	var duration *int
	if totalDurationMs > 0 {
		seconds := int(totalDurationMs / 1000)
		duration = &seconds
	}
	var releaseYear *int
	if firstTrack.Year > 0 {
		releaseYear = intPtrLocal(firstTrack.Year)
	}
	var persistentID *string
	if firstTrack.PersistentID != "" {
		persistentID = strPtr(firstTrack.PersistentID)
	}

	book := &database.Book{
		Title:                title,
		FilePath:             bookFilePath,
		Format:               format,
		Duration:             duration,
		OriginalFilename:     strPtr(filepath.Base(filePath)),
		AudiobookReleaseYear: releaseYear,
		ITunesPersistentID:   persistentID,
		ITunesPlayCount:      intPtrLocal(firstTrack.PlayCount),
		ITunesRating:         intPtrLocal(firstTrack.Rating),
		ITunesBookmark:       int64PtrLocal(firstTrack.Bookmark),
		ITunesImportSource:   strPtr(libraryPath),
	}

	if !firstTrack.DateAdded.IsZero() {
		book.ITunesDateAdded = &firstTrack.DateAdded
	}
	if firstTrack.PlayDate > 0 {
		lastPlayed := time.Unix(firstTrack.PlayDate, 0)
		book.ITunesLastPlayed = &lastPlayed
	}
	if firstTrack.AlbumArtist != "" && firstTrack.AlbumArtist != firstTrack.Artist {
		book.Narrator = strPtr(firstTrack.AlbumArtist)
	}
	if firstTrack.Comments != "" {
		book.Description = strPtr(firstTrack.Comments)
	}
	if totalSize > 0 {
		book.FileSize = &totalSize
	}

	if len(group.tracks) > 1 {
		slog.Info("iTunes import grouped tracks", "count", len(group.tracks), "album", title)
	}
	return book, nil
}

func (imp *Importer) commonParentDir(tracks []*itunes.Track, opts itunes.ImportOptions) string {
	if len(tracks) == 0 {
		return ""
	}
	var paths []string
	for _, t := range tracks {
		location := opts.RemapPath(t.Location)
		p, err := itunes.DecodeLocation(location)
		if err != nil {
			continue
		}
		paths = append(paths, filepath.Dir(p))
	}
	if len(paths) == 0 {
		return ""
	}

	common := paths[0]
	for _, p := range paths[1:] {
		for common != p && !strings.HasPrefix(p, common+string(filepath.Separator)) {
			common = filepath.Dir(common)
			if common == "/" || common == "." {
				return common
			}
		}
	}
	return common
}

func (imp *Importer) assignAuthorAndSeries(book *database.Book, track *itunes.Track) {
	if book == nil || track == nil {
		return
	}
	if track.Artist != "" {
		ids, err := imp.ensureAuthorIDs(track.Artist)
		if err == nil && len(ids) > 0 {
			book.AuthorID = &ids[0]
			book.Authors = make([]database.BookAuthor, 0, len(ids))
			for i, id := range ids {
				book.Authors = append(book.Authors, database.BookAuthor{
					AuthorID: id,
					Role:     "author",
					Position: i,
				})
			}
		}
	}
	seriesName := extractSeriesName(track.Album)
	if seriesName != "" {
		if seriesID, err := imp.ensureSeriesID(seriesName, book.AuthorID); err == nil {
			book.SeriesID = seriesID
		}
	}
}

func (imp *Importer) ensureAuthorIDs(name string) ([]int, error) {
	parts := dedup.SplitCompositeAuthorName(name)
	if len(parts) == 0 {
		parts = []string{name}
	}

	var ids []int
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		part = dedup.NormalizeAuthorName(part)
		author, err := imp.store.GetAuthorByName(part)
		if err != nil {
			return nil, err
		}
		if author == nil {
			author, err = imp.store.CreateAuthor(part)
			if err != nil {
				return nil, err
			}
		}
		ids = append(ids, author.ID)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no valid author names in %q", name)
	}
	return ids, nil
}

func (imp *Importer) ensureSeriesID(name string, authorID *int) (*int, error) {
	// Strip any embedded title/position contamination from the series name.
	if cleaned, _, flagged := metadata.StripSeriesContamination(strings.TrimSpace(name), ""); !flagged && cleaned != "" {
		name = cleaned
	}

	series, err := imp.store.GetSeriesByName(name, authorID)
	if err != nil {
		return nil, err
	}
	if series != nil {
		return &series.ID, nil
	}
	series, err = imp.store.CreateSeries(name, authorID)
	if err != nil {
		return nil, err
	}
	return &series.ID, nil
}

func (imp *Importer) resolveImportMode(mode string) itunes.ImportMode {
	switch mode {
	case string(itunes.ImportModeOrganized):
		return itunes.ImportModeOrganized
	case string(itunes.ImportModeOrganize):
		return itunes.ImportModeOrganize
	default:
		return itunes.ImportModeImport
	}
}

func (imp *Importer) importLibraryState(mode itunes.ImportMode) string {
	if mode == itunes.ImportModeOrganized {
		return "organized"
	}
	return "imported"
}

// --- package-level helpers (no state) ---

// toITunesPathMappings converts service PathMappings to the low-level itunes package type.
func toITunesPathMappings(src []PathMapping) []itunes.PathMapping {
	out := make([]itunes.PathMapping, len(src))
	for i, m := range src {
		out[i] = itunes.PathMapping{From: m.From, To: m.To}
	}
	return out
}

func extractSeriesName(album string) string {
	if album == "" {
		return ""
	}
	for _, sep := range []string{",", "-", ":"} {
		parts := strings.SplitN(album, sep, 2)
		if len(parts) == 2 {
			return strings.TrimSpace(parts[0])
		}
	}
	return strings.TrimSpace(album)
}

func remapWindowsPath(p string, opts itunes.ImportOptions) string {
	if len(p) < 2 || p[1] != ':' {
		return p
	}
	normalized := strings.ReplaceAll(p, "\\", "/")
	for _, m := range opts.PathMappings {
		from := strings.ReplaceAll(m.From, "\\", "/")
		if from == "" || m.To == "" {
			continue
		}
		plainFrom := from
		if strings.HasPrefix(plainFrom, "file://localhost/") {
			plainFrom = plainFrom[len("file://localhost/"):]
		} else if strings.HasPrefix(plainFrom, "file:///") {
			plainFrom = plainFrom[len("file:///"):]
		}
		if plainFrom == "" {
			continue
		}
		if strings.HasPrefix(normalized, plainFrom) {
			return m.To + normalized[len(plainFrom):]
		}
		if strings.HasPrefix(strings.ToLower(normalized), strings.ToLower(plainFrom)) {
			return m.To + normalized[len(plainFrom):]
		}
	}
	return p
}

func calculatePercent(current, total int) int {
	if total <= 0 {
		return 0
	}
	pct := (current * 100) / total
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

func strPtr(s string) *string      { return &s }
func intPtrLocal(v int) *int       { return &v }
func int64PtrLocal(v int64) *int64 { return &v }

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
