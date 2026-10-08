// file: internal/server/itl_paths.go
// version: 1.0.0
// guid: 0b8e6f52-4c1d-4e7a-9a63-5d2f8c1e7b40
// last-edited: 2026-10-07
//
// Read-side helpers for the ITL endpoints that remain after iTunes write-back
// was removed (owner decision 2026-10-07: iTunes is import-only). The ITL is
// parsed, never written.

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/falkcorp/audiobook-organizer/internal/security/pathvalidation"
	"github.com/gin-gonic/gin"
)

// resolveITLPath reads and validates the configured ITL path, writing an
// error response and returning ok=false when it is unset/invalid.
func resolveITLPath(c *gin.Context) (string, bool) {
	rawPath := config.AppConfig.ITunes.LibraryITLPath
	if rawPath == "" {
		httputil.RespondWithBadRequest(c, "the iTunes library ITL path (itunes.library_write_path) is not configured")
		return "", false
	}
	itlPath, err := pathvalidation.CleanAbsolutePath(rawPath)
	if err != nil {
		httputil.RespondWithInternalError(c, "invalid iTunes library ITL path in config")
		return "", false
	}
	return itlPath, true
}

// itlPathMappings converts the configured iTunes path mappings into the itunes
// package type, so an ITL location can be translated to a local path.
func itlPathMappings() []itunes.PathMapping {
	cfg := config.AppConfig.ITunes.PathMappings
	out := make([]itunes.PathMapping, len(cfg))
	for i, m := range cfg {
		out[i] = itunes.PathMapping{From: m.From, To: m.To}
	}
	return out
}
