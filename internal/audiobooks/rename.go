// file: internal/audiobooks/rename.go
// version: 2.1.0
// guid: e5f6a7b8-c9d0-e1f2-a3b4-c5d6e7f8a9b0
// last-edited: 2026-08-18
//
// Thin forwarding layer — the real implementation now lives in
// internal/organizer/rename.go. This file provides type aliases and
// constructor wrappers so the rest of the server package can keep using
// the old names.

package audiobooks

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

// Type aliases for backward compatibility.
type RenameService = organizer.RenameService
type TagChange = organizer.TagChange
type RenamePreview = organizer.RenamePreview
type RenameApplyResult = organizer.RenameApplyResult

// organizerWrapperStore is what the thin organizer wrappers in this package
// need: the organizer package's own Store for the service being constructed,
// plus the two helpers this package binds into it by closure.
//
// Measured with an empty-interface compiler probe -- NewRenameService and
// NewOrganizePreviewService came back with exactly the same three constraints
// and no direct calls of their own, so they share one declaration rather than
// two identical ones. NewOrganizeService is the odd one out: it also builds a
// metafetch.Service, so it stays on database.Store until that is narrowed.
type organizerWrapperStore interface {
	organizer.Store
	importPathLister
	authorSeriesStore
}

// NewRenameService creates a new organizer.RenameService and wires up
// server-specific callbacks.
//
// IsProtectedPath / ResolveAuthorAndSeriesNames are bound to db via
// closures so the helpers can be free functions that take a store
// explicitly (SERVER-GLOBAL-STORE-AUDIT phase 6) without changing the
// function-value signatures organizer.RenameService exposes.
func NewRenameService(db organizerWrapperStore) *RenameService {
	svc := organizer.NewRenameService(db)
	svc.IsProtectedPath = func(filePath string) bool {
		return isProtectedPath(db, filePath)
	}
	svc.ResolveAuthorAndSeriesNames = func(book *database.Book) (string, string) {
		return resolveAuthorAndSeriesNames(db, book)
	}
	svc.FilterUnchangedTags = metafetch.FilterUnchangedTags
	svc.ComputeITunesPath = metafetch.ComputeITunesPath
	return svc
}
