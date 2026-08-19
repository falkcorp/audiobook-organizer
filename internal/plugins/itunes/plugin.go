// file: internal/plugins/itunes/plugin.go
// version: 1.1.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-08-19

// Package itunes is the UOS plugin for iTunes/Music library operations.
// It wraps the internal iTunes service and registers OperationDefs through
// the public pkg/plugin/sdk interface.
package itunes

import (
	"fmt"

	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// Plugin is the iTunes plugin. It wraps the shared iTunes service so that
// the Run functions can call service methods without importing internal packages.
type Plugin struct {
	svc *itunesservice.Service
}

// New constructs an iTunes Plugin. svc may be nil or disabled;
// the Register method will return nil (nil-guard pattern).
//
// It took a database.Store until 2026-08-19 and never read it: an
// empty-interface compiler probe reported zero methods, and the field had
// exactly three mentions in the package -- this declaration, this parameter,
// and the registry lookup that fed it. Removing the parameter beats narrowing
// it; the dependency is deleted rather than shrunk.
func New(svc *itunesservice.Service) *Plugin {
	return &Plugin{svc: svc}
}

// ID implements sdk.Plugin.
func (p *Plugin) ID() string { return "itunes" }

// Name implements sdk.Plugin.
func (p *Plugin) Name() string { return "iTunes/Music Library" }

// Version implements sdk.Plugin.
func (p *Plugin) Version() string { return "1.0.0" }

// registeredDefs is the whitelist of OperationDefs this plugin puts into the
// registry.
//
// It is a whitelist, not "everything in this package", because registering a
// def does not ADD an operation -- it REPLACES whatever else claims that ID.
// Container.PostInit runs before NewServer's opRegistrars loop (server.go:567
// vs :625), so a plugin def always wins a collision and the server's
// registration is the one rejected.
//
// From 2026-07-17 until 2026-08-16 this package registered four stubs. Three of
// them -- itunes.sync, itunes.path-reconcile, itunes.path-repair -- shadowed
// the working implementations in internal/server/itunes_ops.go and
// itunes_path_ops.go, which wire Importer.Sync, Paths.Reconcile and
// Repair.Repair. Each stub returned nil, so all three operations reported
// "completed" in production without doing anything. The only evidence was one
// WARN per boot from the registrar loop, and that WARN was swallowed.
//
// The importDef exclusion below already described this exact hazard in a
// comment; it was simply never applied to its siblings.
//
// Split out of Register so the whitelist can be asserted in a test without an
// enabled Service (Enabled() reads an unexported deps.Config).
func (p *Plugin) registeredDefs() []sdk.OperationDef {
	return []sdk.OperationDef{
		// EXCLUDED, all stubs whose real implementation lives in internal/server:
		//   syncDef           -> server.RegisterITunesSyncOp (Importer.Sync)
		//   importDef         -> server.RegisterITunesImportOp (Importer.Execute)
		//   pathReconciledDef -> server.RegisterITunesPathReconcileOp (Paths.Reconcile)
		//   pathRepairDef     -> server.RegisterITunesPathRepairOp (Repair.Repair)
		//
		// positionSyncDef is a stub too, but it has no server-side counterpart,
		// so it stays registered: dropping it would make the op vanish rather
		// than fail, and "unknown op" is a worse error than an honest one. Its
		// Run returns an error instead of nil -- see runPositionSync. The real
		// implementation it should call, svc.Positions.Sync, exists and has
		// never been wired to anything.
		p.positionSyncDef(),
	}
}

// Register registers all iTunes OperationDefs with the UOS registry.
// Returns nil if the service is nil or disabled (nil-guard pattern).
func (p *Plugin) Register(r sdk.Registry) error {
	// Nil-guard: don't register if service not configured or disabled.
	if p.svc == nil || !p.svc.Enabled() {
		return nil
	}

	for _, def := range p.registeredDefs() {
		if err := r.RegisterOp(def); err != nil {
			return fmt.Errorf("register %s: %w", def.ID, err)
		}
	}

	return nil
}
