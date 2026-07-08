// file: internal/plugins/acoustid/plugin.go
// version: 1.1.0
// guid: d4e5f6a7-b8c9-0123-def0-123456789abc
// last-edited: 2026-07-07

// Package acoustid is the UOS plugin for AcoustID fingerprinting operations.
// It wraps the internal dedup.Engine and registers OperationDefs through
// the public pkg/plugin/sdk interface.
package acoustid

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// Plugin is the AcoustID plugin. It wraps the shared dedup.Engine and embedding store so that
// the Run functions can call engine methods without importing internal packages.
type Plugin struct {
	engine         *dedupengine.Engine
	store          database.Store
	embeddingStore *database.EmbeddingStore
}

// New constructs an acoustid Plugin. engine and embeddingStore may be nil if embedding is disabled;
// the plugin will no-op gracefully in that case.
func New(engine *dedupengine.Engine, store database.Store, embeddingStore *database.EmbeddingStore) *Plugin {
	return &Plugin{engine: engine, store: store, embeddingStore: embeddingStore}
}

// ID implements sdk.Plugin.
func (p *Plugin) ID() string { return "acoustid" }

// Name implements sdk.Plugin.
func (p *Plugin) Name() string { return "AcoustID" }

// Version implements sdk.Plugin.
func (p *Plugin) Version() string { return "1.0.0" }

// Register registers all AcoustID OperationDefs with the UOS registry.
func (p *Plugin) Register(r sdk.Registry) error {
	if p.engine == nil {
		return nil
	}

	ops := []sdk.OperationDef{
		p.scanDef(),
		p.backfillDef(),
		p.fingerprintRescanDef(),
		p.resetAllDef(),
		p.lshBackfillDef(),
		p.onlineLookupDef(),
		p.durationBackfillDef(),
	}

	for _, op := range ops {
		if err := r.RegisterOp(op); err != nil {
			return err
		}
	}
	return nil
}
