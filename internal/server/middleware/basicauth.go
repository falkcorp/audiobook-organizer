// file: internal/server/middleware/basicauth.go
// version: 1.0.0
// guid: a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d

package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/gin-gonic/gin"
)

// BasicAuth returns a Gin middleware that enforces HTTP Basic Authentication
// when config.AppConfig.BasicAuthEnabled is true. Health endpoints and static
// assets are exempt.
func BasicAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !config.AppConfig.BasicAuthEnabled {
			c.Next()
			return
		}

		path := c.Request.URL.Path

		// Exempt health endpoints
		if path == "/api/health" || path == "/api/v1/health" {
			c.Next()
			return
		}

		// Exempt static assets (Vite-built frontend files)
		if strings.HasPrefix(path, "/assets/") ||
			path == "/favicon.ico" ||
			strings.HasSuffix(path, ".js") ||
			strings.HasSuffix(path, ".css") ||
			strings.HasSuffix(path, ".png") ||
			strings.HasSuffix(path, ".svg") ||
			strings.HasSuffix(path, ".woff2") {
			c.Next()
			return
		}

		user, pass, ok := c.Request.BasicAuth()
		if !ok {
			c.Header("WWW-Authenticate", `Basic realm="Audiobook Organizer"`)
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}

		expectedUser := config.AppConfig.BasicAuthUsername
		expectedPass := config.AppConfig.BasicAuthPassword

		userMatch := subtle.ConstantTimeCompare([]byte(user), []byte(expectedUser)) == 1
		passMatch := subtle.ConstantTimeCompare([]byte(pass), []byte(expectedPass)) == 1

		if !userMatch || !passMatch {
			c.Header("WWW-Authenticate", `Basic realm="Audiobook Organizer"`)
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}

		c.Next()
	}
}
