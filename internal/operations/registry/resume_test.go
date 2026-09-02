// file: internal/operations/registry/resume_test.go
// version: 1.4.1
// guid: 6f7a8b9c-0d1e-2345-f012-34567890abcd
// last-edited: 2026-09-02

package registry_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/oklog/ulid/v2"
)

// insertOpV2 pre-loads the store with one operation_v2 row in a caller-chosen
// status, and is the single planter the resume tests build on. The three
// wrappers below name the shapes the resume paths actually distinguish; keep new
// variants as wrappers rather than fresh copies, so a change to the row layout
// lands in one place.
func insertOpV2(store *fakeStore, defID, plugin string, priority int, status, params string) string {
	opID := ulid.Make().String()
	store.InsertOperationV2(database.OperationV2Row{ //nolint:errcheck
		ID:       opID,
		DefID:    defID,
		Plugin:   plugin,
		TraceID:  ulid.Make().String(),
		SpanID:   ulid.Make().String(),
		Status:   status,
		Priority: priority,
		Params:   params,
		QueuedAt: time.Now().UTC(),
	})
	return opID
}

// insertRunningOp pre-loads the store with a running op so resumeAfterStartup
// can find it. Returns the op ID.
func insertRunningOp(store *fakeStore, defID, plugin string, priority int) string {
	return insertOpV2(store, defID, plugin, priority, "running", "{}")
}

// insertQueuedOp pre-loads the store with a QUEUED op -- the shape a shutdown
// leaves behind for an op that was enqueued but never dispatched to a worker.
// Shutdown walks r.running only, so such a row is never rewritten and is still
// carrying its opv2:act: key when the next process starts.
func insertQueuedOp(store *fakeStore, defID, plugin string, priority int) string {
	return insertOpV2(store, defID, plugin, priority, "queued", "{}")
}

// TestResume_DropLeavesInterruptedDropped verifies that ResumeDrop ops found
// in status=running at startup are marked interrupted_dropped.
func TestResume_DropLeavesInterruptedDropped(t *testing.T) {
	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		WatchdogInterval: 30 * time.Second,
	})

	def := makeValidDef("test.resume-drop")
	def.ResumePolicy = registry.ResumeDrop
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error { return nil }
	_ = r.RegisterOp(def)

	opID := insertRunningOp(store, "test.resume-drop", "test", 1)

	ctx := t.Context()
	r.Start(ctx)

	// resumeAfterStartup ran synchronously in Start; check immediately.
	time.Sleep(20 * time.Millisecond)
	if store.statusOf(opID) != "interrupted_dropped" {
		t.Errorf("expected interrupted_dropped, got %s", store.statusOf(opID))
	}
}

// TestResume_AskLeavesInterruptedAsk verifies that ResumeAsk ops are set to
// interrupted_ask.
func TestResume_AskLeavesInterruptedAsk(t *testing.T) {
	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		WatchdogInterval: 30 * time.Second,
	})

	def := makeValidDef("test.resume-ask")
	def.ResumePolicy = registry.ResumeAsk
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error { return nil }
	_ = r.RegisterOp(def)

	opID := insertRunningOp(store, "test.resume-ask", "test", 1)

	ctx := t.Context()
	r.Start(ctx)

	time.Sleep(20 * time.Millisecond)
	if store.statusOf(opID) != "interrupted_ask" {
		t.Errorf("expected interrupted_ask, got %s", store.statusOf(opID))
	}
}

// TestResume_RestartReDispatchesWithIncrementedResumeCount verifies that
// ResumeRestart ops are re-dispatched exactly once and resume_count is incremented.
func TestResume_RestartReDispatchesWithIncrementedResumeCount(t *testing.T) {
	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		WatchdogInterval: 30 * time.Second,
	})

	var runCount atomic.Int32
	ran := make(chan struct{}, 1)
	def := makeValidDef("test.resume-restart")
	def.ResumePolicy = registry.ResumeRestart
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error {
		runCount.Add(1)
		// Signal first run only; buffer=1 so extra sends are dropped.
		select {
		case ran <- struct{}{}:
		default:
		}
		return nil
	}
	_ = r.RegisterOp(def)

	opID := insertRunningOp(store, "test.resume-restart", "test", 1)

	ctx := t.Context()
	r.Start(ctx)

	// resumeAfterStartup should dispatch the op; wait for it to run.
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("resume-restart op did not run within 5s")
	}

	// Give a brief window to detect a spurious second dispatch (double-dispatch bug).
	time.Sleep(100 * time.Millisecond)

	// Run must have been called exactly once — the resumed op, not a double-dispatch.
	if got := runCount.Load(); got != 1 {
		t.Errorf("expected Run called exactly 1 time, got %d (double-dispatch?)", got)
	}

	// resume_count should be 1 (was 0, incremented to 1 in resumeRestart).
	row, err := store.GetOperationV2(opID)
	if err != nil || row == nil {
		t.Fatal("op row not found")
	}
	if row.ResumeCount != 1 {
		t.Errorf("expected resume_count=1, got %d", row.ResumeCount)
	}
}

// TestResume_RestartDoesNotInheritStaleLiveness proves that a resumed attempt
// starts with a fresh watchdog baseline while retaining its persisted operation
// identity and checkpoint. Before the baseline was attempt-scoped, the
// watchdog read LastProgressAt from the pre-restart attempt and canceled the
// fresh scan on its first tick as "idle for hours".
func TestResume_RestartDoesNotInheritStaleLiveness(t *testing.T) {
	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		WatchdogInterval: 20 * time.Millisecond,
	})

	progressed := make(chan struct{}, 1)
	def := makeValidDef("test.resume-fresh-liveness")
	def.ResumePolicy = registry.ResumeRestart
	def.ProgressTimeout = time.Second
	def.Run = func(ctx context.Context, _ json.RawMessage, reporter registry.Reporter) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(80 * time.Millisecond):
		}
		if err := reporter.UpdateProgress(1, 1, "resumed work started"); err != nil {
			return err
		}
		progressed <- struct{}{}
		return nil
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	opID := insertRunningOp(store, def.ID, def.Plugin, 1)
	stale := time.Now().UTC().Add(-3 * time.Hour)
	store.setLastProgressAt(opID, &stale)

	ctx := t.Context()
	r.Start(ctx)

	select {
	case <-progressed:
	case <-time.After(2 * time.Second):
		t.Fatal("resumed operation was canceled before its first new-attempt progress update")
	}
}

// TestResume_RequeueFreshRun verifies that ResumeRequeue ops create a new
// queued op (progress=0) and the original is marked interrupted_dropped.
func TestResume_RequeueFreshRun(t *testing.T) {
	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		WatchdogInterval: 30 * time.Second,
	})

	ran := make(chan struct{}, 1)
	def := makeValidDef("test.resume-requeue")
	def.ResumePolicy = registry.ResumeRequeue
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error {
		select {
		case ran <- struct{}{}:
		default:
		}
		return nil
	}
	_ = r.RegisterOp(def)

	originalID := insertRunningOp(store, "test.resume-requeue", "test", 1)

	ctx := t.Context()
	r.Start(ctx)

	// Wait for the new op (fresh run) to complete.
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("requeued op did not run within 5s")
	}

	// Original op should be interrupted_dropped.
	if store.statusOf(originalID) != "interrupted_dropped" {
		t.Errorf("original op: expected interrupted_dropped, got %s", store.statusOf(originalID))
	}
}

// TestResume_ReconcileScanAlwaysDropped verifies that even if reconcile_scan
// has a registered def with ResumeRestart, it is always force-dropped.
func TestResume_ReconcileScanAlwaysDropped(t *testing.T) {
	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		WatchdogInterval: 30 * time.Second,
	})

	// Register a def whose ID matches the hardcoded reconcile_scan constant.
	def := makeValidDef("reconcile_scan")
	def.ResumePolicy = registry.ResumeRestart // would normally restart, but must drop
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error { return nil }
	_ = r.RegisterOp(def)

	opID := insertRunningOp(store, "reconcile_scan", "scanner", 1)

	ctx := t.Context()
	r.Start(ctx)

	time.Sleep(20 * time.Millisecond)
	if store.statusOf(opID) != "interrupted_dropped" {
		t.Errorf("reconcile_scan: expected interrupted_dropped, got %s", store.statusOf(opID))
	}
}

// insertRunningOpWithParams is insertRunningOp with a caller-chosen params blob,
// for the preservation tests below.
func insertRunningOpWithParams(store *fakeStore, defID, plugin, params string) string {
	return insertOpV2(store, defID, plugin, 1, "running", params)
}

// TestResume_PreservesParamsAcrossRestartAndRequeue pins the guarantee that lets
// an operation carry a destructive-by-default choice across a restart.
//
// dry_run is the sharp case. Its zero value is the DESTRUCTIVE one, so an op
// resumed with EMPTY params does not merely lose a setting — an operator who
// asked for a PREVIEW gets a real mutation on the way back up, under the
// original run's own id. Seven maintenance jobs are both resumable and advertise
// dry_run:true, and one of them removes directories from disk.
//
// This used to be worked around one layer up: the v1 operations row has no params
// field at all, so internal/server persisted a side-table blob on every enqueue
// and, when that blob was missing, fell back to whatever the job ADVERTISED. All
// of that was deleted when the v1 maintenance minter was retired, on the claim
// that both resume paths carry the v2 row's params across verbatim. This test is
// that claim, checked rather than assumed.
func TestResume_PreservesParamsAcrossRestartAndRequeue(t *testing.T) {
	const params = `{"job_id":"cleanup-empty-folders","dry_run":true}`

	for _, tc := range []struct {
		name   string
		defID  string
		policy registry.ResumePolicy
	}{
		{"restart resumes the same row", "test.params-restart", registry.ResumeRestart},
		{"requeue copies onto the new row", "test.params-requeue", registry.ResumeRequeue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
				WatchdogInterval: 30 * time.Second,
			})

			seen := make(chan string, 4)
			def := makeValidDef(tc.defID)
			def.ResumePolicy = tc.policy
			def.Run = func(_ context.Context, raw json.RawMessage, _ registry.Reporter) error {
				select {
				case seen <- string(raw):
				default:
				}
				return nil
			}
			_ = r.RegisterOp(def)

			insertRunningOpWithParams(store, tc.defID, "test", params)

			ctx := t.Context()
			r.Start(ctx)

			var got string
			select {
			case got = <-seen:
			case <-time.After(5 * time.Second):
				t.Fatal("resumed op did not run within 5s")
			}

			// Compare decoded, not byte-for-byte: resumeRestart may merge a
			// checkpoint into params, which is allowed to reorder keys.
			var want, have struct {
				JobID  string `json:"job_id"`
				DryRun bool   `json:"dry_run"`
			}
			if err := json.Unmarshal([]byte(params), &want); err != nil {
				t.Fatalf("decode want: %v", err)
			}
			if err := json.Unmarshal([]byte(got), &have); err != nil {
				t.Fatalf("the resumed run received params that do not decode (%s): %v", got, err)
			}
			if have != want {
				t.Fatalf("resumed run received %+v, want %+v (raw %s); an op resumed "+
					"without its params falls back to dry_run=false and applies for real", have, want, got)
			}
		})
	}
}
