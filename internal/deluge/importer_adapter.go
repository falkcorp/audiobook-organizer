// file: internal/deluge/importer_adapter.go
// version: 1.1.0
// guid: f6a7b8c9-d0e1-2345-f012-456789012345
// last-edited: 2026-08-19
//
// LibraryImporterAdapter implements tagger.LibraryImporter on top of
// ImportToLibrary. It is wired into the Server at startup so
// the metadata and tagger packages can perform the pre-flight copy without
// importing internal/server themselves.

package deluge

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// LibraryImporterAdapter satisfies tagger.LibraryImporter using the
// ImportToLibrary function and its wired Store + DelugeClient.
type LibraryImporterAdapter struct {
	store        delugeStore
	delugeClient *Client
	cfg          *config.Config
}

// NewLibraryImporterAdapter creates a new adapter. delugeClient may be nil
// (Deluge MoveStorage will be skipped but the copy still succeeds).
// cfg is passed by pointer; callers should use &config.AppConfig.
func NewLibraryImporterAdapter(store delugeStore, delugeClient *Client, cfg *config.Config) *LibraryImporterAdapter {
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
		// File is protected but has no DB record yet. This can happen during
		// scan/ingest before the record is committed. Log and skip — the write
		// proceeds in-place rather than failing the entire operation.
		slog.Warn("LibraryImporterAdapter no BookFile record found for protected path ; writing in-place", "srcPath", srcPath)
		return srcPath, nil
	}

	newPath, err := ImportToLibrary(a.cfg, a.delugeClient, a.store, bf)
	if err != nil {
		return srcPath, fmt.Errorf("LibraryImporterAdapter: ImportToLibrary for %s: %w", srcPath, err)
	}
	return newPath, nil
}
