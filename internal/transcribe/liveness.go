// file: internal/transcribe/liveness.go
// version: 1.0.0
// guid: 4c1d7e92-6a38-4b05-8f1e-9d2a5c7b3e61
// last-edited: 2026-10-26

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
)

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
	cause := context.Cause(rctx)
	if err == nil {
		return nil
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
	return stuckFromHTTP(ctx, err, url, timeout, jobs)
}

// pollWorkerLiveness probes url every livenessInterval until ctx ends,
// cancelling ctx with the cause that matches what it saw.
func pollWorkerLiveness(ctx context.Context, url string, cancel context.CancelCauseFunc) {
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
		if stuckRefusal(gatedEndpoint{Endpoint: Endpoint{URL: url}, Health: h, Probed: true}) != "" {
			cancel(errLivenessWedged)
			return
		}
	}
}
