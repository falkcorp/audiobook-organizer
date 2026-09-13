// file: internal/operations/registry/touch_liveness_watchdog_test.go
// version: 1.0.0
// guid: 71cc2550-6e84-41cd-b891-7bacda5384db
// last-edited: 2026-09-13

package registry_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// An op whose ONLY liveness signal is TouchLiveness (no UpdateProgress at all)
// runs well past its ProgressTimeout under the real watchdog without a strike.
func TestWatchdog_TouchLivenessAloneKeepsOpAlive(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{WatchdogInterval: 20 * time.Millisecond})

	def := makeValidDef("test.wdog-touch-alive")
	def.ResumePolicy = registry.ResumeDrop
	def.ProgressTimeout = 100 * time.Millisecond
	def.Run = func(runCtx context.Context, _ json.RawMessage, rep registry.Reporter) error {
		deadline := time.Now().Add(500 * time.Millisecond) // 5x ProgressTimeout
		for time.Now().Before(deadline) {
			if err := runCtx.Err(); err != nil {
				return err
			}
			registry.TouchLiveness(rep)
			time.Sleep(20 * time.Millisecond)
		}
		return nil
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)
	opID, _ := r.EnqueueOp(ctx, "test.wdog-touch-alive", nil)

	awaitStatus(t, store, opID, "completed", 5*time.Second)
	for _, kind := range []string{"stuck", "never_reported"} {
		if n := len(store.strikesOfKind(opID, kind)); n != 0 {
			t.Errorf("%d %s strikes on an op that touched liveness every 20ms", n, kind)
		}
	}
}

// The inverse: one touch, then silence. The watchdog strikes it as stuck (it
// did report once) and cancels it.
func TestWatchdog_SilentAfterTouchIsStruck(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{WatchdogInterval: 20 * time.Millisecond})

	canceled := make(chan struct{})
	def := makeValidDef("test.wdog-touch-silent")
	def.ResumePolicy = registry.ResumeDrop
	def.ProgressTimeout = 100 * time.Millisecond
	def.Run = func(runCtx context.Context, _ json.RawMessage, rep registry.Reporter) error {
		registry.TouchLiveness(rep)
		<-runCtx.Done()
		close(canceled)
		return runCtx.Err()
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)
	opID, _ := r.EnqueueOp(ctx, "test.wdog-touch-silent", nil)

	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("silent op was not canceled by the watchdog within 5s")
	}
	awaitStatus(t, store, opID, "canceled", 3*time.Second)
	if n := len(store.strikesOfKind(opID, "stuck")); n == 0 {
		t.Error("expected a stuck strike, got none")
	}
}
