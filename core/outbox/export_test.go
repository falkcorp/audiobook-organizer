// file: core/outbox/export_test.go
// version: 1.0.0
// guid: c3f2f7d8-6a5e-4c61-b8a8-0b2d0b9c4e11
// last-edited: 2026-11-12

package outbox

import "time"

// TestHooks are the crash-injection seams of the relay (hooks): each runs
// inside the step it names, after that step's last durable write was staged
// and before the next one's. A test takes a CrashClone of the file system in
// one to see exactly what a crash at that point leaves.
type TestHooks struct {
	BeforeIntakeCommit func(topic string)
	AfterIntakeCommit  func(topic string)
	AfterHandler       func(consumer string, seq uint64)
	BeforeAckCommit    func(consumer string, seq uint64)
	AfterAckCommit     func(consumer string, seq uint64)
	BeforeDeadCommit   func(consumer string, seq uint64)
	// BeforeRetentionBatch runs after a sweep decided what to delete, before the
	// batch re-reads the cursors the decision rests on.
	BeforeRetentionBatch func(topic string, pp int)
}

// SetTestHooks installs h; call it before the relay starts.
func (o *Outbox) SetTestHooks(h TestHooks) {
	o.hooks.mu.Lock()
	defer o.hooks.mu.Unlock()
	o.hooks.beforeIntakeCommit = h.BeforeIntakeCommit
	o.hooks.afterIntakeCommit = h.AfterIntakeCommit
	o.hooks.afterHandler = h.AfterHandler
	o.hooks.beforeAckCommit = h.BeforeAckCommit
	o.hooks.afterAckCommit = h.AfterAckCommit
	o.hooks.beforeDeadCommit = h.BeforeDeadCommit
	o.hooks.beforeRetentionBatch = h.BeforeRetentionBatch
}

// SetSweepEvery sets the retention sweep interval; call it before the relay starts.
func (o *Outbox) SetSweepEvery(d time.Duration) { o.sweepEvery = d }

// BackoffDelay exposes backoffDelay.
func BackoffDelay(attempt int, lo, hi time.Duration, u float64) time.Duration {
	return backoffDelay(attempt, lo, hi, u)
}

// Running reports whether a relay run is in its loop.
func (o *Outbox) Running() bool { return o.running.Load() }
