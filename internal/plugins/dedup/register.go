// file: internal/plugins/dedup/register.go
// version: 1.1.2
// last-edited: 2026-06-23

// Service registry registration for the dedup UOS plugin (W5/W7).
//
// Build returns the constructed *Plugin when all required services are
// available, or nil when any dep is unavailable (no API key → no dedup
// engine → no plugin).
//
// PostInit (W7) self-registers the plugin's op-defs against the
// container's opregistry, replacing the inline `Register(server.opRegistry)`
// call that used to live in NewServer.

package dedup

import (
	"context"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "dedupplugin",
		Needs:  []string{serviceregistry.KeyStore, serviceregistry.KeyDedup, serviceregistry.KeyEmbeddingStore},
		Groups: []string{"plugins"},
		Build: func(c *serviceregistry.Container) (any, error) {
			engine, _ := serviceregistry.TryGet[*dedupengine.Engine](c, serviceregistry.KeyDedup)
			embStore, _ := serviceregistry.TryGet[*database.EmbeddingStore](c, serviceregistry.KeyEmbeddingStore)
			if engine == nil || embStore == nil {
				return (*Plugin)(nil), nil
			}
			store := serviceregistry.Get[database.Store](c, serviceregistry.KeyStore)
			return New(engine, store, embStore), nil
		},
	})
}

// PostInit self-registers this plugin's op-defs against the container's
// opregistry. Called by Container.PostInit() after all services are built.
//
// Safe to call when the plugin is nil — early-returns without error.
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
