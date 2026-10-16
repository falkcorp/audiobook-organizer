// file: internal/opscatalog/catalog.go
// version: 1.0.0
// guid: 3abf83c4-a38d-47db-a90d-d022d97f7178
// last-edited: 2026-10-16

// Package opscatalog is the one list of operation bundles linked into the
// server binary (05-operations-v3 sdk-api.md §11, 05-PR7).
//
// It holds three things:
//
//   - All: every native ops v3 bundle. Empty at 05-PR7; each port wave adds
//     one entry per package. oplint fails a package that defines
//     Ops(...) ops.Bundle and is not referenced here.
//   - the service-registry plugins (the five imports below). This list used to
//     be internal/plugins/plugins.go, which internal/server blank-imported;
//     metafetch was listed there while nothing in the binary imported that
//     package, so from INIT-3 until 2026-10-01 its ops were never registered.
//     internal/server imports this package, and TestCatalogLinkedIntoBinary
//     reads the binary's real import graph so that cannot happen again.
//   - Tombstones (tombstones.go) and the startup validation (validate.go).
package opscatalog

import (
	"errors"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	acoustidplugin "github.com/falkcorp/audiobook-organizer/internal/plugins/acoustid"
	dedupplugin "github.com/falkcorp/audiobook-organizer/internal/plugins/dedup"
	delugeplugin "github.com/falkcorp/audiobook-organizer/internal/plugins/deluge"
	itunesplugin "github.com/falkcorp/audiobook-organizer/internal/plugins/itunes"
	metafetchplugin "github.com/falkcorp/audiobook-organizer/internal/plugins/metafetch"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// Deps is the set of narrow capability interfaces the catalog builds each
// bundle's own Deps from. internal/server constructs it once from the
// concrete store; a missing method is a COMPILE error there, never a runtime
// type assertion (sdk-api.md §11.1). It never holds database.Store or an
// interface embedding it (oplint rule store-in-deps).
//
// It has no store interfaces yet because no native bundle exists; each port
// wave adds the narrow interfaces its bundles need.
type Deps struct {
	// Services is the set of services this process provides. A bundle whose
	// Requires names a service not in this set fails Build, so boot fails
	// instead of registering a def that panics on first run.
	Services map[ops.ServiceKey]bool
}

// All is the only list of native bundles linked into the binary. Each entry
// names one package's Ops function as pkg.Ops: either the entry itself, or a
// func literal that builds that package's own Deps from these and calls
// pkg.Ops(...). oplint resolves the entries by those type-checked
// references, so an entry that hides the call behind a helper is a finding.
var All = []func(Deps) ops.Bundle{}

// Bundles calls every entry of All with d. It makes no service checks: the
// startup ledger check uses it with a zero Deps to learn every native def ID,
// whether or not this process provides the services the bundle requires.
func Bundles(d Deps) []ops.Bundle {
	out := make([]ops.Bundle, 0, len(All))
	for _, f := range All {
		out = append(out, f(d))
	}
	return out
}

// Build returns the bundles of All built from d, refusing a bundle that names
// no plugin, a plugin named twice, and a bundle whose Requires names a
// service d does not provide. It is empty-safe: with no bundles it returns
// none and no error. Every problem is returned, joined.
func Build(d Deps) ([]ops.Bundle, error) {
	bundles := Bundles(d)
	var errs []error
	seen := map[string]bool{}
	for i, b := range bundles {
		if b.Plugin == "" {
			errs = append(errs, fmt.Errorf("opscatalog: bundle %d of All names no plugin", i))
			continue
		}
		if seen[b.Plugin] {
			errs = append(errs, fmt.Errorf("opscatalog: plugin %q has more than one bundle in All", b.Plugin))
		}
		seen[b.Plugin] = true
		for _, k := range b.Requires {
			if !d.Services[k] {
				errs = append(errs, fmt.Errorf("opscatalog: bundle %s requires service %q, which this process does not provide", b.Plugin, k))
			}
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return bundles, nil
}

// StaticPluginDefs returns the dependency-free OperationDefs() lists of the
// service-registry plugins. Those plugins register from serviceregistry
// PostInit and only when their dependencies are wired (a dedup engine, a
// Deluge client, an enabled iTunes service), so a correctly configured server
// with no Deluge has none of Deluge's defs in its live registry. The ledger
// check resolves IDs against these lists too, so that server still boots.
//
// Each list comes from a zero-value Plugin; building a def does not touch the
// plugin's dependencies (each OperationDefs doc comment says so).
func StaticPluginDefs() [][]registry.OperationDef {
	return [][]registry.OperationDef{
		(&acoustidplugin.Plugin{}).OperationDefs(),
		(&dedupplugin.Plugin{}).OperationDefs(),
		(&delugeplugin.Plugin{}).OperationDefs(),
		(&itunesplugin.Plugin{}).OperationDefs(),
		(&metafetchplugin.Plugin{}).OperationDefs(),
	}
}
