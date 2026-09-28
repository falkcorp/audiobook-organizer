// file: internal/organizer/inplace_ownership.go
// version: 1.0.0
// guid: febbcbf3-dd0e-4f14-b704-ef979dcf2873
// last-edited: 2026-09-28

package organizer

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// Outcome categories for an in-place move the organizer refuses outright,
// before any destination logic. Like the collision outcomes they are keys of
// Stats.Collisions, so a refusal is counted as Skipped, never as Failed.
const (
	// OutcomeFrozenITunes: the source or the target lives in the hands-off
	// Original iTunes tree (pathutil.UnderFrozenITunesTree). iTunes owns those
	// files; moving one breaks the iTunes library that references it.
	OutcomeFrozenITunes = "frozen_itunes"
	// OutcomeOwnedByOtherBook: a file the move would take is claimed by a
	// book_file row of a DIFFERENT book. Moving it strands that book's row at
	// a path that no longer exists -- how the Eldest and Scattered Suns parents
	// lost their chapter files on 2026-09-27 to fragment books the scanner had
	// minted from those very chapters.
	OutcomeOwnedByOtherBook = "owned_by_other_book"
	// OutcomePartialMultiFile: the book's path is ONE file but the book owns
	// other present files too. The in-place move renames only the file at the
	// book's path, so the rest would be left behind under the old directory.
	OutcomePartialMultiFile = "partial_multi_file"
)

// refuseUnsafeInPlaceMove returns a *DestinationConflictError when moving src
// to target would damage something the move does not own, and nil when the
// move may go ahead. A store error is returned as-is: an ownership question
// that could not be answered is not a yes.
//
// It runs after the already-in-place check (nothing moves there) and before
// the destination is examined, so the answer depends only on the source.
func (orgSvc *Service) refuseUnsafeInPlaceMove(book *database.Book, src string, srcInfo os.FileInfo, target string) error {
	if pathutil.UnderFrozenITunesTree(src) || pathutil.UnderFrozenITunesTree(target) {
		return &DestinationConflictError{Category: OutcomeFrozenITunes, Source: src, Target: target,
			Reason: "the file is in the iTunes-managed tree (" + pathutil.FrozenITunesSegment + "); it is never moved"}
	}

	files := []string{src}
	if srcInfo != nil && srcInfo.IsDir() {
		var err error
		if files, err = regularFilesUnder(src); err != nil {
			return fmt.Errorf("cannot list %s to check file ownership before moving it: %w", src, err)
		}
	}

	owners := map[string]bool{} // bookID -> the book exists
	var claimedBy []string
	for _, f := range files {
		rows, err := database.BookFileRowsAtPath(orgSvc.db, f)
		if err != nil {
			return fmt.Errorf("cannot check who owns %s before moving it: %w", f, err)
		}
		for _, r := range rows {
			if r.BookID == book.ID {
				continue
			}
			exists, known := owners[r.BookID]
			if !known {
				b, err := orgSvc.db.GetBookByID(r.BookID)
				if err != nil {
					return fmt.Errorf("cannot read book %s, which claims %s: %w", r.BookID, f, err)
				}
				exists = b != nil
				owners[r.BookID] = exists
				if exists {
					claimedBy = append(claimedBy, r.BookID)
				}
			}
		}
	}
	if len(claimedBy) > 0 {
		slices.Sort(claimedBy)
		return &DestinationConflictError{Category: OutcomeOwnedByOtherBook, Source: src, Target: target,
			Reason: fmt.Sprintf("a file it would move is owned by another book's book_file row (%v); moving it would strand that book's row", claimedBy)}
	}

	if srcInfo != nil && !srcInfo.IsDir() {
		rows, err := orgSvc.db.GetBookFiles(book.ID)
		if err != nil {
			return fmt.Errorf("cannot load book_files of %s before moving %s: %w", book.ID, src, err)
		}
		// Only a book with SEVERAL present rows is multi-file. A single-file
		// book whose one row has drifted from its FilePath is a different,
		// known divergence that reOrganizeInPlace already flags for rescan.
		present, elsewhere := 0, false
		for _, r := range rows {
			if r.Missing {
				continue
			}
			present++
			if r.FilePath != src {
				elsewhere = true
			}
		}
		if present > 1 && elsewhere {
			return &DestinationConflictError{Category: OutcomePartialMultiFile, Source: src, Target: target,
				Reason: "the book owns other present files besides " + filepath.Base(src) + "; moving only its path would leave them behind"}
		}
	}
	return nil
}

// regularFilesUnder lists every regular file below dir, recursively.
func regularFilesUnder(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			out = append(out, p)
		}
		return nil
	})
	return out, err
}
