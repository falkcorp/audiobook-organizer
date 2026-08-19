// file: internal/versions/lifecycle.go
// version: 1.1.0
// guid: 5a3b4c0d-6e7f-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-08-19
//
// Version lifecycle operations (spec 3.1 task 6).
//
// Lifecycle states:
//   active → alt → trash → inactive_purged → (hard delete)
//
// Delete puts a version in trash (14-day TTL). Auto-promote selects
// the most recent alt if the active version was trashed. Restore
// moves trash back to alt. Purge physically deletes files and
// marks inactive_purged (keeps metadata for fingerprint). Hard
// delete removes all traces.

package versions

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TrashTTLDays is the number of days a trashed version is kept before
// automatic purge.
const TrashTTLDays = 14

// AutoPromoteAlt selects the most recent alt version and promotes it
// to active. Called when the active version is trashed.
func AutoPromoteAlt(store altPromoter, bookID string) error {
	allVers, err := store.GetBookVersionsByBookID(bookID)
	if err != nil {
		return err
	}
	var bestAlt *database.BookVersion
	for i := range allVers {
		v := &allVers[i]
		if v.Status != database.BookVersionStatusAlt {
			continue
		}
		if bestAlt == nil || v.IngestDate.After(bestAlt.IngestDate) {
			bestAlt = v
		}
	}
	if bestAlt == nil {
		return nil
	}
	bestAlt.Status = database.BookVersionStatusActive
	return store.UpdateBookVersion(bestAlt)
}

// PurgeVersion physically deletes the version's files and marks it
// inactive_purged. Keeps the DB rows for fingerprint matching.
func PurgeVersion(store versionPurger, ver *database.BookVersion) error {
	book, err := store.GetBookByID(ver.BookID)
	if err != nil || book == nil {
		return fmt.Errorf("book %s not found", ver.BookID)
	}

	// Remove files from .versions/{vid}/ or book root.
	bookDir := ""
	if book.FilePath != "" {
		// Extract directory by finding the last slash
		for i := len(book.FilePath) - 1; i >= 0; i-- {
			if book.FilePath[i] == '/' {
				bookDir = book.FilePath[:i]
				break
			}
		}
	}

	if bookDir != "" {
		if err := RemoveVersionSlot(bookDir, ver.ID); err != nil {
			slog.Warn("remove version slot", "ver", ver.ID, "err", err)
		}
		_ = PruneEmptyVersionsDir(bookDir)
	}

	// Also remove any files directly associated with this version.
	files, _ := store.GetBookFiles(ver.BookID)
	for _, f := range files {
		if f.VersionID == ver.ID {
			_ = os.Remove(f.FilePath)
		}
	}

	now := time.Now()
	ver.Status = database.BookVersionStatusInactivePurged
	ver.PurgedDate = &now
	return store.UpdateBookVersion(ver)
}

// CleanupTrashedVersions is the maintenance task that purges versions
// past their TTL. Called by the scheduler.
func CleanupTrashedVersions(store trashedVersionCleaner) (purged int) {
	trashed, err := store.ListTrashedBookVersions()
	if err != nil {
		slog.Warn("list trashed versions", "error", err)
		return 0
	}

	cutoff := time.Now().Add(-time.Duration(TrashTTLDays) * 24 * time.Hour)
	for i := range trashed {
		v := &trashed[i]
		if v.CreatedAt.After(cutoff) {
			continue
		}
		if err := PurgeVersion(store, v); err != nil {
			slog.Warn("purge trashed", "v", v.ID, "err", err)
			continue
		}
		purged++
	}
	return purged
}
