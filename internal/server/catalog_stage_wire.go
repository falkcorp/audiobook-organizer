// file: internal/server/catalog_stage_wire.go
// version: 1.0.0
// guid: b5e18c37-0d92-4a64-a8f3-7c2e9d1b4056
// last-edited: 2026-10-12

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/authority"
	"github.com/falkcorp/audiobook-organizer/internal/catalog"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// wireCatalogStage installs the local author-catalog matcher (B1/B2) in the
// metadata search. The matcher is only the lookup: the search runs it when
// metadata_fetch.catalog_stage is on (default off), so installing it changes
// nothing until the owner turns the switch. A store that is not Pebble-backed
// has no catalog, and nothing is installed.
//
// store is the server's store, taken as the only thing the stage asks of it
// directly -- the raw reads the authority lists answer through -- and
// unwrapped through its decorator chain to find the Pebble-backed catalog.
func wireCatalogStage(mfs *metafetch.Service, store authority.Reader) {
	if mfs == nil || store == nil {
		return
	}
	cat := database.NewCatalogStoreFromStore(store)
	if cat == nil {
		return
	}
	mfs.SetCatalogMatcher(catalog.NewMatcher(cat, func() string { return config.Snapshot().Catalog.Marketplace }, store))
}
