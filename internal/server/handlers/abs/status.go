// file: internal/server/handlers/abs/status.go
// version: 1.0.0
// guid: 5d13a840-7f26-4b91-8c05-2e97b6d1fa38
// last-edited: 2026-07-30

package abs

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Ping handles GET /ping.
//
// It MUST answer 200 with no credential (§1.7.3 item 14): Absorb polls it every 20 s
// while offline and every 60 s while online, and the result gates its entire
// online/offline state machine — a 401 here parks the app offline. AudioBooth never
// calls it. The endpoint is still Cloudflare-Access-authenticated at the edge
// (§1.9.3), so "unauthenticated" means "unauthenticated at the app layer", not public.
//
// `success` is a real JSON boolean: 0/1 throws in Swift and reads as false in Dart
// (§1.8.5 item 10).
func (h *Handler) Ping(c *gin.Context) {
	respondJSON(c, http.StatusOK, gin.H{"success": true})
}

// Status handles GET /status.
//
// Unauthenticated for the same reason as /ping, plus a specific one: AudioBooth probes
// /status from its add-server screen using headers that have not been persisted yet
// (§1.9.3), i.e. before it has any credential of ours at all.
//
// serverVersion reports 2.36.0 — at or above 2.22.0 suppresses AudioBooth's
// "update your server" nag banner (§1.8.8 item 6). We only claim a version whose
// gating features we actually implement.
func (h *Handler) Status(c *gin.Context) {
	respondJSON(c, http.StatusOK, statusResponse{
		App:          "audiobookshelf",
		AuthFormData: authFormData{AuthLoginCustomMessage: ""},
		// "local" regardless of credential mode. In Mode C the Cloudflare edge has
		// already authenticated the person and /login skips the password check, but
		// advertising anything else would send clients down an OpenID flow they cannot
		// complete against us.
		AuthMethods:   []string{"local"},
		IsInit:        true,
		Language:      "en-us",
		ServerVersion: h.cfg.ServerVersion,
	})
}
