// file: core/outbox/health.go
// version: 1.1.0
// guid: 5f6a4c2c-9f56-4a53-8c5f-0f3c6c1a6a40
// last-edited: 2026-11-15

package outbox

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/health"
)

// The reasons core.outbox reports Degraded with (05-events §5E.5.2, §5E.5.3).
const (
	reasonDeadLetters   = "dead_letters"
	reasonRelayDisabled = "relay_disabled"
	reasonStopped       = "delivery_stopped"
)

// healthState folds what the relay knows into the core.outbox component's one
// Result: Degraded while a dead letter exists, while the relay is disabled by
// its dial, and while a topic or a (consumer, partition) is stopped on a row
// it cannot read.
type healthState struct {
	rep health.Reporter
	now func() time.Time

	mu       sync.Mutex
	last     time.Time // the At of the last Result: a later one never carries an earlier time
	disabled bool
	dead     map[string]int    // consumer -> dead letters
	stopped  map[string]string // subject -> why
}

func newHealthState(rep health.Reporter, now func() time.Time) *healthState {
	return &healthState{rep: rep, now: now, dead: map[string]int{}, stopped: map[string]string{}}
}

func (h *healthState) setDisabled(v bool) {
	h.mu.Lock()
	h.disabled = v
	h.mu.Unlock()
}

// setDead replaces the per-consumer dead letter counts.
func (h *healthState) setDead(counts map[string]int) {
	h.mu.Lock()
	h.dead = counts
	h.mu.Unlock()
}

// stop records that subject (a topic or a pair) stopped, and why; a repeated
// stop keeps its first reason. The first one reports at once: an operator
// should not wait for the next status refresh to learn that delivery stopped.
func (h *healthState) stop(subject, why string) (first bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.stopped[subject]; ok {
		return false
	}
	h.stopped[subject] = why
	h.reportLocked(h.now())
	return true
}

// clear forgets a stop once the row is readable again, and reports at once.
func (h *healthState) clear(subject string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, was := h.stopped[subject]; was {
		delete(h.stopped, subject)
		h.reportLocked(h.now())
	}
}

// report computes the Result from the current state and pushes it.
func (h *healthState) report(now time.Time) {
	if h == nil || h.rep == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reportLocked(now)
}

// reportLocked is report with mu held. now is raised to the last report's time
// if it is earlier, so the Results are ordered by At as well as by arrival.
func (h *healthState) reportLocked(now time.Time) {
	if h.rep == nil {
		return
	}
	if now.Before(h.last) {
		now = h.last
	}
	h.last = now
	res := health.Result{Status: health.StatusUp, At: now}
	var parts []string
	total := 0
	for _, n := range h.dead {
		total += n
	}
	if total > 0 {
		res.Reason = reasonDeadLetters
		parts = append(parts, fmt.Sprintf("%d dead letters across %d consumers", total, len(h.dead)))
	}
	if len(h.stopped) > 0 {
		subjects := make([]string, 0, len(h.stopped))
		for s, why := range h.stopped {
			subjects = append(subjects, s+": "+why)
		}
		slices.Sort(subjects)
		res.Reason = reasonStopped
		parts = append(parts, "delivery stopped: "+strings.Join(subjects, "; "))
	}
	if h.disabled {
		res.Reason = reasonRelayDisabled
		parts = append(parts, "the relay is turned off (core.outbox.relay_enabled)")
	}
	if res.Reason != "" {
		res.Status = health.StatusDegraded
		res.Detail = strings.Join(parts, "; ")
	}
	h.rep.Report(res)
}
