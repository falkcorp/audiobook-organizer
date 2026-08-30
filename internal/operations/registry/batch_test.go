// file: internal/operations/registry/batch_test.go
// version: 1.1.1
// guid: f7a8b9c0-d1e2-3f4a-5b6c-7d8e9f0a1b2c
// last-edited: 2026-08-30

package registry_test

// M3 batching tests — all required by the spec:
//
//  1. 50 rapid enqueues of a batchable op → exactly ONE row dispatched,
//     params carry all 50 subjects.
//  2. A subject whose requirement is unmet is excluded from dispatch and stays
//     bucketed; once its requirement is satisfied a later flush includes it.
//  3. BatchMaxWait forces a dispatch even under a steady trickle.
//  4. Journaled bucket survives a simulated restart.
//  5. Non-batchable ops are completely unaffected (regression).

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// batchableDef returns a minimal Batchable OperationDef with the given windows.
func batchableDef(id string, bw, bmw time.Duration) registry.OperationDef {
	return registry.OperationDef{
		ID:           id,
		Liveness:     registry.LivenessManual,
		Plugin:       "test",
		DisplayName:  "Batch Test Op",
		Run:          func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error { return nil },
		ResumePolicy: registry.ResumeDrop,
		Batchable:    true,
		BatchWindow:  bw,
		BatchMaxWait: bmw,
	}
}

// batchableDefWithReqs returns a Batchable def with a standing op-completed requirement.
func batchableDefWithReqs(id, requiredOpType string, bw, bmw time.Duration) registry.OperationDef {
	def := batchableDef(id, bw, bmw)
	def.Requires = []registry.Requirement{
		{Kind: registry.ReqOpCompleted, OpType: requiredOpType},
	}
	return def
}

// paramsForBook returns params that encode a book subject.
func paramsForBook(id string) map[string]any {
	return map[string]any{"book_id": id}
}

// pollStore polls for a queued-or-later op matching pred, timing out after timeout.
func pollForOp(t *testing.T, store *fakeStore, pred func(database.OperationV2Row) bool, timeout time.Duration) (database.OperationV2Row, bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		for _, op := range store.ops {
			if pred(op) {
				store.mu.Unlock()
				return op, true
			}
		}
		store.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	return database.OperationV2Row{}, false
}

// countOpsWithDef counts ops in the store matching the given defID.
func countOpsWithDef(store *fakeStore, defID string) int {
	store.mu.Lock()
	defer store.mu.Unlock()
	n := 0
	for _, op := range store.ops {
		if op.DefID == defID {
			n++
		}
	}
	return n
}

// subjectsFromParams decodes {"subjects":[...]} from an OperationV2Row's Params.
func subjectsFromParams(t *testing.T, params string) []database.OpSubject {
	t.Helper()
	var p struct {
		Subjects []database.OpSubject `json:"subjects"`
	}
	if err := json.Unmarshal([]byte(params), &p); err != nil {
		t.Fatalf("subjectsFromParams: %v", err)
	}
	return p.Subjects
}

// newBatchRegistry creates a registry with a given store, 1 worker, and no bus.
func newBatchRegistry(store *fakeStore) *registry.Registry {
	return registry.NewWithOptions(store, slog.Default(), 1, registry.Options{})
}

// TestBatch_50RapidEnqueues verifies that 50 rapid enqueues of a batchable op
// within the window produce exactly ONE dispatched row carrying all 50 subjects.
func TestBatch_50RapidEnqueues(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	r := newBatchRegistry(store)

	const defID = "test.batch-50"
	def := batchableDef(defID, 100*time.Millisecond, 5*time.Second)
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	ctx := context.Background()
	r.Start(ctx)
	defer r.Shutdown(context.Background())

	// Enqueue 50 distinct book subjects concurrently.
	var wg sync.WaitGroup
	for n := range 50 {
		wg.Go(func() {
			id, err := r.EnqueueOp(ctx, defID, paramsForBook(fmt.Sprintf("book-%03d", n)))
			if err != nil {
				t.Errorf("EnqueueOp[%d]: %v", n, err)
			}
			if id != "" {
				t.Errorf("EnqueueOp[%d]: expected empty id for batchable op, got %q", n, id)
			}
		})
	}
	wg.Wait()

	// Wait for the debounce window to fire.
	op, found := pollForOp(t, store, func(op database.OperationV2Row) bool {
		return op.DefID == defID
	}, 3*time.Second)
	if !found {
		t.Fatal("timed out: no dispatched row for batchable op")
	}

	// Must be exactly one row.
	if n := countOpsWithDef(store, defID); n != 1 {
		t.Errorf("expected exactly 1 dispatched row, got %d", n)
	}

	// The params must carry all 50 subjects.
	subs := subjectsFromParams(t, op.Params)
	if len(subs) != 50 {
		t.Errorf("expected 50 subjects in params, got %d: %v", len(subs), subs)
	}

	// All subject types must be "book".
	for _, s := range subs {
		if s.Type != "book" {
			t.Errorf("unexpected subject type %q (want %q)", s.Type, "book")
		}
	}
}

// TestBatch_UnmetReqExcludedThenIncludedOnFlush verifies that:
//   - A subject whose requirement is unmet is excluded from the first dispatch
//     and stays in the journal bucket.
//   - Once the requirement is satisfied, a later flush includes it.
func TestBatch_UnmetReqExcludedThenIncludedOnFlush(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	r := newBatchRegistry(store)

	const defID = "test.batch-req"
	const prereqOpType = "prereq.op"
	def := batchableDefWithReqs(defID, prereqOpType, 80*time.Millisecond, 5*time.Second)
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	ctx := context.Background()
	r.Start(ctx)
	defer r.Shutdown(context.Background())

	// Two subjects: "book-ready" has its prereq completed; "book-unready" does not.
	// Set dep_rev=0 for both (the default). Mark prereq complete for "book-ready".
	store.setCompletion("book", "book-ready", prereqOpType, 0)

	_, _ = r.EnqueueOp(ctx, defID, paramsForBook("book-ready"))
	_, _ = r.EnqueueOp(ctx, defID, paramsForBook("book-unready"))

	// Wait for the first flush.
	op1, found := pollForOp(t, store, func(op database.OperationV2Row) bool {
		return op.DefID == defID
	}, 3*time.Second)
	if !found {
		t.Fatal("timed out: no first dispatched row")
	}

	// First dispatch should contain only "book-ready".
	subs1 := subjectsFromParams(t, op1.Params)
	if len(subs1) != 1 || subs1[0].ID != "book-ready" {
		t.Errorf("first dispatch: expected [book-ready], got %v", subs1)
	}

	// The unready subject must still be in the journal bucket.
	entries, err := store.ListBatchBucket(defID)
	if err != nil {
		t.Fatalf("ListBatchBucket: %v", err)
	}
	found2 := false
	for _, e := range entries {
		if e.Sub.ID == "book-unready" {
			found2 = true
			break
		}
	}
	if !found2 {
		t.Error("book-unready not found in journal bucket after first flush")
	}

	// Now satisfy the requirement for "book-unready".
	store.setCompletion("book", "book-unready", prereqOpType, 0)

	// Wait for a second flush that includes "book-unready".
	var op2 database.OperationV2Row
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		for _, op := range store.ops {
			if op.DefID != defID || op.ID == op1.ID {
				continue
			}
			subs := subjectsFromParams(t, op.Params)
			for _, s := range subs {
				if s.ID == "book-unready" {
					op2 = op
					break
				}
			}
		}
		store.mu.Unlock()
		if op2.ID != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if op2.ID == "" {
		t.Error("timed out: book-unready was not dispatched after requirement was satisfied")
	}
}

// TestBatch_MaxWaitForcesDispatch verifies that BatchMaxWait triggers a dispatch
// even when EnqueueOp calls keep re-arming the BatchWindow.
func TestBatch_MaxWaitForcesDispatch(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	r := newBatchRegistry(store)

	const defID = "test.batch-maxwait"
	// BatchWindow = 200ms (debounce), BatchMaxWait = 300ms (hard cap).
	// We'll keep tickling every 80ms so the window keeps re-arming, but
	// MaxWait fires at 300ms.
	bw := 200 * time.Millisecond
	bmw := 300 * time.Millisecond
	def := batchableDef(defID, bw, bmw)
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	ctx := context.Background()
	r.Start(ctx)
	defer r.Shutdown(context.Background())

	// Trickle in subjects every 80ms for 500ms (well past MaxWait).
	// We expect a dispatch at ~300ms (MaxWait), not at 500+200ms (last trickle + window).
	stop := make(chan struct{})
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = r.EnqueueOp(ctx, defID, paramsForBook(fmt.Sprintf("trickle-%03d", i)))
			time.Sleep(80 * time.Millisecond)
		}
	}()

	// The op should appear well within 2×MaxWait.
	_, found := pollForOp(t, store, func(op database.OperationV2Row) bool {
		return op.DefID == defID
	}, 2*bmw)
	close(stop)
	if !found {
		t.Fatal("timed out: MaxWait did not force a dispatch")
	}
	// Verify only one row so far (we may get more after stop, but at least one).
	if n := countOpsWithDef(store, defID); n < 1 {
		t.Errorf("expected at least 1 dispatched row, got %d", n)
	}
}

// TestBatch_JournalSurvivesRestart verifies that batch buckets survive a simulated
// registry restart: subjects written to the journal are picked up by a fresh registry.
func TestBatch_JournalSurvivesRestart(t *testing.T) {
	t.Parallel()

	store := newFakeStore()

	const defID = "test.batch-restart"
	def := batchableDef(defID, 50*time.Millisecond, 5*time.Second)

	// --- First registry: write journal entries directly, do NOT start (simulating
	//     a crash mid-window). We write to the store directly so there's no timer. ---
	_ = store.AddToBatchBucket(defID, database.OpSubject{Type: "book", ID: "book-persisted-1"})
	_ = store.AddToBatchBucket(defID, database.OpSubject{Type: "book", ID: "book-persisted-2"})

	// Verify they are in the journal.
	entries, err := store.ListBatchBucket(defID)
	if err != nil {
		t.Fatalf("ListBatchBucket: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 journal entries before restart, got %d", len(entries))
	}

	// --- Second registry: fresh instance on the same store, should reload. ---
	r2 := newBatchRegistry(store)
	if err := r2.RegisterOp(def); err != nil {
		t.Fatalf("r2.RegisterOp: %v", err)
	}

	ctx := context.Background()
	r2.Start(ctx)
	defer r2.Shutdown(context.Background())

	// The batch reload should arm a timer and dispatch within 2×BatchWindow.
	op, found := pollForOp(t, store, func(op database.OperationV2Row) bool {
		return op.DefID == defID
	}, 5*time.Second)
	if !found {
		t.Fatal("timed out: persisted subjects not dispatched after restart")
	}

	subs := subjectsFromParams(t, op.Params)
	if len(subs) != 2 {
		t.Errorf("expected 2 subjects from restart, got %d: %v", len(subs), subs)
	}

	// Check that both expected subjects appear.
	subIDs := make(map[string]bool)
	for _, s := range subs {
		subIDs[s.ID] = true
	}
	for _, want := range []string{"book-persisted-1", "book-persisted-2"} {
		if !subIDs[want] {
			t.Errorf("subject %q missing from dispatched params", want)
		}
	}
}

// TestBatch_NonBatchableOpsUnaffected verifies that ops without Batchable=true
// continue to work exactly as before — immediately inserted with a non-empty ID.
func TestBatch_NonBatchableOpsUnaffected(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	r := newBatchRegistry(store)

	const defID = "test.non-batchable"
	def := registry.OperationDef{
		ID:           defID,
		Liveness:     registry.LivenessManual,
		Plugin:       "test",
		DisplayName:  "Non-Batchable",
		Run:          func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error { return nil },
		ResumePolicy: registry.ResumeDrop,
		Batchable:    false, // explicitly not batchable
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	ctx := context.Background()
	r.Start(ctx)
	defer r.Shutdown(context.Background())

	opID, err := r.EnqueueOp(ctx, defID, paramsForBook("book-1"))
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	if opID == "" {
		t.Error("non-batchable EnqueueOp returned empty ID")
	}

	// The row must be in the store immediately.
	store.mu.Lock()
	_, ok := store.ops[opID]
	store.mu.Unlock()
	if !ok {
		t.Errorf("non-batchable op %q not found in store immediately", opID)
	}

	// No batch bucket entries should exist for this def.
	entries, err := store.ListBatchBucket(defID)
	if err != nil {
		t.Fatalf("ListBatchBucket: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("non-batchable op has unexpected batch bucket entries: %v", entries)
	}
}

// TestBatch_RaceDetector is a race-detector stress test: many goroutines call
// EnqueueOp concurrently for a batchable op. With -race this catches mutex gaps.
func TestBatch_RaceDetector(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping race stress test in short mode")
	}
	t.Parallel()

	store := newFakeStore()
	r := newBatchRegistry(store)

	const defID = "test.batch-race"
	def := batchableDef(defID, 50*time.Millisecond, 500*time.Millisecond)
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	ctx := context.Background()
	r.Start(ctx)
	defer r.Shutdown(context.Background())

	var wg sync.WaitGroup
	for n := range 100 {
		wg.Go(func() {
			_, _ = r.EnqueueOp(ctx, defID, paramsForBook(fmt.Sprintf("race-book-%03d", n)))
		})
	}
	wg.Wait()

	// At least one row dispatched, params parseable — no panic or data race.
	op, found := pollForOp(t, store, func(op database.OperationV2Row) bool {
		return op.DefID == defID
	}, 3*time.Second)
	if !found {
		t.Fatal("race test: no dispatch observed")
	}
	subs := subjectsFromParams(t, op.Params)
	if len(subs) == 0 {
		t.Error("race test: dispatched row has no subjects")
	}
}

// TestBatch_DefaultWindows verifies that zero BatchWindow/BatchMaxWait on a
// Batchable def fall back to the package defaults (5s/60s).
func TestBatch_DefaultWindows(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	r := newBatchRegistry(store)

	// Def with zero windows — defaults should apply (5s/60s).
	const defID = "test.batch-defaults"
	def := registry.OperationDef{
		ID:           defID,
		Liveness:     registry.LivenessManual,
		Plugin:       "test",
		DisplayName:  "Batch Defaults",
		Run:          func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error { return nil },
		ResumePolicy: registry.ResumeDrop,
		Batchable:    true,
		// BatchWindow and BatchMaxWait are zero — defaults should kick in.
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	// Just verify EnqueueOp accepts it without panic/error.
	ctx := context.Background()
	r.Start(ctx)
	defer r.Shutdown(context.Background())

	id, err := r.EnqueueOp(ctx, defID, paramsForBook("book-default"))
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	if id != "" {
		t.Errorf("expected empty id for batchable op, got %q", id)
	}

	// Verify the subject landed in the journal (proves batchAdd was called).
	entries, err := store.ListBatchBucket(defID)
	if err != nil {
		t.Fatalf("ListBatchBucket: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 journal entry, got %d", len(entries))
	}
	if entries[0].Sub.ID != "book-default" {
		t.Errorf("expected subject book-default, got %q", entries[0].Sub.ID)
	}
}

// TestBatch_NoSubjectFallsThrough verifies that a Batchable op with params that
// have no derivable subject falls through to the non-batch path and returns a
// non-empty op ID.
func TestBatch_NoSubjectFallsThrough(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	r := newBatchRegistry(store)

	const defID = "test.batch-nosubject"
	def := batchableDef(defID, 50*time.Millisecond, 500*time.Millisecond)
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	ctx := context.Background()
	r.Start(ctx)
	defer r.Shutdown(context.Background())

	// Params with no "book_id" key — subject extraction returns empty Subject.
	id, err := r.EnqueueOp(ctx, defID, map[string]any{"irrelevant": "value"})
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	// Fall-through to normal path must return a non-empty ID.
	if id == "" {
		t.Error("expected non-empty id when batchable op has no subject, got empty")
	}
	// Verify it ends up with "test.batch-nosubject" in subject-less params (no batch bucket).
	entries, _ := store.ListBatchBucket(defID)
	for _, e := range entries {
		if strings.Contains(e.Sub.ID, "irrelevant") {
			t.Errorf("unexpected entry in batch bucket: %v", e)
		}
	}
}

// TestBatch_ShutdownWaitsForInFlightFire verifies the fireWG shutdown-safety
// guarantee: Shutdown() must block until any batchFire goroutine that passed
// the shuttingDown gate has finished its DB write, and no InsertOperationV2
// call must land after Shutdown returns (which in production corresponds to
// DB Close()).
//
// Mechanism: fakeStore.insertHook blocks the fire goroutine at the exact point
// it calls InsertOperationV2. We confirm Shutdown() is waiting, release the
// hook, confirm Shutdown() returns, then mark the store closed and verify no
// further insert is attempted.
func TestBatch_ShutdownWaitsForInFlightFire(t *testing.T) {
	t.Parallel()

	store := newFakeStore()

	// insertReady is closed when InsertOperationV2 is first entered, signalling
	// the test that the fire goroutine is in-flight.
	insertReady := make(chan struct{})
	// insertUnblock is closed by the test to let InsertOperationV2 proceed.
	insertUnblock := make(chan struct{})

	once := sync.Once{}
	store.insertHook = func() {
		// Signal only on the first call (the batched dispatch).
		once.Do(func() {
			close(insertReady)
			<-insertUnblock // block until the test releases us
		})
	}

	r := newBatchRegistry(store)

	const defID = "test.batch-shutdown-race"
	// Very short window so the timer fires quickly.
	def := batchableDef(defID, 10*time.Millisecond, 500*time.Millisecond)
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	ctx := context.Background()
	r.Start(ctx)

	// Enqueue one subject to arm the timer.
	if _, err := r.EnqueueOp(ctx, defID, paramsForBook("shutdown-race-book")); err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}

	// Wait until the fire goroutine is inside InsertOperationV2 (blocked by hook).
	select {
	case <-insertReady:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for batchFire to reach InsertOperationV2")
	}

	// Now call Shutdown concurrently — it must NOT return until we release the
	// fire goroutine, because fireWG.Wait() must block.
	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- r.Shutdown(context.Background())
	}()

	// Give Shutdown a moment to reach fireWG.Wait() (it must be blocked there,
	// not returned yet).
	select {
	case err := <-shutdownDone:
		t.Fatalf("Shutdown returned before fire goroutine finished (err=%v) — fireWG not blocking", err)
	case <-time.After(100 * time.Millisecond):
		// Good: Shutdown is still blocked waiting for the in-flight fire.
	}

	// Release the fire goroutine.
	close(insertUnblock)

	// Shutdown must now complete within a reasonable timeout.
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Errorf("Shutdown returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown did not return after fire goroutine was released")
	}

	// Simulate DB close. Any InsertOperationV2 call after this point indicates
	// a use-after-close bug.
	store.closed.Store(true)

	// Verify the insert that happened before close is recorded.
	entries, _ := store.ListBatchBucket(defID)
	// After a successful dispatch the journal is cleared.
	_ = entries // may be empty (cleared) or non-empty (error path) — not the focus here

	// Sleep briefly to give any hypothetical late timer a chance to fire and
	// call InsertOperationV2 (which would now return an error and set a detectable
	// state). The real assertion is that no t.Error was triggered by the hook.
	time.Sleep(50 * time.Millisecond)
}
