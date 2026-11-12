// file: core/kernel/stoporder_test.go
// version: 1.0.0
// guid: 059e27d6-41ec-4378-a91e-2addd27673e8
// last-edited: 2026-11-11

package kernel

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/coretest/logcapture"
	"github.com/falkcorp/mediacore/tasks"
)

// stopLog is the order in which a test's fakes were driven.
type stopLog struct{ recorder }

// fakeExecutor stands in for the executor: it embeds a nil tasks.Executor, so
// only Run and Shutdown may be called, the two the kernel's lifecycle uses.
type fakeExecutor struct {
	tasks.Executor
	log *stopLog
}

func (f fakeExecutor) Run(context.Context) error      { f.log.add("exec.run"); return nil }
func (f fakeExecutor) Shutdown(context.Context) error { f.log.add("exec.shutdown"); return nil }

// fakeScheduler stands in for the scheduler. Run blocks until the stop has
// taken effect; Stop returns at once and the goroutine ends a moment later,
// the way a real scheduler's loop does, so a kernel that does not wait for
// the goroutine reaches the executor first.
type fakeScheduler struct {
	log     *stopLog
	stopped chan struct{}
	runErr  error         // returned by Run, after runGate, instead of blocking
	runGate chan struct{} // closed by the test to let Run return runErr
	ran     chan struct{}
}

func newFakeScheduler(log *stopLog) *fakeScheduler {
	return &fakeScheduler{log: log, stopped: make(chan struct{}), ran: make(chan struct{})}
}

func (f *fakeScheduler) Members(string) []tasks.Kind { return nil }

func (f *fakeScheduler) Run(context.Context) error {
	close(f.ran)
	if f.runErr != nil {
		<-f.runGate
		return f.runErr
	}
	<-f.stopped
	time.Sleep(25 * time.Millisecond)
	f.log.add("sched.end")
	return nil
}

func (f *fakeScheduler) Stop(context.Context) error {
	f.log.add("sched.stop")
	close(f.stopped)
	return nil
}

// TestStopTasks_SchedulerStopsWaitsThenExecutorDrains (06 §6.5.2; 05-tasks
// 5T.4 step 4): the scheduler is asked to stop, its goroutine is awaited, and
// only then does the executor drain, so the scheduler fires nothing into a
// draining executor. The scheduler's goroutine outlives its Stop, so swapping
// the first two calls and dropping the wait are both caught.
func TestStopTasks_SchedulerStopsWaitsThenExecutorDrains(t *testing.T) {
	log := &stopLog{}
	sched := newFakeScheduler(log)
	k := &Kernel{tasks: &kernelTasks{exec: fakeExecutor{log: log}, sched: sched}}
	if err := k.startTasks(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-sched.ran
	if err := k.stopTasks(t.Context()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"exec.run", "sched.stop", "sched.end", "exec.shutdown"}; !slices.Equal(log.list(), want) {
		t.Errorf("order = %v, want %v", log.list(), want)
	}
	if err := k.stopTasks(t.Context()); err != nil || len(log.list()) != 4 {
		t.Errorf("a second stop = %v, order %v: want it a no-op", err, log.list())
	}
}

// TestStartTasks_RunAfterStopIsNotAnError: a stop that overtakes the
// scheduler's goroutine makes Scheduler.Run refuse to start. That is the
// shutdown, not a fault, so it is not logged at ERROR; the same error with no
// stop under way still is.
func TestStartTasks_RunAfterStopIsNotAnError(t *testing.T) {
	for _, tc := range []struct {
		name      string
		stop      bool
		wantError bool
	}{{"stop overtook the goroutine", true, false}, {"no stop", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			lc := logcapture.New(t)
			log := &stopLog{}
			sched := newFakeScheduler(log)
			sched.runErr, sched.runGate = errors.New("scheduler: Run after Stop"), make(chan struct{})
			k := &Kernel{cfg: Config{Logger: lc.Logger()}, tasks: &kernelTasks{exec: fakeExecutor{log: log}, sched: sched}}
			if err := k.startTasks(t.Context()); err != nil {
				t.Fatal(err)
			}
			if tc.stop {
				k.tasks.mu.Lock()
				k.tasks.stopped = true
				k.tasks.mu.Unlock()
			}
			close(sched.runGate) // Run returns its error now
			<-k.tasks.schedEnd
			var errs int
			for _, r := range lc.Records() {
				if r.Level >= slog.LevelError {
					errs++
				}
			}
			if (errs > 0) != tc.wantError {
				t.Errorf("ERROR records = %d, want one: %v", errs, tc.wantError)
			}
		})
	}
}

// TestRunCoreStops_AuditGetsItsOwnCappedSlice (05-health §5H.4): the audit
// writer stops after the tasks and under a deadline of its own, the lesser of
// auditStopSlice and what remains, so a writer that cannot drain never takes
// the whole budget from the workers and hooks that stop after it.
func TestRunCoreStops_AuditGetsItsOwnCappedSlice(t *testing.T) {
	deadlineOf := func(ctx context.Context) time.Time {
		d, ok := ctx.Deadline()
		if !ok {
			t.Error("a stop step got a context with no deadline")
		}
		return d
	}
	run := func(budget time.Duration) (taskDeadline, auditDeadline, deadline time.Time, order []string) {
		var td, ad atomic.Value
		var seq stopLog
		deadline = time.Now().Add(budget)
		err := runCoreStops(t.Context(), deadline,
			func(ctx context.Context) error { td.Store(deadlineOf(ctx)); seq.add("tasks"); return nil },
			func(ctx context.Context) error { ad.Store(deadlineOf(ctx)); seq.add("audit"); return nil })
		if err != nil {
			t.Fatal(err)
		}
		return td.Load().(time.Time), ad.Load().(time.Time), deadline, seq.list()
	}

	// A long budget: the tasks get all of it, the audit writer only its slice.
	td, ad, deadline, order := run(time.Hour)
	if !slices.Equal(order, []string{"tasks", "audit"}) {
		t.Errorf("order = %v, want tasks then audit", order)
	}
	if !td.Equal(deadline) {
		t.Errorf("tasks deadline = %v, want the budget's %v", td, deadline)
	}
	if left := time.Until(ad); left > auditStopSlice || left < auditStopSlice-time.Second {
		t.Errorf("audit deadline is %v away, want about auditStopSlice (%v), not the %v budget", left, auditStopSlice, time.Hour)
	}

	// A short budget: the audit writer never outlives it.
	_, ad, deadline, _ = run(time.Second)
	if ad.After(deadline) {
		t.Errorf("audit deadline %v is past the budget's %v", ad, deadline)
	}

	// An earlier Run cancellation does not stop either step's context.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var cancelled int
	_ = runCoreStops(ctx, time.Now().Add(time.Hour),
		func(c context.Context) error { cancelled += btoi(c.Err() != nil); return nil },
		func(c context.Context) error { cancelled += btoi(c.Err() != nil); return nil })
	if cancelled != 0 {
		t.Errorf("%d stop steps ran under an already-cancelled context; the stop order must outlive Run's cancellation", cancelled)
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// TestStartCoreServices_AuditWriterOutlivesRunsContext: Run's context ends
// when the process is told to stop, and the listener and the executor drain
// after that, still emitting. The writer is stopped by stopAudit alone, so a
// record emitted after the cancellation is written, not dropped.
func TestStartCoreServices_AuditWriterOutlivesRunsContext(t *testing.T) {
	db := bootAuditDB(t, auditOwnerSpec())
	s, err := buildAudit(t.Context(), db, slog.New(slog.DiscardHandler), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	k := &Kernel{audit: s}
	ctx, cancel := context.WithCancel(t.Context())
	if err := k.startCoreServices(ctx); err != nil {
		t.Fatal(err)
	}
	cancel() // SIGTERM
	// A writer bound to ctx notices on its own goroutine; give it the time, so
	// the record below arrives after it would have stopped.
	time.Sleep(100 * time.Millisecond)
	actor := audit.Actor{UserID: "alfred_pennyworth", Username: "Alfred Pennyworth"}
	s.Emit(t.Context(), audit.Event{Action: "core.auth.login", Outcome: audit.Failure, Actor: actor, Reason: "bad_password"})
	if err := k.stopCoreServices(t.Context(), time.Now().Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	var got int
	for _, err := range s.Query(t.Context(), audit.Filter{ActorUserID: "alfred_pennyworth"}) {
		if err != nil {
			t.Fatal(err)
		}
		got++
	}
	if got != 1 {
		t.Errorf("records emitted after Run's context ended = %d, want 1 written", got)
	}
}
