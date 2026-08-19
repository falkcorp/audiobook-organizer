// file: internal/server/deluge_importer_adapter.go
// version: 2.1.0
// guid: 7b3e9f21-4a0c-4d87-b5e8-1f6d2c0a3b74
// last-edited: 2026-08-19
//
// LibraryImporterAdapter has moved to internal/deluge/importer_adapter.go.
// This file re-exports the type and constructor for backward-compatibility
// within the server package.

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/deluge"
)

// LibraryImporterAdapter is re-exported from internal/deluge for backward
// compatibility. Use deluge.LibraryImporterAdapter directly in new code.
type LibraryImporterAdapter = deluge.LibraryImporterAdapter

// NewLibraryImporterAdapter creates a new adapter. See deluge.NewLibraryImporterAdapter.
func NewLibraryImporterAdapter(store delugeAdapterStore, delugeClient *deluge.Client, cfg *config.Config) *LibraryImporterAdapter {
	return deluge.NewLibraryImporterAdapter(store, delugeClient, cfg)
}
