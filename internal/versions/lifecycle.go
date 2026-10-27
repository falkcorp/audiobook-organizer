// file: internal/versions/lifecycle.go
// version: 1.6.1
// guid: 5a3b4c0d-6e7f-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-10-27
//
// Version lifecycle operations (spec 3.1 task 6).
//
// Lifecycle states:
//   active → alt → trash → purging → inactive_purged → (hard delete)
//
// purging is the claim PurgeVersion takes before it removes the first file. A
// purge that finishes passes through it in one call; one that fails part way
// leaves the version there, where it cannot be restored (some files are gone)
// and where the nightly cleanup and purge-now retry it.
//
// Delete puts a version in trash (14-day TTL, counted from TrashedAt --
// when it entered the trash, not when it was created). Auto-promote selects
// the most recent alt if the active version was trashed. Restore
// moves trash back to alt. Purge physically deletes files and
// marks inactive_purged (keeps metadata for fingerprint). Hard
// delete removes all traces.
//
// A retired version (database.BookVersionStatusRetired) is outside this
// machine. It holds the retired book_file rows of a book that was retired into
// the version's own book, it is created in that state and leaves it only
// through UnretireBookFiles, and every transition here refuses it with
// ErrRetiredVersion: trashing it would start the 14-day clock that ends in
// PurgeVersion, and PurgeVersion deletes files from disk.

package versions

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// TrashTTLDays is the number of days a trashed version is kept before
// automatic purge, counted from the moment it entered the trash.
const TrashTTLDays = 14

var lifecycleLog = logger.New("versions.lifecycle")

// ErrVersionNotInTrash is returned by PurgeVersion when the version left the
// trash after the caller read it (it was restored): the claim was refused and
// nothing was deleted. It is not a failure of the purge, the user's restore
// won; the nightly cleanup skips such a version, and purge-now answers 409.
var ErrVersionNotInTrash = errors.New("version is no longer in the trash")

// ErrRetiredVersion is returned by the lifecycle steps that refuse a retired
// version: MarkTrashed, MarkRestored and PurgeVersion. Callers branch on it
// with errors.Is; the server answers it with a 409. AutoPromoteAlt never
// returns it (it skips a retired version silently), and swap has no guard yet:
// it is tracked in the todo fragment for this stack.
var ErrRetiredVersion = errors.New("a retired version is not part of the version lifecycle")

// refuseRetired is the one place a lifecycle step checks for a retired
// version. The store refuses to change a retired version only while it still
// holds retired rows (database.ErrRetiredVersionHasFiles); a retired version
// that holds none, or a step that acts on the caller's copy before it writes
// (PurgeVersion removes files first), is not covered by that, so each step
// refuses here, up front, from the status the caller holds.
func refuseRetired(v *database.BookVersion, action string) error {
	if v != nil && v.Status == database.BookVersionStatusRetired {
		return fmt.Errorf("%s version %s: %w (it holds the retired files of book %s, which stay where they are)",
			action, v.ID, ErrRetiredVersion, v.RetiredFromBookID)
	}
	return nil
}

// refusePurging refuses a lifecycle step on a version whose purge has started.
func refusePurging(v *database.BookVersion, action string) error {
	if v != nil && v.Status == database.BookVersionStatusPurging {
		return fmt.Errorf("%s version %s: %w (an earlier purge removed some of its files; "+
			"the nightly cleanup or Purge now finishes it)", action, v.ID, database.ErrVersionPurging)
	}
	return nil
}

// MarkTrashed moves v into the trash in memory and stamps TrashedAt with now.
// A version already in the trash keeps its stamp, so a repeated trash request
// neither restarts nor shortens its window. The caller persists v; the store
// enforces the same rule on write (database.PebbleStore.UpdateBookVersion),
// this just makes the stamp visible on the caller's copy too.
//
// It refuses a retired version (ErrRetiredVersion) and leaves v untouched: a
// retired version in the trash would reach PurgeVersion 14 days later. It
// refuses a purging version (database.ErrVersionPurging) too.
func MarkTrashed(v *database.BookVersion, now time.Time) error {
	if err := refuseRetired(v, "trash"); err != nil {
		return err
	}
	if err := refusePurging(v, "trash"); err != nil {
		return err
	}
	if v.Status == database.BookVersionStatusTrash && v.TrashedAt != nil {
		return nil
	}
	stamp := now
	v.Status = database.BookVersionStatusTrash
	v.TrashedAt = &stamp
	return nil
}

// MarkRestored moves a trashed v back to alt in memory and clears TrashedAt.
// The caller persists v. It refuses a retired version (ErrRetiredVersion),
// which is not in the trash and must not become an alt, and a purging version
// (database.ErrVersionPurging): its purge has started, so some of its files may
// already be gone and an alt made of it would name them. The store refuses the
// same write (database.refusePurgeTransition), which is what closes the race
// with a purge that is claimed after the caller read the version.
func MarkRestored(v *database.BookVersion) error {
	if err := refuseRetired(v, "restore"); err != nil {
		return err
	}
	if err := refusePurging(v, "restore"); err != nil {
		return err
	}
	v.Status = database.BookVersionStatusAlt
	v.TrashedAt = nil
	return nil
}

// AutoPromoteAlt selects the most recent alt version and promotes it
// to active. Called when the active version is trashed.
//
// Only an alt version is a candidate, so a retired version is never promoted
// even when it is newer than every alt: the status test runs before the date
// comparison. Promoting it would put the retired files of another book into the
// holder's active slot. TestAutoPromoteAlt_IgnoresRetired pins this; it fails if
// the status test is loosened to admit any status but trash.
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
//
// It refuses a retired version (ErrRetiredVersion) before touching the disk.
// The refusal has to come first: the file removal below happens before
// UpdateBookVersion, whose own refusal comes too late, and the files of a
// retired version belong to a book the library still keeps.
//
// It also never removes a file that some other book_file row still names. A
// retired version keeps the rows of a book that was retired into this one's
// book, and those rows can name the very paths this version's own rows name
// (PLAN section 5.1 rule 4: the holder's active rows hold every path of the
// shell), so purging this version must leave those files alone. The decision
// is made for every path before the first removal and fails closed: a path
// whose owners cannot be listed (database.ErrBookFilesAtPathUnavailable) aborts
// the purge with nothing deleted and the version unchanged, so the next run
// retries it. Owners are every other book_file row (retired ones included) and
// every other LIVE book whose own FilePath is the path, the same two questions
// the book purge asks (audiobooks purgePathStillReferenced): a book can name a
// file with no row at all. The version's own book is not another owner.
//
// The version is claimed before the first file is removed: it is written as
// purging (database.BookVersionStatusPurging), under the version's stripe, and
// the write is refused with ErrVersionNotInTrash when the stored version is no
// longer trash or purging, so a restore that landed after the caller read the
// version wins and nothing is deleted. From then on a restore is refused
// (database.ErrVersionPurging).
//
// A file that cannot be removed leaves the version purging. Marking it purged
// would orphan the file, because nothing looks at a purged version's disk
// again; left purging it is retried by the next cleanup run (and by purge-now),
// and a file the first attempt already removed is simply gone then (a missing
// file counts as removed). It is purging, not trash, because some of its files
// are already gone and a restore would hand back an alt that names them.
//
// A version whose book no longer exists has nothing on disk to remove: the
// slot is found through the book's FilePath and the rows through its id, and
// DeleteBook refuses while rows exist. It is marked inactive_purged and the
// call succeeds, so one such version cannot fail the nightly cleanup forever.
// A book that cannot be READ is a different thing: that is an error, the
// version is untouched, and the next run retries it.
func PurgeVersion(store versionPurger, ver *database.BookVersion) error {
	if err := refuseRetired(ver, "purge"); err != nil {
		return err
	}
	book, err := store.GetBookByID(ver.BookID)
	if err != nil {
		return fmt.Errorf("purge version %s: read book %s: %w", ver.ID, ver.BookID, err)
	}
	if book == nil {
		lifecycleLog.Warn("purge version %s: its book %s no longer exists, so there is nothing on disk to remove; marking the version purged",
			ver.ID, ver.BookID)
		// Claim first: the same check that a restore since the caller read the
		// version wins, so a restored version is not marked purged.
		if err := claimPurge(store, ver); err != nil {
			return err
		}
		return markPurged(store, ver)
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

	// Everything this purge would delete: the files under the version's slot,
	// plus any file directly associated with this version by a row.
	own, err := store.GetBookFiles(ver.BookID)
	if err != nil {
		return fmt.Errorf("purge version %s: list book files: %w", ver.ID, err)
	}
	targets := make(map[string]struct{})
	if bookDir != "" {
		slotFiles, err := versionSlotFiles(bookDir, ver.ID)
		if err != nil {
			return fmt.Errorf("purge version %s: list slot: %w", ver.ID, err)
		}
		for _, p := range slotFiles {
			targets[p] = struct{}{}
		}
	}
	for _, f := range own {
		if f.VersionID == ver.ID && f.FilePath != "" {
			targets[f.FilePath] = struct{}{}
		}
	}

	// Decide first, delete after, so a lookup failure leaves the disk alone.
	var doomed []string
	for p := range targets {
		owner, err := otherRowNamingPath(store, p, ver)
		if err != nil {
			return fmt.Errorf("purge version %s: cannot verify %s is unreferenced: %w", ver.ID, p, err)
		}
		if owner != nil {
			lifecycleLog.Warn("purge version %s: keeping %s, book_file %s of book %s (version %q, retired=%t) still names it",
				ver.ID, p, owner.ID, owner.BookID, owner.VersionID, owner.Retired)
			continue
		}
		live, err := store.LiveBookIDsAtPath(p)
		if err != nil {
			return fmt.Errorf("purge version %s: cannot verify %s is not a live book's file: %w", ver.ID, p, err)
		}
		// The version's own book naming its file is the normal case, not a
		// second owner: the file is the version's, and the version is what is
		// being purged. Any OTHER live book naming it keeps it.
		live = slices.DeleteFunc(live, func(id string) bool { return id == ver.BookID })
		if len(live) > 0 {
			lifecycleLog.Warn("purge version %s: keeping %s, it is the file path of live book(s) %v",
				ver.ID, p, live)
			continue
		}
		doomed = append(doomed, p)
	}

	// Claim the version before the first removal. Everything above only reads,
	// so a lookup failure leaves the version as it was.
	if err := claimPurge(store, ver); err != nil {
		return err
	}
	var removeErrs []error
	for _, p := range doomed {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			lifecycleLog.Warn("purge version %s: remove %s failed: %v", ver.ID, p, err)
			removeErrs = append(removeErrs, fmt.Errorf("remove %s: %w", p, err))
		}
	}
	if bookDir != "" {
		pruneEmptyDirs(VersionSlotDir(bookDir, ver.ID))
		_ = PruneEmptyVersionsDir(bookDir)
	}
	if len(removeErrs) > 0 {
		return fmt.Errorf("purge version %s: %d of %d file(s) could not be removed; the version stays purging (it cannot be restored) and is retried: %w",
			ver.ID, len(removeErrs), len(doomed), errors.Join(removeErrs...))
	}
	return markPurged(store, ver)
}

// claimPurge writes ver as purging. The store refuses the write when the
// stored version is no longer trash or purging (restored since the caller read
// it): that is ErrVersionNotInTrash, the purge does not go ahead, and ver is
// put back as it was read.
func claimPurge(store versionPurger, ver *database.BookVersion) error {
	was := ver.Status
	ver.Status = database.BookVersionStatusPurging
	if err := store.UpdateBookVersion(ver); err != nil {
		ver.Status = was
		if errors.Is(err, database.ErrVersionPurgeClaimLost) {
			return fmt.Errorf("purge version %s: %w (%v)", ver.ID, ErrVersionNotInTrash, err)
		}
		return fmt.Errorf("purge version %s: claim it: %w", ver.ID, err)
	}
	return nil
}

// markPurged records the end of a purge.
func markPurged(store versionPurger, ver *database.BookVersion) error {
	now := time.Now()
	ver.Status = database.BookVersionStatusInactivePurged
	ver.PurgedDate = &now
	return store.UpdateBookVersion(ver)
}

// otherRowNamingPath returns a book_file row that names path and is not one of
// ver's own rows (same book, same version), or nil when none does. Retired
// rows are included: BookFilesAtPath does not filter them. Any lookup error is
// returned, so the caller fails closed.
func otherRowNamingPath(store versionPurger, path string, ver *database.BookVersion) (*database.BookFile, error) {
	rows, err := store.BookFilesAtPath(path)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].BookID == ver.BookID && rows[i].VersionID == ver.ID {
			continue
		}
		return &rows[i], nil
	}
	return nil, nil
}

// versionSlotFiles lists every non-directory entry under the version's
// .versions slot. A slot that does not exist has no files. Symlinks are listed
// as themselves, never followed.
func versionSlotFiles(bookDir, versionID string) ([]string, error) {
	slot := VersionSlotDir(bookDir, versionID)
	if _, err := os.Lstat(slot); os.IsNotExist(err) {
		return nil, nil
	}
	var files []string
	err := filepath.WalkDir(slot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

// pruneEmptyDirs removes dir and its subdirectories bottom-up, but only the
// empty ones: a directory that still holds a kept file stays, and so do its
// parents. Idempotent when dir does not exist.
func pruneEmptyDirs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			pruneEmptyDirs(filepath.Join(dir, e.Name()))
		}
	}
	// os.Remove refuses a non-empty directory, which is the check.
	_ = os.Remove(dir)
}

// CleanupTrashedVersions is the maintenance task that purges versions
// whose trash window has run out. Called by the scheduler.
//
// It reports how many it purged and how many it could not (failed), with an
// error that is non-nil exactly when failed > 0 or the trash could not be
// listed. A run in which every due purge fails (book_files flagged incomplete,
// a file that cannot be removed) must not read as "purged 0" to the operation
// that called it: the versions stay in the trash past their window and only the
// error says so.
func CleanupTrashedVersions(store TrashedVersionCleaner) (purged, failed int, err error) {
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
// A version in database.BookVersionStatusPurging (a purge that failed part way)
// is retried here at once, with no window: its window had run out when the
// purge was claimed, or the user asked for it with purge-now.
//
// The trash is small and the loop is sequential on purpose: each purge deletes
// files. The re-read below narrows the race with a restore that lands mid-run,
// and PurgeVersion's claim closes it: the claim is written under the version's
// stripe and refused when the stored version is no longer trash, so a restore
// between the re-read and the claim wins (the version is skipped, not failed)
// and a restore after the claim is refused. The one write this does not cover
// is the legacy stamp below, which a restore in the same instant can still
// turn back into a trash row with a fresh window (harmless: it is purged 14
// days later, not at once).
//
// failed counts the versions this run could not act on: a purge that failed,
// a re-read that failed, a stamp that could not be written. err is nil when
// failed is 0; otherwise it names the count and the id of EVERY failed
// version, and wraps every failure (errors.Is sees each), so one stuck version
// cannot hide a new failure behind it. When the trash cannot be listed at all,
// err is that failure and failed is 0 (no version was attempted).
func cleanupTrashedVersionsAt(store TrashedVersionCleaner, now time.Time) (purged, failed int, err error) {
	trashed, err := store.ListTrashedBookVersions()
	if err != nil {
		lifecycleLog.Warn("trash cleanup: list trashed versions failed: %v", err)
		return 0, 0, fmt.Errorf("trash cleanup: list trashed versions: %w", err)
	}
	var failures []error
	var failedIDs []string
	fail := func(id string, e error) {
		failed++
		failures = append(failures, e)
		failedIDs = append(failedIDs, id)
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
			fail(id, fmt.Errorf("re-read version %s: %w", id, err))
			continue
		}
		// Only a trashed (or purging) version is ever purged here. A retired
		// version is not trashed (MarkTrashed refuses it) and would not be
		// listed; PurgeVersion refuses it as well, so a stale or hand-edited row
		// cannot reach the disk.
		if v == nil || (v.Status != database.BookVersionStatusTrash && v.Status != database.BookVersionStatusPurging) {
			continue
		}
		if v.Status == database.BookVersionStatusPurging {
			// An unfinished purge: no window, no stamp. Retry it.
			if err := PurgeVersion(store, v); err != nil {
				lifecycleLog.Warn("trash cleanup: retrying the purge of version %s failed: %v", v.ID, err)
				fail(id, err)
				continue
			}
			purged++
			continue
		}
		if v.TrashedAt == nil {
			stamp := now
			v.TrashedAt = &stamp
			if err := store.UpdateBookVersion(v); err != nil {
				lifecycleLog.Warn("trash cleanup: version %s has no trashed-at time and stamping it failed; not purging it: %v", id, err)
				fail(id, fmt.Errorf("stamp version %s: %w", id, err))
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
			if errors.Is(err, ErrVersionNotInTrash) {
				lifecycleLog.Info("trash cleanup: version %s was restored while this run was under way; not purging it", v.ID)
				continue
			}
			lifecycleLog.Warn("trash cleanup: purging version %s failed: %v", v.ID, err)
			fail(id, err)
			continue
		}
		purged++
	}
	if stamped > 0 {
		lifecycleLog.Info("trash cleanup: stamped %d trashed version(s) that had no trashed-at time; none of them were purged", stamped)
	}
	if failed > 0 {
		lifecycleLog.Warn("trash cleanup: purged %d, failed %d of %d trashed version(s) (%s); the failed ones stay in the trash and are retried next run",
			purged, failed, len(trashed), strings.Join(failedIDs, ", "))
		return purged, failed, fmt.Errorf("trash cleanup: %d of %d trashed version(s) could not be handled (%d purged); failed versions %s: %w",
			failed, len(trashed), purged, strings.Join(failedIDs, ", "), errors.Join(failures...))
	}
	return purged, 0, nil
}
