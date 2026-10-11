// file: internal/versions/lifecycle.go
// version: 1.3.1
// guid: 5a3b4c0d-6e7f-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-10-11
//
// Version lifecycle operations (spec 3.1 task 6).
//
// Lifecycle states:
//   active → alt → trash → inactive_purged → (hard delete)
//
// Delete puts a version in trash (14-day TTL, counted from TrashedAt --
// when it entered the trash, not when it was created). Auto-promote selects
// the most recent alt if the active version was trashed. Restore
// moves trash back to alt. Purge physically deletes files and
// marks inactive_purged (keeps metadata for fingerprint). Hard
// delete removes all traces.

package versions

import (
	"fmt"
	"os"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// TrashTTLDays is the number of days a trashed version is kept before
// automatic purge, counted from the moment it entered the trash.
const TrashTTLDays = 14

var lifecycleLog = logger.New("versions.lifecycle")

// MarkTrashed moves v into the trash in memory and stamps TrashedAt with now.
// A version already in the trash keeps its stamp, so a repeated trash request
// neither restarts nor shortens its window. The caller persists v; the store
// enforces the same rule on write (database.PebbleStore.UpdateBookVersion),
// this just makes the stamp visible on the caller's copy too.
func MarkTrashed(v *database.BookVersion, now time.Time) {
	if v.Status == database.BookVersionStatusTrash && v.TrashedAt != nil {
		return
	}
	stamp := now
	v.Status = database.BookVersionStatusTrash
	v.TrashedAt = &stamp
}

// MarkRestored moves a trashed v back to alt in memory and clears TrashedAt.
// The caller persists v.
func MarkRestored(v *database.BookVersion) {
	v.Status = database.BookVersionStatusAlt
	v.TrashedAt = nil
}

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
			lifecycleLog.Warn("remove version slot for %s failed: %v", ver.ID, err)
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
// whose trash window has run out. Called by the scheduler.
func CleanupTrashedVersions(store TrashedVersionCleaner) (purged int) {
	return cleanupTrashedVersionsAt(store, time.Now())
}

// cleanupTrashedVersionsAt is CleanupTrashedVersions with the clock passed in.
//
// The window is measured from TrashedAt. Until 2026-10-11 it was measured from
// CreatedAt, so any version older than TrashTTLDays was purged -- files
// deleted from disk -- at the first run after it was trashed, instead of 14
// days later as the UI promises.
//
// A trashed version with no TrashedAt was trashed before the field existed.
// When it went in is unknown, so it is stamped now and NOT purged: it gets a
// full window from the first run that sees it. A version lacking TrashedAt is
// never purged.
//
// The trash is small and the loop is sequential on purpose: each purge deletes
// files. The re-read below narrows, but does not close, the race with a
// restore that lands mid-run: UpdateBookVersion has no compare-and-swap, so a
// restore between the re-read and the stamp write is re-trashed, and one
// between the re-read and PurgeVersion still loses its files. Closing it needs
// an atomic claim of the row before any file is deleted.
func cleanupTrashedVersionsAt(store TrashedVersionCleaner, now time.Time) (purged int) {
	trashed, err := store.ListTrashedBookVersions()
	if err != nil {
		lifecycleLog.Warn("trash cleanup: list trashed versions failed: %v", err)
		return 0
	}

	ttl := time.Duration(TrashTTLDays) * 24 * time.Hour
	stamped := 0
	for i := range trashed {
		id := trashed[i].ID
		// The list is a snapshot. Re-read so a restore that landed after it
		// is seen (see the race note above for what this does not cover).
		v, err := store.GetBookVersion(id)
		if err != nil {
			lifecycleLog.Warn("trash cleanup: re-read version %s failed, skipping it this run: %v", id, err)
			continue
		}
		if v == nil || v.Status != database.BookVersionStatusTrash {
			continue
		}
		if v.TrashedAt == nil {
			stamp := now
			v.TrashedAt = &stamp
			if err := store.UpdateBookVersion(v); err != nil {
				lifecycleLog.Warn("trash cleanup: version %s has no trashed-at time and stamping it failed; not purging it: %v", id, err)
				continue
			}
			stamped++
			lifecycleLog.Info("trash cleanup: version %s (book %s) predates trashed-at tracking; stamped %s, eligible for purge after %s",
				id, v.BookID, stamp.UTC().Format(time.RFC3339), stamp.Add(ttl).UTC().Format(time.RFC3339))
			continue
		}
		if now.Sub(*v.TrashedAt) < ttl {
			continue
		}
		if err := PurgeVersion(store, v); err != nil {
			lifecycleLog.Warn("trash cleanup: purging version %s failed: %v", v.ID, err)
			continue
		}
		purged++
	}
	if stamped > 0 {
		lifecycleLog.Info("trash cleanup: stamped %d trashed version(s) that had no trashed-at time; none of them were purged", stamped)
	}
	return purged
}
