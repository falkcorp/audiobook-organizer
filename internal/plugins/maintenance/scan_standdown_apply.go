// file: internal/plugins/maintenance/scan_standdown_apply.go
// version: 1.0.0
// guid: 9f2c6a71-4b83-4de0-8c1a-5e7d0b2f3a64
// last-edited: 2026-09-06

// Package maintenance — shared helpers for holding the scan stand-down (PR #3080)
// across a write op's apply phase. Every op that rewrites book_file rows on disk
// or in the DB shares the same contract: acquire the gate before the first write,
// renew it on the per-item heartbeat, and treat a lost lease as a hard abort of
// the remaining writes (a lapsed lease means the scanner has resumed and would
// race the op's writes). These two helpers keep that contract in one place so the
// retrofitted ops — and Branch B — cannot drift apart on it.
package maintenance

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// acquireScanStandDownForApply acquires the scan stand-down for an op's apply
// phase, keyed by the op's own operation id. It returns:
//   - holderID: the op id used as the gate holder key,
//   - held: whether the gate is actually held. It is false — and the op then runs
//     with no interlock, exactly as it did before #3080 — when there is no
//     controller or the reporter carries no op id (a direct-call test, or a
//     degraded context with no registry, where AcquireScanStandDown itself returns
//     a no-op release). Empty holder ids are never passed to the registry, which
//     rejects them.
//   - release: always safe to defer-call, even when held is false.
//
// On a genuine acquire failure (the scan would not park within ctx/lease) it
// returns that error and the caller must not proceed to write.
func acquireScanStandDownForApply(ctx context.Context, scan ScanController, reporter sdk.Reporter, reason string) (holderID string, held bool, release func(), err error) {
	release = func() {}
	if scan == nil {
		return "", false, release, nil
	}
	holderID = registry.ReporterOpID(reporter)
	if holderID == "" {
		return "", false, release, nil
	}
	rel, aerr := scan.AcquireScanStandDown(ctx, holderID, reason)
	if aerr != nil {
		return holderID, false, release, aerr
	}
	return holderID, true, rel, nil
}

// scanStandDownLostForApply renews the stand-down lease (the per-item heartbeat)
// and reports whether the caller must abort its remaining writes. It returns
// false ("keep going") when the gate is not held, so an op with no interlock is
// never spuriously aborted. When held, it returns true only if the lease is gone —
// i.e. RenewScanStandDown reports the holder is no longer registered, which means
// the scanner has been (or is about to be) resumed.
func scanStandDownLostForApply(scan ScanController, holderID string, held bool) bool {
	if !held {
		return false
	}
	return !scan.RenewScanStandDown(holderID)
}
