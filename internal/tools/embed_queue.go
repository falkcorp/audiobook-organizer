// file: internal/tools/embed_queue.go
// version: 1.0.0
// guid: b4c5d6e7-f8a9-0123-bcde-123456789012
// last-edited: 2026-06-15

package tools

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// EmbedQueueConfig holds constructor parameters for EmbedQueue.
type EmbedQueueConfig struct {
	Capacity      int
	DrainFn       func(ctx context.Context) error
	Debounce      time.Duration
	AllowPeriodic bool
}

// EmbedQueue buffers book IDs pending embedding and drains them via DrainFn.
// When AllowPeriodic is true, each Enqueue resets a debounce timer that fires
// DrainNow when it expires.
type EmbedQueue struct {
	cfg      EmbedQueueConfig
	ch       chan string
	mu       sync.Mutex
	timer    *time.Timer
	bgCtx    context.Context
	bgCancel context.CancelFunc
}

// NewEmbedQueue creates an EmbedQueue. Call Start to activate the debounce timer.
func NewEmbedQueue(cfg EmbedQueueConfig) *EmbedQueue {
	if cfg.Capacity <= 0 {
		cfg.Capacity = 10_000
	}
	return &EmbedQueue{
		cfg: cfg,
		ch:  make(chan string, cfg.Capacity),
	}
}

// Start activates the debounce timer goroutine.
func (q *EmbedQueue) Start(ctx context.Context) {
	q.bgCtx, q.bgCancel = context.WithCancel(ctx)
	if q.cfg.AllowPeriodic {
		go q.timerLoop()
	}
}

// Stop cancels the background goroutine.
func (q *EmbedQueue) Stop() {
	if q.bgCancel != nil {
		q.bgCancel()
	}
}

// Enqueue adds bookID to the queue. Non-blocking: drops with a log warning if full.
func (q *EmbedQueue) Enqueue(bookID string) {
	select {
	case q.ch <- bookID:
	default:
		slog.Warn("embed_queue: full, dropping book", "book_id", bookID, "capacity", cap(q.ch))
		return
	}
	if q.cfg.AllowPeriodic {
		q.mu.Lock()
		if q.timer != nil {
			q.timer.Reset(q.cfg.Debounce)
		}
		q.mu.Unlock()
	}
}

// DrainNow immediately invokes DrainFn.
func (q *EmbedQueue) DrainNow(ctx context.Context) error {
	q.mu.Lock()
	if q.timer != nil {
		q.timer.Stop()
	}
	q.mu.Unlock()
	return q.cfg.DrainFn(ctx)
}

// Len returns the number of items currently in the queue.
func (q *EmbedQueue) Len() int { return len(q.ch) }

func (q *EmbedQueue) timerLoop() {
	q.mu.Lock()
	q.timer = time.NewTimer(q.cfg.Debounce)
	q.mu.Unlock()

	for {
		select {
		case <-q.bgCtx.Done():
			q.mu.Lock()
			if q.timer != nil {
				q.timer.Stop()
			}
			q.mu.Unlock()
			return
		case <-q.timer.C:
			if len(q.ch) > 0 {
				if err := q.cfg.DrainFn(q.bgCtx); err != nil {
					slog.Error("embed_queue: drain failed", "err", err)
				}
			}
			q.mu.Lock()
			q.timer.Reset(q.cfg.Debounce)
			q.mu.Unlock()
		}
	}
}
