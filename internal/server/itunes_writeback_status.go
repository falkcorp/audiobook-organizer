// file: internal/server/itunes_writeback_status.go
// version: 1.0.0
// guid: 2f8b5d16-9c3a-4e71-b4d2-7a1e6c9f0b35
// last-edited: 2026-10-07
//
// Owner-visible state of the iTunes write-back queue. Added 2026-10-07 after
// the batcher silently dropped every batch for weeks (only an ERROR log line,
// no ids): the queue now never drops, and this is where its failures, backoff
// and held removes are seen. See docs/plans/2026-10-07-itunes-writeback-drops.md.

package server

import (
	"strconv"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/gin-gonic/gin"
)

// itunesWritebackStatusHandler handles GET /api/v1/itunes/writeback/status.
func (s *Server) itunesWritebackStatusHandler(c *gin.Context) {
	if s.writeBackBatcher == nil {
		httputil.RespondWithServiceUnavailable(c, "iTunes write-back batcher not configured")
		return
	}
	httputil.RespondWithOK(c, s.writeBackBatcher.Status())
}

// itunesWritebackReleaseHeldHandler handles
// POST /api/v1/itunes/writeback/held/release?limit=N. It moves up to N (at
// most MaxRemovesPerFlush) held removes back into the queue: the owner's
// explicit action on the remove-cap circuit breaker.
func (s *Server) itunesWritebackReleaseHeldHandler(c *gin.Context) {
	if s.writeBackBatcher == nil {
		httputil.RespondWithServiceUnavailable(c, "iTunes write-back batcher not configured")
		return
	}
	limit := itunesservice.MaxRemovesPerFlush
	if v := c.Query("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			httputil.RespondWithBadRequest(c, "limit must be a positive integer")
			return
		}
		limit = n
	}
	released, stillHeld := s.writeBackBatcher.ReleaseHeldRemoves(limit)
	httputil.RespondWithOK(c, gin.H{
		"released":   released,
		"still_held": stillHeld,
		"status":     s.writeBackBatcher.Status(),
	})
}
