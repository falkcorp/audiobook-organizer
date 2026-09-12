// file: internal/operations/registry/scan_standdown_hold.go
// version: 1.0.0
// guid: 3b7e91d4-0c52-4f6a-a8e3-6d2f1c9b5a07
// last-edited: 2026-09-12

package registry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oklog/ulid/v2"
)

// Standing rule: metadata is NEVER applied while a library scan is running. The
// scan rewrites book rows and files under the library root; an apply that races
// it loses writes to whichever side lands second. This file turns that rule from
// prose into a control for the two kinds of caller that apply metadata:
//
//   - ops (batch-apply-cached, bulk write-back, metadata refresh/upgrade, ...)
//     block on AcquireScanStandDown via HoldScanStandDown, exactly like the
//     maintenance callers of acquireScanStandDownForApply: the scan is quiesced,
//     the op fails with a clear error if it does not park, and the lease is
//     renewed on the op's own progress heartbeat.
//   - HTTP requests (single-book apply, candidate apply, ...) never quiesce a
//     scan and never wait: TryAcquireScanStandDown refuses immediately with
//     ErrScanRunning, which handlers map to 409.

// ErrScanRunning is returned by TryAcquireScanStandDown when a library.scan is
// running (or claimed by the dispatcher). Its text is the user-facing message.
var ErrScanRunning = errors.New("a library scan is running; try again when it finishes")

// ErrScanStandDownLost reports that a holder's lease lapsed (no heartbeat inside
// the lease window) so the scanner may already be running again. The holder's
// context is canceled with this cause and its remaining writes are abandoned.
var ErrScanStandDownLost = errors.New("scan stand-down lease lost; remaining metadata writes abandoned")

// ScanStandDownGate is the slice of the registry an op needs to hold the scan
// stand-down. *Registry satisfies it, and so does every ScanController wrapper
// (the maintenance plugin's deps, *server.Server).
type ScanStandDownGate interface {
	AcquireScanStandDown(ctx context.Context, holderOpID, reason string) (release func(), err error)
	RenewScanStandDown(holderOpID string) bool
}

// ScanStandDownTryGate is the non-blocking gate a request handler needs.
type ScanStandDownTryGate interface {
	TryAcquireScanStandDown(holderOpID, reason string) (release func(), err error)
}

// TryAcquireScanStandDown registers holderOpID as a stand-down holder WITHOUT
// quiescing anything. If a library.scan is running, or has been claimed by the
// dispatcher, it returns ErrScanRunning at once (zero wait). Otherwise the caller
// holds the gate: the dispatcher will not start a new library.scan (Gate 3.5)
// until release is called or the lease lapses.
//
// Ordering is what makes this race-free. The holder is registered BEFORE r.running
// is inspected: a scan claimed before registration is visible in r.running (the
// claim block writes it under r.mu), and a claim after registration is refused by
// the Gate 3.5 check the claim block makes under the same r.mu.
//
// Stub handles (claimed, goroutine not started) count as running here, unlike in
// findRunningScan: the pickup gate would drop such a scan as interrupted_quiesced,
// and with no scanOpID recorded nothing would re-queue it. Refusing is cheaper than
// stranding a scan for one request.
//
// No marker is persisted: there is no quiesced scan to protect across a reboot,
// and the marker is a singleton that a concurrent blocking holder may own.
func (r *Registry) TryAcquireScanStandDown(holderOpID, reason string) (func(), error) {
	if holderOpID == "" {
		return nil, fmt.Errorf("scan stand-down: empty holder opID")
	}
	r.scanGate.mu.Lock()
	r.scanGate.holders[holderOpID] = time.Now().Add(r.leaseTTL())
	r.scanGate.mu.Unlock()

	if r.anyScanClaimed() {
		r.scanGate.mu.Lock()
		delete(r.scanGate.holders, holderOpID)
		live := r.liveHoldersLocked()
		r.scanGate.mu.Unlock()
		if live == 0 {
			// A scan enqueued in the window above was held back by Gate 3.5 on
			// our account; wake the dispatcher so it is not left waiting.
			r.pingDispatch()
		}
		return nil, ErrScanRunning
	}

	r.logger.Info("registry: scan stand-down acquired (no-wait)",
		"holder_op_id", holderOpID, "reason", reason)
	var once sync.Once
	return func() { once.Do(func() { r.releaseScanStandDown(holderOpID) }) }, nil
}

// anyScanClaimed reports whether any library.scan handle, full or stub, is in
// r.running.
func (r *Registry) anyScanClaimed() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, h := range r.running {
		if h != nil && h.defID == scanStandDownDefID {
			return true
		}
	}
	return false
}

// RequestScanStandDownHolderID mints a unique holder id for a request-scoped
// (non-op) caller. The registry rejects empty ids, and an HTTP request has no op id.
func RequestScanStandDownHolderID(reason string) string {
	return "http:" + reason + ":" + ulid.Make().String()
}

// ScanStandDownHold is an op's hold on the scan stand-down. Obtain it with
// HoldScanStandDown, run the op's work under Context() and Reporter(), and end
// with Finish.
type ScanStandDownHold struct {
	ctx      context.Context
	cancel   context.CancelCauseFunc
	gate     ScanStandDownGate
	holderID string
	held     bool
	release  func()
	reporter Reporter
	lost     atomic.Bool
}

// HoldScanStandDown acquires the scan stand-down for an op that applies metadata.
// It follows acquireScanStandDownForApply (internal/plugins/maintenance) exactly:
// keyed by the op's own id, no gate when there is no controller or no op id (a
// direct-call test or degraded context), and a genuine acquire failure is returned
// as an error the op must fail with, before its first write.
//
// The lease is renewed on the op's own progress heartbeat: every UpdateProgress
// through Reporter() renews it. That is deliberately not a ticker, for the same
// reason backupProgressReporter is not one: a ticker would keep the scanner parked
// for a wedged op. If a renewal fails (the lease lapsed), Context() is canceled
// with ErrScanStandDownLost and Finish reports it.
func HoldScanStandDown(ctx context.Context, gate ScanStandDownGate, rep Reporter, reason string) (*ScanStandDownHold, error) {
	hctx, cancel := context.WithCancelCause(ctx)
	h := &ScanStandDownHold{ctx: hctx, cancel: cancel, gate: gate, release: func() {}, reporter: rep}
	if gate == nil {
		return h, nil
	}
	holderID := ReporterOpID(rep)
	if holderID == "" {
		return h, nil
	}
	rel, err := gate.AcquireScanStandDown(ctx, holderID, reason)
	if err != nil {
		cancel(err)
		return nil, fmt.Errorf("%s: %s and it did not stand down: %w", reason, ErrScanRunning.Error(), err)
	}
	h.holderID, h.held, h.release = holderID, true, rel
	h.reporter = &standDownReporter{Reporter: rep, hold: h}
	return h, nil
}

// Context is the op's context for its writes; canceled if the lease is lost.
func (h *ScanStandDownHold) Context() context.Context { return h.ctx }

// Reporter is the op's reporter; its UpdateProgress renews the lease.
func (h *ScanStandDownHold) Reporter() Reporter { return h.reporter }

// Held reports whether the gate is actually held (false for the no-gate cases).
func (h *ScanStandDownHold) Held() bool { return h.held }

// Lost reports whether the lease lapsed during the op.
func (h *ScanStandDownHold) Lost() bool { return h.lost.Load() }

// beat renews the lease; on failure it cancels Context and returns false.
func (h *ScanStandDownHold) beat() bool {
	if !h.held {
		return true
	}
	if h.lost.Load() {
		return false
	}
	if !h.gate.RenewScanStandDown(h.holderID) {
		h.lost.Store(true)
		h.cancel(ErrScanStandDownLost)
		return false
	}
	return true
}

// Finish releases the gate (re-queuing the quiesced scan when this was the last
// holder) and returns runErr, joined with ErrScanStandDownLost when the lease
// lapsed so the op's failure names the real cause rather than "context canceled".
func (h *ScanStandDownHold) Finish(runErr error) error {
	h.release()
	h.cancel(nil)
	if h.lost.Load() {
		return errors.Join(ErrScanStandDownLost, runErr)
	}
	return runErr
}

// standDownReporter renews the lease on every progress stamp. It forwards the
// optional side interfaces callers type-assert (OpID, InvalidateLibraryStats) so
// wrapping never hides them.
type standDownReporter struct {
	Reporter
	hold *ScanStandDownHold
}

func (s *standDownReporter) UpdateProgress(current, total int, message string) error {
	if !s.hold.beat() {
		_ = s.Reporter.Log(slog.LevelWarn, "scan stand-down lease lost; aborting remaining metadata writes")
	}
	return s.Reporter.UpdateProgress(current, total, message)
}

func (s *standDownReporter) OpID() string { return ReporterOpID(s.Reporter) }

func (s *standDownReporter) InvalidateLibraryStats() {
	if inv, ok := s.Reporter.(interface{ InvalidateLibraryStats() }); ok {
		inv.InvalidateLibraryStats()
	}
}
