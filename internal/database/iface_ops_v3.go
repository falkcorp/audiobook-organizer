// file: internal/database/iface_ops_v3.go
// version: 1.3.0
// guid: 2ee5100a-fa87-4b98-9558-0a39c733c64c
// last-edited: 2026-10-13

package database

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
)

// Operations v3 storage contract (docs/proposals/2026-10-holistic/
// 05-operations-v3/state-and-persistence.md section 2). Every run is stored
// as an opv3:run: RunRecord with exact census indexes, while the opv2: row is
// still written in the same Pebble batch (the mirror) so the previous binary
// can be redeployed at any time. Key layout and the write paths are in
// pebble_store_ops_v3.go.

// ErrStaleTransition is returned by TransitionRunV3 when the stored run's
// (State, Attempt, FenceEpoch) does not match the request's Expect* fields.
// Nothing was written.
var ErrStaleTransition = errors.New("opv3: stale transition: the run changed since it was read")

// ErrRunNotFoundV3 is returned by the v3 writers for an id with no run record.
var ErrRunNotFoundV3 = errors.New("opv3: run not found")

// Actor is who caused a run or a transition.
type Actor struct {
	UserID     string `json:"user_id"`
	AuthMethod string `json:"auth_method"` // session | token | system; never derived from an unsigned header
}

// ApprovalRecord is a fixer's approve/reject decision.
type ApprovalRecord struct {
	ApprovedBy Actor     `json:"approved_by"`
	ApprovedAt time.Time `json:"approved_at"`
	Decision   string    `json:"decision"` // approve | reject
	Note       string    `json:"note,omitempty"`
}

// V2Origin is set on a record built from an opv2:op: row rather than by the
// live write paths: by migration 065, or by the boot reconcile
// (ReconcileOpsV3WithV2) rebuilding a record whose row a binary without the
// v2 façade projection changed. It describes that row as it was then;
// RunRecord.V2RowHash, not this, says whether the record is current.
type V2Origin struct {
	Status      string `json:"status"`               // original v2 status string
	HighWater   int64  `json:"high_water,omitempty"` // v2 resume high-water mark
	ResumeCount int    `json:"resume_count,omitempty"`
	ErrMessage  string `json:"err_message,omitempty"`
}

// ChunkProgress is the chunk part of a ProgressSnapshot (Batch/Fixer only).
type ChunkProgress struct {
	Done  int `json:"done"`
	Total int `json:"total"` // for AppendOnly: chunks cut so far
	Size  int `json:"size"`  // ChunkSize in effect
}

// WorkerProgress is one worker's row in a ProgressSnapshot.
type WorkerProgress struct {
	Worker    int       `json:"worker"`     // 0..Concurrency-1
	Chunk     int       `json:"chunk"`      // -1 when idle
	Item      string    `json:"item"`       // Label(current item)
	ItemIndex int       `json:"item_index"` // position inside the chunk
	ChunkSize int       `json:"chunk_size"` // items in this chunk (partition-adjusted)
	LeasedAt  time.Time `json:"leased_at"`
	Heartbeat time.Time `json:"heartbeat"`
	Stuck     bool      `json:"stuck"` // heartbeat older than the item budget
}

// ProgressSnapshot is the typed progress of sdk-api.md section 7 (the brief
// calls it section 6), defined here so pkg/ops (05-PR5) can type-alias it and
// there is one definition.
type ProgressSnapshot struct {
	Phase     string           `json:"phase"`
	Phases    []string         `json:"phases"`     // declared order
	Done      int64            `json:"done"`       // items settled (ok + failed + skipped)
	Total     *int64           `json:"total"`      // null = unknown, never 0/0
	Unit      string           `json:"unit"`       // "books", "files", "rows"
	Failed    int64            `json:"failed"`     // items whose Item returned an error
	Counters  map[string]int64 `json:"counters"`   // ok, failed, skipped:<reason>, op-defined
	Chunks    *ChunkProgress   `json:"chunks"`     // Batch/Fixer only
	Workers   []WorkerProgress `json:"workers"`    // one row per worker, Batch/Fixer only
	RatePerS  float64          `json:"rate_per_s"` // items/s over the last 30 s
	ETA       *time.Duration   `json:"eta"`        // only when Total known
	Current   string           `json:"current"`    // Label(item) of the most recently started item
	UpdatedAt time.Time        `json:"updated_at"`
}

// ItemOutcomeCounts is what a run's opv3:item: rows hold, kept on the run
// record so ItemPage.Total and Truncated are exact without a scan.
type ItemOutcomeCounts struct {
	Stored  int64 `json:"stored"`  // rows under opv3:item:{id}:
	Dropped int64 `json:"dropped"` // outcomes refused by the per-run cap
}

// RunRecord is one run (opv3:run:{id}). Field names follow
// state-and-persistence.md section 2; InterruptedWhy, EventSeq, ItemOutcomes
// and UnmappedV2Status are this PR's additions (see each).
type RunRecord struct {
	SchemaVersion int      `json:"v"` // 3
	ID            string   `json:"id"`
	DefID         string   `json:"def_id"`
	Kind          string   `json:"kind"`
	State         state.V3 `json:"state"`
	WaitReason    string   `json:"wait_reason,omitempty"` // queued only: deps | ready | pause
	StopReason    string   `json:"stop_reason,omitempty"`
	Zombie        bool     `json:"zombie,omitempty"`
	// InterruptedWhy is interrupted's reason (quiesced | crash | legacy).
	InterruptedWhy string           `json:"interrupted_why,omitempty"`
	Mode           string           `json:"mode"` // preview | live
	Trigger        string           `json:"trigger"`
	Params         json.RawMessage  `json:"params"`
	ParentID       string           `json:"parent_id,omitempty"`
	PipelineStage  string           `json:"pipeline_stage,omitempty"`
	SuccessorID    string           `json:"successor_id,omitempty"` // dropped only; migrated v2 requeue rows
	Attempt        int              `json:"attempt"`
	FenceEpoch     uint64           `json:"fence_epoch"`
	IdemKey        string           `json:"idem_key"`
	Actor          Actor            `json:"actor"`
	Approval       *ApprovalRecord  `json:"approval,omitempty"`
	Progress       ProgressSnapshot `json:"progress"`
	Result         json.RawMessage  `json:"result,omitempty"` // small; large results go to op_result:
	Error          string           `json:"error,omitempty"`
	QueuedAt       *time.Time       `json:"queued_at,omitempty"`
	StartedAt      *time.Time       `json:"started_at,omitempty"`
	CompletedAt    *time.Time       `json:"completed_at,omitempty"`
	LastProgressAt *time.Time       `json:"last_progress_at,omitempty"`
	// LastCheckpointAt is the last SaveCheckpointV3 or v2 checkpoint.
	LastCheckpointAt *time.Time `json:"last_checkpoint_at,omitempty"`
	Priority         int        `json:"priority"`
	TraceID          string     `json:"trace_id,omitempty"`
	SpanID           string     `json:"span_id,omitempty"`
	// EventSeq is the seq of the last opv3:evt: row written for this run.
	EventSeq uint64 `json:"evt_seq"`
	// ItemOutcomes counts the run's opv3:item: rows.
	ItemOutcomes *ItemOutcomeCounts `json:"item_outcomes,omitempty"`
	// UnmappedV2Status is the original v2 status of a live v2 write the
	// mapping table does not know (the run is recorded as failed).
	UnmappedV2Status string `json:"unmapped_v2_status,omitempty"`
	// FromV2 is set on records built from a v2 row (V2Origin).
	FromV2 *V2Origin `json:"from_v2,omitempty"`
	// V2RowHash is the hash (opsV3RowHash) of the opv2:op: row bytes this
	// record was last synced with: stamped by every v2 façade write, by every
	// native write while the mirror is on, and by migration 065. A stored row
	// with a different hash was written by something that did not update this
	// record -- a previous binary after a rollback -- and the boot reconcile
	// rebuilds the record from it. Stored on opv3:prog:, not opv3:run:,
	// because every v2 write changes it (pebble_store_ops_v3.go "RUN RECORD
	// SPLIT").
	V2RowHash string `json:"v2_row_hash,omitempty"`
}

// KeyRange is one completed AppendOnly range in a Ledger.
type KeyRange struct {
	First string `json:"first"`
	Last  string `json:"last"`
}

// LedgerItems is a Ledger's settled item counts.
type LedgerItems struct {
	Done    int64 `json:"done"`
	Failed  int64 `json:"failed"`
	Skipped int64 `json:"skipped"`
}

// Ledger is the chunk ledger (D28a): which chunks are complete.
type Ledger struct {
	SchemaVersion int              `json:"v"`
	ChunkSize     int              `json:"chunk_size"`
	ChunkCount    int              `json:"chunk_count"`      // Snapshot: fixed at freeze; AppendOnly: chunks cut so far
	Done          []byte           `json:"done,omitempty"`   // Snapshot: bitmap, bit c = chunk c complete
	Ranges        []KeyRange       `json:"ranges,omitempty"` // AppendOnly: completed {first,last} key ranges, merged
	Items         LedgerItems      `json:"items"`
	Counters      map[string]int64 `json:"counters"`
	// ChunksDone counts completed chunks across every attempt. A Snapshot
	// run can count its bitmap, but merged AppendOnly ranges cannot be
	// counted back into chunks, so the runner persists the number (05-PR5).
	ChunksDone int `json:"chunks_done,omitempty"`
	// Marks are the keys items recorded with rc.Mark, by name, from
	// completed chunks only (a Fixer's applied rows), so they survive a
	// resume exactly as the counters do.
	Marks     map[string][]string `json:"marks,omitempty"`
	UpdatedAt time.Time           `json:"updated_at"`
}

// Lease is one chunk a worker holds. Leases live in the runner's memory and
// are copied into opv3:ckpt: with every ledger write.
type Lease struct {
	Chunk     int       `json:"chunk"`
	Worker    int       `json:"worker"`
	LeasedAt  time.Time `json:"leased_at"`
	Heartbeat time.Time `json:"heartbeat"`
	ItemIndex int       `json:"item_index"`
	Item      string    `json:"item"` // Label(current item)
}

// Checkpoint is opv3:ckpt:{id}.
type Checkpoint struct {
	SchemaVersion int             `json:"v"`
	Phase         string          `json:"phase,omitempty"`
	State         json.RawMessage `json:"state,omitempty"`        // rc.Checkpoint payload (Tasks)
	Leases        []Lease         `json:"leases,omitempty"`       // copy of the runner's lease table
	LegacyState   []byte          `json:"legacy_state,omitempty"` // migrated opv2:state: blob
	SavedAt       time.Time       `json:"saved_at"`
}

// Intent is a write-ahead record (opswriter, PR 5), deleted on commit.
type Intent struct {
	Seq       uint64          `json:"seq"`
	Subject   string          `json:"subject"` // "book:<id>", "book_file:<id>"
	Kind      string          `json:"kind"`    // modify | repoint | effect
	Planned   json.RawMessage `json:"planned"` // what is about to be written
	WrittenAt time.Time       `json:"written_at"`
}

// Transition is one opv3:evt: row: the state-machine audit trail.
type Transition struct {
	Seq        uint64    `json:"seq"`
	From       state.V3  `json:"from"`
	To         state.V3  `json:"to"`
	Event      string    `json:"event"`
	Reason     string    `json:"reason,omitempty"` // stop / wait / interrupted reason
	Attempt    int       `json:"attempt"`
	FenceEpoch uint64    `json:"fence_epoch"`
	Actor      *Actor    `json:"actor,omitempty"`
	At         time.Time `json:"at"`
}

// LogLine is one opv3:log: row (same shape as opv2:log:). Key is filled on
// read only (the storage key, used as ListLogV3's afterKey cursor).
type LogLine struct {
	At      time.Time         `json:"at"`
	Level   string            `json:"level"` // info | warn | error
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
	Key     string            `json:"-"`
}

// LedgerBatch is everything CommitLedgerV3 writes in one Pebble batch.
type LedgerBatch struct {
	Ledger   Ledger           // opv3:ledger:
	Leases   []Lease          // stored inside opv3:ckpt:
	Progress ProgressSnapshot // folded into RunRecord.Progress
	ItemRows []ItemOutcome    // optional; same batch
}

// OpCounts is opv3:meta:counts, maintained in the same batch as the ix:state
// write.
type OpCounts struct {
	ByState map[state.V3]int64 `json:"by_state"`
	ByDef   map[string]int64   `json:"by_def"` // all states
	Total   int64              `json:"total"`
}

// SchedState is opv3:sched:{def_id}, written by 05-PR8.
type SchedState struct {
	DefID     string     `json:"def_id"`
	LastFire  *time.Time `json:"last_fire,omitempty"`
	NextFire  *time.Time `json:"next_fire,omitempty"`
	LastRunID string     `json:"last_run_id,omitempty"`
	Missed    int64      `json:"missed"`
}

// DefSnapshot is opv3:def:{def_id}, kept for runs whose def was later removed.
type DefSnapshot struct {
	DefID        string          `json:"def_id"`
	Title        string          `json:"title"`
	Kind         string          `json:"kind"`
	Permission   string          `json:"permission"`
	ParamsSchema json.RawMessage `json:"params_schema,omitempty"`
	RemovedAt    time.Time       `json:"removed_at"`
}

// ItemOutcome is one opv3:item: row: a failed or skipped item of a run.
type ItemOutcome struct {
	Key     string `json:"key"`     // item label/key
	Outcome string `json:"outcome"` // failed | skipped:<reason>
	Error   string `json:"error,omitempty"`
	Chunk   int    `json:"chunk"`
}

// ItemQuery selects a page of a run's item outcomes. Outcome is a prefix of
// the outcome string ("skipped" matches every skipped:<reason>); "" is all.
type ItemQuery struct {
	Outcome string
	Cursor  string
	Limit   int
}

// ItemPage is one page of item outcomes.
type ItemPage struct {
	Rows      []ItemOutcome
	Total     int64 // exact count of stored rows matching the query
	Truncated bool  // true when the per-run cap (10,000 rows) dropped some outcomes
	Next      string
}

// TransitionRequest is one native state-machine write (TransitionRunV3).
type TransitionRequest struct {
	ID               string
	ExpectState      state.V3
	ExpectAttempt    int
	ExpectFenceEpoch uint64
	Event            state.Event
	StopReason       string // timeout | user | watchdog | shutdown | quiesce
	WaitReason       string // deps | ready | pause (queued only)
	InterruptedWhy   string // quiesced | crash | legacy
	SuccessorID      string // dropped only
	Error            string
	Result           json.RawMessage
	Zombie           bool
	Actor            *Actor
	Approval         *ApprovalRecord
	Now              time.Time
}

// RunQuery selects runs for ListRunsV3. DefID and State filter; Cursor is the
// Next of a previous page.
type RunQuery struct {
	DefID  string
	State  state.V3
	Cursor string
	Limit  int
}

// RunPage is one page of runs. Total is always exact (counted from the
// index), never derived from the page.
type RunPage struct {
	Runs  []RunRecord
	Total int64
	Next  string
}

// OpsV3RunWriter is the run state machine's write side.
type OpsV3RunWriter interface {
	// InsertRunV3 creates a queued run (event enqueue) and its v2 mirror row.
	InsertRunV3(rec RunRecord) error
	// TransitionRunV3 is the only v3-native writer of run state. It is a
	// compare-and-set on (State, Attempt, FenceEpoch): a request whose
	// Expect* fields do not match the stored record returns ErrStaleTransition.
	// It validates with state.Next, stages the record, indexes, event and
	// counts, and the mirrored opv2: row, in one batch under opsMu.
	TransitionRunV3(req TransitionRequest) (*RunRecord, error)
	// DeleteRunV3 discards a run whose state is in allowed: the record, its
	// indexes, events, checkpoint, snapshot, ledger, intents, item rows and
	// log, plus the mirrored opv2: row and its keys, in one batch.
	DeleteRunV3(id string, allowed []state.V3) (deleted bool, err error)
}

// OpsV3RunReader is the run state machine's read side.
type OpsV3RunReader interface {
	GetRunV3(id string) (*RunRecord, error)
	ListRunsV3(q RunQuery) (RunPage, error) // census: exact Total from ix:state / ix:def
	OpCountsV3() (OpCounts, error)          // opv3:meta:counts
	ListQueuedRunsV3(limit int) ([]RunRecord, error)
	// ListDoneRunsSinceV3 is the timeline window (opv3:ix:done:). complete is
	// false when limit cut the window short, so the API can say so.
	ListDoneRunsSinceV3(since time.Time, limit int) (runs []RunRecord, complete bool, err error)
	ListChildRunsV3(parentID string) ([]RunRecord, error)
	ListRunEventsV3(id string, limit int) ([]Transition, error)
}

// OpsV3ChunkStore is the checkpoint, snapshot and chunk-ledger surface
// (chunk leasing, D28a).
type OpsV3ChunkStore interface {
	SaveCheckpointV3(id string, ck Checkpoint) error
	GetCheckpointV3(id string) (*Checkpoint, error)
	PutSnapshotPageV3(id string, page int, keys []string) error
	GetSnapshotPageV3(id string, page int) ([]string, bool, error)
	PutChunkOffsetsV3(id string, offsets []int32) error
	GetChunkOffsetsV3(id string) ([]int32, bool, error)
	// CommitLedgerV3 writes the ledger, the lease table (inside the checkpoint)
	// and the run's progress snapshot in ONE batch. Only the runner's ledger
	// goroutine calls it. It never touches opv2: (a v2 binary cannot use a
	// ledger).
	CommitLedgerV3(id string, b LedgerBatch) error
	GetLedgerV3(id string) (*Ledger, error)
}

// OpsV3AuxStore is the write-ahead intents, per-item outcomes and run log.
type OpsV3AuxStore interface {
	PutIntentV3(id string, in Intent) error
	ClearIntentV3(id string, seq uint64) error
	ListIntentsV3(id string) ([]Intent, error)
	// PutItemOutcomesV3 stores failed and skipped items (capped at 10,000 rows
	// per run; ItemPage.Truncated reports a drop).
	PutItemOutcomesV3(id string, rows []ItemOutcome) error
	ListItemOutcomesV3(id string, q ItemQuery) (ItemPage, error)
	AppendLogV3(id string, line LogLine) error
	// ListLogV3 reads opv3:log: and, merged in time order, the run's
	// opv2:log: lines (read-through: migrated runs and every run the v2
	// registry still logs for). afterKey is the Key of the last line read.
	ListLogV3(id string, limit int, afterKey string) ([]LogLine, error)
}

// OpsV3Store is the operations v3 storage surface. It is a composition of
// four pieces because interfacebloat caps a declaration at 8 entries; the
// method set is the 05-PR4 brief's OpsV3Store.
//
// It is NOT part of Store (TestStoreFlattenedWidth: Store may only narrow). A
// consumer declares the few methods it calls as its own interface and
// resolves it with AsCapability, which sees through the server's
// search-index decorator to the *PebbleStore (the operations registry's
// runStateV3 is the model).
type OpsV3Store interface {
	OpsV3RunWriter
	OpsV3RunReader
	OpsV3ChunkStore
	OpsV3AuxStore
}
