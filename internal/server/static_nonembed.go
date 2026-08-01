//go:build !embed_frontend
// version: 1.1.0
// file: internal/server/static_nonembed.go
// last-edited: 2026-08-01
// guid: 2b3c4d5e-6f7a-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-05-01

package server

import (
	"embed"
	"net/http"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
)

// SetEmbeddedFS is a no-op when not embedding frontend
func SetEmbeddedFS(fs embed.FS) {
	// No-op: not using embedded frontend
}

// setupStaticFiles serves a placeholder HTML page (no embedded frontend)
func (s *Server) setupStaticFiles() {
	s.setupPlaceholder()
}

// setupPlaceholder serves the API documentation placeholder page
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
        .api-list { background: #f8f9fa; padding: 20px; border-radius: 4px; margin: 20px 0; }
        .api-endpoint { font-family: 'Courier New', monospace; background: #e9ecef; padding: 4px 8px; margin: 2px 0; border-radius: 3px; display: block; }
        .method { color: #007bff; font-weight: bold; }
        .info-box { background: #fff3cd; border-left: 4px solid #ffc107; padding: 15px; margin: 20px 0; }
    </style>
</head>
<body>
    <div class="container">
        <h1>🎧 Audiobook Organizer API Server</h1>

        <div class="info-box">
            <strong>ℹ️ Frontend Not Embedded</strong><br>
            This build was compiled without the embedded web interface.
            To enable the frontend, rebuild with: <code>go build -tags embed_frontend</code>
        </div>

        <h2>Available API Endpoints:</h2>
        <div class="api-list">
            <h3>Health & System:</h3>
            <code class="api-endpoint"><span class="method">GET</span> /api/health - Health check</code>
            <code class="api-endpoint"><span class="method">GET</span> /api/v1/system/status - System status</code>
            <code class="api-endpoint"><span class="method">GET</span> /api/v1/config - Get configuration</code>

            <h3>Audiobooks:</h3>
            <code class="api-endpoint"><span class="method">GET</span> /api/v1/audiobooks - List audiobooks</code>
            <code class="api-endpoint"><span class="method">GET</span> /api/v1/audiobooks/:id - Get audiobook details</code>
            <code class="api-endpoint"><span class="method">PUT</span> /api/v1/audiobooks/:id - Update audiobook</code>
            <code class="api-endpoint"><span class="method">DELETE</span> /api/v1/audiobooks/:id - Delete audiobook</code>

            <h3>Authors & Series:</h3>
            <code class="api-endpoint"><span class="method">GET</span> /api/v1/authors - List authors</code>
            <code class="api-endpoint"><span class="method">GET</span> /api/v1/series - List series</code>

            <h3>Operations:</h3>
            <code class="api-endpoint"><span class="method">POST</span> /api/v1/operations/scan - Start library scan</code>
            <code class="api-endpoint"><span class="method">GET</span> /api/v1/operations/:id/status - Get operation status</code>

            <h3>Backup:</h3>
            <code class="api-endpoint"><span class="method">GET</span> /api/v1/backup/list - List backups</code>
            <code class="api-endpoint"><span class="method">POST</span> /api/v1/backup/create - Create backup</code>
            <code class="api-endpoint"><span class="method">POST</span> /api/v1/backup/restore - Restore backup</code>
        </div>

        <h3>Quick Links:</h3>
        <p>
            <a href="/api/health" target="_blank">Health Check</a> |
            <a href="/api/v1/config" target="_blank">Configuration</a> |
            <a href="/api/v1/audiobooks" target="_blank">Audiobooks</a> |
            <a href="/api/v1/system/status" target="_blank">System Status</a>
        </p>

        <p style="color: #666; margin-top: 40px; border-top: 1px solid #ddd; padding-top: 20px;">
            <strong>Documentation:</strong> See <a href="https://github.com/falkcorp/audiobook-organizer">GitHub Repository</a> for full API documentation.
        </p>
    </div>
</body>
</html>
		`
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
	})

	// Return 404 for unknown routes
	s.router.NoRoute(func(c *gin.Context) {
		// Server-side paths must 404 rather than render the SPA shell. Serving
		// the React index for an unimplemented protocol endpoint tells a client
		// the feature exists — see isNonSPAPath for the AudioBooth OIDC case.
		if isNonSPAPath(c.Request.URL.Path) {
			httputil.RespondWithNotFound(c, "endpoint", "")
			return
		}
		// For non-API routes, redirect to home
		c.Redirect(http.StatusFound, "/")
	})
}
