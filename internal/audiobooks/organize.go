// file: internal/audiobooks/organize.go
// version: 2.3.0
// guid: c3d4e5f6-a7b8-c9d0-e1f2-a3b4c5d6e7f8
// last-edited: 2026-07-13
//
// Thin forwarding layer — the real implementation now lives in
// internal/organizer/service.go. This file provides type aliases and
// constructor wrappers so the rest of the server package can keep using
// the old names without a large rename diff.

package audiobooks

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
)

// Type aliases — allow existing server code to keep using the old names.
type OrganizeService = organizer.Service
type OrganizeRequest = organizer.Request
type OrganizeStats = organizer.Stats

// NewOrganizeService creates a new organizer.Service and wires up
// server-specific callbacks (isProtectedPath, iTunes discovery, etc.).
func NewOrganizeService(db database.Store) *OrganizeService {
	svc := organizer.NewService(db)

	// Wire server-specific callbacks. iTunes callbacks are set later in
	// Server.New() after itunesSvc is constructed (see server.go).
	svc.ApplyOrganizedFileMetadata = func(book *database.Book, newPath string) {
		scanner.ApplyOrganizedFileMetadata(book, newPath)
	}
	svc.ComputeITunesPath = metafetch.ComputeITunesPath
	svc.FetchMetadataForBook = func(ctx context.Context, bookID string) (interface{}, error) {
		mfs := metafetch.NewService(db)
		return mfs.FetchMetadataForBook(ctx, bookID)
	}

	return svc
}
