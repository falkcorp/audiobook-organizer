// file: internal/operations/registry/scan_standdown_hold.go
// version: 1.2.0
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

// ScanStandDownRequestGate is what a request-scoped hold needs: the no-wait
// acquire plus renewal, so a long request (bulk fetch, candidate apply, the
// single apply's background file job) renews per item instead of outliving
// its lease.
type ScanStandDownRequestGate interface {
	ScanStandDownTryGate
	RenewScanStandDown(holderOpID string) bool
}

type scanStandDownHoldKey struct{}

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
// The acquire itself persists no marker: there is no quiesced scan to protect
// across a reboot, and the marker is a singleton that a concurrent blocking
// holder may own. A request hold that renews (Beat → RenewScanStandDown) does
// write the marker with its http: holder id and whatever scanOpID is recorded;
// that is harmless because startup acts on the marker only when ScanOpID is set.
func (r *Registry) TryAcquireScanStandDown(holderOpID, reason string) (func(), error) {
	if holderOpID == "" {
		return nil, fmt.Errorf("scan stand-down: empty holder opID")
	}
	r.scanGate.mu.Lock()
	r.scanGate.holders[holderOpID] = time.Now().Add(r.leaseTTL())
	r.scanGate.mu.Unlock()

	if r.anyScanClaimed() {
		// Refuse through the normal release path, never an inline delete. Our
		// brief registration can make the worker pickup gate drop a claimed scan
		// as interrupted_quiesced and park it on scanGate.dropped; only
		// releaseScanStandDown drains that list, so an inline delete here would
		// strand the scan until some later holder's last release or a restart.
		// As the last holder out it also wakes the dispatcher for any scan Gate
		// 3.5 held back on our account.
		r.releaseScanStandDown(holderOpID)
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
	gate     interface{ RenewScanStandDown(string) bool }
	holderID string
	held     bool
	release  func()
	reporter Reporter
	lost     atomic.Bool
	// refs counts the owner (1) plus every Retain not yet done. The gate is
	// released when it reaches zero, so work handed to a background pool keeps
	// the scan down until that work finishes.
	refs      atomic.Int64
	ownerDone sync.Once
}

func newHold(parent context.Context) *ScanStandDownHold {
	hctx, cancel := context.WithCancelCause(parent)
	h := &ScanStandDownHold{ctx: hctx, cancel: cancel, release: func() {}}
	h.refs.Store(1)
	return h
}

// take records a successful acquire and makes the hold reachable from its
// context, so ScanStandDownCheckpoint (and RunItems) can beat it per item from
// code that only has the ctx.
func (h *ScanStandDownHold) take(gate interface{ RenewScanStandDown(string) bool }, holderID string, release func()) {
	h.gate, h.holderID, h.held, h.release = gate, holderID, true, release
	h.ctx = context.WithValue(h.ctx, scanStandDownHoldKey{}, h)
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
	h := newHold(ctx)
	h.reporter = rep
	if gate == nil {
		return h, nil
	}
	holderID := ReporterOpID(rep)
	if holderID == "" {
		return h, nil
	}
	rel, err := gate.AcquireScanStandDown(ctx, holderID, reason)
	if err != nil {
		h.cancel(err)
		return nil, fmt.Errorf("%s: %s and it did not stand down: %w", reason, ErrScanRunning.Error(), err)
	}
	h.take(gate, holderID, rel)
	h.reporter = &standDownReporter{Reporter: rep, hold: h}
	return h, nil
}

// TryHoldScanStandDown is the request-path hold: no wait and no quiesce
// (TryAcquireScanStandDown), ErrScanRunning while a scan is running, and a hold
// the handler beats per item with Checkpoint exactly like an op. Its context is
// rooted in Background, not the request: the single apply's file job runs after
// the response is written, and a client disconnect must not read as a lost hold.
// A nil gate yields an ungated hold (tests that never run a scan).
func TryHoldScanStandDown(gate ScanStandDownRequestGate, reason string) (*ScanStandDownHold, error) {
	h := newHold(context.Background())
	if gate == nil {
		return h, nil
	}
	holderID := RequestScanStandDownHolderID(reason)
	rel, err := gate.TryAcquireScanStandDown(holderID, reason)
	if err != nil {
		h.cancel(err)
		return nil, err
	}
	h.take(gate, holderID, rel)
	return h, nil
}

// ScanStandDownCheckpoint is the per-item beat for code that only has the ctx.
// When ctx carries a hold it renews the lease and returns ErrScanStandDownLost
// once the lease is gone; in every case it returns the context's cancellation
// cause. Call it before each item's write: a lost hold must stop the loop before
// the next write, not after the batch.
func ScanStandDownCheckpoint(ctx context.Context) error {
	if h := holdFromContext(ctx); h != nil {
		return h.Checkpoint()
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return nil
}

func holdFromContext(ctx context.Context) *ScanStandDownHold {
	h, _ := ctx.Value(scanStandDownHoldKey{}).(*ScanStandDownHold)
	return h
}

// Context is the op's context for its writes; canceled if the lease is lost.
func (h *ScanStandDownHold) Context() context.Context { return h.ctx }

// Reporter is the op's reporter; its UpdateProgress renews the lease.
func (h *ScanStandDownHold) Reporter() Reporter { return h.reporter }

// Held reports whether the gate is actually held (false for the no-gate cases).
func (h *ScanStandDownHold) Held() bool { return h.held }

// Lost reports whether the lease lapsed during the op.
func (h *ScanStandDownHold) Lost() bool { return h.lost.Load() }

// Beat renews the lease. It returns false once the hold is lost, after
// canceling Context with ErrScanStandDownLost. Safe for concurrent use by pool
// workers. Renewal is an in-memory map update; the registry throttles the
// marker write it also does, so a per-item beat in a tight loop is cheap.
func (h *ScanStandDownHold) Beat() bool {
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

func (h *ScanStandDownHold) beat() bool { return h.Beat() }

// Checkpoint is the per-item call: it beats the lease and then checks the
// context, returning ErrScanStandDownLost for a lost hold and the cancellation
// cause otherwise. Callers stop (skip the item's write) on any non-nil result.
func (h *ScanStandDownHold) Checkpoint() error {
	if !h.Beat() {
		return ErrScanStandDownLost
	}
	if h.ctx.Err() != nil {
		return context.Cause(h.ctx)
	}
	return nil
}

// Retain keeps the gate held for work that outlives the caller (a job handed to
// the file-IO pool). Call done when that work finishes, or when it is dropped
// without running; the gate is released once the owner and every Retain are done.
func (h *ScanStandDownHold) Retain() (done func()) {
	h.refs.Add(1)
	var once sync.Once
	return func() { once.Do(h.unref) }
}

func (h *ScanStandDownHold) unref() {
	if h.refs.Add(-1) == 0 {
		h.release()
		h.cancel(nil)
	}
}

// Release ends the owner's share of a request hold (see Retain).
func (h *ScanStandDownHold) Release() { _ = h.Finish(nil) }

// Finish releases the gate (re-queuing the quiesced scan when this was the last
// holder) and returns runErr, joined with ErrScanStandDownLost when the lease
// lapsed so the op's failure names the real cause rather than "context canceled".
func (h *ScanStandDownHold) Finish(runErr error) error {
	h.ownerDone.Do(h.unref)
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
