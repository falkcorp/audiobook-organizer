// file: internal/server/plugins_init.go
// version: 1.2.1
// guid: a2b3c4d5-e6f7-8a9b-0c1d-2e3f4a5b6c7d
// last-edited: 2026-11-08

package server

import (
	"context"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"

	// The blank import triggers the webhook plugin's init() to register with
	// plugin.Global(). The deluge plugin has no such init(): its service def is
	// listed in serviceDefs (registry_wire.go), which imports the package.
	_ "github.com/falkcorp/audiobook-organizer/internal/plugins/webhook"
)

// initPlugins enables plugins declared as enabled in config, then initializes
// them with per-plugin settings and scoped HTTP routes under /api/v1/plugins/.
// Must be called after s.eventBus, s.pluginRegistry, and s.router
// are all set (i.e. at the end of NewServer, after setupRoutes).
func (s *Server) initPlugins(ctx context.Context) {
	// Mark enabled plugins from config before calling InitAllScoped.
	pluginConfigs := make(map[string]map[string]string)
	for id, cfg := range config.AppConfig.Plugins {
		if cfg.Enabled {
			s.pluginRegistry.Enable(id)
		}
		if len(cfg.Settings) > 0 {
			pluginConfigs[id] = cfg.Settings
		}
	}

	baseDeps := plugin.Deps{
		Events: s.eventBus,
		Logger: logger.New("plugin"),
	}

	pluginGroup := s.router.Group("/api/v1/plugins")

	if err := s.pluginRegistry.InitAllScoped(ctx, baseDeps, pluginGroup, pluginConfigs); err != nil {
		slog.Warn("plugin initialization error", "err", err)
	}
}
