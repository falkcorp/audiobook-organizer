// file: internal/operations/registry/registry_test.go
// version: 1.5.0
// guid: d0e1f2a3-b4c5-6d7e-8f9a-0b1c2d3e4f5a
// last-edited: 2026-07-01

package registry_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// makeValidDef returns a valid OperationDef for use in tests.
func makeValidDef(id string) registry.OperationDef {
	return registry.OperationDef{
		ID:              id,
		Plugin:          "test",
		DisplayName:     "Test Op",
		Description:     "For testing",
		Run:             func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error { return nil },
		DefaultPriority: registry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		ResumePolicy:    registry.ResumeDrop,
		ConcurrencyKey:  "",
	}
}

func newTestRegistry(t *testing.T) (*registry.Registry, *fakeStore) {
	t.Helper()
	store := newFakeStore()
	r := registry.New(store, slog.Default(), 4, nil)
	return r, store
}

// --- RegisterOp tests ---

func TestRegisterOp_RejectsNilRun(t *testing.T) {
	r, _ := newTestRegistry(t)
	def := makeValidDef("test.nil-run")
	def.Run = nil
	if err := r.RegisterOp(def); err == nil {
		t.Fatal("expected error for nil Run, got nil")
	}
}

func TestRegisterOp_RejectsDuplicateID(t *testing.T) {
	r, _ := newTestRegistry(t)
	def := makeValidDef("test.dup")
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("first registration failed: %v", err)
	}
	if err := r.RegisterOp(def); err == nil {
		t.Fatal("expected error for duplicate ID, got nil")
	}
}

func TestRegisterOp_RejectsResumeUnspecified(t *testing.T) {
	r, _ := newTestRegistry(t)
	def := makeValidDef("test.unspecified")
	def.ResumePolicy = registry.ResumeUnspecified
	if err := r.RegisterOp(def); err == nil {
		t.Fatal("expected error for ResumeUnspecified, got nil")
	}
}

func TestRegisterOp_RejectsEmptyID(t *testing.T) {
	r, _ := newTestRegistry(t)
	def := makeValidDef("")
	if err := r.RegisterOp(def); err == nil {
		t.Fatal("expected error for empty ID, got nil")
	}
}

func TestRegisterOp_RejectsColonInID(t *testing.T) {
	r, _ := newTestRegistry(t)
	def := makeValidDef("plugin:op-type")
	if err := r.RegisterOp(def); err == nil {
		t.Fatal("expected error for ID containing ':', got nil")
	}
}

func TestRegisterOp_AcceptsValidDef(t *testing.T) {
	r, _ := newTestRegistry(t)
	def := makeValidDef("test.valid")
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	defs := r.ActiveDefs()
	if len(defs) != 1 || defs[0].ID != "test.valid" {
		t.Errorf("expected 1 def with id test.valid, got %v", defs)
	}
}

// --- EnqueueOp tests ---

func TestEnqueueOp_ErrorForUnknownDef(t *testing.T) {
	r, _ := newTestRegistry(t)
	_, err := r.EnqueueOp(context.Background(), "unknown.def", nil)
	if err == nil {
		t.Fatal("expected error for unknown defID, got nil")
	}
}

func TestEnqueueOp_SucceedsForRegisteredDef(t *testing.T) {
	r, store := newTestRegistry(t)
	def := makeValidDef("test.enqueue")
	_ = r.RegisterOp(def)

	opID, err := r.EnqueueOp(context.Background(), "test.enqueue", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opID == "" {
		t.Fatal("expected non-empty opID")
	}
	if store.statusOf(opID) != "queued" {
		t.Errorf("expected status queued, got %s", store.statusOf(opID))
	}
}

func TestEnqueueOp_WithPriorityOption(t *testing.T) {
	r, store := newTestRegistry(t)
	def := makeValidDef("test.priority")
	_ = r.RegisterOp(def)

	opID, err := r.EnqueueOp(context.Background(), "test.priority", nil,
		registry.WithPriority(registry.PriorityHigh))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	row, _ := store.GetOperationV2(opID)
	if row == nil {
		t.Fatal("op row not found")
	}
	if row.Priority != int(registry.PriorityHigh) {
		t.Errorf("expected priority %d, got %d", registry.PriorityHigh, row.Priority)
	}
}

// --- Cancel tests ---

func TestCancel_QueuedOpSetsCanceled(t *testing.T) {
	r, store := newTestRegistry(t)
	def := makeValidDef("test.cancel-queued")
	_ = r.RegisterOp(def)

	opID, _ := r.EnqueueOp(context.Background(), "test.cancel-queued", nil)
	if err := r.Cancel(opID); err != nil {
		t.Fatalf("Cancel returned error: %v", err)
	}
	if store.statusOf(opID) != "canceled" {
		t.Errorf("expected status canceled, got %s", store.statusOf(opID))
	}
}

func TestCancel_RunningOpCancelsContext(t *testing.T) {
	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()

	r, _ := newTestRegistry(t)

	started := make(chan struct{})
	canceled := make(chan struct{})

	def := makeValidDef("test.cancel-running")
	def.Run = func(runCtx context.Context, _ json.RawMessage, rep registry.Reporter) error {
		close(started)
		<-runCtx.Done()
		close(canceled)
		return runCtx.Err()
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.cancel-running", nil)

	// Wait for run to start.
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("op did not start within 5s")
	}

	// Cancel the running op.
	if err := r.Cancel(opID); err != nil {
		t.Fatalf("Cancel returned error: %v", err)
	}

	// Verify context was canceled inside the run.
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("op context was not canceled within 5s")
	}
}

// --- ActiveDefs tests ---

func TestActiveDefs_ReturnsAllRegistered(t *testing.T) {
	r, _ := newTestRegistry(t)
	_ = r.RegisterOp(makeValidDef("test.a"))
	_ = r.RegisterOp(makeValidDef("test.b"))
	_ = r.RegisterOp(makeValidDef("test.c"))
	defs := r.ActiveDefs()
	if len(defs) != 3 {
		t.Errorf("expected 3 defs, got %d", len(defs))
	}
}

// --- op.created event tests ---

// recordingBus captures every Publish call so the test can assert on event
// names. Implements registry.Bus.
type recordingBus struct {
	events []recordedEvent
}

type recordedEvent struct {
	name    string
	payload any
}

func (b *recordingBus) Publish(_ context.Context, name string, payload any) error {
	b.events = append(b.events, recordedEvent{name: name, payload: payload})
	return nil
}

func TestEnqueueOp_PublishesOpCreated(t *testing.T) {
	store := newFakeStore()
	bus := &recordingBus{}
	r := registry.New(store, slog.Default(), 4, nil)
	r.SetBus(bus)
	_ = r.RegisterOp(makeValidDef("test.opcreated"))

	opID, err := r.EnqueueOp(context.Background(), "test.opcreated", nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	var found bool
	for _, ev := range bus.events {
		if ev.name != "op.created" {
			continue
		}
		p, ok := ev.payload.(map[string]any)
		if !ok {
			t.Fatalf("op.created payload not a map: %T", ev.payload)
		}
		if p["op_id"] != opID {
			t.Errorf("op.created op_id: got %v want %v", p["op_id"], opID)
		}
		if p["resumed"] != false {
			t.Errorf("op.created resumed: got %v want false for fresh enqueue", p["resumed"])
		}
		found = true
	}
	if !found {
		t.Fatalf("expected an op.created event, got events: %+v", bus.events)
	}
}

// --- Task 4: enqueue parking tests ---

// TestEnqueueOp_ParksWhenRequirementUnmet asserts that an op whose def has
// Requires is parked as "waiting_deps" when the requirement is not satisfied.
// The fakeStore's GetDepRev/GetOpCompletion stubs return 0/false, so any
// op_completed requirement is always unmet.
func TestEnqueueOp_ParksWhenRequirementUnmet(t *testing.T) {
	r, store := newTestRegistry(t)
	def := makeValidDef("test.park-unmet")
	def.Requires = []registry.Requirement{
		{Kind: registry.ReqOpCompleted, OpType: "test.prereq"},
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	params := map[string]string{"book_id": "b1"}
	opID, err := r.EnqueueOp(context.Background(), "test.park-unmet", params)
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}

	row, err := store.GetOperationV2(opID)
	if err != nil || row == nil {
		t.Fatalf("GetOperationV2: %v", err)
	}
	if row.Status != "waiting_deps" {
		t.Errorf("expected status=waiting_deps, got %q", row.Status)
	}
	if row.SubjectType != "book" {
		t.Errorf("expected SubjectType=book, got %q", row.SubjectType)
	}
	if row.SubjectID != "b1" {
		t.Errorf("expected SubjectID=b1, got %q", row.SubjectID)
	}
	if row.Requirements == "" {
		t.Error("expected Requirements JSON to be persisted")
	}
}

// TestEnqueueOp_NoRequirements_StillQueued asserts that an op with no
// requirements goes straight to "queued" (additive guarantee: no new branches).
func TestEnqueueOp_NoRequirements_StillQueued(t *testing.T) {
	r, store := newTestRegistry(t)
	def := makeValidDef("test.no-reqs")
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	opID, err := r.EnqueueOp(context.Background(), "test.no-reqs", nil)
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	if store.statusOf(opID) != "queued" {
		t.Errorf("expected status=queued for op without requirements, got %q", store.statusOf(opID))
	}
}

// TestEnqueueOp_CycleInDef_RejectsRegistration asserts that RegisterOp
// rejects an OperationDef that would form a requirement cycle once a
// second def creating the cycle is registered.
func TestEnqueueOp_CycleInDef_RejectsRegistration(t *testing.T) {
	r, _ := newTestRegistry(t)

	defA := makeValidDef("test.cycle-a")
	defA.Requires = []registry.Requirement{{Kind: registry.ReqOpCompleted, OpType: "test.cycle-b"}}
	defB := makeValidDef("test.cycle-b")
	defB.Requires = []registry.Requirement{{Kind: registry.ReqOpCompleted, OpType: "test.cycle-a"}}

	// First registration is fine (no cycle yet with only one node).
	if err := r.RegisterOp(defA); err != nil {
		t.Fatalf("RegisterOp(defA): %v", err)
	}
	// Second registration closes the cycle → must be rejected.
	if err := r.RegisterOp(defB); err == nil {
		t.Fatal("expected RegisterOp to reject a def that creates a requirement cycle")
	}
}

// TestEnqueueOp_WithRequiresOption_Parks verifies that per-enqueue requirements
// added via WithRequires also cause parking when unmet.
func TestEnqueueOp_WithRequiresOption_Parks(t *testing.T) {
	r, store := newTestRegistry(t)
	def := makeValidDef("test.park-via-option")
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	params := map[string]string{"book_id": "b2"}
	opID, err := r.EnqueueOp(context.Background(), "test.park-via-option", params,
		registry.WithRequires(registry.Requirement{Kind: registry.ReqOpCompleted, OpType: "test.other"}))
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	if store.statusOf(opID) != "waiting_deps" {
		t.Errorf("expected status=waiting_deps, got %q", store.statusOf(opID))
	}
}

// TestEnqueueOp_NoBookID_EmptySubject verifies that an op with requirements
// but no book_id in params still enqueues (parks) with an empty subject —
// the system degrades gracefully rather than returning an error.
func TestEnqueueOp_NoBookID_EmptySubject(t *testing.T) {
	r, store := newTestRegistry(t)
	def := makeValidDef("test.park-no-bookid")
	def.Requires = []registry.Requirement{
		{Kind: registry.ReqOpCompleted, OpType: "test.prereq"},
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	// No book_id in params.
	opID, err := r.EnqueueOp(context.Background(), "test.park-no-bookid", map[string]string{})
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	row, _ := store.GetOperationV2(opID)
	if row == nil {
		t.Fatal("op row not found")
	}
	// With no subject, requirements cannot be evaluated → park conservatively.
	if row.Status != "waiting_deps" {
		t.Errorf("expected status=waiting_deps for op with requirements but no subject, got %q", row.Status)
	}
}

// --- Task 5/6: completion recording + wakeup + failure propagation ---

// smartFakeStore extends fakeStore with real dep_rev and completion tracking
// so the scheduler can evaluate requirements against actual stored state.
type smartFakeStore struct {
	*fakeStore
	mu              sync.Mutex
	depRevs         map[string]uint64            // "type:id" → rev
	completions     map[string]uint64            // "type:id:opType" → rev
	fileCompletions map[string]map[string]uint64 // "type:id:opType" → fileID→rev
}

func newSmartFakeStore() *smartFakeStore {
	return &smartFakeStore{
		fakeStore:       newFakeStore(),
		depRevs:         make(map[string]uint64),
		completions:     make(map[string]uint64),
		fileCompletions: make(map[string]map[string]uint64),
	}
}

func (s *smartFakeStore) GetDepRev(sub database.OpSubject) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.depRevs[sub.Type+":"+sub.ID], nil
}

func (s *smartFakeStore) BumpDepRev(sub database.OpSubject) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sub.Type + ":" + sub.ID
	s.depRevs[key]++
	return s.depRevs[key], nil
}

func (s *smartFakeStore) RecordOpCompletion(sub database.OpSubject, opType, _ string, depRev uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completions[sub.Type+":"+sub.ID+":"+opType] = depRev
	return nil
}

func (s *smartFakeStore) GetOpCompletion(sub database.OpSubject, opType string) (uint64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rev, ok := s.completions[sub.Type+":"+sub.ID+":"+opType]
	return rev, ok, nil
}

func (s *smartFakeStore) ListFileCompletions(sub database.OpSubject, opType string) (map[string]uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fileCompletions[sub.Type+":"+sub.ID+":"+opType], nil
}

func (s *smartFakeStore) ListWaitingDepsOps() ([]database.OperationV2Row, error) {
	s.fakeStore.mu.Lock()
	defer s.fakeStore.mu.Unlock()
	var result []database.OperationV2Row
	for _, op := range s.fakeStore.ops {
		if op.Status == "waiting_deps" {
			result = append(result, op)
		}
	}
	return result, nil
}

func (s *smartFakeStore) BookFiles(_ string) ([]string, error) { return nil, nil }

func (s *smartFakeStore) UpdateOperationV2Status(id, status string, startedAt, completedAt *time.Time, errMsg *string) error {
	return s.fakeStore.UpdateOperationV2Status(id, status, startedAt, completedAt, errMsg)
}

// TestDepsScheduler_WakeOnCompletion verifies that a parked op is promoted to
// "queued" when the prerequisite op completes for the same subject.
func TestDepsScheduler_WakeOnCompletion(t *testing.T) {
	store := newSmartFakeStore()
	r := registry.New(store, slog.Default(), 4, nil)

	// Register prereq op (no requirements).
	prereq := makeValidDef("test.prereq-wake")
	if err := r.RegisterOp(prereq); err != nil {
		t.Fatalf("RegisterOp prereq: %v", err)
	}

	// Register dependent op that requires prereq to have completed.
	dep := makeValidDef("test.dep-wake")
	dep.Requires = []registry.Requirement{{Kind: registry.ReqOpCompleted, OpType: "test.prereq-wake"}}
	if err := r.RegisterOp(dep); err != nil {
		t.Fatalf("RegisterOp dep: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)

	// Enqueue dependent — should be parked since prereq hasn't run.
	params := map[string]string{"book_id": "b-wake"}
	depOpID, err := r.EnqueueOp(ctx, "test.dep-wake", params)
	if err != nil {
		t.Fatalf("EnqueueOp dep: %v", err)
	}
	if store.statusOf(depOpID) != "waiting_deps" {
		t.Fatalf("expected dep to be parked, got %q", store.statusOf(depOpID))
	}

	// Complete the prereq externally (simulate worker completion via scheduler).
	sched := registry.NewDepsScheduler(r, store)
	sub := registry.Subject{Type: "book", ID: "b-wake"}
	if err := sched.OnOpCompleted(ctx, sub, "test.prereq-wake"); err != nil {
		t.Fatalf("OnOpCompleted: %v", err)
	}

	// After wakeup, the dependent should be promoted to "queued" or "completed"
	// (4 workers may pick it up and finish before our poll loop observes "queued").
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s := store.statusOf(depOpID)
		if s == "queued" || s == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	s := store.statusOf(depOpID)
	if s != "queued" && s != "completed" {
		t.Errorf("expected dep to be promoted to queued or completed after prereq completion, got %q", s)
	}
}

// TestDepsScheduler_FailPropagation verifies that a parked op is failed when
// the op it requires fails.
func TestDepsScheduler_FailPropagation(t *testing.T) {
	store := newSmartFakeStore()
	r := registry.New(store, slog.Default(), 4, nil)

	prereq := makeValidDef("test.prereq-fail")
	if err := r.RegisterOp(prereq); err != nil {
		t.Fatalf("RegisterOp prereq: %v", err)
	}

	dep := makeValidDef("test.dep-fail")
	dep.Requires = []registry.Requirement{{Kind: registry.ReqOpCompleted, OpType: "test.prereq-fail"}}
	if err := r.RegisterOp(dep); err != nil {
		t.Fatalf("RegisterOp dep: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)

	params := map[string]string{"book_id": "b-fail"}
	depOpID, err := r.EnqueueOp(ctx, "test.dep-fail", params)
	if err != nil {
		t.Fatalf("EnqueueOp dep: %v", err)
	}
	if store.statusOf(depOpID) != "waiting_deps" {
		t.Fatalf("expected dep to be parked, got %q", store.statusOf(depOpID))
	}

	sched := registry.NewDepsScheduler(r, store)
	sub := registry.Subject{Type: "book", ID: "b-fail"}
	if err := sched.OnOpFailed(ctx, sub, "test.prereq-fail"); err != nil {
		t.Fatalf("OnOpFailed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if store.statusOf(depOpID) == "failed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	status := store.statusOf(depOpID)
	if status != "failed" {
		t.Errorf("expected dep to be failed after prereq failure, got %q", status)
	}
	// Verify the error message mentions the failed dependency.
	row, _ := store.GetOperationV2(depOpID)
	if row != nil && (row.ErrorMessage == nil || *row.ErrorMessage == "") {
		t.Error("expected error message to be set on failed dep")
	}
}

// TestE2E_DependencyOrdering is the Task 6 end-to-end proof:
// Register B (requires A), enqueue B (parks), enqueue+run A for same subject,
// assert B is promoted and runs.
func TestE2E_DependencyOrdering(t *testing.T) {
	store := newSmartFakeStore()

	bRan := make(chan struct{}, 1)

	defA := makeValidDef("e2e.op-a")
	defA.ResumePolicy = registry.ResumeDrop

	defB := makeValidDef("e2e.op-b")
	defB.ResumePolicy = registry.ResumeDrop
	defB.Requires = []registry.Requirement{{Kind: registry.ReqOpCompleted, OpType: "e2e.op-a"}}
	defB.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error {
		select {
		case bRan <- struct{}{}:
		default:
		}
		return nil
	}

	r := registry.New(store, slog.Default(), 4, nil)
	if err := r.RegisterOp(defA); err != nil {
		t.Fatalf("RegisterOp A: %v", err)
	}
	if err := r.RegisterOp(defB); err != nil {
		t.Fatalf("RegisterOp B: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sched := registry.NewDepsScheduler(r, store)
	r.SetDepsScheduler(sched)
	r.Start(ctx)

	params := map[string]string{"book_id": "e2e-book"}

	// Enqueue B first — must park because A has never run.
	bOpID, err := r.EnqueueOp(ctx, "e2e.op-b", params)
	if err != nil {
		t.Fatalf("EnqueueOp B: %v", err)
	}
	if store.statusOf(bOpID) != "waiting_deps" {
		t.Fatalf("B should be parked before A runs, got %q", store.statusOf(bOpID))
	}

	// Enqueue A — runs immediately (no requirements), completes, wakes B.
	_, err = r.EnqueueOp(ctx, "e2e.op-a", params)
	if err != nil {
		t.Fatalf("EnqueueOp A: %v", err)
	}

	// Wait for B to run.
	select {
	case <-bRan:
		// B ran — success.
	case <-time.After(10 * time.Second):
		t.Fatalf("B did not run within 10s after A completed; B status=%q",
			store.statusOf(bOpID))
	}
}
