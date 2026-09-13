// file: internal/metafetch/rename_only_preflight.go
// version: 1.0.0
// guid: 5c7e9a1b-3d2f-4e6a-8b0c-2f4a6c8e0b17
// last-edited: 2026-09-13

package metafetch

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// RenameOnlyPreflight answers, before RunApplyPipelineRenameOnly moves
// anything, whether that rename is known to fail. It is RenamePreflight for
// the write-back ("Save to Files") path, which renames the book as it stands
// and has no candidate.
//
// Unlike RenamePreflight's preview it is not gated on auto_rename_on_apply:
// the write-back renames when the request asks for it even with that setting
// off, and a preflight that stood down in that configuration would never
// catch the failure it exists for.
//
// It refuses (wrapping ErrApplyFileWorkWouldFail) when the plan cannot be
// computed -- a broken pattern, or two files of the book on one target
// (organizer.ErrDuplicateRenameTarget, whose text is carried in the error) --
// or when a planned target is held under a recorded, unresolved collision
// that nothing has changed since. RunApplyPipelineRenameOnly would retry that
// rename and fail it the same way, part-way through the files.
//
// A book whose files cannot be listed is not a refusal: the rename reads the
// same rows and reports its own error. It is logged so it is never silent.
func (mfs *Service) RenameOnlyPreflight(id string) error {
	book, err := mfs.db.GetBookByID(id)
	if err != nil || book == nil {
		return fmt.Errorf("audiobook not found")
	}
	target, ok := mfs.existingLibraryCopy(book)
	if !ok || target == nil {
		// A protected book with no library copy yet: the rename makes the
		// copy first, so there is no plan to check here.
		return nil
	}
	files, err := mfs.db.GetBookFiles(target.ID)
	if err != nil {
		preflightLog.Warn("rename-only preflight could not list files of book %s; leaving the decision to the rename: %s",
			logger.SanitizeLogValue(target.ID), logger.SanitizeLogValue(err.Error()))
		return nil
	}
	files = dedupeBookFilesByPath(target.ID, files)
	if len(files) == 0 {
		files = virtualBookFiles(target.ID, target)
	}
	if len(files) == 0 {
		return nil
	}
	// The book as it stands, under its real ID: RunApplyPipelineRenameOnly
	// plans with the same row, so the organizer resolves author and series
	// through the same joins.
	planned := *target
	pv := mfs.previewRenamePlan(target.ID, &planned, files)
	if pv.Blocking != "" {
		return fmt.Errorf("%w: %s", ErrApplyFileWorkWouldFail, pv.Blocking)
	}
	return nil
}
