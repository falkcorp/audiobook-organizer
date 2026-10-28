// file: internal/versions/swap.go
// version: 1.6.0
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

// RunVersionSwap executes the primary-swap operation. It's designed
// to be called from both fresh requests and resume-on-restart.
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
) error {
	if progress == nil {
		progress = func(string, int) {}
	}

	// Load both versions.
	fromVer, err := store.GetBookVersion(params.FromVersionID)
	if err != nil || fromVer == nil {
		return fmt.Errorf("from-version %s not found: %w", params.FromVersionID, err)
	}
	toVer, err := store.GetBookVersion(params.ToVersionID)
	if err != nil || toVer == nil {
		return fmt.Errorf("to-version %s not found: %w", params.ToVersionID, err)
	}
	if fromVer.BookID != params.BookID || toVer.BookID != params.BookID {
		return fmt.Errorf("version/book mismatch")
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
	if err != nil || book == nil {
		return fmt.Errorf("book %s not found", params.BookID)
	}
	// Refused before the first status write, like a retired version: the swap
	// takes the book's folder to be the parent of FilePath (see bookPathIsFolder).
	if bookPathIsFolder(book.FilePath) {
		return fmt.Errorf("swap versions of book %s: %s", book.ID, folderBookReason(book.FilePath))
	}
	bookDir := filepath.Dir(book.FilePath)

	// ── Step 1: Mark DB transitional states ─────────────────────
	// Only set if not already in the transitional state (resume path).
	fromWas, toWas := fromVer.Status, toVer.Status
	if fromVer.Status != database.BookVersionStatusSwappingOut {
		fromVer.Status = database.BookVersionStatusSwappingOut
		if err := store.UpdateBookVersion(fromVer); err != nil {
			return fmt.Errorf("mark from swapping_out: %w", err)
		}
	}
	if toVer.Status != database.BookVersionStatusSwappingIn {
		toVer.Status = database.BookVersionStatusSwappingIn
		if err := store.UpdateBookVersion(toVer); err != nil {
			return fmt.Errorf("mark to swapping_in: %w", err)
		}
	}
	progress("db_transitional", 10)

	// ── Step 2: Move current primary files into .versions/{from} ─
	fromFiles, err := filesForVersion(store, params.BookID, params.FromVersionID)
	if err != nil {
		return fmt.Errorf("load from-files: %w", err)
	}
	fromPaths := filePaths(fromFiles)
	newFromPaths, errs := MoveToVersionsDir(bookDir, params.FromVersionID, fromPaths)
	if len(errs) > 0 {
		// Nothing has moved when every file is still where it was: the
		// transitional statuses (which also drop the book's active index) are
		// put back, so a swap that never started does not leave the book with
		// no active version. A partial move keeps them: the files are in two
		// places and a resume has to finish it.
		if !anyMoved(fromPaths, newFromPaths) {
			if rbErr := rollbackSwapStatuses(store, fromVer, fromWas, toVer, toWas); rbErr != nil {
				return fmt.Errorf("move-to-versions: %v (and the versions could not be put back: %w)", errs, rbErr)
			}
		}
		return fmt.Errorf("move-to-versions: %v", errs)
	}
	progress("fs_move_from", 40)

	// ── Step 3: Move target version files up to book root ────────
	toFiles, err := filesForVersion(store, params.BookID, params.ToVersionID)
	if err != nil {
		return fmt.Errorf("load to-files: %w", err)
	}
	toPaths := filePaths(toFiles)
	newToPaths, errs := MoveFromVersionsDir(bookDir, params.ToVersionID, toPaths)
	if len(errs) > 0 {
		return fmt.Errorf("move-from-versions: %v", errs)
	}
	progress("fs_move_to", 70)

	// ── Step 4: Finalize DB states + update file paths ──────────
	// Update from-version file paths to their .versions/ locations.
	for i, bf := range fromFiles {
		if i < len(newFromPaths) {
			bf.FilePath = newFromPaths[i]
			if err := store.UpdateBookFile(bf.ID, &bf); err != nil {
				slog.Warn("update from-file path", "bf", bf.ID, "err", err)
			}
		}
	}
	// Update to-version file paths to their new root locations.
	for i, bf := range toFiles {
		if i < len(newToPaths) {
			bf.FilePath = newToPaths[i]
			if err := store.UpdateBookFile(bf.ID, &bf); err != nil {
				slog.Warn("update to-file path", "bf", bf.ID, "err", err)
			}
		}
	}

	// Set final statuses.
	fromVer.Status = database.BookVersionStatusAlt
	if err := store.UpdateBookVersion(fromVer); err != nil {
		return fmt.Errorf("finalize from status: %w", err)
	}
	toVer.Status = database.BookVersionStatusActive
	if err := store.UpdateBookVersion(toVer); err != nil {
		return fmt.Errorf("finalize to status: %w", err)
	}

	// Update the book's primary file_path to the first file of the
	// new active version.
	if len(newToPaths) > 0 {
		oldPath := book.FilePath
		book.FilePath = newToPaths[0]
		// `book` was read before the file moves above, so it is not written
		// back whole: only file_path is set, on the row as stored now, and any
		// column another writer committed meanwhile is kept.
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
			slog.Warn("update book file_path", "error", err)
		} else {
			_ = store.RecordPathChange(&database.BookPathChange{
				BookID:     book.ID,
				OldPath:    oldPath,
				NewPath:    book.FilePath,
				ChangeType: "version_swap",
			})
		}
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

// anyMoved reports whether any of the files has left its old path for its new
// one.
func anyMoved(oldPaths, newPaths []string) bool {
	for i, old := range oldPaths {
		if i >= len(newPaths) || newPaths[i] == old {
			continue
		}
		if _, err := os.Stat(newPaths[i]); err == nil {
			return true
		}
	}
	return false
}

// rollbackSwapStatuses puts the two versions back to the statuses they had
// before the swap marked them. It tries both whatever the first one does, and
// returns what failed: the versions are then still in the transitional statuses
// and the swap's error tells the caller so.
func rollbackSwapStatuses(store versionUpdater, fromVer *database.BookVersion, fromWas string, toVer *database.BookVersion, toWas string) error {
	fromVer.Status = fromWas
	fromErr := store.UpdateBookVersion(fromVer)
	toVer.Status = toWas
	toErr := store.UpdateBookVersion(toVer)
	return errors.Join(fromErr, toErr)
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
