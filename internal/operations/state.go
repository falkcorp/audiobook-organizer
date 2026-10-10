// file: internal/operations/state.go
// version: 1.8.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-10-09

package operations

import (
	"encoding/json"
	"time"
)

// OperationState is the resumable checkpoint for any operation.
type OperationState struct {
	OperationID string    `json:"operation_id"`
	Type        string    `json:"type"`        // "scan", "organize", "itunes_import"
	Phase       string    `json:"phase"`       // e.g. "grouping", "importing", "enriching", "organizing"
	PhaseIndex  int       `json:"phase_index"` // current item index within phase
	PhaseTotal  int       `json:"phase_total"` // total items in current phase
	Status      string    `json:"status"`      // "running", "interrupted"
	UpdatedAt   time.Time `json:"updated_at"`
}

// BulkMetadataFetchParams stores the immutable parameters for a bulk metadata fetch operation.
// PreferAudible=true moves Audible to the front of the source chain.
// SkipCached=true skips books that already have a valid (non-expired) cache entry.
type BulkMetadataFetchParams struct {
	PreferAudible bool `json:"prefer_audible"`
	SkipCached    bool `json:"skip_cached"`
}

// The state helpers below take one-method interfaces rather than
// database.OperationStore (30 methods) or database.Store (398). Each function
// uses exactly one method, and the parameter type is what propagates width: a
// caller must hold something satisfying the declared type, so a wide parameter
// forces every caller — and every interface those callers declare — to carry the
// whole surface. organizer.Store embedded database.OperationStore for precisely
// this reason: not because the organizer needs 30 operation methods, but because
// it passed its store to the params writer and ClearState.
//
// Widening any of these back is a decision with a blast radius, not a
// convenience: prefer adding a new narrow interface beside them.

// OperationStateWriter persists the raw checkpoint blob for an operation.
type OperationStateWriter interface {
	SaveOperationState(opID string, state []byte) error
}

// OperationStateReader reads the raw checkpoint blob for an operation.
type OperationStateReader interface {
	GetOperationState(opID string) ([]byte, error)
}

// OperationStateDeleter removes an operation's persisted state.
type OperationStateDeleter interface {
	DeleteOperationState(opID string) error
}

// SaveCheckpoint persists an operation's progress checkpoint.
func SaveCheckpoint(store OperationStateWriter, opID, opType, phase string, index, total int) error {
	state := OperationState{
		OperationID: opID,
		Type:        opType,
		Phase:       phase,
		PhaseIndex:  index,
		PhaseTotal:  total,
		Status:      "running",
		UpdatedAt:   time.Now(),
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return store.SaveOperationState(opID, data)
}

// LoadCheckpoint loads an operation's progress checkpoint. Returns nil if none exists.
func LoadCheckpoint(store OperationStateReader, opID string) (*OperationState, error) {
	data, err := store.GetOperationState(opID)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	var state OperationState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// ClearState removes all persisted state for an operation (called on completion/failure).
func ClearState(store OperationStateDeleter, opID string) error {
	return store.DeleteOperationState(opID)
}
