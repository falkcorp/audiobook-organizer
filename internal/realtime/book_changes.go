// file: internal/realtime/book_changes.go
// version: 1.0.0
// guid: 1e7b3d58-4a92-4c06-8f3d-9b2e6a0c5d71
// last-edited: 2026-09-27

package realtime

import (
	"sort"
	"sync"
	"time"
)

// EventBooksChanged tells clients which book rows changed so a list can patch
// those rows in place instead of reloading (owner request 2026-09-27: "why do
// we have to even refresh"). Data: {"kind": "created"|"updated"|"deleted",
// "ids": [...]}.
const EventBooksChanged EventType = "books.changed"

// BooksChangedMaxIDs bounds one event's id list; a larger batch is split
// across several events so a 10k-book operation never produces one huge frame.
const BooksChangedMaxIDs = 500

// BookChangeCoalescer batches per-book change notifications and broadcasts
// them at most once per interval per kind, deduplicated. Add is cheap and
// non-blocking (a map insert under a mutex), because it runs on the book
// writer's goroutine.
//
// A deleted id supersedes an updated one in the same window; a created id
// stays "created" even if it is also updated before the flush (a client
// treats it as new either way).
type BookChangeCoalescer struct {
	mu        sync.Mutex
	pending   map[string]map[string]struct{} // kind -> ids
	armed     bool
	interval  time.Duration
	broadcast func(*Event)
	afterFunc func(time.Duration, func()) // injectable for tests
	now       func() time.Time
}

// NewBookChangeCoalescer returns a coalescer that flushes interval after the
// first change of a window and hands each event to broadcast.
func NewBookChangeCoalescer(interval time.Duration, broadcast func(*Event)) *BookChangeCoalescer {
	return &BookChangeCoalescer{
		pending:   map[string]map[string]struct{}{},
		interval:  interval,
		broadcast: broadcast,
		afterFunc: func(d time.Duration, f func()) { time.AfterFunc(d, f) },
		now:       time.Now,
	}
}

// Add records that a book changed. kind is "created", "updated" or "deleted".
func (c *BookChangeCoalescer) Add(kind, bookID string) {
	if c == nil || bookID == "" {
		return
	}
	c.mu.Lock()
	switch kind {
	case "deleted":
		delete(c.pending["updated"], bookID)
		delete(c.pending["created"], bookID)
	case "updated":
		if _, created := c.pending["created"][bookID]; created {
			c.mu.Unlock()
			return
		}
		if _, deleted := c.pending["deleted"][bookID]; deleted {
			c.mu.Unlock()
			return
		}
	}
	set := c.pending[kind]
	if set == nil {
		set = map[string]struct{}{}
		c.pending[kind] = set
	}
	set[bookID] = struct{}{}
	arm := !c.armed
	c.armed = true
	c.mu.Unlock()
	if arm {
		c.afterFunc(c.interval, c.Flush)
	}
}

// Flush broadcasts everything pending now and re-opens the window.
func (c *BookChangeCoalescer) Flush() {
	c.mu.Lock()
	pending := c.pending
	c.pending = map[string]map[string]struct{}{}
	c.armed = false
	c.mu.Unlock()

	for _, kind := range []string{"deleted", "created", "updated"} {
		set := pending[kind]
		if len(set) == 0 {
			continue
		}
		ids := make([]string, 0, len(set))
		for id := range set {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for start := 0; start < len(ids); start += BooksChangedMaxIDs {
			end := min(start+BooksChangedMaxIDs, len(ids))
			c.broadcast(&Event{
				Type:      EventBooksChanged,
				Timestamp: c.now(),
				Data: map[string]any{
					"kind": kind,
					"ids":  ids[start:end],
				},
			})
		}
	}
}
