// file: internal/repairs/standdown.go
// version: 1.0.0
// guid: 0f6d3b82-9e14-4c7a-b25d-7a1e8c4f9d03
// last-edited: 2026-09-27

package repairs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// StandDown is the library-scan stand-down control
// (internal/operations/registry/scan_standdown.go, exposed to ops through
// maintenance.ScanController). Acquiring it pauses a running library.scan at
// a checkpoint and holds new scans queued; release resumes the scan.
type StandDown interface {
	AcquireScanStandDown(ctx context.Context, holderOpID, reason string) (release func(), err error)
	RenewScanStandDown(holderOpID string) bool
	ScanStandDownValid(holderOpID string) bool
}

// WaitOptions tunes AcquireStandDownWaiting. Zero values take the defaults.
type WaitOptions struct {
	// AttemptTimeout bounds one acquire attempt. The registry lease (and the
	// op watchdog's progress timeout) is 5 minutes and an acquire blocks
	// without stamping progress, so an attempt must end well inside it.
	AttemptTimeout time.Duration
	// RetryInterval is the wait between attempts.
	RetryInterval time.Duration
	// Sleep waits d or until ctx ends; tests inject an immediate one.
	Sleep func(ctx context.Context, d time.Duration) error
}

const (
	defaultAttemptTimeout = 60 * time.Second
	defaultRetryInterval  = 30 * time.Second
)

// ErrNoHolderID: apply was asked to run without an operation id. The
// stand-down is keyed by the op id; without one the op would write with no
// interlock at all, so the framework refuses instead.
var ErrNoHolderID = errors.New("repairs: no operation id to hold the scan stand-down with; refusing to write")

// AcquireStandDownWaiting acquires the scan stand-down for an apply, WAITING
// for it rather than failing.
//
// Owner ruling 2026-09-27: a repair apply is never rejected because a
// library.scan is running. It pauses the scan instead. If an attempt fails
// (the scan did not park within AttemptTimeout, or the registry refused), the
// apply waits RetryInterval and tries again, stamping liveness and logging
// each attempt so the op shows "waiting for the scan" and the watchdog does
// not reap it. It gives up only when ctx ends (cancel, or the op Timeout),
// and then returns ctx's error.
//
// Waiting in the op was chosen over re-queueing: a re-queued apply is a new
// op id with a new plan link and would lose its place, while a waiting op
// keeps its params, its row selection and its visible status. The registry
// releases a holder whose acquire timed out or was cancelled, so a retry
// starts clean.
//
// sd == nil (no registry: a direct-call test or a degraded context) returns a
// no-op release with held=false, the same contract as the maintenance
// plugin's acquireScanStandDownForApply. holderOpID must not be empty.
func AcquireStandDownWaiting(ctx context.Context, sd StandDown, holderOpID, reason string,
	reporter registry.Reporter, opts WaitOptions) (release func(), held bool, err error) {
	noop := func() {}
	if sd == nil {
		return noop, false, nil
	}
	if holderOpID == "" {
		return noop, false, ErrNoHolderID
	}
	if opts.AttemptTimeout <= 0 {
		opts.AttemptTimeout = defaultAttemptTimeout
	}
	if opts.RetryInterval <= 0 {
		opts.RetryInterval = defaultRetryInterval
	}
	if opts.Sleep == nil {
		opts.Sleep = sleepCtx
	}
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return noop, false, fmt.Errorf("%s: gave up waiting for the library scan to stand down: %w", reason, err)
		}
		actx, cancel := context.WithTimeout(ctx, opts.AttemptTimeout)
		rel, aerr := sd.AcquireScanStandDown(actx, holderOpID, reason)
		cancel()
		if aerr == nil {
			if rel == nil {
				rel = noop
			}
			return rel, true, nil
		}
		if reporter != nil {
			registry.TouchLiveness(reporter)
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf(
				"%s: library scan has not stood down yet (attempt %d: %v); waiting %s and retrying",
				reason, attempt, aerr, opts.RetryInterval))
		}
		if err := opts.Sleep(ctx, opts.RetryInterval); err != nil {
			return noop, false, fmt.Errorf("%s: gave up waiting for the library scan to stand down after %d attempts (last: %v): %w",
				reason, attempt, aerr, err)
		}
		if reporter != nil {
			registry.TouchLiveness(reporter)
		}
	}
}

// StandDownLost renews the lease (the per-item heartbeat) and reports whether
// the caller must abort its remaining writes. held=false never aborts.
func StandDownLost(sd StandDown, holderOpID string, held bool) bool {
	if !held || sd == nil {
		return false
	}
	return !sd.RenewScanStandDown(holderOpID)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
