// file: internal/metafetch/service_apply.go
// version: 1.14.0
// guid: 6ca469ca-7d2e-4738-b6f1-ae09449ed9e4
// last-edited: 2026-09-12

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
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/policy"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// renderableCoverURL decides what cover_url to persist during an apply.
//
// applied is what ApplyMetadataToBook just wrote; when the candidate carried a
// cover that is the candidate's REMOTE url. The UI serves covers through
// /api/v1/covers/proxy, which rejects hosts outside its allow-list, so a remote
// url renders as NOTHING. Return the previous (local, working) cover instead and
// let the background download repoint it once the image is on disk.
//
// If the applied value is anything other than that remote url — a local path, or
// unchanged because the candidate had no cover — keep it.
func renderableCoverURL(previous, applied *string, candidateRemote string) *string {
	if candidateRemote == "" || applied == nil {
		return applied
	}
	if *applied == candidateRemote {
		return previous
	}
	return applied
}

// ApplyMetadataToBook applies fetched metadata to an in-memory book, honoring
// the user's field locks. It returns the lock keys that were skipped because
// the user has spoken for them (database.UserLockableFields), or an error if the
// locks could not be read -- in which case NOTHING is applied. Callers that
// persist the book afterwards must check the error; a lock-read failure is not
// "nothing locked".
//
// History recording is the caller's job (see guardedApply for the path that
// records it); every other apply behavior lives in applyMetadataUnguarded.
func (mfs *Service) ApplyMetadataToBook(book *database.Book, meta metadata.BookMetadata) ([]string, error) {
	_, skipped, err := mfs.guardedApply(book, meta, "")
	return skipped, err
}

// applyMetadataUnguarded is the field-by-field apply body. It trusts that meta
// has ALREADY had locked fields stripped (StripLockedFields) -- never call it
// from outside guardedApply.
func (mfs *Service) applyMetadataUnguarded(book *database.Book, meta metadata.BookMetadata) {
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
		book.Publisher = new(meta.Publisher)
	}
	if meta.Language != "" && IsBetterStringPtr(book.Language, meta.Language) {
		book.Language = new(meta.Language)
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
			book.AudiobookReleaseYear = new(meta.PublishYear)
		} else if book.PrintYear == nil || *book.PrintYear == 0 {
			book.PrintYear = new(meta.PublishYear)
		}
	}
	if meta.CoverURL != "" {
		book.CoverURL = new(meta.CoverURL)
	}
	if meta.Narrator != "" && !IsGarbageValue(meta.Narrator) && IsBetterStringPtr(book.Narrator, meta.Narrator) {
		book.Narrator = new(meta.Narrator)
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

	// Apply ISBN/ASIN. Providers now populate ISBN10/ISBN13 separately; fall back
	// to the single ISBN (length-split) for any provider/path that set only that,
	// so both identifier columns can be filled instead of just one.
	if isbn13 := firstNonEmpty(meta.ISBN13, isbnOfLen(meta.ISBN, 13)); isbn13 != "" {
		book.ISBN13 = &isbn13
	}
	if isbn10 := firstNonEmpty(meta.ISBN10, isbnOfLen(meta.ISBN, 10)); isbn10 != "" {
		book.ISBN10 = &isbn10
	}
	if meta.ASIN != "" {
		book.ASIN = new(meta.ASIN)
	}
	if meta.Description != "" {
		book.Description = new(meta.Description)
	}
	if meta.Genre != "" {
		book.Genre = new(meta.Genre)
	}

	// Content-matcher SIGNAL fields (see the Book struct): captured from provider
	// data previously dropped, used to identify/match — not authoritative served
	// values. Set when the provider reported them.
	if meta.Abridged != nil {
		abridged := *meta.Abridged
		book.Abridged = &abridged
	}
	if meta.Subtitle != "" {
		subtitle := meta.Subtitle
		book.Subtitle = &subtitle
	}
	if meta.PageCount > 0 {
		pageCount := meta.PageCount
		book.PageCount = &pageCount
	}
	if meta.SeriesSecondary != "" && !IsGarbageValue(meta.SeriesSecondary) {
		secondary := meta.SeriesSecondary
		book.SeriesSecondary = &secondary
		if meta.SeriesSecondaryPosition != "" {
			secondaryPos := meta.SeriesSecondaryPosition
			book.SeriesSecondaryPosition = &secondaryPos
		}
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
	}

	// Series position. Applied with a valid series name, OR on its own when the
	// candidate carries no series name and the book already has a series -- the
	// "series_position" apply field selected without "series". It used to sit
	// inside the series-name block, so a lone series_position selection wrote
	// nothing. A garbage series name still suppresses it: a position that came
	// with a series we refused is not trusted on its own.
	validSeries := meta.Series != "" && !IsGarbageValue(meta.Series)
	positionOnly := meta.Series == "" && book.SeriesID != nil
	if meta.SeriesPosition != "" && (validSeries || positionOnly) {
		// Preserve the raw position (incl. decimals like "1.5") as a SIGNAL —
		// the served *int SeriesSequence below cannot hold it. Set
		// unconditionally so a fractional position is captured even though the
		// Atoi best-effort below drops it.
		raw := meta.SeriesPosition
		book.SeriesPositionRaw = &raw
		if pos, err := strconv.Atoi(meta.SeriesPosition); err == nil {
			book.SeriesSequence = &pos
		}
	}
}

// isbnOfLen returns s only when it is exactly n characters long, else "". Used to
// length-classify a single ISBN into its ISBN-10 / ISBN-13 column.
func isbnOfLen(s string, n int) string {
	if len(s) == n {
		return s
	}
	return ""
}

// firstNonEmpty returns the first non-empty argument, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// displayOrNone renders a value for a human-readable activity summary. An empty
// old value would otherwise leave a dangling arrow ("Applied narrator:  → Bob"),
// which reads as a rendering bug rather than as "there was no previous value".
func displayOrNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
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

	// Activity rows are read out of context in the unified activity feed, so the
	// summary has to name the book itself; the BookID field only gives the UI a
	// link target. Fall back to the ID when the title is empty so the line never
	// starts with a bare ": Applied ...".
	activityTitle := book.Title
	if activityTitle == "" {
		activityTitle = book.ID
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
				Summary: fmt.Sprintf("%s: Applied %s: %s → %s", activityTitle, c.field, displayOrNone(truncateActivity(c.oldVal, 50)), truncateActivity(c.newVal, 50)),
				Details: map[string]any{"field": c.field, "old_value": c.oldVal, "new_value": c.newVal, "source": sourceName},
			})
		}
	}
}

// syncMetadataToLibraryCopy copies metadata fields from the original book to
// the library copy so that both DB records stay in sync. This is needed because
// ApplyMetadataCandidate only updates the original book's DB record, leaving
// the library copy with stale metadata.
//
// FIELD LOCKS. The library copy is a SEPARATE book row with its own id, so it
// carries its OWN lock rows -- a user who edited the copy locked the copy, not
// the original. Copying the original's columns over it wholesale is a write to
// the copy like any other and goes through the same chokepoint. Without this,
// a fetch that the original's locks correctly refused could still land on the
// copy by the back door, and the two rows would then disagree about a field
// the user had locked.
//
// Fails closed: an unreadable lock set skips the whole sync rather than
// overwriting the copy blind. The copy stays stale, which the next successful
// apply fixes; an overwritten user edit is not recoverable.
func (mfs *Service) syncMetadataToLibraryCopy(original, libCopy *database.Book) {
	restored, lockErr := database.ApplyRespectingLocks(mfs.db, libCopy, func(b *database.Book) {
		mfs.copyMetadataColumns(original, b)
	})
	if lockErr != nil {
		slog.Warn("not syncing metadata to the library copy: its field locks are unreadable",
			"libCopyID", libCopy.ID, "error", lockErr)
		return
	}
	lockedOnCopy := map[string]bool{}
	for _, key := range restored {
		lockedOnCopy[key] = true
	}
	if len(restored) > 0 {
		slog.Info("library-copy sync left the copy's user-locked fields alone",
			"libCopyID", libCopy.ID, "fields", restored)
	}

	if _, err := mfs.db.UpdateBook(libCopy.ID, libCopy); err != nil {
		slog.Warn("failed to sync metadata to library copy", "id", libCopy.ID, "error", err)
	} else {
		slog.Info("synced metadata from to library copy", "originalID", original.ID, "libCopyID", libCopy.ID)
	}

	// Author/narrator associations are the join-table spelling of the
	// author_name / narrator columns, so the same lock governs them. Copying
	// the original's rows over a locked copy would undo through the join table
	// exactly what the guard above just preserved on the column.
	if !lockedOnCopy[database.FieldKeyAuthorName] {
		if authors, err := mfs.db.GetBookAuthors(original.ID); err == nil && len(authors) > 0 {
			var newAuthors []database.BookAuthor
			for _, ba := range authors {
				newAuthors = append(newAuthors, database.BookAuthor{
					BookID: libCopy.ID, AuthorID: ba.AuthorID, Role: ba.Role, Position: ba.Position,
				})
			}
			_ = mfs.db.SetBookAuthors(libCopy.ID, newAuthors)
		}
	}

	if !lockedOnCopy[database.FieldKeyNarrator] {
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
}

// copyMetadataColumns is the field list syncMetadataToLibraryCopy copies. Split
// out so the whole copy can be handed to the lock guard as one mutation.
// Preserves the library copy's own file/path/version fields by omitting them.
func (mfs *Service) copyMetadataColumns(original, libCopy *database.Book) {
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

	// Work identity, ratings and the collection fields. These are NOT
	// decoration: UserRating* is what the user typed and exists nowhere
	// else, and WorkID is what joins this book to its other editions.
	// Until 2026-09-02 ensureLibraryCopy built the library copy with a full
	// struct copy (newBook := *book), so they came along for free; it now
	// builds it through organizer.CreateOrganizedVersion, whose explicit
	// field list omits them. They are carried here rather than there
	// because CreateOrganizedVersion's omissions are a wider, pre-existing
	// gap on the main organize path -- widening its list is a change to
	// every organized version, not to this one caller.
	//
	// THEY GO IN THIS FUNCTION, NOT IN syncMetadataToLibraryCopy. #3054 made
	// this field list the body handed to database.ApplyRespectingLocks, so an
	// assignment here is refused when the user has locked that column and an
	// assignment in the caller is not. Today that is a distinction without a
	// difference for these thirteen: UserLockableFields has no key for a
	// rating, WorkID, Quantity or VersionNotes, so no lock can refuse them and
	// either placement behaves identically -- do NOT read a passing test as
	// proof the placement is load-bearing, because no test can currently tell
	// the two apart. It is still the right home: the guard covers this list by
	// construction, so the day a rating becomes lockable it is protected
	// without anyone remembering this block exists. Splitting the list in two
	// is how that day goes wrong.
	libCopy.WorkID = original.WorkID
	libCopy.AudibleRatingOverall = original.AudibleRatingOverall
	libCopy.AudibleRatingPerformance = original.AudibleRatingPerformance
	libCopy.AudibleRatingStory = original.AudibleRatingStory
	libCopy.AudibleRatingCount = original.AudibleRatingCount
	libCopy.GoogleRatingAverage = original.GoogleRatingAverage
	libCopy.GoogleRatingCount = original.GoogleRatingCount
	libCopy.UserRatingOverall = original.UserRatingOverall
	libCopy.UserRatingStory = original.UserRatingStory
	libCopy.UserRatingPerformance = original.UserRatingPerformance
	libCopy.UserRatingNotes = original.UserRatingNotes
	libCopy.Quantity = original.Quantity
	libCopy.VersionNotes = original.VersionNotes
}

// ensureLibraryCopy returns a book record with files in the library folder.
// If the book is already in the library, returns it as-is. If the book is in a
// protected path (iTunes/import), looks for an existing library version or
// organizes (hard-links) the file(s) to the library and creates a new version record.
//
// The copy is made by the organize service's OrganizeOneBook +
// CreateOrganizedVersion — the same path "Organize Library" and the post-scan
// hook use — not by a private re-implementation. Until 2026-09-02 this
// function carried its own: it called OrganizeBookDirectory / OrganizeBook
// directly, so it never saw Landing.Created and could not remove the copies it
// had just written when its own row writes failed; it created the book_file
// rows one CreateBookFile at a time with each error only logged; and it then
// demoted the ORIGINAL to non-primary unconditionally, so a failed row write
// produced a version group whose primary owned no audio while the row that
// still had the files was marked superseded. CreateOrganizedVersion writes the
// rows atomically, rolls back the row and the created files on any failure,
// and leaves the original untouched unless everything landed.
//
// Returned records carry the display metadata the caller most recently
// applied: syncMetadataToLibraryCopy runs on a freshly created copy here, so
// a caller that forgets to run it (three of the four do) gets Description,
// Genre and MetadataReviewStatus on the new copy rather than the narrower
// field set CreateOrganizedVersion clones.
func (mfs *Service) ensureLibraryCopy(book *database.Book) *database.Book {
	if target, ok := mfs.existingLibraryCopy(book); ok {
		return target
	}

	log := logger.New("metafetch-library-copy")
	orgSvc := mfs.libraryOrganizeService()
	org := organizer.NewOrganizer(&config.AppConfig)

	// OrganizeOneBook owns the directory / single-file decision (it reads the
	// book_file rows and stats FilePath), so this function no longer has a
	// copy of it that can disagree with the one CreateOrganizedVersion
	// validates against.
	landing, err := orgSvc.OrganizeOneBook(org, book, log)
	if err != nil {
		slog.Warn("failed to create library copy for protected book", "id", book.ID, "error", err)
		return nil
	}
	if landing.InPlace {
		// Unreachable by construction — an in-place landing is only produced
		// for a book already under RootDir, and those returned above. If it
		// ever happens there is no copy to hand back, and versioning an
		// in-place landing is exactly what CreateOrganizedVersion refuses.
		slog.Error("library copy: protected book landed in place; refusing to version it", "id", book.ID, "path", landing.Path)
		return nil
	}

	// No operation id: this copy is a side effect of a metadata apply or
	// write-back, not an organize operation the user can undo as such.
	created, err := orgSvc.CreateOrganizedVersion(book, landing, "", log)
	if err != nil {
		// CreateOrganizedVersion has already removed the files it wrote and
		// the row it created, and has NOT demoted the original.
		slog.Warn("failed to create library book record for protected book", "id", book.ID, "error", err)
		return nil
	}

	mfs.syncMetadataToLibraryCopy(book, created)

	slog.Info("created library copy for protected book", "path", created.FilePath, "newBookID", created.ID, "originalBookID", book.ID)
	return created
}

// libraryOrganizeService is the organize service ensureLibraryCopy routes
// through, built once from this service's own store. It is constructed here
// rather than injected because every metafetch store already satisfies
// organizer.Store (Store embeds it), and a second wiring path would be one more
// place for the two to disagree. ApplyOrganizedFileMetadata and
// ComputeITunesPath match what audiobooks.NewOrganizeService installs on the
// production organize service, so a copy made here is indistinguishable from
// one made by "Organize Library".
func (mfs *Service) libraryOrganizeService() *organizer.Service {
	mfs.organizeOnce.Do(func() {
		svc := organizer.NewService(mfs.db)
		svc.ApplyOrganizedFileMetadata = scanner.ApplyOrganizedFileMetadata
		svc.ComputeITunesPath = ComputeITunesPath
		mfs.organizeSvc = svc
	})
	return mfs.organizeSvc
}

// persistFetchedMetadata records "what the provider said" as the fetched_value
// of each field-state row. The rows come from ApplyFields, the same list the
// apply allowlist uses, so every field an apply can write gets provenance.
// It used to be a hand-written list of eight fields that had drifted from the
// apply: narrator, series, description, genre, subtitle, runtime and the rest
// were written to the book with no fetched_value recorded.
func (mfs *Service) persistFetchedMetadata(bookID string, meta metadata.BookMetadata) {
	fetchedValues := FetchedProvenance(meta)
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
		ISBN10:         candidate.ISBN10,
		ISBN13:         candidate.ISBN13,
		ASIN:           candidate.ASIN,
		Genre:          candidate.Genre,
		CoverURL:       candidate.CoverURL,
		Description:    candidate.Description,
		Language:       candidate.Language,
		DurationSec:    candidate.DurationSec,
		// Content-matcher SIGNAL fields — carried so a manual apply persists them
		// too, matching the auto-fetch path.
		Abridged:                candidate.Abridged,
		Subtitle:                candidate.Subtitle,
		PageCount:               candidate.PageCount,
		SeriesSecondary:         candidate.SeriesSecondary,
		SeriesSecondaryPosition: candidate.SeriesSecondaryPosition,
		// candidate.Year is a bare int with no kind attached; derive whether it
		// is an audiobook release year (Audible/Audnexus) from the source name so
		// ApplyMetadataToBook routes it to the same field as the auto-fetch path.
		PublishYearIsAudiobookRelease: metadata.SourceProducesAudiobookReleaseYear(candidate.Source),
	}

	// If fields is non-empty, zero out every field NOT in it. The allowlist is
	// ApplyFields (apply_fields.go) -- the same list provenance is recorded
	// from, and the list the web apply dialogs render -- so a field the UI can
	// deselect can never be written anyway. Until 2026-09-12 this was a
	// hand-written block that knew ten names: "series_position" (sent by the
	// UI) had no branch, unchecking "isbn" left ISBN10/ISBN13 to be written,
	// and subtitle/abridged/page count/secondary series/runtime were written
	// whatever the user selected.
	meta = FilterApplyFields(meta, fields)

	// Strip embedded "Series Name, Book N" before persisting — protects
	// against Audible/Audnexus candidates where the book number is baked
	// into the series name. Same normalization the auto-fetch paths run.
	NormalizeMetaSeries(&meta)

	// Remember the cover we are currently serving. ApplyMetadataToBook overwrites
	// book.CoverURL with the candidate's REMOTE url, which is not renderable by
	// the UI: it proxies covers through /api/v1/covers/proxy, which rejects hosts
	// outside its allow-list ("URL not from an allowed cover source", observed as
	// a 400 for m.media-amazon.com).
	//
	// That was harmless while the download ran inline, because the remote value
	// was replaced with the local path microseconds later and never observed.
	// Once the download moved to the background it became visible: the apply
	// response carried the remote url, the UI proxied it, got a 400, and the
	// cover went BLANK until the background download finished and a refresh
	// picked up the local path.
	previousCoverURL := book.CoverURL

	// guardedApply strips the user's locked fields, records change history for
	// what will actually change, and applies. fetched keeps the UNSTRIPPED
	// candidate so provenance below still records what the provider said, which
	// is exactly what the UI's "fetched vs override" panel exists to show.
	fetched := meta
	meta, skippedLocked, err := mfs.guardedApply(book, meta, candidate.Source)
	if err != nil {
		return nil, err
	}

	// Keep serving the previous cover until the new one is actually on disk.
	// DownloadPendingCover repoints this at the local path when it completes.
	book.CoverURL = renderableCoverURL(previousCoverURL, book.CoverURL, meta.CoverURL)

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
	// A nil book with a nil error violates the contract every real Store honors
	// (PebbleStore returns an error on every path that yields no book), but
	// database.MockStore returns (nil, nil) whenever UpdateBookFunc is unset. That
	// made this an outright panic at updatedBook.Title below rather than anything
	// diagnosable. Fail loudly instead: a store that reports success while handing
	// back nothing is a bug wherever it happens, not a case to route around.
	if updatedBook == nil {
		return nil, fmt.Errorf("update book %s: store reported success but returned no book", id)
	}

	// Check whether any other book already carries the same hash — if so,
	// emit a dedup candidate so the user can review the potential duplicate.
	// The apply itself has already succeeded, so a failed duplicate check is
	// logged at Error level and does not fail the apply: the next apply of any
	// book in the cluster re-runs the election, and nothing was demoted here.
	if book.MetadataSourceHash != nil {
		if err := mfs.checkMetadataSourceHashDuplicates(id, *book.MetadataSourceHash); err != nil {
			slog.Error("MATCH-4 auto-merge skipped: primary election aborted on a read error; no book was demoted",
				"id", id, "hash", *book.MetadataSourceHash, "error", err)
		}
	}

	// Persist fetched values for provenance tracking. This is the full candidate,
	// including locked fields that were NOT applied: the state row's fetched_value
	// is "what the provider said", the override is what the user said, and the
	// UI shows both side by side.
	mfs.persistFetchedMetadata(id, fetched)

	// Generate segment titles (fast, DB-only)
	if err := mfs.generateSegmentTitles(id, updatedBook.Title); err != nil {
		slog.Warn("generate segment titles failed for", "id", id, "error", err)
	}

	// Cover art is downloaded in the BACKGROUND — see pendingCover below.
	//
	// It used to run inline, with a comment calling it a "fast network fetch".
	// Measured on production 2026-08-15, applying metadata to one book: the
	// request took 6.44s end to end and ~4s of that was this download. Everything
	// else the apply path does (tags, rename, write-back) was already backgrounded
	// through the file-IO pool, so this single call was the majority of the wait
	// a user sees after clicking Apply.
	//
	// The response no longer carries the final local cover_url. That is the
	// deliberate trade: the caller gets the book back immediately and the cover
	// appears a moment later. FetchMetadataResponse.PendingCoverURL has existed
	// (documented as "set by ApplyMetadataCandidate for background download")
	// since before this change but was never set or read by anything — the idea
	// predates the measurement; only the wiring was missing.
	pendingCover := ""
	if meta.CoverURL != "" && config.AppConfig.RootDir != "" {
		pendingCover = meta.CoverURL
	}

	// Queue background ISBN/ASIN enrichment if identifiers are missing.
	// No nil check: the guard above already rejected a nil book, and this one
	// sitting AFTER an unguarded updatedBook.Title deref was what staticcheck
	// flagged (SA5011) -- it implied a nil was possible at a point that would
	// already have panicked.
	mfs.queueISBNEnrichment(id, updatedBook)

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
		Message:             "metadata candidate applied",
		Book:                updatedBook,
		Source:              candidate.Source,
		PendingCoverURL:     pendingCover,
		SkippedLockedFields: skippedLocked,
	}, nil
}

// DownloadPendingCover fetches a candidate's cover art and points the book at
// the local copy. Intended to run in the background off the request path — see
// the comment in ApplyMetadataCandidate for why.
//
// Safe to call with an empty URL (no-op) and safe to call concurrently for
// different books; the cover is written to a per-book path.
func (mfs *Service) DownloadPendingCover(bookID, coverURL string) {
	mfs.saveCover(bookID, coverURL, true)
}

// downloadAutoFetchCover is DownloadPendingCover for auto-fetch: a book that
// already has a local cover keeps it, and the fetched cover is downloaded only
// when there is none. That is auto-fetch's behaviour before 2026-09-12, when
// it called metadata.DownloadCoverArt (which returns an existing file
// untouched); the apply-writes change had briefly routed it through the
// replace path too. Nobody picked a new cover on this path, so a provider's
// first result must not overwrite one the user chose. An explicit apply
// (DownloadPendingCover) still replaces.
func (mfs *Service) downloadAutoFetchCover(bookID, coverURL string) {
	mfs.saveCover(bookID, coverURL, false)
}

// saveCover is the body of DownloadPendingCover (replace) and
// downloadAutoFetchCover (!replace, which keeps an existing cover file).
func (mfs *Service) saveCover(bookID, coverURL string, replace bool) {
	if bookID == "" || coverURL == "" || config.AppConfig.RootDir == "" {
		return
	}

	coverPath := ""
	if !replace {
		coverPath = metadata.CoverPathForBook(config.AppConfig.RootDir, bookID)
	}
	if coverPath != "" {
		slog.Info("auto-fetch: book already has a local cover; kept it", "path", coverPath, "id", bookID)
	} else {
		// ReplaceCoverArt, not DownloadCoverArt: on an explicit apply of a new
		// cover, DownloadCoverArt returns an existing cover file untouched, so a
		// book that already had a cover kept the old image forever.
		// ReplaceCoverArt writes a temp file and renames it over the old one only
		// once the new image is complete, so a failed download leaves the old
		// cover in place. (On the auto-fetch path no cover exists here.)
		download := metadata.ReplaceCoverArt
		if mfs.coverDownload != nil {
			download = mfs.coverDownload
		}
		var err error
		coverPath, err = download(coverURL, config.AppConfig.RootDir, bookID)
		if err != nil {
			slog.Warn("background cover art download failed", "id", bookID, "error", err)
			return
		}
		slog.Info("cover art saved to", "path", coverPath, "id", bookID)
	}

	// Re-read rather than reusing the book the caller had: the apply path and the
	// background file-IO job both write this row, and UpdateBook does a full
	// column replacement, so writing a stale struct here would silently revert
	// whatever landed in between.
	book, err := mfs.db.GetBookByID(bookID)
	if err != nil || book == nil {
		slog.Warn("background cover art: book vanished before cover_url update", "id", bookID, "error", err)
		return
	}

	localCoverURL := "/api/v1/covers/local/" + filepath.Base(coverPath)
	book.CoverURL = &localCoverURL
	if _, err := mfs.db.UpdateBook(bookID, book); err != nil {
		slog.Warn("background cover art: failed to update cover_url", "id", bookID, "error", err)
	}
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
//
// Every read the election depends on is completed BEFORE any book is
// demoted, and a failure on any of them aborts the whole cluster with an
// error and no write. Until 2026-09-11 a GetBookFiles error was scored as
// zero files — the same score a book with no files gets — so a transient
// read error on the book that actually had the most files made it lose the
// election, and every other book in the cluster (itself included) was then
// demoted to a primary chosen on bad data, with no log line saying why.
func (mfs *Service) checkMetadataSourceHashDuplicates(bookID, hash string) error {
	matches, err := mfs.db.GetBooksByMetadataSourceHash(hash)
	if err != nil {
		return fmt.Errorf("MATCH-4 metadata-source-hash lookup for book %s: %w", bookID, err)
	}

	// Build the full set of non-merged books sharing this hash; ensure bookID is included.
	allMap := make(map[string]database.Book, len(matches)+1)
	for _, b := range matches {
		allMap[b.ID] = b
	}
	if _, ok := allMap[bookID]; !ok {
		self, err := mfs.db.GetBookByID(bookID)
		if err != nil {
			return fmt.Errorf("MATCH-4 load book %s for the primary election: %w", bookID, err)
		}
		if self != nil {
			allMap[bookID] = *self
		}
	}

	if len(allMap) < 2 {
		return nil // no duplicates
	}

	// Score first, decide second: a read error on ANY candidate aborts the
	// election before a primary is chosen, so it can never be mistaken for a
	// book that legitimately has zero files.
	fileCounts := make(map[string]int, len(allMap))
	for id := range allMap {
		files, err := mfs.db.GetBookFiles(id)
		if err != nil {
			return fmt.Errorf("MATCH-4 count files for book %s in hash cluster %s: %w", id, hash, err)
		}
		fileCounts[id] = len(files)
	}

	// Pick primary: book with the most book_files. On tie, prefer earlier created_at.
	primaryID := bookID
	maxFiles := -1
	for id, b := range allMap {
		n := fileCounts[id]
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
	return nil
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

// existingLibraryCopy resolves the row file work may touch WITHOUT creating
// anything. ok=false means book is protected (iTunes/import) and has no usable
// library copy; ensureLibraryCopy then creates one, and the auto-fetch path
// (existingCopyOnly) skips the file work instead.
func (mfs *Service) existingLibraryCopy(book *database.Book) (*database.Book, bool) {
	root := config.AppConfig.RootDir
	if root == "" {
		return book, true // no library configured
	}
	if pathUnderRoot(book.FilePath, root) {
		return book, true // already in library
	}
	if !mfs.isProtectedPath(book.FilePath) {
		return book, true // not protected, safe to modify
	}
	if sib := mfs.librarySibling(book); sib != nil {
		slog.Info("using existing library copy for protected book", "siblingID", sib.ID, "bookID", book.ID)
		return sib, true
	}
	return nil, false
}

// librarySibling returns a version-group sibling that is a real library copy:
// its book path is under root_dir AND none of its present book_file rows point
// into a protected tree. The second check is new. A sibling whose book path
// was under the library while a file row still pointed into the iTunes tree
// used to be handed back as "the library copy", and the apply pipeline then
// renamed every one of its rows -- including the iTunes file.
func (mfs *Service) librarySibling(book *database.Book) *database.Book {
	if book.VersionGroupID == nil || *book.VersionGroupID == "" {
		return nil
	}
	siblings, err := mfs.db.GetBooksByVersionGroup(*book.VersionGroupID)
	if err != nil {
		return nil
	}
	for i := range siblings {
		sib := &siblings[i]
		if sib.ID == book.ID || !pathUnderRoot(sib.FilePath, config.AppConfig.RootDir) {
			continue
		}
		if p := mfs.firstProtectedFileRow(sib.ID); p != "" {
			slog.Warn("version sibling under root_dir still has a file row under a protected path; not using it as the library copy",
				"siblingID", sib.ID, "bookID", book.ID, "protected_path", p)
			continue
		}
		return sib
	}
	return nil
}

// firstProtectedFileRow returns the first present book_file path of bookID
// that lies under a protected tree, or "". A listing error returns "" (the
// sibling stays usable): every file-touching leg re-checks each path itself
// -- the rename leg through dropProtectedRenameEntries, the embed and tag legs
// through their own isProtectedPath checks -- so this is the second line of
// defence, and refusing the sibling on a transient read error would make the
// user-initiated apply create a duplicate library copy.
func (mfs *Service) firstProtectedFileRow(bookID string) string {
	files, err := mfs.db.GetBookFiles(bookID)
	if err != nil {
		slog.Warn("could not list a version sibling's files to check for protected rows", "bookID", bookID, "error", err)
		return ""
	}
	for _, bf := range files {
		if !bf.Missing && mfs.isProtectedPath(bf.FilePath) {
			return bf.FilePath
		}
	}
	return ""
}

// libraryCopyFor is ensureLibraryCopy under a copy policy: createLibraryCopy
// may create one, existingCopyOnly never does and returns nil instead.
func (mfs *Service) libraryCopyFor(book *database.Book, policy copyPolicy) *database.Book {
	if policy == existingCopyOnly {
		target, _ := mfs.existingLibraryCopy(book)
		return target
	}
	return mfs.ensureLibraryCopy(book)
}

// autoFetchHasLibraryCopy reports whether auto-fetch may do file work for
// book: only when a library copy ALREADY exists under root_dir, either the
// book itself or a clean version sibling of a protected book. Auto-fetch never
// creates a library copy (owner decision, 2026-09-12): the iTunes importer and
// the organize pass reach it for books nobody asked to copy.
func (mfs *Service) autoFetchHasLibraryCopy(book *database.Book) bool {
	root := config.AppConfig.RootDir
	if root == "" {
		return false
	}
	if pathUnderRoot(book.FilePath, root) {
		return true
	}
	return mfs.isProtectedPath(book.FilePath) && mfs.librarySibling(book) != nil
}
