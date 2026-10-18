// file: internal/server/itl_pid.go
// version: 1.5.0
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

// pidIntegrityHandler handles GET /api/v1/itunes/pid-integrity. Always
// read-only: it reports the duplicate-PID census + relocate-correctness probe.
// The ITL path (itunes.library_write_path) is optional: when it is unset the
// census still runs and tracks_in_itl is 0. A path that is set but invalid
// answers a single 500.
func (s *Server) pidIntegrityHandler(c *gin.Context) {
	itlPath, err := configuredITLPath() // "" is fine: the census runs without the ITL
	if err != nil {
		httputil.RespondWithInternalError(c, "invalid iTunes library ITL path in config")
		return // one response only: the error, not the census after it
	}
	report, err := itunes.ComputePIDIntegrity(s.storeForWiring(), itlPath)
	if err != nil {
		httputil.RespondWithInternalError(c, fmt.Sprintf("pid-integrity census failed: %v", err))
		return
	}
	httputil.RespondWithOK(c, gin.H{"report": report})
}
