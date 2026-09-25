// file: internal/plugins/metafetch/plugin.go
// version: 1.3.0
// guid: 9c4d1f0a-2b7e-4c61-8a3d-5e9f0b1c2d34
// last-edited: 2026-09-25

// Package metafetch is the UOS plugin for metadata-fetch maintenance/analysis
// operations. It wraps the internal metafetch.Service (persisted candidate
// caches) and the database store, and registers OperationDefs through the
// public pkg/plugin/sdk interface.
//
// INIT-3-T1 introduces exactly one op here: metafetch.calibrate-scoring, a
// READ-ONLY scoring calibration harness (see calibrate_scoring.go). The
// package deliberately mirrors internal/plugins/dedup's structure — a thin
// Plugin wrapper, a register.go that self-registers via serviceregistry +
// PostInit, and a blank import in internal/plugins/plugins.go — rather than
// inventing a new op framework.
package metafetch

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// Plugin is the metafetch UOS plugin. It holds the read-only dependencies the
// calibration harness needs: the metafetch.Service (for persisted candidate
// caches via GetCachedCandidates) and the database store (for enumerating
// books and reading metadata-field-state override provenance). Neither is
// mutated by any op in this package.
type Plugin struct {
	store    pluginStore
	mfs      *metafetch.Service
	registry sdk.Registry // set in Register; unused by the read-only ops today
}

// New constructs a metafetch Plugin. Either dependency may be nil (e.g. the
// metafetch service failed to build); the ops return a descriptive error at
// run time rather than panicking.
func New(store pluginStore, mfs *metafetch.Service) *Plugin {
	return &Plugin{store: store, mfs: mfs}
}

// ID implements sdk.Plugin.
func (p *Plugin) ID() string { return "metafetch" }

// Name implements sdk.Plugin.
func (p *Plugin) Name() string { return "Metadata Fetch" }

// Version implements sdk.Plugin.
func (p *Plugin) Version() string { return "1.0.0" }

// Register registers all metafetch OperationDefs with the UOS registry.
func (p *Plugin) Register(r sdk.Registry) error {
	p.registry = r
	ops := p.OperationDefs()
	for _, op := range ops {
		if err := r.RegisterOp(op); err != nil {
			return err
		}
	}
	return nil
}

// OperationDefs returns every OperationDef this plugin registers, independent
// of whether its dependencies are wired. Register gates on those dependencies;
// this does not, so the op-ID ledger guard (internal/server
// TestOpIDs_NoRenameWithoutAlias) can enumerate the plugin's IDs and FormerIDs
// from a zero-value Plugin. Keep Register's list and this one the same list.
func (p *Plugin) OperationDefs() []sdk.OperationDef {
	return []sdk.OperationDef{
		p.calibrateScoringDef(), // INIT-3-T1: read-only scoring calibration report
	}
}

// pluginStore is what this plugin reads, measured with an empty-interface
// compiler probe under -gcflags=-e: two methods, no forwarding constraints. It
// was pluginStore -- 398 methods -- until 2026-08-19.
type pluginStore interface {
	GetAllBooksFullFrom(afterID string, limit int) ([]database.Book, error)
	GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error)
	// GetBookFiles feeds the calibration replay's book runtime
	// (database.LoadBookRuntime), the same runtime production scoring uses.
	GetBookFiles(bookID string) ([]database.BookFile, error)
}
