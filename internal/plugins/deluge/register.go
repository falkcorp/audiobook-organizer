// file: internal/plugins/deluge/register.go
// version: 1.1.2
// last-edited: 2026-06-23

// Service registry registration for the deluge UOS plugin (W5/W7).
//
// Build returns nil when Deluge is not configured (no global client OR
// no protected-path cache configured). PostInit self-registers the
// plugin's op-defs.

package deluge

import (
	"context"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	delugeclient "github.com/falkcorp/audiobook-organizer/internal/deluge"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "delugeplugin",
		Needs:  []string{serviceregistry.KeyStore, serviceregistry.KeyConfig},
		Groups: []string{"plugins"},
		Build: func(c *serviceregistry.Container) (any, error) {
			cfg := serviceregistry.Get[*config.Config](c, serviceregistry.KeyConfig)
			// Mirror the original inline guard: skip when library root
			// isn't configured (test paths). Without this, MockStore-based
			// tests blow up because PostInit's Plugin.Register triggers
			// UpsertOpDefinitionV2 calls the mock doesn't expect.
			if cfg.RootDir == "" {
				return (*Plugin)(nil), nil
			}
			client := delugeclient.GetClient()
			if client == nil {
				return (*Plugin)(nil), nil
			}
			cache := delugeclient.NewProtectedPathCache(client, cfg.ProtectedPaths)
			store := serviceregistry.Get[database.Store](c, serviceregistry.KeyStore)
			return New(client, cache, store), nil
		},
	})
}

func (p *Plugin) PostInit(ctx context.Context, c *serviceregistry.Container) error {
	if p == nil {
		return nil
	}
	wrapper, ok := serviceregistry.TryGet[*opsregistry.RegistryWrapper](c, "opregistry")
	if !ok || wrapper == nil {
		slog.Warn("PostInit opregistry not available, skipping op-def registration")
		return nil
	}
	return p.Register(wrapper.Registry)
}
