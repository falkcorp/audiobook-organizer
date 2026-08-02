//go:build embed_frontend

// version: 1.1.0
// file: internal/server/static_embed.go
// last-edited: 2026-08-01
// guid: 1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-05-01

package server

import (
	"embed"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path/filepath"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
)

// webFS holds the embedded filesystem passed from main package
var webFS embed.FS

// SetEmbeddedFS sets the embedded filesystem for serving static files
func SetEmbeddedFS(fs embed.FS) {
	webFS = fs
}

// setupStaticFiles serves the embedded React frontend
func (s *Server) setupStaticFiles() {
	// Get the web/dist subdirectory from the embedded filesystem
	webDist, err := fs.Sub(webFS, "web/dist")
	if err != nil {
		// If Sub fails, log and continue - we'll fall back to placeholder
		webDist = nil
	}

	// NoRoute handler to serve static files or SPA index.html
	s.router.NoRoute(func(c *gin.Context) {
		// Server-side paths must 404 rather than render the SPA shell. Serving
		// the React index for an unimplemented protocol endpoint tells a client
		// the feature exists — see isNonSPAPath for the AudioBooth OIDC case.
		if isNonSPAPath(c.Request.URL.Path) {
			httputil.RespondWithNotFound(c, "endpoint", "")
			return
		}

		// If webDist is nil, show placeholder
		if webDist == nil {
			s.setupPlaceholder()
			c.Request.URL.Path = "/"
			s.router.HandleContext(c)
			return
		}

		// Try to serve the file from the embedded filesystem
		path := c.Request.URL.Path
		if path == "/" {
			path = "/index.html"
		}

		// Remove leading slash for fs.Open
		filePath := path[1:]

		file, err := webDist.Open(filePath)
		if err == nil {
			defer file.Close()

			// Get file info for size
			stat, err := file.Stat()
			if err == nil && !stat.IsDir() {
				// Detect MIME type from extension
				contentType := mime.TypeByExtension(filepath.Ext(filePath))
				if contentType == "" {
					contentType = "application/octet-stream"
				}

				// Read file content
				content, err := io.ReadAll(file)
				if err == nil {
					c.Data(http.StatusOK, contentType, content)
					return
				}
			}
		}

		// File not found or error, serve index.html for SPA routing
		indexData, err := fs.ReadFile(webDist, "index.html")
		if err != nil {
			c.String(http.StatusInternalServerError, "Failed to load frontend")
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexData)
	})
}

// setupPlaceholder serves the API documentation placeholder page (fallback)
func (s *Server) setupPlaceholder() {
	s.router.GET("/", func(c *gin.Context) {
		html := `
<!DOCTYPE html>
<html>
<head>
    <title>Audiobook Organizer</title>
    <style>
        body { font-family: Arial, sans-serif; margin: 40px; background-color: #f5f5f5; }
        .container { max-width: 800px; margin: 0 auto; background: white; padding: 40px; border-radius: 8px; box-shadow: 0 2px 10px rgba(0,0,0,0.1); }
        h1 { color: #333; }
        .error-box { background: #f8d7da; border-left: 4px solid #dc3545; padding: 15px; margin: 20px 0; }
    </style>
</head>
<body>
    <div class="container">
        <h1>🎧 Audiobook Organizer</h1>
        <div class="error-box">
            <strong>⚠️ Error Loading Frontend</strong><br>
            The embedded frontend files could not be loaded. Please check that the web/dist directory was properly embedded during build.
        </div>
        <p>The API server is still available at <code>/api</code> endpoints.</p>
    </div>
</body>
</html>
		`
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
	})
}
