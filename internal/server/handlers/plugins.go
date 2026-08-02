// file: internal/server/handlers/plugins.go
// version: 1.0.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef0123456789
// last-edited: 2026-06-02

package handlers

import (
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	"github.com/gin-gonic/gin"
)

// PluginRegistrar is the narrow interface PluginsHandler requires for the plugin registry.
type PluginRegistrar interface {
	All() []plugin.Plugin
	Get(id string) (plugin.Plugin, bool)
	IsEnabled(id string) bool
	Enable(id string)
	Disable(id string)
}

// PluginsHandler handles all plugin-related HTTP endpoints.
type PluginsHandler struct {
	registry   PluginRegistrar
	pluginCfgs map[string]config.PluginConfig
}

// NewPluginsHandler creates a new PluginsHandler.
func NewPluginsHandler(registry PluginRegistrar, pluginCfgs map[string]config.PluginConfig) *PluginsHandler {
	return &PluginsHandler{registry: registry, pluginCfgs: pluginCfgs}
}

// pluginInfo is the JSON shape returned for each plugin.
type pluginInfo struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities"`
	Enabled      bool     `json:"enabled"`
	Initialized  bool     `json:"initialized"`
	Health       string   `json:"health,omitempty"`
}

func pluginToInfo(p plugin.Plugin, reg PluginRegistrar) pluginInfo {
	caps := make([]string, len(p.Capabilities()))
	for i, c := range p.Capabilities() {
		caps[i] = string(c)
	}
	info := pluginInfo{
		ID:           p.ID(),
		Name:         p.Name(),
		Version:      p.Version(),
		Capabilities: caps,
		Enabled:      reg.IsEnabled(p.ID()),
	}
	if err := p.HealthCheck(); err != nil {
		info.Health = err.Error()
	} else {
		info.Health = "ok"
		info.Initialized = true
	}
	return info
}

// ListPlugins handles GET /api/v1/plugins
func (h *PluginsHandler) ListPlugins(c *gin.Context) {
	if h.registry == nil {
		httputil.RespondWithOK(c, gin.H{"plugins": []pluginInfo{}})
		return
	}
	all := h.registry.All()
	result := make([]pluginInfo, 0, len(all))
	for _, p := range all {
		result = append(result, pluginToInfo(p, h.registry))
	}
	httputil.RespondWithOK(c, gin.H{"plugins": result})
}

// GetPlugin handles GET /api/v1/plugins/:id
func (h *PluginsHandler) GetPlugin(c *gin.Context) {
	id := c.Param("id")
	if h.registry == nil {
		httputil.RespondWithNotFound(c, "plugin system", "not initialized")
		return
	}
	p, ok := h.registry.Get(id)
	if !ok {
		httputil.RespondWithNotFound(c, "plugin", id)
		return
	}
	httputil.RespondWithOK(c, pluginToInfo(p, h.registry))
}

// EnablePlugin handles POST /api/v1/plugins/:id/enable
func (h *PluginsHandler) EnablePlugin(c *gin.Context) {
	id := c.Param("id")
	if h.registry == nil {
		httputil.RespondWithInternalError(c, "plugin system not initialized")
		return
	}
	if _, ok := h.registry.Get(id); !ok {
		httputil.RespondWithNotFound(c, "plugin", id)
		return
	}
	h.registry.Enable(id)

	// Persist to config.
	if h.pluginCfgs == nil {
		h.pluginCfgs = make(map[string]config.PluginConfig)
	}
	cfg := h.pluginCfgs[id]
	cfg.Enabled = true
	h.pluginCfgs[id] = cfg

	httputil.RespondWithOK(c, gin.H{"id": id, "enabled": true})
}

// DisablePlugin handles POST /api/v1/plugins/:id/disable
func (h *PluginsHandler) DisablePlugin(c *gin.Context) {
	id := c.Param("id")
	if h.registry == nil {
		httputil.RespondWithInternalError(c, "plugin system not initialized")
		return
	}
	if _, ok := h.registry.Get(id); !ok {
		httputil.RespondWithNotFound(c, "plugin", id)
		return
	}
	h.registry.Disable(id)

	if h.pluginCfgs == nil {
		h.pluginCfgs = make(map[string]config.PluginConfig)
	}
	cfg := h.pluginCfgs[id]
	cfg.Enabled = false
	h.pluginCfgs[id] = cfg

	httputil.RespondWithOK(c, gin.H{"id": id, "enabled": false})
}

// PluginHealth handles GET /api/v1/plugins/:id/health
func (h *PluginsHandler) PluginHealth(c *gin.Context) {
	id := c.Param("id")
	if h.registry == nil {
		httputil.RespondWithSuccess(c, 503, gin.H{"id": id, "health": "plugin system not initialized"})
		return
	}
	p, ok := h.registry.Get(id)
	if !ok {
		httputil.RespondWithNotFound(c, "plugin", id)
		return
	}
	if err := p.HealthCheck(); err != nil {
		httputil.RespondWithOK(c, gin.H{"id": id, "health": err.Error(), "ok": false})
		return
	}
	httputil.RespondWithOK(c, gin.H{"id": id, "health": "ok", "ok": true})
}

// UpdatePluginSettings handles PUT /api/v1/plugins/:id/settings
func (h *PluginsHandler) UpdatePluginSettings(c *gin.Context) {
	id := c.Param("id")
	if h.registry == nil {
		httputil.RespondWithInternalError(c, "plugin system not initialized")
		return
	}
	if _, ok := h.registry.Get(id); !ok {
		httputil.RespondWithNotFound(c, "plugin", id)
		return
	}

	var settings map[string]string
	if err := c.ShouldBindJSON(&settings); err != nil {
		httputil.RespondWithBadRequest(c, "invalid settings: "+err.Error())
		return
	}

	if h.pluginCfgs == nil {
		h.pluginCfgs = make(map[string]config.PluginConfig)
	}
	cfg := h.pluginCfgs[id]
	cfg.Settings = settings
	h.pluginCfgs[id] = cfg

	httputil.RespondWithOK(c, gin.H{"id": id, "settings": settings})
}
