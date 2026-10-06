// file: internal/server/handlers/audiobooks/handler_files.go
// version: 1.9.0
// guid: 82f8d1f7-46d5-4ead-b5c1-ba796fd785f9
// last-edited: 2026-10-06

// File / segment endpoints for the audiobooks domain: segment listing,
// book-file listing + patch, track-info extraction, relocate, and segment
// tags. Split out of handler.go for readability; one Handler, one New().

package audiobookshandler

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// fileStatTimeout caps the WHOLE disk check for one files/segments response.
// os.Stat does not honour context cancellation, so a hung NAS mount would
// otherwise stall the request indefinitely; past this deadline the remaining
// files are reported as unknown (file_exists: null) instead of waiting.
const fileStatTimeout = 3 * time.Second

// fileStatWorkers bounds the stat fan-out per request.
const fileStatWorkers = 8

// fileStatMaxInFlight bounds stats in flight across the WHOLE process. A
// goroutine blocked in os.Stat on a hung mount holds an OS thread until the
// kernel gives up, and the per-request deadline cannot free it; without a
// process-wide cap, repeated page loads against a hung mount accumulate stuck
// threads toward Go's 10,000-thread fatal limit. A stat slot is released only
// when os.Stat actually returns, so this is a hard ceiling on stuck threads.
// When every slot is taken the stat is not attempted: the path reads unknown.
const fileStatMaxInFlight = 32

// fileStatBreakerCooldown is how long a root stays tripped after a stat under
// it outlived the request deadline. While tripped, paths under that root read
// unknown without being stat'ed, so a hung mount costs one timeout per
// cooldown instead of one per request.
const fileStatBreakerCooldown = 60 * time.Second

// fileDiskState is what a live os.Stat said about one book_file path.
// Exists is nil when the answer is unknown: an empty path, a stat error other
// than not-exist (permission, I/O), the deadline passing first, the process-
// wide stat cap being full, or the path's root being tripped. CheckError then
// says which.
type fileDiskState struct {
	Exists     *bool
	CheckError string
}

// statChecker holds the process-wide stat state: the in-flight semaphore and
// the per-root circuit breaker. One instance (defaultStatChecker) serves every
// request; tests build their own.
type statChecker struct {
	slots    chan struct{}
	cooldown time.Duration
	now      func() time.Time

	mu      sync.Mutex
	tripped map[string]time.Time // root -> tripped until
}

func newStatChecker(maxInFlight int, cooldown time.Duration) *statChecker {
	return &statChecker{
		slots:    make(chan struct{}, maxInFlight),
		cooldown: cooldown,
		now:      time.Now,
		tripped:  make(map[string]time.Time),
	}
}

var defaultStatChecker = newStatChecker(fileStatMaxInFlight, fileStatBreakerCooldown)

// statRoot is the breaker key for a path: its first two components
// ("/mnt/nas/books/a.m4b" -> "/mnt/nas"). That is the mount-point depth of
// every library root this app is deployed with (/mnt/<share>, /Volumes/<vol>,
// /srv/<x>); it is a heuristic, since resolving the real mount would need a
// stat — the very call that hangs.
func statRoot(path string) string {
	clean := filepath.ToSlash(filepath.Clean(path))
	parts := strings.SplitN(strings.TrimPrefix(clean, "/"), "/", 3)
	if len(parts) >= 2 {
		return "/" + parts[0] + "/" + parts[1]
	}
	return "/" + parts[0]
}

func (sc *statChecker) isTripped(root string) bool {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	until, ok := sc.tripped[root]
	if !ok {
		return false
	}
	if sc.now().After(until) {
		delete(sc.tripped, root)
		return false
	}
	return true
}

func (sc *statChecker) trip(root string) {
	sc.mu.Lock()
	sc.tripped[root] = sc.now().Add(sc.cooldown)
	sc.mu.Unlock()
}

// statOne stats one path unless its root is tripped or the process-wide cap
// is full. It never blocks waiting for a slot.
func (sc *statChecker) statOne(path string, stat func(string) (os.FileInfo, error)) fileDiskState {
	if sc.isTripped(statRoot(path)) {
		return fileDiskState{CheckError: "disk check suspended: an earlier stat under " + statRoot(path) + " timed out"}
	}
	select {
	case sc.slots <- struct{}{}:
	default:
		return fileDiskState{CheckError: "disk check busy: too many stats in flight"}
	}
	defer func() { <-sc.slots }()
	return classifyStat(stat(path))
}

// statFilePaths stats every path through defaultStatChecker under one overall
// deadline (fileStatTimeout, or the request context if it ends first). The
// result is index-aligned with paths and always fully populated.
func statFilePaths(ctx context.Context, paths []string) []fileDiskState {
	ctx, cancel := context.WithTimeout(ctx, fileStatTimeout)
	defer cancel()
	return defaultStatChecker.statPaths(ctx, paths, os.Stat)
}

// statPaths is statFilePaths with the deadline already on ctx and the stat
// call injected, so tests can drive a hung or failing filesystem.
//
// Bounds: at most fileStatWorkers goroutines per request, and at most
// cap(sc.slots) stats in flight process-wide. A worker blocked in a hung stat
// when the deadline passes is left behind (it cannot be interrupted) but
// keeps its process-wide slot until the stat returns, so stuck threads never
// exceed that cap; it sends into a buffered channel, so it never blocks on
// the send. A path whose stat had STARTED but not returned by the deadline
// trips its root; a path still queued behind it reads "timed out" without
// tripping anything, so a merely slow, large book does not suspend a root.
func (sc *statChecker) statPaths(ctx context.Context, paths []string, stat func(string) (os.FileInfo, error)) []fileDiskState {
	out := make([]fileDiskState, len(paths))
	if len(paths) == 0 {
		return out
	}

	type statResult struct {
		idx   int
		state fileDiskState
	}
	jobs := make(chan int, len(paths))
	results := make(chan statResult, len(paths))
	pending := 0
	for i, p := range paths {
		if p == "" {
			out[i] = fileDiskState{CheckError: "no file path"}
			continue
		}
		jobs <- i
		pending++
	}
	close(jobs)

	started := make([]atomic.Bool, len(paths))
	// Not joined: a worker stuck in a hung stat must not hold the response.
	for range min(fileStatWorkers, pending) {
		go func() {
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				started[i].Store(true)
				results <- statResult{idx: i, state: sc.statOne(paths[i], stat)}
			}
		}()
	}

	done := make([]bool, len(paths))
	for received := 0; received < pending; received++ {
		select {
		case r := <-results:
			out[r.idx] = r.state
			done[r.idx] = true
		case <-ctx.Done():
			// A stat that finished as the deadline fired is a real answer;
			// select picks randomly between ready cases, so drain first.
			for drained := false; !drained; {
				select {
				case r := <-results:
					out[r.idx] = r.state
					done[r.idx] = true
				default:
					drained = true
				}
			}
			for i, p := range paths {
				if p != "" && !done[i] {
					out[i] = fileDiskState{CheckError: "disk check timed out"}
					if started[i].Load() {
						sc.trip(statRoot(p))
					}
				}
			}
			return out
		}
	}
	return out
}

// classifyStat turns one stat outcome into present, definitely absent, or
// unknown. Only fs.ErrNotExist is "absent": a permission or I/O error says
// nothing about whether the file is there.
func classifyStat(_ os.FileInfo, err error) fileDiskState {
	switch {
	case err == nil:
		yes := true
		return fileDiskState{Exists: &yes}
	case errors.Is(err, fs.ErrNotExist):
		no := false
		return fileDiskState{Exists: &no}
	default:
		return fileDiskState{CheckError: err.Error()}
	}
}

// bookFilePaths returns each row's FilePath, index-aligned with files.
func bookFilePaths(files []database.BookFile) []string {
	paths := make([]string, len(files))
	for i := range files {
		paths[i] = files[i].FilePath
	}
	return paths
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

	// Convert BookFile to legacy segment JSON shape. file_exists is a LIVE
	// disk check, and missing is the stored flag. They used to be one field:
	// file_exists was !f.Missing, so a row whose file had vanished without the
	// scanner noticing read as present (393 of 409 sampled prod rows pointed
	// at absent paths while claiming file_exists=true). See statFilePaths for
	// the timeout and pool bounds.
	disk := statFilePaths(c.Request.Context(), bookFilePaths(files))
	result := make([]gin.H, 0, len(files))
	for i, f := range files {
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
			"missing":          f.Missing,
			"file_exists":      disk[i].Exists,
			"file_check_error": disk[i].CheckError,
		})
	}

	httputil.RespondWithOK(c, result)
}

// ListBookFiles returns all book_files rows for a book with a live
// disk-existence check. GET /audiobooks/:id/files.
//
// file_exists is an os.Stat made for this response (null when unknown, with
// file_check_error saying why); missing is the stored BookFile.Missing flag,
// which only changes when a scan or repair notices. The stat is affordable here
// because the endpoint is per book, not a library-wide listing. It is bounded
// three ways (see statPaths): 8 workers per request under a 3s deadline, a
// process-wide cap on stats in flight (a full cap reads unknown rather than
// waiting), and a per-root breaker that stops stat'ing a root for 60s after a
// stat under it hung past the deadline.
//
// ?disk_check=false skips the stat for callers that only need the paths and
// fetch many books at once (the dedup embedding tab loads files for every
// candidate on a page). file_exists is then null with file_check_error
// "not checked", so the response still never claims disk truth it lacks.
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
	var disk []fileDiskState
	if c.Query("disk_check") == "false" {
		disk = make([]fileDiskState, len(files))
		for i := range disk {
			disk[i] = fileDiskState{CheckError: "not checked"}
		}
	} else {
		disk = statFilePaths(c.Request.Context(), bookFilePaths(files))
	}
	results := make([]gin.H, 0, len(files))
	for i, f := range files {
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
			"file_exists":                 disk[i].Exists,
			"file_check_error":            disk[i].CheckError,
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
		newBookPath := files[0].FilePath
		// ModifyBook sets FilePath alone. `book` was read before the disk
		// checks above, so a whole-row UpdateBook would revert any column
		// another writer committed meanwhile.
		updated, err := store.ModifyBook(book.ID, func(b *database.Book) error {
			if b.FilePath == newBookPath {
				return database.ErrSkipBookWrite
			}
			b.FilePath = newBookPath
			return nil
		})
		switch {
		case err != nil:
			filesLog.Warn("failed to update book file_path for %s: %s", book.ID, err.Error())
		case updated == nil:
			filesLog.Warn("failed to update book file_path for %s: book no longer exists", book.ID)
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
