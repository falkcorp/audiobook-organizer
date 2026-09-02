// file: internal/operations/registry/shutdown_drain_test.go
// version: 1.0.1
// guid: 4c1f8a52-7d63-4e90-9b21-6f0a2c5d8e74
// last-edited: 2026-09-02

package registry_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// TestShutdown_WaitsForOpGoroutineToExit is the regression test for the
// test-suite data race + "pebble: closed" panic. Both shared one root cause:
// during shutdown, executeRun released the run handle (so Shutdown reported
// "all workers drained") BEFORE an abandoned op goroutine had actually exited.
// That goroutine then outlived the caller, racing the next test's global
// config write and writing to a closed store.
//
// This test registers an op that ignores context cancellation, calls Shutdown
// with an unbounded context, and asserts Shutdown does NOT return until the op
// goroutine has truly exited. It FAILS on the pre-fix code (Shutdown returns
// after AbandonGrace while the goroutine is still alive).
func TestShutdown_WaitsForOpGoroutineToExit(t *testing.T) {
	ctx := t.Context()

	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		WatchdogInterval: 30 * time.Second,      // keep the watchdog out of the way
		AbandonedCap:     10,                    // don't block dispatch
		AbandonGrace:     50 * time.Millisecond, // classify-as-abandoned quickly
	})

	release := make(chan struct{})
	started := make(chan struct{})
	var exited atomic.Bool

	def := makeValidDef("test.drain")
	def.Plugin = "drain-plugin"
	def.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		close(started)
		<-release // deliberately ignore runCtx — mimics autoBackup not honoring cancellation
		exited.Store(true)
		return nil
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("register op: %v", err)
	}
	r.Start(ctx)

	if _, err := r.EnqueueOp(ctx, "test.drain", nil); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("op did not start within 5s")
	}

	// Shutdown with an unbounded context: it MUST block until the op goroutine
	// actually exits, not merely until it is classified as abandoned.
	shutdownReturned := make(chan struct{})
	go func() {
		_ = r.Shutdown(context.Background())
		close(shutdownReturned)
	}()

	// The op ignores cancellation, so it is still running well past AbandonGrace.
	// Pre-fix, Shutdown returns here (handle released early) — that is the leak.
	select {
	case <-shutdownReturned:
		t.Fatal("Shutdown returned while the op goroutine was still running (goroutine leak)")
	case <-time.After(500 * time.Millisecond):
		// Correct: still blocked because the goroutine has not exited.
	}
	if exited.Load() {
		t.Fatal("op goroutine exited prematurely — invalid test setup")
	}

	// Let the op finish; Shutdown must now return promptly.
	close(release)
	select {
	case <-shutdownReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return after the op goroutine exited")
	}
	if !exited.Load() {
		t.Fatal("op goroutine never recorded its exit")
	}
}
