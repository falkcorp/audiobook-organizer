// file: internal/tools/registry.go
// version: 1.0.0
// guid: b8c9d0e1-f2a3-4567-bcde-567890123456
// last-edited: 2026-06-15

package tools

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// ErrToolNotAvailable is returned when a tool cannot be resolved through any mode.
var ErrToolNotAvailable = errors.New("tools: binary not available")

// ToolDef registers a tool with the registry.
type ToolDef struct {
	Name    string
	Release ToolRelease
}

// ToolStatus is the current runtime state of a tool.
type ToolStatus struct {
	Name         string   `json:"name"`
	Mode         ToolMode `json:"mode"`
	Available    bool     `json:"available"`
	ResolvedPath string   `json:"resolved_path,omitempty"`
	Version      string   `json:"version,omitempty"`
}

// ToolRegistry resolves external binary paths using the configured mode.
// Safe for concurrent use after init.
type ToolRegistry struct {
	cfg      *ToolsConfig
	mu       sync.RWMutex
	tools    map[string]ToolDef
	resolved map[string]string
}

// NewToolRegistry creates a registry backed by cfg.
func NewToolRegistry(cfg *ToolsConfig) *ToolRegistry {
	return &ToolRegistry{
		cfg:      cfg,
		tools:    make(map[string]ToolDef),
		resolved: make(map[string]string),
	}
}

// Register adds a tool definition.
func (r *ToolRegistry) Register(def ToolDef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[def.Name] = def
}

// Resolve returns the usable binary path for name.
func (r *ToolRegistry) Resolve(name string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if p, ok := r.resolved[name]; ok {
		return p, nil
	}

	def, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("%w: %s (not registered)", ErrToolNotAvailable, name)
	}

	toolCfg := r.toolConfig(name)

	switch toolCfg.Mode {
	case ToolModeDisabled:
		return "", fmt.Errorf("%w: %s (disabled)", ErrToolNotAvailable, name)

	case ToolModeManaged:
		p := r.managedPath(def)
		if _, err := os.Stat(p); err == nil {
			r.resolved[name] = p
			return p, nil
		}
		return "", fmt.Errorf("%w: %s (managed binary not found at %s — install via /api/v1/tools/%s/install)", ErrToolNotAvailable, name, p, name)

	case ToolModeSystem:
		p, err := exec.LookPath(name)
		if err != nil {
			return "", fmt.Errorf("%w: %s (not on PATH)", ErrToolNotAvailable, name)
		}
		r.resolved[name] = p
		return p, nil

	case ToolModeCustom:
		p := toolCfg.CustomPath
		if p == "" {
			return "", fmt.Errorf("%w: %s (custom mode but no path configured)", ErrToolNotAvailable, name)
		}
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("%w: %s (custom path %s not found)", ErrToolNotAvailable, name, p)
		}
		r.resolved[name] = p
		return p, nil
	}

	return "", fmt.Errorf("%w: %s (unknown mode %q)", ErrToolNotAvailable, name, toolCfg.Mode)
}

// Available returns true if Resolve would succeed.
func (r *ToolRegistry) Available(name string) bool {
	_, err := r.Resolve(name)
	return err == nil
}

// Status returns the current ToolStatus for name.
func (r *ToolRegistry) Status(name string) ToolStatus {
	r.mu.RLock()
	def, registered := r.tools[name]
	r.mu.RUnlock()
	s := ToolStatus{Name: name, Mode: r.toolConfig(name).Mode}
	if !registered {
		return s
	}
	p, err := r.Resolve(name)
	if err == nil {
		s.Available = true
		s.ResolvedPath = p
		s.Version = def.Release.Version
	}
	return s
}

// AllStatuses returns ToolStatus for every registered tool.
func (r *ToolRegistry) AllStatuses() []ToolStatus {
	r.mu.RLock()
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		names = append(names, n)
	}
	r.mu.RUnlock()
	out := make([]ToolStatus, len(names))
	for i, n := range names {
		out[i] = r.Status(n)
	}
	return out
}

// ManagedPath returns the expected on-disk path for the managed binary.
func (r *ToolRegistry) ManagedPath(name string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def := r.tools[name]
	return r.managedPath(def)
}

func (r *ToolRegistry) managedPath(def ToolDef) string {
	return filepath.Join(r.cfg.ManagedDir, def.Name, def.Release.Version, def.Name)
}

func (r *ToolRegistry) toolConfig(name string) ToolConfig {
	switch name {
	case "ollama":
		return r.cfg.Ollama
	case "fpcalc":
		return r.cfg.Fpcalc
	default:
		return ToolConfig{Mode: ToolModeSystem}
	}
}

// InvalidateCache clears the resolved-path cache for name (e.g. after install).
func (r *ToolRegistry) InvalidateCache(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.resolved, name)
}
