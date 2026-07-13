// file: internal/metafetch/service_apply.go
// version: 1.4.0
// guid: 6ca469ca-7d2e-4738-b6f1-ae09449ed9e4
// last-edited: 2026-07-13

package metafetch

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/policy"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"github.com/oklog/ulid/v2"
)

func (mfs *Service) ApplyMetadataToBook(book *database.Book, meta metadata.BookMetadata) {
	originalTitle := book.Title
	if meta.Title != "" && meta.Title != "Untitled" && IsBetterValue(book.Title, meta.Title) {
		// Don't replace a real title with something shorter/worse
		if book.Title != "" && !IsGarbageValue(book.Title) && len(meta.Title) < 3 {
			// Skip very short replacement titles
		} else {
			book.Title = meta.Title
		}
	}
	// Final safety: never leave title empty if it was set before
	if book.Title == "" && originalTitle != "" {
		book.Title = originalTitle
		slog.Warn("applyMetadataToBook prevented title from being cleared for book", "id", book.ID)
	}
	if meta.Publisher != "" && IsBetterStringPtr(book.Publisher, meta.Publisher) {
		book.Publisher = stringPtr(meta.Publisher)
	}
	if meta.Language != "" && IsBetterStringPtr(book.Language, meta.Language) {
		book.Language = stringPtr(meta.Language)
	}
	// Route the year by its source KIND. meta.PublishYear is OVERLOADED:
	// Audible/Audnexus report the audiobook RELEASE year, while Open Library /
	// Google Books / Hardcover / Wikipedia report the original PRINT/work year
	// (often decades earlier). This used to write PublishYear into
	// AudiobookReleaseYear unconditionally, so a print-year candidate clobbered a
	// correct Audible release year — which then propagated to the file `year` tag
	// via service_writeback.go. Now: release → AudiobookReleaseYear (unchanged
	// behavior), print → PrintYear (only when empty). The `!= 0` gate means a
	// present year is never overwritten with 0, and the two fields never
	// cross-contaminate.
	if meta.PublishYear != 0 {
		if meta.PublishYearIsAudiobookRelease {
			book.AudiobookReleaseYear = intPtrHelper(meta.PublishYear)
		} else if book.PrintYear == nil || *book.PrintYear == 0 {
			book.PrintYear = intPtrHelper(meta.PublishYear)
		}
	}
	if meta.CoverURL != "" {
		book.CoverURL = stringPtr(meta.CoverURL)
	}
	if meta.Narrator != "" && !IsGarbageValue(meta.Narrator) && IsBetterStringPtr(book.Narrator, meta.Narrator) {
		book.Narrator = stringPtr(meta.Narrator)
	}

	// Apply author if fetched data is better — resolve to AuthorID and
	// replace the book_authors join table so stale associations are removed.
	extractedAuthor := meta.Author
	if extractedAuthor != "" && !IsGarbageValue(extractedAuthor) {
		// Guard: if extracted artist matches the book's narrator (not the author),
		// the tag has narrator in the artist field — keep the DB author.
		if book.AuthorID != nil && book.Narrator != nil {
			if existingAuthor, aErr := mfs.db.GetAuthorByID(*book.AuthorID); aErr == nil && existingAuthor != nil {
				if strings.EqualFold(extractedAuthor, *book.Narrator) && !strings.EqualFold(extractedAuthor, existingAuthor.Name) {
					slog.Info("applyMetadataToBook extracted artist matches narrator but not author for book — skipping author update", "extracted_author", extractedAuthor, "narrator", *book.Narrator, "name", existingAuthor.Name, "id", book.ID)
					extractedAuthor = ""
				} else if !strings.EqualFold(extractedAuthor, existingAuthor.Name) && !strings.EqualFold(extractedAuthor, *book.Narrator) {
					// Extracted artist doesn't match either stored author or narrator — log mismatch for review
					slog.Warn("applyMetadataToBook extracted artist matches neither author nor narrator for book", "extracted_author", extractedAuthor, "name", existingAuthor.Name, "narrator", *book.Narrator, "id", book.ID)
				}
			}
		}
	}
	if extractedAuthor != "" && !IsGarbageValue(extractedAuthor) {
		author, err := mfs.db.GetAuthorByName(extractedAuthor)
		if err == nil && author == nil {
			author, err = mfs.db.CreateAuthor(extractedAuthor)
		}
		if err == nil && author != nil {
			book.AuthorID = &author.ID
			_ = mfs.db.SetBookAuthors(book.ID, []database.BookAuthor{
				{BookID: book.ID, AuthorID: author.ID, Role: "author", Position: 0},
			})
		}
	}

	// Apply ISBN/ASIN
	if meta.ISBN != "" {
		if len(meta.ISBN) == 10 {
			book.ISBN10 = stringPtr(meta.ISBN)
		} else {
			book.ISBN13 = stringPtr(meta.ISBN)
		}
	}
	if meta.ASIN != "" {
		book.ASIN = stringPtr(meta.ASIN)
	}
	if meta.Description != "" {
		book.Description = stringPtr(meta.Description)
	}
	if meta.Genre != "" {
		book.Genre = stringPtr(meta.Genre)
	}

	// Persist Audible runtime so the scan-duration-mismatch endpoint can
	// compare it offline without live API calls.
	if meta.DurationSec > 0 {
		runtimeMin := meta.DurationSec / 60
		book.AudibleRuntimeMin = &runtimeMin
	}

	// Apply series info if available
	if meta.Series != "" && !IsGarbageValue(meta.Series) {
		series, err := mfs.db.GetSeriesByName(meta.Series, book.AuthorID)
		if err == nil && series == nil {
			series, err = mfs.db.CreateSeries(meta.Series, book.AuthorID)
		}
		if err == nil && series != nil {
			book.SeriesID = &series.ID
		}
		if meta.SeriesPosition != "" {
			if pos, err := strconv.Atoi(meta.SeriesPosition); err == nil {
				book.SeriesSequence = &pos
			}
		}
	}
}

// RecordChangeHistory records metadata changes before they are applied.
func (mfs *Service) RecordChangeHistory(book *database.Book, meta metadata.BookMetadata, sourceName string) {
	now := time.Now()

	// Resolve current author name for history
	var currentAuthor string
	if book.AuthorID != nil {
		if author, err := mfs.db.GetAuthorByID(*book.AuthorID); err == nil && author != nil {
			currentAuthor = author.Name
		}
	}

	// Resolve current series name for history
	var currentSeries string
	if book.SeriesID != nil {
		if series, err := mfs.db.GetSeriesByID(*book.SeriesID); err == nil && series != nil {
			currentSeries = series.Name
		}
	}

	changes := []struct {
		field  string
		oldVal string
		newVal string
	}{
		{"title", book.Title, meta.Title},
		{"author_name", currentAuthor, meta.Author},
		{"narrator", derefString(book.Narrator), meta.Narrator},
		{"publisher", derefString(book.Publisher), meta.Publisher},
		{"language", derefString(book.Language), meta.Language},
		{"series", currentSeries, meta.Series},
		{"series_position", derefIntAsString(book.SeriesSequence), meta.SeriesPosition},
		{"cover_url", derefString(book.CoverURL), meta.CoverURL},
	}

	if meta.PublishYear != 0 {
		// Record against the field the year actually routes to (see
		// ApplyMetadataToBook): release-kind → audiobook_release_year,
		// print-kind → print_year.
		yearField := "print_year"
		yearOld := derefIntAsString(book.PrintYear)
		if meta.PublishYearIsAudiobookRelease {
			yearField = "audiobook_release_year"
			yearOld = derefIntAsString(book.AudiobookReleaseYear)
		}
		changes = append(changes, struct {
			field  string
			oldVal string
			newVal string
		}{yearField, yearOld, strconv.Itoa(meta.PublishYear)})
	}

	for _, c := range changes {
		if c.newVal == "" || c.newVal == c.oldVal {
			continue
		}
		oldJSON := jsonEncodeString(c.oldVal)
		newJSON := jsonEncodeString(c.newVal)
		record := &database.MetadataChangeRecord{
			BookID:        book.ID,
			Field:         c.field,
			PreviousValue: &oldJSON,
			NewValue:      &newJSON,
			ChangeType:    "fetched",
			Source:        sourceName,
			ChangedAt:     now,
		}
		if err := mfs.db.RecordMetadataChange(record); err != nil {
			slog.Warn("failed to record metadata change for .", "id", book.ID, "field", c.field, "error", err)
		}
		// Dual-write to unified activity log
		if mfs.activityService != nil {
			_ = mfs.activityService.Record(database.ActivityEntry{
				Tier:    "change",
				Type:    "metadata_apply",
				Level:   "info",
				Source:  "background",
				BookID:  book.ID,
				Summary: fmt.Sprintf("Applied %s: %s → %s", c.field, truncateActivity(c.oldVal, 50), truncateActivity(c.newVal, 50)),
				Details: map[string]any{"field": c.field, "old_value": c.oldVal, "new_value": c.newVal, "source": sourceName},
			})
		}
	}
}

// syncMetadataToLibraryCopy copies metadata fields from the original book to
// the library copy so that both DB records stay in sync. This is needed because
// ApplyMetadataCandidate only updates the original book's DB record, leaving
// the library copy with stale metadata.
func (mfs *Service) syncMetadataToLibraryCopy(original, libCopy *database.Book) {
	// Sync display/metadata fields — preserve library copy's file/path/version fields
	libCopy.Title = original.Title
	libCopy.AuthorID = original.AuthorID
	libCopy.Narrator = original.Narrator
	libCopy.SeriesID = original.SeriesID
	libCopy.SeriesSequence = original.SeriesSequence
	libCopy.Publisher = original.Publisher
	libCopy.Language = original.Language
	libCopy.Description = original.Description
	libCopy.AudiobookReleaseYear = original.AudiobookReleaseYear
	libCopy.PrintYear = original.PrintYear
	libCopy.ISBN10 = original.ISBN10
	libCopy.ISBN13 = original.ISBN13
	libCopy.ASIN = original.ASIN
	libCopy.Edition = original.Edition
	libCopy.Genre = original.Genre
	libCopy.OpenLibraryID = original.OpenLibraryID
	libCopy.HardcoverID = original.HardcoverID
	libCopy.GoogleBooksID = original.GoogleBooksID
	libCopy.CoverURL = original.CoverURL
	libCopy.MetadataReviewStatus = original.MetadataReviewStatus

	if _, err := mfs.db.UpdateBook(libCopy.ID, libCopy); err != nil {
		slog.Warn("failed to sync metadata to library copy", "id", libCopy.ID, "error", err)
	} else {
		slog.Info("synced metadata from to library copy", "originalID", original.ID, "libCopyID", libCopy.ID)
	}

	// Also sync author associations
	if authors, err := mfs.db.GetBookAuthors(original.ID); err == nil && len(authors) > 0 {
		var newAuthors []database.BookAuthor
		for _, ba := range authors {
			newAuthors = append(newAuthors, database.BookAuthor{
				BookID: libCopy.ID, AuthorID: ba.AuthorID, Role: ba.Role, Position: ba.Position,
			})
		}
		_ = mfs.db.SetBookAuthors(libCopy.ID, newAuthors)
	}

	// Sync narrator associations
	if narrators, err := mfs.db.GetBookNarrators(original.ID); err == nil && len(narrators) > 0 {
		var newNarrators []database.BookNarrator
		for _, bn := range narrators {
			newNarrators = append(newNarrators, database.BookNarrator{
				BookID: libCopy.ID, NarratorID: bn.NarratorID, Role: bn.Role, Position: bn.Position,
			})
		}
		_ = mfs.db.SetBookNarrators(libCopy.ID, newNarrators)
	}
}

// ensureLibraryCopy returns a book record with files in the library folder.
// If the book is already in the library, returns it as-is. If the book is in a
// protected path (iTunes/import), looks for an existing library version or
// organizes (hard-links) the file(s) to the library and creates a new version record.
// For multi-file books, all segments are also organized and recreated.
func (mfs *Service) ensureLibraryCopy(book *database.Book) *database.Book {
	if config.AppConfig.RootDir == "" {
		return book // no library configured
	}
	if strings.HasPrefix(book.FilePath, config.AppConfig.RootDir) {
		return book // already in library
	}
	if !mfs.isProtectedPath(book.FilePath) {
		return book // not protected, safe to modify
	}

	// Check for existing library version in the same version group
	if book.VersionGroupID != nil && *book.VersionGroupID != "" {
		siblings, err := mfs.db.GetBooksByVersionGroup(*book.VersionGroupID)
		if err == nil {
			for i := range siblings {
				if siblings[i].ID != book.ID && strings.HasPrefix(siblings[i].FilePath, config.AppConfig.RootDir) {
					slog.Info("using existing library copy for protected book", "siblingID", siblings[i].ID, "bookID", book.ID)
					return &siblings[i]
				}
			}
		}
	}

	// Collect file paths for multi-file books
	bookFiles, bfErr := mfs.db.GetBookFiles(book.ID)
	var activeFiles []database.BookFile
	if bfErr == nil {
		for _, bf := range bookFiles {
			if !bf.Missing {
				activeFiles = append(activeFiles, bf)
			}
		}
	}

	org := organizer.NewOrganizer(&config.AppConfig)
	var newBookPath string
	var pathMap map[string]string

	if len(activeFiles) > 1 {
		// Multi-file: organize all book files to library directory
		filePaths := make([]string, len(activeFiles))
		for i, bf := range activeFiles {
			filePaths[i] = bf.FilePath
		}
		targetDir, pm, err := org.OrganizeBookDirectory(book, filePaths)
		if err != nil {
			slog.Warn("failed to create library copy for multi-file book", "id", book.ID, "error", err)
			return nil
		}
		pathMap = pm
		// Use the directory as the book's primary path
		newBookPath = targetDir
	} else {
		// Single-file: organize just the book file
		p, _, err := org.OrganizeBook(book)
		if err != nil {
			slog.Warn("failed to create library copy for", "id", book.ID, "error", err)
			return nil
		}
		newBookPath = p
	}

	// Create version-linked record for the library copy
	isPrimary := true
	isNotPrimary := false
	organizedState := "organized"
	versionGroupID := ""
	if book.VersionGroupID != nil && *book.VersionGroupID != "" {
		versionGroupID = *book.VersionGroupID
	} else {
		versionGroupID = ulid.Make().String()
	}

	newBook := *book
	newBook.ID = ulid.Make().String()
	newBook.FilePath = newBookPath
	newBook.LibraryState = &organizedState
	newBook.VersionGroupID = &versionGroupID
	newBook.IsPrimaryVersion = &isPrimary

	created, err := mfs.db.CreateBook(&newBook)
	if err != nil {
		slog.Warn("failed to create library book record for", "id", book.ID, "error", err)
		return nil
	}

	// Record the library-copy event so history shows where the copy came from.
	_ = mfs.db.RecordPathChange(&database.BookPathChange{
		BookID:     created.ID,
		OldPath:    book.FilePath,
		NewPath:    created.FilePath,
		ChangeType: "library_copy",
	})

	// Copy book_authors to the new record
	if authors, err := mfs.db.GetBookAuthors(book.ID); err == nil && len(authors) > 0 {
		var newAuthors []database.BookAuthor
		for _, ba := range authors {
			newAuthors = append(newAuthors, database.BookAuthor{
				BookID: created.ID, AuthorID: ba.AuthorID, Role: ba.Role, Position: ba.Position,
			})
		}
		_ = mfs.db.SetBookAuthors(created.ID, newAuthors)
	}

	// Copy book files with updated file paths for multi-file books
	if len(activeFiles) > 1 && pathMap != nil {
		for _, bf := range activeFiles {
			newBF := bf
			newBF.ID = ulid.Make().String()
			newBF.BookID = created.ID
			if newPath, ok := pathMap[bf.FilePath]; ok {
				newBF.FilePath = newPath
				newBF.ITunesPath = ComputeITunesPath(newPath)
			}
			if err := mfs.db.CreateBookFile(&newBF); err != nil {
				slog.Warn("failed to copy book_file for library book", "bookFileID", bf.ID, "newBookID", created.ID, "error", err)
			}
		}
	}

	// Demote original to non-primary
	book.VersionGroupID = &versionGroupID
	book.IsPrimaryVersion = &isNotPrimary
	_, _ = mfs.db.UpdateBook(book.ID, book)

	slog.Info("created library copy -> for protected book ( file(s))", "path", newBookPath, "newBookID", created.ID, "originalBookID", book.ID, "file", len(activeFiles))
	return created
}
func (mfs *Service) persistFetchedMetadata(bookID string, meta metadata.BookMetadata) {
	fetchedValues := map[string]any{}
	if meta.Title != "" {
		fetchedValues["title"] = meta.Title
	}
	if meta.Publisher != "" {
		fetchedValues["publisher"] = meta.Publisher
	}
	if meta.Language != "" {
		fetchedValues["language"] = meta.Language
	}
	if meta.PublishYear != 0 {
		// Provenance key mirrors the routed field (see ApplyMetadataToBook).
		if meta.PublishYearIsAudiobookRelease {
			fetchedValues["audiobook_release_year"] = meta.PublishYear
		} else {
			fetchedValues["print_year"] = meta.PublishYear
		}
	}
	if meta.CoverURL != "" {
		fetchedValues["cover_url"] = meta.CoverURL
	}
	if meta.Author != "" {
		fetchedValues["author_name"] = meta.Author
	}
	if meta.ISBN != "" {
		if len(meta.ISBN) == 10 {
			fetchedValues["isbn10"] = meta.ISBN
		} else {
			fetchedValues["isbn13"] = meta.ISBN
		}
	}
	if meta.ASIN != "" {
		fetchedValues["asin"] = meta.ASIN
	}
	if len(fetchedValues) > 0 {
		if err := mfs.updateFetchedMetadataState(bookID, fetchedValues); err != nil {
			slog.Error("FetchMetadataForBook failed to persist fetched metadata state", "error", err)
		}
	}
}

// ApplyMetadataCandidate applies a user-selected metadata candidate to a book.
// If fields is non-empty, only the listed fields are applied.
func (mfs *Service) ApplyMetadataCandidate(id string, candidate MetadataCandidate, fields []string) (*FetchMetadataResponse, error) {
	book, err := mfs.db.GetBookByID(id)
	if err != nil || book == nil {
		return nil, fmt.Errorf("audiobook not found")
	}

	// Policy check: policy:no-metadata tag skips automated metadata application.
	if tags, tagErr := mfs.db.GetBookTags(id); tagErr == nil {
		if policy.EvaluatePolicy(tags).NoMetadataFetch {
			return nil, fmt.Errorf("metadata application disabled by policy:no-metadata tag")
		}
	}

	// Warn when the candidate runtime diverges significantly from the local
	// file duration — this suggests a wrong Audible match or an abridged copy.
	const durationMismatchThresholdSec = 600
	if candidate.DurationDeltaSec > durationMismatchThresholdSec {
		bookDurSec := 0
		if book.Duration != nil {
			bookDurSec = *book.Duration
		}
		slog.Warn("duration-mismatch apply book title candidate deltas (books audibles) wrong match or abridged version", "bookID", id, "bookTitle", book.Title, "candidateTitle", candidate.Title, "durationDeltaSec", candidate.DurationDeltaSec, "bookDurationSec", bookDurSec, "candidateDurationSec", candidate.DurationSec)
	}

	meta := metadata.BookMetadata{
		Title:          candidate.Title,
		Author:         candidate.Author,
		Narrator:       candidate.Narrator,
		Series:         candidate.Series,
		SeriesPosition: candidate.SeriesPosition,
		PublishYear:    candidate.Year,
		Publisher:      candidate.Publisher,
		ISBN:           candidate.ISBN,
		CoverURL:       candidate.CoverURL,
		Description:    candidate.Description,
		Language:       candidate.Language,
		DurationSec:    candidate.DurationSec,
		// candidate.Year is a bare int with no kind attached; derive whether it
		// is an audiobook release year (Audible/Audnexus) from the source name so
		// ApplyMetadataToBook routes it to the same field as the auto-fetch path.
		PublishYearIsAudiobookRelease: metadata.SourceProducesAudiobookReleaseYear(candidate.Source),
	}

	// If fields list is non-empty, zero out fields NOT in the list
	if len(fields) > 0 {
		allowed := map[string]bool{}
		for _, f := range fields {
			allowed[f] = true
		}
		if !allowed["title"] {
			meta.Title = ""
		}
		if !allowed["author"] {
			meta.Author = ""
		}
		if !allowed["narrator"] {
			meta.Narrator = ""
		}
		if !allowed["series"] {
			meta.Series = ""
			meta.SeriesPosition = ""
		}
		if !allowed["year"] {
			meta.PublishYear = 0
		}
		if !allowed["publisher"] {
			meta.Publisher = ""
		}
		if !allowed["isbn"] {
			meta.ISBN = ""
		}
		if !allowed["cover_url"] {
			meta.CoverURL = ""
		}
		if !allowed["description"] {
			meta.Description = ""
		}
		if !allowed["language"] {
			meta.Language = ""
		}
	}

	// Strip embedded "Series Name, Book N" before persisting — protects
	// against Audible/Audnexus candidates where the book number is baked
	// into the series name. Same normalization the auto-fetch paths run.
	NormalizeMetaSeries(&meta)

	// Record history BEFORE applying changes so old values are correct
	mfs.RecordChangeHistory(book, meta, candidate.Source)

	mfs.ApplyMetadataToBook(book, meta)

	// Set review status and record which provider supplied the metadata
	matched := "matched"
	book.MetadataReviewStatus = &matched
	src := candidate.Source
	book.MetadataSource = &src
	th := hintsFromBook(book)
	audioConfirmed := !th.empty() &&
		th.title != "" &&
		util.NormalizeTitle(th.title) == util.NormalizeTitle(candidate.Title)
	if audioConfirmed {
		// Also require author match if we have one, but only if the token is
		// substantial (>3 chars) to avoid short fragments like "Ki" from
		// over-constraining the match.
		if th.author == "" || len(th.author) <= 3 || containsCI(candidate.Author, th.author) {
			ac := "audio_confirmed"
			book.MetadataReviewStatus = &ac
			slog.Info("metadata apply: audio-confirmed match", "id", id, "title", candidate.Title)
			appendMetadataVersionNote(book, "audio_confirmed")
		}
	}

	// Compute metadata_source_hash = sha256("{source}:{canonical_id}") so the
	// dedup engine can later detect books sharing the exact same external record.
	canonicalID := metadataCanonicalID(candidate)
	if canonicalID != "" {
		h := fmt.Sprintf("%x", sha256.Sum256([]byte(src+":"+canonicalID)))
		book.MetadataSourceHash = &h
	}

	updatedBook, updateErr := mfs.db.UpdateBook(id, book)
	if updateErr != nil {
		return nil, fmt.Errorf("failed to update book: %w", updateErr)
	}

	// Check whether any other book already carries the same hash — if so,
	// emit a dedup candidate so the user can review the potential duplicate.
	if book.MetadataSourceHash != nil {
		mfs.checkMetadataSourceHashDuplicates(id, *book.MetadataSourceHash)
	}

	// Persist fetched values for provenance tracking
	mfs.persistFetchedMetadata(id, meta)

	// Generate segment titles (fast, DB-only)
	if err := mfs.generateSegmentTitles(id, updatedBook.Title); err != nil {
		slog.Warn("generate segment titles failed for", "id", id, "error", err)
	}

	// Download cover art (fast network fetch + file write — keep inline so
	// the response includes the updated cover_url for the UI).
	if meta.CoverURL != "" && config.AppConfig.RootDir != "" {
		coverPath, coverErr := metadata.DownloadCoverArt(meta.CoverURL, config.AppConfig.RootDir, id)
		if coverErr != nil {
			slog.Warn("cover art download failed for", "id", id, "error", coverErr)
		} else {
			slog.Info("cover art saved to", "path", coverPath)
			localCoverURL := "/api/v1/covers/local/" + filepath.Base(coverPath)
			if updatedBook != nil {
				updatedBook.CoverURL = &localCoverURL
				mfs.db.UpdateBook(id, updatedBook)
			}
		}
	}

	// Queue background ISBN/ASIN enrichment if identifiers are missing
	if updatedBook != nil {
		mfs.queueISBNEnrichment(id, updatedBook)
	}

	// Tag the book with metadata:source:* and metadata:language:*
	// as system-applied provenance tags. Uses the singleton
	// helpers so a no-op re-apply of the same source/language is
	// a true no-op at the tag layer (no wasted writes). Done after
	// UpdateBook so a failed update never leaves stale tags behind.
	mfs.ApplyMetadataSystemTags(id, candidate.Source, meta.Language)

	// Apply Audible category ladder tags. These are additive enrichment — they
	// are not controlled by the fields allowlist and do not fail the apply if
	// a tag write errors.
	for _, tag := range candidate.CategoryTags {
		if err := mfs.db.AddBookTagWithSource(id, tag, "audible_category"); err != nil {
			slog.Warn("failed to apply category tag to book", "value", tag, "id", id, "error", err)
		}
	}

	// Intentionally keep the metadata fetch cache after apply. The cached
	// API results are still valid — the TTL (MetadataFetchCacheTTLDays)
	// governs when re-fetches happen. Wiping here would force every
	// subsequent scan to hit the external API again.

	return &FetchMetadataResponse{
		Message: "metadata candidate applied",
		Book:    updatedBook,
		Source:  candidate.Source,
	}, nil
}

func appendMetadataVersionNote(book *database.Book, marker string) {
	if book.VersionNotes != nil && strings.Contains(*book.VersionNotes, marker) {
		return
	}
	if book.VersionNotes == nil || strings.TrimSpace(*book.VersionNotes) == "" {
		book.VersionNotes = &marker
		return
	}
	notes := strings.TrimSpace(*book.VersionNotes) + "\n" + marker
	book.VersionNotes = &notes
}

// checkMetadataSourceHashDuplicates auto-flags any existing non-merged book
// that shares the same metadata_source_hash as bookID (MATCH-4). The book
// with the most book_files is kept as primary; all others get
// merged_into_book_id set to point at it.
func (mfs *Service) checkMetadataSourceHashDuplicates(bookID, hash string) {
	matches, err := mfs.db.GetBooksByMetadataSourceHash(hash)
	if err != nil {
		slog.Warn("MATCH-4 metadata-source-hash dedup query failed for book", "id", bookID, "error", err)
		return
	}

	// Build the full set of non-merged books sharing this hash; ensure bookID is included.
	allMap := make(map[string]database.Book, len(matches)+1)
	for _, b := range matches {
		allMap[b.ID] = b
	}
	if _, ok := allMap[bookID]; !ok {
		if self, err := mfs.db.GetBookByID(bookID); err == nil && self != nil {
			allMap[bookID] = *self
		}
	}

	if len(allMap) < 2 {
		return // no duplicates
	}

	// Pick primary: book with the most book_files. On tie, prefer earlier created_at.
	primaryID := bookID
	maxFiles := -1
	for id, b := range allMap {
		files, err := mfs.db.GetBookFiles(id)
		n := 0
		if err == nil {
			n = len(files)
		}
		isBetter := n > maxFiles
		if n == maxFiles && b.CreatedAt != nil {
			if cur, ok := allMap[primaryID]; ok && cur.CreatedAt != nil && b.CreatedAt.Before(*cur.CreatedAt) {
				isBetter = true
			}
		}
		if isBetter {
			maxFiles = n
			primaryID = id
		}
	}

	for id := range allMap {
		if id == primaryID {
			continue
		}
		if err := mfs.db.FlagMetadataHashDuplicate(primaryID, id); err != nil {
			slog.Warn("MATCH-4 failed to flag book as duplicate of", "dupID", id, "primaryID", primaryID, "error", err)
		} else {
			slog.Info("MATCH-4 auto-flagged book as merged into primary (hash )", "dupID", id, "primaryID", primaryID, "hash", hash)
		}
	}
}

// ApplyMetadataSystemTags writes the metadata:source:* and
// metadata:language:* system tags for a book. Logs but doesn't
// propagate errors — tagging is provenance metadata, not part
// of the apply transaction, so a tag write failure shouldn't
// fail the apply itself.
func (mfs *Service) ApplyMetadataSystemTags(bookID, sourceName, language string) {
	sourceTag := MetadataSourceTag(sourceName)
	if sourceTag != "" {
		if err := database.EnsureSingletonBookTag(
			mfs.db, bookID, "metadata:source:", sourceTag, "system",
		); err != nil {
			slog.Warn("failed to tag book with", "id", bookID, "value", sourceTag, "error", err)
		}
	}
	langTag := MetadataLanguageTag(language)
	if langTag != "" {
		if err := database.EnsureSingletonBookTag(
			mfs.db, bookID, "metadata:language:", langTag, "system",
		); err != nil {
			slog.Warn("failed to tag book with", "id", bookID, "value", langTag, "error", err)
		}
	}
}

// MarkNoMatch marks a book as having no metadata match.
func (mfs *Service) MarkNoMatch(id string) error {
	book, err := mfs.db.GetBookByID(id)
	if err != nil || book == nil {
		return fmt.Errorf("audiobook not found")
	}

	status := "no_match"
	book.MetadataReviewStatus = &status
	_, err = mfs.db.UpdateBook(id, book)
	return err
}
