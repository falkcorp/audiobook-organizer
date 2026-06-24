// file: internal/database/iface_ops_v2.go
// version: 2.6.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-06-14

package database

import "time"

// OpSubject identifies the entity a dependency-scheduled operation is acting on
// (e.g. a book or a library scan). It is the persisted form of the registry's
// Subject value; the registry converts Subject→OpSubject at its boundary so the
// database package never imports registry.
type OpSubject struct {
	Type string `json:"type"` // e.g. "book", "library"
	ID   string `json:"id"`   // subject's opaque identifier
}

// OpDefinitionV2Row is the DB representation of a registered OperationDef.
type OpDefinitionV2Row struct {
	ID             string
	Plugin         string
	DisplayName    string
	Description    string
	Capabilities   string // JSON array
	Permissions    string // JSON array
	Cancellable    bool
	Isolate        bool
	ResumePolicy   string
	ScheduleCron   *string
	Triggers       string // JSON array
	DependsOn      string // JSON array
	Phases         string // JSON array
	TimeoutSeconds int
	RegisteredAt   time.Time
}

// OperationV2Row is a queued/running/terminal row from operations_v2.
type OperationV2Row struct {
	ID                string
	DefID             string
	Plugin            string
	ParentID          *string
	ActorUserID       *string
	TraceID           string
	SpanID            string
	ParentSpanID      *string
	Status            string
	Priority          int
	ProgressCurrent   int
	ProgressTotal     int
	ProgressMessage   string
	CurrentPhase      *string
	Params            string
	ErrorMessage      *string
	ResultData        *string
	QueuedAt          time.Time
	StartedAt         *time.Time
	CompletedAt       *time.Time
	LastProgressAt    *time.Time
	LastCheckpointAt  *time.Time
	HighWaterProgress int
	ResumeCount       int
	// UOS dependency-scheduling fields (Task 2). Zero values on old rows are safe.
	SubjectType    string // e.g. "book" — the entity this op acts on
	SubjectID      string // opaque ID of the subject
	Requirements   string // JSON array of Requirement objects ([]registry.Requirement)
	ReqSnapshotRev uint64 // dep_rev at the time the requirements were evaluated
}

// OpStrikeV2Row is a single row in op_strikes_v2.
type OpStrikeV2Row struct {
	DefID       string
	OperationID string
	Kind        string // "uncheckpointed" | "stuck" | "infinite_restart"
	Details     string // JSON object with plugin, message, etc.
	OccurredAt  time.Time
}

// OpStateV2Row is a single row in op_state_v2.
type OpStateV2Row struct {
	OperationID   string
	Phase         *string
	StateBlob     []byte
	SchemaVersion int
	WrittenAt     time.Time
}

// OpLogV2Row is a single log line written to op_logs_v2.
type OpLogV2Row struct {
	OperationID string
	Level       string // "debug", "info", "warn", "error"
	Message     string
	Attrs       string // JSON object
	CreatedAt   time.Time
}

// OpErrorV2Row is a persistent error record written to op_errors_v2.
type OpErrorV2Row struct {
	OperationID string
	Plugin      string
	DefID       string
	Message     string
	Attrs       string // JSON object
	OccurredAt  time.Time
}

// OpsV2Store covers the UOS v2 schema surface used by the registry.
// Only implemented by SQLiteStore; PebbleStore returns ErrNotSupported.
type OpsV2Store interface {
	// UpsertOpDefinitionV2 inserts or replaces a definition row.
	UpsertOpDefinitionV2(row OpDefinitionV2Row) error

	// DeleteOrphanOpDefsV2 removes rows not in the keepIDs set.
	DeleteOrphanOpDefsV2(keepIDs []string) error

	// InsertOperationV2 inserts a new queued run.
	InsertOperationV2(row OperationV2Row) error

	// ListQueuedOperationsV2 returns queued ops ordered by priority DESC, queued_at ASC.
	ListQueuedOperationsV2() ([]OperationV2Row, error)

	// ListActiveOperationsV2 returns ops with status 'queued' or 'running'.
	ListActiveOperationsV2() ([]OperationV2Row, error)

	// GetOperationV2 returns a single run by id.
	GetOperationV2(id string) (*OperationV2Row, error)

	// UpdateOperationV2Status sets the status (and optional timestamps).
	// startedAt / completedAt are set when non-nil.
	UpdateOperationV2Status(id, status string, startedAt, completedAt *time.Time, errMsg *string) error

	// SetOperationV2StatusIfQueued atomically sets status=canceled only if status was queued.
	// Returns true if the row was updated.
	SetOperationV2StatusIfQueued(id, newStatus string) (bool, error)

	// CountRunningByPluginV2 returns the number of running ops for a plugin.
	CountRunningByPluginV2(plugin string) (int, error)

	// IncrementResumeCountV2 atomically increments resume_count for the given op.
	IncrementResumeCountV2(id string) error

	// InsertOpStrikeV2 appends a row to op_strikes_v2.
	InsertOpStrikeV2(row OpStrikeV2Row) error

	// GetOpStateV2 returns the state blob for an op, or nil if none.
	GetOpStateV2(opID string) (*OpStateV2Row, error)

	// DeleteOpStateV2 removes the state blob for an op (used by ResumeRequeue).
	DeleteOpStateV2(opID string) error

	// UpdateOpProgressV2 updates the progress columns and last_progress_at.
	UpdateOpProgressV2(id string, current, total int, message string) error

	// UpdateOpPhaseV2 sets (or clears) current_phase on an operation.
	UpdateOpPhaseV2(id string, phase *string) error

	// UpdateOpCheckpointV2 sets last_checkpoint_at and updates high_water_progress
	// to MAX(old, newHWM).
	UpdateOpCheckpointV2(id string, newHWM int) error

	// AppendOpLogsV2 bulk-inserts log rows into op_logs_v2.
	AppendOpLogsV2(rows []OpLogV2Row) error

	// InsertOpErrorV2 inserts a single row into op_errors_v2.
	InsertOpErrorV2(row OpErrorV2Row) error

	// UpsertOpStateV2 inserts or replaces a checkpoint row in op_state_v2.
	UpsertOpStateV2(row OpStateV2Row) error

	// UpdateOperationV2Params replaces the params blob on an operation row.
	// Used by resumeRestart to inject checkpoint state before re-dispatch.
	UpdateOperationV2Params(id string, params []byte) error

	// ListOperationsV2Since returns all operations whose queued_at timestamp is
	// at or after the given time, ordered by started_at DESC NULLS LAST,
	// queued_at DESC. At most limit rows are returned (0 = use a safe default).
	ListOperationsV2Since(since time.Time, limit int) ([]OperationV2Row, error)

	// GetOpLogsV2 returns the last limit log lines for the given operation ID,
	// ordered by created_at ASC. A limit ≤ 0 returns all rows.
	GetOpLogsV2(opID string, limit int) ([]OpLogV2Row, error)

	// --- UOS dependency-scheduling (Task 2) ---

	// GetDepRev returns the current dependency-revision counter for sub.
	// Returns 0, nil if no counter exists yet (first call before any bump).
	GetDepRev(sub OpSubject) (uint64, error)

	// BumpDepRev atomically increments the dep_rev counter for sub and returns
	// the new value.  The first call on a never-seen subject transitions 0→1.
	BumpDepRev(sub OpSubject) (uint64, error)

	// RecordOpCompletion stores a completion record for opType on sub at the
	// given depRev.  fileID is empty for book-level completions; non-empty for
	// per-file completions.
	RecordOpCompletion(sub OpSubject, opType, fileID string, depRev uint64) error

	// GetOpCompletion retrieves the stored depRev for a book-level completion
	// (fileID == "").  Returns (rev, true, nil) when found, (0, false, nil)
	// when absent.
	GetOpCompletion(sub OpSubject, opType string) (rev uint64, ok bool, err error)

	// ListFileCompletions returns a map of fileID→depRev for all per-file
	// completion records for opType on sub.
	ListFileCompletions(sub OpSubject, opType string) (map[string]uint64, error)

	// ListWaitingDepsOps returns all OperationV2Row entries whose Status is
	// "waiting_deps".  Used by the dependency evaluator to re-check parked ops.
	ListWaitingDepsOps() ([]OperationV2Row, error)

	// PromoteToQueued atomically transitions an operation from "waiting_deps"
	// to "queued", writing both the row JSON and the opv2:q: queue-index key
	// (identical encoding to InsertOperationV2 for a queued op) so that
	// ListQueuedOperationsV2 can discover the promoted op.
	// Returns an error if the op does not exist or its status is not "waiting_deps".
	PromoteToQueued(id string) error

	// --- M3 batch bucket (journaled pending subjects) ---
	//
	// Keyspace: op:batch:<opType>:<subjectType>:<subjectID> → JSON(BatchBucketEntry)
	// This journal lets the registry survive a crash mid-window without dropping subjects.
	// Entries are removed by ClearBatchBucket once they have been dispatched.

	// AddToBatchBucket adds a subject to the persistent pending bucket for opType.
	// Idempotent: if an entry already exists for this (opType, subjectType, subjectID)
	// triple, the call is a no-op (the existing AddedAt timestamp is preserved so
	// MaxWait is anchored to the first arrival).
	AddToBatchBucket(opType string, sub OpSubject) error

	// ListBatchBucket returns all pending subjects for opType.
	// Returns an empty slice (not an error) when no bucket exists.
	ListBatchBucket(opType string) ([]BatchBucketEntry, error)

	// ClearBatchBucket removes the given subjects from the bucket for opType.
	// Subjects not present in the bucket are silently skipped.
	ClearBatchBucket(opType string, subs []OpSubject) error
}

// BatchBucketEntry is a single pending subject in a batchable op's journal.
// AddedAt records the wall-clock time of first addition so the registry can
// compute whether BatchMaxWait has been exceeded at reload.
type BatchBucketEntry struct {
	Sub     OpSubject `json:"sub"`
	AddedAt int64     `json:"added_at"` // Unix nanoseconds
}
