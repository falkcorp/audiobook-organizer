// file: internal/versions/swap.go
// version: 1.7.0
// guid: 6c3d5a2e-8b4c-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-10-28
//
// Primary-version swap tracked operation (spec 3.1 task 3).
//
// Swaps which BookVersion is "active" (primary) for a given book.
// The current primary's files move into .versions/{fromID}/ and
// the target version's files move up to the book's root directory.
//
// Every step is idempotent so the operation can be resumed after a
// crash: if swapping_in/swapping_out statuses are found on restart,
// the server re-runs from the filesystem move stage (the DB
// transitions are already committed).

package versions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// VersionSwapParams identifies the two versions involved in a swap.
type VersionSwapParams struct {
	BookID        string `json:"book_id"`
	FromVersionID string `json:"from_version_id"`
	ToVersionID   string `json:"to_version_id"`
}

// NotifyDelugeFunc is a callback for notifying Deluge after a version swap.
// The caller provides this from the server package where NotifyDelugeAfterVersionSwap lives.
// NotifyDelugeFunc is notified after a version swap. It takes no store — the
// implementor closes over its own — so this package does not propagate one.
type NotifyDelugeFunc func(fromVer, toVer *database.BookVersion, bookFilePath string)

// ErrSwapRefused is what RunVersionSwap returns (wrapped, errors.Is), having
// written nothing, when the swap cannot be done safely: the two versions are not
// of the book, the book's folder is unknown or a multi-file folder, or the files
// are not where the swap assumes (swapLayoutBlocker). A refusal is a conflict
// with the library's state, not a fault: the caller answers it with a 409.
var ErrSwapRefused = errors.New("the swap was refused before anything was written")

// ErrSwapIncomplete is what RunVersionSwap returns (wrapped, errors.Is) when
// the files have moved but the database could not be brought along. Nothing is
// put back: the files are in their new places and the versions stay in
// swapping_out/swapping_in, which needs a person to repair.
var ErrSwapIncomplete = errors.New("the swap moved the files but could not finish writing the database; the versions are left in swapping_out/swapping_in")

func swapRefusal(bookID, why string) error {
	return fmt.Errorf("swap versions of book %s: %w: %s", bookID, ErrSwapRefused, why)
}

// RunVersionSwap executes the primary-swap operation. It's designed
// to be called from both fresh requests and resume-on-restart.
//
// Before its first write it re-runs the preflight PickSwapTarget used (the
// layout check and the move check, see swapPreflight) and refuses with
// ErrSwapRefused if it fails. Once it has started, a failure puts everything it
// did back: files that were moved are moved back and the two versions get their
// old statuses (and with them the book's active index) again. Only a failure
// after the files have moved to stay (the database writes of step 4), or a
// move that could not be undone, leaves the versions in swapping_out/swapping_in.
//
// onWriteBack is called with the book ID after a successful swap so
// the caller can enqueue a write-back operation. May be nil.
//
// notifyDeluge is called to notify Deluge of the swap. May be nil.
func RunVersionSwap(
	ctx context.Context,
	store swapStore,
	params VersionSwapParams,
	progress func(step string, pct int),
	onWriteBack func(bookID string),
	notifyDeluge NotifyDelugeFunc,
) (retErr error) {
	if progress == nil {
		progress = func(string, int) {}
	}

	// Load both versions.
	fromVer, err := store.GetBookVersion(params.FromVersionID)
	if err != nil {
		return fmt.Errorf("load from-version %s: %w", params.FromVersionID, err)
	}
	if fromVer == nil {
		return fmt.Errorf("from-version %s not found", params.FromVersionID)
	}
	toVer, err := store.GetBookVersion(params.ToVersionID)
	if err != nil {
		return fmt.Errorf("load to-version %s: %w", params.ToVersionID, err)
	}
	if toVer == nil {
		return fmt.Errorf("to-version %s not found", params.ToVersionID)
	}
	if fromVer.BookID != params.BookID || toVer.BookID != params.BookID {
		return fmt.Errorf("%w: version/book mismatch", ErrSwapRefused)
	}
	// A retired version holds the retired files of another book and is outside
	// the version lifecycle: swapping one in would put those files in this
	// book's active slot, swapping one out would move them. Refused before the
	// first status write, so a refusal leaves both versions exactly as they were.
	if err := refuseRetired(fromVer, "swap out"); err != nil {
		return err
	}
	if err := refuseRetired(toVer, "swap in"); err != nil {
		return err
	}

	book, err := store.GetBookByID(params.BookID)
	if err != nil {
		return fmt.Errorf("load book %s: %w", params.BookID, err)
	}
	if book == nil {
		return fmt.Errorf("book %s not found", params.BookID)
	}
	// Refused before the first status write, like a retired version: the swap
	// takes the book's folder to be the parent of FilePath, which only holds for
	// an absolute path to a single file (see swapBookDir).
	bookDir, why := swapBookDir(book)
	if why != "" {
		return swapRefusal(book.ID, why)
	}

	// The files, and the preflight on them, before the first write: a swap that
	// would fail or do the wrong thing is refused with both versions untouched.
	fromFiles, err := filesForVersion(store, params.BookID, params.FromVersionID)
	if err != nil {
		return fmt.Errorf("load from-files: %w", err)
	}
	toFiles, err := filesForVersion(store, params.BookID, params.ToVersionID)
	if err != nil {
		return fmt.Errorf("load to-files: %w", err)
	}
	if why := swapPreflight(bookDir, params.FromVersionID, params.ToVersionID, fromFiles, toFiles); why != "" {
		return swapRefusal(book.ID, why)
	}
	fromPaths, toPaths := filePaths(fromFiles), filePaths(toFiles)
	fromSlot := VersionSlotDir(bookDir, params.FromVersionID)
	fromDst, toDst := destPaths(fromSlot, fromPaths), destPaths(bookDir, toPaths)
	// Files already where this swap would put them (a resumed swap) are not
	// this call's to move back.
	fromAlready, toAlready := movedFlags(fromPaths, fromDst), movedFlags(toPaths, toDst)

	// ── Step 1: Mark DB transitional states ─────────────────────
	// Only set if not already in the transitional state (resume path). Every
	// status this call changes is put back by the deferred restore on any error
	// until the files have moved for good; a status whose write failed was not
	// changed and is not touched.
	var marked []statusRestore
	statusesArmed := true
	defer func() {
		if retErr == nil || !statusesArmed {
			return
		}
		if rbErr := restoreStatuses(store, marked); rbErr != nil {
			retErr = fmt.Errorf("%w (and the versions could not be put back: %w)", retErr, rbErr)
		}
	}()
	if fromVer.Status != database.BookVersionStatusSwappingOut {
		was := fromVer.Status
		fromVer.Status = database.BookVersionStatusSwappingOut
		if err := store.UpdateBookVersion(fromVer); err != nil {
			fromVer.Status = was
			return fmt.Errorf("mark from swapping_out: %w", err)
		}
		marked = append(marked, statusRestore{fromVer, was})
	}
	if toVer.Status != database.BookVersionStatusSwappingIn {
		was := toVer.Status
		toVer.Status = database.BookVersionStatusSwappingIn
		if err := store.UpdateBookVersion(toVer); err != nil {
			toVer.Status = was
			return fmt.Errorf("mark to swapping_in: %w", err)
		}
		marked = append(marked, statusRestore{toVer, was})
	}
	progress("db_transitional", 10)

	// stuck is the error for a failed move whose undo failed too: the files are
	// in two places, so the statuses stay as they are for a person to finish.
	stuck := func(cause error, undoErr error) error {
		statusesArmed = false
		return fmt.Errorf("%w (and moving back what had moved failed: %w; the files are in two places and the versions are left in swapping_out/swapping_in)", cause, undoErr)
	}

	// ── Step 2: Move current primary files into .versions/{from} ─
	newFromPaths, errs := MoveToVersionsDir(bookDir, params.FromVersionID, fromPaths)
	if len(errs) > 0 {
		cause := fmt.Errorf("move-to-versions: %v", errs)
		if anyMoved(fromPaths, fromDst) {
			if undoErr := undoMoves(fromPaths, fromDst, fromAlready); undoErr != nil {
				return stuck(cause, undoErr)
			}
		}
		pruneEmptySlot(bookDir, params.FromVersionID)
		return cause
	}
	progress("fs_move_from", 40)

	// ── Step 3: Move target version files up to book root ────────
	newToPaths, errs := MoveFromVersionsDir(bookDir, params.ToVersionID, toPaths)
	if len(errs) > 0 {
		// Whatever of the entering version moved goes back first (it may sit on
		// a root path the leaving version vacated), then the leaving version's.
		cause := fmt.Errorf("move-from-versions: %v", errs)
		var undoErr error
		if anyMoved(toPaths, toDst) {
			undoErr = undoMoves(toPaths, toDst, toAlready)
		}
		// The leaving version's files are always put back: step 2 moved them.
		undoErr = errors.Join(undoErr, undoMoves(fromPaths, fromDst, fromAlready))
		if undoErr != nil {
			return stuck(cause, undoErr)
		}
		pruneEmptySlot(bookDir, params.FromVersionID)
		return cause
	}
	progress("fs_move_to", 70)

	// ── Step 4: Finalize DB states + update file paths ──────────
	// The files are in their new places now and stay there: from here a failure
	// does not put the statuses back (they say a swap is in progress, which is
	// true) and is returned, never only logged, so the caller does not go on to
	// trash a version whose swap did not finish. Every row is attempted, so one
	// failure does not leave the rest behind it.
	statusesArmed = false
	var rowErrs []error
	for i, bf := range fromFiles {
		if err := repointFile(store, params.BookID, bf.ID, fromPaths[i], newFromPaths[i]); err != nil {
			rowErrs = append(rowErrs, fmt.Errorf("from-file %s: %w", bf.ID, err))
		}
	}
	for i, bf := range toFiles {
		if err := repointFile(store, params.BookID, bf.ID, toPaths[i], newToPaths[i]); err != nil {
			rowErrs = append(rowErrs, fmt.Errorf("to-file %s: %w", bf.ID, err))
		}
	}
	if len(rowErrs) > 0 {
		return fmt.Errorf("%w: %w", ErrSwapIncomplete, errors.Join(rowErrs...))
	}

	// Update the book's primary file_path to the first file of the new active
	// version. `book` was read before the file moves above, so it is not
	// written back whole: only file_path is set, on the row as stored now, and
	// any column another writer committed meanwhile is kept.
	oldPath := book.FilePath
	updated, err := store.ModifyBook(book.ID, func(b *database.Book) error {
		if b.FilePath == newToPaths[0] {
			return database.ErrSkipBookWrite
		}
		b.FilePath = newToPaths[0]
		return nil
	})
	if err == nil && updated == nil {
		err = fmt.Errorf("book %s no longer exists", book.ID)
	}
	if err != nil {
		return fmt.Errorf("%w: update book file_path: %w", ErrSwapIncomplete, err)
	}
	book.FilePath = newToPaths[0]

	// Set final statuses.
	fromVer.Status = database.BookVersionStatusAlt
	if err := store.UpdateBookVersion(fromVer); err != nil {
		return fmt.Errorf("%w: finalize from status: %w", ErrSwapIncomplete, err)
	}
	toVer.Status = database.BookVersionStatusActive
	if err := store.UpdateBookVersion(toVer); err != nil {
		return fmt.Errorf("%w: finalize to status: %w", ErrSwapIncomplete, err)
	}

	// The audit record is not part of the swap: it is done, and failing it now
	// would leave a finished swap looking unfinished.
	if err := store.RecordPathChange(&database.BookPathChange{
		BookID:     book.ID,
		OldPath:    oldPath,
		NewPath:    book.FilePath,
		ChangeType: "version_swap",
	}); err != nil {
		slog.Warn("record version swap path change", "book", book.ID, "old", oldPath, "new", book.FilePath, "err", err)
	}
	progress("db_finalized", 90)

	// ── Step 5: Notify Deluge + enqueue writeback ──────────────
	if notifyDeluge != nil {
		notifyDeluge(fromVer, toVer, book.FilePath)
	}
	if onWriteBack != nil {
		onWriteBack(params.BookID)
	}
	progress("complete", 100)

	return nil
}

// repointFile sets one book_file row's path from oldPath to newPath, under the
// row's write stripe and only if the row still names oldPath: a row another
// writer repointed meanwhile is an error, not overwritten. A row already at
// newPath is left as it is.
func repointFile(store swapStore, bookID, fileID, oldPath, newPath string) error {
	row, err := store.ModifyBookFile(bookID, fileID, func(r *database.BookFile) error {
		switch r.FilePath {
		case newPath:
			return database.ErrSkipBookFileWrite
		case oldPath:
			r.FilePath = newPath
			return nil
		}
		return fmt.Errorf("the row names %s, not the %s the swap moved", r.FilePath, oldPath)
	})
	if err != nil {
		return err
	}
	if row == nil {
		return errors.New("the row no longer exists")
	}
	return nil
}

// filesForVersion returns the BookFile rows whose VersionID matches.
// If the library hasn't been migrated yet (VersionID still empty),
// returns all files for the book — the caller's move operations are
// still correct because the active version owns all top-level files.
func filesForVersion(store bookFileLister, bookID, versionID string) ([]database.BookFile, error) {
	all, err := store.GetBookFiles(bookID)
	if err != nil {
		return nil, err
	}
	var matched []database.BookFile
	for _, bf := range all {
		if bf.VersionID == versionID || bf.VersionID == "" {
			matched = append(matched, bf)
		}
	}
	return matched, nil
}

// destPaths is where each of paths lands when moved into dir.
func destPaths(dir string, paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = filepath.Join(dir, filepath.Base(p))
	}
	return out
}

// lexists reports whether p is there, a dangling symbolic link included.
func lexists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// fileMoved reports whether a file has gone from oldPath to newPath: its source
// is absent AND its destination is present. A destination that exists while the
// source is still there is not a move (the file was not taken, or the
// destination is someone else's), and neither is a source that is gone with no
// destination (the file is lost, which a move did not do).
func fileMoved(oldPath, newPath string) bool {
	return oldPath != newPath && !lexists(oldPath) && lexists(newPath)
}

// anyMoved reports whether any of the files has gone from its old path to its
// new one (see fileMoved).
func anyMoved(oldPaths, newPaths []string) bool {
	for i, old := range oldPaths {
		if i < len(newPaths) && fileMoved(old, newPaths[i]) {
			return true
		}
	}
	return false
}

// movedFlags reports, per file, fileMoved.
func movedFlags(oldPaths, newPaths []string) []bool {
	out := make([]bool, len(oldPaths))
	for i, old := range oldPaths {
		out[i] = i < len(newPaths) && fileMoved(old, newPaths[i])
	}
	return out
}

// undoMoves renames each file that has moved from oldPaths[i] to newPaths[i]
// back, except those flagged in already (they were moved before this call and
// are not its to undo). It tries every file and returns what failed.
func undoMoves(oldPaths, newPaths []string, already []bool) error {
	var errs []error
	for i, old := range oldPaths {
		if (i < len(already) && already[i]) || i >= len(newPaths) || !fileMoved(old, newPaths[i]) {
			continue
		}
		if err := os.Rename(newPaths[i], old); err != nil {
			errs = append(errs, fmt.Errorf("move %s back to %s: %w", newPaths[i], old, err))
		}
	}
	return errors.Join(errs...)
}

// pruneEmptySlot removes the version's slot folder, and .versions with it, when
// the move that was undone left them empty. Best effort: an empty folder is
// harmless. Only a real directory is removed: os.Remove would also take a
// regular file, or a symlink, that happens to sit at the slot's path.
func pruneEmptySlot(bookDir, versionID string) {
	slot := VersionSlotDir(bookDir, versionID)
	if fi, err := os.Lstat(slot); err == nil && fi.IsDir() {
		_ = os.Remove(slot) // fails, harmlessly, unless it is empty
	}
	_ = PruneEmptyVersionsDir(bookDir)
}

// statusRestore is a version and the status it had before the swap marked it.
type statusRestore struct {
	ver *database.BookVersion
	was string
}

// restoreStatuses puts the versions back to the statuses they had before the
// swap marked them. It tries every one whatever the first does, and returns what
// failed: those versions are then still in the transitional status and the
// swap's error says so.
func restoreStatuses(store versionUpdater, marked []statusRestore) error {
	var errs []error
	for _, m := range marked {
		m.ver.Status = m.was
		if err := store.UpdateBookVersion(m.ver); err != nil {
			errs = append(errs, fmt.Errorf("restore version %s to %s: %w", m.ver.ID, m.was, err))
		}
	}
	return errors.Join(errs...)
}

func filePaths(files []database.BookFile) []string {
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.FilePath
	}
	return paths
}

// ResumeVersionSwaps checks for any BookVersions in swapping_in or
// swapping_out status and resumes the swap operation. Called on
// server startup to recover from interrupted swaps.
func ResumeVersionSwaps(ctx context.Context, store unusedStore) {
	// Find versions in transitional states by scanning all versions.
	// In a large library this could be slow — a future optimization
	// would add an index key for transitional statuses. For now,
	// crash recovery is rare enough that a full scan is acceptable.
	//
	// Look for swapping_in versions — each one implies a swap was in
	// progress and the "to" version tells us the book + partner.
	// The "from" version is the one with swapping_out for the same book.

	// This is a best-effort scan. If the store doesn't support listing
	// by status, we skip — the UI can surface the broken state and
	// let the user manually re-trigger.
	slog.Info("[INFO] Checking for interrupted version swaps...")
	// TODO: implement full scan when ListBookVersionsByStatus is available
}
