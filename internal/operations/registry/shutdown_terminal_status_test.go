// file: internal/operations/registry/shutdown_terminal_status_test.go
// version: 1.0.0
// guid: 9d4b1e07-5c62-4a38-b0f7-2e91a6c3d5b8
// last-edited: 2026-08-23

package registry_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	registry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// A run stopped by SHUTDOWN and a run stopped by a USER are both runs whose
// context was canceled, and until now both were recorded as "canceled". That
// single status had to answer two different questions, and it answered the wrong
// one: a job the server interrupted looked exactly like a job someone deliberately
// killed, so nothing could ever distinguish work worth resuming from work that
// must stay dead.
//
// These tests pin the four-way precedence in finalStatusForCanceledRun. They
// assert what is RECORDED, not what resumes: resumeAfterStartup still reads only
// the queued|running active index, so an interrupted_* row remains invisible to it
// and nothing auto-resumes as a result of this change.
func startBlockedOp(t *testing.T, r *registry.Registry, store *fakeStore, defID string, policy registry.ResumePolicy, timeout time.Duration) string {
	t.Helper()

	started := make(chan struct{})
	def := makeValidDef(defID)
	def.ResumePolicy = policy
	def.ProgressTimeout = 30 * time.Minute
	if timeout > 0 {
		def.Timeout = timeout
	}
	def.Run = func(runCtx context.Context, _ json.RawMessage, rep registry.Reporter) error {
		_ = rep.UpdateProgress(1, 10, "")
		close(started)
		<-runCtx.Done()
		return runCtx.Err()
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("register %s: %v", defID, err)
	}

	opID, err := r.EnqueueOp(context.Background(), defID, nil)
	if err != nil {
		t.Fatalf("enqueue %s: %v", defID, err)
	}
	<-started
	return opID
}

// waitForStatus polls because the worker releases its run handle BEFORE writing
// the terminal status, so Shutdown's drain loop can observe zero running ops and
// return while the status write is still in flight. Asserting immediately after
// Shutdown returns is a race.
func waitForStatus(t *testing.T, store *fakeStore, opID string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		if row, _ := store.GetOperationV2(opID); row != nil {
			last = row.Status
			if last != "running" && last != "queued" {
				return last
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return last
}

func TestShutdown_InterruptedRunIsRecordedResumable(t *testing.T) {
	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{})
	r.Start(context.Background())

	opID := startBlockedOp(t, r, store, "test.shutdown-quiesce", registry.ResumeRestart, 0)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = r.Shutdown(shutdownCtx)

	got := waitForStatus(t, store, opID)
	if got == "canceled" {
		t.Fatal("a run interrupted by shutdown was recorded as \"canceled\", which is " +
			"indistinguishable from a user deliberately killing it; the resume sweep can " +
			"never safely act on that")
	}
	if got != "interrupted_quiesced" {
		t.Fatalf("status = %q, want interrupted_quiesced for a ResumeRestart op stopped by shutdown", got)
	}
}

// THE NEGATIVE TEST. This is the one that makes the change safe rather than
// merely different: without the userCanceled marker, a cancel that lands just
// before shutdown gets overwritten with a resumable status and the op comes back
// from the dead on the next boot. The race is not narrow -- the run can take
// seconds to notice its context while shutdown proceeds.
func TestShutdown_UserCanceledRunStaysCanceled(t *testing.T) {
	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{})
	r.Start(context.Background())

	opID := startBlockedOp(t, r, store, "test.shutdown-user-cancel", registry.ResumeRestart, 0)

	// Cancel FIRST, then shut down -- the exact interleaving that would otherwise
	// relabel a deliberate kill as a shutdown interruption.
	if err := r.Cancel(opID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = r.Shutdown(shutdownCtx)

	got := waitForStatus(t, store, opID)
	if got != "canceled" {
		t.Fatalf("status = %q, want canceled; a user-canceled run must never be recorded "+
			"as shutdown-interrupted, or it will be resurrected once the sweep reads "+
			"interrupted rows", got)
	}
}

// ResumeDrop must still record interrupted_dropped, not interrupted_quiesced --
// interruptedStatus already distinguishes them and shutdown must respect the
// declared policy rather than flattening every op to one status.
func TestShutdown_DropPolicyIsRecordedDropped(t *testing.T) {
	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{})
	r.Start(context.Background())

	opID := startBlockedOp(t, r, store, "test.shutdown-drop", registry.ResumeDrop, 0)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = r.Shutdown(shutdownCtx)

	got := waitForStatus(t, store, opID)
	if got != "interrupted_dropped" {
		t.Fatalf("status = %q, want interrupted_dropped for a ResumeDrop op", got)
	}
}
