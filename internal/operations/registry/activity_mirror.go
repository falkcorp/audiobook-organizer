// file: internal/operations/registry/activity_mirror.go
// version: 1.0.0
// guid: af8533f9-bdc2-492d-a225-d66ffa0e0e9f
// last-edited: 2026-09-11

package registry

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
)

// activityMirror moves the Activity Log copy of an operation's log lines off
// the operation's own goroutine.
//
// WHY THIS EXISTS. dbReporter.Log mirrors every line into the Activity Log,
// and until 2026-09-11 it did so synchronously. On the SQLite activity backend
// that write goes through a writer pool capped at ONE connection, and
// maintenance.optimize-activity-db holds that connection for minutes while it
// runs ANALYZE. On 2026-09-11 a library scan logged a line nine seconds after
// the optimize job started, blocked inside Record behind the ANALYZE, stopped
// advancing, and was killed by the watchdog at the five-minute mark together
// with the optimize job. The watchdog was right — the scan really was frozen —
// so the fix is here, not in the watchdog: an operation must never wait on
// the Activity Log to make progress.
//
// DROPS ARE DELIBERATE, AND THEY ARE NOT DATA LOSS. The canonical record of an
// operation's log is op_logs_v2, written by dbReporter's own flush path; this
// mirror is a convenience copy for the unified Activity view. When the queue
// is full the line is dropped from that copy only, counted in
// audiobook_organizer_op_activity_mirror_dropped_total, and reported in a
// rate-limited WARN that says "activity mirror". That wording is distinct
// from internal/activity/writer.go's "activity channel full" on purpose: they
// are two different drop points on two different paths, and whoever is
// chasing a missing Activity row needs to tell them apart.
type activityMirror struct {
	inner  ActivityRecorder
	logger *slog.Logger
	queue  chan database.ActivityEntry

	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once

	dropped atomic.Uint64
	// lastDropWarn / lastErrWarn hold UnixNano of the last WARN of each kind,
	// so a stalled store produces one line a minute rather than one per op
	// log line.
	lastDropWarn atomic.Int64
	lastErrWarn  atomic.Int64
}

const (
	// activityMirrorQueueSize absorbs a multi-minute stall of the activity
	// writer at ordinary op log rates without dropping; it is a count of
	// entries, each a few hundred bytes, so the ceiling is a few MB.
	activityMirrorQueueSize = 4096
	// activityMirrorDrainBudget bounds how long Stop keeps writing queued
	// entries before discarding the rest, so shutdown is not held hostage by
	// a backlog.
	activityMirrorDrainBudget = 5 * time.Second
	activityMirrorWarnEvery   = time.Minute
)

func newActivityMirror(inner ActivityRecorder, logger *slog.Logger, size int) *activityMirror {
	if logger == nil {
		logger = slog.Default()
	}
	if size < 1 {
		size = 1
	}
	m := &activityMirror{
		inner:  inner,
		logger: logger,
		queue:  make(chan database.ActivityEntry, size),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go m.run()
	return m
}

// Record enqueues entry for the drain goroutine and returns immediately. It
// never blocks and always returns nil: the only caller discards the error, and
// a failure to mirror is reported by the drain goroutine, not the op.
func (m *activityMirror) Record(entry database.ActivityEntry) error {
	select {
	case <-m.stop:
		// After Stop nothing drains the queue, so do not fill it.
		m.drop()
		return nil
	default:
	}
	select {
	case m.queue <- entry:
	default:
		m.drop()
	}
	return nil
}

// Dropped reports how many entries this mirror has discarded.
func (m *activityMirror) Dropped() uint64 { return m.dropped.Load() }

// Stop drains what it can within activityMirrorDrainBudget and waits for the
// drain goroutine to exit. After Stop returns, the wrapped recorder will not
// be called again, so the caller may close the store behind it. Idempotent.
//
// The wait is unconditional, like Registry.Shutdown's join on the deps
// sweeper and for the same reason: returning while a Record is still inside
// the store lets the caller close that store under it. A single Record is
// bounded by the store's own busy timeout, and the drain loop checks its
// budget between records, so the wait is finite.
func (m *activityMirror) Stop() {
	m.stopOnce.Do(func() { close(m.stop) })
	slow := time.NewTimer(activityMirrorDrainBudget + 5*time.Second)
	defer slow.Stop()
	select {
	case <-m.done:
	case <-slow.C:
		m.logger.Warn("registry: still waiting on the activity mirror's in-flight write before shutdown")
		<-m.done
	}
}

func (m *activityMirror) run() {
	defer close(m.done)
	for {
		select {
		case entry := <-m.queue:
			m.write(entry)
		case <-m.stop:
			m.drain()
			return
		}
	}
}

func (m *activityMirror) drain() {
	deadline := time.Now().Add(activityMirrorDrainBudget)
	for time.Now().Before(deadline) {
		select {
		case entry := <-m.queue:
			m.write(entry)
		default:
			m.logFinalDrops()
			return
		}
	}
	// Out of budget: discard the remainder, counted like any other drop.
	for {
		select {
		case <-m.queue:
			m.drop()
		default:
			m.logFinalDrops()
			return
		}
	}
}

func (m *activityMirror) write(entry database.ActivityEntry) {
	if err := m.inner.Record(entry); err != nil && m.warnDue(&m.lastErrWarn) {
		m.logger.Warn("registry: activity mirror could not write an operation log line to the Activity Log "+
			"(op_logs_v2 still has it)", "type", entry.Type, "error", err)
	}
}

func (m *activityMirror) drop() {
	n := m.dropped.Add(1)
	metrics.IncOpActivityMirrorDropped()
	if m.warnDue(&m.lastDropWarn) {
		m.logger.Warn("registry: activity mirror queue full or stopped; dropping operation log lines from the "+
			"Activity Log (op_logs_v2 still has them)", "dropped_total", n, "queue_size", cap(m.queue))
	}
}

func (m *activityMirror) logFinalDrops() {
	if n := m.dropped.Load(); n > 0 {
		m.logger.Warn("registry: activity mirror stopped", "dropped_total", n)
	}
}

// warnDue reports whether a WARN of the kind tracked by last may be emitted
// now, and claims the slot if so.
func (m *activityMirror) warnDue(last *atomic.Int64) bool {
	now := time.Now().UnixNano()
	prev := last.Load()
	if prev != 0 && now-prev < int64(activityMirrorWarnEvery) {
		return false
	}
	return last.CompareAndSwap(prev, now)
}
