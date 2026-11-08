// file: internal/server/stophook_budget_test.go
// version: 1.0.1
// guid: 877b968e-c9c6-4cac-b2e9-06bc892394d0
// last-edited: 2026-11-08

package server

import (
	"context"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// wedgedStopper is a container service whose Stop ignores its context and
// blocks until the test lets it go, the way the activity Writer's or the
// search index's Stop would on a stuck disk.
type wedgedStopper struct{ release chan struct{} }

func (w wedgedStopper) Stop(context.Context) error {
	<-w.release
	return nil
}

// TestStopHook_WedgedStepsStayWithinTheBudget (R1-15 review m2): a stop step
// whose API takes no deadline (the file I/O pool, the container's Stoppers,
// the scheduler's tasks) cannot hold StopHook past the kernel's shutdown
// budget. Each case wedges one step on a channel the test closes only in
// Cleanup. StopHook must return by the deadline (plus scheduling slack) and
// close Stopped, where it used to wait for the step however long it took.
func TestStopHook_WedgedStepsStayWithinTheBudget(t *testing.T) {
	const budget = 400 * time.Millisecond
	cases := []struct {
		name  string
		wedge func(t *testing.T, s *Server, release chan struct{})
	}{
		{"file I/O pool", func(t *testing.T, s *Server, release chan struct{}) {
			p := NewFileIOPool(1)
			started := make(chan struct{})
			p.SubmitTyped("arkham-transfer", "hold", func() {
				close(started)
				<-release
			})
			<-started
			s.fileIOPool = p
		}},
		{"container", func(t *testing.T, s *Server, release chan struct{}) {
			c := serviceregistry.NewContainerFrom().Override("chromemstore", wedgedStopper{release: release})
			if err := c.Build(context.Background()); err != nil {
				t.Fatal(err)
			}
			s.container = c
		}},
		{"scheduler tasks", func(t *testing.T, s *Server, release chan struct{}) {
			s.life.backgroundWG.Go(func() { <-release })
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{stopped: make(chan struct{})}
			release := make(chan struct{})
			tc.wedge(t, s, release)
			t.Cleanup(func() { close(release) })

			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			start := time.Now()
			if err := s.StopHook(ctx); err != nil {
				t.Fatalf("StopHook: %v", err)
			}
			if took := time.Since(start); took > budget+time.Second {
				t.Errorf("StopHook took %s with the %s step wedged; the budget was %s", took, tc.name, budget)
			}
			select {
			case <-s.Stopped():
			default:
				t.Error("StopHook returned without closing Stopped")
			}
		})
	}
}

// TestStopWithin_ReportsWhetherTheStepFinished pins the helper's contract: a
// step that returns in time is reported finished, and one still running at
// its share of the budget is reported unfinished as soon as the share ends.
func TestStopWithin_ReportsWhetherTheStepFinished(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if !stopWithin(ctx, time.Second, 0, "quick", func() {}) {
		t.Error("a step that returned at once was reported unfinished")
	}

	release := make(chan struct{})
	defer close(release)
	short, cancelShort := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancelShort()
	start := time.Now()
	if stopWithin(short, time.Minute, 200*time.Millisecond, "wedged", func() { <-release }) {
		t.Error("a wedged step was reported finished")
	}
	if took := time.Since(start); took > 250*time.Millisecond {
		t.Errorf("stopWithin waited %s; its share was the 300ms budget less a 200ms reserve", took)
	}
}
