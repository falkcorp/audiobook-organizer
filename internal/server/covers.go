// file: internal/server/covers.go
// version: 1.4.1
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-05-15
//
// HTTP handlers for cover proxy and local cover serving.
// Business logic extracted to internal/covers.

package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/covers"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/security/safepath"
	"github.com/gin-gonic/gin"
)

// handleCoverProxy proxies and caches cover images from external URLs.
// GET /api/v1/covers/proxy?url=https://covers.openlibrary.org/...
func (s *Server) handleCoverProxy(c *gin.Context) {
	coverURL := c.Query("url")
	if coverURL == "" {
		httputil.RespondWithBadRequest(c, "url parameter required")
		return
	}

	// Only allow known cover sources
	if !covers.IsAllowedCoverSource(coverURL) {
		httputil.RespondWithBadRequest(c, "URL not from an allowed cover source")
		return
	}

	cacheDirSP, err := safepath.Join(config.AppConfig.RootDir, ".covers")
	if err != nil {
		httputil.RespondWithInternalError(c, "invalid cache directory")
		return
	}
	cacheDir := cacheDirSP.String()
	cachePath, errMsg := covers.FetchAndCacheCover(coverURL, cacheDir)

	if errMsg != "" {
		statusCode := http.StatusInternalServerError
		code := "INTERNAL_ERROR"
		if strings.Contains(errMsg, "source returned") {
			statusCode = http.StatusBadGateway
			code = "UPSTREAM_ERROR"
		} else if strings.Contains(errMsg, "fetch") {
			statusCode = http.StatusBadGateway
			code = "BAD_GATEWAY"
		}
		httputil.RespondWithError(c, statusCode, errMsg, code)
		return
	}

	c.File(cachePath)
}

// handleLocalCover serves locally extracted cover art files.
// GET /api/v1/covers/local/:filename
func (s *Server) handleLocalCover(c *gin.Context) {
	filename := c.Param("filename")

	// Prevent path traversal
	if strings.Contains(filename, "/") || strings.Contains(filename, "\\") || strings.Contains(filename, "..") {
		httputil.RespondWithBadRequest(c, "invalid filename")
		return
	}

	// Validate cover path with safepath
	coverSP, err := safepath.Join(config.AppConfig.RootDir, "covers", filename)
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid filename")
		return
	}
	coverPath := coverSP.String()

	coverPath, err = covers.FindCoverFile(filepath.Base(coverPath), config.AppConfig.RootDir)
	if err != nil {
		if os.IsNotExist(err) {
			httputil.RespondWithNotFound(c, "cover", "")
			return
		}
		httputil.RespondWithInternalError(c, "failed to find cover")
		return
	}

	c.File(coverPath)
}
