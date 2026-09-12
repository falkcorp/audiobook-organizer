// file: internal/server/metadata_scan_standdown.go
// version: 1.0.1
// guid: 6e0a4c28-9d71-4b3f-bc5e-81f7a2d34c96
// last-edited: 2026-09-12

package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/gin-gonic/gin"
)

// metadataScanGate is what the metadata apply paths need from the scan
// stand-down: the blocking gate for ops and the no-wait gate for requests.
type metadataScanGate interface {
	opsregistry.ScanStandDownGate
	opsregistry.ScanStandDownTryGate
}

// scanGate resolves the gate at call time. Tests set scanStandDownGateOverride;
// production uses the *Server methods, which delegate to s.opRegistry and are
// no-ops when it is nil.
func (s *Server) scanGate() metadataScanGate {
	if s.scanStandDownGateOverride != nil {
		return s.scanStandDownGateOverride
	}
	return s
}

// TryAcquireScanStandDown implements opsregistry.ScanStandDownTryGate.
func (s *Server) TryAcquireScanStandDown(holderOpID, reason string) (func(), error) {
	if s.scanStandDownGateOverride != nil {
		return s.scanStandDownGateOverride.TryAcquireScanStandDown(holderOpID, reason)
	}
	if s.opRegistry == nil {
		return func() {}, nil
	}
	return s.opRegistry.TryAcquireScanStandDown(holderOpID, reason)
}

// holdMetadataScanStandDown is the op-side acquire for every metadata-applying
// op registered in package server. See opsregistry.HoldScanStandDown.
func (s *Server) holdMetadataScanStandDown(ctx context.Context, reporter opsregistry.Reporter, reason string) (*opsregistry.ScanStandDownHold, error) {
	return opsregistry.HoldScanStandDown(ctx, s.scanGate(), reporter, reason)
}

// holdScanStandDownForRequest is the request-side gate: it never quiesces a scan
// and never waits. While a library scan is running it writes 409 and returns
// ok=false; otherwise the caller holds the gate until it calls release.
func (s *Server) holdScanStandDownForRequest(c *gin.Context, reason string) (*opsregistry.ScanStandDownHold, bool) {
	hold, err := opsregistry.TryHoldScanStandDown(s.scanGate(), reason)
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
