// file: internal/operations/registry/scan_standdown_hold.go
// version: 1.6.0
// guid: 3b7e91d4-0c52-4f6a-a8e3-6d2f1c9b5a07
// last-edited: 2026-09-30

package registry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
)

// Bulk metadata OPS (batch-apply-cached, bulk write-back, metadata
// refresh/upgrade, ...) block on AcquireScanStandDown via HoldScanStandDown,
// exactly like the maintenance callers of acquireScanStandDownForApply: the scan
// is quiesced, the op fails with a clear error if it does not park, and the
// lease is renewed on the op's own progress heartbeat.
//
// HTTP requests used a no-wait variant here (TryAcquireScanStandDown) that
// refused with 409 SCAN_RUNNING while any scan ran. It was removed on
// 2026-09-30: requests now coordinate with the scanner per BOOK
// (internal/scanlock) and are never refused because a scan is running.

// ErrScanRunning prefixes the error an op returns when the scan does not stand
// down for it (HoldScanStandDown).
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

type scanStandDownHoldKey struct{}

// LibraryScanRunning reports whether a library.scan is claimed or running. It
// is a read: it registers no holder, parks nothing and starts no grace timer.
// For long read-mostly ops that yield to a scan (acoustid.window-backfill)
// rather than holding the scan down for hours.
func (r *Registry) LibraryScanRunning() bool { return r.anyScanClaimed() }

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

// TouchLiveness forwards the liveness stamp. Without it an op running under a
// stand-down hold would heartbeat into nothing and be killed as stuck. It does
// not renew the lease: the lease tracks progress, and this is not progress.
func (s *standDownReporter) TouchLiveness() { TouchLiveness(s.Reporter) }

func (s *standDownReporter) InvalidateLibraryStats() {
	if inv, ok := s.Reporter.(interface{ InvalidateLibraryStats() }); ok {
		inv.InvalidateLibraryStats()
	}
}
