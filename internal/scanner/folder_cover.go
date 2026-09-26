// file: internal/scanner/folder_cover.go
// version: 1.0.0
// guid: 2a7c9e14-5d3b-4f60-8b21-9e4d7f1c6a38
// last-edited: 2026-09-26

package scanner

import (
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/foldercover"
)

// fillFolderCover gives a newly created book the cover image from its own
// folder when it has no cover and no embedded art (internal/foldercover holds
// the whole rule). It runs after CreateBook has returned and every stripe is
// released: the folder read, the tag read and the cover write are IO, and the
// write itself is a ModifyBook that re-checks cover_url, so a cover set by
// anything else in between is never overwritten.
//
// A failure here never fails the scan: the book is created either way and
// maintenance.folder-cover-backfill picks up whatever this missed.
func fillFolderCover(book *database.Book) {
	store := getStore()
	if store == nil || book == nil || book.ID == "" || config.AppConfig.RootDir == "" {
		return
	}
	if book.CoverURL != nil && *book.CoverURL != "" {
		return
	}
	plan := foldercover.Apply(store, book, config.AppConfig.RootDir)
	switch plan.Outcome {
	case foldercover.OutcomeApplied:
		defaultLog.Info("folder cover set for %s from %s", book.ID, plan.Candidate.Path)
	case foldercover.OutcomeError, foldercover.OutcomeLocksUnavailable:
		defaultLog.Warn("folder cover skipped for %s (%s): %v", book.ID, plan.Outcome, plan.Err)
	}
}
