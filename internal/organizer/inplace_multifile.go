// file: internal/organizer/inplace_multifile.go
// version: 1.0.0
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
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/oklog/ulid/v2"
)

// BookFileMove is one file a multi-file in-place move relocated: the
// book_file row, and where its file went from and to. CommitLanding records
// one undo.ChangeTypeBookFileMove row per move.
type BookFileMove struct {
	BookFileID string
	From, To   string
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
// Every file is checked before the first one moves -- iTunes tree, another
// book's claim (strict lookup), base-name clashes, occupied destinations --
// and a failure part-way through moves the already-moved files back. Rows
// flagged Missing stay where they are: there is no file to move.
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

	byBase := map[string]string{}
	var files []string
	var moves []BookFileMove
	for _, r := range present {
		if pathutil.UnderFrozenITunesTree(r.FilePath) {
			return "", nil, &DestinationConflictError{Category: OutcomeFrozenITunes, Source: r.FilePath, Target: targetDir,
				Reason: "a file of the book is in the iTunes-managed tree (" + pathutil.FrozenITunesSegment + "); it is never moved"}
		}
		base := filepath.Base(r.FilePath)
		if other, dup := byBase[base]; dup && other != r.FilePath {
			return "", nil, &DestinationConflictError{Category: OutcomeMultiFileNameClash, Source: r.FilePath, Target: targetDir,
				Reason: fmt.Sprintf("%s and %s share the name %q and cannot both land in one directory", other, r.FilePath, base)}
		}
		byBase[base] = r.FilePath
		files = append(files, r.FilePath)
		if dst := filepath.Join(targetDir, base); dst != r.FilePath {
			moves = append(moves, BookFileMove{BookFileID: r.ID, From: r.FilePath, To: dst})
		}
	}
	if err := orgSvc.refuseFilesOwnedElsewhere(book, files, src, targetDir); err != nil {
		return "", nil, err
	}
	// No declineChapterFolder here: it protects a SINGLE chapter of a
	// one-chapter-per-folder layout from losing the number its folder
	// carries. This moves every present file of the book together, and two
	// chapters whose base names would collide are refused above.

	// Files land INSIDE targetDir, which a single-file move into that folder
	// locks (lockInPlaceDestination keys on the target's parent), and the
	// directory itself is what a directory book moving to targetDir locks
	// (keyed on targetDir's parent). Hold both so this move serializes with
	// either kind.
	unlock := lockInPlaceDestinations(filepath.Join(targetDir, "_"), targetDir)
	defer unlock()
	for _, m := range moves {
		if _, err := os.Lstat(m.From); err != nil {
			return "", nil, fmt.Errorf("source %s of book %s is not readable (%v) — re-scan the library to update tracking", m.From, book.ID, err)
		}
		if _, err := os.Lstat(m.To); err == nil {
			return "", nil, &DestinationConflictError{Category: OutcomeMultiFileTargetOccupied, Source: m.From, Target: m.To,
				Reason: "a file of the book would land on a path that is already occupied"}
		}
	}
	if len(moves) > 0 {
		if err := os.MkdirAll(targetDir, 0o775); err != nil {
			return "", nil, fmt.Errorf("cannot create target directory %s: %w (check parent permissions and disk space)", targetDir, err)
		}
	}

	var moved []BookFileMove
	for _, m := range moves {
		if err := moveExclusive(m.From, m.To); err != nil {
			rbErr := rollbackFileMoves(moved)
			if errors.Is(err, fs.ErrExist) {
				err = &DestinationConflictError{Category: OutcomeMultiFileTargetOccupied, Source: m.From, Target: m.To,
					Reason: "the destination appeared while the book was being moved; every moved file was put back"}
			} else {
				err = fmt.Errorf("cannot move %s -> %s: %w", m.From, m.To, err)
			}
			if rbErr != nil {
				return "", nil, fmt.Errorf("%w; putting the already-moved files back also failed: %v", err, rbErr)
			}
			return "", nil, err
		}
		moved = append(moved, m)
	}

	// The files are where they belong now. Rewrite each row, then the book.
	// A row write that fails leaves that row naming the old path; the move is
	// not undone for it (the other rows already name new paths), and the book
	// is flagged so the next scan re-resolves the row, the same way the
	// single-file branch self-heals.
	rescan := false
	byID := make(map[string]database.BookFile, len(present))
	for _, r := range present {
		byID[r.ID] = r
	}
	for _, m := range moved {
		bf := byID[m.BookFileID]
		bf.FilePath = m.To
		bf.ITunesPath = orgSvc.ComputeITunesPath(m.To)
		if err := orgSvc.db.UpdateBookFile(bf.ID, &bf); err != nil {
			log.Warn("ReOrganizeInPlace: failed to update book_file %s path for book %s: %s", bf.ID, book.ID, err.Error())
			rescan = true
		}
	}
	organized := "organized"
	now := time.Now()
	if err := orgSvc.modifyBook(book.ID, func(b *database.Book) error {
		b.FilePath = targetDir
		b.LibraryState = &organized
		b.LastOrganizedAt = &now
		return nil
	}); err != nil {
		log.Warn("Failed to update book path for %s: %s", book.ID, err.Error())
		rescan = true
	}
	if rescan {
		_ = orgSvc.db.MarkNeedsRescan(book.ID)
	}
	book.FilePath = targetDir
	book.LibraryState = &organized
	book.LastOrganizedAt = &now

	cleaned := map[string]bool{}
	for _, m := range moved {
		if d := filepath.Dir(m.From); !cleaned[d] {
			cleaned[d] = true
			orgSvc.cleanupEmptyParents(d, config.AppConfig.RootDir, log)
		}
	}
	log.Info("Re-organized multi-file book %s: %d file(s) %s → %s", book.ID, len(moved), filepath.Dir(src), targetDir)
	return targetDir, moved, nil
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

// rollbackFileMoves puts already-moved files back, newest first.
func rollbackFileMoves(moved []BookFileMove) error {
	var errs []error
	for i := len(moved) - 1; i >= 0; i-- {
		if err := moveExclusive(moved[i].To, moved[i].From); err != nil {
			errs = append(errs, fmt.Errorf("%s -> %s: %w", moved[i].To, moved[i].From, err))
		}
	}
	return errors.Join(errs...)
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
// recorded as what happened instead: one restorable book_file_move per file,
// and a book_path_update for the book's path.
func RecordInPlaceMove(store OperationChangeWriter, bookID string, landing *Landing, oldPath, operationID string) {
	if store == nil || landing == nil || operationID == "" {
		return
	}
	add := func(changeType, field, oldValue, newValue string) {
		_ = store.CreateOperationChange(&database.OperationChange{
			ID:          ulid.Make().String(),
			OperationID: operationID,
			BookID:      bookID,
			ChangeType:  changeType,
			FieldName:   field,
			OldValue:    oldValue,
			NewValue:    newValue,
		})
	}
	if !landing.MultiFile {
		add("organize_rename", "file_path", oldPath, landing.Path)
		return
	}
	for _, m := range landing.FileMoves {
		add(undo.ChangeTypeBookFileMove, "book_file:"+m.BookFileID, m.From, m.To)
	}
	add(undo.ChangeTypeBookPathUpdate, "file_path", oldPath, landing.Path)
}
