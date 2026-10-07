// file: internal/itunes/service/writeback_plan.go
// version: 1.0.0
// guid: 9b1d6e2a-3f47-4c85-a0e9-5d2c7b8f1a64
// last-edited: 2026-10-07
//
// planBookWrite is the one place that decides what the write-back batcher
// writes to the iTunes library for a book. The flush (drainFlush) and the
// requeue preview (PlanRequeue) both call it, so a preview reports exactly what
// a flush of the same book will try to write. Extracted unchanged from the
// per-book body of drainFlush on 2026-10-07.

package itunesservice

import (
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

// bookPlanStore is what planBookWrite reads.
type bookPlanStore interface {
	GetAuthorByID(id int) (*database.Author, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
}

// trackChange is one track the flush would write.
type trackChange struct {
	// PID as stored on the book or book_file row (case not normalized).
	PID string
	// Current is the library's track for PID, nil when the PID is not in the
	// library (the flush then emits its updates unconditionally).
	Current  *itunes.ITLTrack
	Location *itunes.ITLLocationUpdate
	Metadata *itunes.ITLMetadataUpdate
}

// bookWritePlan is what the flush would write for one book.
type bookWritePlan struct {
	Changes []trackChange
	// Unchanged counts tracks that match the library and are skipped.
	Unchanged int
	// PIDs are every PID the book's write path considers, lowercase.
	PIDs []string
	// FilesErr is GetBookFiles' error. The flush ignores it, as it always
	// has: with no files it falls back to the book-level PID.
	FilesErr error
}

// planBookWrite computes the location and metadata updates the flush would
// write for book against the library tracks in tracksByPID (keyed by lowercase
// PID hex). The caller has already excluded nil and non-primary books.
func planBookWrite(store bookPlanStore, book *database.Book, tracksByPID map[string]*itunes.ITLTrack) bookWritePlan {
	var plan bookWritePlan

	// Get author name for metadata
	authorName := ""
	if book.AuthorID != nil {
		if author, err := store.GetAuthorByID(*book.AuthorID); err == nil && author != nil {
			authorName = author.Name
		}
	}
	// Narrator ends up in the Composer field for audiobooks —
	// Apple Music shows it there and most audiobook workflows
	// (scanners, converters, players) key on that mapping.
	narrator := ""
	if book.Narrator != nil {
		narrator = *book.Narrator
	}
	// Genre: prefer the book's own genre when set, fall back to
	// "Audiobook" so iTunes classifies correctly. Previously
	// every write hardcoded "Audiobook" even when the user had
	// set a more specific value.
	genre := "Audiobook"
	if book.Genre != nil && *book.Genre != "" {
		genre = *book.Genre
	}

	files, ferr := store.GetBookFiles(book.ID)
	plan.FilesErr = ferr
	if len(files) > 0 {
		for _, f := range files {
			if f.ITunesPersistentID == "" {
				continue
			}
			// Diff-before-write (T008 / HIGH-3): only emit an
			// ITLMetadataUpdate (and/or location update) when at
			// least one field differs from the current library value.
			// ITLTrack.Composer is not stored in the parsed struct
			// (0x0C not read), so it is always included in the update
			// when other fields change.
			pidKey := strings.ToLower(f.ITunesPersistentID)
			plan.PIDs = append(plan.PIDs, pidKey)
			desiredLoc := ""
			if f.ITunesPath != "" {
				// SPEC §1b / TASK-006: normalize f.ITunesPath (which has
				// historically held BOTH native paths and file:// URLs)
				// into the canonical WinPath. Unmappable values are NOT
				// written — per-item WARN + metric, never a raw value into
				// 0x0D (the CRIT-2 corruption).
				if winPath, ok := normalizeITunesLocation(f.ITunesPersistentID, f.ITunesPath); ok {
					desiredLoc = winPath
				}
			}
			meta := &itunes.ITLMetadataUpdate{
				PersistentID: f.ITunesPersistentID,
				Name:         f.Title,
				Album:        book.Title,
				Artist:       authorName,
				Composer:     narrator,
				Genre:        genre,
			}

			if cur, ok := tracksByPID[pidKey]; ok {
				// We have current library state: compare field by field.
				// Location update is suppressed when the desired path
				// matches the current 0x0D value (or is unmappable/empty).
				locationChanged := desiredLoc != "" && cur.Location != desiredLoc
				metadataChanged := cur.Name != f.Title ||
					cur.Album != book.Title ||
					cur.Artist != authorName ||
					cur.Genre != genre

				if !locationChanged && !metadataChanged {
					plan.Unchanged++
					continue
				}
				ch := trackChange{PID: f.ITunesPersistentID, Current: cur}
				if locationChanged {
					ch.Location = &itunes.ITLLocationUpdate{
						PersistentID: f.ITunesPersistentID,
						NewLocation:  desiredLoc,
					}
				}
				if metadataChanged {
					ch.Metadata = meta
				}
				plan.Changes = append(plan.Changes, ch)
			} else {
				// No current library state for this PID (track is new or
				// library parse failed) — emit both unconditionally.
				ch := trackChange{PID: f.ITunesPersistentID, Metadata: meta}
				if desiredLoc != "" {
					ch.Location = &itunes.ITLLocationUpdate{
						PersistentID: f.ITunesPersistentID,
						NewLocation:  desiredLoc,
					}
				}
				plan.Changes = append(plan.Changes, ch)
			}
		}
	} else if book.ITunesPersistentID != nil && *book.ITunesPersistentID != "" {
		pidKey := strings.ToLower(*book.ITunesPersistentID)
		plan.PIDs = append(plan.PIDs, pidKey)
		cur, ok := tracksByPID[pidKey]
		if ok &&
			cur.Name == book.Title &&
			cur.Album == book.Title &&
			cur.Artist == authorName &&
			cur.Genre == genre {
			plan.Unchanged++
			return plan
		}
		ch := trackChange{
			PID: *book.ITunesPersistentID,
			Metadata: &itunes.ITLMetadataUpdate{
				PersistentID: *book.ITunesPersistentID,
				Name:         book.Title,
				Album:        book.Title,
				Artist:       authorName,
				Composer:     narrator,
				Genre:        genre,
			},
		}
		if ok {
			ch.Current = cur
		}
		plan.Changes = append(plan.Changes, ch)
	}
	return plan
}
