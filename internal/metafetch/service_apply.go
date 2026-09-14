// file: internal/metafetch/service_apply.go
// version: 1.32.0
// guid: 6ca469ca-7d2e-4738-b6f1-ae09449ed9e4
// last-edited: 2026-09-14

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
	_, skipped, _, err := mfs.guardedApply(book, meta, "")
	return skipped, err
}

// applyMetadataUnguarded is the field-by-field apply body. It trusts that meta
// has ALREADY had locked fields stripped (StripLockedFields) -- never call it
// from outside guardedApply.
//
// The candidate's author is ADDED to the book_authors join, never replacing it
// (see applyAuthorCredit). It returns an error only when the author join could
// not be read or written; the book struct may then be partly mutated and the
// caller must not persist it.
// The returned credits are the author join applyAuthorCredit read and wrote
// under the store's lock, nil when no author was applied.
func (mfs *Service) applyMetadataUnguarded(book *database.Book, meta metadata.BookMetadata) (*AuthorCredits, error) {
	var credits *AuthorCredits
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

	// Apply author: resolve the name to an AuthorID and ADD it to the
	// book_authors join (applyAuthorCredit). An apply never removes a credit.
	extractedAuthor := meta.Author
	if extractedAuthor != "" && !IsGarbageValue(extractedAuthor) {
		// Guard: if extracted artist matches the book's narrator (not the author),
		// the tag has narrator in the artist field — keep the DB author.
		if book.AuthorID != nil && book.Narrator != nil {
			if existingAuthor, aErr := mfs.db.GetAuthorByID(*book.AuthorID); aErr == nil && existingAuthor != nil {
				if strings.EqualFold(extractedAuthor, *book.Narrator) && !strings.EqualFold(extractedAuthor, existingAuthor.Name) {
					slog.Info("applyMetadataToBook extracted artist matches narrator but not author for book — skipping author update", "extracted_author", logger.SanitizeLogValue(extractedAuthor), "narrator", logger.SanitizeLogValue(*book.Narrator), "name", existingAuthor.Name, "id", book.ID)
					extractedAuthor = ""
				} else if !strings.EqualFold(extractedAuthor, existingAuthor.Name) && !strings.EqualFold(extractedAuthor, *book.Narrator) {
					// Extracted artist doesn't match either stored author or narrator — log mismatch for review
					slog.Warn("applyMetadataToBook extracted artist matches neither author nor narrator for book", "extracted_author", logger.SanitizeLogValue(extractedAuthor), "name", existingAuthor.Name, "narrator", logger.SanitizeLogValue(*book.Narrator), "id", book.ID)
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
			c, aerr := mfs.applyAuthorCredit(book, author.ID)
			if aerr != nil {
				return nil, aerr
			}
			credits = c
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
	return credits, nil
}

// applyAuthorCredit puts the candidate's resolved author onto the book.
//
// A candidate carries ONE author string. Until 2026-09-14 every apply wrote it
// as a one-row join, replacing the credits: a book credited to [A, B] that got
// a candidate naming A came out credited to [A] alone, from auto-fetch, the
// batch applies and the hand-picked single apply alike, and nothing logged it.
//
// Author apply is ADD-ONLY on every path (owner decision 2026-09-14); removing
// an author is a manual edit, never a side effect of an apply. Existing links
// are never removed. The candidate's author is appended at the next position
// if it is not already credited; book.AuthorID is set only when empty, so the
// primary author is not repointed.
//
// A read or write failure is returned, never discarded: the merge cannot tell
// what it would drop without the current join, so it fails closed.
//
// It returns the join it read and the join it left, both under the lock, for
// history: undo removes exactly what this call added.
func (mfs *Service) applyAuthorCredit(book *database.Book, authorID int) (*AuthorCredits, error) {
	// The read-merge-write runs inside ModifyBookAuthors, under the store's
	// book_authors stripe: two concurrent applies to one book each adding a
	// different author both land. A caller-side Get -> merge -> Set lost one
	// of them (TestApplyMetadataToBook_ConcurrentFillOnlyAppliesKeepBothAuthors).
	primary := book.AuthorID
	added, kept := false, 0
	var before []database.BookAuthor
	after, err := mfs.db.ModifyBookAuthors(book.ID, func(existing []database.BookAuthor) ([]database.BookAuthor, error) {
		before = append([]database.BookAuthor(nil), existing...)
		// A book whose primary author lives only in the author_id column (no
		// join rows) keeps it as a credit: the append must not orphan it.
		if len(existing) == 0 && primary != nil && *primary != authorID {
			existing = append(existing, database.BookAuthor{
				BookID: book.ID, AuthorID: *primary, Role: "author", Position: 0,
			})
		}
		nextPos := 0
		for _, ba := range existing {
			if ba.AuthorID == authorID {
				return nil, database.ErrSkipBookAuthorsWrite
			}
			if ba.Position >= nextPos {
				nextPos = ba.Position + 1
			}
		}
		added, kept = true, len(existing)
		return append(existing, database.BookAuthor{
			BookID: book.ID, AuthorID: authorID, Role: "author", Position: nextPos,
		}), nil
	})
	if err != nil {
		return nil, fmt.Errorf("add author credit (add-only): %w", err)
	}
	if added && kept > 0 {
		applyAuthorLog.Info("fill-only apply added author %d to book %s alongside %d existing credit(s)",
			authorID, logger.SanitizeLogValue(book.ID), kept)
	}
	if book.AuthorID == nil {
		book.AuthorID = &authorID
	}
	return &AuthorCredits{Before: before, After: after}, nil
}

// applyAuthorLog is applyAuthorCredit's logger.New printf-style logger.
var applyAuthorLog = logger.New("metafetch.apply-authors")

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

// applyMarkerLog and historyLog replace direct slog calls in this file
// (logger.New printf-style is the repo's logging API).
var (
	applyMarkerLog = logger.New("metafetch.apply")
	historyLog     = logger.New("metafetch.history")
)

// audioConfirmedMarker decides the apply's audio_confirmed marker.
//
// With nobody reviewing (ownerReviewed false: auto-fetch, metadata.upgrade,
// an unpinned batch apply, a manual apply) it is the marker's rule on
// origin/main before 2026-09-13 (legacyAudioConfirmedMarker) AND the shared
// matcher, so it never marks a pair main did not. On an owner-reviewed row
// apply the shared matcher alone may annotate it: a human has looked at the
// book, and the marker then records that the audio agrees with their choice.
func audioConfirmedMarker(candidate MetadataCandidate, th transcriptionHints, ownerReviewed bool) bool {
	if th.empty() || th.title == "" {
		return false
	}
	shared := util.TitleAgrees(candidate.Title, candidate.SeriesPosition, th.title) &&
		util.AuthorAgrees(candidate.Author, th.author)
	if ownerReviewed {
		return shared
	}
	return shared && legacyAudioConfirmedMarker(candidate, th)
}

// legacyAudioConfirmedMarker is the audio_confirmed rule as it stood on
// origin/main: normalized title equality, then a transcribed author of more
// than three characters as a case-insensitive substring of the candidate's.
func legacyAudioConfirmedMarker(candidate MetadataCandidate, th transcriptionHints) bool {
	if util.NormalizeTitle(th.title) != util.NormalizeTitle(candidate.Title) {
		return false
	}
	return th.author == "" || len(th.author) <= 3 || containsCI(candidate.Author, th.author)
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
//
// The copy is written with ModifyBook: the original's columns are laid onto
// the copy's row as it stands under its write stripe, not onto the caller's
// earlier read of it, so a concurrent write to a column this sync does not
// own is kept. The lock rows are read inside the callback; that is a Pebble
// read of lock keys, not a book write, so it is safe under the stripe.
func (mfs *Service) syncMetadataToLibraryCopy(original, libCopy *database.Book) {
	var restored []string
	var lockErr error
	updated, err := mfs.db.ModifyBook(libCopy.ID, func(row *database.Book) error {
		restored, lockErr = database.ApplyRespectingLocks(mfs.db, row, func(b *database.Book) {
			mfs.copyMetadataColumns(original, b)
		})
		return lockErr
	})
	if lockErr != nil {
		slog.Warn("not syncing metadata to the library copy: its field locks are unreadable",
			"libCopyID", libCopy.ID, "error", lockErr)
		return
	}
	switch {
	case err != nil:
		slog.Warn("failed to sync metadata to library copy", "id", libCopy.ID, "error", err)
	case updated == nil:
		logger.New("metafetch-library-copy").Warn("library copy %s vanished before the metadata sync", logger.SanitizeLogValue(libCopy.ID))
		return
	default:
		// Keep the caller's struct in step with what was written, as the
		// in-place mutation did before.
		*libCopy = *updated
		slog.Info("synced metadata from to library copy", "originalID", original.ID, "libCopyID", libCopy.ID)
	}
	lockedOnCopy := map[string]bool{}
	for _, key := range restored {
		lockedOnCopy[key] = true
	}
	if len(restored) > 0 {
		slog.Info("library-copy sync left the copy's user-locked fields alone",
			"libCopyID", libCopy.ID, "fields", restored)
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

// ensureLibraryCopy makes the library copy of a book in a protected path
// (iTunes/import) that has none: it organizes (hard-links) the file(s) to the
// library and creates a new version record. Its one caller, lockLibraryCopy,
// has already looked for a copy under the version-group key and found none.
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
//
// VERSION-GROUP LOCK. The caller holds the book's version-group key
// (lockVersionGroup), passes the book as re-read under it, and keeps holding
// it until this returns: through the copy's book row, its book_file rows and
// the metadata sync below, so no lookup under the key ever finds a half-made
// copy. Two applies on different versions of one book hold different book
// locks, so until 2026-09-12 both could find no copy and both make one,
// leaving two library copies in one version group. The organizer's
// CreateOrganizedVersion takes the same key for its own versions; the organize
// service used here (libraryOrganizeService) has no locker, because the key is
// already held and the lock table is not reentrant.
func (mfs *Service) ensureLibraryCopy(book *database.Book) *database.Book {
	if mfs.libraryCopyMaker != nil {
		return mfs.libraryCopyMaker(book)
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
// one made by "Organize Library". It has no VersionGroupLocker:
// ensureLibraryCopy calls CreateOrganizedVersion already holding the
// version-group key, and the lock table is not reentrant.
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
// If fields is non-empty, only the listed fields are applied. It is the
// hand-picked apply and may overwrite filled fields; batch and automatic
// callers use ApplyMetadataCandidateWithOptions with FillOnly.
func (mfs *Service) ApplyMetadataCandidate(id string, candidate MetadataCandidate, fields []string) (*FetchMetadataResponse, error) {
	return mfs.ApplyMetadataCandidateWithOptions(id, candidate, fields, ApplyOptions{})
}

// ApplyMetadataCandidateWithOptions is ApplyMetadataCandidate with opts
// deciding how the apply is recorded and, with FillOnly, that it only fills
// empty descriptive fields (see ApplyOptions).
func (mfs *Service) ApplyMetadataCandidateWithOptions(id string, candidate MetadataCandidate, fields []string, opts ApplyOptions) (*FetchMetadataResponse, error) {
	book, err := mfs.db.GetBookByID(id)
	if err != nil || book == nil {
		return nil, fmt.Errorf("audiobook not found")
	}
	// before is the row as read. The apply below mutates book (and resolves or
	// creates author/series rows on the way), then only the fields it changed
	// relative to before are merged onto the row re-read under the book's write
	// lock -- see the ModifyBook call.
	before, err := database.SnapshotBook(book)
	if err != nil {
		return nil, err
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
		slog.Warn("duration-mismatch apply book title candidate deltas (books audibles) wrong match or abridged version", "bookID", logger.SanitizeLogValue(id), "bookTitle", book.Title, "candidateTitle", logger.SanitizeLogValue(candidate.Title), "durationDeltaSec", logger.SanitizeLogValue(strconv.Itoa(candidate.DurationDeltaSec)), "bookDurationSec", bookDurSec, "candidateDurationSec", logger.SanitizeLogValue(strconv.Itoa(candidate.DurationSec)))
	}

	// candidateMetadata (apply_preview.go) is shared with the dry-run preview.
	meta := CandidateMetadata(candidate)

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
	historySource := opts.historySource(candidate.Source)
	if opts.FillOnly {
		var kept []string
		meta, kept = StripFilledFields(book, meta)
		if len(kept) > 0 {
			applyMarkerLog.Info("fill-only apply kept filled fields book_id=%s source=%s kept=%v",
				logger.SanitizeLogValue(id), logger.SanitizeLogValue(candidate.Source), kept)
		}
	}
	// credits is the author join as read and written under the store's
	// book_authors lock, so undo removes exactly what this apply added. It
	// replaces a join read taken here, outside that lock, which could miss
	// another apply's credit and so let undo delete it.
	meta, skippedLocked, credits, err := mfs.guardedApply(book, meta, historySource)
	if err != nil {
		return nil, err
	}
	if opts.OwnerReviewed {
		appendOwnerReviewedNote(book, opts.overrideLabel(), time.Now())
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
	if audioConfirmedMarker(candidate, th, opts.OwnerReviewed) {
		ac := "audio_confirmed"
		book.MetadataReviewStatus = &ac
		applyMarkerLog.Info("metadata apply: audio-confirmed match id=%s title=%s", logger.SanitizeLogValue(id), logger.SanitizeLogValue(candidate.Title))
		appendMetadataVersionNote(book, "audio_confirmed")
	}

	// Compute metadata_source_hash = sha256("{source}:{canonical_id}") so the
	// dedup engine can later detect books sharing the exact same external record.
	canonicalID := metadataCanonicalID(candidate)
	if canonicalID != "" {
		h := fmt.Sprintf("%x", sha256.Sum256([]byte(src+":"+canonicalID)))
		book.MetadataSourceHash = &h
	}

	// Write only what this apply changed, onto the row as it stands under the
	// book's write lock, then record history from the committed row
	// (CommitApply). A whole-struct UpdateBook(id, book) here replaced every
	// column with this apply's read, so a book-page save (or any other write)
	// that committed during the apply was silently reverted. A book with an
	// error means the write stands and its history did not land; CommitApply
	// logged it at Error and undo refuses that apply.
	updatedBook, updateErr := mfs.CommitApply(id, before, book, credits, historySource)
	if updatedBook == nil {
		return nil, updateErr
	}
	// An owner-reviewed apply went through past legs the certainty gate
	// refused, and its change history is the only record to audit or revert
	// it from. When that history did not land, the error is returned with the
	// response (below) so the op reports and counts it. History is written
	// after the commit, so the write itself stands; the rest of this apply
	// still runs so the book is not left half-applied.
	var historyErr error
	if opts.OwnerReviewed && updateErr != nil {
		historyErr = fmt.Errorf("owner-reviewed apply of %s: change history not recorded: %w", id, updateErr)
	}

	// Check whether any other book already carries the same hash — if so,
	// emit a dedup candidate so the user can review the potential duplicate.
	// The apply itself has already succeeded, so a failed duplicate check is
	// logged at Error level and does not fail the apply: the next apply of any
	// book in the cluster re-runs the election, and nothing was demoted here.
	if book.MetadataSourceHash != nil {
		if err := mfs.checkMetadataSourceHashDuplicates(id, *book.MetadataSourceHash); err != nil {
			slog.Error("MATCH-4 auto-merge skipped: primary election aborted on a read error; no book was demoted",
				"id", logger.SanitizeLogValue(id), "hash", *book.MetadataSourceHash, "error", logger.SanitizeLogValue(err.Error()))
		}
	}

	// Persist fetched values for provenance tracking. This is the full candidate,
	// including locked fields that were NOT applied: the state row's fetched_value
	// is "what the provider said", the override is what the user said, and the
	// UI shows both side by side.
	mfs.persistFetchedMetadata(id, fetched)

	// Generate segment titles (fast, DB-only)
	if err := mfs.generateSegmentTitles(id, updatedBook.Title); err != nil {
		slog.Warn("generate segment titles failed for", "id", logger.SanitizeLogValue(id), "error", err)
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

	// Apply provider category tags (Audible category ladders, Google Books
	// categories). These are additive enrichment — they are not controlled by
	// the fields allowlist and do not fail the apply if a tag write errors. The
	// tag source names the provider: it was hard-coded "audible_category" when
	// Audible was the only source that populated CategoryTags.
	tagSource := metadata.CategoryTagSource(candidate.Source)
	for _, tag := range candidate.CategoryTags {
		if err := mfs.db.AddBookTagWithSource(id, tag, tagSource); err != nil {
			slog.Warn("failed to apply category tag to book", "value", logger.SanitizeLogValue(tag), "id", logger.SanitizeLogValue(id), "error", err)
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
	}, historyErr
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
		slog.Info("auto-fetch: book already has a local cover; kept it", "path", logger.SanitizeLogValue(coverPath), "id", logger.SanitizeLogValue(bookID))
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
			slog.Warn("background cover art download failed", "id", logger.SanitizeLogValue(bookID), "error", logger.SanitizeLogValue(err.Error()))
			return
		}
		slog.Info("cover art saved to", "path", logger.SanitizeLogValue(coverPath), "id", logger.SanitizeLogValue(bookID))
	}

	// Set only cover_url, on the row as it stands under the book's write
	// stripe: the apply path and the background file-IO job both write this
	// row, and a whole-struct UpdateBook of an earlier read would silently
	// revert whatever landed in between. The download above runs before the
	// lock is taken.
	localCoverURL := "/api/v1/covers/local/" + filepath.Base(coverPath)
	updated, err := mfs.db.ModifyBook(bookID, func(row *database.Book) error {
		row.CoverURL = &localCoverURL
		return nil
	})
	if err != nil {
		slog.Warn("background cover art: failed to update cover_url", "id", logger.SanitizeLogValue(bookID), "error", logger.SanitizeLogValue(err.Error()))
		return
	}
	if updated == nil {
		slog.Warn("background cover art: book vanished before cover_url update", "id", logger.SanitizeLogValue(bookID))
	}
}

// appendOwnerReviewedNote records one owner-reviewed override on the book's
// version notes: "owner_reviewed <UTC time>: <overridden reasons>". Unlike
// appendMetadataVersionNote it never deduplicates: each override is a
// separate decision and must stay visible, not collapse into the first.
func appendOwnerReviewedNote(book *database.Book, overridden string, at time.Time) {
	line := "owner_reviewed " + at.UTC().Format(time.RFC3339) + ": " + overridden
	if book.VersionNotes == nil || strings.TrimSpace(*book.VersionNotes) == "" {
		book.VersionNotes = &line
		return
	}
	notes := strings.TrimSpace(*book.VersionNotes) + "\n" + line
	book.VersionNotes = &notes
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
			slog.Warn("MATCH-4 failed to flag book as duplicate of", "dupID", logger.SanitizeLogValue(id), "primaryID", logger.SanitizeLogValue(primaryID), "error", logger.SanitizeLogValue(err.Error()))
		} else {
			slog.Info("MATCH-4 auto-flagged book as merged into primary (hash )", "dupID", logger.SanitizeLogValue(id), "primaryID", logger.SanitizeLogValue(primaryID), "hash", hash)
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
			slog.Warn("failed to tag book with", "id", logger.SanitizeLogValue(bookID), "value", logger.SanitizeLogValue(sourceTag), "error", err)
		}
	}
	langTag := MetadataLanguageTag(language)
	if langTag != "" {
		if err := database.EnsureSingletonBookTag(
			mfs.db, bookID, "metadata:language:", langTag, "system",
		); err != nil {
			slog.Warn("failed to tag book with", "id", logger.SanitizeLogValue(bookID), "value", logger.SanitizeLogValue(langTag), "error", err)
		}
	}
}

// MarkNoMatch marks a book as having no metadata match.
func (mfs *Service) MarkNoMatch(id string) error {
	// One field, set on the row under the book's write stripe, so a concurrent
	// writer's change is not reverted by a whole-struct replace.
	status := "no_match"
	updated, err := mfs.db.ModifyBook(id, func(row *database.Book) error {
		row.MetadataReviewStatus = &status
		return nil
	})
	if err != nil {
		return err
	}
	if updated == nil {
		return fmt.Errorf("audiobook not found")
	}
	return nil
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
	siblings, ok := mfs.versionGroupSiblings(book, "library-copy lookup")
	if !ok {
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

// versionGroupSiblings lists book's version group for a caller that can carry
// on without it: write-back to sibling copies skips them, and the library-copy
// lookup returns no sibling (the apply then declines a protected book). A read
// error is logged, not swallowed. Until 2026-09-12 both callers dropped it with
// no trace, so an unreadable group looked exactly like a group with no
// siblings. ok is false when the group could not be read.
func (mfs *Service) versionGroupSiblings(book *database.Book, purpose string) ([]database.Book, bool) {
	siblings, err := mfs.db.GetBooksByVersionGroup(*book.VersionGroupID)
	if err != nil {
		slog.Warn("could not read version group; continuing without its siblings",
			"purpose", purpose, "bookID", book.ID, "versionGroupID", *book.VersionGroupID, "error", err)
		return nil, false
	}
	return siblings, true
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
