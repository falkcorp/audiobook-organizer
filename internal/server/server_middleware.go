// file: internal/server/server_middleware.go
// version: 1.5.0
// guid: 6a093405-441a-4c14-a9c5-46326ea767c1
// last-edited: 2026-06-22

package server

import (
	"encoding/json"
	"log/slog"

	"net/http"
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
)

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := strings.TrimSpace(c.GetHeader("Origin"))
		allowedOrigin := ""
		isDevMode := gin.Mode() == gin.DebugMode

		if origin != "" {
			// Dev-mode CORS: allow Vite dev server only.
			if isDevMode && (origin == "http://localhost:5173" || origin == "https://localhost:5173") {
				allowedOrigin = origin
			}

			// Always allow same-origin requests.
			host := strings.TrimSpace(c.Request.Host)
			if host != "" {
				if origin == "http://"+host || origin == "https://"+host {
					allowedOrigin = origin
				}
			}
		}

		if allowedOrigin != "" {
			c.Header("Access-Control-Allow-Origin", allowedOrigin)
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, Authorization, Cache-Control, X-Requested-With")
			c.Header("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE, PATCH")
		}

		if c.Request.Method == http.MethodOptions {
			if origin != "" && allowedOrigin == "" {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// securityHeadersMiddleware sets browser security headers on every response.
// TLS-only headers (HSTS) are added only when the connection is HTTPS.
// CSP is intentionally omitted here — the React SPA requires inline styles and
// scripts that vary across builds; add it as a separate hardening step once the
// nonce/hash strategy is settled.
func securityHeadersMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "SAMEORIGIN")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Header("X-XSS-Protection", "0") // tell browsers to use built-in XSS filters, not legacy mode

		if c.Request.TLS != nil || strings.EqualFold(strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")), "https") {
			c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}

		c.Next()
	}
}

// isProtectedPath is now a method on *Server so it uses the server's
// resolved store rather than the package-level GetGlobalStore
// (SERVER-GLOBAL-STORE-AUDIT phase 3a). Nil-safe — if s.Store() is
// nil, the import-path check is skipped (matches prior behaviour
// when GetGlobalStore returned nil).
func (s *Server) isProtectedPath(filePath string) bool {
	absPath, _ := filepath.Abs(filePath)

	// Check import paths
	if store := s.Store(); store != nil {
		importPaths, err := store.GetAllImportPaths()
		if err == nil {
			for _, ip := range importPaths {
				ipAbs, _ := filepath.Abs(ip.Path)
				if strings.HasPrefix(absPath, ipAbs+"/") || absPath == ipAbs {
					return true
				}
			}
		}
	}

	// Check iTunes library paths
	if config.AppConfig.ITunes.LibraryReadPath != "" {
		itunesDir := filepath.Dir(config.AppConfig.ITunes.LibraryReadPath)
		itunesAbs, _ := filepath.Abs(itunesDir)
		if strings.HasPrefix(absPath, itunesAbs+"/") || absPath == itunesAbs {
			return true
		}
	}
	if config.AppConfig.ITunes.LibraryWritePath != "" {
		itunesDir := filepath.Dir(config.AppConfig.ITunes.LibraryWritePath)
		itunesAbs, _ := filepath.Abs(itunesDir)
		if strings.HasPrefix(absPath, itunesAbs+"/") || absPath == itunesAbs {
			return true
		}
	}

	// Also check if path contains "iTunes Media" as a safety net
	if strings.Contains(absPath, "iTunes Media") || strings.Contains(absPath, "iTunes%20Media") {
		return true
	}

	// Hard-block .failed/ quarantine folder — never write to or move quarantined files.
	if strings.Contains(filepath.ToSlash(absPath), "/.failed/") {
		return true
	}

	return false
}

func loadDismissedDedupGroups(store database.Store) map[string]bool {
	dismissed := map[string]bool{}
	pref, err := store.GetUserPreference("dedup_dismissed_groups")
	if err != nil || pref == nil || pref.Value == nil || *pref.Value == "" {
		return dismissed
	}
	var keys []string
	if err := json.Unmarshal([]byte(*pref.Value), &keys); err != nil {
		return dismissed
	}
	for _, k := range keys {
		dismissed[k] = true
	}
	return dismissed
}

func saveDismissedDedupGroups(store database.Store, dismissed map[string]bool) {
	keys := make([]string, 0, len(dismissed))
	for k := range dismissed {
		keys = append(keys, k)
	}
	data, err := json.Marshal(keys)
	if err != nil {
		slog.Warn("failed to marshal dismissed dedup groups", "err", err)
		return
	}
	if err := store.SetUserPreference("dedup_dismissed_groups", string(data)); err != nil {
		slog.Warn("failed to save dismissed dedup groups", "err", err)
	}
}
