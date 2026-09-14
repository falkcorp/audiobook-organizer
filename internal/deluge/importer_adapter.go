// file: internal/deluge/importer_adapter.go
// version: 1.4.0
// guid: f6a7b8c9-d0e1-2345-f012-456789012345
// last-edited: 2026-09-13
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
)

// LibraryImporterAdapter satisfies tagger.LibraryImporter using the
// ImportToLibrary function and its wired Store + DelugeClient.
type LibraryImporterAdapter struct {
	store        Store
	delugeClient *Client
	cfg          *config.Config
}

// NewLibraryImporterAdapter creates a new adapter. delugeClient may be nil
// (Deluge MoveStorage will be skipped but the copy still succeeds).
// cfg is passed by pointer; callers should use &config.AppConfig.
func NewLibraryImporterAdapter(store Store, delugeClient *Client, cfg *config.Config) *LibraryImporterAdapter {
	return &LibraryImporterAdapter{
		store:        store,
		delugeClient: delugeClient,
		cfg:          cfg,
	}
}

// ImportPath implements tagger.LibraryImporter.
//
// It looks up the BookFile record by path and delegates to ImportToLibrary.
// If no matching record exists, it synthesises a minimal one so the copy
// still happens (the DB update step within ImportToLibrary will then fail
// and surface an error, which the caller should handle).
func (a *LibraryImporterAdapter) ImportPath(ctx context.Context, srcPath string) (string, error) {
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
		return srcPath, fmt.Errorf("LibraryImporterAdapter: no BookFile record for protected path %s; refusing to write it in place", srcPath)
	}

	newPath, err := ImportToLibrary(a.cfg, a.delugeClient, a.store, bf)
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
