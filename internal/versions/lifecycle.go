// file: internal/versions/lifecycle.go
// version: 1.5.0
// guid: 5a3b4c0d-6e7f-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-10-27
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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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

// BookServesFilesError is what PurgeVersion returns, having deleted nothing,
// when the version's own book still serves one of the version's files. It is
// not a failure of the purge machinery: the version stays in the trash, files
// intact and restorable, and a later run tries again.
//
// How it happens: trashing the ACTIVE version only flips its status. Its files
// stay where the book plays them (book.FilePath and the book_file rows of the
// version), so 14 days later the purge was deleting the playable files and
// leaving the book pointing at a missing file.
type BookServesFilesError struct {
	BookID    string
	VersionID string
	// Paths are the version's files the book still serves.
	Paths []string
}

func (e *BookServesFilesError) Error() string {
	return fmt.Sprintf("version %s of book %s not purged: book still serves %d of its file(s)", e.VersionID, e.BookID, len(e.Paths))
}

// keptReason is the journal text for a version left in the trash by the guard.
const keptReason = "book still serves this file"

// trashedLibraryState is the library_state a soft delete writes
// (database.trashLibraryState, unexported).
const trashedLibraryState = "deleted"

// bookIsTrashed reports whether the book is itself in the trash. A trashed
// book serves nothing, so the guard does not apply to it.
func bookIsTrashed(b *database.Book) bool {
	if b.IsSoftDeleted() {
		return true
	}
	return b.LibraryState != nil && *b.LibraryState == trashedLibraryState
}

// PurgeVersion physically deletes the version's files and marks it
// inactive_purged. Keeps the DB rows for fingerprint matching.
//
// A file a live book still serves is never deleted: when the version's book is
// not itself trashed and a file is served (see servedGuard: the book's
// FilePath or, for a multi-file book, the folder it names; a row of its active
// version or of no version; the same file reached by a symlink or hard link;
// or any of those for ANOTHER live book), PurgeVersion deletes nothing, leaves
// the version in the trash and returns a *BookServesFilesError. It fails
// closed: if the book, its versions, its rows or another book's rows cannot be
// read, or the version's slot cannot be walked, nothing is deleted and the
// error is returned. A book that is itself in the trash is not guarded, as
// before.
func PurgeVersion(store versionPurger, ver *database.BookVersion) error {
	book, err := store.GetBookByID(ver.BookID)
	if err != nil || book == nil {
		return fmt.Errorf("book %s not found", ver.BookID)
	}
	files, err := store.GetBookFiles(ver.BookID)
	if err != nil {
		return fmt.Errorf("list files of book %s: %w", ver.BookID, err)
	}

	// Where the version's files live: its .versions/{vid}/ slot and any
	// book_file rows keyed to it.
	bookDir := ""
	if book.FilePath != "" {
		if d := filepath.Dir(book.FilePath); d != "." && d != string(filepath.Separator) {
			bookDir = d
		}
	}
	var own []string
	for i := range files {
		if files[i].VersionID == ver.ID {
			own = append(own, files[i].FilePath)
		}
	}
	if bookDir != "" {
		slot := VersionSlotDir(bookDir, ver.ID)
		walkErr := filepath.WalkDir(slot, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				own = append(own, path)
			}
			return nil
		})
		if walkErr != nil && !errors.Is(walkErr, fs.ErrNotExist) {
			return fmt.Errorf("list files in version slot %s: %w", slot, walkErr)
		}
	}

	if !bookIsTrashed(book) {
		guard, err := newServedGuard(store, book, files)
		if err != nil {
			return err
		}
		var kept []string
		seen := make(map[string]struct{})
		for _, p := range own {
			c := canonPath(p)
			if _, dup := seen[c]; dup {
				continue
			}
			seen[c] = struct{}{}
			served, err := guard.serves(p)
			if err != nil {
				return err
			}
			if served {
				kept = append(kept, p)
			}
		}
		if len(kept) > 0 {
			lifecycleLog.Warn("trash purge kept version %s of book %s in the trash: %d path(s) kept, reason: %s",
				ver.ID, ver.BookID, len(kept), keptReason)
			return &BookServesFilesError{BookID: ver.BookID, VersionID: ver.ID, Paths: kept}
		}
	}

	if bookDir != "" {
		if err := RemoveVersionSlot(bookDir, ver.ID); err != nil {
			lifecycleLog.Warn("remove version slot for %s failed: %v", ver.ID, err)
		}
		_ = PruneEmptyVersionsDir(bookDir)
	}

	// Also remove any files directly associated with this version.
	for i := range files {
		if files[i].VersionID == ver.ID {
			_ = os.Remove(files[i].FilePath)
		}
	}

	now := time.Now()
	ver.Status = database.BookVersionStatusInactivePurged
	ver.PurgedDate = &now
	return store.UpdateBookVersion(ver)
}

// KeptVersion is a trashed version the cleanup left alone because its book
// still serves its files.
type KeptVersion struct {
	BookID    string
	VersionID string
	Paths     int
}

// CleanupResult is what one cleanup run did.
type CleanupResult struct {
	// Purged is the number of versions purged.
	Purged int
	// Kept lists the versions left in the trash because their book still
	// serves their files; they are retried on every run.
	Kept []KeptVersion
}

// CleanupTrashedVersions is the maintenance task that purges versions
// whose trash window has run out. Called by the scheduler.
func CleanupTrashedVersions(store TrashedVersionCleaner) CleanupResult {
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
func cleanupTrashedVersionsAt(store TrashedVersionCleaner, now time.Time) (res CleanupResult) {
	trashed, err := store.ListTrashedBookVersions()
	if err != nil {
		lifecycleLog.Warn("trash cleanup: list trashed versions failed: %v", err)
		return res
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
			var served *BookServesFilesError
			if errors.As(err, &served) {
				// Already logged (WARN, one per version) by PurgeVersion.
				res.Kept = append(res.Kept, KeptVersion{BookID: served.BookID, VersionID: served.VersionID, Paths: len(served.Paths)})
				continue
			}
			lifecycleLog.Warn("trash cleanup: purging version %s failed: %v", v.ID, err)
			continue
		}
		res.Purged++
	}
	if stamped > 0 {
		lifecycleLog.Info("trash cleanup: stamped %d trashed version(s) that had no trashed-at time; none of them were purged", stamped)
	}
	return res
}
