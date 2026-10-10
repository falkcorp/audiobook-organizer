// file: internal/plugins/maintenance/optimize_watchdog_test.go
// version: 1.0.1
// guid: 5e1f8c3a-9b27-4d60-a4c8-3d6b2f9e7a15
// last-edited: 2026-10-09

package maintenance

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/childop"
	"github.com/falkcorp/audiobook-organizer/internal/serverdecode"
)

// simClock is simulated wall time: every read of a child row is one minute.
// The registry watchdog compares wall time against the last UpdateProgress,
// so a sweep is safe exactly when the longest simulated gap between its
// progress reports stays under its ProgressTimeout.
type simClock struct {
	mu  sync.Mutex
	now time.Duration
}

func (c *simClock) tick() {
	c.mu.Lock()
	c.now += time.Minute
	c.mu.Unlock()
}

func (c *simClock) read() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// childScript plays one child's life: queued, then running with progress
// that moves on every read (or stands still when stalled), then completed.
type childScript struct {
	clock   *simClock
	queued  int // minutes queued
	running int // minutes running
	stalled bool
	reads   int
}

func (s *childScript) GetOperationV2(string) (*database.OperationV2Row, error) {
	s.clock.tick()
	i := s.reads
	s.reads++
	switch {
	case i < s.queued:
		return &database.OperationV2Row{Status: "queued"}, nil
	case i < s.queued+s.running:
		cur := i - s.queued + 1
		if s.stalled {
			cur = 1
		}
		return &database.OperationV2Row{
			Status: "running", ProgressCurrent: cur, ProgressTotal: s.running,
			ProgressMessage: fmt.Sprintf("fingerprinted %d", cur),
		}, nil
	default:
		return &database.OperationV2Row{Status: "completed"}, nil
	}
}

// followDeps runs the production follower (childop.Follow) against a scripted
// child per enqueue. relay=false reproduces the pre-fix wait, which dropped
// the child's progress on the floor.
type followDeps struct {
	fakeDeps
	clock   *simClock
	queued  int
	running int
	stalled bool
	relay   bool
	scripts map[string]*childScript
}

func (d *followDeps) EnqueueOp(_ context.Context, defID string, _ any) (string, error) {
	if d.scripts == nil {
		d.scripts = map[string]*childScript{}
	}
	d.scripts[defID] = &childScript{clock: d.clock, queued: d.queued, running: d.running, stalled: d.stalled}
	return defID, nil
}

func (d *followDeps) WaitForOp(ctx context.Context, opID string, onObserve func(childop.Observation)) error {
	if !d.relay {
		onObserve = nil
	}
	row, err := childop.Follow(ctx, d.scripts[opID], opID, childop.Options{
		Interval: time.Microsecond, OnObserve: onObserve,
	})
	if err != nil {
		return err
	}
	if row.Status != "completed" {
		return fmt.Errorf("child %s ended %s", opID, row.Status)
	}
	return nil
}

// gapReporter records the simulated time of every progress report.
type gapReporter struct {
	fakeReporter
	clock *simClock
	mu    sync.Mutex
	at    []time.Duration
	msgs  []string
}

func (r *gapReporter) UpdateProgress(_, _ int, msg string) error {
	r.mu.Lock()
	r.at = append(r.at, r.clock.read())
	r.msgs = append(r.msgs, msg)
	r.mu.Unlock()
	return nil
}

func (r *gapReporter) maxGap() time.Duration {
	var gap, last time.Duration
	for _, t := range r.at {
		gap = max(gap, t-last)
		last = t
	}
	return max(gap, r.clock.read()-last)
}

func runOptimizeSim(t *testing.T, d *followDeps) *gapReporter {
	t.Helper()
	t.Setenv(serverdecode.EnvVar, "1") // the sweep only has 4 children when decoding is allowed
	prev := config.AppConfig.Maintenance.AcoustIDBackfill
	config.AppConfig.Maintenance.AcoustIDBackfill = true
	t.Cleanup(func() { config.AppConfig.Maintenance.AcoustIDBackfill = prev })
	rep := &gapReporter{clock: d.clock}
	if err := New(d).runOptimize(context.Background(), nil, rep); err != nil {
		t.Fatalf("runOptimize: %v", err)
	}
	return rep
}

// Each child waits 15 simulated minutes in the queue and then runs for 90 --
// far past the 5-minute default watchdog -- and the sweep must report often
// enough that its watchdog never fires.
func TestOptimize_LongRunningChildDoesNotTripWatchdog(t *testing.T) {
	d := &followDeps{clock: &simClock{}, queued: 15, running: 90, relay: true}
	rep := runOptimizeSim(t, d)

	if len(d.scripts) != 4 {
		t.Fatalf("want 4 children run, got %d", len(d.scripts))
	}
	budget := New(d).optimizeDef().ProgressTimeout
	if gap := rep.maxGap(); gap >= 5*time.Minute || gap >= budget {
		t.Fatalf("longest silence %s would trip the watchdog (default 5m, def %s)", gap, budget)
	}
	var sawQueued, sawChild bool
	for _, m := range rep.msgs {
		sawQueued = sawQueued || strings.Contains(m, "queued behind other work")
		sawChild = sawChild || strings.Contains(m, "fingerprinted 42")
	}
	if !sawQueued || !sawChild {
		t.Fatalf("progress should carry the queued wait and the child's own message; queued=%v child=%v", sawQueued, sawChild)
	}
}

// The control: the same run through the pre-fix wait goes silent for a whole
// child, which is what got the sweep reaped in production.
func TestOptimize_WithoutRelayTheSweepGoesSilent(t *testing.T) {
	d := &followDeps{clock: &simClock{}, queued: 15, running: 90, relay: false}
	rep := runOptimizeSim(t, d)
	if gap := rep.maxGap(); gap < 100*time.Minute {
		t.Fatalf("control should be silent for a whole child (~105m), got %s", gap)
	}
}

// Relaying observed progress must not keep the sweep alive behind a wedged
// child: a running child whose row stops changing makes the sweep quiet too,
// so both watchdogs can see it.
func TestOptimize_StalledChildIsNotMasked(t *testing.T) {
	d := &followDeps{clock: &simClock{}, running: 60, stalled: true, relay: true}
	rep := runOptimizeSim(t, d)
	if gap := rep.maxGap(); gap < 55*time.Minute {
		t.Fatalf("a stalled child must leave the sweep silent, longest gap was only %s", gap)
	}
}

// The sweep can relay only what a child writes, so its budget must cover the
// longest silence a child may keep. temp-file-cleanup is LivenessNone.
func TestOptimize_ProgressTimeoutCoversChildBudgets(t *testing.T) {
	p := New(fakeDeps{})
	sweep := p.optimizeDef().ProgressTimeout
	if cleanup := p.tempFileCleanupDef().ProgressTimeout; sweep <= cleanup {
		t.Fatalf("sweep ProgressTimeout %s must exceed temp-file-cleanup's %s", sweep, cleanup)
	}
}
