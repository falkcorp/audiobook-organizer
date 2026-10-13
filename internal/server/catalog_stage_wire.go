// file: internal/server/catalog_stage_wire.go
// version: 1.1.0
// guid: b5e18c37-0d92-4a64-a8f3-7c2e9d1b4056
// last-edited: 2026-10-12

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/authority"
	"github.com/falkcorp/audiobook-organizer/internal/catalog"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

var catalogStageLog = logger.New("catalog-stage")

// wireCatalogStage installs the local author-catalog matcher (B1/B2) in the
// metadata search. The matcher is only the lookup: the search runs it when
// metadata_fetch.catalog_stage is on (default off), so installing it changes
// nothing until the owner turns the switch.
//
// store is the server's store, taken as the only thing the stage asks of it
// directly -- the raw reads the authority lists answer through. The catalog is
// resolved from it at each match, unwrapped through its decorator chain to the
// Pebble store, as the catalog routes and the harvest op resolve it, so the
// matcher never holds a store that was reopened. A store that is not
// Pebble-backed has no catalog and the stage finds nothing; the startup line
// says which it is, so a switch turned on over a store with no catalog is not
// a silent no-op.
func wireCatalogStage(mfs *metafetch.Service, store authority.Reader) {
	if mfs == nil || store == nil {
		return
	}
	resolve := func() *database.CatalogStore { return database.NewCatalogStoreFromStore(store) }
	mfs.SetCatalogMatcher(catalog.NewMatcher(resolve, func() string { return config.Snapshot().Catalog.Marketplace }, store))
	catalogStageLog.Info("catalog blocking stage: matcher installed (catalog store resolves: %v; metadata_fetch.catalog_stage=%v)",
		resolve() != nil, config.Snapshot().MetadataFetch.CatalogStage)
}
