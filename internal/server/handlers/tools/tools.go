// file: internal/server/handlers/tools/tools.go
// version: 1.0.0
// guid: d6e7f8a9-b0c1-2345-defa-345678901234
// last-edited: 2026-06-15

// Package toolshandler provides HTTP handlers for managed external-tool lifecycle.
package toolshandler

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/tools"
)

// Downloader is the function signature for downloading a tool binary.
type Downloader func(ctx context.Context, def tools.ToolDef, destDir string, progress tools.ProgressFunc) (string, error)

// Handler provides HTTP handlers for /api/v1/tools.
type Handler struct {
	registry   *tools.ToolRegistry
	downloader Downloader
	cfg        *tools.ToolsConfig
}

// New creates a Handler. downloader may be nil (defaults to tools.Download).
func New(registry *tools.ToolRegistry, cfg *tools.ToolsConfig, downloader Downloader) *Handler {
	if downloader == nil {
		downloader = tools.Download
	}
	return &Handler{registry: registry, downloader: downloader, cfg: cfg}
}

// List returns all registered tool statuses.
//
//	GET /api/v1/tools
func (h *Handler) List(c *gin.Context) {
	c.JSON(http.StatusOK, h.registry.AllStatuses())
}

// Status returns the status for a single tool by name.
//
//	GET /api/v1/tools/:name/status
func (h *Handler) Status(c *gin.Context) {
	name := c.Param("name")
	c.JSON(http.StatusOK, h.registry.Status(name))
}

// Install downloads and installs a managed binary.
//
//	POST /api/v1/tools/:name/install
//
// Returns 409 if the tool is already installed at the current version.
func (h *Handler) Install(c *gin.Context) {
	name := c.Param("name")
	rel, ok := tools.LatestRelease(name)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown tool: " + name})
		return
	}

	// Return 409 if already installed.
	existing := h.registry.ManagedPath(name)
	if _, err := tools.StatFile(existing); err == nil {
		c.JSON(http.StatusConflict, gin.H{
			"error":   "already installed",
			"path":    existing,
			"version": rel.Version,
		})
		return
	}

	destDir := h.cfg.ManagedDir
	if destDir == "" {
		destDir = "/var/lib/audiobook-organizer/tools"
	}
	def := tools.ToolDef{Name: name, Release: rel}
	path, err := h.downloader(c.Request.Context(), def, destDir, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	h.registry.InvalidateCache(name)
	c.JSON(http.StatusOK, gin.H{
		"installed": true,
		"path":      path,
		"version":   rel.Version,
	})
}
