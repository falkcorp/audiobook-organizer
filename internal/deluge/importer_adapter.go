// file: internal/deluge/importer_adapter.go
// version: 1.6.0
// guid: f6a7b8c9-d0e1-2345-f012-456789012345
// last-edited: 2026-09-14
//
// LibraryImporterAdapter implements tagger.LibraryImporter on top of
// ImportToLibrary. It is wired into the Server at startup so
// the metadata and tagger packages can perform the pre-flight copy without
// importing internal/server themselves.

package deluge

import (
	"context"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

// LibraryImporterAdapter satisfies tagger.LibraryImporter using the
// ImportToLibrary function and its wired Store + DelugeClient.
type LibraryImporterAdapter struct {
	store        Store
	delugeClient *Client
	cfg          *config.Config
	protected    tagger.PathChecker
}

// NewLibraryImporterAdapter creates a new adapter. delugeClient may be nil
// (Deluge MoveStorage will be skipped but the copy still succeeds).
// cfg is passed by pointer; callers should use &config.AppConfig.
// protected is the predicate the write guard uses (the server's
// ProtectedPathCache): ImportToLibrary needs it to tell a row imported to a
// library file from one that still names the protected copy. Nil disables
// those checks.
func NewLibraryImporterAdapter(store Store, delugeClient *Client, cfg *config.Config, protected tagger.PathChecker) *LibraryImporterAdapter {
	return &LibraryImporterAdapter{
		store:        store,
		delugeClient: delugeClient,
		cfg:          cfg,
		protected:    protected,
	}
}

// ImportPath implements tagger.LibraryImporter.
//
// It looks up the BookFile record by path and delegates to ImportToLibrary.
// If no matching record exists, it synthesises a minimal one so the copy
// still happens (the DB update step within ImportToLibrary will then fail
// and surface an error, which the caller should handle).
//
// bookFileID is the caller's row for srcPath ("" when it knows none). A
// non-empty ID that is not the row found at srcPath is refused: the path
// index holds one entry per path, and importing would repoint that other row.
// The import never asks Deluge to move the torrent's storage (NoMoveStorage):
// a guard-triggered copy lands at RootDir/<basename>, and moving storage there
// would make the library root a Deluge save path.
func (a *LibraryImporterAdapter) ImportPath(ctx context.Context, srcPath, bookFileID string) (string, error) {
	if a == nil || a.store == nil || a.cfg == nil {
		return srcPath, fmt.Errorf("LibraryImporterAdapter: not fully initialised")
	}

	bf, err := a.store.GetBookFileByPath(srcPath)
	if err != nil {
		return srcPath, fmt.Errorf("LibraryImporterAdapter: look up BookFile for %s: %w", srcPath, err)
	}
	if bf == nil {
		// File is protected but has no DB record yet (scan/ingest before the
		// record is committed). There is no row to repoint, so nothing can be
		// imported -- and the write must not proceed in place on a protected
		// file, which until 2026-09-13 it did. Refuse it.
		// Wraps tagger.ErrProtectedPathWrite: the file is left alone because
		// it is protected, which callers count as a skip, not a failure.
		return srcPath, fmt.Errorf("LibraryImporterAdapter: no BookFile record for protected path %s, so there is no row to repoint to a library copy: %w", srcPath, tagger.ErrProtectedPathWrite)
	}

	if bookFileID != "" && bf.ID != bookFileID {
		return srcPath, fmt.Errorf("LibraryImporterAdapter: the path index names book file %s (book %s) for %s, not the caller's row %s; refusing to import",
			bf.ID, bf.BookID, srcPath, bookFileID)
	}

	newPath, err := ImportToLibraryWith(a.cfg, a.delugeClient, a.store, bf, ImportOptions{
		Protected:          a.protected,
		ExpectedBookFileID: bookFileID,
		NoMoveStorage:      true,
	})
	if err != nil {
		if newPath != "" {
			// The copy could not be recorded AND could not be removed: name
			// it so it can be cleaned up, rather than dropping the path.
			return srcPath, fmt.Errorf("LibraryImporterAdapter: ImportToLibrary for %s left an unrecorded copy at %s: %w", srcPath, newPath, err)
		}
		return srcPath, fmt.Errorf("LibraryImporterAdapter: ImportToLibrary for %s: %w", srcPath, err)
	}
	return newPath, nil
}
