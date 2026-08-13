// file: internal/errhandling/skipcounter.go
// version: 1.0.0
// guid: ed47a1ef-7722-4c13-8cc7-48e09d29d4ae
// last-edited: 2026-08-11

package errhandling

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
)

// SkipCounter records how many items a loop skipped and why, and emits exactly
// one summary log line at loop exit.
//
// It exists for bucket (d) of the silent-failure audit: 78 loops that
// `continue` on error with no counter and no log. The defining defect is not
// that the item was skipped — skipping is often correct — it is that the loop
// reports "processed 4,812 books" and that number is indistinguishable from
// "processed 4,812 books and silently skipped 300."
//
// A SkipCounter makes the denominator honest:
//
//	skips := errhandling.NewSkipCounter("auto-organize")
//	defer skips.LogSummary(ctx)
//
//	for i := range books {
//	    dbBook, err := store.GetBookByFilePath(books[i].FilePath)
//	    if err != nil {
//	        skips.Skip("store lookup failed", err)
//	        continue
//	    }
//	    if dbBook == nil {
//	        skips.Skip("book not in store", nil)
//	        continue
//	    }
//	    // ... do the work ...
//	    skips.Processed()
//	}
//
// Note the two Skip calls. Splitting "the lookup broke" from "the row isn't
// there" is the point: they need opposite responses, and the audit's anchor
// site collapses them into one branch.
//
// # Reasons
//
// reason must be a small, bounded set of literal strings — it is a grouping
// key, not a message. Do not interpolate an item ID into it; pass the ID as a
// field on the error or leave it to the per-reason sample.
//
// # Concurrency
//
// A SkipCounter is safe for concurrent use, so it can be shared across a
// bounded worker pool (see the concurrency rules in CLAUDE.md) without extra
// locking. The zero value is not usable; call [NewSkipCounter].
type SkipCounter struct {
	// name identifies the loop in the summary log, e.g. "auto-organize".
	name string

	mu        sync.Mutex
	processed int
	byReason  map[string]int
	// firstErr keeps one representative error per reason. Logging every
	// error would reproduce the 300-log-lines problem; logging none would
	// lose the ability to diagnose. One sample per reason is the compromise.
	firstErr map[string]error
	// order preserves first-seen order of reasons for stable output when
	// counts tie.
	order []string
}

// NewSkipCounter returns a SkipCounter for a loop identified by name. name
// appears in the summary log and should describe the loop, not the package.
func NewSkipCounter(name string) *SkipCounter {
	return &SkipCounter{
		name:     name,
		byReason: make(map[string]int),
		firstErr: make(map[string]error),
	}
}

// Skip records that one item was skipped for the given reason. err may be nil
// when the skip is not caused by an error (a legitimately absent row, say);
// the reason is what gets counted either way.
func (c *SkipCounter) Skip(reason string, err error) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, seen := c.byReason[reason]; !seen {
		c.order = append(c.order, reason)
	}
	c.byReason[reason]++
	if err != nil {
		if _, have := c.firstErr[reason]; !have {
			c.firstErr[reason] = err
		}
	}
}

// Processed records that one item was handled successfully. Calling it is what
// makes the summary's ratio meaningful; a loop that only calls Skip still
// reports a correct skipped count but a processed count of zero.
func (c *SkipCounter) Processed() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.processed++
	c.mu.Unlock()
}

// Add records n successfully processed items at once, for loops that batch.
func (c *SkipCounter) Add(n int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.processed += n
	c.mu.Unlock()
}

// Skipped returns the total number of skipped items across all reasons.
func (c *SkipCounter) Skipped() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, v := range c.byReason {
		n += v
	}
	return n
}

// ProcessedCount returns the number of successfully processed items.
func (c *SkipCounter) ProcessedCount() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.processed
}

// ByReason returns a copy of the per-reason skip counts.
func (c *SkipCounter) ByReason() map[string]int {
	if c == nil {
		return map[string]int{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int, len(c.byReason))
	for k, v := range c.byReason {
		out[k] = v
	}
	return out
}

// LogSummary emits exactly one log line describing the loop's outcome. Call it
// once, at loop exit — `defer skips.LogSummary(ctx)` immediately after
// [NewSkipCounter] is the intended usage.
//
// Level is INFO when nothing was skipped and WARN when anything was, so a
// silently-shrunk denominator is visible at default log levels rather than
// buried at DEBUG.
func (c *SkipCounter) LogSummary(ctx context.Context) {
	if c == nil {
		return
	}
	c.mu.Lock()
	processed := c.processed
	reasons := make([]string, len(c.order))
	copy(reasons, c.order)
	counts := make(map[string]int, len(c.byReason))
	for k, v := range c.byReason {
		counts[k] = v
	}
	samples := make(map[string]error, len(c.firstErr))
	for k, v := range c.firstErr {
		samples[k] = v
	}
	c.mu.Unlock()

	skipped := 0
	for _, v := range counts {
		skipped += v
	}

	// Highest count first; first-seen order breaks ties so output is stable.
	pos := make(map[string]int, len(reasons))
	for i, r := range reasons {
		pos[r] = i
	}
	sort.SliceStable(reasons, func(i, j int) bool {
		if counts[reasons[i]] != counts[reasons[j]] {
			return counts[reasons[i]] > counts[reasons[j]]
		}
		return pos[reasons[i]] < pos[reasons[j]]
	})

	attrs := []slog.Attr{
		slog.String("loop", c.name),
		slog.Int("processed", processed),
		slog.Int("skipped", skipped),
		slog.Int("total", processed+skipped),
	}
	for _, r := range reasons {
		attrs = append(attrs, slog.Int("skipped."+r, counts[r]))
		if err := samples[r]; err != nil {
			attrs = append(attrs, slog.String("sample."+r, err.Error()))
		}
	}

	level := slog.LevelInfo
	msg := fmt.Sprintf("%s complete: %d processed, %d skipped", c.name, processed, skipped)
	if skipped > 0 {
		level = slog.LevelWarn
	}
	activeLogger().LogAttrs(ctx, level, msg, attrs...)
}
