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

// organizeServiceBase is what this wrapper forwards its store into: the
// organizer service it constructs, and the metafetch service the
// FetchMetadataForBook callback builds per call. Naming both rather than
// inlining their methods keeps this at two entries and means it re-narrows on
// its own whenever either package narrows further.
//
// database.Store until 2026-08-19, and it could not have been narrowed sooner:
// metafetch.NewService still took the union until the four-layer forwarding
// chain beneath it was narrowed first.
//
// It is what a store in the container's "store" slot is guaranteed to be: that
// slot holds whatever database.Store NewServer was given (a wrapper that embeds
// the interface, a generated mock), which does not have
// CreateBookWithRelocation, so the registry's Build cannot require it.
type organizeServiceBase interface {
	organizer.Store
	metafetch.Store
}

// organizeServiceStore is organizeServiceBase plus organizer.RelocatingCopyCreator,
// which is on it and not on database.Store (which may not grow:
// TestStoreFlattenedWidth). A direct caller of NewOrganizeService must hand in a
// store that has the method, and the compiler checks it. The production store
// is books' *database.PebbleStore (database.NewBooksStore returns the concrete
// type, so the assertion below is the proof); a container built over a store
// without it (a test's wrapper or mock) wires no creator, and the single-book
// organize copy then refuses (organizer.ErrRelocatingCopyCreatorNotWired)
// instead of creating a copy that moves no torrent.
type organizeServiceStore interface {
	organizeServiceBase
	organizer.RelocatingCopyCreator
}

// Compile-time proof that the production store satisfies it.
var _ organizeServiceStore = (*database.PebbleStore)(nil)

// RequireRelocatingCopyCreator is the production boot check: it fails, naming
// organizer.RelocatingCopyCreator, the method and the store's dynamic type, when
// store cannot create the single-book organize's copy together with its Deluge
// follow-up. Books' module Register calls it with the store it hands NewServer,
// so a wrapper that dropped the method would stop the boot instead of silently
// losing every Deluge move. (Tests build servers over mocks and wrappers and do
// not pass through it: the container wires the creator only when present.)
func RequireRelocatingCopyCreator(store any) error {
	if _, ok := store.(organizer.RelocatingCopyCreator); !ok {
		return fmt.Errorf("audiobooks: the store %T does not implement organizer.RelocatingCopyCreator (CreateBookWithRelocation): the single-book organize copy could not stage its Deluge follow-up", store)
	}
	return nil
}

// NewOrganizeService creates a new organizer.Service and wires up
// server-specific callbacks (isProtectedPath, iTunes discovery, etc.).
func NewOrganizeService(db organizeServiceStore) *OrganizeService {
	return newOrganizeService(db, db)
}

// newOrganizeService is NewOrganizeService over a store whose creator may be
// nil (the container's case: see organizeServiceBase).
func newOrganizeService(db organizeServiceBase, creator organizer.RelocatingCopyCreator) *OrganizeService {
	svc := organizer.NewService(db)
	if creator != nil {
		svc.SetRelocatingCopyCreator(creator)
	}

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
