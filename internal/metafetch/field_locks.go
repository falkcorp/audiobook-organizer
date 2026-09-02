// file: internal/metafetch/field_locks.go
// version: 1.0.0
// guid: 2e223955-0b75-4da2-8cbe-a6a99c75bf07
// last-edited: 2026-09-02

package metafetch

import (
	"fmt"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// Every metafetch apply path funnels through guardedApply below, so the user's
// field locks (database.UserLockableFields) are honored by construction rather
// than by each caller remembering to check. Before 2026-09-02 NONE of them did:
// auto-fetch, candidate apply, batch-apply-cached, transcription auto-match and
// the metadata upgrade job all wrote straight over locked columns, while the
// provenance panel layered the override value back on READ -- so the UI showed
// the user's value and every list view, search index, write-back tag and
// organize path used the overwritten one.

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

// lockedFields reads the book's lock set through the shared guard. Fail closed:
// the error is returned, never swallowed into an empty map.
func (mfs *Service) lockedFields(bookID string) (map[string]bool, error) {
	if mfs == nil {
		return nil, fmt.Errorf("%w: nil service", database.ErrFieldLocksUnavailable)
	}
	return database.LockedUserFields(mfs.db, bookID)
}

// guardedApply is THE apply chokepoint. It resolves the locks, strips locked
// fields, records change history for what will actually change, and applies.
// It returns the stripped metadata (so callers persist provenance / tag against
// what was applied, or the full candidate if they prefer) and the skipped keys.
//
// source == "" skips history recording (ApplyMetadataToBook's contract).
func (mfs *Service) guardedApply(book *database.Book, meta metadata.BookMetadata, source string) (metadata.BookMetadata, []string, error) {
	if book == nil {
		return meta, nil, fmt.Errorf("apply metadata: nil book")
	}
	locked, err := mfs.lockedFields(book.ID)
	if err != nil {
		return meta, nil, fmt.Errorf("refusing to apply metadata to %s: %w", book.ID, err)
	}
	meta, skipped := StripLockedFields(meta, locked)
	if len(skipped) > 0 {
		slog.Info("metadata apply: skipped user-locked fields",
			"book_id", book.ID, "source", source, "skipped_locked", skipped)
	}
	if source != "" {
		mfs.RecordChangeHistory(book, meta, source)
	}
	mfs.applyMetadataUnguarded(book, meta)
	return meta, skipped, nil
}
