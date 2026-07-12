// file: internal/metadata/enhanced.go
// version: 1.12.0
// guid: 7e8d9c0b-1a2f-3e4d-5c6b-7a8d9c0b1a2f
// last-edited: 2026-07-11

package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// metadataBulkOpConcurrency bounds the worker pool for BatchUpdateMetadata and
// ImportMetadata (CONC-13). Both are request-driven bulk ops that run inline
// on a user-facing HTTP request rather than as a background op with its own
// budget, so concurrency is a small fixed value (fix-pattern #5 in
// docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md) — not
// runtime.NumCPU() — to avoid starving the server on a large payload.
const metadataBulkOpConcurrency = 4

// inertMetadataReporter is a minimal registry.Reporter for BatchUpdateMetadata
// and ImportMetadata, which are synchronous helpers called directly from HTTP
// handlers (see internal/server/handlers/metadata/handler.go) with no live
// operations.Reporter in scope. registry.RunItems's concurrent path only ever
// calls UpdateProgress and SetCurrentItem (see run_items.go's runOne), so the
// remaining methods are safe no-ops.
type inertMetadataReporter struct{}

func (inertMetadataReporter) UpdateProgress(current, total int, message string) error { return nil }
func (inertMetadataReporter) Log(level slog.Level, message string, attrs ...slog.Attr) error {
	return nil
}
func (inertMetadataReporter) Logger() *slog.Logger       { return slog.Default() }
func (inertMetadataReporter) Checkpoint(state any) error { return nil }
func (inertMetadataReporter) IsCanceled() bool           { return false }
func (inertMetadataReporter) RunPhase(ctx context.Context, name string, fn func(context.Context, registry.Reporter) error) error {
	return fn(ctx, inertMetadataReporter{})
}
func (inertMetadataReporter) Trigger(ctx context.Context, eventName string, payload any) error {
	return nil
}
func (inertMetadataReporter) SetCurrentItem(label string) {}

// MetadataUpdate represents a metadata update operation
type MetadataUpdate struct {
	BookID   string                 `json:"book_id" binding:"required"`
	Updates  map[string]interface{} `json:"updates" binding:"required"`
	Validate bool                   `json:"validate"`
}

// ValidationRule defines a validation constraint
type ValidationRule struct {
	Field           string
	Required        bool
	MinLength       int
	MaxLength       int
	AllowedValues   []string
	CustomValidator func(interface{}) error
}

// ErrTaglibUnavailable is returned when native taglib writing is not compiled in.
var ErrTaglibUnavailable = errors.New("taglib native writer unavailable")

// DefaultValidationRules returns default validation rules for audiobook metadata
func DefaultValidationRules() map[string]ValidationRule {
	return map[string]ValidationRule{
		"title": {
			Field:     "title",
			Required:  true,
			MinLength: 1,
			MaxLength: 500,
		},
		"author": {
			Field:     "author",
			Required:  false,
			MinLength: 0,
			MaxLength: 200,
		},
		"series": {
			Field:     "series",
			Required:  false,
			MinLength: 0,
			MaxLength: 200,
		},
		"narrator": {
			Field:     "narrator",
			Required:  false,
			MinLength: 0,
			MaxLength: 200,
		},
		"format": {
			Field:         "format",
			Required:      false,
			AllowedValues: []string{"m4b", "mp3", "m4a", "aac", "ogg", "flac", "wma"},
		},
		"publishDate": {
			Field:    "publishDate",
			Required: false,
			CustomValidator: func(v interface{}) error {
				str, ok := v.(string)
				if !ok {
					return fmt.Errorf("publishDate must be a string")
				}
				// Try parsing as date (YYYY-MM-DD format)
				_, err := time.Parse("2006-01-02", str)
				if err != nil {
					return fmt.Errorf("publishDate must be in YYYY-MM-DD format")
				}
				return nil
			},
		},
	}
}

// ValidateMetadata validates metadata updates against rules
func ValidateMetadata(updates map[string]interface{}, rules map[string]ValidationRule) []error {
	var errors []error

	for field, value := range updates {
		rule, exists := rules[field]
		if !exists {
			continue // No validation rule for this field
		}

		// Check required
		if rule.Required && (value == nil || value == "") {
			errors = append(errors, fmt.Errorf("field %s is required", field))
			continue
		}

		// Skip further validation if value is nil/empty and not required
		if value == nil || value == "" {
			continue
		}

		// Convert to string for validation
		strValue := fmt.Sprintf("%v", value)

		// Check length constraints
		if rule.MinLength > 0 && len(strValue) < rule.MinLength {
			errors = append(errors, fmt.Errorf("field %s must be at least %d characters", field, rule.MinLength))
		}
		if rule.MaxLength > 0 && len(strValue) > rule.MaxLength {
			errors = append(errors, fmt.Errorf("field %s must be at most %d characters", field, rule.MaxLength))
		}

		// Check allowed values
		if len(rule.AllowedValues) > 0 {
			valid := false
			for _, allowed := range rule.AllowedValues {
				if strings.EqualFold(strValue, allowed) {
					valid = true
					break
				}
			}
			if !valid {
				errors = append(errors, fmt.Errorf("field %s must be one of: %v", field, rule.AllowedValues))
			}
		}

		// Custom validator
		if rule.CustomValidator != nil {
			if err := rule.CustomValidator(value); err != nil {
				errors = append(errors, fmt.Errorf("field %s validation failed: %w", field, err))
			}
		}
	}

	return errors
}

// batchUpdateStore is the store surface BatchUpdateMetadata needs (INIT-3-T4):
// the book reader/writer (embedded database.BookStore, as the parameter was
// before) plus the author/series lookup-or-create helpers and the
// metadata-change recorder used for author/series name → ID resolution.
// Widening the parameter from database.BookStore leaves ImportMetadata (still
// database.BookStore) untouched; the sole production caller passes
// handlers/metadata's MetadataStore, which already embeds database.BookStore
// and declares these methods. No internal/database interface, mock, or
// generated file changes — this is a call-signature widening only.
type batchUpdateStore interface {
	database.BookStore
	GetAuthorByName(name string) (*database.Author, error)
	CreateAuthor(name string) (*database.Author, error)
	GetSeriesByName(name string, authorID *int) (*database.Series, error)
	CreateSeries(name string, authorID *int) (*database.Series, error)
	RecordMetadataChange(record *database.MetadataChangeRecord) error
}

// BatchUpdateMetadata applies metadata updates to multiple books with validation.
//
// Parallelized via registry.RunItems (CONC-13): each worker processes one
// update (validate → GetBookByID → mutate → UpdateBook) independently. The
// only state shared across workers is the result aggregation (errs,
// successCount), which is guarded by mu so the parallel pass produces the
// same result set as the original serial loop (order-independent — item
// index i is preserved in error messages for identification, but completion
// order across workers is not guaranteed to match input order).
//
// Store-safety: store.UpdateBook (PebbleStore) commits via a Pebble batch
// (safe for concurrent writers) and write-throughs to memdb via memSync,
// which serializes on hashicorp/go-memdb's Txn(true) exclusive writer lock —
// concurrent UpdateBook calls cannot race or corrupt state at this
// concurrency. rules is read-only after construction, so sharing it across
// workers needs no lock.
//
// Author/series resolution (INIT-3-T4) is guarded by resolveMu: GetAuthorByName
// → CreateAuthor (and the series equivalent) is a check-then-create that the
// store does NOT make atomic (nextID is locked, but the existence check and the
// name-index write are not), so two workers resolving the SAME new name at this
// concurrency could otherwise create duplicate author/series rows whose
// name-index points at only one — a data-integrity bug on a prod write path
// (CLAUDE.md concurrency rule: exclusive access where parallel workers must
// never double-create the same row). Serializing only the lookup-or-create
// section is cheap (local Pebble point-gets) and correctness outranks the small
// throughput hit here.
func BatchUpdateMetadata(updates []MetadataUpdate, store batchUpdateStore, validate bool) ([]error, int) {
	rules := DefaultValidationRules()

	var mu sync.Mutex
	var resolveMu sync.Mutex
	var errs []error
	successCount := 0

	// RunItems's fn signature doesn't carry the item's original index, and
	// the error messages below identify updates by their request-payload
	// index — so iterate over indices into updates rather than the updates
	// themselves.
	indices := make([]int, len(updates))
	for i := range indices {
		indices[i] = i
	}

	_ = registry.RunItems(context.Background(), inertMetadataReporter{}, indices, func(_ context.Context, i int) error {
		update := updates[i]

		// Validate if requested
		if validate || update.Validate {
			validationErrors := ValidateMetadata(update.Updates, rules)
			if len(validationErrors) > 0 {
				mu.Lock()
				errs = append(errs, fmt.Errorf("update %d (book %s): %v", i, update.BookID, validationErrors))
				mu.Unlock()
				return nil
			}
		}

		// Get current book
		book, err := store.GetBookByID(update.BookID)
		if err != nil {
			mu.Lock()
			errs = append(errs, fmt.Errorf("update %d: failed to get book %s: %w", i, update.BookID, err))
			mu.Unlock()
			return nil
		}

		// Apply updates. book is the FULL hydrated row from GetBookByID (a
		// direct book:<id> point-get + unmarshal, not a memdb-slim projection),
		// so mutating and writing it back cannot wipe heavy fields.
		if title, ok := update.Updates["title"].(string); ok {
			book.Title = title
		}
		// Resolve author name → book.AuthorID (INIT-3-T4). Edge semantics:
		//   - empty/absent name → no change (skip; never clear an existing ID)
		//   - lookup MISS → CreateAuthor, then assign
		//   - store ERROR on lookup/create → FAIL-OPEN: log, leave AuthorID
		//     unset, and still persist the other applied fields (a store hiccup
		//     never aborts the whole apply; only UpdateBook stays fatal-to-item).
		// resolveMu makes the check-then-create atomic across workers.
		if name, ok := update.Updates["author"].(string); ok && name != "" {
			resolveMu.Lock()
			author, aerr := store.GetAuthorByName(name)
			if aerr == nil && author == nil {
				author, aerr = store.CreateAuthor(name)
			}
			resolveMu.Unlock()
			switch {
			case aerr != nil:
				slog.Warn("BatchUpdateMetadata: author resolution failed; leaving AuthorID unset (fail-open)",
					"book", update.BookID, "author", name, "error", aerr)
			case author != nil && (book.AuthorID == nil || *book.AuthorID != author.ID):
				oldID := book.AuthorID
				newID := author.ID
				book.AuthorID = &newID
				recordIDChange(store, update.BookID, "author_id", oldID, newID)
			}
		}
		// Resolve series name → book.SeriesID, scoped to the (possibly
		// just-resolved) author. Same edge semantics + fail-open posture.
		if name, ok := update.Updates["series"].(string); ok && name != "" {
			resolveMu.Lock()
			series, serr := store.GetSeriesByName(name, book.AuthorID)
			if serr == nil && series == nil {
				series, serr = store.CreateSeries(name, book.AuthorID)
			}
			resolveMu.Unlock()
			switch {
			case serr != nil:
				slog.Warn("BatchUpdateMetadata: series resolution failed; leaving SeriesID unset (fail-open)",
					"book", update.BookID, "series", name, "error", serr)
			case series != nil && (book.SeriesID == nil || *book.SeriesID != series.ID):
				oldID := book.SeriesID
				newID := series.ID
				book.SeriesID = &newID
				recordIDChange(store, update.BookID, "series_id", oldID, newID)
			}
		}
		if format, ok := update.Updates["format"].(string); ok {
			book.Format = format
		}

		// Update in database
		if _, err := store.UpdateBook(update.BookID, book); err != nil {
			mu.Lock()
			errs = append(errs, fmt.Errorf("update %d: failed to update book %s: %w", i, update.BookID, err))
			mu.Unlock()
			return nil
		}

		mu.Lock()
		successCount++
		mu.Unlock()
		return nil
	}, registry.RunItemsOptions{Concurrency: metadataBulkOpConcurrency})

	return errs, successCount
}

// WriteMetadataToFile safely writes metadata to an audiobook file
// Prefers native TagLib writer when built with 'taglib'; falls back to external CLI tools if unavailable or failed.
// Uses backup/rollback strategy via fileops.SafeCopy for all paths.
func WriteMetadataToFile(filePath string, metadata map[string]interface{}, config fileops.OperationConfig) error {
	ext := strings.ToLower(filepath.Ext(filePath))

	// Attempt native writer first if compiled in.
	// Upstream taglib v0.11.1+ writes custom freeform atoms natively for MP4.
	// Do NOT run ffmpeg after taglib — ffmpeg's -map_metadata strips freeform atoms.
	if taglibAvailable {
		if err := writeMetadataWithTaglib(filePath, metadata, config); err == nil {
			return nil
		}
		// Native failed; continue with CLI fallback
	}

	switch ext {
	case ".m4b", ".m4a":
		return writeM4BMetadata(filePath, metadata, config)
	case ".mp3":
		return writeMP3Metadata(filePath, metadata, config)
	case ".flac":
		return writeFLACMetadata(filePath, metadata, config)
	default:
		return fmt.Errorf("unsupported file format: %s", ext)
	}
}

// WriteM4BCustomTags writes custom tags to M4B/M4A files using ffmpeg.
// Exported so it can be called separately from the main write path.
func WriteM4BCustomTags(filePath string, metadata map[string]interface{}) error {
	return writeM4BCustomTagsWithFFmpeg(filePath, metadata)
}

// writeM4BCustomTagsWithFFmpeg writes custom/freeform tags to M4B files using ffmpeg.
// TagLib handles standard MP4 atoms but silently drops custom tags.
// ffmpeg can write arbitrary metadata including custom fields.
func writeM4BCustomTagsWithFFmpeg(filePath string, metadata map[string]interface{}) error {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		return fmt.Errorf("ffmpeg not found: %w", err)
	}

	// Only write tags that taglib can't handle for MP4
	customTags := map[string]string{}

	if narrator, ok := metadata["narrator"].(string); ok && narrator != "" {
		customTags["NARRATOR"] = narrator
	}
	if lang, ok := metadata["language"].(string); ok && lang != "" {
		customTags["LANGUAGE"] = strings.ToLower(lang)
	}
	if pub, ok := metadata["publisher"].(string); ok && pub != "" {
		customTags["PUBLISHER"] = pub
	}
	if isbn10, ok := metadata["isbn10"].(string); ok && isbn10 != "" {
		customTags["ISBN10"] = isbn10
	}
	if isbn13, ok := metadata["isbn13"].(string); ok && isbn13 != "" {
		customTags["ISBN13"] = isbn13
	}
	if series, ok := metadata["series"].(string); ok && series != "" {
		customTags["SERIES"] = series
	}
	if si, ok := metadata["series_index"].(int); ok && si > 0 {
		customTags["SERIES_INDEX"] = fmt.Sprintf("%d", si)
	}
	if asin, ok := metadata["asin"].(string); ok && asin != "" {
		customTags["ASIN"] = asin
	}
	customTags["AUDIOBOOK_ORGANIZER_VERSION"] = CustomTagVersion

	if len(customTags) == 0 {
		return nil
	}

	// Build ffmpeg command: copy all streams, add metadata
	// Use -nostdin and -loglevel error to suppress progress output
	// Use same extension so ffmpeg can detect the output format
	ext := filepath.Ext(filePath) // .m4b or .m4a
	tmpPath := filePath + ".tmp" + ext
	args := []string{"-nostdin", "-loglevel", "error", "-y", "-i", filePath}

	// Preserve audio streams, chapters, and existing metadata. Use -map 0:a
	// (not -map 0) because M4B files may have bin_data subtitle streams that
	// cause ffmpeg to fail.
	args = append(args, "-map", "0:a")
	args = append(args, "-map_chapters", "0")
	args = append(args, "-map_metadata", "0")
	args = append(args, "-c", "copy") // No re-encoding

	for k, v := range customTags {
		args = append(args, "-metadata", fmt.Sprintf("%s=%s", k, v))
	}

	args = append(args, tmpPath)

	cmd := exec.Command(ffmpegPath, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("ffmpeg failed: %w, stderr: %s", err, stderr.String()[:min(stderr.Len(), 500)])
	}

	// Atomic replace
	if err := os.Rename(tmpPath, filePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename failed: %w", err)
	}

	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// writeM4BMetadata writes metadata to M4B/M4A files using AtomicParsley.
// This is the CLI fallback path; the native taglib writer is preferred and
// handles all fields. AtomicParsley supports standard MP4 atoms and custom
// reverse-DNS atoms (--rDNSatom) for extended metadata.
func writeM4BMetadata(filePath string, metadata map[string]interface{}, config fileops.OperationConfig) error {
	// Check if AtomicParsley is available
	if _, err := exec.LookPath("AtomicParsley"); err != nil {
		return fmt.Errorf("AtomicParsley not found in PATH (install: brew install atomicparsley): %w", err)
	}

	slog.Warn("writeM4BMetadata using AtomicParsley CLI fallback for ; native taglib writer is preferred for full tag support", "filePath", filePath)

	// Create backup using safe copy with config
	backupPath := filePath + ".backup"
	if err := fileops.SafeCopy(filePath, backupPath, config); err != nil {
		return fmt.Errorf("backup failed: %w", err)
	}
	defer func() {
		// Clean up backup unless PreserveOriginal is set
		if !config.PreserveOriginal {
			_ = os.Remove(backupPath)
		}
	}()

	// Build AtomicParsley command with metadata updates
	args := []string{filePath, "--overWrite"}

	// --- Standard MP4 atoms ---
	if title, ok := metadata["title"].(string); ok && title != "" {
		args = append(args, "--title", title)
	}
	if artist, ok := metadata["artist"].(string); ok && artist != "" {
		args = append(args, "--artist", artist)
	}
	if album, ok := metadata["album"].(string); ok && album != "" {
		args = append(args, "--album", album)
	}
	if genre, ok := metadata["genre"].(string); ok && genre != "" {
		args = append(args, "--genre", genre)
	}
	if year, ok := metadata["year"].(int); ok && year > 0 {
		args = append(args, "--year", fmt.Sprintf("%d", year))
	}
	if track, ok := metadata["track"].(string); ok && track != "" {
		args = append(args, "--tracknum", track)
	}
	if desc, ok := metadata["description"].(string); ok && desc != "" {
		args = append(args, "--description", desc)
	}
	// Clear composer to prevent stale narrator data from polluting author on re-read.
	// Do NOT write narrator to --composer (©wrt) — use custom NARRATOR tag instead.
	args = append(args, "--composer", "")
	// --grouping maps to ©grp; use for series name
	if series, ok := metadata["series"].(string); ok && series != "" {
		args = append(args, "--grouping", series)
	}

	// --- Reverse-DNS atoms for fields without standard AtomicParsley flags ---
	// These are stored as ----:domain:name atoms and can be read back by taglib.
	const rdnsDomain = "audiobook-organizer"

	rdnsPairs := [][2]string{
		{"NARRATOR", "narrator"},
		{"LANGUAGE", "language"},
		{"PUBLISHER", "publisher"},
		{"SERIES", "series"},
		{"ISBN10", "isbn10"},
		{"ISBN13", "isbn13"},
	}
	for _, pair := range rdnsPairs {
		if val, ok := metadata[pair[1]].(string); ok && val != "" {
			args = append(args, "--rDNSatom", val, "name="+pair[0], "domain="+rdnsDomain)
		}
	}
	if si, ok := metadata["series_index"].(int); ok && si > 0 {
		args = append(args, "--rDNSatom", fmt.Sprintf("%d", si), "name=SERIES_INDEX", "domain="+rdnsDomain)
	}
	// Also try string form of series_index (some callers pass it as string)
	if si, ok := metadata["series_index"].(string); ok && si != "" {
		args = append(args, "--rDNSatom", si, "name=SERIES_INDEX", "domain="+rdnsDomain)
	}

	// --- Custom AUDIOBOOK_ORGANIZER_* reverse-DNS atoms ---
	customPairs := [][2]string{
		{TagBookID, "book_id"}, {TagISBN10, "isbn10"}, {TagISBN13, "isbn13"},
		{TagASIN, "asin"}, {TagOpenLibrary, "open_library_id"},
		{TagHardcover, "hardcover_id"}, {TagGoogleBooks, "google_books_id"},
		{TagEdition, "edition"}, {TagPrintYear, "print_year"},
	}
	for _, pair := range customPairs {
		if val, ok := metadata[pair[1]].(string); ok && val != "" {
			args = append(args, "--rDNSatom", val, "name="+pair[0], "domain="+rdnsDomain)
		}
	}
	args = append(args, "--rDNSatom", CustomTagVersion, "name="+TagVersion, "domain="+rdnsDomain)

	cmd := exec.Command("AtomicParsley", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Restore from backup on failure
		if restoreErr := fileops.SafeCopy(backupPath, filePath, config); restoreErr != nil {
			return fmt.Errorf("tag write failed and restore failed: write=%w, restore=%v, output=%s", err, restoreErr, output)
		}
		return fmt.Errorf("tag write failed (restored from backup): %w, output: %s", err, output)
	}
	return nil
}

// writeMP3Metadata writes metadata to MP3 files using eyeD3
func writeMP3Metadata(filePath string, metadata map[string]interface{}, config fileops.OperationConfig) error {
	// Check if eyeD3 is available
	if _, err := exec.LookPath("eyeD3"); err != nil {
		return fmt.Errorf("eyeD3 not found in PATH (install: pip install eyeD3): %w", err)
	}

	// Create backup
	backupPath := filePath + ".backup"
	if err := fileops.SafeCopy(filePath, backupPath, config); err != nil {
		return fmt.Errorf("backup failed: %w", err)
	}
	defer func() {
		if !config.PreserveOriginal {
			_ = os.Remove(backupPath)
		}
	}()

	// Build eyeD3 command
	args := []string{}
	if title, ok := metadata["title"].(string); ok && title != "" {
		args = append(args, "--title", title)
	}
	if artist, ok := metadata["artist"].(string); ok && artist != "" {
		args = append(args, "--artist", artist)
	}
	if album, ok := metadata["album"].(string); ok && album != "" {
		args = append(args, "--album", album)
	}
	if narrator, ok := metadata["narrator"].(string); ok && narrator != "" {
		// Store narrator in a custom TXXX frame
		args = append(args, "--user-text-frame=NARRATOR:"+narrator)
	}
	if genre, ok := metadata["genre"].(string); ok && genre != "" {
		args = append(args, "--genre", genre)
	}
	if year, ok := metadata["year"].(int); ok && year > 0 {
		args = append(args, "-Y", fmt.Sprintf("%d", year))
	}
	if track, ok := metadata["track"].(string); ok && track != "" {
		args = append(args, "-n", track)
	}
	// Write custom AUDIOBOOK_ORGANIZER_* TXXX frames
	customPairs := [][2]string{
		{TagBookID, "book_id"}, {TagISBN10, "isbn10"}, {TagISBN13, "isbn13"},
		{TagASIN, "asin"}, {TagOpenLibrary, "open_library_id"},
		{TagHardcover, "hardcover_id"}, {TagGoogleBooks, "google_books_id"},
		{TagEdition, "edition"}, {TagPrintYear, "print_year"},
	}
	for _, pair := range customPairs {
		if val, ok := metadata[pair[1]].(string); ok && val != "" {
			args = append(args, "--user-text-frame="+pair[0]+":"+val)
		}
	}
	args = append(args, "--user-text-frame="+TagVersion+":"+CustomTagVersion)
	args = append(args, filePath)

	cmd := exec.Command("eyeD3", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Restore from backup on failure
		if restoreErr := fileops.SafeCopy(backupPath, filePath, config); restoreErr != nil {
			return fmt.Errorf("tag write failed and restore failed: write=%w, restore=%v, output=%s", err, restoreErr, output)
		}
		return fmt.Errorf("tag write failed (restored from backup): %w, output: %s", err, output)
	}
	return nil
}

// writeFLACMetadata writes metadata to FLAC files using metaflac
func writeFLACMetadata(filePath string, metadata map[string]interface{}, config fileops.OperationConfig) error {
	// Check if metaflac is available
	if _, err := exec.LookPath("metaflac"); err != nil {
		return fmt.Errorf("metaflac not found in PATH (install: brew install flac): %w", err)
	}

	// Create backup
	backupPath := filePath + ".backup"
	if err := fileops.SafeCopy(filePath, backupPath, config); err != nil {
		return fmt.Errorf("backup failed: %w", err)
	}
	defer func() {
		if !config.PreserveOriginal {
			_ = os.Remove(backupPath)
		}
	}()

	// Build metaflac command (remove old tags first, then set new)
	removeArgs := []string{"--remove-tag=TITLE", "--remove-tag=ARTIST", "--remove-tag=ALBUM", "--remove-tag=GENRE", "--remove-tag=DATE", "--remove-tag=NARRATOR", filePath}
	if err := exec.Command("metaflac", removeArgs...).Run(); err != nil {
		// Non-fatal if tags don't exist
	}

	// Set new tags
	var args []string
	if title, ok := metadata["title"].(string); ok && title != "" {
		args = append(args, "--set-tag=TITLE="+title)
	}
	if artist, ok := metadata["artist"].(string); ok && artist != "" {
		args = append(args, "--set-tag=ARTIST="+artist)
	}
	if album, ok := metadata["album"].(string); ok && album != "" {
		args = append(args, "--set-tag=ALBUM="+album)
	}
	if narrator, ok := metadata["narrator"].(string); ok && narrator != "" {
		args = append(args, "--set-tag=NARRATOR="+narrator)
	}
	if genre, ok := metadata["genre"].(string); ok && genre != "" {
		args = append(args, "--set-tag=GENRE="+genre)
	}
	if year, ok := metadata["year"].(int); ok && year > 0 {
		args = append(args, fmt.Sprintf("--set-tag=DATE=%d", year))
	}
	if track, ok := metadata["track"].(string); ok && track != "" {
		args = append(args, "--set-tag=TRACKNUMBER="+track)
	}
	// Write custom AUDIOBOOK_ORGANIZER_* Vorbis comments
	customPairs := [][2]string{
		{TagBookID, "book_id"}, {TagISBN10, "isbn10"}, {TagISBN13, "isbn13"},
		{TagASIN, "asin"}, {TagOpenLibrary, "open_library_id"},
		{TagHardcover, "hardcover_id"}, {TagGoogleBooks, "google_books_id"},
		{TagEdition, "edition"}, {TagPrintYear, "print_year"},
	}
	for _, pair := range customPairs {
		if val, ok := metadata[pair[1]].(string); ok && val != "" {
			args = append(args, "--set-tag="+pair[0]+"="+val)
		}
	}
	args = append(args, "--set-tag="+TagVersion+"="+CustomTagVersion)
	args = append(args, filePath)

	cmd := exec.Command("metaflac", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Restore from backup on failure
		if restoreErr := fileops.SafeCopy(backupPath, filePath, config); restoreErr != nil {
			return fmt.Errorf("tag write failed and restore failed: write=%w, restore=%v, output=%s", err, restoreErr, output)
		}
		return fmt.Errorf("tag write failed (restored from backup): %w, output: %s", err, output)
	}
	return nil
}

// recordIDChange records a successful author/series ID resolution as a
// MetadataChangeRecord (old → new) through the shipped metadata-history store,
// so a mis-resolution (name collision → wrong existing author) is auditable and
// reversible. Fail-open: a recording error is logged, never fatal. Previous/new
// values are JSON-encoded to match the existing history writers in
// internal/metafetch (nil previous → "null").
//
// The legacy enhanced.go history stub (MetadataHistory / RecordMetadataChange /
// GetMetadataHistory) that used to live here was dead code shadowing this
// already-shipped subsystem (database.MetadataChangeRecord + PebbleStore impl +
// live /metadata-history routes + MetadataHistory.tsx) and has been retired
// (INIT-3-T4, spec Decision 6 — no new history storage).
func recordIDChange(store batchUpdateStore, bookID, field string, oldID *int, newID int) {
	prev := jsonEncodeIDPtr(oldID)
	next := jsonEncodeIDPtr(&newID)
	rec := &database.MetadataChangeRecord{
		BookID:        bookID,
		Field:         field,
		PreviousValue: &prev,
		NewValue:      &next,
		ChangeType:    "bulk_update",
		Source:        "manual",
		ChangedAt:     time.Now(),
	}
	if err := store.RecordMetadataChange(rec); err != nil {
		slog.Warn("BatchUpdateMetadata: failed to record ID change history (non-fatal)",
			"book", bookID, "field", field, "error", err)
	}
}

// jsonEncodeIDPtr JSON-encodes a nullable int ID (nil → "null") to match the
// JSON-encoded PreviousValue/NewValue convention of the metadata-history store.
func jsonEncodeIDPtr(id *int) string {
	b, err := json.Marshal(id)
	if err != nil {
		return "null"
	}
	return string(b)
}

// ExportMetadata exports book metadata to a structured format
func ExportMetadata(books []database.BookCore) (map[string]interface{}, error) {
	result := make(map[string]interface{})

	bookData := make([]map[string]interface{}, 0, len(books))
	for _, book := range books {
		bookData = append(bookData, map[string]interface{}{
			"id":              book.ID,
			"title":           book.Title,
			"author_id":       book.AuthorID,
			"series_id":       book.SeriesID,
			"series_sequence": book.SeriesSequence,
			"format":          book.Format,
			"file_path":       book.FilePath,
			"duration":        book.Duration,
		})
	}

	result["books"] = bookData
	result["count"] = len(books)
	result["exported_at"] = time.Now().Format(time.RFC3339)

	return result, nil
}

// ImportMetadata imports book metadata from a structured format.
//
// Parallelized via registry.RunItems (CONC-13): each worker processes one
// book entry (decode → validate → CreateBook) independently. The only state
// shared across workers is the result aggregation (errs, importCount),
// guarded by mu so the parallel pass produces the same result set as the
// original serial loop (order-independent — item index i is preserved in
// error messages for identification).
//
// Store-safety: store.CreateBook (PebbleStore) generates its own ULID with a
// call-local monotonic entropy source (no shared/global counter), commits via
// a Pebble batch (safe for concurrent writers), and write-throughs to memdb
// via memSync, which serializes on hashicorp/go-memdb's Txn(true) exclusive
// writer lock — concurrent CreateBook calls cannot race or corrupt state at
// this concurrency.
func ImportMetadata(data map[string]interface{}, store database.BookStore, validate bool) (int, []error) {
	booksData, ok := data["books"].([]interface{})
	if !ok {
		return 0, []error{fmt.Errorf("invalid data format: books field missing or invalid")}
	}

	var mu sync.Mutex
	var errs []error
	importCount := 0

	// RunItems's fn signature doesn't carry the item's original index, and
	// the error messages below identify books by their request-payload
	// index — so iterate over indices into booksData rather than the
	// entries themselves.
	indices := make([]int, len(booksData))
	for i := range indices {
		indices[i] = i
	}

	_ = registry.RunItems(context.Background(), inertMetadataReporter{}, indices, func(_ context.Context, i int) error {
		bookData, ok := booksData[i].(map[string]interface{})
		if !ok {
			mu.Lock()
			errs = append(errs, fmt.Errorf("book %d: invalid book data format", i))
			mu.Unlock()
			return nil
		}

		// Validate if requested
		if validate {
			validationErrors := ValidateMetadata(bookData, DefaultValidationRules())
			if len(validationErrors) > 0 {
				mu.Lock()
				errs = append(errs, fmt.Errorf("book %d: validation failed: %v", i, validationErrors))
				mu.Unlock()
				return nil
			}
		}

		// Create book object
		duration := getIntField(bookData, "duration")
		book := &database.Book{
			Title:          getStringField(bookData, "title"),
			Format:         getStringField(bookData, "format"),
			FilePath:       getStringField(bookData, "file_path"),
			Duration:       &duration,
			AuthorID:       getIntPtrField(bookData, "author_id"),
			SeriesID:       getIntPtrField(bookData, "series_id"),
			SeriesSequence: getIntPtrField(bookData, "series_sequence"),
		}

		// Create or update book
		if _, err := store.CreateBook(book); err != nil {
			mu.Lock()
			errs = append(errs, fmt.Errorf("book %d: failed to import: %w", i, err))
			mu.Unlock()
			return nil
		}

		mu.Lock()
		importCount++
		mu.Unlock()
		return nil
	}, registry.RunItemsOptions{Concurrency: metadataBulkOpConcurrency})

	return importCount, errs
}

// Helper functions for type-safe field extraction
func getStringField(data map[string]interface{}, field string) string {
	if val, ok := data[field].(string); ok {
		return val
	}
	return ""
}

func getIntField(data map[string]interface{}, field string) int {
	if val, ok := data[field].(float64); ok {
		return int(val)
	}
	if val, ok := data[field].(int); ok {
		return val
	}
	return 0
}

func getIntPtrField(data map[string]interface{}, field string) *int {
	if val, ok := data[field].(float64); ok {
		intVal := int(val)
		return &intVal
	}
	if val, ok := data[field].(int); ok {
		return &val
	}
	return nil
}
