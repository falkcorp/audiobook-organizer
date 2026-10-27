// file: internal/operations/registry/continue_in_place_test.go
// version: 1.1.0
// guid: 4d1c8a5e-6b3f-4e7a-9c20-1f5a8b7d3e64
// last-edited: 2026-10-26

package registry_test

// ErrContinueInPlace: a resumable in-process run too big for one Timeout yields
// before its deadline and the registry restarts the SAME row. A deadline that
// fires, a user cancel and a watchdog kill keep the status they always had, even
// when the run's own error is the sentinel.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
)

// A run that yields once is re-queued under its own id, gets a fresh deadline,
// finishes, and was never counted as a timeout, a cancel or a failure.
func TestContinueInPlace_YieldRequeuesTheSameRow(t *testing.T) {
	bus := &t06TermBus{}
	g, store := newV3OtelRig(t, registry.Options{Bus: bus})
	const id = "test.continue-in-place"
	def := makeValidDef(id)
	def.ResumePolicy = registry.ResumeRestart
	def.Timeout = time.Hour
	var attempts atomic.Int32
	deadlines := make(chan time.Time, 4)
	def.Run = func(ctx context.Context, _ json.RawMessage, rep registry.Reporter) error {
		dl, _ := ctx.Deadline()
		deadlines <- dl
		if attempts.Add(1) == 1 {
			for i := 1; i <= 4; i++ {
				_ = rep.UpdateProgress(i, 10, "")
			}
			return fmt.Errorf("batch done, more to do: %w", registry.ErrContinueInPlace)
		}
		// A resumed run that skips its finished rows reports the position it
		// inherited first, then goes on from it.
		for i := 4; i <= 10; i++ {
			_ = rep.UpdateProgress(i, 10, "")
		}
		return nil
	}
	if err := g.reg.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	g.reg.Start(t.Context())
	opID, err := g.reg.EnqueueOp(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	awaitV2(t, store, opID, "completed")
	awaitV3State(t, store, opID, state.V3Succeeded)

	if got := attempts.Load(); got != 2 {
		t.Fatalf("Run was called %d times, want 2 (the yield, then the resumed attempt)", got)
	}
	first, second := <-deadlines, <-deadlines
	if !second.After(first) {
		t.Errorf("the resumed attempt's deadline %v is not after the first's %v: it did not get a fresh Timeout", second, first)
	}
	row, err := store.GetOperationV2(opID)
	if err != nil || row == nil {
		t.Fatalf("GetOperationV2: %v %v", row, err)
	}
	if row.ResumeCount != 1 {
		t.Errorf("resume_count = %d, want 1", row.ResumeCount)
	}
	ev := eventNames(t, store, opID)
	if strings.Contains(ev, "v2_write") {
		t.Errorf("events = %q: the yield or its resume was an off-table v2_write", ev)
	}
	if !strings.Contains(ev, "dispatch stop_requested goroutine_returned resume dispatch return_nil") {
		t.Errorf("events = %q, want ... dispatch stop_requested goroutine_returned resume dispatch return_nil", ev)
	}
	rec := runV3(t, store, opID)
	if rec.StopReason == state.StopTimeout {
		t.Errorf("v3 stop reason = %q: a yield must not read as a timeout", rec.StopReason)
	}
	waitForOtel(t, "runs{completed}", func() bool { return g.runOutcomes(t, id)["completed"] == 1 })
	got := g.runOutcomes(t, id)
	for _, bad := range []string{"timed_out", "canceled", "failed", "dropped"} {
		if got[bad] != 0 {
			t.Errorf("ops.runs = %v, want no %s outcome for a yielded run", got, bad)
		}
	}
	// N4: ten rows, each counted once. The resumed attempt's first report is the
	// position it inherited (4), so the four finished rows are not counted again.
	if items := g.counterValue(t, "audiobook_organizer.ops.items", id, "processed"); items != 10 {
		t.Errorf("ops.items{processed} = %d, want 10: the resumed attempt counted finished rows again", items)
	}
	// N3: a pause that is re-queued is not an ending. op.terminal fires once, for
	// the real end; none says the op was interrupted.
	terminals := bus.terminalStatuses(opID)
	if len(terminals) != 1 || terminals[0] != "completed" {
		t.Errorf("op.terminal statuses = %v, want exactly [completed]: an in-place continue must not publish a terminal event", terminals)
	}
	if n := bus.resumedCreated(opID); n != 1 {
		t.Errorf("resumed op.created published %d times, want 1: the re-queue's own announcement", n)
	}
	// The op log carries one "operation finished" line (the real end) and the
	// pause's own line; the pause is not logged as a finish.
	var logs string
	waitFor(t, "the final op log flush", 5*time.Second, func() bool {
		logs = opLogText(t, store, opID)
		return strings.Contains(logs, "operation finished")
	})
	if n := strings.Count(logs, "operation finished"); n != 1 {
		t.Errorf("op log has %d 'operation finished' lines, want 1 (the real end only)", n)
	}
	if !strings.Contains(logs, "continuing in place under the same operation id") {
		t.Errorf("op log %q lacks the pause's own line", logs)
	}
}

// A run that returns the sentinel AFTER its deadline fired is still timed_out
// and is not resumed: the deadline decision comes first.
func TestContinueInPlace_DeadlineStillTimesOut(t *testing.T) {
	g, store := newV3OtelRig(t, registry.Options{})
	const id = "test.continue-late-timeout"
	def := makeValidDef(id)
	def.ResumePolicy = registry.ResumeRestart
	def.Timeout = 20 * time.Millisecond
	var attempts atomic.Int32
	def.Run = func(ctx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		attempts.Add(1)
		<-ctx.Done()
		return registry.ErrContinueInPlace // too late: the deadline already fired
	}
	if err := g.reg.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	g.reg.Start(t.Context())
	opID, err := g.reg.EnqueueOp(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	awaitV2(t, store, opID, "canceled")
	rec := awaitV3State(t, store, opID, state.V3TimedOut)
	if rec.StopReason != state.StopTimeout {
		t.Errorf("v3 stop reason = %q, want %q", rec.StopReason, state.StopTimeout)
	}
	row, _ := store.GetOperationV2(opID)
	if row == nil || row.ErrorMessage == nil || !strings.HasPrefix(*row.ErrorMessage, state.TimeoutPrefix) {
		t.Fatalf("v2 row = %+v, want canceled with a %q message", row, state.TimeoutPrefix)
	}
	time.Sleep(150 * time.Millisecond) // a wrongly resumed run would have started again by now
	if got := attempts.Load(); got != 1 {
		t.Errorf("Run was called %d times, want 1: a timed-out run must not be resumed", got)
	}
	waitForOtel(t, "runs{timed_out}", func() bool { return g.runOutcomes(t, id)["timed_out"] == 1 })
}

// A user cancel that makes the run return the sentinel stays canceled, and is
// never recorded as a quiesce: the run reported progress, so only the context
// guard (not the progress check) keeps it from being honoured as a yield.
func TestContinueInPlace_UserCancelStaysCanceled(t *testing.T) {
	g, store := newV3OtelRig(t, registry.Options{})
	const id = "test.continue-user-cancel"
	def := makeValidDef(id)
	def.ResumePolicy = registry.ResumeRestart
	started := make(chan struct{})
	var attempts atomic.Int32
	def.Run = func(ctx context.Context, _ json.RawMessage, rep registry.Reporter) error {
		attempts.Add(1)
		_ = rep.UpdateProgress(3, 10, "")
		close(started)
		<-ctx.Done()
		return registry.ErrContinueInPlace
	}
	if err := g.reg.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	g.reg.Start(t.Context())
	opID, err := g.reg.EnqueueOp(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	recv(t, started, "run to start")
	if err := g.reg.Cancel(opID); err != nil {
		t.Fatal(err)
	}
	awaitV2(t, store, opID, "canceled")
	awaitV3State(t, store, opID, state.V3Canceled)
	time.Sleep(150 * time.Millisecond)
	if got := attempts.Load(); got != 1 {
		t.Errorf("Run was called %d times, want 1: a canceled run must not be resumed", got)
	}
	assertNotQuiesced(t, store, opID)
}

// A watchdog kill (no progress past ProgressTimeout) stays canceled even when
// the killed run's error is the sentinel, and is never recorded as a quiesce.
// The run reported progress first, so the progress check cannot be what refuses
// it.
func TestContinueInPlace_WatchdogKillStaysCanceled(t *testing.T) {
	g, store := newV3OtelRig(t, registry.Options{WatchdogInterval: 20 * time.Millisecond})
	const id = "test.continue-watchdog"
	def := makeValidDef(id)
	def.ResumePolicy = registry.ResumeRestart
	def.ProgressTimeout = 100 * time.Millisecond
	var attempts atomic.Int32
	def.Run = func(runCtx context.Context, _ json.RawMessage, rep registry.Reporter) error {
		attempts.Add(1)
		_ = rep.UpdateProgress(3, 10, "") // then silence: the watchdog kills it
		<-runCtx.Done()
		return registry.ErrContinueInPlace
	}
	if err := g.reg.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	g.reg.Start(t.Context())
	opID, err := g.reg.EnqueueOp(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	awaitV2(t, store, opID, "canceled")
	awaitV3State(t, store, opID, state.V3Canceled)
	time.Sleep(150 * time.Millisecond)
	if got := attempts.Load(); got != 1 {
		t.Errorf("Run was called %d times, want 1: a watchdog-killed run must not be resumed", got)
	}
	assertNotQuiesced(t, store, opID)
}

// A def that does not resume in place cannot yield: the sentinel is an ordinary
// failure carrying its text, never a silent drop or an unbounded re-run.
func TestContinueInPlace_NonRestartDefFails(t *testing.T) {
	g, store := newV3OtelRig(t, registry.Options{})
	const id = "test.continue-drop-def"
	def := makeValidDef(id) // ResumeDrop
	var attempts atomic.Int32
	def.Run = func(context.Context, json.RawMessage, registry.Reporter) error {
		attempts.Add(1)
		return registry.ErrContinueInPlace
	}
	if err := g.reg.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	g.reg.Start(t.Context())
	opID, err := g.reg.EnqueueOp(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	awaitV2(t, store, opID, "failed")
	row, _ := store.GetOperationV2(opID)
	if row == nil || row.ErrorMessage == nil || !strings.Contains(*row.ErrorMessage, "continue in place") {
		t.Fatalf("v2 row = %+v, want failed with the sentinel's text", row)
	}
	time.Sleep(100 * time.Millisecond)
	if got := attempts.Load(); got != 1 {
		t.Errorf("Run was called %d times, want 1", got)
	}
}
