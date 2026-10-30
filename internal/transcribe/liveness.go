// file: internal/transcribe/liveness.go
// version: 1.3.0
// guid: 4c1d7e92-6a38-4b05-8f1e-9d2a5c7b3e61
// last-edited: 2026-10-30

package transcribe

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// livenessInterval is how often a long request polls its worker's /health. A
// variable only so tests can shorten it; production never writes it.
var livenessInterval = 30 * time.Second

// livenessMisses is how many CONSECUTIVE failed probes make a worker count as
// unreachable. One miss is a busy event loop or a dropped packet; three in a
// row (about 90 s at the default interval) is a worker that is gone.
const livenessMisses = 3

// The two reasons superviseRequest cancels a request it was watching. They are
// context causes, never returned to a caller: superviseRequest maps each to the
// error type its situation deserves.
var (
	errLivenessUnreachable = errors.New("worker stopped answering /health")
	errLivenessWedged      = errors.New("worker's current decode is past its deadline")
	// errRequestDone is the cause superviseRequest sets itself once the request
	// returned, so a poller cancel that arrives afterwards cannot replace it.
	errRequestDone = errors.New("request finished")
)

// superviseAfterRequestHook, when set, runs after do returned an error and
// before superviseRequest classifies it, with the request's cancel func: a
// test uses it to land a liveness cancel in exactly that window. Nil in
// production; only tests write it.
var superviseAfterRequestHook func(cancel context.CancelCauseFunc)

// superviseRequest runs do (one long request to the worker at url) while
// watching the worker from the side, and ends the request early when the worker
// is plainly not going to answer it.
//
// The request ceiling (requestTimeout) is a backstop, not a detector: it is the
// sum of every clip's own limit plus the waits that precede a clip, which is
// minutes to hours for a full chunk. Everything the worker's own per-clip
// deadline does not cover -- the host asleep behind a socket kept alive by TCP
// offload, a black-holed network, a kernel hang that survives SIGKILL, a wedged
// web process -- would hold the slot, and the dispatch's other jobs, for that
// whole ceiling. Polling /health finds those in minutes:
//
//   - livenessMisses consecutive failed probes: the worker is unreachable. The
//     result is a *TransportError (an outage), NOT a stuck worker: the request
//     is re-sent after the usual wait, and no file is blamed.
//   - busy_seconds past busy_limit_seconds plus stuckGrace: the worker answers
//     /health but has lost control of its own decoder. The result is a
//     *WorkerStuckError, the same as the request ceiling would have produced.
//   - machine_id that is not the machine the dispatch froze for the port (ctx
//     carries it: withFrozenWorker): the port was handed to another machine
//     mid-request. The result is a *WorkerSwappedError, which blames no machine
//     (see workerswap.go). The check comes before the stuck one: a decode that
//     is past its deadline on the NEW machine says nothing about the old one.
//
// Only a worker that reports busy_seconds is polled (h.BusySeconds != nil). A
// worker older than whisper_mlx_server.py 1.8.0 blocks its event loop for the
// length of a request, so a probe would fail on a healthy worker; it keeps the
// ceiling alone.
//
// do receives a context that is cancelled when the supervisor gives up, and its
// error is mapped here, so callers pass the raw request error through.
func superviseRequest(ctx context.Context, url string, h remoteHealth, timeout time.Duration, jobs []string, do func(ctx context.Context) error) error {
	rctx, cancel := context.WithCancelCause(ctx)
	var pollDone chan struct{}
	// Cancel BEFORE waiting for the poller: it may be inside a 3 s probe, which
	// the cancellation cuts short. A cancel after the supervisor's own never
	// replaces its cause (the first cause wins).
	defer func() {
		cancel(nil)
		if pollDone != nil {
			<-pollDone
		}
	}()

	if h.BusySeconds != nil {
		pollDone = make(chan struct{})
		go func() {
			defer close(pollDone)
			pollWorkerLiveness(rctx, url, cancel)
		}()
	}

	started := time.Now()
	err := do(rctx)
	if err == nil {
		// A response that raced the poller's cancel is not trusted either: it
		// may be the swapped-in machine's, and cannot be told from the old one's.
		if ic, ok := context.Cause(rctx).(*identityChangedCause); ok {
			return swappedError(url, ic, time.Since(started), jobs)
		}
		// The poll cannot see a swap in the window after its last probe (up to
		// livenessInterval), nor at all for a worker it does not poll, so the
		// answer is checked once more before it is accepted.
		if fw, frozen := frozenWorkerFrom(ctx); frozen {
			return verifyAfterResponse(ctx, url, fw, time.Since(started), jobs)
		}
		return nil
	}
	if superviseAfterRequestHook != nil {
		superviseAfterRequestHook(cancel)
	}
	// Read the supervisor's cause only for a request it actually ended, and
	// decide that from the request's OWN error: a request the supervisor ended
	// failed with its cause (net/http returns context.Cause) or with
	// context.Canceled. do can also fail for its own reasons (an HTTP 500, a
	// decode error) while the poller is still running, and a probe can cancel
	// rctx in the window after do returned; reading rctx's state instead (as an
	// earlier version did) let that probe relabel the 500 as a stuck or
	// unreachable worker. The error do returned cannot change.
	cancel(errRequestDone)
	cause := context.Cause(rctx)
	if !errors.Is(err, cause) && !errors.Is(err, context.Canceled) {
		cause = nil
	}
	// The caller's own cancellation wins over anything the supervisor decided.
	if ctx.Err() != nil {
		return err
	}
	elapsed := time.Since(started)
	switch cause {
	case errLivenessUnreachable:
		return &TransportError{
			Endpoints: []string{url},
			Err: fmt.Errorf("%s stopped answering /health (%d probes in a row) %s into a request of %d job(s): %w",
				url, livenessMisses, elapsed.Round(time.Second), len(jobs), errLivenessUnreachable),
			Recognized: true,
		}
	case errLivenessWedged:
		return &WorkerStuckError{URL: url, After: elapsed, Jobs: jobs}
	}
	if ic, ok := cause.(*identityChangedCause); ok {
		return swappedError(url, ic, elapsed, jobs)
	}
	return stuckFromHTTP(ctx, err, url, timeout, jobs)
}

// verifyAfterResponse is the check made before a successful response is
// accepted on a request with a frozen identity: one synchronous /health probe,
// judged by swappedTo. A response is the one thing the poll cannot vouch for: a
// port handed over after the poll's last probe answers as the new machine and
// would be stored under the old one's name. It returns nil when the worker is
// still the frozen machine (or the frozen identity names none).
//
// An unreadable probe withholds the response (a WorkerSwappedError with
// Unverified set): nothing says it came from the frozen machine, and the cost of
// withholding is only that the jobs are sent again elsewhere, while the cost of
// accepting is a strike or a transcript credited to a machine that never saw the
// file. This is the rule regate applies after a cooldown wait (a port that cannot
// be read is not used). The probe is not tied to the request's cancellation: a
// cancelled op still must not store an unverified answer, and the probe is
// bounded by healthProbeTimeout. The durable fix is for the worker to echo its
// machine_id in the batch response itself (see the decision note).
func verifyAfterResponse(ctx context.Context, url string, fw frozenWorker, after time.Duration, jobs []string) error {
	if !fw.comparable() {
		return nil
	}
	h, ok := probeRemoteHealth(context.WithoutCancel(ctx), url)
	if !ok {
		return &WorkerSwappedError{URL: url, Was: fw.key, After: after, Jobs: jobs, Unverified: true}
	}
	if now, swapped := fw.swappedTo(url, h); swapped {
		return &WorkerSwappedError{URL: url, Was: fw.key, Now: now, After: after, Jobs: jobs}
	}
	return nil
}

// swappedError is the error for a request ended by an identityChangedCause. It
// deliberately does not wrap the request's own error: that is a *url.Error for
// the cancelled call, which isTransportishError would read as an outage.
func swappedError(url string, ic *identityChangedCause, after time.Duration, jobs []string) error {
	return &WorkerSwappedError{URL: url, Was: ic.was, Now: ic.now, After: after, Jobs: jobs}
}

// pollWorkerLiveness probes url every livenessInterval until ctx ends,
// cancelling ctx with the cause that matches what it saw. The identity the
// dispatch froze for the port is read from ctx (withFrozenWorker); a request
// sent without one is not checked for a swap.
func pollWorkerLiveness(ctx context.Context, url string, cancel context.CancelCauseFunc) {
	fw, frozen := frozenWorkerFrom(ctx)
	t := time.NewTicker(livenessInterval)
	defer t.Stop()
	misses := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		h, ok := probeRemoteHealth(ctx, url)
		if ctx.Err() != nil {
			return
		}
		if !ok {
			misses++
			if misses >= livenessMisses {
				cancel(errLivenessUnreachable)
				return
			}
			continue
		}
		misses = 0
		if frozen {
			if now, swapped := fw.swappedTo(url, h); swapped {
				cancel(&identityChangedCause{was: fw.key, now: now})
				return
			}
		}
		if stuckRefusal(gatedEndpoint{Endpoint: Endpoint{URL: url}, Health: h, Probed: true}) != "" {
			cancel(errLivenessWedged)
			return
		}
	}
}
