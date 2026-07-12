// file: internal/plugins/metafetch/plugin.go
// version: 1.0.0
// guid: 9c4d1f0a-2b7e-4c61-8a3d-5e9f0b1c2d34
// last-edited: 2026-07-11

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
	store    database.Store
	mfs      *metafetch.Service
	registry sdk.Registry // set in Register; unused by the read-only ops today
}

// New constructs a metafetch Plugin. Either dependency may be nil (e.g. the
// metafetch service failed to build); the ops return a descriptive error at
// run time rather than panicking.
func New(store database.Store, mfs *metafetch.Service) *Plugin {
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
	ops := []sdk.OperationDef{
		p.calibrateScoringDef(), // INIT-3-T1: read-only scoring calibration report
	}
	for _, op := range ops {
		if err := r.RegisterOp(op); err != nil {
			return err
		}
	}
	return nil
}
