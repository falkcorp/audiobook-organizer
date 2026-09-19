// file: internal/server/wire_fpworker_routes.go
// version: 1.0.0
// guid: 409812a9-b3d6-4cc8-b214-1d323191b8fc
// last-edited: 2026-09-19

package server

import (
	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/fpworker"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// Worker API rate limit (design (d)): ~120 requests/min per client IP, burst
// 20. A worker renews every 2 min and posts a batch per ~50 files, so this is
// far above a healthy worker's rate and only bounds a runaway one.
const (
	fpWorkerRatePerMinute = 120
	fpWorkerRateBurst     = 20
)

// wireFingerprintWorkerRoutes mounts /api/v1/fingerprint/worker/* on the api
// group (not protected: the feature-flag 404 must answer ahead of
// authentication). Chain: flag (404 while fingerprint_remote_workers_enabled
// is off, read per request), 8 MB body cap, the worker limiter, auth, then
// the fingerprint.worker permission, which a service user's API key can be
// scoped to alone.
func (s *Server) wireFingerprintWorkerRoutes(api *gin.RouterGroup, authMiddleware gin.HandlerFunc) {
	limiter := servermiddleware.NewIPRateLimiter(fpWorkerRatePerMinute, fpWorkerRateBurst)
	limiter.Start(s.bgCtx)
	h := fpworker.New(s.fpWorkerHub, func() bool { return config.AppConfig.FingerprintRemoteWorkersEnabled })
	h.Register(api, limiter.Middleware(), authMiddleware, s.perm(auth.PermFingerprintWorker))
}
