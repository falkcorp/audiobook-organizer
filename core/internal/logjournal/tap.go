// file: core/internal/logjournal/tap.go
// version: 1.0.0
// guid: b3c8f4a2-1d76-4e59-9a0b-6f2e8d1c7a34
// last-edited: 2026-11-13

package logjournal

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/log"
)

// TapName is the journal tap's name: its drops are
// log.Counters.Dropped[TapName], which the metrics bridge exports as
// mediacore_log_dropped_total{sink="journal"} (05-logging §9).
const TapName = "journal"

// Batching bounds (05-logging §5): the tap writes when 256 records are pending
// or one second after the first of them arrived, whichever comes first, so a
// busy process commits in groups and a quiet one still lands a record within a
// second.
const (
	// BatchRecords is the most records one Append carries.
	BatchRecords = 256
	// BatchDelay is the longest a record waits before it is written.
	BatchDelay = time.Second
	// minQueue is core.log.journal.queue_size's schema minimum.
	minQueue = 64
	// writeTimeout bounds one write that has no caller context to bound it.
	writeTimeout = 30 * time.Second
)

// TapOptions are the knobs tests turn; zero values are the contract's.
type TapOptions struct {
	// MaxRecords is the batch size bound (BatchRecords when zero).
	MaxRecords int
	// Delay is the batch age bound (BatchDelay when zero).
	Delay time.Duration
}

// TapCounters are the tap's own counters, the source of the metrics bridge's
// mediacore_log_journal_write_errors_total and mediacore_log_journal_lag_seconds.
type TapCounters struct {
	// WriteErrors counts batches the store refused. Those records are lost: the
	// journal fails open, so a broken store never blocks or breaks logging.
	WriteErrors uint64
	// Lag is how long the oldest record not yet durably written has been
	// waiting; 0 when nothing is pending.
	Lag time.Duration
	// Degraded is true from a failed write until the next successful one.
	Degraded bool
	// Pending is the number of records waiting for their batch.
	Pending int
}

// Tap feeds the journal from a log.Logging: it registers a log tap under
// TapName that gathers records into batches and appends each batch to the
// store. It is safe for concurrent use.
type Tap struct {
	store log.JournalStore
	max   int
	delay time.Duration

	// flushMu serialises writes and keeps them in batch order: a flush takes it
	// before it takes the pending records.
	flushMu sync.Mutex

	mu       sync.Mutex
	pending  []log.Record
	oldest   time.Time // arrival of pending[0]
	inflight time.Time // arrival of the oldest record of the batch being written, or zero
	timer    *time.Timer
	stopped  bool

	writeErrors atomic.Uint64
	degraded    atomic.Bool
	stopTap     func()
}

// StartTap starts the journal tap on l and returns it. With s.Enabled false
// nothing is registered and the returned Tap is inert (its counters read zero),
// so the caller needs no branch. s.MinLevel must be debug, info, warn or error.
//
// Shutdown order: l.Close drains the tap's queue through the batcher, then
// Flush writes what the batcher still holds. Tap.Stop discards the queue the
// way log's stop does, so it is for tearing a tap down, not for shutdown.
func StartTap(l *log.Logging, store log.JournalStore, s Settings, opts ...TapOptions) (*Tap, error) {
	t := &Tap{store: store, max: BatchRecords, delay: BatchDelay, stopTap: func() {}}
	for _, o := range opts {
		if o.MaxRecords > 0 {
			t.max = o.MaxRecords
		}
		if o.Delay > 0 {
			t.delay = o.Delay
		}
	}
	level, err := ParseLevel(s.MinLevel)
	if err != nil {
		return nil, err
	}
	if !s.Enabled {
		return t, nil
	}
	queue := max(s.QueueSize, minQueue)
	t.stopTap = l.Tap(TapName, level, queue, func(_ context.Context, r log.Record) { t.offer(r) })
	return t, nil
}

// ParseLevel is the slog level of a core.log.journal.min_level value.
func ParseLevel(s string) (slog.Level, error) {
	switch s {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("logjournal: min_level %q is not debug, info, warn or error", s)
}

// offer takes one record from the log tap. Reaching the size bound it writes
// inline, which holds the log tap's goroutine and so lets its queue fill and
// drop (counted) rather than letting the batcher grow without bound.
func (t *Tap) offer(r log.Record) {
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		return
	}
	t.pending = append(t.pending, r)
	full := len(t.pending) >= t.max
	if len(t.pending) == 1 {
		t.oldest = time.Now()
		if !full {
			t.arm()
		}
	}
	t.mu.Unlock()
	if full {
		t.flushInBackground()
	}
}

// arm starts the age timer. t.mu is held.
func (t *Tap) arm() {
	if t.timer == nil {
		t.timer = time.AfterFunc(t.delay, t.flushInBackground)
		return
	}
	t.timer.Reset(t.delay)
}

// flushInBackground is flush for the callers that have no one to return to (the
// size bound and the age timer). A failed write is already counted in
// WriteErrors and has set Degraded, which is how the health token and the
// metric report it; it is not logged, because a record logged here would
// re-enter this tap.
func (t *Tap) flushInBackground() {
	if err := t.flush(context.Background()); err != nil {
		return
	}
}

// flush writes the pending records as one batch. A store error drops the batch
// and counts it; ctx, when not already bounded, gets writeTimeout.
func (t *Tap) flush(ctx context.Context) error {
	t.flushMu.Lock()
	defer t.flushMu.Unlock()
	t.mu.Lock()
	batch := t.pending
	t.pending = nil
	if t.timer != nil {
		t.timer.Stop()
	}
	if len(batch) > 0 {
		t.inflight = t.oldest
	}
	t.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, writeTimeout)
		defer cancel()
	}
	err := t.store.Append(ctx, batch)
	t.mu.Lock()
	t.inflight = time.Time{}
	t.mu.Unlock()
	if err != nil {
		t.writeErrors.Add(1)
		t.degraded.Store(true)
		return err
	}
	t.degraded.Store(false)
	return nil
}

// Flush writes whatever the batcher holds, now, and returns the store's error
// if that write failed (the batch is dropped either way). Call it after the
// logger's Close has drained the tap's queue.
func (t *Tap) Flush(ctx context.Context) error { return t.flush(ctx) }

// Stop removes the tap from the logger (its queued records are discarded, as
// log's stop does), refuses further records and writes what the batcher holds.
func (t *Tap) Stop(ctx context.Context) error {
	t.stopTap()
	t.mu.Lock()
	t.stopped = true
	t.mu.Unlock()
	return t.flush(ctx)
}

// Counters is the tap's current counters.
func (t *Tap) Counters() TapCounters {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := TapCounters{
		WriteErrors: t.writeErrors.Load(),
		Degraded:    t.degraded.Load(),
		Pending:     len(t.pending),
	}
	var since time.Time
	switch {
	case !t.inflight.IsZero():
		since = t.inflight
	case len(t.pending) > 0:
		since = t.oldest
	}
	if !since.IsZero() {
		c.Lag = max(time.Since(since), 0)
	}
	return c
}
