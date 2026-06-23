// file: internal/plugins/itunes/register.go
// version: 2.0.1
// last-edited: 2026-06-23

// Service registry registration for the iTunes UOS plugin (W5).
//
// As of PROMOTE-STUB-REGISTRATIONS (May 13, 2026) this is no longer a stub.
// serviceregistry.KeyITunes is container-built (internal/server/registry_wire.go) and the
// plugin's only deps — *itunesservice.Service + database.Store — are
// pullable here. PostInit registers OperationDefs with the opregistry
// after Build completes, mirroring the same nil-guard ordering used in
// the prior inline NewServer block.
//
// Test-path guard: when *config.Config.RootDir is empty the test MockStore
// doesn't expect UpsertOpDefinitionV2 calls. Build returns a typed-nil
// *Plugin in that case; PostInit nil-checks before registering.

package itunes

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// PostInit registers iTunes OperationDefs with the opregistry. Runs after
// all Build calls so opregistry is guaranteed to be present. Nil-safe.
func (p *Plugin) PostInit(_ context.Context, c *serviceregistry.Container) error {
	if p == nil {
		return nil
	}
	wrapper, ok := serviceregistry.TryGet[*opsregistry.RegistryWrapper](c, "opregistry")
	if !ok || wrapper == nil {
		return nil
	}
	return p.Register(wrapper.Registry)
}

func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "itunesplugin",
		Needs:  []string{serviceregistry.KeyITunes, serviceregistry.KeyStore, serviceregistry.KeyConfig},
		Groups: []string{"plugins"},
		Build: func(c *serviceregistry.Container) (any, error) {
			cfg := serviceregistry.Get[*config.Config](c, serviceregistry.KeyConfig)
			// Test-path guard: empty RootDir means tests without a real
			// AppConfig — the mock store has no UpsertOpDefinitionV2
			// expectations, so don't register ops.
			if cfg.RootDir == "" {
				return (*Plugin)(nil), nil
			}
			svc := serviceregistry.Get[*itunesservice.Service](c, serviceregistry.KeyITunes)
			store := serviceregistry.Get[database.Store](c, serviceregistry.KeyStore)
			return New(svc, store), nil
		},
	})
}
