// file: internal/metafetch/service.go
// version: 5.11.0
// guid: e5f6a7b8-c9d0-e1f2-a3b4-c5d6e7f8a9b0
// last-edited: 2026-09-02

package metafetch

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/openlibrary"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

// WriteBackEnqueuer is satisfied by server.WriteBackBatcher.
type WriteBackEnqueuer interface {
	Enqueue(bookID string)
}

// forwardedStores is what metafetch hands its store to rather than calls
// itself. Every entry is another package's declared parameter type, so this is
// the literal forwarding requirement, and it is kept separate from the groups
// below because that is the distinction the compiler probe reports: a direct
// call fails as "has no field or method", a forwarding requirement as "does not
// implement".
type forwardedStores interface {
	// database.EnsureSingletonBookTag, from the tag write-back path.
	database.BookTagSingletonStore
	// The tagger deps struct built in service_writeback.go.
	database.BookFileHashUpdater
	// database.{Get,Put}CachedMetadataFetch, the fetch/search response cache.
	database.RawKVStore
	// hasCheckpoint / setCheckpoint / clearCheckpoints in the write-back
	// phase gate, which forward to organizer.
	database.UserPreferenceStore
	// newPathOrganizer, for computing an organized destination path.
	organizer.OrganizerStore
}

// metadataCacheStore is the per-book candidate cache in cache.go.
type metadataCacheStore interface {
	GetMetadataCache(bookID string) (*database.MetadataCandidateCache, error)
	PutMetadataCache(entry *database.MetadataCandidateCache) error
	ListMetadataCacheKeys() ([]database.MetadataCacheSummary, error)
	DeleteMetadataCache(bookID string) error
}

// metadataFieldStateStore is the per-field manual-override state plus the
// change-history record written whenever a fetched value is applied.
type metadataFieldStateStore interface {
	GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error)
	UpsertMetadataFieldState(state *database.MetadataFieldState) error
	DeleteMetadataFieldState(bookID, field string) error
	RecordMetadataChange(record *database.MetadataChangeRecord) error
}

// metafetchBookStore is the book entity: lookup, create, update, plus the two
// duplicate-detection views and the flag their match writes.
type metafetchBookStore interface {
	GetBookByID(id string) (*database.Book, error)
	CreateBook(book *database.Book) (*database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
	GetBookTags(bookID string) ([]string, error)
	GetBooksByVersionGroup(groupID string) ([]database.Book, error)
	GetBooksByMetadataSourceHash(hash string) ([]database.Book, error)
	FlagMetadataHashDuplicate(primaryID, duplicateID string) error
}

// metafetchFileStore is everything about bytes on disk: the file rows, the
// move/rename bookkeeping the write-back path records, and the import roots a
// destination path is checked against.
type metafetchFileStore interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetBookFileByPath(filePath string) (*database.BookFile, error)
	CreateBookFile(file *database.BookFile) error
	UpdateBookFile(id string, file *database.BookFile) error
	RecordPathChange(change *database.BookPathChange) error
	SetLastWrittenAt(id string, t time.Time) error
	MarkNeedsRescan(bookID string) error
	GetAllImportPaths() ([]database.ImportPath, error)
}

// metafetchContributorStore is the get-or-create pass service_apply.go runs when
// a fetched result names an author or series that may not exist yet.
type metafetchContributorStore interface {
	GetAuthorByName(name string) (*database.Author, error)
	CreateAuthor(name string) (*database.Author, error)
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	SetBookAuthors(bookID string, authors []database.BookAuthor) error
	GetSeriesByName(name string, authorID *int) (*database.Series, error)
	CreateSeries(name string, authorID *int) (*database.Series, error)
}

// metafetchNarratorStore is the narrator half. Separate from the contributor
// group above because the apply path never creates a narrator by name -- it
// only reads and rewrites the join rows.
type metafetchNarratorStore interface {
	GetBookNarrators(bookID string) ([]database.BookNarrator, error)
	SetBookNarrators(bookID string, narrators []database.BookNarrator) error
	GetNarratorByID(id int) (*database.Narrator, error)
}

// Store is the dependency surface of Service, measured with an
// empty-interface compiler probe run under -gcflags=-e: 36 direct calls and
// five forwarding constraints, 64 distinct methods in total.
//
// It was previously database.Store -- 398 methods. The probe could not report
// this set until two things below it were narrowed, because both of them took
// database.Store and so re-imposed the union on anything that forwarded to
// them: database.EnsureSingletonBookTag, and the three checkpoint helpers in
// pipeline_checkpoint.go (which forward to organizer, where the same three
// functions already declared database.UserPreferenceStore -- the metafetch copy
// was an unnarrowed duplicate of an already-narrowed twin).
//
// Exported, unlike most consumer-side interfaces here, because a caller that
// forwards its own store into NewService has to be able to name this
// requirement -- see audiobooks.NewOrganizeService, which composes it with
// organizer.Store. organizer.Store is exported for the same reason.
type Store interface {
	forwardedStores

	metadataCacheStore
	metadataFieldStateStore
	metafetchBookStore
	metafetchFileStore
	metafetchContributorStore
	metafetchNarratorStore
}

type Service struct {
	db               Store
	olStore          *openlibrary.OLStore
	overrideSources  []metadata.MetadataSource // for testing
	isbnEnrichment   *ISBNService
	activityService  *activity.Service
	dedupEngine      *dedup.Engine
	metadataScorer   ai.MetadataCandidateScorer // optional; nil = fallback to F1
	llmScorer        ai.MetadataCandidateScorer // optional; nil = no LLM rerank tier
	writeBackBatcher WriteBackEnqueuer
	// safeWriteDeps guards tag/cover writes against Deluge-protected paths.
	// Zero-value = no guard (writes proceed in-place). Set via SetSafeWriteDeps.
	safeWriteDeps tagger.SafeWriteDeps

	// Memoized metadata-source chain. BuildSourceChain builds the client chain
	// (each source wrapped in a circuit breaker) ONCE and reuses it across every
	// per-book fetch so the Hardcover 60-rpm limiter (its requestLog) and the
	// per-source circuit breakers accumulate/persist across a whole batch instead
	// of being recreated per book. cachedChainFP is a fingerprint of the metadata
	// source config; the chain is rebuilt only when that changes (settings edit),
	// so runtime config changes are still honored. Guarded by chainMu. The chain
	// itself is safe for concurrent use by the worker pools that share it: every
	// source client is stateless (http.Client + immutable config), and the
	// Hardcover limiter and CircuitBreaker each carry their own mutex.
	chainMu       sync.Mutex
	cachedChain   []metadata.MetadataSource
	cachedChainFP string
}

type FetchMetadataResponse struct {
	Message         string
	Book            *database.Book
	Source          string
	FetchedCount    int
	PendingCoverURL string // set by ApplyMetadataCandidate for background download
	// SkippedLockedFields lists the lock keys (database.UserLockableFields) the
	// apply refused to write because the user had locked or overridden them.
	// Nil when nothing was skipped. Callers surface it in op summaries and
	// responses so a locked field is visibly "kept", never silently dropped.
	SkippedLockedFields []string
}

// MetadataCandidate represents a single search result for manual metadata matching.
type MetadataCandidate struct {
	Title          string  `json:"title"`
	Author         string  `json:"author"`
	Narrator       string  `json:"narrator,omitempty"`
	Series         string  `json:"series,omitempty"`
	SeriesPosition string  `json:"series_position,omitempty"`
	Year           int     `json:"year,omitempty"`
	Publisher      string  `json:"publisher,omitempty"`
	ISBN           string  `json:"isbn,omitempty"`
	ASIN           string  `json:"asin,omitempty"`
	CoverURL       string  `json:"cover_url,omitempty"`
	Description    string  `json:"description,omitempty"`
	Language       string  `json:"language,omitempty"`
	Source         string  `json:"source"`
	Score          float64 `json:"score"`
	// ScoreBreakdown is the ordered derivation of Score, for the review UI's
	// evidence panel. Replaying its steps reproduces Score -- asserted as a
	// property in service_scoring_breakdown_test.go, not merely hoped for.
	//
	// Nil when the candidate did not come from a scoring path (e.g. a
	// hand-constructed candidate in a test or a calibration harness). Nil means
	// "no derivation recorded"; it never means the score was zero.
	ScoreBreakdown *ScoreBreakdown `json:"score_breakdown,omitempty"`
	// DurationSec is the runtime from the metadata source (Audible: runtime_length_min × 60).
	// Zero means the source did not provide a duration.
	DurationSec int `json:"duration_sec,omitempty"`
	// DurationDeltaSec is abs(candidate_duration - book_duration) in seconds.
	// Zero means either side had no duration, or they matched exactly.
	// Non-zero lets the review UI flag candidates whose runtime diverges significantly.
	DurationDeltaSec int `json:"duration_delta_sec,omitempty"`
	// CategoryTags holds Audible category ladder node names (e.g. "Science Fiction").
	// Only populated for Audible-sourced candidates. Applied as book_tags on apply.
	CategoryTags []string `json:"category_tags,omitempty"`
	// DurationMismatch is true when DurationDeltaSec exceeds 600 s (10 min).
	// The review UI already renders a warning chip when duration_delta_sec > 600;
	// this flag makes the threshold decision explicit in the API response.
	DurationMismatch bool `json:"duration_mismatch,omitempty"`
	// DurationScore is the additive score component from the duration signal.
	// Positive when the candidate runtime closely matches the local file duration;
	// negative when the runtimes diverge significantly (wrong edition / abridged).
	// Zero when either side lacks duration data.
	// Scoring bands (delta ratio = |candidate_dur - book_dur| / book_dur):
	//   < 5%  → +20,  < 10% → +15,  < 20% → +10,
	//   > 50% → -10,  > 100% → -20.
	DurationScore float64 `json:"duration_score,omitempty"`
	// TranscriptionBoosted is true when this candidate's title, author, or
	// narrator matched the book's Whisper-transcribed intro fields, causing a
	// score multiplier to be applied. Lets the review UI surface a
	// "matched on transcription" filter so users can focus on books where
	// audio-derived metadata was the deciding factor.
	TranscriptionBoosted bool `json:"transcription_boosted,omitempty"`
	// AudibleRatingOverall is the Audible overall star rating (1–5 scale).
	// Zero means the source did not provide a rating.
	AudibleRatingOverall float64 `json:"audible_rating_overall,omitempty"`
	// AudibleRatingCount is the number of Audible star ratings.
	AudibleRatingCount int `json:"audible_rating_count,omitempty"`
	// GoogleRatingAverage is the Google Books average rating (1–5 scale).
	// Zero means the source did not provide a rating.
	GoogleRatingAverage float64 `json:"google_rating_average,omitempty"`
	// GoogleRatingCount is the number of Google Books ratings.
	GoogleRatingCount int `json:"google_rating_count,omitempty"`
}

// SearchMetadataResponse is returned by SearchMetadataForBook.
type SearchMetadataResponse struct {
	Results       []MetadataCandidate `json:"results"`
	Query         string              `json:"query"`
	SourcesTried  []string            `json:"sources_tried"`
	SourcesFailed map[string]string   `json:"sources_failed,omitempty"`
}

// SearchOptions carries optional per-request flags for SearchMetadataForBook.
// Adding a new option never breaks existing callers — they can keep using the
// zero-value or the simpler variadic signature.
type SearchOptions struct {
	// UseRerank asks the LLM rerank tier to run on the top candidates (if
	// MetadataLLMScoringEnabled is true on the server). When false, only
	// the base scorer tier runs.
	UseRerank bool
}

// embedCoverInBookFiles embeds cover art into all audio files for a book.
// Always overwrites existing cover art. Before overwriting, extracts the old
// cover and saves it as a timestamped version in covers/history/ so it can be
// restored later via the changelog.
func (mfs *Service) embedCoverInBookFiles(book *database.Book, coverPath string) {
	if book == nil || book.FilePath == "" || coverPath == "" {
		return
	}

	// A TagLib CAPABILITY list — which containers can carry an embedded cover
	// picture — not supported_extensions. It stays narrow on purpose: .wav and
	// .aiff have no standard cover atom, and .aax/.aaxc are DRM-encrypted.
	coverEmbeddableExts := map[string]bool{
		".mp3": true, ".m4b": true, ".m4a": true, ".aac": true,
		".ogg": true, ".flac": true,
	}

	// If book is in a protected path, get or create a library copy
	if mfs.isProtectedPath(book.FilePath) {
		libCopy := mfs.ensureLibraryCopy(book)
		if libCopy == nil {
			slog.Warn("cannot embed cover: protected book has no library copy",
				"book_id", book.ID, "book_title", book.Title,
				"protected_path", book.FilePath)
			return
		}
		book = libCopy
	}

	// collectFiles gathers all audio files that need cover embedding
	var files []string
	ext := strings.ToLower(filepath.Ext(book.FilePath))
	if coverEmbeddableExts[ext] {
		files = append(files, book.FilePath)
	} else {
		// Multi-file book
		bookFiles, err := mfs.db.GetBookFiles(book.ID)
		if err != nil {
			slog.Warn("failed to list book files for cover embedding",
				"book_id", book.ID, "book_title", book.Title, "error", err)
			return
		}
		for _, bf := range bookFiles {
			if bf.Missing {
				continue
			}
			if mfs.isProtectedPath(bf.FilePath) {
				continue
			}
			bfExt := strings.ToLower(filepath.Ext(bf.FilePath))
			if coverEmbeddableExts[bfExt] {
				files = append(files, bf.FilePath)
			}
		}
	}

	if len(files) == 0 {
		return
	}

	newCoverData, _ := os.ReadFile(coverPath)
	newHash := ""
	if len(newCoverData) > 0 {
		newHash = fmt.Sprintf("%x", sha256.Sum256(newCoverData))[:12]
	}

	// Every file of a multi-file book must carry the artwork. The skip check is
	// therefore PER FILE.
	//
	// It used to compare only files[0] and return for the whole book on a match,
	// which is all-or-nothing in the wrong direction: a book whose first file
	// already had the cover skipped the embed for every remaining file, so those
	// files stayed permanently artwork-less and no amount of re-running fixed it.
	// Comparing per file both closes that hole and keeps the saving, since a file
	// that already matches is still skipped — and skipping is what matters, as an
	// embed is a full rewrite of the audio file.
	embedded, skipped, failed := 0, 0, 0
	archived := false
	for _, f := range files {
		if newHash != "" {
			if existingData, _, _ := metadata.ExtractCoverArtBytes(f); len(existingData) > 0 {
				if fmt.Sprintf("%x", sha256.Sum256(existingData))[:12] == newHash {
					skipped++
					continue
				}
			}
		}

		// Archive the cover we are about to overwrite, once per book, from the
		// first file we actually touch.
		if !archived {
			mfs.archiveExistingCover(book.ID, f)
			archived = true
		}

		// EmbedCoverArtSafe imports the file from a Deluge-protected path before
		// writing if the pre-flight guard is wired (mfs.safeWriteDeps).
		if err := tagger.EmbedCoverArtSafe(context.Background(), f, coverPath, mfs.safeWriteDeps); err != nil {
			slog.Warn("cover art embedding failed for file",
				"path", f, "error", err,
				"book_id", book.ID, "book_title", book.Title,
				"cover_path", coverPath)
			failed++
		} else {
			embedded++
		}
	}
	if embedded > 0 || failed > 0 {
		slog.Info("cover art embed complete for book",
			"id", book.ID, "embedded", embedded, "skipped_unchanged", skipped, "failed", failed, "files", len(files))
	} else if skipped > 0 {
		slog.Debug("cover art already present in every file, nothing to embed",
			"id", book.ID, "files", len(files))
	}
}

// archiveExistingCover extracts the current embedded cover art from an audio
// file and saves it as a timestamped version in covers/history/{bookID}/ so it
// can be restored later. Records a metadata change for changelog tracking.
func (mfs *Service) archiveExistingCover(bookID string, audioFilePath string) {
	data, mimeType, err := metadata.ExtractCoverArtBytes(audioFilePath)
	if err != nil || len(data) == 0 {
		return // no existing cover to archive
	}

	// Determine extension from MIME type
	ext := ".jpg"
	switch {
	case strings.Contains(mimeType, "png"):
		ext = ".png"
	case strings.Contains(mimeType, "webp"):
		ext = ".webp"
	case strings.Contains(mimeType, "gif"):
		ext = ".gif"
	}

	// Hash the cover data for deduplication
	coverHash := fmt.Sprintf("%x", sha256.Sum256(data))

	// Check if we already have this exact image archived (by hash)
	dedupDir := filepath.Join(config.AppConfig.RootDir, "covers", "dedup")
	if err := os.MkdirAll(dedupDir, 0775); err != nil {
		slog.Warn("failed to create cover dedup dir", "error", err)
		return
	}

	dedupPath := filepath.Join(dedupDir, coverHash+ext)
	if _, err := os.Stat(dedupPath); err != nil {
		// New unique image — save to dedup store
		if err := os.WriteFile(dedupPath, data, 0664); err != nil {
			slog.Warn("failed to write dedup cover for", "id", bookID, "error", err)
			return
		}
	}

	// Create a history entry that references the dedup hash instead of storing a copy
	historyDir := filepath.Join(config.AppConfig.RootDir, "covers", "history", bookID)
	if err := os.MkdirAll(historyDir, 0775); err != nil {
		slog.Warn("failed to create cover history dir", "error", err)
		return
	}

	ts := time.Now().Format("20060102-150405")
	// History entry is a symlink to the dedup store to avoid duplicate storage
	archivePath := filepath.Join(historyDir, ts+ext)
	if err := os.Symlink(dedupPath, archivePath); err != nil {
		// Symlink failed (cross-device, Windows, etc.) — fall back to hardlink or copy
		if err := os.Link(dedupPath, archivePath); err != nil {
			// Hardlink also failed — just copy
			if err := os.WriteFile(archivePath, data, 0664); err != nil {
				slog.Warn("failed to archive old cover for", "id", bookID, "error", err)
				return
			}
		}
	}
	slog.Info("archived old cover art (hash)", "path", archivePath, "hash", coverHash[:12])

	// Record in metadata change history so it appears in the changelog
	now := time.Now()
	summaryJSON := jsonEncodeString(fmt.Sprintf("cover_art: archived previous cover to %s", filepath.Base(archivePath)))
	record := &database.MetadataChangeRecord{
		BookID:     bookID,
		Field:      "cover_art",
		NewValue:   &summaryJSON,
		ChangeType: "cover-archive",
		Source:     "system",
		ChangedAt:  now,
	}
	if err := mfs.db.RecordMetadataChange(record); err != nil {
		slog.Warn("failed to record cover archive history for", "id", bookID, "error", err)
	}
	// Dual-write to unified activity log
	if mfs.activityService != nil {
		_ = mfs.activityService.Record(database.ActivityEntry{
			Tier:    "change",
			Type:    "metadata_apply",
			Level:   "info",
			Source:  "background",
			BookID:  bookID,
			Summary: fmt.Sprintf("Archived cover art to %s", filepath.Base(archivePath)),
		})
	}
}

// looksLikeASIN checks if a string looks like an Amazon ASIN (10 alphanumeric chars, typically starts with B0).
func looksLikeASIN(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != 10 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
			return false
		}
	}
	return true
}

// extractASIN finds an ASIN-like pattern (B0 followed by 8 alphanumeric chars) anywhere in the string.
func extractASIN(s string) string {
	s = strings.TrimSpace(s)
	// Split on whitespace and check each token
	for word := range strings.FieldsSeq(s) {
		word = strings.Trim(word, ",.;:!?()[]{}\"'")
		if looksLikeASIN(word) {
			return word
		}
	}
	return ""
}

// metadataCanonicalID extracts the canonical external identifier from a
// MetadataCandidate for use in the metadata_source_hash computation.
// Priority: ASIN > ISBN-13 > ISBN-10 > ISBN. Returns "" if none present.
func metadataCanonicalID(c MetadataCandidate) string {
	if c.ASIN != "" {
		return c.ASIN
	}
	if c.ISBN != "" && len(c.ISBN) == 13 {
		return c.ISBN
	}
	if c.ISBN != "" {
		return c.ISBN
	}
	return ""
}

// audioFilesInDir returns the audio files found directly inside dir.
// It globs for common audiobook extensions. Returns nil if dir is not a
// directory or contains no matching files.
// virtualBookFiles is the single-file (row-less) book's stand-in for its
// book_file rows: one entry built from book.FilePath, or nil when FilePath is
// empty or names a directory. It carries the book row's recorded size because
// that is the only identity a stranded-temp resume can check against
// (organizer.strandedTempMismatch refuses a size-less entry forever) — 12,525
// prod books have no rows, so dropping the size here parked their renames for
// good.
func virtualBookFiles(id string, book *database.Book) []database.BookFile {
	if book == nil || book.FilePath == "" {
		return nil
	}
	ext := strings.TrimPrefix(filepath.Ext(book.FilePath), ".")
	if ext == "" {
		return nil
	}
	vf := database.BookFile{
		ID:       "virtual-" + id,
		BookID:   id,
		FilePath: book.FilePath,
		Format:   ext,
	}
	if book.FileSize != nil {
		vf.FileSize = *book.FileSize
	}
	return []database.BookFile{vf}
}

// RunApplyPipelineRenameOnly runs only the rename portion of the apply pipeline.
// Used by the "Save to Files" button to rename files without re-writing tags (tags are written separately).
func (mfs *Service) RunApplyPipelineRenameOnly(id string, book *database.Book) error {
	// If the book is in a protected path, run on library copy
	if mfs.isProtectedPath(book.FilePath) {
		libCopy := mfs.ensureLibraryCopy(book)
		if libCopy == nil {
			return fmt.Errorf("no library copy for protected book %s", id)
		}
		id = libCopy.ID
		book = libCopy
	}

	bookFiles, err := mfs.db.GetBookFiles(id)
	if err != nil {
		return fmt.Errorf("list book files: %w", err)
	}
	bookFiles = dedupeBookFilesByPath(id, bookFiles)

	// For single-file books with no book files, create a virtual entry from book.FilePath
	if len(bookFiles) == 0 {
		bookFiles = virtualBookFiles(id, book)
	}
	if len(bookFiles) == 0 {
		return nil
	}

	// Plan the rename through the Organizer, so it resolves author/series and
	// expands the naming patterns exactly the way the organize path does. This
	// block used to hand-roll its own FormatVars and read path_format — a
	// separate builder that disagreed with organize by two directory levels and
	// pulled every book back and forth between the two answers.
	entries, err := newPathOrganizer(mfs.db).ComputeTargetPaths(book, bookFiles)
	if err != nil {
		// A broken naming pattern must NOT fall through to a rename: the target
		// would be built from a half-substituted template and would relocate the
		// library somewhere no scan expects.
		return fmt.Errorf("compute target paths for book %s: %w", id, err)
	}

	renameResult, renameErr := RenameFiles(entries)
	// Even when RenameFiles returns an error, entries in renameResult.Succeeded
	// have physically moved on disk — their DB paths MUST still be updated
	// below, or the library loses track of files that did move. The error is
	// returned after the DB sync + empty-dir cleanup.

	// Update book file records with new paths
	bfMap := make(map[string]*database.BookFile, len(bookFiles))
	for i := range bookFiles {
		bfMap[bookFiles[i].ID] = &bookFiles[i]
	}
	for _, entry := range renameResult.Succeeded {
		if strings.HasPrefix(entry.SegmentID, "virtual-") {
			// Virtual entry = single-file book. Update book.FilePath directly to the new file path.
			book.FilePath = entry.TargetPath
			// Keep in-memory virtual BookFile in sync so ITunesPath can be computed below.
			if len(bookFiles) > 0 && bookFiles[0].ID == entry.SegmentID {
				bookFiles[0].FilePath = entry.TargetPath
			}
			if _, err := mfs.db.UpdateBook(id, book); err != nil {
				slog.Warn("failed to update book path for", "id", id, "error", err)
			} else {
				slog.Info("renamed single-file book", "id", id, "path", entry.TargetPath)
			}
		} else if bf, ok := bfMap[entry.SegmentID]; ok {
			bf.FilePath = entry.TargetPath
			bf.ITunesPath = ComputeITunesPath(entry.TargetPath)
			if err := mfs.db.UpdateBookFile(bf.ID, bf); err != nil {
				slog.Warn("failed to update book_file path for", "id", bf.ID, "error", err)
			}
		}
		// Record path change for each successful rename
		if entry.SourcePath != entry.TargetPath {
			_ = mfs.db.RecordPathChange(&database.BookPathChange{
				BookID:     id,
				OldPath:    entry.SourcePath,
				NewPath:    entry.TargetPath,
				ChangeType: "rename",
			})
			// Dual-write to unified activity log
			if mfs.activityService != nil {
				_ = mfs.activityService.Record(database.ActivityEntry{
					Tier:    "change",
					Type:    "rename",
					Level:   "info",
					Source:  "background",
					BookID:  id,
					Summary: fmt.Sprintf("Moved: %s → %s", filepath.Base(entry.SourcePath), filepath.Base(entry.TargetPath)),
					Details: map[string]any{"old_path": entry.SourcePath, "new_path": entry.TargetPath},
				})
			}
		}
	}

	// Update book file_path for multi-segment books (directory path)
	if len(renameResult.Succeeded) > 0 && !strings.HasPrefix(renameResult.Succeeded[0].SegmentID, "virtual-") {
		newBookPath := filepath.Dir(renameResult.Succeeded[0].TargetPath)
		if newBookPath != book.FilePath {
			book.FilePath = newBookPath
			if _, err := mfs.db.UpdateBook(id, book); err != nil {
				slog.Warn("failed to update book path for", "id", id, "error", err)
			} else {
				slog.Info("renamed book files for", "id", id, "path", newBookPath)
			}
		}
	}

	// Always ensure itunes_path is set on each BookFile if a mapping exists.
	for i := range bookFiles {
		if bookFiles[i].ITunesPath == "" {
			if itunesPath := ComputeITunesPath(bookFiles[i].FilePath); itunesPath != "" {
				bookFiles[i].ITunesPath = itunesPath
				if !strings.HasPrefix(bookFiles[i].ID, "virtual-") {
					if err := mfs.db.UpdateBookFile(bookFiles[i].ID, &bookFiles[i]); err != nil {
						slog.Warn("failed to update itunes_path for book file", "id", bookFiles[i].ID, "error", err)
					}
				}
			}
		}
	}

	// Clean up empty directories left after rename
	for _, entry := range renameResult.Succeeded {
		oldDir := filepath.Dir(entry.SourcePath)
		if oldDir != filepath.Dir(entry.TargetPath) {
			removeEmptyDirs(oldDir, config.AppConfig.RootDir)
		}
	}

	// Now that DB paths for every succeeded rename are persisted, surface the
	// rename failure (skipping dedup/writeback follow-ups for the failed run).
	if renameErr != nil {
		return fmt.Errorf("rename files: %w", renameErr)
	}

	// Trigger dedup check after metadata apply
	if mfs.dedupEngine != nil {
		go func() {
			if _, err := mfs.dedupEngine.CheckBook(context.Background(), id); err != nil {
				slog.Warn("dedup re-check failed for book after metadata apply", "id", id, "error", err)
			}
		}()
	}

	// Enqueue iTunes writeback so location changes from the rename
	// propagate to iTunes. Callers (bulk write-back) also enqueue,
	// the batcher dedupes.
	if mfs.writeBackBatcher != nil {
		mfs.writeBackBatcher.Enqueue(id)
	}

	return nil
}

// truncateActivity shortens s to maxLen runes, appending "..." if truncated.
func truncateActivity(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// isbnEnrichmentStore is what ISBNService reads and writes. Measured with an
// empty-interface compiler probe: four methods. It was database.Store -- 398
// methods -- until 2026-08-19.
type isbnEnrichmentStore interface {
	GetBookByID(id string) (*database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetAuthorByID(id int) (*database.Author, error)
}

// bookFileLister and rejectedKeyScanner are the two one-method surfaces the
// free functions in batch.go need. Each took database.Store.
type bookFileLister interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
}

type rejectedKeyScanner interface {
	ScanPrefix(prefix string) ([]database.KVPair, error)
}
