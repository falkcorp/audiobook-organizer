// file: internal/operations/registry/types.go
// version: 2.4.0
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-06-22

// Package registry provides the UOS-02 in-memory OperationDef registry,
// dispatcher, and in-process worker pool. See the spec at
// docs/superpowers/specs/2026-05-04-unified-operations-system.md.
package registry

import (
	"context"
	"encoding/json"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
)

// OperationDef is the static registration of a unit of async work.
// All fields match the spec §1 contract exactly.
type OperationDef struct {
	// Identity. Required.
	ID          string // globally unique, e.g. "acoustid.fingerprint-extract"
	Plugin      string // owning plugin, e.g. "acoustid"
	DisplayName string // human-readable, shown in UI
	Description string // 1-2 sentences for the plugin detail panel

	// Execution. Required.
	Run             func(ctx context.Context, params json.RawMessage, reporter Reporter) error
	DefaultPriority Priority // PriorityLow | PriorityNormal | PriorityHigh

	// Cancellation. Required.
	Cancellable bool // false = registry's Cancel API rejects; true = ctx.Done() honored

	// Isolation. Required.
	Isolate bool          // true = subprocess; false = in-process goroutine
	Timeout time.Duration // 0 = use defaults (120m in-process, 6h subprocess); cap 24h

	// Resumability. Required (no default — must be explicit).
	ResumePolicy ResumePolicy // ResumeRestart | ResumeRequeue | ResumeDrop | ResumeAsk

	// Watchdog. Optional.
	// MinCheckpointInterval is the maximum gap between Checkpoint calls before a
	// strike is written. Only meaningful when ResumePolicy == ResumeRestart.
	// Zero means "use default 60s".
	MinCheckpointInterval time.Duration
	// ProgressTimeout is the maximum gap between UpdateProgress calls before the
	// op is considered stuck and its context is canceled.
	// Zero means "use default 5m".
	ProgressTimeout time.Duration
	// ProgressFlushInterval controls how often the lazy background goroutine
	// persists the in-memory progress clock to the DB for crash-recovery
	// observability. Zero or negative → default 30s. Greater than 5m → capped
	// at 5m. Synchronous overrides this (no background flush needed).
	ProgressFlushInterval time.Duration
	// Synchronous, if true, writes last_progress_at to the database on every
	// UpdateProgress call (legacy behaviour). Zero-value = false = in-memory
	// atomic clock with lazy periodic DB flush.
	Synchronous bool

	// Concurrency. Required.
	// ConcurrencyKey: ops with same non-empty key serialize; empty = no serialization.
	ConcurrencyKey string

	// MaxConcurrent is set on the Plugin, not the OperationDef (spec §1).
	// Per-plugin caps are tracked in Registry.pluginMax via SetPluginMaxConcurrent.

	// Inputs. Optional.
	ParamsSchema *json.RawMessage // if set, params validated before enqueue

	// Permissions. Optional.
	Permissions  []auth.Permission // user perms required to trigger via API
	Capabilities []Capability      // system capabilities the op needs
	RunsAs       ActorMode         // ActorContext (default) | ActorSystem

	// Scheduling. Optional.
	Schedule *string // cron expression; if set, registry runs on this schedule

	// Triggers. Optional.
	Triggers []EventSubscription // event names this op fires on

	// Dependencies. Optional.
	DependsOn []string // op def IDs that must NOT be running for this op to start

	// Requires are standing prerequisites evaluated before every enqueue of this op.
	// Unlike DependsOn (which means "must NOT run concurrently"), Requires means
	// "these must be SATISFIED first". Op-completed requirements only in M1;
	// field-set (M2) extends the same machinery. An op without Requires behaves
	// exactly as today — no new branches are entered.
	Requires []Requirement

	// Batching. Optional (M3).
	//
	// When Batchable is true, EnqueueOp does NOT immediately create an OperationV2Row.
	// Instead the call's subject is added to a per-op-type in-memory + journaled bucket
	// and a debounce timer is (re-)armed. When the timer fires, one OperationV2Row is
	// created whose params carry all collected subjects as {"subjects":[...]}.
	//
	// Return contract: EnqueueOp for a batchable op returns ("", nil) — there is no
	// op ID yet; the ID is assigned at flush time. Callers that need the resulting op
	// ID should subscribe to the op.created event (once that bus is wired) or use a
	// polling scan. All existing callers either ignore or log the returned ID, so this
	// is safe.
	//
	// Requirement gating: per-enqueue WithRequires cannot be journaled in the bucket
	// (the store method receives only the subject). Requirement evaluation at dispatch
	// uses OperationDef.Requires only. M4 consumers (e.g. dedup.check-book) must
	// therefore declare their requirements on the OperationDef, not per-enqueue.
	Batchable bool

	// BatchWindow is the debounce window: the timer is reset to BatchWindow from the
	// last arrival, but capped so the first flush never waits past BatchMaxWait.
	// Zero with Batchable=true uses defaultBatchWindow (5s).
	BatchWindow time.Duration

	// BatchMaxWait is the hard cap on how long subjects can accumulate before a
	// flush is forced even under a steady trickle of new arrivals.
	// Zero with Batchable=true uses defaultBatchMaxWait (60s).
	BatchMaxWait time.Duration

	// Phases. Optional, for fine-grained resume.
	Phases []Phase // if set, registry tracks phase progress for resume

	// NotifyLevel controls where this op appears. Default (0) = NotifyAlert:
	// shows in the bell badge and the activity timeline. NotifyActivity: shows
	// in the activity timeline only — use for background/single-book ops that
	// don't need to interrupt the user.
	NotifyLevel NotifyLevel
}

// ResumePolicy controls what happens when the server restarts with an
// in-flight run. ResumeUnspecified is forbidden; RegisterOp rejects it.
type ResumePolicy int

const (
	ResumeUnspecified ResumePolicy = iota // forbidden — registry refuses registration
	ResumeRestart                         // reload last checkpoint, call Run again
	ResumeRequeue                         // re-run from zero (idempotent ops only)
	ResumeDrop                            // abandon on restart, mark interrupted_dropped
	ResumeAsk                             // surface in UI, wait for user choice
)

// Priority controls dispatch order within the global worker pool.
type Priority int

const (
	PriorityLow    Priority = 0
	PriorityNormal Priority = 1
	PriorityHigh   Priority = 2
)

// NotifyLevel controls where an operation appears in the UI.
type NotifyLevel int

const (
	// NotifyAlert is the default: shows in the bell badge and activity timeline.
	NotifyAlert NotifyLevel = 0
	// NotifyActivity shows in the activity timeline only; no bell badge.
	// Use for background or single-book ops that don't warrant interrupting the user.
	NotifyActivity NotifyLevel = 1
)

// ActorMode controls the identity under which a triggered run executes.
type ActorMode int

const (
	ActorContext ActorMode = iota // run as the user/system that triggered (default)
	ActorSystem                   // run as system regardless of caller
)

// Capability is a coarse permission an OperationDef declares it needs.
// Declared statically; lint-enforced in v1; runtime-enforced in vNext.
type Capability string

const (
	CapLibraryRead  Capability = "library.read"
	CapLibraryWrite Capability = "library.write"
	CapFilesRead    Capability = "files.read"
	CapFilesWrite   Capability = "files.write"
	CapFilesExecute Capability = "files.execute"

	CapNetworkOpenAI      Capability = "network.openai"
	CapNetworkAudible     Capability = "network.audible"
	CapNetworkOpenLibrary Capability = "network.openlibrary"
	CapNetworkGoogleBooks Capability = "network.googlebooks"
	CapNetworkITunes      Capability = "network.itunes"
	CapNetworkGeneric     Capability = "network.generic"

	CapScheduleCron  Capability = "schedule.cron"
	CapScheduleEvent Capability = "schedule.event"

	CapSubprocessSpawn Capability = "subprocess.spawn"
	CapDBMigrate       Capability = "db.migrate"
)

// EventSubscription wires an event name to a handler on the OperationDef.
// The Handler field is used by the registry's in-process event bus;
// spec §6.4's Filter field is reconciled when the event bus lands (UOS-05).
type EventSubscription struct {
	EventName string
	Handler   func(ctx context.Context, payload any) error
}

// Phase names a logical stage of an operation for fine-grained resume.
// Phase semantics (skip completed phases on restart) land in UOS-03.
type Phase struct {
	Name string
}

// Subject identifies the entity a requirement/completion is about.
// v1 subjects are book-scoped; "file" is reserved for a later milestone.
type Subject struct {
	Type string // "book" (v1); "file" reserved
	ID   string
}

// RequirementKind is the discriminator for a Requirement.
type RequirementKind string

const (
	// ReqOpCompleted requires that op-type X has completed for the subject
	// at the current dep_rev (i.e. since the subject last changed).
	ReqOpCompleted RequirementKind = "op_completed"
	// ReqFieldSet requires that a named field on the subject is non-empty.
	// Defined now for type stability; evaluated starting in M2.
	ReqFieldSet RequirementKind = "field_set"
)

// Requirement is a single prerequisite/condition for an op to become runnable.
// The Kind field selects which remaining fields are meaningful.
type Requirement struct {
	Kind        RequirementKind `json:"kind"`
	OpType      string          `json:"op_type,omitempty"`      // ReqOpCompleted: required op def ID
	Field       string          `json:"field,omitempty"`        // ReqFieldSet: subject field (M2)
	SubjectType string          `json:"subject_type,omitempty"` // override; defaults to the dependent's own subject type
	AllFiles    bool            `json:"all_files,omitempty"`    // ReqOpCompleted + book subject: require completion for ALL files of the book
}

// EnqueueOption is the function-option pattern for EnqueueOp.
type EnqueueOption func(*EnqueueOptions)

// EnqueueOptions carries optional metadata for a new operation run.
type EnqueueOptions struct {
	ParentID     string
	ActorUserID  string
	TraceID      string
	SpanID       string
	ParentSpanID string
	Priority     *Priority
	Requires     []Requirement // per-enqueue requirements added on top of the def's Requires
}

// WithParent sets the parent run ID for trigger lineage.
func WithParent(id string) EnqueueOption {
	return func(o *EnqueueOptions) { o.ParentID = id }
}

// WithActor sets the user ID of the actor triggering the run.
func WithActor(userID string) EnqueueOption {
	return func(o *EnqueueOptions) { o.ActorUserID = userID }
}

// WithPriority overrides the OperationDef's DefaultPriority for this run.
func WithPriority(p Priority) EnqueueOption {
	return func(o *EnqueueOptions) { o.Priority = &p }
}

// WithRequires adds per-enqueue requirements on top of the def's Requires.
// These are evaluated together with OperationDef.Requires before the op is
// admitted to the queue.
func WithRequires(reqs ...Requirement) EnqueueOption {
	return func(o *EnqueueOptions) { o.Requires = append(o.Requires, reqs...) }
}
