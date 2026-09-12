// file: internal/server/handlers/metadata/scan_standdown.go
// version: 1.1.0
// guid: a41d7f3e-5b20-4c8e-9f16-2e8c0b7d9a53
// last-edited: 2026-09-12

package metadatahandler

import (
	"errors"
	"net/http"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/gin-gonic/gin"
)

// SetScanStandDownGate wires the no-wait scan stand-down gate. Nil leaves the
// handlers ungated (unit tests that never run a scan).
func (h *Handler) SetScanStandDownGate(g opsregistry.ScanStandDownRequestGate) {
	h.scanGate = g
}

// holdScanStandDown gates a handler that writes book metadata inline. Metadata is
// never applied during a library scan: while one is running this writes 409 at
// once (no wait, and the scan is not interrupted for a single request) and
// returns ok=false. Otherwise the caller holds the gate, so no scan can start,
// until it calls hold.Release. Handlers that loop over books call
// hold.Checkpoint per book (renews the lease; stops before the write once it is
// lost); work handed to the file-IO pool keeps the gate with hold.Retain.
func (h *Handler) holdScanStandDown(c *gin.Context, reason string) (*opsregistry.ScanStandDownHold, bool) {
	hold, err := opsregistry.TryHoldScanStandDown(h.scanGate, reason)
	if err != nil {
		if errors.Is(err, opsregistry.ErrScanRunning) {
			httputil.RespondWithError(c, http.StatusConflict, opsregistry.ErrScanRunning.Error(), "SCAN_RUNNING")
			return nil, false
		}
		httputil.InternalError(c, "scan stand-down", err)
		return nil, false
	}
	return hold, true
}
