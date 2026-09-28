// file: internal/organizer/inplace_ownership.go
// version: 1.1.0
// guid: febbcbf3-dd0e-4f14-b704-ef979dcf2873
// last-edited: 2026-09-28

package organizer

import (
	"errors"
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
// Stats.Collisions, so a refusal is counted as Skipped, never as Failed, and
// none of them records a durable skip: the book is retried on the next run.
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
	// OutcomeOwnershipUnverified: the complete multi-row ownership index is not
	// available (memdb still warming up after a restart), so "no other book
	// owns this file" cannot be established. The single-row fallback the
	// scanner accepts can hide a second owner, which is not good enough for a
	// move. Retried on a later run.
	OutcomeOwnershipUnverified = "ownership_unverified"
	// OutcomeMultiFileNameClash: two present files of a multi-file book share
	// a base name, so they cannot both land in one target directory.
	OutcomeMultiFileNameClash = "multi_file_name_clash"
	// OutcomeMultiFileTargetOccupied: a file of a multi-file book would land
	// on a path something else already occupies.
	OutcomeMultiFileTargetOccupied = "multi_file_target_occupied"
)

// refuseUnsafeInPlaceMove returns a *DestinationConflictError when moving src
// to target would damage something the move does not own, and nil when the
// move may go ahead. A store error is returned as-is: an ownership question
// that could not be answered is not a yes.
//
// It runs after the already-in-place check (nothing moves there) and before
// the destination is examined, so the answer depends only on the source.
// Multi-file books whose path is one of their files never reach it: they take
// reOrganizeMultiFileInPlace, which checks every file the same way.
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
	return orgSvc.refuseFilesOwnedElsewhere(book, files, src, target)
}

// refuseFilesOwnedElsewhere returns a *DestinationConflictError when any of
// files is claimed by a book other than book, or when that cannot be
// established because the complete ownership index is unavailable.
//
// Soft-deleted owners: a soft-deleted book still owns its rows (restore puts
// the book back with them), so its claim counts -- UNLESS a live book also
// claims the same file, in which case the live claim is the one that matters
// and the soft-deleted one is on its way out. The book being moved is live
// here (the organizer skips soft-deleted books), so a file this book owns is
// never blocked by a soft-deleted co-owner.
func (orgSvc *Service) refuseFilesOwnedElsewhere(book *database.Book, files []string, src, target string) error {
	type ownerState struct{ exists, softDeleted bool }
	owners := map[string]ownerState{}
	claimed := map[string]bool{}
	for _, f := range files {
		rows, err := database.BookFileRowsAtPathStrict(orgSvc.db, f)
		if errors.Is(err, database.ErrOwnershipLookupIncomplete) {
			return &DestinationConflictError{Category: OutcomeOwnershipUnverified, Source: src, Target: target,
				Reason: "the complete book_file ownership index is not available yet (memdb warming up); retried on a later run"}
		}
		if err != nil {
			return fmt.Errorf("cannot check who owns %s before moving it: %w", f, err)
		}
		liveClaim := false // some live book (this one included) claims f
		var liveOthers, softOthers []string
		for _, r := range rows {
			if r.BookID == book.ID {
				liveClaim = true
				continue
			}
			st, known := owners[r.BookID]
			if !known {
				b, err := orgSvc.db.GetBookByID(r.BookID)
				if err != nil {
					return fmt.Errorf("cannot read book %s, which claims %s: %w", r.BookID, f, err)
				}
				st = ownerState{exists: b != nil, softDeleted: b != nil && b.IsSoftDeleted()}
				owners[r.BookID] = st
			}
			switch {
			case !st.exists:
				// dangling row: its book is gone, so it owns nothing
			case st.softDeleted:
				softOthers = append(softOthers, r.BookID)
			default:
				liveClaim = true
				liveOthers = append(liveOthers, r.BookID)
			}
		}
		for _, id := range liveOthers {
			claimed[id] = true
		}
		if !liveClaim {
			for _, id := range softOthers {
				claimed[id] = true
			}
		}
	}
	if len(claimed) == 0 {
		return nil
	}
	claimedBy := make([]string, 0, len(claimed))
	for id := range claimed {
		claimedBy = append(claimedBy, id)
	}
	slices.Sort(claimedBy)
	return &DestinationConflictError{Category: OutcomeOwnedByOtherBook, Source: src, Target: target,
		Reason: fmt.Sprintf("a file it would move is owned by another book's book_file row (%v); moving it would strand that book's row", claimedBy)}
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
