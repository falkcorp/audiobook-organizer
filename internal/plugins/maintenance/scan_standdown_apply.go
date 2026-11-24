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
	"errors"
	"fmt"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
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

// nativeScanStandDown is the ScanController of a native run: the apply helpers
// above take their stand-down from it, and it takes the run's own, rc.StandDown
// over the library scan's concurrency key (the lease the executor renews on the
// run's progress and reaps when it lapses), instead of the operations registry's
// controller, which the flip removes. One value serves one run's one hold.
type nativeScanStandDown struct {
	rc  *tasks.RC
	key tasks.Key

	mu   sync.Mutex
	hold tasks.Hold
	// loss is why the last renewal failed (nil while the hold is healthy). The
	// callers' Lost hook is a bool, so the reason is read back through
	// ScanStandDownLoss to word the abort.
	loss error
}

// AcquireScanStandDown takes the run's hold on the scan key. holderOpID is
// unused: the executor keys the hold by the run.
func (n *nativeScanStandDown) AcquireScanStandDown(_ context.Context, _, reason string) (func(), error) {
	if n.key == "" {
		return nil, fmt.Errorf("no scan stand-down key configured, refusing to %s without it", reason)
	}
	h, err := n.rc.StandDown(n.key)
	if err != nil {
		return nil, fmt.Errorf("%s: the library scan did not stand down: %w", reason, err)
	}
	n.mu.Lock()
	n.hold, n.loss = h, nil
	n.mu.Unlock()
	return h.Release, nil
}

// RenewScanStandDown extends the hold and reports whether it is still held. A
// renewal that fails ends the run's writes whichever way it failed (the lease
// lapsed, or the marker that records it could not be written and a crash would
// no longer keep the scan parked), and ScanStandDownLoss says which.
func (n *nativeScanStandDown) RenewScanStandDown(string) bool {
	n.mu.Lock()
	h := n.hold
	n.mu.Unlock()
	if h == nil {
		return false
	}
	if err := h.Renew(); err != nil {
		n.mu.Lock()
		n.loss = err
		n.mu.Unlock()
		return false
	}
	return true
}

// ScanStandDownValid reports whether the hold was taken and no renewal has found
// it lost. Hold has no read that does not extend, so this is the last renewal's
// answer.
func (n *nativeScanStandDown) ScanStandDownValid(string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.hold != nil && n.loss == nil
}

// ScanStandDownLoss is the error the last failed renewal returned, nil when no
// renewal has failed.
func (n *nativeScanStandDown) ScanStandDownLoss() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.loss
}

// scanStandDownLossReporter is what a ScanController that can say why its hold
// is gone implements (nativeScanStandDown).
type scanStandDownLossReporter interface{ ScanStandDownLoss() error }

// scanStandDownLossText says what happened to a stand-down hold that
// scanStandDownLostForApply found gone: the lease lapsed (the scanner may
// already have resumed), or the lease is still the run's but its marker could not
// be written. A controller that cannot say (the registry's) reports a lapse,
// which is the only way it loses a hold.
func scanStandDownLossText(scan ScanController) string {
	if r, ok := scan.(scanStandDownLossReporter); ok {
		if err := r.ScanStandDownLoss(); err != nil && !errors.Is(err, tasks.ErrHoldLost) {
			return fmt.Sprintf("scan stand-down marker could not be written (%v)", err)
		}
	}
	return "scan stand-down lease lapsed"
}
