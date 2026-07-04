// file: internal/plugin/registry.go
// version: 1.2.2
// guid: 73ed97c1-8466-4a1b-bc07-cb81ce34c502
// last-edited: 2026-07-03

package plugin

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/gin-gonic/gin"
)

// registry is the global plugin registry.
var globalRegistry = &Registry{
	plugins: make(map[string]Plugin),
	enabled: make(map[string]bool),
}

// Registry manages plugin lifecycle.
type Registry struct {
	plugins   map[string]Plugin
	enabled   map[string]bool
	initOrder []string
	mu        sync.RWMutex
}

// Register adds a plugin to the global registry. Called from init().
func Register(p Plugin) {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	if _, exists := globalRegistry.plugins[p.ID()]; exists {
		slog.Warn("plugin already registered, skipping duplicate", "id", p.ID())
		return
	}
	globalRegistry.plugins[p.ID()] = p
	slog.Info("plugin registered", "name", p.Name(), "id", p.ID(), "version", p.Version())
}

// Global returns the global registry.
func Global() *Registry {
	return globalRegistry
}

// Get returns a plugin by ID.
func (r *Registry) Get(id string) (Plugin, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.plugins[id]
	return p, ok
}

// All returns all registered plugins.
func (r *Registry) All() []Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Plugin, 0, len(r.plugins))
	for _, p := range r.plugins {
		out = append(out, p)
	}
	return out
}

// Enable marks a plugin as enabled.
func (r *Registry) Enable(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.enabled[id] = true
}

// Disable marks a plugin as disabled.
func (r *Registry) Disable(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.enabled[id] = false
}

// IsEnabled returns whether a plugin is enabled.
func (r *Registry) IsEnabled(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.enabled[id]
}

// ByCapability returns all enabled plugins with a given capability.
func (r *Registry) ByCapability(cap Capability) []Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Plugin
	for id, p := range r.plugins {
		if !r.enabled[id] {
			continue
		}
		for _, c := range p.Capabilities() {
			if c == cap {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

// InitAll initializes all enabled plugins using shared deps. Plugins receive
// baseDeps.Config as-is; use InitAllScoped for per-plugin config and routers.
func (r *Registry) InitAll(ctx context.Context, baseDeps Deps) error {
	return r.InitAllScoped(ctx, baseDeps, nil, nil)
}

// InitAllScoped initializes all enabled plugins, threading per-plugin config
// from pluginConfigs and creating a scoped PluginRouter under parentGroup for
// each plugin. Either parameter may be nil (falls back to shared deps).
func (r *Registry) InitAllScoped(ctx context.Context, baseDeps Deps, parentGroup *gin.RouterGroup, pluginConfigs map[string]map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.initOrder = nil
	for id, p := range r.plugins {
		if !r.enabled[id] {
			slog.Info("plugin disabled, skipping init", "id", id)
			continue
		}

		deps := baseDeps
		if pluginConfigs != nil {
			if cfg, ok := pluginConfigs[id]; ok {
				deps.Config = cfg
			}
		}
		if parentGroup != nil {
			deps.Router = NewPluginRouter(parentGroup, id)
		}

		if err := p.Init(ctx, deps); err != nil {
			return fmt.Errorf("plugin %s init failed: %w", id, err)
		}
		r.initOrder = append(r.initOrder, id)
		slog.Info("plugin initialized", "id", id)
	}
	return nil
}

// ShutdownAll shuts down all initialized plugins in reverse order.
func (r *Registry) ShutdownAll(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i := len(r.initOrder) - 1; i >= 0; i-- {
		id := r.initOrder[i]
		if p, ok := r.plugins[id]; ok {
			if err := p.Shutdown(ctx); err != nil {
				slog.Warn("plugin shutdown error", "id", id, "err", err)
			} else {
				slog.Info("plugin shut down", "id", id)
			}
		}
	}
	r.initOrder = nil
}

// HealthCheckAll runs health checks on all initialized plugins.
func (r *Registry) HealthCheckAll() map[string]error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	results := make(map[string]error, len(r.initOrder))
	for _, id := range r.initOrder {
		if p, ok := r.plugins[id]; ok {
			results[id] = p.HealthCheck()
		}
	}
	return results
}

// ResetForTesting clears the global registry. Test-only.
func ResetForTesting() {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	globalRegistry.plugins = make(map[string]Plugin)
	globalRegistry.enabled = make(map[string]bool)
	globalRegistry.initOrder = nil
}
