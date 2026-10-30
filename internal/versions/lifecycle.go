// file: internal/versions/lifecycle.go
// version: 1.18.3
// guid: 5a3b4c0d-6e7f-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-10-30
//
// Version lifecycle operations (spec 3.1 task 6).
//
// Lifecycle states:
//   active → alt → trash → purging → inactive_purged → (hard delete)
//                             ↓  ↑
//                        purge_failed → (hard delete)
//
// purging is the claim PurgeVersion takes before it removes the first file, and
// only when it is going to remove one: a trashed version with nothing removable
// stays in the trash. A purge that finishes passes through it in one call; one
// that fails part way leaves the version there, where it cannot be restored
// (some files are gone) and where the nightly cleanup and purge-now retry it.
//
// purge_failed is where a purge ends when a file can never be removed
// (permission, a read-only filesystem), and where a purging version is moved
// when PurgeUndecidedEscalateAfter attempts in a row settled nothing (a path
// that cannot be told apart from a dataset that is down): without it the version
// would sit in purging, refused by restore and by hard delete, for ever. It
// records the files and reasons on the version, is counted by the nightly
// cleanup as needing attention (not retried), and is left by purge-now (a retry,
// back through purging), or by hard delete.
//
// Delete puts a version in trash (14-day TTL, counted from TrashedAt --
// when it entered the trash, not when it was created). The active version is
// not trashed in place: the server first swaps another version in (PickSwapTarget
// chooses it, RunVersionSwap moves the files), then trashes the one that left.
// Restore
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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
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

// ErrVersionAlreadyPurged accompanies ErrVersionNotInTrash (errors.Is sees both)
// when the version the caller meant to purge is already inactive_purged: another
// purge (the nightly cleanup, a second purge-now) finished it after the caller
// read it. Nothing was deleted by this call and nothing is left to delete, which
// is not the same news as "the version was restored", so purge-now words it
// differently.
var ErrVersionAlreadyPurged = errors.New("version was already purged")

// ErrVersionDeleted accompanies ErrVersionNotInTrash (errors.Is sees both) when
// the version the caller meant to purge no longer exists: it was hard-deleted
// after the caller read it. Nothing was deleted by this call; purge-now words it
// as "deleted", not as "restored".
var ErrVersionDeleted = errors.New("version was deleted")

// ErrRetiredVersion is returned by the lifecycle steps that refuse a retired
// version: MarkTrashed, MarkRestored and PurgeVersion. Callers branch on it
// with errors.Is; the server answers it with a 409. RunVersionSwap refuses it
// too, on either of its two versions, before it writes anything. PickSwapTarget
// never returns a retired version (only an alt is a candidate), so it does not
// produce it.
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

// refusePurging refuses a lifecycle step on a version whose purge has started
// (purging) or gave up (purge_failed).
func refusePurging(v *database.BookVersion, action string) error {
	if v == nil {
		return nil
	}
	switch v.Status {
	case database.BookVersionStatusPurging:
		return fmt.Errorf("%s version %s: %w (an earlier purge removed some of its files; "+
			"the nightly cleanup or Purge now finishes it)", action, v.ID, database.ErrVersionPurging)
	case database.BookVersionStatusPurgeFailed:
		return fmt.Errorf("%s version %s: %w (an earlier purge removed some of its files and could not remove the rest; "+
			"Purge now retries it, or it can be hard-deleted)", action, v.ID, database.ErrVersionPurging)
	}
	return nil
}

// ErrVersionSwapping is returned by MarkTrashed for a version in swapping_out or
// swapping_in: a swap that stopped half way (RunVersionSwap returned
// ErrSwapIncomplete, or the process died). Its files may be in two places and the
// book's file_path and rows may name either, so the version is not the caller's
// to trash: 14 days later the cleanup would delete files the book plays. The
// server answers it with a 409; ResumeVersionSwap repairs the book.
var ErrVersionSwapping = errors.New("the version is part of a swap that did not finish")

// refuseSwapping refuses a lifecycle step on a version whose swap has started
// and not finished (swapping_out, swapping_in).
func refuseSwapping(v *database.BookVersion, action string) error {
	if v == nil {
		return nil
	}
	if v.Status == database.BookVersionStatusSwappingOut || v.Status == database.BookVersionStatusSwappingIn {
		return fmt.Errorf("%s version %s: %w (it is %s: a swap of this book stopped half way and its files may be in two places; "+
			"it needs ResumeVersionSwap, and trashing it could lead to the files the book plays being deleted)", action, v.ID, ErrVersionSwapping, v.Status)
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
// refuses a purging version (database.ErrVersionPurging) and a purged one
// (database.ErrVersionPurged): its files are gone, and a trash row would start a
// clock that ends in a purge of nothing. It refuses a version in swapping_out or
// swapping_in (ErrVersionSwapping): a swap that stopped half way is not undone by
// trashing the version it left, which is what a retried DELETE would do.
func MarkTrashed(v *database.BookVersion, now time.Time) error {
	if err := refuseRetired(v, "trash"); err != nil {
		return err
	}
	if err := refusePurging(v, "trash"); err != nil {
		return err
	}
	if err := refuseSwapping(v, "trash"); err != nil {
		return err
	}
	if v.Status == database.BookVersionStatusInactivePurged {
		return fmt.Errorf("trash version %s: %w (its files are already gone)", v.ID, database.ErrVersionPurged)
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

// BookServesFilesError is what PurgeVersion returns, having deleted nothing,
// when a live book still serves one of the version's files: the version's own
// book (a file outside this version's own .versions/{id}/ slot, the book's
// FilePath or folder, a row of its active or no version, the same file by
// symlink or hard link) or ANOTHER live book (see servedGuard). It is not a
// failure of the purge machinery: the version stays in the trash, files intact
// and restorable, never claimed, and a later run tries again.
//
// How it happens: trashing the ACTIVE version used to flip its status and
// nothing else. Its files stayed where the book plays them (book.FilePath and
// the book_file rows of the version), so 14 days later the purge was deleting
// the playable files and leaving the book pointing at a missing file. The trash
// handler now swaps another version in first (PickSwapTarget, RunVersionSwap);
// this guard stays as the second line, for a version that became active again
// or was never swapped out.
type BookServesFilesError struct {
	BookID    string
	VersionID string
	// Paths are the version's files a live book still serves.
	Paths []string
}

func (e *BookServesFilesError) Error() string {
	return fmt.Sprintf("version %s of book %s not purged: a live book still serves %d of its file(s)", e.VersionID, e.BookID, len(e.Paths))
}

// KeptReason is the journal and result text for a version left in the trash by
// the served-file guard.
const KeptReason = "book still serves this file"

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

// PurgeDecision is what a PurgeDecider says about one path.
type PurgeDecision int

const (
	// PurgeRemove: the path is a file (or an empty directory) that may be
	// unlinked now.
	PurgeRemove PurgeDecision = iota
	// PurgeGone: nothing is there, and the folder it would be in vouches for
	// that (it is there and holds something).
	PurgeGone
	// PurgeKeep: the path must not be removed, and that will not change by
	// asking again (a protected path, outside the library root, a directory
	// that still holds files). The reason is recorded on the version
	// (BookVersion.PurgeKept) and the purge goes on without it.
	PurgeKeep
)

// PurgeGuard is the book purge's safety, as a version purge asks it: the
// library-root and per-path checks that live in internal/audiobooks
// (AudiobookService.VersionPurgeGuard implements it). A version purge deletes
// files from disk, and it did so with none of them until 2026-10-27: a file
// under an import path or the iTunes library, or the "missing" files of a
// library that is not mounted (ENOENT counted as removed), were all its to
// delete. It is a parameter, not an import, because internal/audiobooks is the
// one place those checks are kept and the book purge and this one must not
// disagree about them.
type PurgeGuard interface {
	// RootReady reports whether the library looks mounted (the root exists and
	// most of a sample of its live files can be seen). PurgeVersion asks it
	// before it takes the merge lock, as the book purge does, so the stats do
	// not run under it; a purge on a library that is not ready removes
	// nothing and claims nothing.
	RootReady() error
	// NewDecider starts the decisions of ONE purge attempt. It takes no folders
	// to exempt: a folder that is empty is "cannot tell" to every path in it
	// that was not itself unlinked and recorded, because an empty folder is also
	// how the mount point of a dataset that is not mounted looks.
	NewDecider() PurgeDecider
}

// PurgeDecider decides the paths of one purge attempt. Decide is read-only, so
// PurgeVersion asks it for every path BEFORE it claims the version. A non-nil
// error from it is a question it could not answer now (a stat that failed, a
// folder that cannot vouch for "gone"): the path is left alone, and the version
// is retried.
type PurgeDecider interface {
	Decide(path string) (decision PurgeDecision, reason string, err error)
	// InsideRoot says whether dir is strictly inside the library root once its
	// symlinks are resolved (nil when it is). The purge prunes the empty folders
	// of a version's slot only when it is: a slot that resolves outside the
	// library is not the purge's to tidy.
	InsideRoot(dir string) error
}

// ErrPurgeFailed is returned by PurgeVersion when a file could not be removed
// for a reason retrying will not change (permission, a read-only filesystem),
// or when the purge has made no progress for PurgeUndecidedEscalateAfter
// attempts in a row: the version is now in
// database.BookVersionStatusPurgeFailed, with the files and reasons on it, and
// is no longer retried by the nightly cleanup. It can be hard-deleted.
var ErrPurgeFailed = errors.New("a file of the version cannot be removed and retrying will not change that")

// ErrPurgeCannotTell is what a *CannotTellError matches (errors.Is): the purge
// removed nothing because the paths it left are not there and nothing vouches
// that they were removed (their folder is missing or empty, which is also how a
// dataset that is not mounted looks). The version is untouched, or still purging
// with its attempt counted; it is not a failure of the version.
var ErrPurgeCannotTell = errors.New("cannot tell whether a file of the version is gone")

// CannotTellError carries the paths of an ErrPurgeCannotTell and why each could
// not be settled.
type CannotTellError struct {
	VersionID string
	Files     []database.PurgeFileNote
	msg       string
}

func (e *CannotTellError) Error() string {
	return fmt.Sprintf("%s: %v: %s", e.msg, ErrPurgeCannotTell, notePaths(e.Files))
}

func (e *CannotTellError) Is(target error) bool { return target == ErrPurgeCannotTell }

// ErrPurgeNothingServed is what a *NothingServedError matches (errors.Is): the
// purge of a trashed version deleted nothing because the book does not serve a
// single file that exists on disk, so the guard has nothing to compare the
// version's files against. It is not ErrPurgeCannotTell: nothing here is about a
// drive that may be down or a purge that may have finished; the book's own path
// is wrong (a stale or lost FilePath, an empty folder) and only fixing the book,
// or restoring the version, lets the purge go on.
var ErrPurgeNothingServed = errors.New("the book serves nothing that exists on disk")

// NothingServedError carries the book's path and the paths it serves, with why
// none of them counts, for an ErrPurgeNothingServed.
type NothingServedError struct {
	VersionID string
	BookID    string
	// BookPath is the book's FilePath: what to fix. Empty when the book has none.
	BookPath string
	// Files are the paths the book is taken to serve (its FilePath and the rows
	// of its active version), each with why it does not count.
	Files []database.PurgeFileNote
	// Attempt is, for a version that is purging (it cannot go back to the trash),
	// how many attempts in a row this one makes (BookVersion.PurgeUndecidedNights):
	// the refusal is counted like a stuck attempt (settleStuck). Zero for a trashed
	// or purge_failed version, which is not counted.
	Attempt int
	// Escalated is set on the attempt that reached PurgeUndecidedEscalateAfter: the
	// version is now purge_failed (nothing was deleted to get there), so the error
	// also matches ErrPurgeFailed.
	Escalated bool
}

func (e *NothingServedError) Error() string {
	where := "the book has no file path"
	if e.BookPath != "" {
		where = fmt.Sprintf("the book's path is %s", e.BookPath)
	}
	switch {
	case e.Escalated:
		return fmt.Sprintf("version %s of book %s not purged: %v (%s); %d attempts in a row found nothing the book serves, so the version is now purge_failed (nothing was deleted), no longer retried every night, and can be hard-deleted or retried with Purge now once the book's path is fixed: %s",
			e.VersionID, e.BookID, ErrPurgeNothingServed, where, e.Attempt, notePaths(e.Files))
	case e.Attempt > 0:
		return fmt.Sprintf("version %s of book %s not purged: %v (%s); attempt %d of %d, after which the version becomes purge_failed (nothing is deleted) and can be hard-deleted; fixing the book's path lets the purge finish: %s",
			e.VersionID, e.BookID, ErrPurgeNothingServed, where, e.Attempt, PurgeUndecidedEscalateAfter, notePaths(e.Files))
	}
	return fmt.Sprintf("version %s of book %s not purged: %v (%s); fix the book's path, or restore the version: %s",
		e.VersionID, e.BookID, ErrPurgeNothingServed, where, notePaths(e.Files))
}

func (e *NothingServedError) Is(target error) bool {
	return target == ErrPurgeNothingServed || (e.Escalated && target == ErrPurgeFailed)
}

// PurgeUndecidedEscalateAfter is how many purge attempts in a row, while the
// version is purging, may leave a path unsettled with nothing unlinked before the
// version moves to purge_failed. The nightly cleanup is one attempt a night, so
// this is three nights, the same count as the merge-stop streak
// (MergeStopEscalateAfter); a Purge now click runs but adds no night on a day
// that already counted one (nextNightCount). It is the exit
// from a purging version that can never finish: purging refuses restore and hard
// delete, purge_failed can be hard-deleted.
const PurgeUndecidedEscalateAfter = 3

// purgeClock is the clock settleStuck reads for the calendar day an attempt is
// counted on. A variable only so a test can move the day; production leaves it.
var purgeClock = time.Now

// SetPurgeClockForTest replaces the clock the purge counts nights on and
// returns the function that restores it. It exists for tests in other packages
// (the server's purge-now route tests) that need several attempts to fall on
// different calendar days; production never calls it.
func SetPurgeClockForTest(now func() time.Time) (restore func()) {
	old := purgeClock
	purgeClock = now
	return func() { purgeClock = old }
}

// nextNightCount is the unsettled-night count after one more unsettled attempt
// on the calendar day today (YYYY-MM-DD), given the count was and the day wasOn
// it last grew. An attempt on the day the count last grew leaves it where it is;
// any other day adds one. Shared by the purge (settleStuck) and the kept-file
// sweep (settleNights) so a night means the same thing to both.
func nextNightCount(was int, wasOn, today string) (nights int, on string) {
	if wasOn == today && was > 0 {
		return was, wasOn
	}
	return was + 1, today
}

// BackgroundMergeLockWait is how long the nightly cleanup waits, per version,
// for a running merge or combine to release the merge lock before it gives up
// on that version for the night (merge.ErrMergeBusy; the next run retries it).
// A var only so a test can shorten it.
//
// The wait is a ceiling, not a guarantee. merge.TryLockMergeRMWWithin polls
// sync.Mutex.TryLock every 10ms, and TryLock never takes a mutex that is in
// starvation mode (the runtime switches to it once a blocked Lock() caller has
// waited more than 1ms, and then hands the mutex straight to the head of the
// queue). While blocking callers keep queuing on the merge lock, a poller can
// fail for the whole wait even though the lock changes hands many times, and
// the night is reported as StoppedForMerge. That is why the cleanup counts
// consecutive stopped nights (MergeStopEscalateAfter) and raises them instead
// of trusting the next night to get through.
var BackgroundMergeLockWait = 10 * time.Minute

// MergeStopEscalateAfter is how many nights in a row the cleanup may stop for
// the merge lock before it reports the pattern (CleanupResult.MergeStopStreak)
// as a warning in the operation result and log. One busy night is a combine
// that ran long; three is something holding the lock at 06:00 every day, and
// the trash is not being purged. It is a warning, not a failure: the op stays
// green, as StoppedForMerge always has.
const MergeStopEscalateAfter = 3

// mergeStopStreakKey is where the cleanup keeps the number of consecutive
// nights it stopped for the merge lock (a decimal integer in the store's raw
// key-value space, found with database.AsCapability[database.RawKVStore]).
const mergeStopStreakKey = "versions:trash_cleanup:merge_stop_streak"

// mergeStopStreakUnavailableOnce keeps the warning that the streak cannot be kept
// (a store without database.RawKVStore) to one line per process.
var mergeStopStreakUnavailableOnce sync.Once

// removeFile is os.Remove; a variable so a test can make a removal fail the
// way a read-only filesystem does.
var removeFile = os.Remove

// PurgeVersion physically deletes the version's files and marks it
// inactive_purged. Keeps the DB rows for fingerprint matching. It is
// PurgeVersionWithin for a background caller (the nightly cleanup), which waits
// up to BackgroundMergeLockWait for the merge lock.
func PurgeVersion(store versionPurger, ver *database.BookVersion, guard PurgeGuard) error {
	return PurgeVersionWithin(store, ver, guard, BackgroundMergeLockWait)
}

// PurgeVersionWithin is PurgeVersion with the time it will wait for the merge
// lock passed in (an interactive caller passes merge.InteractiveMergeLockWait
// and answers merge.ErrMergeBusy, which the error wraps, instead of hanging
// behind a combine).
//
// It refuses a retired version (ErrRetiredVersion) before touching the disk.
// The refusal has to come first: the file removal below happens before
// UpdateBookVersion, whose own refusal comes too late, and the files of a
// retired version belong to a book the library still keeps.
//
// It never removes a file a live book still serves (servedGuard has the rules).
// While the version's own book is live, only files inside this version's own
// .versions/{id}/ slot are purgeable at all: trashing never moves files, so a
// file of the book outside the slot is still where the book plays it. That is
// the first question of the read-only pass below, asked of every path before the
// claim: if any path of a TRASHED version is served the purge deletes nothing,
// claims nothing, leaves the whole version in the trash and returns a
// *BookServesFilesError (the nightly cleanup lists it under CleanupResult.Kept).
// It fails closed: a book, version list or row list that cannot be read is an
// error, never "not served". A version that is already purging or purge_failed
// has removed files and cannot go back to the trash, so a path a book has come
// to serve since is kept on disk like any other kept path and the purge goes on.
//
// A trashed version is also not purged while the book is not known to serve
// anything that exists: when none of the paths the book serves (its FilePath, the
// rows of its active version) is on disk, or the only one is a folder with no
// file in it, the guard has nothing to compare against, and "nothing served"
// would be a guess. The purge then returns a *NothingServedError (errors.Is
// ErrPurgeNothingServed, not ErrPurgeCannotTell: the book's path is what is
// wrong, not a drive) and deletes nothing (servedGuard.requireServedFileOnDisk).
// A trashed version stays in the trash, restorable. A purge_failed version stays
// purge_failed (it can be hard-deleted). A purging version cannot go back to the
// trash and refuses restore and hard delete, so the refusal is counted like a
// stuck attempt (settleNothingServed): after PurgeUndecidedEscalateAfter in a row
// it becomes purge_failed, with the files it could not decide in PurgeFailed, and
// the error of that attempt also matches ErrPurgeFailed. The rule is the kept-file sweep's too,
// so a retry of a version that sweep escalated cannot delete what it refused to.
//
// A retry (purging, purge_failed) also decides every file the version's notes name
// (PurgeKept, PurgeFailed), not only the files of its slot and rows: a note that
// the retry did not ask about again would be erased with its file still on disk.
// A purge deletes only inside the version's own .versions/{id}/ slot: a noted
// file outside it is never removed, whether or not the version's own book still
// exists (classifyNotedPath, OutsideSlotNeverAutoRemovedReason). It is kept, noted
// again and listed, never dropped.
//
// It never removes a file that some other book_file row still names. A
// retired version keeps the rows of a book that was retired into this one's
// book, and those rows can name the very paths this version's own rows name
// (PLAN section 5.1 rule 4: the holder's active rows hold every path of the
// shell), so purging this version must leave those files alone. Owners are
// every other book_file row (retired ones included) and every other book row
// whose own FilePath is the path, a trashed (restorable) book included, the
// same two questions the book purge asks at removal time (audiobooks
// otherBookRowAt): a book can name a file with no row at all. The book lookup
// is database.BookRowPathReader, found with database.AsCapability; a store that
// does not provide it fails the purge closed like a lookup error does. The
// version's own book is not another owner.
//
// The owners and the disk are asked twice. First for EVERY path, read-only,
// before the claim: a path whose owners cannot be listed
// (database.ErrBookFilesAtPathUnavailable) aborts the purge with nothing deleted
// and the version unchanged, and so does a version with nothing removable. Then
// again for each path immediately before its removal, as the book purge does,
// because a row can be written between the decision and the unlink. The merge
// lock is held from before the first decision to the end, the lock the writers
// of book_file rows (the repoint fixers, retire) hold, so a merge-family writer
// cannot add an owner in the middle; a writer that does not take it (the library
// scan) is caught by the second question, for all but the instant between it and
// the unlink.
//
// Every path is also put to the guard (PurgeGuard): not a protected path (an
// import path, the iTunes library, .failed), inside the library root, and a
// "not there" counts as removed only when something vouches for it. A path the
// guard or an owner keeps is not removed and is recorded on the version
// (database.BookVersion.PurgeKept), which still ends inactive_purged: the caller
// (the nightly cleanup, the purge-now handler) reports the kept files instead of
// presenting the purge as complete.
//
// What vouches that a missing path is gone, and nothing else:
//
//   - the path is in BookVersion.PurgeUnlinked: an earlier attempt of this purge
//     unlinked it and recorded that. A recorded path that reads "not there" is
//     gone, path by path; its folder is never trusted for its neighbours, so a
//     neighbour this purge never unlinked, sitting in a folder that is empty
//     because its dataset is down, is not called gone; or
//   - the guard says so (PurgeGone): the folder is there and holds something.
//
// Anything else that is not there is "cannot tell". A path found missing when the
// unlink runs (it was there at the decision) is "cannot tell" too, and is never
// recorded: only a real unlink is.
//
// The version is claimed only when the purge is going to unlink something: it is
// written as purging (database.BookVersionStatusPurging), under the version's
// stripe, and the write is refused with ErrVersionNotInTrash when the stored
// version is no longer trash, purging or purge_failed, so a restore that landed
// after the caller read the version wins and nothing is deleted. From then on a
// restore is refused (database.ErrVersionPurging). A trashed version with
// nothing removable stays in the trash, restorable: the error is a
// *CannotTellError (errors.Is ErrPurgeCannotTell) naming the paths and why, when
// all that is in the way is "cannot tell", and the version is not touched. The
// same holds, with a *NothingServedError (errors.Is ErrPurgeNothingServed), when
// the book serves nothing that exists on disk.
//
// A file that cannot be removed or decided leaves the version purging when the
// failure may pass (a lookup, a stat, EIO): marking it purged would orphan the
// file, because nothing looks at a purged version's disk again; left purging it
// is retried by the next cleanup run (and by purge-now), and a file the first
// attempt already removed is simply gone then. It is purging, not trash, because
// some of its files are already gone and a restore would hand back an alt that
// names them. Once a removal's record write fails the purge stops unlinking
// (the remaining paths are untouched). When every failure is one retrying will
// not change (permission, a read-only filesystem, a directory where a file was
// expected: removeErrorIsPermanent) the version ends in
// database.BookVersionStatusPurgeFailed instead, with the paths and reasons
// recorded, and the error wraps ErrPurgeFailed; the nightly cleanup does not
// retry it, the owner can retry it or hard-delete it.
//
// A purging version whose attempts keep ending with a path unsettled and nothing
// unlinked (the file is gone but its unlink was never recorded, a dataset that is
// down for days) is counted (BookVersion.PurgeUndecidedNights) and, at
// PurgeUndecidedEscalateAfter attempts in a row, moved to purge_failed with the
// paths and the reason "cannot tell whether gone": that is the way out, because
// purge_failed can be hard-deleted and purging cannot. The folders a removal
// emptied are pruned only when the purge completes, and only those inside the
// library root.
//
// A version whose book no longer exists has no row to name its files, and its
// slot is found through the book's FilePath: the book's tombstone
// (database.GetBookTombstone) still has it, and the slot's files are removed
// with the same checks. With no tombstone there is nothing to find, and the
// version is marked purged with the files it could not locate unreported (a
// warning is logged): DeleteBook refuses while rows exist, so there are no
// rows to name them. A book that cannot be READ is a different thing: that is
// an error, the version is untouched, and the next run retries it.
func PurgeVersionWithin(store versionPurger, ver *database.BookVersion, guard PurgeGuard, wait time.Duration) error {
	if err := refuseRetired(ver, "purge"); err != nil {
		return err
	}
	if guard == nil {
		return fmt.Errorf("purge version %s: no purge guard is configured, so nothing is deleted", ver.ID)
	}
	// The library-root check makes stats that can take seconds: before the lock.
	if err := guard.RootReady(); err != nil {
		return fmt.Errorf("purge version %s: %w", ver.ID, err)
	}
	unlock, err := merge.TryLockMergeRMWWithin(wait)
	if err != nil {
		return fmt.Errorf("purge version %s: %w", ver.ID, err)
	}
	defer unlock()
	// The first check above ran before a wait that can last ten minutes: the
	// library can be unmounted in that time (or a mount point that was up goes
	// down), and every path would then fail to resolve inside a root that is not
	// there. It is asked again with the lock held, so a purge that waited does
	// not decide a single path on a mount that is no longer ready.
	if err := guard.RootReady(); err != nil {
		return fmt.Errorf("purge version %s: %w", ver.ID, err)
	}
	return purgeVersionLocked(store, ver, guard)
}

// purgeVersionLocked is PurgeVersionWithin with the merge lock held.
//
// The caller's copy of the version was read before the lock wait, which can
// last minutes, so it is read again here and ver is refreshed from the stored
// row: the status that decides whether this is a retry, the PurgeUnlinked
// record a retry trusts path by path, and the row the claim writes back all
// come from after the wait, not from before it. A version that is no longer
// the trash's by then is refused with ErrVersionNotInTrash (and
// ErrVersionAlreadyPurged when another purge finished it).
func purgeVersionLocked(store versionPurger, ver *database.BookVersion, guard PurgeGuard) error {
	fresh, err := store.GetBookVersion(ver.ID)
	if err != nil {
		return fmt.Errorf("purge version %s: read it again under the merge lock: %w", ver.ID, err)
	}
	if fresh == nil {
		return fmt.Errorf("purge version %s: %w: %w (it was hard-deleted while this purge waited for the merge lock)", ver.ID, ErrVersionNotInTrash, ErrVersionDeleted)
	}
	if err := refuseRetired(fresh, "purge"); err != nil {
		return err
	}
	switch fresh.Status {
	case database.BookVersionStatusTrash, database.BookVersionStatusPurging, database.BookVersionStatusPurgeFailed:
	case database.BookVersionStatusInactivePurged:
		return fmt.Errorf("purge version %s: %w: %w (it was purged while this purge waited for the merge lock)", ver.ID, ErrVersionNotInTrash, ErrVersionAlreadyPurged)
	default:
		return fmt.Errorf("purge version %s: %w (it is %s now)", ver.ID, ErrVersionNotInTrash, fresh.Status)
	}
	*ver = *fresh
	book, bookDir, noSlot, err := versionBookDir(store, ver)
	if err != nil {
		return fmt.Errorf("purge version %s: %w", ver.ID, err)
	}
	if noSlot {
		lifecycleLog.Warn("purge version %s: its book %s no longer exists and has no tombstone, so the version's slot cannot be located; marking the version purged",
			ver.ID, ver.BookID)
	}

	// Everything this purge would delete: the files under the version's slot,
	// plus any file directly associated with this version by a row.
	own, err := store.GetBookFiles(ver.BookID)
	if err != nil {
		return fmt.Errorf("purge version %s: list book files: %w", ver.ID, err)
	}
	// One entry per FILE, not per spelling of it: a slot walk that reaches a
	// file through a symlinked library root and a row that names it through the
	// real path are the same target, and counting it twice would report it twice
	// (and unlink it twice). The first spelling met is the one kept: the slot
	// walk's, then the rows'.
	targets := make(map[string]string)
	addTarget := func(p string) {
		k := targetKey(p)
		if _, ok := targets[k]; !ok {
			targets[k] = p
		}
	}
	if bookDir != "" {
		slotFiles, err := versionSlotFiles(bookDir, ver.ID)
		if err != nil {
			return fmt.Errorf("purge version %s: list slot: %w", ver.ID, err)
		}
		for _, p := range slotFiles {
			addTarget(p)
		}
	}
	for _, f := range own {
		if f.VersionID == ver.ID && f.FilePath != "" {
			addTarget(f.FilePath)
		}
	}
	// A retry (purging, purge_failed) also owes every file the version's notes
	// name: PurgeKept (files an earlier attempt, or the kept-file sweep, left
	// because something owned them) and PurgeFailed (files that could not be
	// removed or settled). The slot walk and the rows do not find them (they lie
	// outside the slot, or the rows are gone), and finishPurge replaces PurgeKept
	// and markPurged clears PurgeFailed, so a note that was not asked about again
	// would be erased with its file still on disk. Asked here, each is removed,
	// found gone, kept (and so noted again) or left undecided; none is dropped
	// unasked. noteOnly marks those found only through a note: they are decided
	// by classifyNotedPath.
	noteOnly := map[string]struct{}{}
	if ver.Status == database.BookVersionStatusPurging || ver.Status == database.BookVersionStatusPurgeFailed {
		for _, n := range slices.Concat(ver.PurgeKept, ver.PurgeFailed) {
			if n.Path == "" {
				continue
			}
			k := targetKey(n.Path)
			if _, ok := targets[k]; ok {
				continue
			}
			targets[k] = n.Path
			noteOnly[k] = struct{}{}
		}
	}
	paths := make([]string, 0, len(targets))
	for _, p := range targets {
		paths = append(paths, p)
	}
	slices.Sort(paths)

	// Whether a live book still serves a path is asked of the same state the
	// owners are: read-only, for every path, before the claim.
	sg, err := newServedGuard(store, book, ver.BookID, own, ver.ID)
	if err != nil {
		return fmt.Errorf("purge version %s: %w", ver.ID, err)
	}
	// Decide EVERY path first, read-only, before the version is claimed: a
	// lookup failure, or a state nothing can be removed from, leaves the version
	// exactly as it was (a trashed version stays restorable trash). Only a purge
	// that is going to unlink something claims the version.
	decider := guard.NewDecider()
	recorded := map[string]struct{}{}
	for _, p := range ver.PurgeUnlinked {
		recorded[p] = struct{}{}
	}
	classify := func(p string) (pathVerdict, string, error) {
		if _, ok := noteOnly[targetKey(p)]; ok {
			return classifyNotedPath(store, decider, sg, ver, p, recorded)
		}
		return classifyPurgePath(store, decider, sg, ver, p, recorded)
	}
	var doomed []string
	kept := map[string]string{}
	var undecided []database.PurgeFileNote
	var served []string
	for _, p := range paths {
		verdict, why, err := classify(p)
		if err != nil {
			return fmt.Errorf("purge version %s: %w", ver.ID, err)
		}
		switch verdict {
		case verdictRemove:
			doomed = append(doomed, p)
		case verdictServed:
			served = append(served, p)
		case verdictKept:
			lifecycleLog.Warn("purge version %s: keeping %s: %s", ver.ID, p, why)
			kept[p] = why
		case verdictUndecided:
			lifecycleLog.Warn("purge version %s: cannot tell whether %s is gone yet: %s", ver.ID, p, why)
			undecided = append(undecided, database.PurgeFileNote{Path: p, Reason: why})
		}
	}

	if len(served) > 0 {
		if ver.Status == database.BookVersionStatusTrash {
			// A live book still plays at least one of these files: delete nothing,
			// claim nothing, and leave the whole version in the trash, restorable.
			// The whole version and not just the served files: a half-purged alt
			// is not something a restore can hand back.
			lifecycleLog.Warn("trash purge kept version %s of book %s in the trash: %d path(s) kept, reason: %s", ver.ID, ver.BookID, len(served), KeptReason)
			return &BookServesFilesError{BookID: ver.BookID, VersionID: ver.ID, Paths: served}
		}
		// A purge that is already under way (purging, purge_failed) has removed
		// files and cannot be unwound; a path the book has come to serve since is
		// left on disk like any other kept path, and the purge goes on with the
		// rest.
		for _, p := range served {
			lifecycleLog.Warn("purge version %s: keeping %s: %s (the purge was already under way)", ver.ID, p, KeptReason)
			kept[p] = KeptReason
		}
	}

	// A version is deleted only while the book is known to serve a file that
	// exists: with none, the guard has nothing to compare against (a stale
	// FilePath, a lost file) and "nothing served" would be a guess. It comes
	// after the served files above, so a file the book is found to serve still
	// answers BookServesFilesError. It holds for a purge that is already under
	// way (purging, purge_failed) as well: an escalated version is retried by
	// Purge now, and the kept-file sweep refuses to remove anything while the
	// book serves nothing on disk, so a retry must not delete what the sweep
	// would not. A trashed version is left in the trash, restorable, and a
	// purge_failed one as it is (it can be hard-deleted). A purging version
	// cannot go back to the trash and refuses restore and hard delete, so the
	// refusal is counted like a stuck attempt and, after
	// PurgeUndecidedEscalateAfter in a row, moves it to purge_failed
	// (settleNothingServed); that deletes nothing.
	if len(doomed) > 0 {
		if err := sg.requireServedFileOnDisk(); err != nil {
			lifecycleLog.Warn("purge kept version %s (%s) of book %s untouched: %v", ver.ID, ver.Status, ver.BookID, err)
			return settleNothingServed(store, ver, kept, doomed, undecided, err)
		}
	}

	if len(doomed) == 0 {
		if len(undecided) > 0 {
			// Nothing to remove and something that cannot be told apart from a
			// dataset that is down. No claim: a trashed version stays trash (the
			// owner can still restore it), and a version that is already purging
			// counts the night (settleStuck).
			return settleStuck(store, ver, kept, stuckPaths{undecided: undecided}, 0, len(paths))
		}
		// Every path is gone or kept: nothing to unlink, the purge only finishes.
		if err := claimPurge(store, ver); err != nil {
			return err
		}
		return finishPurge(store, ver, decider, bookDir, kept)
	}

	// Claim the version before the first removal. Everything above only reads.
	if err := claimPurge(store, ver); err != nil {
		return err
	}

	// The served guard above was built from the book and rows as they were
	// before the claim. Each removal below asks it again, so it is rebuilt now,
	// from what the store says after the claim: a book that came to serve a path
	// (a FilePath re-pointed, a row written, a version promoted) while the
	// version was being claimed is seen at the unlink and not only in the pass
	// that has already ended. A rebuild that cannot read is a lookup that cannot
	// answer: nothing is unlinked, and the version, now purging, counts the
	// attempt like any other stuck purge.
	sg, err = rebuildServedGuard(store, ver)
	if err != nil {
		var stuck stuckPaths
		stuck.undecided = undecided
		for _, p := range doomed {
			stuck.failed = append(stuck.failed, stuckFailure{path: p, err: err})
		}
		return settleStuck(store, ver, kept, stuck, 0, len(paths))
	}

	// recordUnlinked persists the removal of p before anything else happens to
	// the version: after every removal, not once at the end, because the record
	// is what a retry trusts, path by path. Written AFTER the unlink, so a crash
	// between the two leaves an unrecorded path (the retry then cannot call it
	// gone unless its folder vouches for it, and escalates if it never can) and
	// can never leave an unremoved path recorded. A version holds a handful of
	// files, so a write each is cheap next to the unlink it follows.
	recordUnlinked := func(p string) error {
		if !slices.Contains(ver.PurgeUnlinked, p) {
			ver.PurgeUnlinked = append(ver.PurgeUnlinked, p)
		}
		if err := store.UpdateBookVersion(ver); err != nil {
			return fmt.Errorf("%s was removed but recording that failed: %w", p, err)
		}
		return nil
	}
	var stuck stuckPaths
	stuck.undecided = undecided
	var permanent []error
	var failedNotes []database.PurgeFileNote
	unlinked := 0
loop:
	for i, p := range doomed {
		// The owners and the decision again, for this path, right before its
		// removal: a row can have been written, or a mount gone, since the pass
		// above.
		verdict, why, err := classify(p)
		if err != nil {
			stuck.failed = append(stuck.failed, stuckFailure{path: p, err: err})
			continue
		}
		switch verdict {
		case verdictServed:
			// A book started serving it after the claim: the version cannot go back
			// to the trash now, so this path stays on disk like any kept path.
			lifecycleLog.Warn("purge version %s: keeping %s: %s (decided since the purge began)", ver.ID, p, KeptReason)
			kept[p] = KeptReason
			continue
		case verdictKept:
			lifecycleLog.Warn("purge version %s: keeping %s: %s (decided since the purge began)", ver.ID, p, why)
			kept[p] = why
			continue
		case verdictUndecided:
			lifecycleLog.Warn("purge version %s: cannot tell whether %s is gone yet: %s", ver.ID, p, why)
			stuck.undecided = append(stuck.undecided, database.PurgeFileNote{Path: p, Reason: why})
			continue
		case verdictGone:
			// Gone since the pass above, and its folder vouches for that. This
			// purge did not unlink it, so it is not recorded as unlinked.
			continue
		}
		err = removeFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			// Decide said the file was there and the unlink says it is not: it was
			// removed by someone else in between, or the filesystem under it went
			// away. This purge did not unlink it, so it is neither recorded as
			// unlinked nor called gone: it is a question for the next attempt,
			// which decides it from the disk (gone if its folder vouches, cannot
			// tell if it does not).
			lifecycleLog.Warn("purge version %s: %s was not there to remove; not counted as removed, decided again next attempt", ver.ID, p)
			stuck.undecided = append(stuck.undecided, database.PurgeFileNote{Path: p,
				Reason: "it was not there to remove, and this purge did not unlink it, so it cannot tell whether it is gone"})
			continue
		}
		if err != nil {
			lifecycleLog.Warn("purge version %s: remove %s failed: %v", ver.ID, p, err)
			e := fmt.Errorf("remove %s: %w", p, err)
			if removeErrorIsPermanent(err) {
				permanent = append(permanent, e)
				failedNotes = append(failedNotes, database.PurgeFileNote{Path: p, Reason: err.Error()})
			} else {
				stuck.failed = append(stuck.failed, stuckFailure{path: p, err: e})
			}
			continue
		}
		unlinked++
		if err := recordUnlinked(p); err != nil {
			// The write that records removals failed: stop here. Every path after
			// this one is left untouched, because a removal that cannot be
			// recorded is a removal the retry cannot trust, and the next attempt
			// is better placed to make them once the store answers again.
			lifecycleLog.Warn("purge version %s: %v; stopping, %d path(s) left untouched", ver.ID, err, len(doomed)-i-1)
			stuck.failed = append(stuck.failed, stuckFailure{path: p, err: err})
			for _, rest := range doomed[i+1:] {
				stuck.failed = append(stuck.failed, stuckFailure{path: rest,
					err: errors.New("not attempted: recording the removal before it failed")})
			}
			break loop
		}
	}

	switch {
	case stuck.any():
		return settleStuck(store, ver, kept, stuck, unlinked, len(doomed))
	case len(permanent) > 0:
		now := time.Now()
		ver.Status = database.BookVersionStatusPurgeFailed
		ver.PurgeFailed = failedNotes
		ver.PurgeFailedAt = &now
		ver.PurgeKept = keptNotes(kept)
		ver.PurgeUndecidedNights = 0
		if err := store.UpdateBookVersion(ver); err != nil {
			return fmt.Errorf("purge version %s: %d file(s) cannot be removed, and recording that failed: %w", ver.ID, len(permanent), errors.Join(append(permanent, err)...))
		}
		return fmt.Errorf("purge version %s: %d of %d file(s) cannot be removed; the version is now purge_failed, no longer retried every night: %w: %w",
			ver.ID, len(permanent), len(doomed), ErrPurgeFailed, errors.Join(permanent...))
	}
	return finishPurge(store, ver, decider, bookDir, kept)
}

// finishPurge ends a purge every path of which is gone, kept or removed: the
// emptied folders of the slot are pruned (only now: a folder pruned under a
// retry would make the files that are already gone read as "its folder is
// missing too" for ever), and the version is marked purged.
func finishPurge(store versionPurger, ver *database.BookVersion, decider PurgeDecider, bookDir string, kept map[string]string) error {
	if bookDir != "" {
		pruneVersionDirs(decider, bookDir, ver.ID)
	}
	ver.PurgeKept = keptNotes(kept)
	return markPurged(store, ver)
}

// pruneVersionDirs removes the empty folders a finished purge left: the slot's,
// and the book's .versions folder when that is empty too. Each is pruned only
// when it resolves inside the library root (a slot or .versions folder that is
// a symlink to somewhere else is not the purge's to tidy: pruneEmptyDirs
// refuses a slot that is not where it says it is, and the root check refuses
// one that is outside the library).
func pruneVersionDirs(decider PurgeDecider, bookDir, versionID string) {
	if slot := VersionSlotDir(bookDir, versionID); decider.InsideRoot(slot) == nil {
		pruneEmptyDirs(slot)
	}
	if vdir := VersionsDir(bookDir); decider.InsideRoot(vdir) == nil {
		_ = PruneEmptyVersionsDir(bookDir)
	}
}

type pathVerdict int

const (
	verdictRemove pathVerdict = iota
	// verdictGone: nothing to remove. Either the path was unlinked and recorded
	// by an earlier attempt of this purge and reads "not there", or it is not
	// there and its folder vouches for that.
	verdictGone
	// verdictServed: a live book still serves the path (servedGuard). Before the
	// claim of a trashed version this ends the purge with nothing touched.
	verdictServed
	// verdictKept: another owner, or the guard, says it stays.
	verdictKept
	// verdictUndecided: the path is not there but nothing vouches that it was
	// removed, or the question could not be asked. The reason is returned.
	verdictUndecided
)

// classifyPurgePath is the whole decision about one path of a version purge. It
// only reads. A non-nil error is an owner lookup that could not answer (fail
// closed: the purge does not go ahead); an "undecided" verdict is a path the
// disk could not settle.
//
// A path a live book still serves (servedGuard) is verdictServed, whatever else
// is true of it, except a recorded path that now reads "not there".
//
// A path an earlier attempt of THIS purge unlinked and recorded
// (BookVersion.PurgeUnlinked) is trusted path by path: if it reads "not there"
// it is gone, whatever its folder looks like. Nothing wider is trusted: not the
// recorded path's folder, and so not its neighbours, which this purge may never
// have unlinked and which may sit in a folder that is empty only because its
// dataset is not mounted.
func classifyPurgePath(store versionPurger, decider PurgeDecider, sg *servedGuard, ver *database.BookVersion, p string, recorded map[string]struct{}) (pathVerdict, string, error) {
	if _, ok := recorded[p]; ok {
		_, serr := os.Lstat(p)
		switch {
		case serr == nil:
			// It is there again (recreated since): decided like any other path.
		case errors.Is(serr, fs.ErrNotExist) || errors.Is(serr, syscall.ENOTDIR):
			return verdictGone, "", nil
		default:
			return verdictUndecided, fmt.Sprintf("cannot tell whether %s is gone: %v", p, serr), nil
		}
	}
	// A file a live book still serves is asked first, and ends more than the
	// owner check below does: see servedGuard. Any read error is an error here
	// (fail closed), never "not served".
	isServed, err := sg.serves(p)
	if err != nil {
		return verdictKept, "", err
	}
	if isServed {
		return verdictServed, KeptReason, nil
	}
	reason, err := otherOwnerOf(store, p, ver)
	if err != nil {
		return verdictKept, "", err
	}
	if reason != "" {
		return verdictKept, reason, nil
	}
	decision, why, err := decider.Decide(p)
	if err != nil {
		return verdictUndecided, fmt.Sprintf("cannot tell whether %s is gone: %v", p, err), nil
	}
	switch decision {
	case PurgeGone:
		return verdictGone, "", nil
	case PurgeKeep:
		return verdictKept, why, nil
	}
	return verdictRemove, "", nil
}

// OutsideSlotNeverAutoRemovedReason is why a noted file outside the version's
// own slot is kept: the purge and the kept-file sweep delete only inside the
// version's own .versions/{id}/ slot.
const OutsideSlotNeverAutoRemovedReason = "this file is outside the version's own .versions folder, and a purge or sweep removes files only inside it; it is kept and listed so the owner can decide"

// classifyNotedPath is classifyPurgePath for a path that comes from a note
// (BookVersion.PurgeKept or PurgeFailed) rather than from the version's own slot
// or rows. Such a path was kept because something else owned it, and the notes
// cannot say that nothing owns it again: a book trashed or hard-deleted since
// serves nothing, a row can be stored under a spelling no lookup produces, a
// trashed book's FilePath can be the parent folder, and a path can be reused by
// a re-import before the scan writes its row. Deleting on the strength of "no
// owner found" is exactly the guess the notes exist to avoid, so the rule is
// the plain one: a noted path outside this version's own slot
// (servedGuard.inVersionSlot) is never removed, whether or not the own book
// still exists. It is kept (and so noted again, and listed in the sweep's
// report) with OutsideSlotNeverAutoRemovedReason. A path found already gone is
// still gone (nothing is deleted to settle it), and a path inside the slot is
// decided like any other.
func classifyNotedPath(store versionPurger, decider PurgeDecider, sg *servedGuard, ver *database.BookVersion, p string, recorded map[string]struct{}) (pathVerdict, string, error) {
	verdict, why, err := classifyPurgePath(store, decider, sg, ver, p, recorded)
	if err == nil && verdict == verdictRemove && !sg.inVersionSlot(p) {
		return verdictKept, OutsideSlotNeverAutoRemovedReason, nil
	}
	return verdict, why, err
}

// stuckPaths is what one purge attempt could not settle: paths it could not
// tell whether they are gone (undecided), and removals or lookups that failed
// for a reason that may pass (failed).
type stuckPaths struct {
	undecided []database.PurgeFileNote
	failed    []stuckFailure
}

type stuckFailure struct {
	path string
	err  error
}

func (s stuckPaths) any() bool { return len(s.undecided) > 0 || len(s.failed) > 0 }

// notes are the stuck paths as notes for the version record (escalation), the
// undecided ones with the reason "cannot tell whether gone".
func (s stuckPaths) notes() []database.PurgeFileNote {
	notes := slices.Clone(s.undecided)
	for _, f := range s.failed {
		notes = append(notes, database.PurgeFileNote{Path: f.path, Reason: f.err.Error()})
	}
	slices.SortFunc(notes, func(a, b database.PurgeFileNote) int { return strings.Compare(a.Path, b.Path) })
	return notes
}

func (s stuckPaths) errs() []error {
	var errs []error
	for _, n := range s.undecided {
		errs = append(errs, errors.New(n.Reason))
	}
	for _, f := range s.failed {
		errs = append(errs, f.err)
	}
	return errs
}

// settleStuck ends an attempt that left paths unsettled. The version stays as it
// is, with one exception: a version that is purging counts the attempt, and
// escalates to purge_failed after PurgeUndecidedEscalateAfter attempts in a row
// that unlinked nothing. That is the way out: purging refuses restore and hard
// delete, so a version whose files can never be told apart from a dataset that
// is down (the file is gone but its unlink was never recorded, say) would sit
// there for ever; purge_failed can be hard-deleted, and Purge now retries it.
//
//   - A trashed version never reaches the claim when nothing is removable, so it
//     stays trash (restorable), unchanged.
//   - A purge_failed version stays purge_failed.
//   - A purging version counts the attempt, at most once per calendar day
//     (BookVersion.PurgeUndecidedNightOn, via nextNightCount, the same rule the
//     kept-file sweep uses); a progress attempt, one that unlinked something,
//     resets the count.
func settleStuck(store versionPurger, ver *database.BookVersion, kept map[string]string, stuck stuckPaths, unlinked, total int) error {
	onlyUndecided := len(stuck.failed) == 0
	mkErr := func() error {
		msg := fmt.Sprintf("purge version %s: %d of %d file(s) could not be removed or decided; the version stays %s and is retried",
			ver.ID, len(stuck.undecided)+len(stuck.failed), total, ver.Status)
		if onlyUndecided {
			return &CannotTellError{VersionID: ver.ID, Files: stuck.undecided, msg: msg}
		}
		return fmt.Errorf("%s: %w", msg, errors.Join(stuck.errs()...))
	}
	if ver.Status != database.BookVersionStatusPurging {
		return mkErr()
	}
	if unlinked > 0 {
		ver.PurgeUndecidedNights, ver.PurgeUndecidedNightOn = 0, ""
	} else {
		// One night is one calendar day, as in the kept-file sweep: a Purge now
		// click that follows the nightly attempt (or another click) the same day
		// still runs, and still stores the day, but does not add a night.
		ver.PurgeUndecidedNights, ver.PurgeUndecidedNightOn = nextNightCount(ver.PurgeUndecidedNights, ver.PurgeUndecidedNightOn, purgeClock().Format(time.DateOnly))
	}
	if ver.PurgeUndecidedNights >= PurgeUndecidedEscalateAfter {
		notes := stuck.notes()
		nights := ver.PurgeUndecidedNights
		if err := escalateToPurgeFailed(store, ver, kept, notes); err != nil {
			return fmt.Errorf("purge version %s: %d attempt(s) in a row could not settle %d file(s), and recording that failed: %w",
				ver.ID, nights, len(notes), errors.Join(append(stuck.errs(), err)...))
		}
		lifecycleLog.Warn("purge version %s: %d attempts in a row could not settle %s; the version is now purge_failed so it can be hard-deleted",
			ver.ID, nights, notePaths(notes))
		return fmt.Errorf("purge version %s: %d attempts in a row could not settle %d file(s) (%s); the version is now purge_failed, no longer retried every night, and can be hard-deleted: %w: %w",
			ver.ID, nights, len(notes), notePaths(notes), ErrPurgeFailed, errors.Join(stuck.errs()...))
	}
	if err := store.UpdateBookVersion(ver); err != nil {
		lifecycleLog.Warn("purge version %s: cannot store the attempt count: %v", ver.ID, err)
	}
	return mkErr()
}

// escalateToPurgeFailed moves a purging version to purge_failed: notes (the
// files this and the earlier attempts could not settle) go in PurgeFailed, kept
// (the files left because something owns them) in PurgeKept, and the attempt count
// is cleared. It deletes nothing. When the record cannot be written the version is
// put back as it was, in memory, and the error returned.
func escalateToPurgeFailed(store versionPurger, ver *database.BookVersion, kept map[string]string, notes []database.PurgeFileNote) error {
	now := time.Now()
	prevKept, prevFailed, prevFailedAt, prevNights := ver.PurgeKept, ver.PurgeFailed, ver.PurgeFailedAt, ver.PurgeUndecidedNights
	ver.Status = database.BookVersionStatusPurgeFailed
	ver.PurgeFailed = notes
	ver.PurgeFailedAt = &now
	ver.PurgeKept = keptNotes(kept)
	ver.PurgeUndecidedNights = 0
	if err := store.UpdateBookVersion(ver); err != nil {
		ver.Status = database.BookVersionStatusPurging
		ver.PurgeFailed, ver.PurgeFailedAt, ver.PurgeKept = prevFailed, prevFailedAt, prevKept
		ver.PurgeUndecidedNights = prevNights
		return err
	}
	return nil
}

// settleNothingServed ends an attempt that deleted nothing because the version's
// live book serves no file that exists on disk (requireServedFileOnDisk). err is
// that refusal (a *NothingServedError), returned as it is for a trashed or
// purge_failed version: the first stays trash, restorable, the second stays
// purge_failed, which can be hard-deleted.
//
// A purging version has no such way out (it refuses restore and hard delete, and
// the book's path may never be fixed), so the refusal is counted like a stuck
// attempt (settleStuck): BookVersion.PurgeUndecidedNights goes up, and at
// PurgeUndecidedEscalateAfter in a row the version becomes purge_failed, with the
// files it could not decide in PurgeFailed. Escalating deletes nothing, and the
// returned error then also matches ErrPurgeFailed (NothingServedError.Escalated).
func settleNothingServed(store versionPurger, ver *database.BookVersion, kept map[string]string, doomed []string, undecided []database.PurgeFileNote, err error) error {
	var ns *NothingServedError
	if ver.Status != database.BookVersionStatusPurging || !errors.As(err, &ns) {
		return err
	}
	// One night is one calendar day, as in settleStuck: a second refusal the same
	// day runs, stores the record, and adds no night.
	ver.PurgeUndecidedNights, ver.PurgeUndecidedNightOn = nextNightCount(ver.PurgeUndecidedNights, ver.PurgeUndecidedNightOn, purgeClock().Format(time.DateOnly))
	ns.Attempt = ver.PurgeUndecidedNights
	if ver.PurgeUndecidedNights < PurgeUndecidedEscalateAfter {
		if uerr := store.UpdateBookVersion(ver); uerr != nil {
			lifecycleLog.Warn("purge version %s: cannot store the attempt count: %v", ver.ID, uerr)
		}
		return ns
	}
	notes := slices.Clone(undecided)
	for _, p := range doomed {
		notes = append(notes, database.PurgeFileNote{Path: p, Reason: "not removed: the version's book serves nothing that exists on disk, so there was nothing to check this file against"})
	}
	slices.SortFunc(notes, func(a, b database.PurgeFileNote) int { return strings.Compare(a.Path, b.Path) })
	nights := ver.PurgeUndecidedNights
	if uerr := escalateToPurgeFailed(store, ver, kept, notes); uerr != nil {
		return fmt.Errorf("purge version %s: %d attempt(s) in a row found nothing the book serves, and recording that failed: %w", ver.ID, nights, errors.Join(ns, uerr))
	}
	ns.Escalated = true
	lifecycleLog.Warn("purge version %s: %d attempts in a row found nothing the book serves; the version is now purge_failed (nothing was deleted) so it can be hard-deleted", ver.ID, nights)
	return ns
}

// dirOfPath is the directory part of path ("" when it has no slash), as the
// purge has always found a book's folder.
func dirOfPath(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return ""
}

// versionBookDir finds the folder a version's slot lives under: the version's
// book's own folder, or, for a book that no longer exists, the folder of its
// tombstone. book is nil when the book is gone. noSlot is true when the book is
// gone and has no tombstone either: there is then nothing to find, bookDir is
// empty, and the caller decides what that means. A book (or tombstone) that
// cannot be READ is an error, never "no folder". Shared by the purge and the
// kept-file sweep so the two find the same slot.
func versionBookDir(store versionPurger, ver *database.BookVersion) (book *database.Book, bookDir string, noSlot bool, err error) {
	book, err = store.GetBookByID(ver.BookID)
	if err != nil {
		return nil, "", false, fmt.Errorf("read book %s: %w", ver.BookID, err)
	}
	if book != nil {
		return book, dirOfPath(book.FilePath), false, nil
	}
	tomb, err := bookTombstoneOf(store, ver.BookID)
	if err != nil {
		return nil, "", false, fmt.Errorf("its book %s no longer exists and its tombstone cannot be read: %w", ver.BookID, err)
	}
	if tomb == nil {
		return nil, "", true, nil
	}
	return nil, dirOfPath(tomb.FilePath), false, nil
}

// bookTombstoneReader is the lookup of a hard-deleted book's last snapshot,
// found with database.AsCapability. A store without it cannot locate the slot
// of a version whose book is gone.
type bookTombstoneReader interface {
	GetBookTombstone(id string) (*database.Book, error)
}

func bookTombstoneOf(store versionPurger, bookID string) (*database.Book, error) {
	r, ok := database.AsCapability[bookTombstoneReader](store)
	if !ok {
		return nil, nil
	}
	return r.GetBookTombstone(bookID)
}

// keptNotes turns the kept paths into the notes stored on the version, in path
// order.
func keptNotes(kept map[string]string) []database.PurgeFileNote {
	if len(kept) == 0 {
		return nil
	}
	notes := make([]database.PurgeFileNote, 0, len(kept))
	for p, why := range kept {
		notes = append(notes, database.PurgeFileNote{Path: p, Reason: why})
	}
	slices.SortFunc(notes, func(a, b database.PurgeFileNote) int { return strings.Compare(a.Path, b.Path) })
	return notes
}

// removeErrorIsPermanent says whether a failed removal will fail the same way
// every time it is retried: no permission, a read-only filesystem, a directory
// where a file was expected. Anything else (EIO, ESTALE, EBUSY, a timeout) may
// pass, and is retried. So may a directory that still holds something (POSIX
// lets rmdir answer ENOTEMPTY or EEXIST): the decider already keeps a directory
// it saw holding files, so this one means something was written into it between
// that look and the unlink, and the next attempt sees the directory as it is
// then (kept, or empty and removable). That is a race passing, not a refusal
// that stays, and calling it permanent would park the version in purge_failed
// until the owner retries it by hand.
func removeErrorIsPermanent(err error) bool {
	if errors.Is(err, fs.ErrPermission) {
		return true
	}
	for _, e := range []error{syscall.EROFS, syscall.EISDIR, syscall.EPERM, syscall.EACCES} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// otherOwnerOf says why path must not be removed because something other than
// ver names it, "" when nothing does: another book_file row (a retired
// version's included), or another book row (soft-deleted ones included). A
// lookup that cannot answer is an error: fail closed, never "no owner".
func otherOwnerOf(store versionPurger, path string, ver *database.BookVersion) (string, error) {
	owner, err := otherRowNamingPath(store, path, ver)
	if err != nil {
		return "", fmt.Errorf("cannot verify %s is unreferenced: %w", path, err)
	}
	if owner != nil {
		return fmt.Sprintf("book_file %s of book %s (version %q, retired=%t) still names it",
			owner.ID, owner.BookID, owner.VersionID, owner.Retired), nil
	}
	others, err := otherBooksNamingPath(store, path, ver.BookID)
	if err != nil {
		return "", fmt.Errorf("cannot verify %s is not a book's file: %w", path, err)
	}
	if len(others) > 0 {
		return fmt.Sprintf("it is the file path of book(s) %v (live or in the trash)", others), nil
	}
	return "", nil
}

// claimPurge writes ver as purging. The store refuses the write when the
// stored version is no longer trash, purging or purge_failed (restored since
// the caller read it): that is ErrVersionNotInTrash, the purge does not go
// ahead, and ver is put back as it was read.
func claimPurge(store versionPurger, ver *database.BookVersion) error {
	was := ver.Status
	ver.Status = database.BookVersionStatusPurging
	if err := store.UpdateBookVersion(ver); err != nil {
		ver.Status = was
		if errors.Is(err, database.ErrVersionPurgeClaimLost) {
			// Lost to a restore, or to a purge that finished since the re-read:
			// the caller words those differently.
			if cur, rerr := store.GetBookVersion(ver.ID); rerr == nil && cur != nil && cur.Status == database.BookVersionStatusInactivePurged {
				return fmt.Errorf("purge version %s: %w: %w (%v)", ver.ID, ErrVersionNotInTrash, ErrVersionAlreadyPurged, err)
			}
			return fmt.Errorf("purge version %s: %w (%v)", ver.ID, ErrVersionNotInTrash, err)
		}
		// A hard delete that landed since the re-read: the store has no row to
		// claim. Not a failure of the purge, and not a restore either.
		if cur, rerr := store.GetBookVersion(ver.ID); rerr == nil && cur == nil {
			return fmt.Errorf("purge version %s: %w: %w (it was hard-deleted while this purge was starting)", ver.ID, ErrVersionNotInTrash, ErrVersionDeleted)
		}
		return fmt.Errorf("purge version %s: claim it: %w", ver.ID, err)
	}
	return nil
}

// markPurged records the end of a purge. A version that failed before and is
// purged now loses its failure record; PurgeKept is whatever the caller set.
func markPurged(store versionPurger, ver *database.BookVersion) error {
	now := time.Now()
	ver.Status = database.BookVersionStatusInactivePurged
	ver.PurgedDate = &now
	ver.PurgeFailed = nil
	ver.PurgeFailedAt = nil
	ver.PurgeUnlinked = nil
	ver.PurgeUndecidedNights = 0
	ver.PurgeUndecidedNightOn = ""
	return store.UpdateBookVersion(ver)
}

// otherRowNamingPath returns a book_file row that names path and is not one of
// ver's own rows (same book, same version), or nil when none does. Retired
// rows are included: BookFilesAtPath does not filter them. Every spelling of
// the path is asked (pathSpellings): the lookup matches strings exactly, and
// the row may have been written through a symlinked library root. Any lookup
// error is returned, so the caller fails closed.
func otherRowNamingPath(store versionPurger, path string, ver *database.BookVersion) (*database.BookFile, error) {
	for _, v := range pathSpellings(path) {
		rows, err := store.BookFilesAtPath(v)
		if err != nil {
			return nil, err
		}
		for i := range rows {
			if rows[i].BookID == ver.BookID && rows[i].VersionID == ver.ID {
				continue
			}
			return &rows[i], nil
		}
	}
	return nil, nil
}

// otherBooksNamingPath returns the id of every book row, soft-deleted ones
// included, whose own FilePath is path, other than ownBookID. A trashed book is
// restorable and wants its audio back, so it holds a path exactly as a live one
// does (the live-only lookup, LiveBookIDsAtPath, missed it). The lookup is
// database.BookRowPathReader, which is not on database.Store, so it is resolved
// with database.AsCapability; a store without it, or a lookup that errors, is
// an error: the caller fails closed and deletes nothing, never "no owner".
// Every spelling of the path is asked (pathSpellings), each id once.
func otherBooksNamingPath(store versionPurger, path, ownBookID string) ([]string, error) {
	rows, ok := database.AsCapability[database.BookRowPathReader](store)
	if !ok {
		return nil, fmt.Errorf("store %T does not provide the all-rows book path lookup (database.BookRowPathReader)", store)
	}
	var others []string
	for _, v := range pathSpellings(path) {
		ids, err := rows.BookIDsAtPath(v)
		if err != nil {
			return nil, fmt.Errorf("books-at-path lookup: %w", err)
		}
		for _, id := range ids {
			// The version's own book naming its file is the normal case, not a
			// second owner: the file is the version's, and the version is what is
			// being purged.
			if id != ownBookID && !slices.Contains(others, id) {
				others = append(others, id)
			}
		}
	}
	return others, nil
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

// pruneEmptyDirs removes a version's slot folder (<book dir>/.versions/<id>)
// and its subdirectories bottom-up, but only the empty ones: a directory that
// still holds a kept file stays, and so do its parents. Idempotent when the slot
// does not exist.
//
// No symlink is ever followed. The slot must be where its name says it is:
// resolved, it equals the resolved book folder joined with ".versions" and the
// version id, so neither the slot nor the .versions folder above it is a link
// (a link there would make the walk remove empty directories of the link's
// target, outside the library). Below the slot, every directory is looked at
// with Lstat, and a link is left where it is (it names nothing this purge
// owns). A caller that knows the library root also requires the slot to be
// inside it (pruneVersionDirs).
func pruneEmptyDirs(slot string) {
	versionsDir := filepath.Dir(slot)
	realBook, err := filepath.EvalSymlinks(filepath.Dir(versionsDir))
	if err != nil {
		return
	}
	realSlot, err := filepath.EvalSymlinks(slot)
	if err != nil || realSlot != filepath.Join(realBook, filepath.Base(versionsDir), filepath.Base(slot)) {
		return
	}
	pruneEmptyTree(slot)
}

// pruneEmptyTree is the walk under a slot pruneEmptyDirs has vouched for.
func pruneEmptyTree(dir string) {
	if info, err := os.Lstat(dir); err != nil || info.Mode()&os.ModeSymlink != 0 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			pruneEmptyTree(filepath.Join(dir, e.Name()))
		}
	}
	// os.Remove refuses a non-empty directory, which is the check.
	_ = os.Remove(dir)
}

// CleanupResult is what one run of the trash cleanup did.
type CleanupResult struct {
	// Purged counts the versions this run finished purging, including those
	// that finished with files kept (KeptFiles).
	Purged int
	// Failed counts the versions this run could not act on (see
	// cleanupTrashedVersionsAt).
	Failed int
	// Kept are the versions left in the trash, untouched and restorable, because
	// a live book still serves one of their files (BookServesFilesError). Files
	// holds each served path with the reason. They are retried on every run, are
	// not counted in Purged, and are not failures.
	Kept []KeptVersion
	// KeptFiles are the versions purged this run with files left on disk
	// because another row or book names them (database.BookVersion.PurgeKept
	// has the paths and reasons). Each is also counted in Purged: the version
	// is purged, but not every file is gone, and a report must say so.
	KeptFiles []KeptVersion
	// KeptSweep is what the kept-file sweep did (SweepKeptFiles): files that
	// earlier purges kept because another row or book named them, revisited
	// once that owner was gone. Versions purged by this very run are not
	// revisited (the purge has just asked).
	KeptSweep KeptSweepResult
	// KeptSweepErr is why the sweep could not list the purged versions, when it
	// could not. Like SwapIncompleteErr it is not in the error
	// CleanupTrashedVersions returns, which is about the purges; the caller
	// reports it as a failure of the night.
	KeptSweepErr error
	// NeedsAttention are the versions in database.BookVersionStatusPurgeFailed:
	// a purge that gave up on a file it cannot remove. The run does not retry
	// them and does not count them as Failed (that would fail the operation
	// every night for ever); it lists them so they are seen.
	NeedsAttention []KeptVersion
	// CannotTell are the versions the run left alone because the purge could not
	// tell whether their files are gone (a missing path whose folder is missing
	// or empty, which is also how a dataset that is not mounted looks) and
	// nothing was left to remove. A trashed one stays trash and restorable; a
	// purging one has the night counted and moves to purge_failed after
	// PurgeUndecidedEscalateAfter. The paths and reasons are in Files. Not a
	// failure (the library may simply be down) and not counted in Failed.
	CannotTell []KeptVersion
	// NothingServed are the versions the run left untouched because the book
	// serves nothing that exists on disk (NothingServedError): its path is stale
	// or lost, or a folder with no file in it. Each entry carries the book's path
	// (BookPath). Not a failure and not counted in Failed, and listed again every
	// run until the book's path is fixed or the version restored. A trashed
	// version stays in the trash, restorable, and is never escalated (it is never
	// claimed). A purging version cannot be restored, so each run counts the
	// refusal like a stuck attempt and, after PurgeUndecidedEscalateAfter in a row,
	// moves it to purge_failed (nothing is deleted): the entry of that night
	// carries the files it could not decide, and from the next night the version is
	// in NeedsAttention, where it can be hard-deleted.
	NothingServed []KeptVersion
	// SwapIncomplete are the books with a version left in swapping_out/swapping_in
	// by a version swap that did not finish (ListSwapIncompleteBooks): each has no
	// active version until ResumeVersionSwap repairs it. The run only reports
	// them; it moves nothing. Not a failure and not counted in Failed, and listed
	// again every run until repaired.
	SwapIncomplete []SwapIncompleteBook
	// SwapIncompleteErr is why SwapIncomplete could not be read, when it could
	// not. It is not in the error CleanupTrashedVersions returns, which is about
	// the purges (and the merge-lock streak is judged by it); the caller reports
	// it as a failure of the night.
	SwapIncompleteErr error
	// StoppedForMerge is set when a merge or combine held the merge lock for the
	// whole of BackgroundMergeLockWait, so the run stopped there: the versions
	// from that one on were not attempted and are picked up by the next run.
	// It is not a failure of any version (a busy library is not a broken
	// version) and does not count in Failed; waiting the same time for every
	// remaining version would hold the 06:00 job for N times the wait.
	StoppedForMerge bool
	// MergeStopStreak is how many nights in a row, this one included, the
	// cleanup stopped for the merge lock (0 when this run did not). The count
	// is kept in the store (mergeStopStreakKey) between runs; a run that gets
	// past the lock resets it. At MergeStopEscalateAfter or more it is reported
	// as a warning, never a failure: the trash is not being purged and nothing
	// else says so.
	MergeStopStreak int
}

// KeptVersion names a version and its book, with the files a purge left or
// could not remove.
type KeptVersion struct {
	VersionID string
	BookID    string
	Files     []database.PurgeFileNote
	// BookPath is the book's own path, set for CleanupResult.NothingServed (the
	// path that has to be fixed); empty otherwise.
	BookPath string
}

// CleanupTrashedVersions is the maintenance task that purges versions
// whose trash window has run out. Called by the scheduler.
//
// It reports what it did (CleanupResult), with an error that is non-nil
// exactly when Failed > 0 or the trash could not be listed. A run in which every
// due purge fails (book_files flagged incomplete, a file that cannot be
// removed) must not read as "purged 0" to the operation that called it: the
// versions stay in the trash past their window and only the error says so.
//
// keptSweepApply is the kept-file sweep's switch (config kept_sweep_apply_enabled),
// read once by the caller for the whole run: false reports what the sweep would
// remove and unlinks nothing (see SweepKeptFiles).
func CleanupTrashedVersions(store TrashedVersionCleaner, guard PurgeGuard, keptSweepApply bool) (CleanupResult, error) {
	res, err := cleanupTrashedVersionsAt(store, time.Now(), guard, keptSweepApply)
	// A run that could not even list the trash (err with nothing failed) says
	// nothing about the lock, so the streak stays as it was. Every other run
	// either stopped for the merge lock or got past it.
	if err == nil || res.Failed > 0 {
		res.MergeStopStreak = recordMergeStopStreak(store, res.StoppedForMerge)
		if res.MergeStopStreak >= MergeStopEscalateAfter {
			lifecycleLog.Warn("trash cleanup: stopped for the merge lock %d nights in a row; something holds it when this job runs (a combine that never ends, or blocked lock waiters starving the poll), and the trash is not being purged",
				res.MergeStopStreak)
		}
	}
	return res, err
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
// purge was claimed, or the user asked for it with purge-now. A version in
// database.BookVersionStatusPurgeFailed is NOT retried: its purge gave up on a
// file that cannot be removed, and trying again each night only fails again; it
// is listed in NeedsAttention.
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
// Failed counts the versions this run could not act on: a purge that failed,
// a re-read that failed, a stamp that could not be written. err is nil when
// Failed is 0; otherwise it names the count and the id of EVERY failed
// version, and wraps every failure (errors.Is sees each), so one stuck version
// cannot hide a new failure behind it. When the trash cannot be listed at all,
// err is that failure and Failed is 0 (no version was attempted).
func cleanupTrashedVersionsAt(store TrashedVersionCleaner, now time.Time, guard PurgeGuard, keptSweepApply bool) (res CleanupResult, err error) {
	trashed, err := store.ListTrashedBookVersions()
	if err != nil {
		lifecycleLog.Warn("trash cleanup: list trashed versions failed: %v", err)
		return CleanupResult{}, fmt.Errorf("trash cleanup: list trashed versions: %w", err)
	}
	var failures []error
	var failedIDs []string
	fail := func(id string, e error) {
		res.Failed++
		failures = append(failures, e)
		failedIDs = append(failedIDs, id)
	}
	purgedNow := map[string]struct{}{}
	purgedOK := func(v *database.BookVersion) {
		res.Purged++
		purgedNow[v.ID] = struct{}{}
		if len(v.PurgeKept) > 0 {
			res.KeptFiles = append(res.KeptFiles, KeptVersion{VersionID: v.ID, BookID: v.BookID, Files: v.PurgeKept})
			lifecycleLog.Warn("trash cleanup: version %s (book %s) purged with %d file(s) kept because another row or book names them: %s",
				v.ID, v.BookID, len(v.PurgeKept), notePaths(v.PurgeKept))
		}
	}

	ttl := time.Duration(TrashTTLDays) * 24 * time.Hour
	stamped := 0
	// stopForMerge ends the run at the first purge that found the merge lock
	// held for the whole wait: see CleanupResult.StoppedForMerge.
	stopForMerge := func(id string) {
		res.StoppedForMerge = true
		lifecycleLog.Warn("trash cleanup: a merge or combine held the merge lock for %s while purging version %s; stopping here, the rest of the trash is handled by the next run",
			BackgroundMergeLockWait, id)
	}
trashLoop:
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
		// Only a trashed (or purging, or purge_failed) version is ever purged
		// here. A retired version is not trashed (MarkTrashed refuses it) and
		// would not be listed; PurgeVersion refuses it as well, so a stale or
		// hand-edited row cannot reach the disk.
		if v == nil || (v.Status != database.BookVersionStatusTrash && v.Status != database.BookVersionStatusPurging &&
			v.Status != database.BookVersionStatusPurgeFailed) {
			continue
		}
		if v.Status == database.BookVersionStatusPurgeFailed {
			res.NeedsAttention = append(res.NeedsAttention, KeptVersion{VersionID: v.ID, BookID: v.BookID, Files: v.PurgeFailed})
			continue
		}
		if v.Status == database.BookVersionStatusPurging {
			// An unfinished purge: no window, no stamp. Retry it.
			if err := PurgeVersion(store, v, guard); err != nil {
				if errors.Is(err, ErrVersionNotInTrash) {
					// A concurrent purge-now finished (or otherwise moved) it, or a
					// hard delete removed it, between the re-read and the claim:
					// nothing to do, not a failure.
					lifecycleLog.Info("trash cleanup: version %s left the purging state while this run was under way; not retrying it", v.ID)
					continue
				}
				if errors.Is(err, merge.ErrMergeBusy) {
					stopForMerge(id)
					break trashLoop
				}
				if noteServed(&res, v, err) || noteNothingServed(&res, v, err) || noteCannotTell(&res, v, err) {
					continue
				}
				lifecycleLog.Warn("trash cleanup: retrying the purge of version %s failed: %v", v.ID, err)
				fail(id, err)
				continue
			}
			purgedOK(v)
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
		if err := PurgeVersion(store, v, guard); err != nil {
			if errors.Is(err, ErrVersionNotInTrash) {
				lifecycleLog.Info("trash cleanup: version %s was restored or deleted while this run was under way; not purging it", v.ID)
				continue
			}
			if errors.Is(err, merge.ErrMergeBusy) {
				stopForMerge(id)
				break trashLoop
			}
			if noteServed(&res, v, err) || noteNothingServed(&res, v, err) || noteCannotTell(&res, v, err) {
				continue
			}
			lifecycleLog.Warn("trash cleanup: purging version %s failed: %v", v.ID, err)
			fail(id, err)
			continue
		}
		purgedOK(v)
	}
	if stamped > 0 {
		lifecycleLog.Info("trash cleanup: stamped %d trashed version(s) that had no trashed-at time; none of them were purged", stamped)
	}
	if n := len(res.CannotTell); n > 0 {
		lifecycleLog.Warn("trash cleanup: %d version(s) have files that cannot be told apart from an unmounted dataset and were left alone: %s",
			n, keptVersionIDs(res.CannotTell))
	}
	if n := len(res.NothingServed); n > 0 {
		lifecycleLog.Warn("trash cleanup: %d version(s) were left in the trash because their book serves nothing that exists on disk (fix the book's path or restore the version): %s",
			n, keptVersionIDs(res.NothingServed))
	}
	if n := len(res.NeedsAttention); n > 0 {
		lifecycleLog.Warn("trash cleanup: %d version(s) are purge_failed (a file cannot be removed) and need the owner: %s; not retried",
			n, keptVersionIDs(res.NeedsAttention))
	}
	// After the purges, so a failure here never holds one up. Not when the run
	// stopped for the merge lock: the sweep would wait on it as well.
	if !res.StoppedForMerge {
		res.KeptSweep, res.KeptSweepErr = SweepKeptFiles(store, guard, KeptSweepOptions{Apply: keptSweepApply, Skip: purgedNow, Now: now})
		if res.KeptSweepErr != nil {
			lifecycleLog.Warn("trash cleanup: %v", res.KeptSweepErr)
		}
		if k := res.KeptSweep; k.Removed+k.Gone+k.WouldRemove+k.WouldDrop+k.StillKept+k.KeptOnError > 0 {
			mode := "removed " + strconv.Itoa(k.Removed) + ", note dropped for " + strconv.Itoa(k.Gone) + " already gone"
			if k.ReportOnly {
				mode = "REPORT ONLY (nothing changed): would remove " + strconv.Itoa(k.WouldRemove) + ", would drop " + strconv.Itoa(k.WouldDrop) + " notes of files already gone"
			}
			lifecycleLog.Info("trash cleanup: kept-file sweep of %d purged version(s): %s, %d still owned (%d of them outside their version's own folder, which the sweep never removes), %d kept on error",
				k.Versions, mode, k.StillKept, k.OutsideSlot, k.KeptOnError)
		}
		if n := len(res.KeptSweep.Escalated); n > 0 {
			lifecycleLog.Warn("trash cleanup: %d purged version(s) had a kept file the sweep could not settle for %d nights in a row and are now purge_failed: %s",
				n, PurgeUndecidedEscalateAfter, keptVersionIDs(res.KeptSweep.Escalated))
		}
	}
	// Read-only, so a failure here never holds anything up.
	if res.SwapIncomplete, res.SwapIncompleteErr = ListSwapIncompleteBooks(store); res.SwapIncompleteErr != nil {
		lifecycleLog.Warn("trash cleanup: could not list the books whose version swap did not finish: %v", res.SwapIncompleteErr)
	} else if n := len(res.SwapIncomplete); n > 0 {
		lifecycleLog.Warn("trash cleanup: %d book(s) have a version swap that did not finish and no active version (resume the swap from the Version Management page): %s",
			n, swapIncompleteBookIDs(res.SwapIncomplete))
	}
	if res.Failed > 0 {
		lifecycleLog.Warn("trash cleanup: purged %d, failed %d of %d trashed version(s) (%s); the failed ones stay in the trash and are retried next run",
			res.Purged, res.Failed, len(trashed), strings.Join(failedIDs, ", "))
		return res, fmt.Errorf("trash cleanup: %d of %d trashed version(s) could not be handled (%d purged); failed versions %s: %w",
			res.Failed, len(trashed), res.Purged, strings.Join(failedIDs, ", "), errors.Join(failures...))
	}
	return res, nil
}

func swapIncompleteBookIDs(books []SwapIncompleteBook) string {
	ids := make([]string, len(books))
	for i, b := range books {
		ids[i] = b.BookID
	}
	return strings.Join(ids, ", ")
}

// noteServed files a purge that left a version in the trash because a live book
// still serves its files under CleanupResult.Kept, and reports true. PurgeVersion
// logged it (one WARN per version).
func noteServed(res *CleanupResult, v *database.BookVersion, err error) bool {
	var served *BookServesFilesError
	if !errors.As(err, &served) {
		return false
	}
	files := make([]database.PurgeFileNote, len(served.Paths))
	for i, p := range served.Paths {
		files[i] = database.PurgeFileNote{Path: p, Reason: KeptReason}
	}
	res.Kept = append(res.Kept, KeptVersion{BookID: served.BookID, VersionID: served.VersionID, Files: files})
	return true
}

// noteCannotTell files a purge that left a version alone because it could not tell
// whether its files are gone under CleanupResult.CannotTell, and reports true. Any
// other error is the caller's to count as a failure.
func noteCannotTell(res *CleanupResult, v *database.BookVersion, err error) bool {
	var ct *CannotTellError
	if !errors.As(err, &ct) {
		return false
	}
	lifecycleLog.Warn("trash cleanup: version %s (book %s) left alone: %v", v.ID, v.BookID, err)
	res.CannotTell = append(res.CannotTell, KeptVersion{VersionID: v.ID, BookID: v.BookID, Files: ct.Files})
	return true
}

// noteNothingServed files a purge that left a version in the trash because its
// book serves nothing that exists on disk under CleanupResult.NothingServed, and
// reports true. The entry carries the book's path, the thing to fix. It is
// checked before noteCannotTell: the two are different errors, but the
// ordering keeps one from ever being filed as the other.
func noteNothingServed(res *CleanupResult, v *database.BookVersion, err error) bool {
	var ns *NothingServedError
	if !errors.As(err, &ns) {
		return false
	}
	res.NothingServed = append(res.NothingServed, KeptVersion{VersionID: v.ID, BookID: v.BookID, Files: ns.Files, BookPath: ns.BookPath})
	return true
}

// recordMergeStopStreak updates the number of consecutive nights the cleanup
// stopped for the merge lock and returns it (0 after a night that did not stop).
// A store that cannot keep it (no raw key-value space, or a read or write that
// fails) never fails the run: the streak is then this night alone, 1 when it
// stopped, and the failure is logged.
func recordMergeStopStreak(store any, stopped bool) int {
	kv, ok := database.AsCapability[database.RawKVStore](store)
	if !ok {
		mergeStopStreakUnavailableOnce.Do(func() {
			lifecycleLog.Warn("trash cleanup: the store %T has no raw key-value space, so the merge-stop streak cannot be kept between nights; a lock held at 06:00 every night will not be escalated", store)
		})
		if stopped {
			return 1
		}
		return 0
	}
	prev := 0
	raw, err := kv.GetRaw(mergeStopStreakKey)
	if err != nil {
		lifecycleLog.Warn("trash cleanup: cannot read the merge-stop streak, counting this night alone: %v", err)
	} else if len(raw) > 0 {
		if n, perr := strconv.Atoi(string(raw)); perr == nil && n > 0 {
			prev = n
		}
	}
	streak := 0
	if stopped {
		streak = prev + 1
	}
	switch {
	case streak > 0:
		err = kv.SetRaw(mergeStopStreakKey, []byte(strconv.Itoa(streak)))
	case prev > 0:
		err = kv.DeleteRaw(mergeStopStreakKey)
	}
	if err != nil {
		lifecycleLog.Warn("trash cleanup: cannot store the merge-stop streak: %v", err)
	}
	return streak
}

// notePaths joins the paths of notes for a log line.
func notePaths(notes []database.PurgeFileNote) string {
	paths := make([]string, len(notes))
	for i, n := range notes {
		paths[i] = n.Path
	}
	return strings.Join(paths, ", ")
}

// keptVersionIDs joins the version ids of vs for a log line.
func keptVersionIDs(vs []KeptVersion) string {
	ids := make([]string, len(vs))
	for i, v := range vs {
		ids[i] = v.VersionID
	}
	return strings.Join(ids, ", ")
}
