// file: internal/plugins/metafetch/register.go
// version: 1.0.0
// guid: 7a2f9c1d-4e63-4b80-9c15-6d0e1f2a3b40
// last-edited: 2026-07-11

// Service registry registration for the metafetch UOS plugin (INIT-3-T1).
//
// Mirrors internal/plugins/dedup/register.go: an init() that self-registers a
// ServiceDef whose Build returns the constructed *Plugin (or a typed-nil when a
// required dependency is unavailable), plus a PostInit that registers the
// plugin's op-defs against the container's opregistry after all services exist.

package metafetch

import (
	"context"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "metafetchplugin",
		Needs:  []string{serviceregistry.KeyStore, serviceregistry.KeyMetaFetch},
		Groups: []string{"plugins"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[database.Store](c, serviceregistry.KeyStore)
			mfs, _ := serviceregistry.TryGet[*metafetch.Service](c, serviceregistry.KeyMetaFetch)
			if store == nil || mfs == nil {
				return (*Plugin)(nil), nil
			}
			return New(store, mfs), nil
		},
	})
}

// PostInit self-registers this plugin's op-defs against the container's
// opregistry. Called by Container.PostInit() after all services are built.
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
