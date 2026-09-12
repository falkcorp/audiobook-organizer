// file: internal/server/handlers/metadata/scan_standdown.go
// version: 1.0.0
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
func (h *Handler) SetScanStandDownGate(g opsregistry.ScanStandDownTryGate) {
	h.scanGate = g
}

// holdScanStandDown gates a handler that writes book metadata inline. Metadata is
// never applied during a library scan: while one is running this writes 409 at
// once (no wait, and the scan is not interrupted for a single request) and
// returns ok=false. Otherwise the caller holds the gate, so no scan can start,
// until it calls release.
func (h *Handler) holdScanStandDown(c *gin.Context, reason string) (release func(), ok bool) {
	if h.scanGate == nil {
		return func() {}, true
	}
	rel, err := h.scanGate.TryAcquireScanStandDown(opsregistry.RequestScanStandDownHolderID(reason), reason)
	if err != nil {
		if errors.Is(err, opsregistry.ErrScanRunning) {
			httputil.RespondWithError(c, http.StatusConflict, opsregistry.ErrScanRunning.Error(), "SCAN_RUNNING")
			return nil, false
		}
		httputil.InternalError(c, "scan stand-down", err)
		return nil, false
	}
	return rel, true
}
