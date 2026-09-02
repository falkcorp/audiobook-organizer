// file: internal/metafetch/field_locks.go
// version: 1.1.0
// guid: 2e223955-0b75-4da2-8cbe-a6a99c75bf07
// last-edited: 2026-09-02

package metafetch

import (
	"fmt"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// The user's field locks (database.UserLockableFields) are enforced by ONE
// chokepoint, database.FieldLocks, shared by every writer of a lockable Book
// column in the codebase. In this package that chokepoint is reached through
// guardedApply (the metadata apply body) and ISBNService.EnrichBookISBN (the
// background identifier enrichment both apply paths queue). Before 2026-09-02
// NEITHER checked: auto-fetch, candidate apply, batch-apply-cached,
// transcription auto-match and the metadata upgrade job all wrote straight over
// locked columns, while the provenance panel layered the override value back
// on READ -- so the UI showed the user's value and every list view, search
// index, write-back tag and organize path used the overwritten one.

// StripLockedFields blanks every field of meta whose lock key is set in locked,
// so the unguarded apply body cannot write it, and returns the keys it blanked
// (only those the candidate actually carried a value for -- an absent value
// is not a skip). Keys are database.FieldKey* so a renamed key is a compile
// error here rather than an inert guard.
//
// series_name also blanks the position: the candidate's position belongs to
// the candidate's series, which was just refused, so writing it alone would
// attach a number from one series to another.
func StripLockedFields(meta metadata.BookMetadata, locked map[string]bool) (metadata.BookMetadata, []string) {
	var skipped []string
	skip := func(key string) { skipped = append(skipped, key) }

	for _, f := range database.UserLockableFields {
		if !locked[f.Key] {
			continue
		}
		switch f.Key {
		case database.FieldKeyTitle:
			if meta.Title != "" {
				meta.Title = ""
				skip(f.Key)
			}
		case database.FieldKeyAuthorName:
			if meta.Author != "" {
				meta.Author = ""
				skip(f.Key)
			}
		case database.FieldKeySeriesName:
			if meta.Series != "" {
				meta.Series = ""
				meta.SeriesPosition = ""
				skip(f.Key)
			}
		case database.FieldKeySeriesPosition:
			if meta.SeriesPosition != "" {
				meta.SeriesPosition = ""
				skip(f.Key)
			}
		case database.FieldKeyNarrator:
			if meta.Narrator != "" {
				meta.Narrator = ""
				skip(f.Key)
			}
		case database.FieldKeyPublisher:
			if meta.Publisher != "" {
				meta.Publisher = ""
				skip(f.Key)
			}
		case database.FieldKeyLanguage:
			if meta.Language != "" {
				meta.Language = ""
				skip(f.Key)
			}
		case database.FieldKeyAudiobookReleaseYear:
			// A print-kind year routes to PrintYear, which has no lock key.
			if meta.PublishYear != 0 && meta.PublishYearIsAudiobookRelease {
				meta.PublishYear = 0
				skip(f.Key)
			}
		case database.FieldKeyISBN10:
			if len(meta.ISBN) == 10 {
				meta.ISBN = ""
				skip(f.Key)
			}
		case database.FieldKeyISBN13:
			if meta.ISBN != "" && len(meta.ISBN) != 10 {
				meta.ISBN = ""
				skip(f.Key)
			}
		case database.FieldKeyASIN:
			if meta.ASIN != "" {
				meta.ASIN = ""
				skip(f.Key)
			}
		case database.FieldKeyGenre:
			if meta.Genre != "" {
				meta.Genre = ""
				skip(f.Key)
			}
		case database.FieldKeyDescription:
			if meta.Description != "" {
				meta.Description = ""
				skip(f.Key)
			}
		default:
			// A key in the vocabulary that this switch does not know how to
			// blank would be a hole in the guard. TestStripLockedFieldsCoversVocabulary
			// pins that this branch is unreachable.
			panic(fmt.Sprintf("metafetch: lock key %q has no StripLockedFields case", f.Key))
		}
	}
	return meta, skipped
}

// loadFieldLocks reads the book's lock set through the shared guard. Fail
// closed: the error is returned, never swallowed into an empty set.
func (mfs *Service) loadFieldLocks(bookID string) (database.FieldLocks, error) {
	if mfs == nil {
		return database.FieldLocks{}, fmt.Errorf("%w: nil service", database.ErrFieldLocksUnavailable)
	}
	return database.LoadFieldLocks(mfs.db, bookID)
}

// guardedApply is this package's entry to the shared chokepoint. It loads the
// locks (fail closed), strips locked fields from the candidate, records change
// history for what will actually change, and runs the apply body inside
// database.FieldLocks.Apply.
//
// Why strip AND restore: applyMetadataUnguarded has side effects on two locked
// fields -- it resolves/creates an Author row and rewrites the book_authors
// join for author_name, and resolves/creates a Series row for series_name.
// Restoring the id afterwards would leave those side effects behind, so the
// value is removed before the body ever sees it. Apply then guarantees that
// whatever the body did, no locked column leaves here changed; any key it had
// to restore is a body bug, and is reported alongside the stripped keys.
//
// It returns the stripped metadata (so callers persist provenance / tag against
// what was applied, or the full candidate if they prefer) and the skipped keys.
// source == "" skips history recording (ApplyMetadataToBook's contract).
func (mfs *Service) guardedApply(book *database.Book, meta metadata.BookMetadata, source string) (metadata.BookMetadata, []string, error) {
	if book == nil {
		return meta, nil, fmt.Errorf("apply metadata: nil book")
	}
	locks, err := mfs.loadFieldLocks(book.ID)
	if err != nil {
		return meta, nil, fmt.Errorf("refusing to apply metadata to %s: %w", book.ID, err)
	}
	meta, skipped := StripLockedFields(meta, locks.Set())
	if source != "" {
		mfs.RecordChangeHistory(book, meta, source)
	}
	restored := locks.Apply(book, func(b *database.Book) { mfs.applyMetadataUnguarded(b, meta) })
	if len(restored) > 0 {
		// Strip should have made this unreachable; if it fires, a new write in
		// applyMetadataUnguarded reaches a locked column by a route
		// StripLockedFields does not know about.
		slog.Warn("metadata apply: apply body reached a locked column after strip; restored",
			"book_id", book.ID, "source", source, "restored", restored)
		skipped = mergeSkipped(skipped, restored)
	}
	if len(skipped) > 0 {
		slog.Info("metadata apply: skipped user-locked fields",
			"book_id", book.ID, "source", source, "skipped_locked", skipped)
	}
	return meta, skipped, nil
}

// mergeSkipped unions two skipped-key lists in vocabulary order.
func mergeSkipped(a, b []string) []string {
	seen := map[string]bool{}
	for _, k := range a {
		seen[k] = true
	}
	for _, k := range b {
		seen[k] = true
	}
	var out []string
	for _, f := range database.UserLockableFields {
		if seen[f.Key] {
			out = append(out, f.Key)
		}
	}
	return out
}
