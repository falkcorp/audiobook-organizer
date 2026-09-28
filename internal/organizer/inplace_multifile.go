// file: internal/organizer/inplace_multifile.go
// version: 1.1.0
// guid: 3f0f7d52-6a2e-4b8e-9d3c-1b7e5a0c9e41
// last-edited: 2026-09-28

package organizer

import (
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/oklog/ulid/v2"
)

// BookFileMove is one book_file row a multi-file in-place move repointed, and
// where its file went from and to. CommitLanding records one
// undo.ChangeTypeBookFileMove row per move. Two rows at the same path (a
// duplicate) are two BookFileMoves of one file move.
type BookFileMove struct {
	BookFileID string
	From, To   string
}

// multiFileMove is the file move the multi-file path performs, and the one its
// rollback performs. A variable only so tests can fail a move part-way;
// production always uses moveExclusive.
var multiFileMove = moveExclusive

// pathMove is one file of a multi-file move and every row that names it.
type pathMove struct {
	From, To string
	rowIDs   []string
}

// multiFileInPlaceRows reports whether book is a multi-file book whose path is
// ONE of its files -- the shape the scanner gives books it carves out of a
// shared directory (Book.sharesDirectory), which keep their FilePath on their
// first file. Such a book cannot be moved by renaming its path: that moves one
// chapter and leaves the rest behind. It returns the book's present rows when
// the shape matches.
func (orgSvc *Service) multiFileInPlaceRows(book *database.Book, info os.FileInfo) ([]database.BookFile, bool, error) {
	if info == nil || info.IsDir() {
		return nil, false, nil
	}
	rows, err := orgSvc.db.GetBookFiles(book.ID)
	if err != nil {
		return nil, false, fmt.Errorf("cannot load book_files of %s before moving %s: %w", book.ID, book.FilePath, err)
	}
	var present []database.BookFile
	elsewhere := false
	for _, r := range rows {
		if r.Missing || r.FilePath == "" {
			continue
		}
		present = append(present, r)
		if r.FilePath != book.FilePath {
			elsewhere = true
		}
	}
	if len(present) > 1 && elsewhere {
		return present, true, nil
	}
	return nil, false, nil
}

// reOrganizeMultiFileInPlace moves every present file of a multi-file book
// whose path is one of its files into the book's target directory, rewrites
// each book_file row to the file's new path, and points the book at the
// directory. Until 2026-09-28 such a book was refused outright
// (partial_multi_file), so the scanner's sub-grouped books could never be
// organized and stayed library_state=imported, which ABS hides.
//
// Nothing is deleted: no row is removed, and a file is only ever renamed.
// Every check runs under the destination lock and before the first file
// moves: iTunes tree, a file outside the library root, another book's claim on
// a source (strict lookup), another book's row -- Missing or not -- at a
// destination, base-name clashes, occupied destinations, and a target
// directory that already holds another book's files. A failure part-way
// through moves the already-moved files back, and a row or book write that
// fails after the moves undoes the whole move. Rows flagged Missing stay where
// they are: there is no file to move.
//
// The target layout for sub-grouped books. Each book goes to its OWN target
// directory (GenerateTargetDirPath, from its own metadata), so sub-groups with
// distinct titles -- Genesis, Exodus, ... of a Bible folder -- land in
// distinct folders. When two books' metadata name the SAME folder, the second
// is refused (target_dir_shared) rather than merged into the first's folder: a
// folder that holds two books' files can be moved by neither afterwards
// (refuseUnsafeInPlaceMove would see the other book's files), so a merge
// would strand both. The exception is a book whose files are ALREADY all in
// its target folder alongside other books' files -- the in-place sub-group of
// a flat folder whose books share a title. Nothing moves; the book keeps its
// path on its first file, which is the sub-group convention and names exactly
// this book's files, and is stamped organized. Pointing it at the folder would
// claim its siblings' files.
func (orgSvc *Service) reOrganizeMultiFileInPlace(book *database.Book, present []database.BookFile, log logger.Logger) (string, []BookFileMove, error) {
	org := orgSvc.newOrganizer()
	if !org.HasResolvedAuthor(book) {
		return "", nil, ErrAuthorUnresolved
	}
	src := book.FilePath
	targetDir, err := org.GenerateTargetDirPath(book)
	if err != nil {
		return "", nil, err
	}
	if pathutil.UnderFrozenITunesTree(targetDir) {
		return "", nil, &DestinationConflictError{Category: OutcomeFrozenITunes, Source: src, Target: targetDir,
			Reason: "the target is in the iTunes-managed tree (" + pathutil.FrozenITunesSegment + "); nothing is moved there"}
	}
	root := config.AppConfig.RootDir

	// Plan one move per distinct path. Duplicate rows at one path share it:
	// the file moves once and every row naming it is repointed.
	byPath := map[string]*pathMove{}
	byBase := map[string]string{}
	var files []string
	var plan []*pathMove
	for _, r := range present {
		if pathutil.UnderFrozenITunesTree(r.FilePath) {
			return "", nil, &DestinationConflictError{Category: OutcomeFrozenITunes, Source: r.FilePath, Target: targetDir,
				Reason: "a file of the book is in the iTunes-managed tree (" + pathutil.FrozenITunesSegment + "); it is never moved"}
		}
		// A dedup merge (MoveBookFilesToBook) can leave a library book owning a
		// row in the download or import folder. Moving it would pull the file
		// out of there -- a torrent client seeding it would lose it.
		if root == "" || !pathutil.IsWithin(r.FilePath, root) {
			return "", nil, &DestinationConflictError{Category: OutcomeOutsideLibraryRoot, Source: r.FilePath, Target: targetDir,
				Reason: "a file of the book is outside the library root (a download or import folder); moving it would take it from there"}
		}
		if pm, dup := byPath[r.FilePath]; dup {
			pm.rowIDs = append(pm.rowIDs, r.ID)
			continue
		}
		base := filepath.Base(r.FilePath)
		if other, clash := byBase[base]; clash {
			return "", nil, &DestinationConflictError{Category: OutcomeMultiFileNameClash, Source: r.FilePath, Target: targetDir,
				Reason: fmt.Sprintf("%s and %s share the name %q and cannot both land in one directory", other, r.FilePath, base)}
		}
		byBase[base] = r.FilePath
		files = append(files, r.FilePath)
		pm := &pathMove{From: r.FilePath, To: filepath.Join(targetDir, base), rowIDs: []string{r.ID}}
		byPath[r.FilePath] = pm
		if pm.To != pm.From {
			plan = append(plan, pm)
		}
	}
	// No declineChapterFolder here: it protects a SINGLE chapter of a
	// one-chapter-per-folder layout from losing the number its folder
	// carries. This moves every present file of the book together, and two
	// chapters whose base names would collide are refused above.

	// Files land INSIDE targetDir, which a single-file move into that folder
	// locks (lockInPlaceDestination keys on the target's parent), and the
	// directory itself is what a directory book moving to targetDir locks
	// (keyed on targetDir's parent). Hold both so this move serializes with
	// either kind. Every ownership and occupancy check below runs under the
	// lock, so a concurrent move into the same folder cannot slip between the
	// check and the move.
	unlock := lockInPlaceDestinations(filepath.Join(targetDir, "_"), targetDir)
	defer unlock()
	if err := orgSvc.refuseFilesOwnedElsewhere(book, files, src, targetDir); err != nil {
		return "", nil, err
	}
	for _, m := range plan {
		if _, err := os.Lstat(m.From); err != nil {
			return "", nil, fmt.Errorf("source %s of book %s is not readable (%v) — re-scan the library to update tracking", m.From, book.ID, err)
		}
		if _, err := os.Lstat(m.To); err == nil {
			return "", nil, &DestinationConflictError{Category: OutcomeMultiFileTargetOccupied, Source: m.From, Target: m.To,
				Reason: "a file of the book would land on a path that is already occupied"}
		}
		if err := orgSvc.refuseRowsAtDestination(book, m.From, m.To); err != nil {
			return "", nil, err
		}
	}
	own := make(map[string]bool, len(files))
	for _, f := range files {
		own[f] = true
	}
	foreign, err := orgSvc.foreignContentIn(book, targetDir, own, src)
	if err != nil {
		return "", nil, err
	}
	organized := "organized"
	now := time.Now()
	if foreign != "" {
		if len(plan) > 0 {
			return "", nil, &DestinationConflictError{Category: OutcomeTargetDirShared, Source: src, Target: targetDir,
				Reason: fmt.Sprintf("the target folder already holds another book's file (%s); two books in one folder could then be moved by neither", foreign)}
		}
		// Already in place among its siblings: see the doc comment. The path
		// stays on the first file.
		if err := orgSvc.modifyBook(book.ID, func(b *database.Book) error {
			b.LibraryState = &organized
			b.LastOrganizedAt = &now
			return nil
		}); err != nil {
			return "", nil, fmt.Errorf("cannot stamp in-place sub-grouped book %s as organized: %w", book.ID, err)
		}
		book.LibraryState = &organized
		book.LastOrganizedAt = &now
		return src, nil, nil
	}
	if len(plan) > 0 {
		if err := os.MkdirAll(targetDir, 0o775); err != nil {
			return "", nil, fmt.Errorf("cannot create target directory %s: %w (check parent permissions and disk space)", targetDir, err)
		}
	}

	var moved []*pathMove
	for _, m := range plan {
		if err := multiFileMove(m.From, m.To); err != nil {
			if failed, rbErr := putBack(moved); rbErr != nil {
				// NOT a DestinationConflictError, whatever err was: the book is
				// now split across two folders, which is a failure to report
				// (counted Failed, logged at warn, a 500), not a declined move.
				return "", nil, fmt.Errorf("moving %s -> %s failed (%v), and putting %d of %d already-moved file(s) back also failed: %v — book %s's files are split between their old folder and %s; no row was changed",
					m.From, m.To, err, len(failed), len(moved), rbErr, book.ID, targetDir)
			}
			if errors.Is(err, fs.ErrExist) {
				return "", nil, &DestinationConflictError{Category: OutcomeMultiFileTargetOccupied, Source: m.From, Target: m.To,
					Reason: "the destination appeared while the book was being moved; every moved file was put back"}
			}
			return "", nil, fmt.Errorf("cannot move %s -> %s: %w (every moved file was put back)", m.From, m.To, err)
		}
		moved = append(moved, m)
	}

	// The files are where they belong now. Rewrite each row, then the book.
	// Any write that fails undoes the whole move -- files back, rows already
	// rewritten restored -- because a half-written move leaves rows naming
	// paths that no longer hold their file, and the next scan would append the
	// moved files to the book a second time (a staged-arrival append).
	byID := make(map[string]database.BookFile, len(present))
	for _, r := range present {
		byID[r.ID] = r
	}
	var written []database.BookFile // each rewritten row as it was before
	var landed []BookFileMove
	for _, m := range moved {
		for _, id := range m.rowIDs {
			bf := byID[id]
			orig := bf
			bf.FilePath = m.To
			bf.ITunesPath = orgSvc.ComputeITunesPath(m.To)
			if err := orgSvc.db.UpdateBookFile(bf.ID, &bf); err != nil {
				return "", nil, orgSvc.undoMultiFileMove(book, moved, written,
					fmt.Errorf("cannot repoint book_file %s to %s: %w", bf.ID, m.To, err), log)
			}
			written = append(written, orig)
			landed = append(landed, BookFileMove{BookFileID: id, From: m.From, To: m.To})
		}
	}
	if err := orgSvc.modifyBook(book.ID, func(b *database.Book) error {
		b.FilePath = targetDir
		b.LibraryState = &organized
		b.LastOrganizedAt = &now
		return nil
	}); err != nil {
		return "", nil, orgSvc.undoMultiFileMove(book, moved, written,
			fmt.Errorf("cannot point book %s at %s: %w", book.ID, targetDir, err), log)
	}
	book.FilePath = targetDir
	book.LibraryState = &organized
	book.LastOrganizedAt = &now

	cleaned := map[string]bool{}
	for _, m := range moved {
		if d := filepath.Dir(m.From); !cleaned[d] {
			cleaned[d] = true
			orgSvc.cleanupEmptyParents(d, root, log)
		}
	}
	log.Info("Re-organized multi-file book %s: %d file(s) %s → %s", book.ID, len(moved), filepath.Dir(src), targetDir)
	return targetDir, landed, nil
}

// undoMultiFileMove reverses a multi-file move whose DB writes failed: every
// moved file goes back, and every row already rewritten is restored -- except
// a row whose file could not be moved back, which keeps naming the file where
// it is. The returned error always wraps cause; it says whether the undo was
// whole. When it was not, the book is flagged for rescan, and a failure to
// flag it is part of the error rather than dropped.
func (orgSvc *Service) undoMultiFileMove(book *database.Book, moved []*pathMove, written []database.BookFile, cause error, log logger.Logger) error {
	failed, rbErr := putBack(moved)
	stuck := make(map[string]bool, len(failed))
	for _, m := range failed {
		stuck[m.From] = true
	}
	var rowErrs []error
	for _, orig := range written {
		if stuck[orig.FilePath] {
			continue
		}
		o := orig
		if err := orgSvc.db.UpdateBookFile(o.ID, &o); err != nil {
			rowErrs = append(rowErrs, fmt.Errorf("restore book_file %s to %s: %w", o.ID, o.FilePath, err))
		}
	}
	if rbErr == nil && len(rowErrs) == 0 {
		return fmt.Errorf("%w; the move was undone: every file is back and every row restored", cause)
	}
	undoErr := errors.Join(rbErr, errors.Join(rowErrs...))
	if mErr := orgSvc.db.MarkNeedsRescan(book.ID); mErr != nil {
		log.Warn("ReOrganizeInPlace: could not flag book %s for rescan after an incomplete undo: %s", book.ID, mErr.Error())
		return fmt.Errorf("%v; undoing the move was incomplete (%v), and flagging book %s for rescan also failed: %v", cause, undoErr, book.ID, mErr)
	}
	return fmt.Errorf("%v; undoing the move was incomplete (%v); book %s is flagged for rescan", cause, undoErr, book.ID)
}

// refuseRowsAtDestination refuses a move onto dst when any OTHER book has a
// book_file row there, Missing or not. A Missing row at dst is invisible to
// the Lstat check, and landing a file under it makes two books claim one path;
// the merge path (bookfile_merge) then clears the other row's Missing flag
// and it silently owns this book's file.
func (orgSvc *Service) refuseRowsAtDestination(book *database.Book, src, dst string) error {
	rows, err := database.BookFileRowsAtPathStrict(orgSvc.db, dst)
	if errors.Is(err, database.ErrOwnershipLookupIncomplete) {
		return &DestinationConflictError{Category: OutcomeOwnershipUnverified, Source: src, Target: dst,
			Reason: "the complete book_file ownership index is not available yet (memdb warming up); retried on a later run"}
	}
	if err != nil {
		return fmt.Errorf("cannot check who claims %s before moving a file there: %w", dst, err)
	}
	for _, r := range rows {
		if r.BookID != book.ID {
			return &DestinationConflictError{Category: OutcomeMultiFileTargetOccupied, Source: src, Target: dst,
				Reason: fmt.Sprintf("book %s already has a book_file row at the destination (missing=%v); two books would claim one path", r.BookID, r.Missing)}
		}
	}
	return nil
}

// foreignContentIn returns the first file under dir that is not this book's:
// a file another book has a row for, or an untracked audio file. Sidecars
// nobody tracks (cover.jpg, .nfo) are not foreign. "" means dir is absent,
// empty, or holds only this book's files.
func (orgSvc *Service) foreignContentIn(book *database.Book, dir string, own map[string]bool, src string) (string, error) {
	info, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("cannot inspect target directory %s: %w", dir, err)
	}
	if !info.IsDir() {
		return dir, nil
	}
	found, err := regularFilesUnder(dir)
	if err != nil {
		return "", fmt.Errorf("cannot list target directory %s: %w", dir, err)
	}
	audio := make(map[string]bool, len(config.AppConfig.SupportedExtensions))
	for _, ext := range config.AppConfig.SupportedExtensions {
		audio[strings.ToLower(ext)] = true
	}
	for _, f := range found {
		if own[f] {
			continue
		}
		rows, err := database.BookFileRowsAtPathStrict(orgSvc.db, f)
		if errors.Is(err, database.ErrOwnershipLookupIncomplete) {
			return "", &DestinationConflictError{Category: OutcomeOwnershipUnverified, Source: src, Target: dir,
				Reason: "the complete book_file ownership index is not available yet (memdb warming up); retried on a later run"}
		}
		if err != nil {
			return "", fmt.Errorf("cannot check who claims %s in the target directory: %w", f, err)
		}
		for _, r := range rows {
			if r.BookID != book.ID {
				return f, nil
			}
		}
		if len(rows) == 0 && audio[strings.ToLower(filepath.Ext(f))] {
			return f, nil
		}
	}
	return "", nil
}

// lockInPlaceDestinations takes the lockInPlaceDestination stripe of every
// target, each stripe once and in index order so two callers holding
// overlapping sets cannot deadlock.
func lockInPlaceDestinations(targets ...string) func() {
	var idx []uint32
	for _, t := range targets {
		h := fnv.New32a()
		_, _ = h.Write([]byte(filepath.Clean(filepath.Dir(t))))
		i := h.Sum32() % inPlaceDestStripes
		if !slices.Contains(idx, i) {
			idx = append(idx, i)
		}
	}
	slices.Sort(idx)
	for _, i := range idx {
		inPlaceDestLocks[i].Lock()
	}
	return func() {
		for j := len(idx) - 1; j >= 0; j-- {
			inPlaceDestLocks[idx[j]].Unlock()
		}
	}
}

// putBack moves already-moved files back, newest first, and returns the ones
// that could not be moved back.
func putBack(moved []*pathMove) ([]*pathMove, error) {
	var failed []*pathMove
	var errs []error
	for i := len(moved) - 1; i >= 0; i-- {
		if err := multiFileMove(moved[i].To, moved[i].From); err != nil {
			failed = append(failed, moved[i])
			errs = append(errs, fmt.Errorf("%s -> %s: %w", moved[i].To, moved[i].From, err))
		}
	}
	return failed, errors.Join(errs...)
}

// OperationChangeWriter is the one store method RecordInPlaceMove needs.
type OperationChangeWriter interface {
	CreateOperationChange(change *database.OperationChange) error
}

// RecordInPlaceMove writes the undo record of an in-place landing that moved
// the book from oldPath to landing.Path. Every caller that commits an in-place
// landing (CommitLanding, the single-book organize handler) goes through it so
// the two cannot record the same move differently.
//
// A multi-file move is not one rename: the book's path went from its first
// file to the new directory with no single file system object moving between
// them, and each file moved on its own. Recording it as organize_rename would
// have the undo rename the DIRECTORY onto the first file's old path. It is
// recorded as what happened instead: a book_path_update for the book's path,
// then one restorable book_file_move per row. The path update is written
// FIRST because an undo reverts newest-first: the book's path goes back only
// after every file has, so it never names a file that is not there yet.
//
// It returns every record it could not write. A move with a missing record
// cannot be undone, so the callers log the error at warn.
func RecordInPlaceMove(store OperationChangeWriter, bookID string, landing *Landing, oldPath, operationID string) error {
	if store == nil || landing == nil || operationID == "" {
		return nil
	}
	var errs []error
	add := func(changeType, field, oldValue, newValue string) {
		if err := store.CreateOperationChange(&database.OperationChange{
			ID:          ulid.Make().String(),
			OperationID: operationID,
			BookID:      bookID,
			ChangeType:  changeType,
			FieldName:   field,
			OldValue:    oldValue,
			NewValue:    newValue,
		}); err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", changeType, field, err))
		}
	}
	if !landing.MultiFile {
		add("organize_rename", "file_path", oldPath, landing.Path)
	} else {
		add(undo.ChangeTypeBookPathUpdate, "file_path", oldPath, landing.Path)
		for _, m := range landing.FileMoves {
			add(undo.ChangeTypeBookFileMove, "book_file:"+m.BookFileID, m.From, m.To)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("could not record %d undo row(s) of the in-place move of book %s (operation %s); that part of the move cannot be undone: %w",
			len(errs), bookID, operationID, errors.Join(errs...))
	}
	return nil
}
