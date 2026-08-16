// file: internal/server/server_isolation_test.go
// version: 1.0.0
// guid: 9d2e7b41-3c5a-4f08-ab6e-1c4d70f95a2b
// last-edited: 2026-06-04

package server

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// TestSetupTestServerCleanup_DrainsRunningOpBeforeReturning is the server-level
// regression test for the cross-test data race + "pebble: closed" panic that
// made internal/server flaky under -race.
//
// Original bug: an ops-registry worker running an op that reads the global
// config.AppConfig (e.g. organizer.autoBackup) outlived its test because
// Shutdown reported "all workers drained" while the goroutine was still alive.
// The NEXT test's setupTestServer then reassigned config.AppConfig and closed
// the store out from under that goroutine — a data race on the global and a
// "pebble: closed" panic on the store.
//
// This reproduces the shape through setupTestServer's own cleanup and asserts
// cleanup() does not return until the op goroutine exits, so nothing leaks into
// the next test. It exercises the fixed registry drain end-to-end.
func TestSetupTestServerCleanup_DrainsRunningOpBeforeReturning(t *testing.T) {
	server, cleanup := setupTestServer(t)
	if server.opRegistry == nil {
		t.Skip("ops registry not wired in this build")
	}

	release := make(chan struct{})
	started := make(chan struct{})
	var startedOnce, exited atomic.Bool

	def := opsregistry.OperationDef{
		ID:              "test.isolation-drain",
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "isolation-test",
		DisplayName:     "Isolation Drain Test Op",
		Description:     "Reads global config and ignores ctx, mimicking autoBackup",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		ResumePolicy:    opsregistry.ResumeDrop,
		Run: func(_ context.Context, _ json.RawMessage, _ opsregistry.Reporter) error {
			if startedOnce.CompareAndSwap(false, true) {
				close(started)
			}
			// Touch the same global the real autoBackup reads, then block while
			// ignoring ctx — exactly the pattern that used to leak past Shutdown.
			_ = config.AppConfig.DatabasePath
			<-release
			exited.Store(true)
			return nil
		},
	}
	if err := server.opRegistry.RegisterOp(def); err != nil {
		t.Fatalf("register op: %v", err)
	}
	if _, err := server.opRegistry.EnqueueOp(context.Background(), def.ID, nil); err != nil {
		t.Fatalf("enqueue op: %v", err)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("op did not start within 10s")
	}

	// cleanup() calls opRegistry.Shutdown(context.Background()). With the fix it
	// blocks until our op goroutine returns; pre-fix it returned early (after the
	// abandon grace) while the goroutine kept reading config / using the store.
	cleanupReturned := make(chan struct{})
	go func() {
		cleanup()
		close(cleanupReturned)
	}()

	// Wait past the 5s abandon grace to be sure cleanup is genuinely blocked on
	// the still-running goroutine, not merely mid-grace.
	select {
	case <-cleanupReturned:
		t.Fatal("cleanup() returned while the op goroutine was still running — it would race the next test's config write / store close")
	case <-time.After(6 * time.Second):
		// Correct: still blocked.
	}
	if exited.Load() {
		t.Fatal("op exited prematurely — invalid test setup")
	}

	// Let the op finish; cleanup() must now return promptly.
	close(release)
	select {
	case <-cleanupReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup() did not return after the op goroutine exited")
	}
	if !exited.Load() {
		t.Fatal("op goroutine never recorded its exit")
	}
}
