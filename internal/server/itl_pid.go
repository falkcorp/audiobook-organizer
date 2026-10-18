// file: internal/server/itl_pid.go
// version: 1.3.0
// guid: d6b1f048-3e29-4a75-9c81-0f2a7b4c6e93
// last-edited: 2026-10-18
//
// book_file iTunes-PID integrity endpoint. /pid-integrity is a read-only census
// of duplicate PIDs (a PID must identify exactly one book_file). The matching
// POST /pid-repair endpoint was retired by 01-P81k and answers 410 (see
// server_lifecycle.go); the repair plan preview remains in `pid-census --repair`.
// See docs/specs/2026-07-23-itunes-2way-sync-continuation-findings.md.

package server

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/gin-gonic/gin"
)

// pidIntegrityHandler handles GET/POST /api/v1/itunes/pid-integrity. Always
// read-only: it reports the duplicate-PID census + relocate-correctness probe.
func (s *Server) pidIntegrityHandler(c *gin.Context) {
	itlPath, _ := resolveITLPath(c) // best-effort: census works without the ITL
	report, err := itunes.ComputePIDIntegrity(s.storeForWiring(), itlPath)
	if err != nil {
		httputil.RespondWithInternalError(c, fmt.Sprintf("pid-integrity census failed: %v", err))
		return
	}
	httputil.RespondWithOK(c, gin.H{"report": report})
}
