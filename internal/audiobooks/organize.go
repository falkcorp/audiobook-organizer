// file: internal/audiobooks/organize.go
// version: 2.5.0
// guid: c3d4e5f6-a7b8-c9d0-e1f2-a3b4c5d6e7f8
// last-edited: 2026-11-17
//
// Thin forwarding layer — the real implementation now lives in
// internal/organizer/service.go. This file provides type aliases and
// constructor wrappers so the rest of the server package can keep using
// the old names without a large rename diff.

package audiobooks

import (
	"context"
	"errors"
	"fmt"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
)

// Type aliases — allow existing server code to keep using the old names.
type OrganizeService = organizer.Service
type OrganizeRequest = organizer.Request
type OrganizeStats = organizer.Stats

// organizeServiceStore is what this wrapper forwards its store into: the
// organizer service it constructs, and the metafetch service the
// FetchMetadataForBook callback builds per call. Naming both rather than
// inlining their methods keeps this at two entries and means it re-narrows on
// its own whenever either package narrows further.
//
// database.Store until 2026-08-19, and it could not have been narrowed sooner:
// metafetch.NewService still took the union until the four-layer forwarding
// chain beneath it was narrowed first.
//
// organizer.RelocatingCopyCreator is on it, and not on database.Store (which may
// not grow): the one value in the container's "store" slot is a
// *database.PebbleStore, which has the method, and the registry's Get panics at
// boot, naming both types, if a store without it is ever put there.
type organizeServiceStore interface {
	organizer.Store
	organizer.RelocatingCopyCreator
	metafetch.Store
}

// Compile-time proof that the production store satisfies it.
var _ organizeServiceStore = (*database.PebbleStore)(nil)

// NewOrganizeService creates a new organizer.Service and wires up
// server-specific callbacks (isProtectedPath, iTunes discovery, etc.).
func NewOrganizeService(db organizeServiceStore) *OrganizeService {
	svc := organizer.NewService(db)
	svc.SetRelocatingCopyCreator(db)

	// Wire server-specific callbacks. iTunes callbacks are set later in
	// Server.New() after itunesSvc is constructed (see server.go).
	svc.ApplyOrganizedFileMetadata = func(book *database.Book, newPath string) {
		scanner.ApplyOrganizedFileMetadata(book, newPath)
	}
	svc.ComputeITunesPath = metafetch.ComputeITunesPath
	svc.FetchMetadataForBook = func(ctx context.Context, bookID string) (any, error) {
		mfs := metafetch.NewService(db)
		// Organize works through the library and nobody picks a book: the
		// fetch is Unattended, and a retired book is reported to the organizer
		// as a skip it can count (its package cannot import metafetch).
		resp, err := mfs.FetchMetadataForBook(ctx, bookID, metafetch.Unattended)
		if errors.Is(err, metafetch.ErrBookRetired) {
			return nil, fmt.Errorf("%w: %w", organizer.ErrFetchSkippedRetired, err)
		}
		return resp, err
	}

	return svc
}
