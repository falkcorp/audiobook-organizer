// file: pkg/plugin/sdk/progress.go
// version: 1.1.0
// guid: 7b3a9c1e-2d4f-4e6a-8b9c-1a2b3c4d5e6f
// last-edited: 2026-11-18

package sdk

import "fmt"

// Progress is a thin wrapper around Reporter that:
//
//  1. Always uses real (current, total) counts — never the lying
//     `(pct, 100)` pattern.
//  2. Reserves a Start step and a Done step so the bar always advances
//     at least twice even when N == 0. This means UIs never see `0/0`
//     and the percentage is always derivable.
//  3. Formats the message uniformly:
//     "<verb> <i>/<N> (<extra>) (<pct>%)"
//
// Step model: total = N + 2.
//
//	step 0           "Starting …"           (0,   N+2)
//	step 1..N        per-item work          (i,   N+2)   1 <= i <= N
//	step N+1         "Finalizing …"         (N+1, N+2)
//	step N+2         "Done"                 (N+2, N+2)
type Progress struct {
	r     ProgressSink
	n     int // number of real units of work
	total int // n + 2
	cur   int // most recently reported current
}

// ProgressSink is the one Reporter method Progress uses. Taking it rather than
// the whole Reporter lets a caller that holds only a progress callback (a native
// task's RC) use the helper too; every Reporter satisfies it.
type ProgressSink interface {
	UpdateProgress(current, total int, message string) error
}

// NewProgress returns a helper that reserves a start + n + finalize + done
// step schedule. Negative n is clamped to 0, so the worst case still has
// total == 2 — never 0/0.
func NewProgress(r ProgressSink, n int) *Progress {
	if n < 0 {
		n = 0
	}
	return &Progress{r: r, n: n, total: n + 2}
}

// Start emits the initial "starting" frame at (0, total).
func (p *Progress) Start(message string) {
	if p == nil || p.r == nil {
		return
	}
	p.cur = 0
	_ = p.r.UpdateProgress(0, p.total, p.format(0, message))
}

// Step advances the cursor by one and reports (cur, total). Use this when
// you're iterating through the N units of work in order.
func (p *Progress) Step(message string) {
	if p == nil || p.r == nil {
		return
	}
	p.cur++
	if p.cur > p.n+1 {
		p.cur = p.n + 1
	}
	_ = p.r.UpdateProgress(p.cur, p.total, p.format(p.cur, message))
}

// StepN jumps directly to the i-th unit of N. Useful when callbacks come
// with their own (processed, total) pair from a deeper loop (e.g. Pebble
// bulk operations). i is 1-based; passing i == 0 reports the Start frame.
func (p *Progress) StepN(i int, message string) {
	if p == nil || p.r == nil {
		return
	}
	if i < 0 {
		i = 0
	}
	if i > p.n {
		i = p.n
	}
	p.cur = i
	_ = p.r.UpdateProgress(i, p.total, p.format(i, message))
}

// Finalize emits the penultimate frame at (total-1, total) — the cleanup
// or "writing results" phase.
func (p *Progress) Finalize(message string) {
	if p == nil || p.r == nil {
		return
	}
	p.cur = p.total - 1
	_ = p.r.UpdateProgress(p.cur, p.total, p.format(p.cur, message))
}

// Done emits the final frame at (total, total).
func (p *Progress) Done(message string) {
	if p == nil || p.r == nil {
		return
	}
	p.cur = p.total
	_ = p.r.UpdateProgress(p.total, p.total, p.format(p.total, message))
}

// N returns the original unit count (excluding start/finalize/done).
func (p *Progress) N() int { return p.n }

// Total returns the wire total (N + 2).
func (p *Progress) Total() int { return p.total }

// format appends a "(pct%)" suffix to the supplied message. We compute the
// percentage from cur/total so the displayed percent always agrees with
// what the UI would render from the same (current, total) tuple.
func (p *Progress) format(cur int, msg string) string {
	if p.total <= 0 {
		return msg
	}
	pct := float64(cur) / float64(p.total) * 100
	// For large jobs show two decimals so 1088/308857 doesn't read 0.00%.
	if p.n >= 100 {
		return fmt.Sprintf("%s (%.2f%%)", msg, pct)
	}
	return fmt.Sprintf("%s (%.0f%%)", msg, pct)
}
