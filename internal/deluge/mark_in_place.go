// file: internal/deluge/mark_in_place.go
// version: 1.2.0
// guid: 8ee900f5-372f-426f-a797-2f6d18be3aa1
// last-edited: 2026-10-11

package deluge

import (
	"errors"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

// BookFileModifier is the one store call MarkImportedInPlace makes.
type BookFileModifier interface {
	ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
}

// InPlaceOutcome is what MarkImportedInPlace did.
type InPlaceOutcome int

const (
	// InPlaceMarked: the row was marked imported.
	InPlaceMarked InPlaceOutcome = iota
	// InPlaceAlreadyMarked: another writer marked it first; nothing written.
	InPlaceAlreadyMarked
	// InPlaceMoved: the row no longer names path; nothing written.
	InPlaceMoved
	// InPlaceGone: the row no longer exists; nothing written.
	InPlaceGone
	// InPlaceProtected: path is protected (InPlaceProtection), so the row is
	// left pending; nothing written. ImportToLibraryWith refuses the same
	// file with tagger.ErrProtectedPathWrite.
	InPlaceProtected
)

// MarkImportedInPlace records that a pending book file is already at its
// library destination: the importer resolved the destination to the file's
// own path, so there is nothing to copy. It sets ImportedFromDelugeAt and
// nothing else -- no DelugeOriginalPath (the file never left a Deluge save
// path), no path change, no move -- under the row's lock, and only while the
// row still names path and is unmarked.
//
// A protected path is refused first, before the row is read: when
// InPlaceProtection(cfg, protected, path) names a reason (a static protected
// prefix, a Deluge save_path the checker knows, or a Deluge list that has not
// loaded: ProtectedListNotLoaded) it returns
// InPlaceProtected and writes nothing, so the row stays pending exactly as
// ImportToLibraryWith leaves it (tagger.ErrProtectedPathWrite). Marking it
// would hand the protected file to every later tag write as the library
// copy. protected is the server's ProtectedPathCache or nil; with nil only
// cfg's static prefixes apply.
//
// Without it an unprotected row stays on GetBookFilesNeedingDelugeImportCore
// forever. ImportToLibraryWith (the /discovery/import path) has marked an
// unprotected in-place row since 2026-09-14 and refused a protected one;
// deluge.centralize and maintenance.bulk-deluge-import skipped both and wrote
// nothing, so every run listed them again (bulk import counting them
// "imported" each time). A row deluge.link-backfill links is in the library
// and carries a DelugeHash, with ImportedFromDelugeAt unset: this function
// marks it, unless its path is protected (a torrent seeding from inside
// RootDir), in which case it stays pending under every importer.
//
// A write whose fsync failed (database.ErrBookFileDurabilityUnknown) was
// applied, so it is logged and reported as InPlaceMarked.
func MarkImportedInPlace(store BookFileModifier, cfg *config.Config, protected tagger.PathChecker, bookID, fileID, path string, now time.Time) (InPlaceOutcome, error) {
	if store == nil {
		return InPlaceGone, fmt.Errorf("MarkImportedInPlace: store is nil")
	}
	if why := InPlaceProtection(cfg, protected, path); why != "" {
		importLog.Info("MarkImportedInPlace: %s is protected (%s); book file %s left pending, nothing written",
			logger.SanitizeLogValue(path), logger.SanitizeLogValue(why), logger.SanitizeLogValue(fileID))
		return InPlaceProtected, nil
	}
	got := InPlaceGone
	row, err := store.ModifyBookFile(bookID, fileID, func(bf *database.BookFile) error {
		switch {
		case bf.FilePath != path:
			got = InPlaceMoved
			return database.ErrSkipBookFileWrite
		case bf.ImportedFromDelugeAt != nil:
			got = InPlaceAlreadyMarked
			return database.ErrSkipBookFileWrite
		}
		t := now
		bf.ImportedFromDelugeAt = &t
		got = InPlaceMarked
		return nil
	})
	switch {
	case errors.Is(err, database.ErrBookFileDurabilityUnknown):
		importLog.Error("MarkImportedInPlace: marking %s imported was written but its fsync failed (%v): durability unknown; keeping it",
			logger.SanitizeLogValue(fileID), err)
		return got, nil
	case err != nil:
		return got, fmt.Errorf("MarkImportedInPlace: mark book file %s imported at %s: %w", fileID, path, err)
	case row == nil:
		return InPlaceGone, nil
	}
	return got, nil
}
