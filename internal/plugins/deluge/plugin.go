// file: internal/plugins/deluge/plugin.go
// version: 1.1.0
// guid: a1b2c3d4-e5f6-47a8-b9c0-d1e2f3a4b5c6
// last-edited: 2026-08-19

// Package deluge implements the UOS plugin for Deluge integration operations.
package deluge

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	delugeclient "github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// Plugin wraps Deluge integration operations for the UOS registry.
// It owns a reference to the Deluge client and protected path cache.
type Plugin struct {
	client *delugeclient.Client
	cache  *delugeclient.ProtectedPathCache
	store  pluginStore
}

// New constructs a Deluge plugin. client and cache may be nil if Deluge is not configured;
// the plugin gracefully returns nil from Register if so.
func New(client *delugeclient.Client, cache *delugeclient.ProtectedPathCache, store pluginStore) *Plugin {
	return &Plugin{
		client: client,
		cache:  cache,
		store:  store,
	}
}

// ID implements sdk.Plugin.
func (p *Plugin) ID() string { return "deluge" }

// Name implements sdk.Plugin.
func (p *Plugin) Name() string { return "Deluge" }

// Version implements sdk.Plugin.
func (p *Plugin) Version() string { return "1.0.0" }

// Register registers all Deluge OperationDefs with the UOS registry.
// If Deluge is not configured (client == nil), Register returns nil silently.
func (p *Plugin) Register(r sdk.Registry) error {
	if p.client == nil || p.cache == nil {
		return nil
	}

	ops := []sdk.OperationDef{
		p.protectedPathsSyncDef(),
		p.centralizationDef(),
		p.pathUpdateDef(),
	}

	for _, op := range ops {
		if err := r.RegisterOp(op); err != nil {
			return err
		}
	}
	return nil
}

// pluginStore is what this plugin reads and writes, measured with an
// empty-interface compiler probe under -gcflags=-e: six methods, no forwarding
// constraints. It was pluginStore -- 398 methods -- until 2026-08-19.
type pluginStore interface {
	GetBookByID(id string) (*database.Book, error)
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
	GetBookFilesNeedingDelugeImportCore() ([]database.BookFileCore, error)
	GetBookVersionsByBookID(bookID string) ([]database.BookVersion, error)
	MarkFileImportedFromDeluge(ctx context.Context, originalPath, libraryPath, torrentHash string) error
	UpdateBookFile(id string, file *database.BookFile) error
}
