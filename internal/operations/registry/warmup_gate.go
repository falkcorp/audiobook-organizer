// file: internal/operations/registry/warmup_gate.go
// version: 2.0.2
// guid: 9d3a7e52-6c14-4b0f-8a21-5f7e0c9b3d84
// last-edited: 2026-10-10

package registry

import (
	"log/slog"
	"sync/atomic"
	"time"
)

// WarmupWaitTimeout bounds how long operations are held back for the store's
// startup warmup before they run anyway. Memdb warmup on a large library takes
// roughly 130-200 seconds; this leaves headroom without ever holding work
// forever.
const WarmupWaitTimeout = 300 * time.Second

// WarmupStatusMessage is the progress message a queued operation shows while it
// is held back for the store's startup warmup.
const WarmupStatusMessage = "waiting for startup warmup"

// WarmupStatuser is implemented by a store that warms an in-memory read layer
// after start (the Pebble store). done is true once warmup has finished,
// published or fallen back to the slow path, i.e. nothing is left to wait for.
type WarmupStatuser interface {
	WarmupStatus() (ready, done bool, ms int64)
}

// WarmupGate decides, per dispatch cycle, whether non-exempt operations should
// be held back because the store is still warming. Holding happens BEFORE an op
// is claimed or handed to a worker: a held op keeps its queued row and takes no
// worker slot, no concurrency key, and starts no timeout or watchdog clock, so
// exempt (interactive) ops always find a free worker.
//
// The hold is bounded: it ends WarmupWaitTimeout after the gate first saw a
// warming store, logging once, so a warmup that never finishes cannot hold work
// forever.
type WarmupGate struct {
	// Timeout is the bound; zero means WarmupWaitTimeout.
	Timeout time.Duration

	firstSeen atomic.Int64 // unix nanos of the first call that found src warming
	loggedEnd atomic.Bool
}

// Holding reports whether non-exempt ops should be held at now. src nil (no
// store in the chain reports warmup) never holds.
func (g *WarmupGate) Holding(now time.Time, src WarmupStatuser, log *slog.Logger) bool {
	if src == nil {
		return false
	}
	if _, done, _ := src.WarmupStatus(); done {
		// Forget the clock so a later warm-up (a store reopened under the same
		// registry) gets a fresh bound instead of an already-expired one.
		g.firstSeen.Store(0)
		g.loggedEnd.Store(false)
		return false
	}
	g.firstSeen.CompareAndSwap(0, now.UnixNano())
	timeout := g.Timeout
	if timeout <= 0 {
		timeout = WarmupWaitTimeout
	}
	if now.Sub(time.Unix(0, g.firstSeen.Load())) >= timeout {
		if log != nil && g.loggedEnd.CompareAndSwap(false, true) {
			log.Warn("operations: startup warmup did not finish in time; releasing held operations onto the slow read path",
				"timeout", timeout)
		}
		return false
	}
	return true
}
