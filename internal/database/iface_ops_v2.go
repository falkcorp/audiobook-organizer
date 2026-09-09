// file: internal/database/iface_ops_v2.go
// version: 2.13.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-09-09

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

// AsLegacyOperation maps a v2 row onto the v1 Operation shape.
//
// WHY THE V1 SHAPE SURVIVES THE V1 KEYSPACE. Several endpoints put an Operation
// straight into their response bodies and the frontend parses those fields, so
// the shape is a wire contract independent of which keyspace the row came from.
// Handing back a different shape for a v2-keyed run would break the client for
// what is now every run.
//
// legacyType is what the response advertises as `type`. A run's KIND did not
// change when its id did, and the frontend keys off these strings — callers
// with a fixed wire type pass that constant, callers that display the def name
// pass row.DefID.
//
// This is the shared translator. Package-private copies exist in `server`
// (reconcileV2RowAsOperation) and `metabatch` (inline in ResolveCandidateFetch);
// they predate this one and should converge onto it. Do not add a fourth.
func (r *OperationV2Row) AsLegacyOperation(legacyType string) *Operation {
	if r == nil {
		return nil
	}
	op := &Operation{
		ID:           r.ID,
		Type:         legacyType,
		Status:       r.Status,
		Progress:     r.ProgressCurrent,
		Total:        r.ProgressTotal,
		Message:      r.ProgressMessage,
		CreatedAt:    r.QueuedAt,
		StartedAt:    r.StartedAt,
		CompletedAt:  r.CompletedAt,
		ErrorMessage: r.ErrorMessage,
		ResultData:   r.ResultData,
	}
	if r.ActorUserID != nil {
		op.UserID = *r.ActorUserID
	}
	return op
}

// OpStrikeV2Row is a single row in op_strikes_v2.
type OpStrikeV2Row struct {
	DefID       string
	OperationID string
	Kind        string // "uncheckpointed" | "stuck" | "never_reported" | "infinite_restart"
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

// OpDefV2Store persists the registered OperationDefs.
type OpDefV2Store interface {
	// UpsertOpDefinitionV2 inserts or replaces a definition row.
	UpsertOpDefinitionV2(row OpDefinitionV2Row) error
	// DeleteOrphanOpDefsV2 removes rows not in the keepIDs set.
	DeleteOrphanOpDefsV2(keepIDs []string) error
}

// OpV2LifecycleStore creates a v2 operation and moves it through its statuses.
//
// Composed of two leaves rather than declared flat: the method set is
// byte-identical, but a flat declaration is 9 entries and `interfacebloat` caps
// a declaration at 8. The seam is semantic, not arbitrary -- OpV2StatusStore is
// exactly the writers of the status/completed_at pair, which is the field group
// with the subtle invariant (see isTerminalV2Status in pebble_store_ops_v2.go).
type OpV2LifecycleStore interface {
	OpV2RunStore
	OpV2StatusStore
}

// OpV2RunStore creates a run row and updates the payload fields hanging off it.
// Nothing here decides whether an operation is live or finished.
type OpV2RunStore interface {
	// InsertOperationV2 inserts a new queued run.
	InsertOperationV2(row OperationV2Row) error
	// GetOperationV2 returns a single run by id.
	GetOperationV2(id string) (*OperationV2Row, error)
	// UpdateOperationV2Params replaces the params blob on an operation row.
	// Used by resumeRestart to inject checkpoint state before re-dispatch.
	UpdateOperationV2Params(id string, params []byte) error
	// IncrementResumeCountV2 atomically increments resume_count for the given op.
	IncrementResumeCountV2(id string) error
	// SetOperationV2Result stores an operation's final result payload.
	//
	// This is a first-class v2 capability rather than an optional one discovered by
	// type assertion (the pattern legacyOpStore uses): an implementation that
	// silently lacked it would DROP results, and this method exists precisely to
	// give v2 ops somewhere to put output that today only the v1 row can hold.
	// Widening this interface makes the compiler name every fake that needs it.
	//
	// Returns an error when the row does not exist. Callers must not discard it —
	// a swallowed "operation not found" is how a result goes missing with no signal.
	SetOperationV2Result(id string, resultData string) error
}

// OpV2StatusStore holds every writer of an operation's status and its
// completed_at stamp -- the pair that decides whether the rest of the system
// treats the op as live. Grouping them makes that invariant reviewable in one
// place: CompletedAt is the canonical liveness signal (see
// ListOperationsV2Since), so a status write that forgets the stamp produces a
// row that is dead to the worker and alive to every reader.
type OpV2StatusStore interface {
	// UpdateOperationV2Status sets the status (and optional timestamps).
	// startedAt / completedAt are set when non-nil.
	UpdateOperationV2Status(id, status string, startedAt, completedAt *time.Time, errMsg *string) error
	// ResetOperationV2ForResume flips an interrupted op back to "queued" for a
	// ResumeRestart resume and CLEARS CompletedAt (and the interrupt error) to
	// nil — something UpdateOperationV2Status cannot do (nil means "leave
	// unchanged"). Without it a resumed op keeps its interrupt-time CompletedAt
	// and stays invisible in the Active-Operations timeline. QueuedAt is left
	// untouched (it feeds restart-strike accounting).
	ResetOperationV2ForResume(id string) error
	// SetOperationV2StatusIfQueued atomically sets status=canceled only if status was queued.
	// Returns true if the row was updated.
	SetOperationV2StatusIfQueued(id, newStatus string) (bool, error)
	// RepairOpsV2MissingCompletedAt stamps completed_at on rows that hold a
	// terminal status with completed_at null -- rows that are dead to the worker
	// but read as in-flight to every consumer. Returns the number written.
	RepairOpsV2MissingCompletedAt() (int, error)
}

// OpV2QueueStore covers queue and scheduling reads.
type OpV2QueueStore interface {
	// ListQueuedOperationsV2 returns queued ops ordered by priority DESC, queued_at ASC.
	ListQueuedOperationsV2() ([]OperationV2Row, error)
	// ListActiveOperationsV2 returns ops with status 'queued' or 'running'.
	//
	// This is the IN-FLIGHT set, read from the opv2:act: index. It is NOT the
	// resume set: an interrupted_quiesced row has already been removed from that
	// index and will never appear here. Use ListResumableOperationsV2 for the
	// startup resume sweep.
	ListActiveOperationsV2() ([]OperationV2Row, error)
	// ListResumableOperationsV2 returns ops the startup resume sweep should
	// consider: 'queued', 'running', or 'interrupted_quiesced'. Scans the full
	// opv2:op: keyspace rather than the active index, because a quiesced row is
	// precisely the row the index has dropped.
	ListResumableOperationsV2() ([]OperationV2Row, error)
	// ListOperationsV2Since returns all operations whose queued_at timestamp is
	// at or after the given time, ordered by started_at DESC NULLS LAST,
	// queued_at DESC. At most limit rows are returned (0 = use a safe default).
	ListOperationsV2Since(since time.Time, limit int) ([]OperationV2Row, error)
	// CountRunningByPluginV2 returns the number of running ops for a plugin.
	CountRunningByPluginV2(plugin string) (int, error)
	// PromoteToQueued atomically transitions an operation from "waiting_deps"
	// to "queued", writing both the row JSON and the opv2:q: queue-index key
	// (identical encoding to InsertOperationV2 for a queued op) so that
	// ListQueuedOperationsV2 can discover the promoted op.
	// Returns an error if the op does not exist or its status is not "waiting_deps".
	PromoteToQueued(id string) error
	// ListWaitingDepsOps returns all OperationV2Row entries whose Status is
	// "waiting_deps".  Used by the dependency evaluator to re-check parked ops.
	ListWaitingDepsOps() ([]OperationV2Row, error)
}

// OpV2StateStore persists resumable state and checkpoints.
type OpV2StateStore interface {
	// GetOpStateV2 returns the state blob for an op, or nil if none.
	GetOpStateV2(opID string) (*OpStateV2Row, error)
	// DeleteOpStateV2 removes the state blob for an op (used by ResumeRequeue).
	DeleteOpStateV2(opID string) error
	// UpsertOpStateV2 inserts or replaces a checkpoint row in op_state_v2.
	UpsertOpStateV2(row OpStateV2Row) error
	// UpdateOpCheckpointV2 sets last_checkpoint_at and updates high_water_progress
	// to MAX(old, newHWM).
	UpdateOpCheckpointV2(id string, newHWM int) error
}

// OpV2ObservabilityStore covers progress, phase, logs, errors and strikes.
type OpV2ObservabilityStore interface {
	// UpdateOpProgressV2 updates the progress columns and last_progress_at.
	UpdateOpProgressV2(id string, current, total int, message string) error
	// SetOpQueuedProgressV2 writes the progress columns on a row that has not
	// started yet, so a queued run can say how much work it is holding. Reports
	// whether the row was queued and therefore written.
	//
	// It deliberately does NOT stamp last_progress_at, and that is the whole
	// reason it exists rather than reusing UpdateOpProgressV2. The watchdog
	// reads last_progress_at as liveness of the CURRENT attempt
	// (registry/watchdog.go): a stamp older than the def's ProgressTimeout
	// makes it write a "stuck" strike and cancel the run. A queued row's
	// summary is stale by construction — the row may wait hours behind its own
	// ConcurrencyKey — so stamping it here would hand the watchdog an
	// enqueue-time clock for a run that had only just started.
	//
	// The queued-only guard is what makes the name true: without it this would
	// overwrite a running op's real progress with a queue-time estimate.
	SetOpQueuedProgressV2(id string, current, total int, message string) (bool, error)
	// UpdateOpPhaseV2 sets (or clears) current_phase on an operation.
	UpdateOpPhaseV2(id string, phase *string) error
	// AppendOpLogsV2 bulk-inserts log rows into op_logs_v2.
	AppendOpLogsV2(rows []OpLogV2Row) error
	// GetOpLogsV2 returns the last limit log lines for the given operation ID,
	// ordered by created_at ASC. A limit ≤ 0 returns all rows.
	GetOpLogsV2(opID string, limit int) ([]OpLogV2Row, error)
	// InsertOpErrorV2 inserts a single row into op_errors_v2.
	InsertOpErrorV2(row OpErrorV2Row) error
	// InsertOpStrikeV2 appends a row to op_strikes_v2.
	InsertOpStrikeV2(row OpStrikeV2Row) error
}

// OpV2DepStore covers the dependency revision counter.
type OpV2DepStore interface {
	// GetDepRev returns the current dependency-revision counter for sub.
	// Returns 0, nil if no counter exists yet (first call before any bump).
	GetDepRev(sub OpSubject) (uint64, error)
	// BumpDepRev atomically increments the dep_rev counter for sub and returns
	// the new value.  The first call on a never-seen subject transitions 0→1.
	BumpDepRev(sub OpSubject) (uint64, error)
}

// OpV2CompletionStore records per-item completion so reruns can skip work.
type OpV2CompletionStore interface {
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
}

// OpV2BatchStore covers the batch bucket used to coalesce work.
type OpV2BatchStore interface {
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

// OpsV2Store covers the UOS v2 schema surface used by the registry.
// Only implemented by SQLiteStore; PebbleStore returns ErrNotSupported.
//
// Split into the 8 interfaces above on 2026-08-18. This name is retained
// as their composition so the method set is byte-identical and no consumer moves;
// the type checker proves it, because every implementation -- PebbleStore (496
// methods) and database.MockStore (399) among them -- fails to compile if a method
// is dropped or re-signatured in the regrouping.
//
// Consumers should migrate to whichever pieces they use; this composition is the
// transitional shape, not the destination.
type OpsV2Store interface {
	OpDefV2Store
	OpV2LifecycleStore
	OpV2QueueStore
	OpV2StateStore
	OpV2ObservabilityStore
	OpV2DepStore
	OpV2CompletionStore
	OpV2BatchStore
}

// BatchBucketEntry is a single pending subject in a batchable op's journal.
// AddedAt records the wall-clock time of first addition so the registry can
// compute whether BatchMaxWait has been exceeded at reload.
type BatchBucketEntry struct {
	Sub     OpSubject `json:"sub"`
	AddedAt int64     `json:"added_at"` // Unix nanoseconds
}
