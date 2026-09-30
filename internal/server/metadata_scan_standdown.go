// file: internal/server/metadata_scan_standdown.go
// version: 1.1.0
// guid: 6e0a4c28-9d71-4b3f-bc5e-81f7a2d34c96
// last-edited: 2026-09-30

package server

import (
	"context"

	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// scanGate resolves the blocking scan stand-down gate at call time. Tests set
// scanStandDownGateOverride; production uses the *Server methods, which
// delegate to s.opRegistry and are no-ops when it is nil.
//
// Only ops use it. Request handlers took a no-wait variant that answered 409
// while any scan ran; since 2026-09-30 they coordinate per book instead
// (internal/scanlock) and never refuse because a scan is running.
func (s *Server) scanGate() opsregistry.ScanStandDownGate {
	if s.scanStandDownGateOverride != nil {
		return s.scanStandDownGateOverride
	}
	return s
}

// holdMetadataScanStandDown is the op-side acquire for every metadata-applying
// op registered in package server. See opsregistry.HoldScanStandDown.
func (s *Server) holdMetadataScanStandDown(ctx context.Context, reporter opsregistry.Reporter, reason string) (*opsregistry.ScanStandDownHold, error) {
	return opsregistry.HoldScanStandDown(ctx, s.scanGate(), reporter, reason)
}
