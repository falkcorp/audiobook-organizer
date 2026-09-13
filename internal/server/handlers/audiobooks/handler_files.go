// file: internal/server/handlers/audiobooks/handler_files.go
// version: 1.6.0
// guid: 82f8d1f7-46d5-4ead-b5c1-ba796fd785f9
// last-edited: 2026-09-13

// File / segment endpoints for the audiobooks domain: segment listing,
// book-file listing + patch, track-info extraction, relocate, and segment
// tags. Split out of handler.go for readability; one Handler, one New().

package audiobookshandler

import (
	"errors"
	"fmt"
	"hash/crc32"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/security/pathvalidation"
	"github.com/gin-gonic/gin"
)

// relocateTargetAllowed reports whether absPath is inside an allowed directory
// (registered import paths + RootDir + default prefixes). A relocate target
// must land inside the library's allowed roots (go/path-injection). Import
// paths are read from the live store via an inline type assertion (the narrow
// AudiobooksStore interface does not expose them); when unavailable (e.g. in
// tests with a mock store) the RootDir/default-prefix allow-list still applies.
func relocateTargetAllowed(store AudiobooksStore, absPath string) bool {
	var importPaths []database.ImportPath
	if ips, ok := store.(interface {
		GetAllImportPaths() ([]database.ImportPath, error)
	}); ok {
		if got, err := ips.GetAllImportPaths(); err == nil {
			importPaths = got
		}
	}
	return fileops.IsAllowedPath(absPath, importPaths)
}

// ListAudiobookSegments handles GET /audiobooks/:id/segments. Returns file
// segments for a multi-file audiobook in the legacy BookSegment JSON shape.
func (h *Handler) ListAudiobookSegments(c *gin.Context) {
	id := c.Param("id")
	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	book, err := store.GetBookByID(id)
	if err != nil || book == nil {
		httputil.RespondWithNotFound(c, "audiobook", id)
		return
	}

	files, err := store.GetBookFiles(book.ID)
	if err != nil {
		httputil.InternalError(c, "failed to list book files", err)
		return
	}
	if files == nil {
		files = []database.BookFile{}
	}

	// Convert BookFile to legacy segment JSON shape with file_exists.
	// Fix #7: use f.Missing from the database instead of synchronous os.Stat().
	result := make([]gin.H, 0, len(files))
	for _, f := range files {
		result = append(result, gin.H{
			"id":         f.ID,
			"book_id":    int(crc32.ChecksumIEEE([]byte(f.BookID))),
			"file_path":  f.FilePath,
			"format":     f.Format,
			"size_bytes": f.FileSize,
			// Seconds by convention; normalize only genuine ms rows. See
			// database.NormalizeDurationSec.
			"duration_seconds": database.NormalizeDurationSec(f.FileSize, f.Duration),
			"track_number":     f.TrackNumber,
			"total_tracks":     f.TrackCount,
			"segment_title":    f.Title,
			"file_hash":        f.FileHash,
			"active":           !f.Missing,
			"superseded_by":    nil,
			"created_at":       f.CreatedAt,
			"updated_at":       f.UpdatedAt,
			"file_exists":      !f.Missing,
		})
	}

	httputil.RespondWithOK(c, result)
}

// ListBookFiles returns all book_files rows for a book with live disk-existence
// check. GET /audiobooks/:id/files.
func (h *Handler) ListBookFiles(c *gin.Context) {
	bookID := c.Param("id")
	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	files, err := store.GetBookFiles(bookID)
	if err != nil {
		httputil.InternalError(c, "failed to get book files", err)
		return
	}
	if files == nil {
		files = []database.BookFile{}
	}
	// Fix #7: use f.Missing from the database instead of synchronous os.Stat().
	results := make([]gin.H, 0, len(files))
	for _, f := range files {
		results = append(results, gin.H{
			"id":                   f.ID,
			"book_id":              f.BookID,
			"file_path":            f.FilePath,
			"original_filename":    f.OriginalFilename,
			"itunes_path":          f.ITunesPath,
			"itunes_persistent_id": f.ITunesPersistentID,
			"track_number":         f.TrackNumber,
			"track_count":          f.TrackCount,
			"disc_number":          f.DiscNumber,
			"disc_count":           f.DiscCount,
			"title":                f.Title,
			"format":               f.Format,
			"codec":                f.Codec,
			"duration":             f.Duration,
			"file_size":            f.FileSize,
			"bitrate_kbps":         f.BitrateKbps,
			"sample_rate_hz":       f.SampleRateHz,
			"channels":             f.Channels,
			"bit_depth":            f.BitDepth,
			"file_hash":            f.FileHash,
			"original_file_hash":   f.OriginalFileHash,
			"post_metadata_hash":   f.PostMetadataHash,
			"download_hash":        f.DownloadHash,
			// Acoustic fingerprint segments (0=intro, 1-5=body, 6=outro)
			"acoustid_seg0":               f.AcoustIDSeg0,
			"acoustid_seg1":               f.AcoustIDSeg1,
			"acoustid_seg2":               f.AcoustIDSeg2,
			"acoustid_seg3":               f.AcoustIDSeg3,
			"acoustid_seg4":               f.AcoustIDSeg4,
			"acoustid_seg5":               f.AcoustIDSeg5,
			"acoustid_seg6":               f.AcoustIDSeg6,
			"fingerprint_failed_at":       f.FingerprintFailedAt,
			"fingerprint_failure_reason":  f.FingerprintFailureReason,
			"fingerprint_failure_detail":  f.FingerprintFailureDetail,
			"fingerprint_diagnostic_json": f.FingerprintDiagnosticJSON,
			"organize_method":             f.OrganizeMethod,
			"missing":                     f.Missing,
			"file_exists":                 !f.Missing,
			"created_at":                  f.CreatedAt,
			"updated_at":                  f.UpdatedAt,
			// Per-file intro transcription. This response is built from an
			// explicit key list, so new BookFile fields are invisible to the UI
			// until added here by hand — unlike the Pebble row, this layer is NOT
			// additive-safe. GetBookFiles reads Pebble-direct, so intro_transcription
			// is populated here despite being stripped from the memdb projection.
			"intro_transcription":      f.IntroTranscription,
			"transcribed_title":        f.TranscribedTitle,
			"transcribed_author":       f.TranscribedAuthor,
			"transcribed_narrator":     f.TranscribedNarrator,
			"transcribed_translator":   f.TranscribedTranslator,
			"transcribed_cover_artist": f.TranscribedCoverArtist,
			"intro_transcribed_at":     f.IntroTranscribedAt,
			"transcribe_status":        f.TranscribeStatus,
			"transcribe_error":         f.TranscribeError,
			"transcribe_attempted_at":  f.TranscribeAttemptedAt,
		})
	}
	httputil.RespondWithOK(c, gin.H{"files": results, "count": len(results)})
}

// PatchBookFile updates a BookFile (SkipScan, DownloadHash).
// PATCH /audiobooks/:id/files/:file_id.
func (h *Handler) PatchBookFile(c *gin.Context) {
	bookID := c.Param("id")
	fileID := c.Param("file_id")

	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	var body struct {
		SkipScan     *bool   `json:"skip_scan"`
		DownloadHash *string `json:"download_hash"`
		// TrackNumber and DiscNumber set the file's position by hand. 0 is
		// accepted and stored, but it is the "no number" sentinel everywhere
		// that reads it: the rename planner (organizer.planTargetPaths) sorts a
		// track-0 file AFTER every numbered one and names it by position, and
		// the tag write-back and maintenance.enrich-book-files treat it as
		// unset. It does not mean "sorts first".
		TrackNumber *int `json:"track_number"`
		DiscNumber  *int `json:"disc_number"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httputil.RespondWithBadRequest(c, "invalid request body")
		return
	}
	if body.TrackNumber != nil && *body.TrackNumber < 0 {
		httputil.RespondWithBadRequest(c, "track_number must not be negative")
		return
	}
	if body.DiscNumber != nil && *body.DiscNumber < 0 {
		httputil.RespondWithBadRequest(c, "disc_number must not be negative")
		return
	}

	// Field-level write: the store re-reads the row and sets only these
	// fields, so a concurrent writer's column (enrich-book-files' Duration,
	// say) is not put back to what this request would have read. It writes
	// nothing when no value changes.
	before, file, err := store.PatchBookFileFields(bookID, fileID, database.BookFileFieldPatch{
		TrackNumber:  body.TrackNumber,
		DiscNumber:   body.DiscNumber,
		SkipScan:     body.SkipScan,
		DownloadHash: body.DownloadHash,
	})
	if err != nil {
		httputil.InternalError(c, "failed to update book file", err)
		return
	}
	if file == nil {
		httputil.RespondWithNotFound(c, "book file", fileID)
		return
	}
	if before.SkipScan != file.SkipScan {
		filesLog.Info("book file %s: skip_scan set to %t (book %s)",
			logger.SanitizeLogValue(fileID), file.SkipScan, logger.SanitizeLogValue(bookID))
	}
	if before.DownloadHash != file.DownloadHash {
		filesLog.Info("book file %s: download_hash set to %s (book %s)",
			logger.SanitizeLogValue(fileID), logger.SanitizeLogValue(file.DownloadHash), logger.SanitizeLogValue(bookID))
	}

	// Position edits, recorded in the book's metadata change history AFTER the
	// row is written, so the history never shows a change that did not land.
	// Only values that actually changed are recorded.
	type positionChange struct {
		field    string
		old, new int
	}
	var positions []positionChange
	if before.TrackNumber != file.TrackNumber {
		positions = append(positions, positionChange{"track_number", before.TrackNumber, file.TrackNumber})
	}
	if before.DiscNumber != file.DiscNumber {
		positions = append(positions, positionChange{"disc_number", before.DiscNumber, file.DiscNumber})
	}

	// The book_file row has no history of its own; its position edits go in
	// the owning book's history. The field names the file ("track_number" for
	// the book would be ambiguous across its files), and the values are the
	// numbers themselves so the entry reads as the edit that was made.
	now := time.Now()
	for _, pc := range positions {
		prev, next := strconv.Itoa(pc.old), strconv.Itoa(pc.new)
		if err := store.RecordMetadataChange(&database.MetadataChangeRecord{
			BookID:        bookID,
			Field:         fmt.Sprintf("book_file:%s:%s", fileID, pc.field),
			PreviousValue: &prev,
			NewValue:      &next,
			ChangeType:    "manual",
			Source:        "book_file_patch",
			ChangedAt:     now,
		}); err != nil {
			// The edit itself landed; a lost history row is logged, not
			// reported as a failed edit (a retry would change nothing).
			filesLog.Warn("book file %s: %s changed %s -> %s but the change history write failed: %v",
				logger.SanitizeLogValue(fileID), pc.field, prev, next, err)
		}
		filesLog.Info("book file %s: %s set %s -> %s (book %s)",
			logger.SanitizeLogValue(fileID), pc.field, prev, next, logger.SanitizeLogValue(bookID))
	}
	httputil.RespondWithOK(c, file)
}

var filesLog = logger.New("audiobooks.files")

// bookFilePositionField is the metadata-history field PatchBookFile records a
// file position edit under: "book_file:<file id>:<track_number|disc_number>".
func bookFilePositionField(field string) (fileID, column string, ok bool) {
	rest, found := strings.CutPrefix(field, "book_file:")
	if !found {
		return "", "", false
	}
	i := strings.LastIndex(rest, ":")
	if i <= 0 {
		return "", "", false
	}
	fileID, column = rest[:i], rest[i+1:]
	if column != "track_number" && column != "disc_number" {
		return "", "", false
	}
	return fileID, column, true
}

// errBookFilePositionChangedSince is returned when an undo finds the file no
// longer holds the number the recorded edit set: restoring the old number
// would overwrite a later change.
var errBookFilePositionChangedSince = errors.New("the file's position changed after this edit; undo refused")

// revertBookFilePosition undoes one PatchBookFile position edit on the file
// row itself. The book-level undo path (metadataStateService.SetOverride)
// must never see these fields: it would store a book override named
// "book_file:..." that nothing reads, report "undo applied", and leave the
// file's number where it was. handled is false for any other field.
//
// Compare-and-set inside the store (PatchBookFileFields' precondition): the
// row must still carry the recorded NewValue, and only that column is
// written.
func revertBookFilePosition(store AudiobooksStore, bookID string, rec *database.MetadataChangeRecord) (handled bool, err error) {
	fileID, column, ok := bookFilePositionField(rec.Field)
	if !ok {
		return false, nil
	}
	if rec.PreviousValue == nil || rec.NewValue == nil {
		return true, fmt.Errorf("history row for %s has no recorded values", rec.Field)
	}
	prev, perr := strconv.Atoi(*rec.PreviousValue)
	set, serr := strconv.Atoi(*rec.NewValue)
	if perr != nil || serr != nil {
		return true, fmt.Errorf("history row for %s does not hold numbers (%q -> %q)", rec.Field, *rec.PreviousValue, *rec.NewValue)
	}
	patch := database.BookFileFieldPatch{TrackNumber: &prev, IfTrackNumber: &set}
	if column == "disc_number" {
		patch = database.BookFileFieldPatch{DiscNumber: &prev, IfDiscNumber: &set}
	}
	_, after, err := store.PatchBookFileFields(bookID, fileID, patch)
	if errors.Is(err, database.ErrBookFileChangedSince) {
		return true, fmt.Errorf("book file %s: %v: %w", fileID, err, errBookFilePositionChangedSince)
	}
	if err != nil {
		return true, fmt.Errorf("restore %s of book file %s: %w", column, fileID, err)
	}
	if after == nil {
		return true, fmt.Errorf("book file %s is no longer on book %s: %w", fileID, bookID, errBookFilePositionChangedSince)
	}
	return true, nil
}

// ExtractTrackInfo parses track/disk numbers from segment filenames and updates
// segments. POST /audiobooks/:id/extract-track-info.
func (h *Handler) ExtractTrackInfo(c *gin.Context) {
	id := c.Param("id")
	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	book, err := store.GetBookByID(id)
	if err != nil || book == nil {
		httputil.RespondWithNotFound(c, "audiobook", id)
		return
	}

	files, err := store.GetBookFiles(book.ID)
	if err != nil {
		httputil.InternalError(c, "failed to list book files", err)
		return
	}

	filePaths := make([]string, len(files))
	for i, f := range files {
		filePaths[i] = f.FilePath
	}

	trackInfos := metadata.ExtractTrackInfoBatch(filePaths)

	// Second pass: normalize track numbers to be 1-indexed and fill gaps
	// Some players/files use 0-based numbering (0-50); we always want 1-based (1-51)
	hasZero := false
	for _, info := range trackInfos {
		if info.TrackNumber != nil && *info.TrackNumber == 0 {
			hasZero = true
			break
		}
	}
	if hasZero {
		for i := range trackInfos {
			if trackInfos[i].TrackNumber != nil {
				n := *trackInfos[i].TrackNumber + 1
				trackInfos[i].TrackNumber = &n
			}
		}
	}

	// Assign sequential numbers to files that had no parseable track number
	usedNumbers := map[int]bool{}
	for _, info := range trackInfos {
		if info.TrackNumber != nil {
			usedNumbers[*info.TrackNumber] = true
		}
	}
	nextNum := 1
	total := len(files)
	for i := range trackInfos {
		if trackInfos[i].TrackNumber == nil {
			for usedNumbers[nextNum] {
				nextNum++
			}
			n := nextNum
			trackInfos[i].TrackNumber = &n
			usedNumbers[nextNum] = true
			nextNum++
		}
		// Ensure TotalTracks is set for all entries
		trackInfos[i].TotalTracks = &total
	}

	updated := 0
	for i, info := range trackInfos {
		oldTrack := files[i].TrackNumber
		if info.TrackNumber != nil {
			files[i].TrackNumber = *info.TrackNumber
		}
		if info.TotalTracks != nil {
			files[i].TrackCount = *info.TotalTracks
		}
		if err := store.UpdateBookFile(files[i].ID, &files[i]); err != nil {
			slog.Warn("failed to update book file track info", "fileID", files[i].ID, "err", err)
			continue
		}
		updated++

		// Record the track number change in history
		var prevVal, newVal string
		if oldTrack != 0 {
			prevVal = strconv.Itoa(oldTrack)
		}
		if info.TrackNumber != nil {
			newVal = strconv.Itoa(*info.TrackNumber)
		}
		prev := prevVal
		nv := newVal
		_ = store.RecordMetadataChange(&database.MetadataChangeRecord{
			BookID:        id,
			Field:         "track_number",
			PreviousValue: &prev,
			NewValue:      &nv,
			ChangeType:    "auto_number",
			Source:        "filename_extraction",
			ChangedAt:     time.Now(),
		})
	}

	httputil.RespondWithOK(c, gin.H{
		"updated": updated,
		"total":   len(files),
		"files":   files,
	})
}

// RelocateBookFiles updates segment file paths when files have been moved.
// POST /audiobooks/:id/relocate.
func (h *Handler) RelocateBookFiles(c *gin.Context) {
	id := c.Param("id")
	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	book, err := store.GetBookByID(id)
	if err != nil || book == nil {
		httputil.RespondWithNotFound(c, "audiobook", id)
		return
	}

	var req organizer.RelocateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, "invalid request body")
		return
	}

	files, err := store.GetBookFiles(book.ID)
	if err != nil {
		httputil.InternalError(c, "failed to list book files", err)
		return
	}

	result := organizer.RelocateResult{}

	if req.SegmentID != "" && req.NewPath != "" {
		// Individual mode: update one file (SegmentID maps to file ID)
		cleanNewPath, err := pathvalidation.CleanAbsolutePath(req.NewPath)
		if err != nil {
			httputil.RespondWithBadRequest(c, "invalid new_path: "+err.Error())
			return
		}
		if !relocateTargetAllowed(store, cleanNewPath) {
			httputil.RespondWithBadRequest(c, "new_path is not in an allowed directory")
			return
		}
		for i, f := range files {
			if f.ID == req.SegmentID {
				if _, statErr := os.Stat(cleanNewPath); os.IsNotExist(statErr) {
					httputil.RespondWithBadRequest(c, "new path does not exist on disk")
					return
				}
				files[i].FilePath = cleanNewPath
				if err := store.UpdateBookFile(files[i].ID, &files[i]); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("update file %s: %v", f.ID, err))
				} else {
					result.Updated++
				}
				break
			}
		}
	} else if req.FolderPath != "" {
		// Folder mode: scan folder and match files by name
		cleanFolderPath, err := pathvalidation.CleanAbsolutePath(req.FolderPath)
		if err != nil {
			httputil.RespondWithBadRequest(c, "invalid folder_path: "+err.Error())
			return
		}
		if !relocateTargetAllowed(store, cleanFolderPath) {
			httputil.RespondWithBadRequest(c, "folder_path is not in an allowed directory")
			return
		}
		dirEntries, err := os.ReadDir(cleanFolderPath)
		if err != nil {
			httputil.RespondWithBadRequest(c, fmt.Sprintf("cannot read folder: %v", err))
			return
		}

		// Build map of filename -> full path in the new folder
		fileMap := make(map[string]string)
		for _, de := range dirEntries {
			if !de.IsDir() {
				fileMap[de.Name()] = filepath.Join(cleanFolderPath, de.Name())
			}
		}

		for i, f := range files {
			oldName := filepath.Base(f.FilePath)
			if newPath, ok := fileMap[oldName]; ok {
				files[i].FilePath = newPath
				if err := store.UpdateBookFile(files[i].ID, &files[i]); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("update file %s: %v", f.ID, err))
				} else {
					result.Updated++
				}
			}
		}
	} else {
		httputil.RespondWithBadRequest(c, "must provide segment_id+new_path or folder_path")
		return
	}

	// Update book's file_path to match first file
	if result.Updated > 0 && len(files) > 0 {
		book.FilePath = files[0].FilePath
		if _, err := store.UpdateBook(book.ID, book); err != nil {
			slog.Warn("failed to update book file_path", "err", err)
		}
	}

	httputil.RespondWithOK(c, result)
}

// GetSegmentTags returns raw metadata tags for a specific segment file.
// GET /audiobooks/:id/segments/:segmentId/tags.
func (h *Handler) GetSegmentTags(c *gin.Context) {
	id := c.Param("id")
	segmentId := c.Param("segmentId")
	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	book, err := store.GetBookByID(id)
	if err != nil || book == nil {
		httputil.RespondWithNotFound(c, "audiobook", id)
		return
	}

	found, err := store.GetBookFileByID(book.ID, segmentId)
	if err != nil {
		httputil.InternalError(c, "failed to get book file", err)
		return
	}
	if found == nil {
		httputil.RespondWithNotFound(c, "segment", segmentId)
		return
	}

	tags := map[string]string{}
	usedFallback := false
	tagsReadError := ""

	meta, err := metadata.ExtractMetadata(found.FilePath, nil)
	if err != nil {
		tagsReadError = err.Error()
	} else {
		usedFallback = meta.UsedFilenameFallback
		if meta.Title != "" {
			tags["title"] = meta.Title
		}
		if meta.Artist != "" {
			tags["artist"] = meta.Artist
		}
		if meta.Album != "" {
			tags["album"] = meta.Album
		}
		if meta.Genre != "" {
			tags["genre"] = meta.Genre
		}
		if meta.Series != "" {
			tags["series"] = meta.Series
		}
		if meta.SeriesIndex != 0 {
			tags["series_index"] = strconv.Itoa(meta.SeriesIndex)
		}
		if meta.Comments != "" {
			tags["comments"] = meta.Comments
		}
		if meta.Year != 0 {
			tags["year"] = strconv.Itoa(meta.Year)
		}
		if meta.Narrator != "" {
			tags["narrator"] = meta.Narrator
		}
		if meta.Language != "" {
			tags["language"] = meta.Language
		}
		if meta.Publisher != "" {
			tags["publisher"] = meta.Publisher
		}
		if meta.ISBN10 != "" {
			tags["isbn10"] = meta.ISBN10
		}
		if meta.ISBN13 != "" {
			tags["isbn13"] = meta.ISBN13
		}
	}

	resp := gin.H{
		"segment_id":             found.ID,
		"file_path":              found.FilePath,
		"format":                 found.Format,
		"size_bytes":             found.FileSize,
		"duration_sec":           database.NormalizeDurationSec(found.FileSize, found.Duration),
		"track_number":           found.TrackNumber,
		"total_tracks":           found.TrackCount,
		"tags":                   tags,
		"used_filename_fallback": usedFallback,
	}
	if tagsReadError != "" {
		resp["tags_read_error"] = tagsReadError
	}

	httputil.RespondWithOK(c, resp)
}
