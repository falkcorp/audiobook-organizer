// file: internal/operations/registry/deps_scheduler.go
// version: 1.1.2
// guid: a3b4c5d6-e7f8-9a0b-1c2d-3e4f5a6b7c8d
// last-edited: 2026-07-12

// deps_scheduler.go implements the event-driven + sweep re-evaluation loop for
// waiting_deps operations. It is the bridge between op lifecycle events
// (completion, failure) and the parking/promotion machinery built in Tasks 2–4.
//
// Design:
//   - DepsScheduler is a pure coordinator: no goroutines of its own beyond the
//     optional SweepTick ticker (wired by the registry). All event methods are
//     synchronous and safe to call from any goroutine.
//   - Notifications from worker.go are sent async (via goroutine) so the worker's
//     executeRun path is never blocked by DB queries in the scheduler.
//   - The in-memory index of (subjectType, subjectID) → []opID is rebuilt from
//     ListWaitingDepsOps() at construction time and maintained incrementally as
//     ops are promoted or failed.

package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Ensure SchedulerStore embeds DepStore (compile-time check).
var _ DepStore = (SchedulerStore)(nil)

// SchedulerStore is the subset of database.OpsV2Store the scheduler needs.
// It is a superset of DepStore (adds ListWaitingDepsOps and RecordOpCompletion
// and the status-update method).
type SchedulerStore interface {
	DepStore
	ListWaitingDepsOps() ([]database.OperationV2Row, error)
	RecordOpCompletion(sub database.OpSubject, opType, fileID string, depRev uint64) error
	UpdateOperationV2Status(id, status string, startedAt, completedAt *time.Time, errMsg *string) error
	// PromoteToQueued atomically transitions an op from waiting_deps → queued,
	// writing the opv2:q: queue-index key so ListQueuedOperationsV2 finds it.
	PromoteToQueued(id string) error
}

// DepsScheduler coordinates the re-evaluation and promotion of waiting_deps ops.
type DepsScheduler struct {
	mu sync.Mutex
	// index maps "subjectType:subjectID" → set of op IDs in waiting_deps state.
	index  map[string]map[string]struct{}
	store  SchedulerStore
	reg    *Registry
	logger *slog.Logger
}

// NewDepsScheduler creates a DepsScheduler. It does NOT touch the store — the
// waiting_deps index is loaded by the registry's Start() (via rebuildIndex)
// before any goroutine runs. Keeping construction side-effect-free means building
// the registry never calls the store, which matters for the many handler unit
// tests that construct the registry with a mock store and never call Start().
func NewDepsScheduler(reg *Registry, store SchedulerStore) *DepsScheduler {
	return &DepsScheduler{
		index:  make(map[string]map[string]struct{}),
		store:  store,
		reg:    reg,
		logger: reg.logger,
	}
}

// rebuildIndex loads all waiting_deps ops from the store and populates the
// in-memory subject→opIDs index. Safe to call at startup; not concurrent-safe
// (caller must hold mu or call before any concurrent access).
func (s *DepsScheduler) rebuildIndex() {
	ops, err := s.store.ListWaitingDepsOps()
	if err != nil {
		s.logger.Warn("deps_scheduler: failed to load waiting_deps ops", "error", err)
		return
	}
	for _, op := range ops {
		s.addToIndex(op.SubjectType, op.SubjectID, op.ID)
	}
	s.logger.Info("deps_scheduler: loaded waiting_deps index", "count", len(ops))
}

// addToIndex adds opID to the subject index. mu must be held.
func (s *DepsScheduler) addToIndex(subjectType, subjectID, opID string) {
	key := subjectType + ":" + subjectID
	if s.index[key] == nil {
		s.index[key] = make(map[string]struct{})
	}
	s.index[key][opID] = struct{}{}
}

// removeFromIndex removes opID from the subject index. mu must be held.
func (s *DepsScheduler) removeFromIndex(subjectType, subjectID, opID string) {
	key := subjectType + ":" + subjectID
	delete(s.index[key], opID)
}

// OnOpCompleted is called (asynchronously from worker.go) when an op completes
// successfully. It records the completion, then re-evaluates all waiting_deps
// ops for the same subject and promotes those whose requirements are now met.
func (s *DepsScheduler) OnOpCompleted(ctx context.Context, sub Subject, opType string) error {
	dbSub := subjectToOpSubject(sub)

	// Record the completion at the current dep_rev.
	curRev, err := s.store.GetDepRev(dbSub)
	if err != nil {
		return fmt.Errorf("deps_scheduler: GetDepRev(%v): %w", sub, err)
	}
	if err := s.store.RecordOpCompletion(dbSub, opType, "", curRev); err != nil {
		return fmt.Errorf("deps_scheduler: RecordOpCompletion(%v, %q): %w", sub, opType, err)
	}

	s.logger.Info("deps_scheduler: recorded completion; re-evaluating waiting ops",
		"subject_type", sub.Type, "subject_id", sub.ID, "op_type", opType, "dep_rev", curRev)

	return s.reevaluateSubject(ctx, sub)
}

// OnOpFailed is called (asynchronously from worker.go) when an op fails.
// Any waiting_deps op that hard-requires opType for the same subject is failed
// with a propagated error message.
func (s *DepsScheduler) OnOpFailed(ctx context.Context, sub Subject, opType string) error {
	now := time.Now().UTC()
	reason := fmt.Sprintf("unmet dependency: op_type %q failed for %s/%s", opType, sub.Type, sub.ID)

	// Scan all waiting_deps ops for the subject (not just index) so ops parked
	// after scheduler creation are also caught.
	waitingOps, listErr := s.store.ListWaitingDepsOps()
	if listErr != nil {
		s.logger.Warn("deps_scheduler: ListWaitingDepsOps failed during fail-propagation", "error", listErr)
		return nil
	}
	for _, op := range waitingOps {
		if op.SubjectType != sub.Type || op.SubjectID != sub.ID {
			continue
		}
		if !requiresOpType(op.Requirements, opType) {
			continue
		}
		// Fail this op.
		errMsg := reason
		if failErr := s.store.UpdateOperationV2Status(op.ID, "failed", nil, &now, &errMsg); failErr != nil {
			s.logger.Warn("deps_scheduler: failed to mark dep failed",
				"op_id", op.ID, "error", failErr)
			continue
		}
		s.mu.Lock()
		s.removeFromIndex(op.SubjectType, op.SubjectID, op.ID)
		s.mu.Unlock()
		s.logger.Info("deps_scheduler: propagated failure to waiting dep",
			"op_id", op.ID, "failed_op_type", opType, "reason", reason)
	}
	return nil
}

// SweepTick re-evaluates ALL waiting_deps ops. Called periodically (low frequency)
// as a self-healing mechanism and to handle field_set conditions when M2 lands.
func (s *DepsScheduler) SweepTick(ctx context.Context) {
	ops, err := s.store.ListWaitingDepsOps()
	if err != nil {
		s.logger.Warn("deps_scheduler: sweep: ListWaitingDepsOps failed", "error", err)
		return
	}
	s.logger.Info("deps_scheduler: sweep tick", "waiting_count", len(ops))
	for _, op := range ops {
		sub := Subject{Type: op.SubjectType, ID: op.SubjectID}
		if err := s.tryPromote(ctx, op, sub); err != nil {
			s.logger.Warn("deps_scheduler: sweep: tryPromote failed",
				"op_id", op.ID, "error", err)
		}
	}
}

// reevaluateSubject re-evaluates all waiting_deps ops for the given subject.
// It scans all waiting_deps ops (not just those in the in-memory index) so that
// ops parked after the scheduler was created are also considered. The index is
// maintained for efficient sweep but is not the sole source of truth here.
func (s *DepsScheduler) reevaluateSubject(ctx context.Context, sub Subject) error {
	waitingOps, err := s.store.ListWaitingDepsOps()
	if err != nil {
		return fmt.Errorf("deps_scheduler: ListWaitingDepsOps: %w", err)
	}

	for _, op := range waitingOps {
		if op.SubjectType != sub.Type || op.SubjectID != sub.ID {
			continue
		}
		// Ensure it's in the index (may have been parked after rebuildIndex).
		s.mu.Lock()
		s.addToIndex(op.SubjectType, op.SubjectID, op.ID)
		s.mu.Unlock()

		if err := s.tryPromote(ctx, op, sub); err != nil {
			s.logger.Warn("deps_scheduler: tryPromote failed", "op_id", op.ID, "error", err)
		}
	}
	return nil
}

// tryPromote checks whether op's requirements are now satisfied and, if so,
// promotes it from waiting_deps → queued and pings the dispatcher.
func (s *DepsScheduler) tryPromote(ctx context.Context, op database.OperationV2Row, sub Subject) error {
	if op.Requirements == "" {
		// No requirements — promote directly (should not normally be in waiting_deps).
		return s.promote(op, sub)
	}

	var reqs []Requirement
	if err := json.Unmarshal([]byte(op.Requirements), &reqs); err != nil {
		return fmt.Errorf("unmarshal requirements for op %s: %w", op.ID, err)
	}

	// s.store already satisfies DepStore (SchedulerStore embeds it).
	satisfied, _, err := AllSatisfied(s.store, reqs, sub)
	if err != nil {
		return fmt.Errorf("AllSatisfied for op %s: %w", op.ID, err)
	}
	if !satisfied {
		return nil // still waiting
	}
	return s.promote(op, sub)
}

// promote flips status to "queued" via PromoteToQueued (which writes the
// opv2:q: queue-index key), removes from index, and pings the dispatcher.
func (s *DepsScheduler) promote(op database.OperationV2Row, sub Subject) error {
	if err := s.store.PromoteToQueued(op.ID); err != nil {
		return fmt.Errorf("promote op %s: %w", op.ID, err)
	}
	s.mu.Lock()
	s.removeFromIndex(op.SubjectType, op.SubjectID, op.ID)
	s.mu.Unlock()
	s.logger.Info("deps_scheduler: promoted waiting_deps → queued",
		"op_id", op.ID, "subject_type", sub.Type, "subject_id", sub.ID)
	s.reg.pingDispatch()
	return nil
}

// requiresOpType reports whether the Requirements JSON blob contains a
// ReqOpCompleted requirement for the given opType.
func requiresOpType(requirementsJSON, opType string) bool {
	if requirementsJSON == "" {
		return false
	}
	var reqs []Requirement
	if err := json.Unmarshal([]byte(requirementsJSON), &reqs); err != nil {
		return false
	}
	for _, r := range reqs {
		if r.Kind == ReqOpCompleted && r.OpType == opType {
			return true
		}
	}
	return false
}
