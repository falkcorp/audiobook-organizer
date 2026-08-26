// file: internal/operations/registry/liveness_test.go
// version: 1.0.0
// guid: 8c41f7a2-6e05-4d93-b7a8-3f9d20c15e64
// last-edited: 2026-08-16

// Telling "never reported once" apart from "reported, then stopped".
//
// Both states used to produce the same strike -- kind "stuck", message "no
// progress for 5m...". That ambiguity is why a broken wire survived from
// 2026-05-11 to 2026-08-16: LoggerFromReporter discarded the reporter it was
// handed, so eight ops could not report at all, and every symptom they produced
// read as "this operation is slow" rather than "this operation is not
// connected". It was diagnosed as a property of the op and worked around
// locally three separate times.
//
// The two states want opposite responses. A stuck op is a bug in the work: go
// look at what it is blocked on. A never-reported op is a bug in the wiring: the
// op is probably fine and the plumbing is missing. Only one of them is fixed by
// raising ProgressTimeout, which is what all three earlier workarounds did.

package registry_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// TestWatchdog_NeverReportedGetsItsOwnStrike covers the op that cannot report:
// it never calls UpdateProgress and no last_progress_at is ever written, so the
// watchdog measures from StartedAt. That is the LoggerFromReporter shape.
func TestWatchdog_NeverReportedGetsItsOwnStrike(t *testing.T) {
	ctx := t.Context()

	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		WatchdogInterval: 50 * time.Millisecond,
	})

	canceled := make(chan struct{})
	def := makeValidDef("test.never-reported")
	def.ResumePolicy = registry.ResumeDrop
	def.ProgressTimeout = 100 * time.Millisecond
	def.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		// Never touches the reporter -- exactly what the eight LoggerFromReporter
		// ops did for three months.
		<-runCtx.Done()
		close(canceled)
		return runCtx.Err()
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.never-reported", nil)
	awaitStatus(t, store, opID, "running", 3*time.Second)

	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("never-reporting op was not canceled within 3s")
	}
	awaitStatus(t, store, opID, "canceled", 3*time.Second)

	if n := len(store.strikesOfKind(opID, "never_reported")); n == 0 {
		t.Error("an op that never called UpdateProgress did not get a never_reported strike")
	}
	if n := len(store.strikesOfKind(opID, "stuck")); n != 0 {
		t.Errorf("an op that never reported was also filed as %d stuck strike(s); the two kinds must be exclusive", n)
	}
}

// TestWatchdog_ReportedThenStalledIsStillStuck is the control. Without it the
// first test would pass just as well if EVERY strike were relabelled
// never_reported, which would destroy the distinction rather than draw it.
func TestWatchdog_ReportedThenStalledIsStillStuck(t *testing.T) {
	ctx := t.Context()

	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		WatchdogInterval: 50 * time.Millisecond,
	})

	canceled := make(chan struct{})
	def := makeValidDef("test.reported-then-stalled")
	def.ResumePolicy = registry.ResumeDrop
	def.ProgressTimeout = 100 * time.Millisecond
	def.Run = func(runCtx context.Context, _ json.RawMessage, rep registry.Reporter) error {
		// Report once -- the op IS wired -- then wedge. This is a genuine stall.
		_ = rep.UpdateProgress(1, 10, "working")
		<-runCtx.Done()
		close(canceled)
		return runCtx.Err()
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.reported-then-stalled", nil)
	awaitStatus(t, store, opID, "running", 3*time.Second)

	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("stalled op was not canceled within 3s")
	}
	awaitStatus(t, store, opID, "canceled", 3*time.Second)

	if n := len(store.strikesOfKind(opID, "stuck")); n == 0 {
		t.Error("an op that reported and then stalled did not get a stuck strike")
	}
	if n := len(store.strikesOfKind(opID, "never_reported")); n != 0 {
		t.Errorf("an op that DID report was filed as never_reported (%d strikes)", n)
	}
}
