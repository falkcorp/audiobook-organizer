// file: internal/metafetch/service_writeback.go
// version: 1.7.0
// guid: fad73c11-30c2-4fdc-addd-45afef25d792
// last-edited: 2026-08-21

package metafetch

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

// sortedKeys returns a tag map's keys in a stable order so a failure log names
// which tags were being written. Without it a write-back failure told you only
// that "a write failed" — not whether it was trying to set one field or twenty,
// which is the difference between a benign retry and a book about to be
// rewritten wholesale.
func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// writeBackMetadata writes enriched metadata back to a book's audio file(s)
// during the fetch/apply flow.
//
// This used to be a ~160-line near-duplicate of WriteBackMetadataForBook whose
// only distinct input was three fallback values from the just-fetched metadata.
// The two copies drifted, and the duplicate was the worse of the two: it never
// embedded covers from an already-downloaded local cover, never propagated to
// version-group siblings, never redirected protected paths to the library copy,
// and never stamped LastWrittenAt/MarkNeedsRescan in its multi-file branch (so
// those books stayed invisible to any "written since it changed?" skip).
//
// It is now a thin wrapper over the single shared implementation, and the
// fetch/apply path gains all of the above.
func (mfs *Service) writeBackMetadata(book *database.Book, meta metadata.BookMetadata) {
	if book == nil {
		return
	}
	if _, err := mfs.writeBackForBook(book.ID, &writeBackOverrides{
		Title:       meta.Title,
		Author:      meta.Author,
		PublishYear: meta.PublishYear,
	}, nil); err != nil {
		slog.Warn("write-back after fetch failed", "id", book.ID, "error", err)
	}
}

// metadataSourceTag turns a human-readable source name from
// metadata.MetadataSource.Name() into a tag-safe slug under the
// metadata:source:* namespace. Returns "" for empty inputs so
// the caller can skip the tag write.
//
//	"Hardcover"          → "metadata:source:hardcover"
//	"Open Library"       → "metadata:source:open_library"
//	"Google Books"       → "metadata:source:google_books"
//	"Audnexus (Audible)" → "metadata:source:audnexus"
//	"Audible"            → "metadata:source:audible"
func MetadataSourceTag(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	// Special case: drop the "(Audible)" parenthetical on Audnexus
	// so the tag cleanly identifies the source provider, not its
	// upstream. We still have metadata:source:audible for the
	// direct Audible path.
	if strings.HasPrefix(name, "Audnexus") {
		return "metadata:source:audnexus"
	}
	slug := strings.ToLower(name)
	slug = strings.ReplaceAll(slug, " ", "_")
	slug = strings.ReplaceAll(slug, "(", "")
	slug = strings.ReplaceAll(slug, ")", "")
	slug = strings.ReplaceAll(slug, "-", "_")
	return "metadata:source:" + slug
}

// metadataLanguageTag turns a language string from a metadata
// source into a tag under the metadata:language:* namespace.
// Accepts ISO 639-1 codes ("en"), ISO 639-2 codes ("eng"), and
// full English names ("English"); normalizes to the 2-letter
// form where recognized and lowercases everything else. Returns
// "" for empty inputs so the caller can skip the tag write.
func MetadataLanguageTag(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "" {
		return ""
	}
	// Short list of ISO 639-2 / English-name variants we see
	// across the real sources. Unknown languages fall through
	// to the lowercased input so we never drop data — worst
	// case the tag looks weird but it's still filterable.
	canonical := map[string]string{
		"english":    "en",
		"eng":        "en",
		"spanish":    "es",
		"spa":        "es",
		"french":     "fr",
		"fre":        "fr",
		"fra":        "fr",
		"german":     "de",
		"ger":        "de",
		"deu":        "de",
		"italian":    "it",
		"ita":        "it",
		"japanese":   "ja",
		"jpn":        "ja",
		"chinese":    "zh",
		"chi":        "zh",
		"zho":        "zh",
		"mandarin":   "zh",
		"portuguese": "pt",
		"por":        "pt",
		"russian":    "ru",
		"rus":        "ru",
		"dutch":      "nl",
		"nld":        "nl",
		"korean":     "ko",
		"kor":        "ko",
		"arabic":     "ar",
		"ara":        "ar",
	}
	if code, ok := canonical[lang]; ok {
		return "metadata:language:" + code
	}
	// Already a 2-letter code? Keep it.
	if len(lang) == 2 {
		return "metadata:language:" + lang
	}
	// Unknown — slugify and pass through.
	slug := strings.ReplaceAll(lang, " ", "_")
	return "metadata:language:" + slug
}

// buildTagMap constructs the tag map shared by all write-back paths.
// Includes all available metadata fields — standard and custom tags.
func (mfs *Service) BuildTagMap(
	albumTitle, trackTitle, artist, narrator string, year int, track string,
) map[string]interface{} {
	tagMap := make(map[string]interface{})
	tagMap["title"] = trackTitle
	tagMap["album"] = albumTitle
	if artist != "" {
		tagMap["artist"] = artist
	}
	if narrator != "" {
		tagMap["narrator"] = narrator
	}
	if year > 0 {
		tagMap["year"] = year
	}
	tagMap["genre"] = "Audiobook"
	if track != "" {
		tagMap["track"] = track
	}
	return tagMap
}

// generatedChapterTitleRe matches the synthetic per-file title this package
// produces for multi-file books: a zero-padded track number, " - ", then the
// book title (e.g. "01 - The Long Way Home").
var generatedChapterTitleRe = regexp.MustCompile(`^\d+ - (.+)$`)

// chapterTitleFor decides what to write into the "title" tag of one file of a
// multi-file book. It returns the title to write, or "" meaning "leave the
// file's existing title alone".
//
// The synthetic "NN - Book Title" form is only a fallback. Real per-chapter
// titles ("Chapter 1: The Long Way Home", "Prologue", "Epilogue") are genuine
// metadata worth keeping, and the write-back paths used to overwrite them
// unconditionally on every run — so a library's chapter titles were flattened to
// "01 - Book Title", "02 - Book Title", ... and the original text was gone.
//
// A title is treated as ours-to-replace when it is empty, when it is exactly the
// book title (a bulk tag-set left every file identical, which carries no
// per-chapter information), or when it already matches the synthetic pattern for
// this book (so renumbering still works).
func chapterTitleFor(currentTitle, generated, bookTitle string) string {
	cur := strings.TrimSpace(currentTitle)
	switch {
	case cur == "":
		return generated
	case cur == bookTitle:
		// Every file carrying the bare book title tells us nothing about which
		// chapter it is; numbering them is strictly more informative.
		return generated
	case cur == generated:
		// Already correct. Returning it lets the unchanged-tag filter drop it.
		return generated
	}
	// "NN - <book title>" is our own output from a previous run; refresh it so a
	// changed track count or book title still propagates. A different suffix means
	// a human/publisher chapter title, which we keep.
	if m := generatedChapterTitleRe.FindStringSubmatch(cur); m != nil && m[1] == bookTitle {
		return generated
	}
	return ""
}

// buildFullTagMap constructs a tag map with ALL available metadata from the book record,
// including custom tags for fields that don't have standard audio tag equivalents.
func (mfs *Service) BuildFullTagMap(
	book *database.Book, albumTitle, trackTitle, artist, narrator string, year int, track string,
) map[string]interface{} {
	tagMap := mfs.BuildTagMap(albumTitle, trackTitle, artist, narrator, year, track)

	// Add fields that have standard or custom tag equivalents
	if book.Language != nil && *book.Language != "" {
		tagMap["language"] = *book.Language
	}
	if book.Publisher != nil && *book.Publisher != "" {
		tagMap["publisher"] = *book.Publisher
	}
	if book.Description != nil && *book.Description != "" {
		tagMap["description"] = *book.Description
	}
	if book.ISBN10 != nil && *book.ISBN10 != "" {
		tagMap["isbn10"] = *book.ISBN10
	}
	if book.ISBN13 != nil && *book.ISBN13 != "" {
		tagMap["isbn13"] = *book.ISBN13
	}
	if book.ASIN != nil && *book.ASIN != "" {
		tagMap["asin"] = *book.ASIN
	}

	// Series info as custom tags
	if book.SeriesID != nil {
		if series, err := mfs.db.GetSeriesByID(*book.SeriesID); err == nil && series != nil {
			tagMap["series"] = series.Name
		}
	}
	if book.SeriesSequence != nil {
		tagMap["series_index"] = *book.SeriesSequence
	}

	// External provider IDs (written as AUDIOBOOK_ORGANIZER_* custom tags)
	tagMap["book_id"] = book.ID
	if book.OpenLibraryID != nil && *book.OpenLibraryID != "" {
		tagMap["open_library_id"] = *book.OpenLibraryID
	}
	if book.HardcoverID != nil && *book.HardcoverID != "" {
		tagMap["hardcover_id"] = *book.HardcoverID
	}
	if book.GoogleBooksID != nil && *book.GoogleBooksID != "" {
		tagMap["google_books_id"] = *book.GoogleBooksID
	}

	// Edition and print year
	if book.Edition != nil && *book.Edition != "" {
		tagMap["edition"] = *book.Edition
	}
	if book.PrintYear != nil && *book.PrintYear > 0 {
		tagMap["print_year"] = fmt.Sprintf("%d", *book.PrintYear)
	}

	return tagMap
}

// filterUnchangedTags reads the current tags from filePath and removes any
// entries from tagMap whose values already match, so only changed fields are
// written back to the file.
//
// WHY: Custom AUDIOBOOK_ORGANIZER_* tags must be compared via the
// metadata.Metadata struct fields (BookOrganizerID, Edition, PrintYear, etc.)
// so that unchanged custom tags don't force a full write-back and inflate
// copy-on-write (.bak-*) churn. Each tag name in the tagMap (e.g. "book_id")
// maps 1:1 to a Metadata field and is enumerated below per metadata/custom_tags.go.
func FilterUnchangedTags(filePath string, tagMap map[string]interface{}) map[string]interface{} {
	current, err := metadata.ExtractMetadata(filePath, nil)
	if err != nil {
		// Can't read current tags — write everything to be safe
		return tagMap
	}
	return filterTagsAgainst(filePath, current, tagMap)
}

// filterTagsAgainst is the pure comparison half of FilterUnchangedTags, split
// out so the mapping can be unit-tested against a constructed Metadata without
// synthesizing a real audio file on disk.
//
// This split exists because every pre-existing FilterUnchangedTags test pointed
// at a nonexistent path: ExtractMetadata failed, the function returned tagMap
// untouched, and the comparison below never ran. Those tests passed no matter
// what the mapping did — one of them even asserted that "track" survives,
// pinning the bug in place as if it were intended behavior.
func filterTagsAgainst(filePath string, current metadata.Metadata, tagMap map[string]interface{}) map[string]interface{} {
	// Build a map of known tag names to their current values. Every custom tag
	// key emitted by the writer (via metadata/taglib_tagmap.go buildWriteTagMap)
	// must have an entry here, mapping the input key to the corresponding
	// Metadata field value.
	// "track" is emitted unconditionally by the multi-file write path
	// (BuildTagMap adds it whenever track != "", and the multi-file branch always
	// passes "n/total"). Until this entry existed it fell through to the
	// unknown-key branch below and was ALWAYS written, which made
	// len(tagMap) == 0 unreachable for every multi-file book — so each one
	// rewrote every one of its files on every single write-back run, forever.
	//
	// Render the on-disk value in the same "n/total" shape the writer produces.
	// A file tagged with a bare "3" (TrackTotal == 0) renders as "3", won't match
	// "3/12", and is written once — after which it carries the pair and matches.
	// TrackNumber == 0 means we could not read a track at all: leave the key out
	// so the unknown-key branch writes it, which is the correct conservative call.
	trackCur := ""
	if current.TrackNumber > 0 {
		if current.TrackTotal > 0 {
			trackCur = fmt.Sprintf("%d/%d", current.TrackNumber, current.TrackTotal)
		} else {
			trackCur = fmt.Sprintf("%d", current.TrackNumber)
		}
	}

	currentVals := map[string]string{
		"title":  current.Title,
		"album":  current.Album,
		"artist": current.Artist,
		// album_artist and composer both hold the narrator in our
		// audiobook tag convention (album_artist > artist > composer
		// is the read priority). RenameService writes them as two
		// separate keys, so filterUnchangedTags needs to know they
		// compare against current.Narrator too — otherwise every
		// organize pass sees album_artist/composer as "unknown
		// field → always write" and falls through to a real write,
		// which was the root cause of the "organize rewrites tags
		// every time even when unchanged" investigation.
		"album_artist": current.Narrator,
		"composer":     current.Narrator,
		"narrator":     current.Narrator,
		"genre":        current.Genre,
		"year":         fmt.Sprintf("%d", current.Year),
		"language":     current.Language,
		"series":       current.Series,
		"asin":         current.ASIN,
		"description":  current.Comments, // description is stored in comments field
		// Custom AUDIOBOOK_ORGANIZER_* tag mappings from metadata/custom_tags.go:
		// These map input keys (e.g. "book_id") to Metadata struct fields.
		"book_id":         current.BookOrganizerID,
		"open_library_id": current.OpenLibraryID,
		"hardcover_id":    current.HardcoverID,
		"google_books_id": current.GoogleBooksID,
		"edition":         current.Edition,
		"print_year":      current.PrintYear,
	}
	if trackCur != "" {
		currentVals["track"] = trackCur
	}
	if current.Publisher != "" {
		currentVals["publisher"] = current.Publisher
	}
	if current.SeriesIndex > 0 {
		currentVals["series_index"] = fmt.Sprintf("%d", int(current.SeriesIndex))
	}
	if current.ISBN10 != "" {
		currentVals["isbn10"] = current.ISBN10
	}
	if current.ISBN13 != "" {
		currentVals["isbn13"] = current.ISBN13
	}

	filtered := make(map[string]interface{}, len(tagMap))
	for k, v := range tagMap {
		cur, ok := currentVals[k]
		if !ok {
			// Unknown field — always write.
			// Log unknown keys so new custom tags are added consciously to
			// the mapping above rather than silently forcing writes.
			// ("track" used to be the example here; it is mapped above now.
			// This warn is only a real signal once every emitted key is mapped,
			// so do not add new writer keys without a currentVals entry.)
			// Name the FILE, not just the key. This warn used to carry only the
			// key, so it told you a tag was being force-written every run and
			// gave you no way to find which file or book it was on.
			slog.Warn("write-back: unmapped tag key, forcing a write",
				"key", k,
				"value", v,
				"path", filePath,
				"title", current.Title,
				"album", current.Album,
				"hint", "add this key to currentVals in filterTagsAgainst or it rewrites the file on every run")
			filtered[k] = v
			continue
		}
		newStr := fmt.Sprintf("%v", v)
		if newStr != cur {
			filtered[k] = v
		}
	}

	if len(filtered) == 0 {
		return filtered
	}
	return filtered
}

// generateSegmentTitles computes and persists file titles for all book files of a book.
func (mfs *Service) generateSegmentTitles(bookID string, bookTitle string) error {
	bookFiles, err := mfs.db.GetBookFiles(bookID)
	if err != nil {
		return fmt.Errorf("list book files: %w", err)
	}
	bookFiles = dedupeBookFilesByPath(bookID, bookFiles)
	if len(bookFiles) == 0 {
		return nil
	}

	// Sort by track number (0 last), then filepath
	sort.Slice(bookFiles, func(i, j int) bool {
		ti := bookFiles[i].TrackNumber
		tj := bookFiles[j].TrackNumber
		if ti != 0 && tj != 0 {
			if ti != tj {
				return ti < tj
			}
		} else if ti != 0 {
			return true
		} else if tj != 0 {
			return false
		}
		return bookFiles[i].FilePath < bookFiles[j].FilePath
	})

	totalTracks := len(bookFiles)

	// The per-file DISPLAY title, not a path. This is the one thing the old
	// segment_title_format config key still fed, so the key is gone but the
	// format survives as a constant — a book_file.Title is a database field and
	// has nothing to do with where the file lives on disk.
	for i := range bookFiles {
		// Auto-assign track numbers if zero
		if bookFiles[i].TrackNumber == 0 {
			bookFiles[i].TrackNumber = i + 1
		}
		bookFiles[i].TrackCount = totalTracks

		// Compute file title
		title := organizer.FormatSegmentTitle(organizer.DefaultSegmentTitleFormat, bookTitle, bookFiles[i].TrackNumber, totalTracks)
		bookFiles[i].Title = title

		if err := mfs.db.UpdateBookFile(bookFiles[i].ID, &bookFiles[i]); err != nil {
			slog.Warn("failed to update book file title for", "id", bookFiles[i].ID, "error", err)
		}
	}

	return nil
}

// runApplyPipeline runs the file rename pipeline after metadata is applied.
// For protected books (iTunes/import paths), it operates on the library copy
// instead of the original to avoid moving source files.
func (mfs *Service) runApplyPipeline(id string, book *database.Book) error {
	// If the book is in a protected path, run the pipeline on the library copy instead
	if mfs.isProtectedPath(book.FilePath) {
		libCopy := mfs.ensureLibraryCopy(book)
		if libCopy == nil {
			slog.Warn("runApplyPipeline skipping protected book: no library copy exists",
				"book_id", id, "book_title", book.Title,
				"protected_path", book.FilePath,
				"hint", "book lives under a protected path (iTunes/import); rename and tag-write are skipped until a library copy is made")
			return nil
		}
		slog.Info("runApplyPipeline using library copy for protected book", "libCopyID", libCopy.ID, "bookID", id)
		id = libCopy.ID
		book = libCopy
	}

	bookFiles, err := mfs.db.GetBookFiles(id)
	if err != nil {
		return fmt.Errorf("list book files: %w", err)
	}
	bookFiles = dedupeBookFilesByPath(id, bookFiles)
	if len(bookFiles) == 0 {
		return nil
	}

	// Plan the rename through the Organizer — same store lookups for
	// author/series, same "Unknown Author" fallback, same naming patterns as the
	// organize path. The hand-rolled FormatVars + path_format block that used to
	// live here computed a DIFFERENT target, which is what made organize and
	// apply drag books back and forth between two answers.
	entries, err := newPathOrganizer(mfs.db).ComputeTargetPaths(book, bookFiles)
	if err != nil {
		// A broken naming pattern must not fall through to a rename.
		return fmt.Errorf("compute target paths for book %s: %w", id, err)
	}

	if config.AppConfig.AutoRenameOnApply && !hasCheckpoint(mfs.db, id, phaseRename) {
		renameResult, renameErr := RenameFiles(entries)
		// Even when RenameFiles returns an error, entries in
		// renameResult.Succeeded have physically moved on disk — their DB
		// paths MUST still be updated below, or the library loses track of
		// files that did move. The error is returned after the DB sync,
		// before the rename checkpoint is set.
		if len(renameResult.Skipped) > 0 {
			// Name the files. This used to log only the count, which told you
			// something was wrong and nothing about what — you could not find the
			// offending file without going and diffing the library by hand.
			//
			// Capped so one pathological book cannot flood the log; the count is
			// always reported in full so a truncated list is never mistaken for
			// the whole set.
			const maxListed = 20
			skippedPaths := make([]string, 0, min(len(renameResult.Skipped), maxListed))
			for i, e := range renameResult.Skipped {
				if i >= maxListed {
					break
				}
				skippedPaths = append(skippedPaths, e.SourcePath)
			}
			slog.Warn("files skipped during rename (source missing on disk)",
				"book_id", id,
				"book_title", book.Title,
				"skipped_count", len(renameResult.Skipped),
				"total_files", len(bookFiles),
				"listed", len(skippedPaths),
				"truncated", len(renameResult.Skipped) > maxListed,
				"missing_sources", skippedPaths)
		}

		// Update book file records with new paths (only for succeeded renames)
		bfMap := make(map[string]*database.BookFile, len(bookFiles))
		for i := range bookFiles {
			bfMap[bookFiles[i].ID] = &bookFiles[i]
		}
		for _, entry := range renameResult.Succeeded {
			if bf, ok := bfMap[entry.SegmentID]; ok {
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

		// Update the book's file_path to match the new segment directory.
		// For multi-file books, file_path is the parent directory of the segments.
		if len(renameResult.Succeeded) > 0 {
			newBookPath := filepath.Dir(renameResult.Succeeded[0].TargetPath)
			if newBookPath != book.FilePath {
				book.FilePath = newBookPath
				if _, err := mfs.db.UpdateBook(id, book); err != nil {
					slog.Warn("failed to update book path for", "id", id, "error", err)
				} else {
					slog.Info("updated book path for", "id", id, "path", newBookPath)
				}
			}
		}

		// DB paths for every succeeded rename are persisted; now surface the
		// rename failure. The checkpoint is NOT set, so the next apply run
		// retries the rename (including any stranded-temp resume).
		if renameErr != nil {
			return fmt.Errorf("rename files: %w", renameErr)
		}
		setCheckpoint(mfs.db, id, phaseRename)
	}

	// Always ensure itunes_path is set on each BookFile if a mapping exists.
	for i := range bookFiles {
		if bookFiles[i].ITunesPath == "" {
			if itunesPath := ComputeITunesPath(bookFiles[i].FilePath); itunesPath != "" {
				bookFiles[i].ITunesPath = itunesPath
				if err := mfs.db.UpdateBookFile(bookFiles[i].ID, &bookFiles[i]); err != nil {
					slog.Warn("failed to update itunes_path for book file", "id", bookFiles[i].ID, "error", err)
				}
			}
		}
	}

	// Write metadata tags to audio files
	if config.AppConfig.AutoWriteTagsOnApply && !hasCheckpoint(mfs.db, id, phaseTags) {
		if written, err := mfs.WriteBackMetadataForBook(id); err != nil {
			slog.Warn("tag writing failed for book",
				"book_id", id, "book_title", book.Title,
				"book_path", book.FilePath, "error", err)
		} else {
			slog.Info("wrote metadata tags to file(s) for book", "value", written, "id", id)
			setCheckpoint(mfs.db, id, phaseTags)
		}
	}

	// Enqueue iTunes writeback so the batcher picks up both location
	// (if the file was renamed) and metadata changes. The apply
	// handler also enqueues after this returns; the batcher dedupes
	// on book ID so the duplicate is harmless.
	if mfs.writeBackBatcher != nil && !hasCheckpoint(mfs.db, id, phaseITunes) {
		mfs.writeBackBatcher.Enqueue(id)
		setCheckpoint(mfs.db, id, phaseITunes)
	}

	// All phases complete — clear checkpoints.
	clearCheckpoints(mfs.db, id)
	return nil
}

// writeBackOverrides carries values from freshly-fetched metadata that may not
// be persisted on the book record yet. They are only consulted as fallbacks when
// the book's own fields are empty. nil means "use the DB record alone".
//
// This exists so the fetch/apply path can share ONE write-back implementation
// instead of keeping its own copy. It previously had a near-identical ~160-line
// duplicate whose only distinct input was these three values, and the two copies
// had drifted: the duplicate never embedded covers from an already-downloaded
// local cover, never propagated to version-group siblings, never redirected
// protected paths to the library copy, and never stamped LastWrittenAt in its
// multi-file branch.
type writeBackOverrides struct {
	Title       string
	Author      string
	PublishYear int
}

// WriteBackMetadataForBook reads current DB metadata for the book, resolves authors and
// narrators, writes comprehensive tags to all active audio file segments, and records a
// history entry. It is called by POST /api/v1/audiobooks/:id/write-back.
func (mfs *Service) WriteBackMetadataForBook(id string, segmentFilter ...[]string) (int, error) {
	var sf []string
	if len(segmentFilter) > 0 {
		sf = segmentFilter[0]
	}
	return mfs.writeBackForBook(id, nil, sf)
}

// writeBackForBook is the single implementation behind both WriteBackMetadataForBook
// and the fetch/apply path.
func (mfs *Service) writeBackForBook(id string, ov *writeBackOverrides, segmentFilter []string) (int, error) {
	book, err := mfs.db.GetBookByID(id)
	if err != nil || book == nil {
		return 0, fmt.Errorf("audiobook not found: %s", id)
	}

	// If book is in a protected path, write to the library copy instead.
	// Keep a reference to the original book so we can use its (freshly-updated)
	// metadata for building the tag map, rather than the library copy's stale data.
	originalBook := book
	originalID := id
	if mfs.isProtectedPath(book.FilePath) {
		libCopy := mfs.ensureLibraryCopy(book)
		if libCopy == nil {
			return 0, fmt.Errorf("cannot write back: no library copy for protected book %s", id)
		}
		// Sync metadata from the original book to the library copy so both
		// DB records stay in sync and the tag map uses current data.
		mfs.syncMetadataToLibraryCopy(originalBook, libCopy)
		book = libCopy
	}

	// --- Resolve author names ---
	// Use the original book's ID for author/narrator lookup since that's where
	// ApplyMetadataCandidate stores the updated associations.
	var authorNames []string
	bookAuthors, err := mfs.db.GetBookAuthors(originalID)
	if err == nil && len(bookAuthors) > 0 {
		for _, ba := range bookAuthors {
			if author, aerr := mfs.db.GetAuthorByID(ba.AuthorID); aerr == nil && author != nil {
				authorNames = append(authorNames, author.Name)
			}
		}
	} else if originalBook.AuthorID != nil {
		if author, aerr := mfs.db.GetAuthorByID(*originalBook.AuthorID); aerr == nil && author != nil {
			authorNames = append(authorNames, author.Name)
		}
	}
	if len(authorNames) == 0 && ov != nil && ov.Author != "" {
		authorNames = append(authorNames, ov.Author)
	}
	artistStr := strings.Join(authorNames, ", ")

	// --- Resolve narrator names ---
	var narratorNames []string
	bookNarrators, err := mfs.db.GetBookNarrators(originalID)
	if err == nil && len(bookNarrators) > 0 {
		for _, bn := range bookNarrators {
			if narrator, nerr := mfs.db.GetNarratorByID(bn.NarratorID); nerr == nil && narrator != nil {
				narratorNames = append(narratorNames, narrator.Name)
			}
		}
	} else if originalBook.Narrator != nil && *originalBook.Narrator != "" {
		narratorNames = append(narratorNames, *originalBook.Narrator)
	}
	narratorStr := strings.Join(narratorNames, " & ")

	// --- Determine year ---
	// Use original book's year since it has the freshly-applied metadata
	year := 0
	if originalBook.AudiobookReleaseYear != nil && *originalBook.AudiobookReleaseYear > 0 {
		year = *originalBook.AudiobookReleaseYear
	} else if originalBook.PrintYear != nil && *originalBook.PrintYear > 0 {
		year = *originalBook.PrintYear
	} else if ov != nil && ov.PublishYear > 0 {
		year = ov.PublishYear
	}

	opConfig := fileops.OperationConfig{VerifyChecksums: true}

	// --- Collect active book files ---
	bookFiles, bfErr := mfs.db.GetBookFiles(book.ID)
	if bfErr != nil {
		bookFiles = nil
	}
	bookFiles = dedupeBookFilesByPath(book.ID, bookFiles)
	// Filter to non-missing only
	var activeFiles []database.BookFile
	for _, bf := range bookFiles {
		if !bf.Missing {
			activeFiles = append(activeFiles, bf)
		}
	}

	// Apply optional segment/file filter
	if len(segmentFilter) > 0 {
		filterSet := make(map[string]struct{}, len(segmentFilter))
		for _, sid := range segmentFilter {
			filterSet[sid] = struct{}{}
		}
		var filtered []database.BookFile
		for _, bf := range activeFiles {
			if _, ok := filterSet[bf.ID]; ok {
				filtered = append(filtered, bf)
			}
		}
		activeFiles = filtered
	}

	totalTracks := len(activeFiles)
	writtenCount := 0
	skippedProtected := 0

	// Embed cover art via TagLib (independent of tag writes — no ordering constraint).
	if config.AppConfig.RootDir != "" {
		mfs.embedCoverInBookFiles(book, metadata.CoverPathForBook(config.AppConfig.RootDir, book.ID))
	}

	// Use the original book's title for tag content (it has freshly-applied metadata)
	bookTitle := originalBook.Title
	if bookTitle == "" && ov != nil {
		bookTitle = ov.Title
	}
	if totalTracks > 1 {
		// Multi-file: write to each file with per-track title and numbering
		digits := len(fmt.Sprintf("%d", totalTracks))
		trackFmt := fmt.Sprintf("%%0%dd", digits)
		for i, bf := range activeFiles {
			trackNum := i + 1
			generated := fmt.Sprintf(trackFmt+" - %s", trackNum, bookTitle)
			trackStr := fmt.Sprintf("%d/%d", trackNum, totalTracks)

			// One tag read per file, shared by the chapter-title decision and the
			// unchanged-tag filter (see the identical loop in writeBackMetadata).
			cur, curErr := metadata.ExtractMetadata(bf.FilePath, nil)

			segTitle := generated
			if curErr == nil {
				segTitle = chapterTitleFor(cur.Title, generated, bookTitle)
			}

			tagMap := mfs.BuildFullTagMap(book, bookTitle, segTitle, artistStr, narratorStr, year, trackStr)
			if segTitle == "" {
				// Preserve the file's real per-chapter title.
				delete(tagMap, "title")
			}
			if curErr == nil {
				tagMap = filterTagsAgainst(bf.FilePath, cur, tagMap)
			}
			// curErr != nil keeps the historical safe fallback: write everything.
			if len(tagMap) == 0 {
				slog.Debug("write-back file tags already match, skipping", "path", bf.FilePath)
				continue
			}
			if mfs.isProtectedPath(bf.FilePath) {
				slog.Debug("skipping write-back for protected file", "path", bf.FilePath)
				skippedProtected++
				continue
			}
			backupFileBeforeWrite(bf.FilePath)
			if _, _, err := fileops.WriteTagsSafe(bf.FilePath, func(tmpPath string) error {
				return metadata.WriteMetadataToFileInPlace(tmpPath, tagMap, opConfig)
			}, fileops.WriteTagsSafeOptions{BookFileID: bf.ID, Store: mfs.db}); err != nil {
				slog.Warn("write-back failed for file",
					"path", bf.FilePath, "error", err,
					"book_id", book.ID, "book_title", book.Title,
					"book_file_id", bf.ID, "file_index", i+1, "file_count", len(activeFiles),
					"tag_keys", sortedKeys(tagMap))
			} else {
				// Log successes as well as failures. Without this the multi-file
				// branch was silent on success while the single-file branch below
				// logged "wrote metadata back to", so the logs showed only
				// failures for multi-file books and it was impossible to tell a
				// working write path from one that never ran at all.
				slog.Info("wrote metadata back to", "path", bf.FilePath)
				writtenCount++
			}
		}
	} else {
		// Single-file or no files: write to book.FilePath.
		// If book.FilePath is a directory (multi-file book with no file records),
		// glob for audio files inside and write to each one individually.
		if mfs.isProtectedPath(book.FilePath) {
			slog.Debug("skipping write-back for protected path", "path", book.FilePath)
			skippedProtected++
		} else {
			fullTagMap := mfs.BuildFullTagMap(book, bookTitle, bookTitle, artistStr, narratorStr, year, "")
			// Filter out tags whose current on-disk value already
			// matches the DB state, so a re-run of bulk write-back
			// is near-free when nothing actually changed.
			// filterUnchangedTags now covers album_artist and
			// composer (both narrator-sourced in our convention),
			// so the filter correctly no-ops on unchanged books
			// instead of always-writing because of those keys.
			dirFiles := AudioFilesInDir(book.FilePath)
			if len(dirFiles) > 0 {
				// book.FilePath is a directory — write to each audio file found inside.
				slog.Info("write-back is a directory; writing to audio file(s) inside", "path", book.FilePath, "file", len(dirFiles))
				for _, f := range dirFiles {
					if mfs.isProtectedPath(f) {
						slog.Debug("skipping write-back for protected file", "value", f)
						skippedProtected++
						continue
					}
					fm := FilterUnchangedTags(f, fullTagMap)
					if len(fm) == 0 {
						slog.Debug("write-back all tags match, skipping", "value", f)
						continue
					}
					backupFileBeforeWrite(f)
					var wtsOpts fileops.WriteTagsSafeOptions
					if bff, bfferr := mfs.db.GetBookFileByPath(f); bfferr == nil && bff != nil {
						wtsOpts = fileops.WriteTagsSafeOptions{BookFileID: bff.ID, Store: mfs.db}
					}
					if _, _, err := fileops.WriteTagsSafe(f, func(tmpPath string) error {
						return metadata.WriteMetadataToFileInPlace(tmpPath, fm, opConfig)
					}, wtsOpts); err != nil {
						slog.Warn("write-back failed for file",
							"path", f, "error", err,
							"book_id", book.ID, "book_title", book.Title,
							"tag_keys", sortedKeys(fm))
					} else {
						slog.Info("wrote metadata back to", "path", f)
						writtenCount++
					}
				}
			} else {
				fm := FilterUnchangedTags(book.FilePath, fullTagMap)
				if len(fm) == 0 {
					slog.Debug("write-back all tags match, skipping", "path", book.FilePath)
				} else {
					backupFileBeforeWrite(book.FilePath)
					var wtsOpts fileops.WriteTagsSafeOptions
					if bff, bfferr := mfs.db.GetBookFileByPath(book.FilePath); bfferr == nil && bff != nil {
						wtsOpts = fileops.WriteTagsSafeOptions{BookFileID: bff.ID, Store: mfs.db}
					}
					if _, _, err := fileops.WriteTagsSafe(book.FilePath, func(tmpPath string) error {
						return metadata.WriteMetadataToFileInPlace(tmpPath, fm, opConfig)
					}, wtsOpts); err != nil {
						slog.Warn("write-back failed for file",
							"path", book.FilePath, "error", err,
							"book_id", book.ID, "book_title", book.Title,
							"tag_keys", sortedKeys(fm))
					} else {
						writtenCount++
					}
				}
			}
		}
	}

	// --- Write to version-linked copies in the library folder ---
	if book.VersionGroupID != nil && *book.VersionGroupID != "" && config.AppConfig.RootDir != "" {
		siblings, sibErr := mfs.db.GetBooksByVersionGroup(*book.VersionGroupID)
		if sibErr == nil {
			for _, sib := range siblings {
				if sib.ID == book.ID {
					continue // already written above
				}
				if !strings.HasPrefix(sib.FilePath, config.AppConfig.RootDir) {
					continue // only write to library copies, leave import copies alone
				}
				if mfs.isProtectedPath(sib.FilePath) {
					continue
				}
				tagMap := mfs.BuildTagMap(bookTitle, bookTitle, artistStr, narratorStr, year, "")
				tagMap = FilterUnchangedTags(sib.FilePath, tagMap)
				if len(tagMap) == 0 {
					continue // tags already match, nothing to write
				}
				backupFileBeforeWrite(sib.FilePath)
				if _, _, err := fileops.WriteTagsSafe(sib.FilePath, func(tmpPath string) error {
					return metadata.WriteMetadataToFileInPlace(tmpPath, tagMap, opConfig)
				}, fileops.WriteTagsSafeOptions{}); err != nil {
					slog.Warn("write-back failed for version-linked", "path", sib.FilePath, "error", err)
				} else {
					writtenCount++
					slog.Info("wrote metadata to version-linked copy", "path", sib.FilePath)
				}
			}
		}
	}

	// --- Record history entry ---
	now := time.Now()
	summaryVal := fmt.Sprintf("%q (wrote %d file(s))", book.Title, writtenCount)
	summaryJSON := jsonEncodeString(summaryVal)
	record := &database.MetadataChangeRecord{
		BookID:     book.ID,
		Field:      "write_back",
		NewValue:   &summaryJSON,
		ChangeType: "write-back",
		Source:     "manual",
		ChangedAt:  now,
	}
	if err := mfs.db.RecordMetadataChange(record); err != nil {
		slog.Warn("failed to record write-back history for", "id", book.ID, "error", err)
	}
	// Dual-write to unified activity log (Task 16: tag_write)
	if mfs.activityService != nil && writtenCount > 0 {
		_ = mfs.activityService.Record(database.ActivityEntry{
			Tier:    "change",
			Type:    "tag_write",
			Level:   "info",
			Source:  "background",
			BookID:  book.ID,
			Summary: fmt.Sprintf("Wrote tags to %d file(s) for %s", writtenCount, book.Title),
		})
	}

	// Stamp last_written_at
	if writtenCount > 0 {
		if err := mfs.db.SetLastWrittenAt(book.ID, now); err != nil {
			slog.Warn("failed to stamp last_written_at for book", "id", book.ID, "error", err)
		}
		// Flag for rescan so the next incremental scan re-reads the updated tags.
		_ = mfs.db.MarkNeedsRescan(book.ID)
	}

	if skippedProtected > 0 {
		slog.Info("write-back for book wrote file(s), skipped protected path(s)", "id", book.ID, "count", writtenCount-skippedProtected, "value", skippedProtected)
	}

	return writtenCount, nil
}
