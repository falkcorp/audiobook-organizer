// file: internal/metafetch/service_files.go
// version: 1.15.0
// guid: 969b284a-5657-442b-beba-275e325e000b
// last-edited: 2026-09-12

package metafetch

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// AudioFilesInDir returns the library audio files directly inside dir, sorted
// by name.
//
// Follows supported_extensions. The previous implementation globbed a private
// 8-pattern list, which was wrong three ways: it skipped the seven configured
// extensions it did not know about; filepath.Glob is case-sensitive on Linux,
// so a "Chapter 01.MP3" was invisible; and a directory whose own name contains
// a glob metacharacter ("[Unabridged]" is a real shape in this library) made
// every pattern match nothing. Reading the directory and testing the extension
// has none of those failure modes.
func AudioFilesInDir(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	exts := config.SupportedExtensionSet()
	var files []string
	for _, e := range entries {
		if e.IsDir() || !exts.MatchPath(e.Name()) {
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	sort.Strings(files)
	return files
}

// backupFileBeforeWrite creates a timestamped .bak copy of a file before
// writing tags — IF the WriteBackupBeforeTagWrite config flag is enabled.
//
// Default is OFF. Historically this function ran unconditionally on every
// tag write and used os.Link (hardlink) for "no disk space cost". Two
// problems with that:
//
//  1. Tens of thousands of stale backup files accumulated across the
//     library (43K+ files, multi-TB apparent size in production) because
//     nothing ever cleaned them up.
//  2. Hardlinks don't actually preserve pre-write content when the
//     writer modifies the inode in place (which TagLib does for some
//     formats). The "backup" could be a hardlink to the same now-modified
//     data, providing false safety.
//
// The flag is opt-in. Users who turn it on should also run the
// cleanup-backups maintenance endpoint periodically to keep the library
// from growing unbounded.
//
// Failures are logged but non-fatal — the write-back proceeds regardless.
func backupFileBeforeWrite(filePath string) {
	if !config.AppConfig.MetadataScoring.WriteBackupBefore {
		return
	}
	if filePath == "" {
		return
	}
	if _, err := os.Stat(filePath); err != nil {
		return
	}
	backupPath := filePath + ".bak-" + time.Now().Format("20060102-150405")
	if err := os.Link(filePath, backupPath); err != nil {
		// Hardlink failed — fall back to copy
		if err := fileops.SafeCopy(filePath, backupPath, fileops.OperationConfig{}); err != nil {
			slog.Warn("backup before tag write failed:", "path", filePath, "error", err)
			return
		}
	}
	slog.Debug("backup before tag write", "path", backupPath)
}

// tagWriteResult reports what runApplyPipeline did about audio tags.
type tagWriteResult struct {
	// handled: the pipeline owned the tag write for this run (wrote them, found
	// them already written by an interrupted earlier attempt, or failed).
	handled bool
	// err is the pipeline's tag-write failure, if any.
	err error
}

// writeTags is the one tag write every apply path goes through, on the files
// of targetID: the book the job locked (lockLibraryCopy). tagWriter is a test
// seam that counts it; nil in production.
func (mfs *Service) writeTags(id, targetID string) (int, error) {
	if mfs.tagWriter != nil {
		return mfs.tagWriter(id)
	}
	return mfs.writeBackForBook(id, nil, targetID)
}

// copyPolicy says whether lockLibraryCopy may create a library copy for a book
// that lives under a protected (iTunes/import) path. It is read ONLY there. The
// file steps after it take the target it locked (fileWorkTarget) and never
// create or pick a copy of their own.
type copyPolicy bool

const (
	// createLibraryCopy: user-initiated applies and write-backs. A protected
	// book gets a library copy made if it has none, and the file work runs on
	// the copy.
	createLibraryCopy copyPolicy = true
	// existingCopyOnly: auto-fetch, and an apply that writes no audio files.
	// File work runs on a library copy that already exists, and is skipped
	// when there is none.
	existingCopyOnly copyPolicy = false
)

// errFileTargetChanged means a file step resolved a different book than the
// one its job locked.
var errFileTargetChanged = errors.New("the book's library copy changed after its file work locked one")

// fileWorkTarget is the book whose audio files a file step writes: book itself,
// or -- for a book under a protected (iTunes/import) path -- its library copy.
// Nil means a protected book with no copy: nothing may be written.
//
// targetID is what the job locked: lockLibraryCopy's result, "" when it locked
// no copy. A library copy is re-read BY ID and checked to still be one
// (libraryCopyGone); the lookup is not run again. The lookup takes the first
// usable sibling after sortVersions (primary flag, then title), and a job on
// another copy of the group can change either while holding only its own
// lock, so in a group with two copies the pick can flip mid-job. Comparing a
// fresh lookup with the locked copy turned that flip into a spurious
// errFileTargetChanged. The row re-read by id carries the path left by a
// rename earlier in this job, as a lookup's would. A book that is its own
// target, or has none, is still resolved by the lookup: its answer cannot
// flip to another copy, because a book under root_dir or outside every
// protected tree always resolves to itself.
//
// Until 2026-09-12 each step resolved the copy again on its own, so a version
// sibling that became a library copy between the lock step and a file step
// was written by a job that did not hold its lock. A disagreement with
// targetID is now errFileTargetChanged, and nothing is written.
//
// It never creates a copy. It is the ONE resolution both the writers (embed,
// rename pipeline, tag write) and the path-lock key use: before 2026-09-12
// auto-fetch locked the original book's path while runApplyPipeline wrote the
// library copy's, so the lock guarded a path nothing wrote.
func (mfs *Service) fileWorkTarget(book *database.Book, targetID string) (*database.Book, error) {
	if book == nil {
		return nil, nil
	}
	if targetID != "" && targetID != book.ID {
		cp, err := mfs.db.GetBookByID(targetID)
		if err != nil {
			return nil, fmt.Errorf("book %s: re-read its locked library copy %s: %w", book.ID, targetID, err)
		}
		if why := mfs.libraryCopyGone(book, cp); why != "" {
			return nil, fmt.Errorf("book %s: its file work locked library copy %s, but %s, so nothing was written: %w",
				book.ID, targetID, why, errFileTargetChanged)
		}
		return cp, nil
	}
	resolved, _ := mfs.existingLibraryCopy(book)
	resolvedID := ""
	if resolved != nil {
		resolvedID = resolved.ID
	}
	if resolvedID != targetID {
		return nil, fmt.Errorf("book %s: its file work locked %s but the book now resolves to %s, so nothing was written: %w",
			book.ID, describeFileTarget(targetID), describeFileTarget(resolvedID), errFileTargetChanged)
	}
	return resolved, nil
}

// libraryCopyGone says why cp is no longer book's library copy, or "" when it
// still is one: a row under root_dir, in book's version group, with no present
// file row inside a protected tree -- what librarySibling requires.
func (mfs *Service) libraryCopyGone(book, cp *database.Book) string {
	switch {
	case cp == nil:
		return "the copy was deleted"
	case !pathUnderRoot(cp.FilePath, config.AppConfig.RootDir):
		return "the copy is no longer under root_dir"
	case book.VersionGroupID == nil || cp.VersionGroupID == nil || *book.VersionGroupID != *cp.VersionGroupID:
		return "the copy is no longer in the book's version group"
	}
	if p := mfs.firstProtectedFileRow(cp.ID); p != "" {
		return "the copy has a file row under a protected path"
	}
	return ""
}

// describeFileTarget names a file-work target in an error.
func describeFileTarget(id string) string {
	if id == "" {
		return "no library copy"
	}
	return "book " + id
}

// lockPath takes the server's per-path write lock (SetPathLocker) and returns
// its release. With no locker wired, or no path, it is a no-op.
func (mfs *Service) lockPath(path string) func() {
	if mfs.pathLock == nil || path == "" {
		return func() {}
	}
	return mfs.pathLock(path)
}

// lockWriteTarget re-reads book id, resolves the files the next write touches
// (fileWorkTarget, checked against targetID, the book the job locked) and
// locks THAT path. The re-read matters: a rename earlier in the same file work
// moves the files, and a lock on the pre-rename path guards nothing. A
// resolution that disagrees with targetID is an error, and the caller must not
// write. The release is never nil.
func (mfs *Service) lockWriteTarget(id, targetID string) (func(), error) {
	noop := func() {}
	book, err := mfs.db.GetBookByID(id)
	if err != nil || book == nil {
		// The write re-reads the book and reports this itself.
		return noop, nil
	}
	target, err := mfs.fileWorkTarget(book, targetID)
	if err != nil {
		return noop, err
	}
	if target == nil {
		return noop, nil
	}
	return mfs.lockPath(target.FilePath), nil
}

// bookLockKey is the lock-table key for one book's whole file-work sequence.
// Path keys are absolute paths, so the "book:" prefix cannot collide with one.
func bookLockKey(id string) string { return "book:" + id }

// vgLockKey is the lock-table key of book's version group,
// organizer.VersionGroupLockKey. The organizer takes the same key around
// CreateOrganizedVersion, so both makers of a library copy share it. Every
// version of one book shares a group, so two versions that are linked always
// take the same key. A book with no group yet is keyed by its own id
// ("vg:book:<id>", never lockBook's "book:<id>"): it has no version sibling,
// so no other book's job can find or make a copy for it, and its own jobs are
// serialized by its book lock.
func vgLockKey(book *database.Book) string { return organizer.VersionGroupLockKey(book) }

// lockVersionGroup takes book's version-group lock (vgLockKey) and returns the
// book re-read under it, with the release (never nil). The key comes from a
// row read before blocking, so the re-read checks it again: a book moved to
// another group in between would otherwise be checked under a lock its new
// siblings do not take. On a changed key the lock is swapped and the check
// repeated, a bounded number of times. A book deleted meanwhile comes back
// nil with no error.
func (mfs *Service) lockVersionGroup(book *database.Book) (*database.Book, func(), error) {
	noop := func() {}
	key := vgLockKey(book)
	for range 3 {
		release := noop
		if mfs.pathLock != nil {
			release = mfs.pathLock(key)
		}
		fresh, err := mfs.db.GetBookByID(book.ID)
		if err != nil {
			release()
			return nil, noop, fmt.Errorf("re-read book %s under its version-group lock: %w", book.ID, err)
		}
		if fresh == nil {
			release()
			return nil, noop, nil
		}
		if next := vgLockKey(fresh); next != key {
			release()
			key = next
			continue
		}
		return fresh, release, nil
	}
	return nil, noop, fmt.Errorf("book %s kept changing version group while its library copy was being made", book.ID)
}

// lockBook takes book id's own file-work lock and returns its release. With no
// locker wired, or no id, it is a no-op.
//
// Per-book locks serialize file-work jobs for the same book. Without them the
// pool ran two jobs for one book at once, and the path lock was keyed from a
// read made before blocking: job X renamed P to Q under P's lock and then
// tagged Q, while job Y, which had read P earlier, took P's lock once X
// released it and embedded or renamed files that were already at Q.
//
// LOCK ORDER. Every key below lives in one table (SetPathLocker: the server's
// writeBackPathLocks), and none is reentrant. Every entry point that writes a
// book's files -- FinishApplyFileWork, FinishAutoFetchFileWork,
// ApplyMetadataFileIO, WriteBackMetadataForBook, RunApplyPipelineRenameOnly --
// takes them in this order and in no other:
//
//  1. its own book (lockBook), held for the whole job;
//  2. the version group (vgLockKey), in lockLibraryCopy, around the lookup of
//     the files' owner and, when a protected book has no copy, its creation.
//     Every job takes it, a copy's own job included. Released before step 3;
//  3. the book whose files it writes (lockLibraryCopy): for a protected book,
//     its library copy. Held for the rest of the job;
//  4. path locks (lockPath, lockWriteTarget), taken and released per write.
//
// WithBookFilesLocked, for a caller outside this package that moves a book's
// files (batch save's organize), takes 1 and then 4. The organizer's
// CreateOrganizedVersion takes the version-group key too
// (organizer.Service.VersionGroupLocker), under whatever its caller holds.
//
// Why this cannot cycle:
//   - The version-group key is a LEAF: nothing is locked while it is held.
//     Under it, lockLibraryCopy runs existingLibraryCopy and, through
//     ensureLibraryCopy, OrganizeOneBook, CreateOrganizedVersion on
//     metafetch's own organize service (no locker) and
//     syncMetadataToLibraryCopy; the organizer's CreateOrganizedVersion runs
//     ApplyOrganizedFileMetadata and ComputeITunesPath. None takes a key. A
//     holder that never waits cannot be part of a cycle, so the key may be
//     taken under any other.
//   - Without it, what is left is book keys, then path keys. Path keys are
//     innermost: nothing that holds one takes a book key.
//   - A job holds at most two book keys, its own and then its copy's. A
//     library copy always resolves to itself (existingLibraryCopy returns any
//     book under root_dir as its own target, and librarySibling only hands
//     back siblings under root_dir), so a copy's own job takes no second book
//     key. Every wait for a book key while holding one is own-then-copy, and
//     the copy's holder never waits for a book key: no cycle among book keys.
//
// A caller outside this package must NOT hold a key from the same table
// around these entry points. The server's bulk write-back and batch save used
// to hold a path lock around WriteBackMetadataForBook and
// RunApplyPipelineRenameOnly; with the book lock now inside those, that is
// path-then-book, the reverse of step 1-then-4, and it deadlocks against an
// apply of the same book. They no longer do.
func (mfs *Service) lockBook(id string) func() {
	if mfs.pathLock == nil || id == "" {
		return func() {}
	}
	return mfs.pathLock(bookLockKey(id))
}

// lockLibraryCopy runs under book id's own lock (lockBook). It resolves the
// book whose audio files the job writes, locks it for the rest of the job and
// returns its id: the target every file step after it must use
// (fileWorkTarget). The id is "" when there is nothing to write (a protected
// book with no copy under existingCopyOnly, or a deleted book, which the file
// steps report). The release is never nil, error or not.
//
// THE LOOKUP RUNS UNDER THE VERSION-GROUP KEY (resolveLibraryCopy), for every
// job, and so does the creation of a copy when there is none. A copy is made
// by one of two holders of that key -- ensureLibraryCopy, or the organizer's
// CreateOrganizedVersion -- and each holds it until the copy's book row, its
// book_file rows and (in ensureLibraryCopy) its metadata sync are written or
// rolled back. So no lookup ever finds a copy that is still being made.
// Until 2026-09-12 the lookup ran unlocked: a job could find copy S as soon as
// its book row existed, lock S -- which its creator never holds -- and write
// S's files while the creator was still writing S's rows, rolling S back, or
// overwriting S's row with syncMetadataToLibraryCopy. A job whose book is its
// own target takes the key as well: a copy's own job waits until the copy is
// finished.
//
// Under createLibraryCopy a protected book with no copy gets one here, so two
// jobs on two versions of one book make one copy between them. It is the only
// place metafetch makes a copy, for every entry point listed at lockBook;
// WriteBackMetadataForBook (bulk write-back, batch save, the write-back
// handler) and RunApplyPipelineRenameOnly made one with no lock until
// 2026-09-12. Two more gaps it closes, both 2026-09-12:
//   - the copy used to be made part-way through, by whichever step reached it
//     first, so the job never held its lock and an apply of another protected
//     version of the same book could find it and write its files alongside;
//   - after a failed attempt here the job carried on under its own lock alone,
//     and a later step's retry made the copy with no lock on it. A copy that
//     cannot be made is now an error, and no audio file is written.
//
// Callers run it AFTER the cover download: saveCover sets the book's local
// cover_url, and a copy made afterwards inherits it (ensureLibraryCopy runs
// syncMetadataToLibraryCopy). A copy made before the download kept the
// provider's URL, and only a tag write re-synced it.
//
// Making the copy writes rows and files, so the stand-down checkpoint runs
// first; a failed check returns with nothing created. existingCopyOnly
// (auto-fetch, and an apply that writes no audio files) never creates one.
func (mfs *Service) lockLibraryCopy(id string, policy copyPolicy, checkpoint func() error) (string, func(), error) {
	noop := func() {}
	book, err := mfs.db.GetBookByID(id)
	if err != nil {
		// Carrying on would run the file steps, which re-read the book and
		// resolve its copy, without the copy's lock.
		return "", noop, fmt.Errorf("apply file work for book %s: load book: %w", id, err)
	}
	if book == nil {
		// Deleted: the file steps re-read it and report that.
		return "", noop, nil
	}
	target, err := mfs.resolveLibraryCopy(id, book, policy, checkpoint)
	if err != nil || target == nil {
		return "", noop, err
	}
	if target.ID == id {
		// The book's own files: its own lock, already held, covers them.
		return id, noop, nil
	}
	return target.ID, mfs.lockBook(target.ID), nil
}

// resolveLibraryCopy is lockLibraryCopy's lookup, and its creation of a copy
// when there is none, under book's version-group key. The key is released
// before it returns, so before the copy's book lock is taken.
func (mfs *Service) resolveLibraryCopy(id string, book *database.Book, policy copyPolicy, checkpoint func() error) (*database.Book, error) {
	fresh, release, err := mfs.lockVersionGroup(book)
	defer release()
	if err != nil {
		return nil, fmt.Errorf("apply file work for book %s: %w", id, err)
	}
	if fresh == nil {
		// Deleted while this job waited: the file steps report it.
		return nil, nil
	}
	target, ok := mfs.existingLibraryCopy(fresh)
	if ok || policy != createLibraryCopy {
		return target, nil
	}
	if err := standDown(checkpoint, id, "creating the library copy"); err != nil {
		return nil, err
	}
	return mfs.createUsableLibraryCopy(id, fresh)
}

// WithBookFilesLocked runs fn on book id as re-read under its file-work lock,
// with the path of its files locked too: lockBook, a fresh read, then lockPath
// on the fresh FilePath -- steps 1 and 4 of the order at lockBook. It is for a
// caller outside this package that moves a book's files, such as batch save's
// organize, which must not run while an apply of the same book renames or
// tags them. Until 2026-09-12 batch save took only a path lock, keyed on a
// FilePath read before its write-back: an apply that renamed the book P -> Q
// in between let the organize take P's lock, re-read the book, and move the
// files at Q that the apply was still tagging. fn must not call this
// package's file-work entry points: the lock table is not reentrant.
func (mfs *Service) WithBookFilesLocked(id string, fn func(book *database.Book) error) error {
	releaseBook := mfs.lockBook(id)
	defer releaseBook()
	book, err := mfs.db.GetBookByID(id)
	if err != nil {
		return fmt.Errorf("book %s: load under its file-work lock: %w", id, err)
	}
	if book == nil {
		return fmt.Errorf("book %s vanished before its files were locked", id)
	}
	releasePath := mfs.lockPath(book.FilePath)
	defer releasePath()
	return fn(book)
}

// createUsableLibraryCopy makes book's library copy and checks that the file
// steps, which resolve it again (fileWorkTarget), will find it. A copy
// they would not find -- one of its file rows kept a protected source path,
// which is resolveOrganizedFilePath's fallback for a row a scan added
// mid-organize -- would make every step skip without a word, so it is an error.
func (mfs *Service) createUsableLibraryCopy(id string, book *database.Book) (*database.Book, error) {
	created := mfs.ensureLibraryCopy(book)
	if created == nil {
		return nil, fmt.Errorf("apply file work for book %s: could not create its library copy; no audio files were written", id)
	}
	fresh, err := mfs.db.GetBookByID(id)
	if err != nil {
		return nil, fmt.Errorf("apply file work for book %s: re-read after creating library copy %s: %w", id, created.ID, err)
	}
	if fresh == nil {
		return nil, fmt.Errorf("apply file work for book %s: book vanished after creating library copy %s", id, created.ID)
	}
	found, ok := mfs.existingLibraryCopy(fresh)
	if ok && found != nil && found.ID == created.ID {
		return created, nil
	}
	// Name the cause the lookup actually tripped on. CreateOrganizedVersion
	// only logs a failure to write the version-group link onto the original,
	// and the steps resolve the copy through that group.
	cause := "a file row still points into a protected tree"
	switch {
	case fresh.VersionGroupID == nil || created.VersionGroupID == nil || *fresh.VersionGroupID != *created.VersionGroupID:
		cause = "the book is not linked to the copy's version group"
	case ok && found != nil:
		cause = fmt.Sprintf("another library copy, %s, is found first", found.ID)
	}
	return nil, fmt.Errorf("apply file work for book %s: library copy %s was created but is not usable (%s); no audio files were written", id, created.ID, cause)
}

// FinishApplyFileWork is the ONE file-side sequel to a metadata apply, shared by
// the single-book apply, the batch applies and auto-fetch, in this order:
//
//  1. download the candidate's cover (pendingCoverURL, i.e.
//     FetchMetadataResponse.PendingCoverURL, which ApplyMetadataCandidate derives
//     AFTER the field allowlist and the lock guard, so a deselected cover is
//     never downloaded). It must precede step 2: the embed there would
//     otherwise bake the previous image into the files;
//  2. when fileIO: cover embed + rename + (under auto_write_tags_on_apply) tag
//     write, via ApplyMetadataFileIO's pipeline;
//  3. when writeTags: write the tags -- unless step 2 already owned the tag
//     write, so each book's files are tagged exactly once.
//
// Before 2026-09-12 there was no shared sequel. The single-book handler did 1-3
// inline and tagged twice whenever auto_write_tags_on_apply was on (step 2
// wrote them, then its own write-back wrote them again); the batch apply did 2
// and 3 -- tagging twice the same way -- and never 1, so a batch-applied book
// kept its old cover forever; and auto-fetch did its own cover download and
// wrote tags only under write_back_metadata, so its DB and its tags disagreed.
//
// The error is the first file-side failure: a pipeline (rename) failure, else
// the tag-write failure. Tags are still written after a pipeline failure --
// correct tags in a file that did not move are still correct.
//
// LOCKING. The whole sequence runs under the book's own lock (lockBook), so
// two jobs for the same book never overlap; after the cover download a
// protected book's library copy is locked too (lockLibraryCopy, which looks it
// up, and makes it when there is none, under the version-group key). Inside
// them, every audio-file
// write holds the server's per-path lock
// (SetPathLocker) on the path of the files it writes, resolved per write by
// fileWorkTarget: the cover embed and the rename lock the files' current path
// (the library copy's, for a protected book); the tag write re-reads the book
// and locks the post-rename path. That mirrors the bulk write-back in
// internal/server/metadata_ops.go, which shares the lock table. The lock is
// not reentrant, so a caller must NOT hold it around this call. The cover
// download writes the per-book covers file, not the audio files, and takes
// no lock.
//
// STAND-DOWN. checkpoint is the caller's scan stand-down check:
// ScanStandDownHold.Checkpoint for the single-book and batch-candidates
// handlers, registry.ScanStandDownCheckpoint for the metadata.batch-apply-cached
// op, and nil for the restart replay, which holds no stand-down. It runs before each
// file-writing step: the cover download, the file I/O and the standalone tag
// write. A failed check stops the sequel there and is returned, wrapped with
// the step it stopped before. Metadata is never written to files during a
// library scan, and the handlers checked the hold between these steps before
// they shared this sequel; a single check before the call would let a scan
// that resumes mid-sequel run alongside the rename and the tag write.
func (mfs *Service) FinishApplyFileWork(id, pendingCoverURL string, fileIO, writeTags bool, checkpoint func() error) error {
	releaseBook := mfs.lockBook(id)
	defer releaseBook()
	if err := standDown(checkpoint, id, "the cover download"); err != nil {
		return err
	}
	mfs.DownloadPendingCover(id, pendingCoverURL)
	// Only a job that writes audio files (step 2 or 3) may make a library copy;
	// a cover-only apply must not create one just to pick a lock. After the
	// download, so a copy made here inherits the new cover_url.
	lockPolicy := existingCopyOnly
	if fileIO || writeTags {
		lockPolicy = createLibraryCopy
	}
	targetID, releaseCopy, err := mfs.lockLibraryCopy(id, lockPolicy, checkpoint)
	if err != nil {
		return err
	}
	defer releaseCopy()
	return mfs.finishFileWork(id, targetID, fileIO, writeTags, checkpoint)
}

// standDown runs a stand-down checkpoint before a file-writing step. A nil
// checkpoint always passes.
func standDown(checkpoint func() error, id, step string) error {
	if checkpoint == nil {
		return nil
	}
	if err := checkpoint(); err != nil {
		return fmt.Errorf("apply file work for book %s: stopped before %s: %w", id, step, err)
	}
	return nil
}

// FinishAutoFetchFileWork is the file side of auto-fetch (the per-book Fetch
// button, iTunes import enrichment; organize's fetch-first pass has no pool
// and reaches no file work). What it does, in order:
//
//  1. downloads the cover, only when the book has no local cover yet
//     (downloadAutoFetchCover);
//  2. stops there unless the book already has a library copy under root_dir
//     (the book itself, or a clean version sibling of a protected book). It
//     never creates one;
//  3. embeds the stored cover into that copy's audio files;
//  4. writes the tags ONLY when writeTags is set, which its callers pass as
//     write_back_metadata (off by default).
//
// It NEVER renames files. It does not run the apply rename pipeline, which
// renames under auto_rename_on_apply and tags under auto_write_tags_on_apply --
// both on by default, and both settings for explicit applies. Routing
// auto-fetch through that pipeline made the Fetch button move a library book's
// files to the naming-pattern path and retag them under default config; before
// this PR auto-fetch only embedded the cover and wrote tags under
// write_back_metadata, and that is what it does again.
//
// Locking matches FinishApplyFileWork: the book's own lock (lockBook) for the
// whole sequence and its library copy's (lockLibraryCopy) after the cover
// download, then the path lock on the files each write touches, the library
// copy's for a protected book. Callers reach it through the server's
// file-I/O pool (SetFileWorkScheduler); the restart replay
// (recoverAutoFetchFileOp) calls it too.
func (mfs *Service) FinishAutoFetchFileWork(id, pendingCoverURL string, writeTags bool) error {
	releaseBook := mfs.lockBook(id)
	defer releaseBook()
	mfs.downloadAutoFetchCover(id, pendingCoverURL)
	// existingCopyOnly never creates a copy, so no checkpoint; the error is a
	// failed read of the book.
	targetID, releaseCopy, err := mfs.lockLibraryCopy(id, existingCopyOnly, nil)
	if err != nil {
		return fmt.Errorf("auto-fetch file work: %w", err)
	}
	defer releaseCopy()
	book, err := mfs.db.GetBookByID(id)
	if err != nil || book == nil {
		return fmt.Errorf("auto-fetch file work: load book %s: %v", id, err)
	}
	if !mfs.autoFetchHasLibraryCopy(book) {
		slog.Info("auto-fetch: book has no library copy under root_dir; file work skipped (auto-fetch never creates one)",
			"book_id", logger.SanitizeLogValue(id), "path", logger.SanitizeLogValue(book.FilePath))
		return nil
	}
	mfs.embedCover(id, book, targetID)
	if !writeTags {
		return nil
	}
	release, err := mfs.lockWriteTarget(id, targetID)
	if err != nil {
		return fmt.Errorf("auto-fetch file work: %w", err)
	}
	defer release()
	if _, err := mfs.writeTags(id, targetID); err != nil {
		return fmt.Errorf("auto-fetch file work: write tags for book %s: %w", id, err)
	}
	return nil
}

// finishFileWork is steps 2 and 3 of FinishApplyFileWork, with the stand-down
// checkpoint run before each (nil passes). It runs after lockLibraryCopy, which
// made and locked any library copy the job needs and returned it as targetID.
// Every step here writes that target (fileWorkTarget) and none can make or
// switch to a copy outside that lock.
func (mfs *Service) finishFileWork(id, targetID string, fileIO, writeTags bool, checkpoint func() error) error {
	var tags tagWriteResult
	var fileErr error
	if fileIO {
		if err := standDown(checkpoint, id, "the file I/O"); err != nil {
			return err
		}
		tags, fileErr = mfs.applyMetadataFileIO(id, targetID)
	}
	if writeTags && !tags.handled {
		if err := standDown(checkpoint, id, "the tag write"); err != nil {
			if fileIO {
				slog.Warn("apply file work stopped after the file I/O: the book's files may be renamed but their tags are stale until it is applied again",
					"book_id", logger.SanitizeLogValue(id), "err", logger.SanitizeLogValue(err.Error()))
			}
			// errors.Join keeps a rename failure from the step above visible.
			return errors.Join(fileErr, err)
		}
		// Resolved and locked after the pipeline returned (and released its
		// own locks), so the key is the post-rename path of the files written.
		release, err := mfs.lockWriteTarget(id, targetID)
		if err != nil {
			tags.err = err
		} else {
			if _, err := mfs.writeTags(id, targetID); err != nil {
				tags.err = err
			}
			release()
		}
	}
	if fileErr != nil {
		return fileErr
	}
	if tags.err != nil {
		return fmt.Errorf("apply file work: write tags for book %s: %w", id, tags.err)
	}
	return nil
}

// embedCover embeds book id's stored cover into the audio files the file work
// writes (fileWorkTarget, checked against targetID, the book the job locked:
// the library copy's, for a protected book), holding the path lock on those
// files. Slow: ffmpeg. A target that no longer matches is logged and nothing
// is embedded; the embed reports no error by design (ApplyMetadataFileIO).
func (mfs *Service) embedCover(id string, book *database.Book, targetID string) {
	if config.AppConfig.RootDir == "" {
		return
	}
	target, err := mfs.fileWorkTarget(book, targetID)
	if err != nil {
		slog.Warn("cannot embed cover: the book's file-work target changed after it was locked",
			"book_id", logger.SanitizeLogValue(id), "error", logger.SanitizeLogValue(err.Error()))
		return
	}
	if target == nil {
		slog.Warn("cannot embed cover: protected book has no library copy",
			"book_id", logger.SanitizeLogValue(id), "book_title", logger.SanitizeLogValue(book.Title), "protected_path", logger.SanitizeLogValue(book.FilePath))
		return
	}
	release := mfs.lockPath(target.FilePath)
	defer release()
	mfs.embedCoverInBookFiles(target, metadata.CoverPathForBook(config.AppConfig.RootDir, id))
}

// ApplyMetadataFileIO runs the slow file operations after metadata is applied:
// cover embed, file rename and, under auto_write_tags_on_apply, the tag write.
// The cover DOWNLOAD is not here: it runs first, in FinishApplyFileWork, which
// is what apply paths should call. Designed to run in a background goroutine.
//
// It returns an error when the file work did not fully land. Until 2026-08-16
// it returned nothing and swallowed the pipeline failure into a slog.Warn, so
// no caller could tell a completed rename from a failed one — and
// applyCachedCandidateForBook reported Applied:true either way, i.e. the API
// said the apply succeeded while the files had never moved.
//
// WHAT A NON-NIL ERROR MEANS: "the file work did not fully land", NOT "nothing
// happened". runApplyPipeline deliberately persists the book_file rows for
// every rename that DID succeed before returning the failure, so a partial
// rename is durable and already recorded. Callers must therefore keep
// reporting the database apply as successful and flag only the file side --
// see applyOutcome.WriteBackFailed, which exists for exactly this shape.
//
// The cover embed is deliberately NOT part of the returned error:
// embedCoverInBookFiles reports nothing and a missing cover must not mask a
// rename failure or block the pipeline below it.
func (mfs *Service) ApplyMetadataFileIO(id string) error {
	releaseBook := mfs.lockBook(id)
	defer releaseBook()
	targetID, releaseCopy, err := mfs.lockLibraryCopy(id, createLibraryCopy, nil)
	if err != nil {
		return err
	}
	defer releaseCopy()
	_, err = mfs.applyMetadataFileIO(id, targetID)
	return err
}

// applyMetadataFileIO is ApplyMetadataFileIO that also reports what the
// pipeline did about tags, for FinishApplyFileWork's write-once guard. Its
// callers have run lockLibraryCopy, and it writes the target that locked.
func (mfs *Service) applyMetadataFileIO(id, targetID string) (tagWriteResult, error) {
	book, err := mfs.db.GetBookByID(id)
	if err != nil {
		return tagWriteResult{}, fmt.Errorf("apply file I/O: load book %s: %w", id, err)
	}
	if book == nil {
		// Reported rather than ignored: the two recovery handlers replay file
		// ops recorded before a restart, and a book that has since been deleted
		// is the one case where this is expected and benign. Naming it lets the
		// caller log which book vanished instead of silently doing nothing.
		return tagWriteResult{}, fmt.Errorf("apply file I/O: book %s not found", id)
	}

	mfs.embedCover(id, book, targetID)

	// Run file rename + tag write pipeline
	if config.AppConfig.AutoRenameOnApply || config.AppConfig.AutoWriteTagsOnApply {
		tags, err := mfs.runApplyPipeline(id, book, targetID)
		if err != nil {
			return tags, fmt.Errorf("apply file I/O: pipeline for book %s: %w", id, err)
		}
		return tags, nil
	}
	return tagWriteResult{}, nil
}

// computeITunesPath converts a local file path to an iTunes file:// URL
// using the configured path mappings (m.To = Linux prefix, m.From = Windows prefix).
// Returns an empty string if no mapping matches.
func ComputeITunesPath(localPath string) string {
	for _, m := range config.AppConfig.ITunes.PathMappings {
		if m.To == "" || m.From == "" {
			continue
		}
		// Separator-boundary match: To "/lib" must not rewrite "/lib2/…".
		if remainder, ok := pathutil.CutPathPrefix(localPath, m.To); ok {
			windowsPath := m.From + remainder
			encoded := url.PathEscape(windowsPath)
			encoded = strings.ReplaceAll(encoded, "%2F", "/")
			encoded = strings.ReplaceAll(encoded, "%3A", ":")
			return "file://localhost/" + encoded
		}
	}
	return ""
}

// removeEmptyDirs removes empty directories walking up from dir until reaching stopAt.
func removeEmptyDirs(dir, stopAt string) {
	for dir != stopAt && dir != "/" && dir != "." {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			break
		}
		if err := os.Remove(dir); err != nil {
			break
		}
		slog.Info("removed empty directory", "value", dir)
		dir = filepath.Dir(dir)
	}
}
