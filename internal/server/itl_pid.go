// file: internal/server/itl_pid.go
// version: 1.4.0
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
// It needs the configured ITL path (itunes.library_write_path) and answers a
// single 400 when it is unset.
func (s *Server) pidIntegrityHandler(c *gin.Context) {
	itlPath, ok := resolveITLPath(c)
	if !ok {
		return // resolveITLPath already wrote the error response; writing the census as well made two bodies
	}
	report, err := itunes.ComputePIDIntegrity(s.storeForWiring(), itlPath)
	if err != nil {
		httputil.RespondWithInternalError(c, fmt.Sprintf("pid-integrity census failed: %v", err))
		return
	}
	httputil.RespondWithOK(c, gin.H{"report": report})
}
