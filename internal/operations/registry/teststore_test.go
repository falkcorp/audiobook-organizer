// file: internal/operations/registry/teststore_test.go
// version: 2.8.1
// guid: c9d0e1f2-a3b4-5c6d-7e8f-9a0b1c2d3e4f
// last-edited: 2026-07-12

package registry_test

// fakeStore is a minimal in-memory implementation of database.OpsV2Store,
// which is all the registry depends on. No SQLite required.

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// fakeStore implements database.OpsV2Store in memory.
type fakeStore struct {
	mu      sync.Mutex
	defs    map[string]database.OpDefinitionV2Row
	ops     map[string]database.OperationV2Row
	strikes []database.OpStrikeV2Row
	states  map[string]database.OpStateV2Row
	logs    []database.OpLogV2Row
	errors  []database.OpErrorV2Row

	// Configurable dep-store state for M3 batch requirement tests.
	// depRevs maps "subjectType:subjectID" → current dep_rev.
	depRevs map[string]uint64
	// completions maps "subjectType:subjectID:opType" → dep_rev.
	completions map[string]uint64

	// M3 batch bucket: maps "opType:subjectType:subjectID" → BatchBucketEntry.
	batchBucket map[string]database.BatchBucketEntry

	// closed simulates DB-close for shutdown-safety tests. When true,
	// InsertOperationV2 returns an error rather than panicking — the test
	// can assert that no insert is attempted after Shutdown returns.
	closed atomic.Bool

	// insertHook, if non-nil, is called inside InsertOperationV2 (while holding
	// the store mutex) before the row is written. Used by
	// TestBatch_ShutdownWaitsForInFlightFire to block the fire goroutine at a
	// deterministic point so Shutdown's fireWG.Wait() can be observed.
	insertHook func()
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		defs:        make(map[string]database.OpDefinitionV2Row),
		ops:         make(map[string]database.OperationV2Row),
		states:      make(map[string]database.OpStateV2Row),
		depRevs:     make(map[string]uint64),
		completions: make(map[string]uint64),
		batchBucket: make(map[string]database.BatchBucketEntry),
	}
}

// setCompletion records an op completion at a given dep_rev (for test control).
func (f *fakeStore) setCompletion(subType, subID, opType string, rev uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completions[subType+":"+subID+":"+opType] = rev
}

// Compile-time assertion: fakeStore must implement OpsV2Store.
var _ database.OpsV2Store = (*fakeStore)(nil)

func (f *fakeStore) UpsertOpDefinitionV2(row database.OpDefinitionV2Row) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.defs[row.ID] = row
	return nil
}

func (f *fakeStore) DeleteOrphanOpDefsV2(keepIDs []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	keep := make(map[string]bool, len(keepIDs))
	for _, id := range keepIDs {
		keep[id] = true
	}
	for id := range f.defs {
		if !keep[id] {
			delete(f.defs, id)
		}
	}
	return nil
}

func (f *fakeStore) InsertOperationV2(row database.OperationV2Row) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed.Load() {
		return fmt.Errorf("fakeStore: InsertOperationV2 called after Close()")
	}
	if f.insertHook != nil {
		f.insertHook()
	}
	if _, exists := f.ops[row.ID]; exists {
		return fmt.Errorf("duplicate op id %s", row.ID)
	}
	f.ops[row.ID] = row
	return nil
}

// insertQueuedAtomic inserts several ops under a single lock, so a concurrent
// ListQueuedOperationsV2 observes all of them or none. Used to test priority
// ordering deterministically: the dispatcher only guarantees priority among ops
// visible within one cycle, so both ops must become visible atomically.
func (f *fakeStore) insertQueuedAtomic(rows ...database.OperationV2Row) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range rows {
		f.ops[row.ID] = row
	}
}

func (f *fakeStore) ListQueuedOperationsV2() ([]database.OperationV2Row, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []database.OperationV2Row
	for _, op := range f.ops {
		if op.Status == "queued" {
			result = append(result, op)
		}
	}
	// Sort: priority DESC, queued_at ASC
	sort.Slice(result, func(i, j int) bool {
		if result[i].Priority != result[j].Priority {
			return result[i].Priority > result[j].Priority
		}
		return result[i].QueuedAt.Before(result[j].QueuedAt)
	})
	return result, nil
}

func (f *fakeStore) GetOperationV2(id string) (*database.OperationV2Row, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	op, ok := f.ops[id]
	if !ok {
		return nil, fmt.Errorf("op %s not found", id)
	}
	return &op, nil
}

func (f *fakeStore) UpdateOperationV2Status(id, status string, startedAt, completedAt *time.Time, errMsg *string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	op, ok := f.ops[id]
	if !ok {
		return nil // best-effort
	}
	op.Status = status
	if startedAt != nil {
		op.StartedAt = startedAt
	}
	if completedAt != nil {
		op.CompletedAt = completedAt
	}
	if errMsg != nil {
		op.ErrorMessage = errMsg
	}
	f.ops[id] = op
	return nil
}

func (f *fakeStore) SetOperationV2StatusIfQueued(id, newStatus string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	op, ok := f.ops[id]
	if !ok {
		return false, nil
	}
	if op.Status != "queued" {
		return false, nil
	}
	op.Status = newStatus
	f.ops[id] = op
	return true, nil
}

func (f *fakeStore) CountRunningByPluginV2(plugin string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, op := range f.ops {
		if op.Plugin == plugin && op.Status == "running" {
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []database.OperationV2Row
	for _, op := range f.ops {
		if op.Status == "queued" || op.Status == "running" {
			result = append(result, op)
		}
	}
	return result, nil
}

func (f *fakeStore) IncrementResumeCountV2(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	op, ok := f.ops[id]
	if !ok {
		return nil
	}
	op.ResumeCount++
	f.ops[id] = op
	return nil
}

func (f *fakeStore) InsertOpStrikeV2(row database.OpStrikeV2Row) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.strikes = append(f.strikes, row)
	return nil
}

func (f *fakeStore) GetOpStateV2(opID string) (*database.OpStateV2Row, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.states[opID]
	if !ok {
		return nil, nil
	}
	return &st, nil
}

func (f *fakeStore) DeleteOpStateV2(opID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.states, opID)
	return nil
}

// strikesOfKind returns strikes of a given kind for an op.
func (f *fakeStore) strikesOfKind(opID, kind string) []database.OpStrikeV2Row {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []database.OpStrikeV2Row
	for _, s := range f.strikes {
		if s.OperationID == opID && s.Kind == kind {
			out = append(out, s)
		}
	}
	return out
}

// setLastProgressAt allows tests to simulate stale progress timestamps.
func (f *fakeStore) setLastProgressAt(opID string, t *time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	op, ok := f.ops[opID]
	if !ok {
		return
	}
	op.LastProgressAt = t
	f.ops[opID] = op
}

// setStartedAt allows tests to simulate started_at.
func (f *fakeStore) setStartedAt(opID string, t *time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	op, ok := f.ops[opID]
	if !ok {
		return
	}
	op.StartedAt = t
	f.ops[opID] = op
}

// setResumeCount sets the resume_count for an op.
func (f *fakeStore) setResumeCount(opID string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	op, ok := f.ops[opID]
	if !ok {
		return
	}
	op.ResumeCount = n
	f.ops[opID] = op
}

// statusOf is a helper for tests to read an op's current status.
func (f *fakeStore) statusOf(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if op, ok := f.ops[id]; ok {
		return op.Status
	}
	return ""
}

// --- UOS-03 reporter methods ---

func (f *fakeStore) UpdateOpProgressV2(id string, current, total int, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	op, ok := f.ops[id]
	if !ok {
		return nil
	}
	op.ProgressCurrent = current
	op.ProgressTotal = total
	op.ProgressMessage = message
	f.ops[id] = op
	return nil
}

func (f *fakeStore) UpdateOpPhaseV2(id string, phase *string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	op, ok := f.ops[id]
	if !ok {
		return nil
	}
	op.CurrentPhase = phase
	f.ops[id] = op
	return nil
}

func (f *fakeStore) UpdateOpCheckpointV2(id string, newHWM int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	op, ok := f.ops[id]
	if !ok {
		return nil
	}
	now := time.Now()
	op.LastCheckpointAt = &now
	if newHWM > op.HighWaterProgress {
		op.HighWaterProgress = newHWM
	}
	f.ops[id] = op
	return nil
}

func (f *fakeStore) AppendOpLogsV2(rows []database.OpLogV2Row) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, rows...)
	return nil
}

func (f *fakeStore) InsertOpErrorV2(row database.OpErrorV2Row) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errors = append(f.errors, row)
	return nil
}

func (f *fakeStore) UpsertOpStateV2(row database.OpStateV2Row) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[row.OperationID] = row
	return nil
}

func (f *fakeStore) UpdateOperationV2Params(id string, params []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	op, ok := f.ops[id]
	if !ok {
		return nil // best-effort
	}
	op.Params = string(params)
	f.ops[id] = op
	return nil
}

// logsFor returns all log rows for a given operation ID.
func (f *fakeStore) logsFor(opID string) []database.OpLogV2Row {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []database.OpLogV2Row
	for _, l := range f.logs {
		if l.OperationID == opID {
			result = append(result, l)
		}
	}
	return result
}

// errorsFor returns all error rows for a given operation ID.
func (f *fakeStore) errorsFor(opID string) []database.OpErrorV2Row {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []database.OpErrorV2Row
	for _, e := range f.errors {
		if e.OperationID == opID {
			result = append(result, e)
		}
	}
	return result
}

// progressOf returns the current progress fields for a given op.
func (f *fakeStore) progressOf(id string) (current, total int, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	op, ok := f.ops[id]
	if !ok {
		return 0, 0, ""
	}
	return op.ProgressCurrent, op.ProgressTotal, op.ProgressMessage
}

// ListOperationsV2Since returns ops queued at or after the given time, up to limit rows.
func (f *fakeStore) ListOperationsV2Since(since time.Time, limit int) ([]database.OperationV2Row, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit <= 0 {
		limit = 200
	}
	var result []database.OperationV2Row
	for _, op := range f.ops {
		if !op.QueuedAt.Before(since) {
			result = append(result, op)
		}
	}
	// Sort: started_at DESC NULLS LAST, queued_at DESC
	sort.Slice(result, func(i, j int) bool {
		iNil := result[i].StartedAt == nil
		jNil := result[j].StartedAt == nil
		if iNil != jNil {
			return !iNil // non-nil (has started_at) sorts first
		}
		if !iNil && !jNil {
			if !result[i].StartedAt.Equal(*result[j].StartedAt) {
				return result[i].StartedAt.After(*result[j].StartedAt)
			}
		}
		return result[i].QueuedAt.After(result[j].QueuedAt)
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// GetOpLogsV2 returns the last limit log lines for the given operation ID.
func (f *fakeStore) GetOpLogsV2(opID string, limit int) ([]database.OpLogV2Row, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []database.OpLogV2Row
	for _, l := range f.logs {
		if l.OperationID == opID {
			result = append(result, l)
		}
	}
	// Sort ASC by created_at.
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	if limit > 0 && len(result) > limit {
		result = result[len(result)-limit:]
	}
	return result, nil
}

// --- UOS M1 dependency-scheduling (configurable for M3 tests) ---

func (f *fakeStore) GetDepRev(sub database.OpSubject) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.depRevs[sub.Type+":"+sub.ID], nil
}

func (f *fakeStore) BumpDepRev(sub database.OpSubject) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := sub.Type + ":" + sub.ID
	f.depRevs[key]++
	return f.depRevs[key], nil
}

func (f *fakeStore) ListWaitingDepsOps() ([]database.OperationV2Row, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []database.OperationV2Row
	for _, op := range f.ops {
		if op.Status == "waiting_deps" {
			result = append(result, op)
		}
	}
	return result, nil
}

func (f *fakeStore) RecordOpCompletion(sub database.OpSubject, opType, _ string, depRev uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completions[sub.Type+":"+sub.ID+":"+opType] = depRev
	return nil
}

func (f *fakeStore) GetOpCompletion(sub database.OpSubject, opType string) (uint64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := sub.Type + ":" + sub.ID + ":" + opType
	rev, ok := f.completions[key]
	return rev, ok, nil
}

func (f *fakeStore) ListFileCompletions(_ database.OpSubject, _ string) (map[string]uint64, error) {
	return nil, nil
}

// GetBookByID satisfies DepStore (M2). Returns (nil, nil) — fakeStore has no
// book storage; ReqFieldSet requirements are treated as unmet (conservative).
func (f *fakeStore) GetBookByID(_ string) (*database.Book, error) {
	return nil, nil
}

// PromoteToQueued transitions the op from waiting_deps → queued in memory.
// The fake uses a linear scan (no opv2:q: index needed — ListQueuedOperationsV2
// already scans by status), but it correctly rejects non-waiting_deps ops
// to mirror PebbleStore's pre-condition check.
func (f *fakeStore) PromoteToQueued(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	op, ok := f.ops[id]
	if !ok {
		return fmt.Errorf("fakeStore: PromoteToQueued: op %s not found", id)
	}
	if op.Status != "waiting_deps" {
		return fmt.Errorf("fakeStore: PromoteToQueued: expected status %q, got %q for op %s",
			"waiting_deps", op.Status, id)
	}
	op.Status = "queued"
	f.ops[id] = op
	return nil
}

// --- M3 batch bucket ---

func (f *fakeStore) AddToBatchBucket(opType string, sub database.OpSubject) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := opType + ":" + sub.Type + ":" + sub.ID
	if _, exists := f.batchBucket[key]; exists {
		return nil // idempotent
	}
	f.batchBucket[key] = database.BatchBucketEntry{
		Sub:     sub,
		AddedAt: timeNowNano(),
	}
	return nil
}

func (f *fakeStore) ListBatchBucket(opType string) ([]database.BatchBucketEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := opType + ":"
	var result []database.BatchBucketEntry
	for k, v := range f.batchBucket {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			result = append(result, v)
		}
	}
	return result, nil
}

func (f *fakeStore) ClearBatchBucket(opType string, subs []database.OpSubject) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, sub := range subs {
		delete(f.batchBucket, opType+":"+sub.Type+":"+sub.ID)
	}
	return nil
}

// timeNowNano is a variable so tests can override the clock.
var timeNowNano = func() int64 { return time.Now().UnixNano() }
