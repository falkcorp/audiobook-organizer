// file: internal/server/handlers/audiobooks/list_flight.go
// version: 1.0.0
// guid: e5a1c7d3-4b28-4f9e-8c60-2d9b7f1e3a54
// last-edited: 2026-10-06

package audiobookshandler

import (
	"context"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// listFlightBuildTimeout is the hard ceiling on one shared list build. The
// slowest builds measured on production (cold memdb and list cache right after
// a restart) took 44-160 s; a build still running at 5 minutes is not going to
// produce an answer anyone is waiting for.
const listFlightBuildTimeout = 5 * time.Minute

// listFlight collapses concurrent identical list-cache misses into one build,
// and cancels that build when the LAST caller waiting on it leaves.
//
// It replaces a plain singleflight.Group whose build ran under
// context.WithoutCancel: a client could send many distinct queries, close each
// connection, and leave every build running detached (each 44-160 s on a cold
// server). Here each caller waits on its own request context: a caller that
// leaves stops waiting and drops its reference, and when no caller is left the
// build's context is cancelled -- the same thing a disconnect did before builds
// were shared. A build that outlives listFlightBuildTimeout is cancelled too.
//
// Results, including errors, reach only the callers that were waiting for
// that build; nothing here is cached (the caller fills the list cache on
// success).
type listFlight struct {
	mu    sync.Mutex
	calls map[string]*listFlightCall
}

type listFlightCall struct {
	done    chan struct{} // closed when resp/err are set
	resp    gin.H
	err     error
	waiters int
	cancel  context.CancelFunc
}

// do returns build's result for key, sharing one build among concurrent
// callers. reqCtx is this caller's request context: when it is done before the
// build finishes, do returns reqCtx.Err() at once, and the build is cancelled
// if this was the last caller waiting on it. build receives the shared build's
// context (reqCtx's values, the flight's own cancellation and the timeout).
func (f *listFlight) do(reqCtx context.Context, key string, build func(ctx context.Context) (gin.H, error)) (gin.H, error) {
	f.mu.Lock()
	if f.calls == nil {
		f.calls = make(map[string]*listFlightCall)
	}
	call, ok := f.calls[key]
	if !ok {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(reqCtx), listFlightBuildTimeout)
		call = &listFlightCall{done: make(chan struct{}), cancel: cancel}
		f.calls[key] = call
		go func() {
			resp, err := build(ctx)
			f.mu.Lock()
			call.resp, call.err = resp, err
			if f.calls[key] == call {
				delete(f.calls, key)
			}
			f.mu.Unlock()
			cancel()
			close(call.done)
		}()
	}
	call.waiters++
	f.mu.Unlock()

	select {
	case <-call.done:
		return call.resp, call.err
	case <-reqCtx.Done():
		f.mu.Lock()
		call.waiters--
		if call.waiters == 0 {
			// Nobody is waiting for this build any more. Cancel it, and take it
			// out of the map so a caller arriving now starts a fresh build
			// instead of joining one that is being torn down.
			call.cancel()
			if f.calls[key] == call {
				delete(f.calls, key)
			}
		}
		f.mu.Unlock()
		return nil, reqCtx.Err()
	}
}
