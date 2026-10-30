// file: internal/organizer/inplace_ownership.go
// version: 1.5.0
// guid: febbcbf3-dd0e-4f14-b704-ef979dcf2873
// last-edited: 2026-10-30

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
	// OutcomeProtectedSource: the source is protected once its symlinks are
	// followed or by the server's protected-path check (an iTunes library
	// folder, an import path, the quarantine folder, a Deluge save path). A
	// library path spelled through a link into such a folder names a file the
	// move would take out of it.
	OutcomeProtectedSource = "protected_source"
	// OutcomeProtectedListNotLoaded: nothing protects the source as far as the
	// loaded lists know, but the Deluge seeding list has not loaded (Deluge is
	// configured and unreachable), and an unloaded list reports every seeding
	// file unprotected, so no in-place move can be shown to be safe. Counted as a
	// skip, never a failure, and retried on the next run: it clears by itself once
	// the list loads.
	OutcomeProtectedListNotLoaded = "protected_list_not_loaded"
	// OutcomeUnknownGuardVerdict: the rename-source guard returned a verdict this
	// code does not know, the zero value of SourceVerdict included. The source is
	// not cleared, so it is not moved; counted as a skip and retried on a later run.
	OutcomeUnknownGuardVerdict = "unknown_guard_verdict"
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
	// OutcomeOutsideLibraryRoot: a file of a multi-file book is outside
	// RootDir -- typically a download or import folder a dedup merge
	// (MoveBookFilesToBook) left the book owning. Moving it into the library
	// would take it from the torrent client seeding it.
	OutcomeOutsideLibraryRoot = "outside_library_root"
	// OutcomeTargetDirShared: the book's target folder already holds another
	// book's files. Moving in would leave one folder holding two books, which
	// then neither can be moved out of. See reOrganizeMultiFileInPlace.
	OutcomeTargetDirShared = "target_dir_shared"
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

	if err := refuseProtectedInPlaceSource(src, target); err != nil {
		return err
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

// refuseProtectedInPlaceSource refuses to move src when it is protected in a
// way the spelled path does not show. The book's path is a library path, but it
// can be spelled through a symlink into the iTunes tree or another protected
// folder, and moveExclusive (link, then unlink) would then take the real file
// out of there.
//
//   - The frozen iTunes tree is tested on the symlink-resolved src as well as
//     the spelled one (the caller tests the spelled one).
//   - The server's protected-path check (SetRenameSourceGuard, the one RenameFiles
//     asks) is asked about src; it resolves symlinks and knows the iTunes
//     folders, import paths, .failed and the Deluge save paths. Nil, as in tests
//     that do not wire it, protects nothing. It also refuses while the Deluge
//     list has not loaded, as OutcomeProtectedListNotLoaded and not as a
//     protected location: the file is not known to be protected, the list that
//     could say so is not there yet.
//
// Fail closed: a src whose symlinks cannot be resolved is an error, not a pass.
func refuseProtectedInPlaceSource(src, target string) error {
	resolved, err := pathutil.ResolveExistingPrefix(src)
	if err != nil {
		return fmt.Errorf("cannot resolve the symlinks of %s to check it is not protected before moving it: %w", src, err)
	}
	if pathutil.UnderFrozenITunesTree(resolved) {
		return &DestinationConflictError{Category: OutcomeFrozenITunes, Source: src, Target: target,
			Reason: "the file resolves into the iTunes-managed tree (" + pathutil.FrozenITunesSegment + ") at " + resolved + "; it is never moved"}
	}
	guard := loadRenameSourceGuard()
	if guard == nil {
		return nil // no guard installed: nothing to ask
	}
	switch guard(src) {
	case SourceMovable:
		return nil
	case SourceProtected:
		return &DestinationConflictError{Category: OutcomeProtectedSource, Source: src, Target: target,
			Reason: "the file is in a protected location (an iTunes or import folder, the quarantine folder or a seeding path), directly or through a symlink; it is never moved"}
	case SourceListNotLoaded:
		return &DestinationConflictError{Category: OutcomeProtectedListNotLoaded, Source: src, Target: target,
			Reason: ProtectedListNotLoadedReason}
	default:
		// Only an explicit SourceMovable clears a source: the zero value or a
		// verdict from a newer guard is a refusal, not a pass.
		return &DestinationConflictError{Category: OutcomeUnknownGuardVerdict, Source: src, Target: target,
			Reason: UnknownGuardVerdictReason}
	}
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
