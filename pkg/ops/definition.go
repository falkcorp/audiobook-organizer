// file: pkg/ops/definition.go
// version: 1.0.1
// guid: 29dda49d-c6fc-430a-ad07-1bfa986227a7
// last-edited: 2026-10-13

// Package ops is the operations v3 authoring SDK
// (docs/proposals/2026-10-holistic/05-operations-v3/sdk-api.md). An author
// builds a Definition with Task, Batch, Fixer or Pipeline, lists it in the
// package's Bundle, and the executor (internal/operations/registry) runs it.
//
// The package owns its own types: nothing here is a type alias onto the
// runtime (v2's pkg/plugin/sdk is a wall of aliases onto the registry). It
// never imports the registry or the Writer implementation
// (internal/operations/opswriter); both of those import it.
package ops

import (
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
)

// Definition is what the catalog holds. It is opaque: authors build one with
// Task, Batch, Fixer or Pipeline, never with a struct literal, so a default
// can change without editing every call site.
type Definition interface {
	ID() string
	Kind() Kind
	spec() *baseSpec // unexported: only this package can implement Definition
}

// Kind is the shape of a definition.
type Kind int

const (
	// KindTask is one function that runs once.
	KindTask Kind = iota + 1
	// KindBatch is a source of items and a per-item function; the runner owns the loop.
	KindBatch
	// KindFixer is plan -> approve -> apply over rows (generalized internal/repairs).
	KindFixer
	// KindPipeline is a DAG of child definitions with typed hand-off.
	KindPipeline
	// KindAdapted is an unchanged v2 OperationDef held by the catalog
	// (registry.FromV2). The executor runs it exactly as before; pkg/ops
	// never runs it. It goes away with the adapter (05-PR15).
	KindAdapted
)

// String names the kind for logs and the def list.
func (k Kind) String() string {
	switch k {
	case KindTask:
		return "task"
	case KindBatch:
		return "batch"
	case KindFixer:
		return "fixer"
	case KindPipeline:
		return "pipeline"
	case KindAdapted:
		return "adapted"
	default:
		return "unknown"
	}
}

// Common is the part every kind shares. ID (the constructor argument),
// Title, Effects and Permission are required.
type Common struct {
	// Title is shown in the UI. Required.
	Title string
	// Help is the 1-3 sentence description shown in the def list and the
	// generated form. oplint warns when it is empty.
	Help string
	// Effects declares what the op reads and writes. Required: ReadOnly() or
	// Writes(...). It drives the write-set gate, the preview default and
	// whether a Writer is handed out at all.
	Effects Effects
	// Exclusive names the mutual-exclusion key. Default: the def ID. Ops that
	// must serialize with another op name a shared key; library.scan keeps
	// exactly "library.scan".
	Exclusive string
	// Priority defaults to PriorityNormal.
	Priority Priority
	// Timeout defaults by kind: Task 30m, Batch 24h, Fixer 6h (the apply
	// budget; one executor def carries one timeout, see DefaultTimeout).
	Timeout time.Duration
	// Permission is the permission a person needs to trigger the def.
	// Required on every native def: ValidateCatalog refuses an empty one
	// (sdk-api.md §11.1, "Permissions are mandatory"). A def meant to be open
	// to editors says so explicitly.
	Permission auth.Permission
	// Schedule is the ONE place a timer is declared. Nil = manual or child only.
	Schedule *Schedule
	// Resume overrides the kind's default resume policy.
	Resume *ResumePolicy
	// Cancel overrides the kind's default cancel contract.
	Cancel *CancelContract
	// Notify defaults to NotifyAlert for manual runs and NotifyActivity for
	// scheduled ones.
	Notify Notify
	// FormerIDs keeps renames working (v2 aliases.go).
	FormerIDs []string
	// Uses lists the remote lanes this op may hand items to. Audio decoding
	// is allowed ONLY on LaneMac.
	Uses []Lane
}

// Priority orders dispatch. The zero value means PriorityNormal; the three
// values convert 1:1 by name to the registry's Low/Normal/High.
type Priority int

const (
	// PriorityLow runs after everything else that is queued.
	PriorityLow Priority = iota + 1
	// PriorityNormal is the default.
	PriorityNormal
	// PriorityHigh runs first.
	PriorityHigh
)

// Notify says where a run appears. The zero value means "by trigger":
// NotifyAlert for a manual run, NotifyActivity for a scheduled one.
type Notify int

const (
	// NotifyAlert shows in the bell badge and the activity timeline.
	NotifyAlert Notify = iota + 1
	// NotifyActivity shows in the activity timeline only.
	NotifyActivity
)

// ServiceKey names a service a Bundle needs at startup.
type ServiceKey string

// Bundle is what a plugin package exports, one per package.
type Bundle struct {
	Plugin string
	Defs   []Definition
	// Requires lists the services the bundle needs at startup; the catalog
	// fails boot if one is missing instead of registering a def that panics.
	Requires []ServiceKey
}

// Origin is the opaque executor value an adapted (KindAdapted) definition
// carries: the unchanged v2 def. Only the executor implements it.
type Origin interface {
	// AdaptedDefID is the v2 def's ID.
	AdaptedDefID() string
}

// Adapted wraps an executor-native definition so the catalog can hold it
// next to native ones. internal/operations/registry.FromV2 is the only
// intended caller; c carries the v2 def's title, help, permission and
// exclusive key so the catalog can list it. Its behaviour is the executor's
// and does not change.
func Adapted(id string, c Common, origin Origin) Definition {
	s := newSpec(id, KindAdapted, c)
	s.origin = origin
	if origin == nil {
		s.problem("adapted definition has no origin")
	}
	return s
}

// baseSpec is the one concrete Definition. Each constructor fills the part
// for its kind; the executor reads it through Compile.
type baseSpec struct {
	id     string
	kind   Kind
	common Common
	// problems are programmer errors found at construction. Constructors do
	// not panic: ValidateCatalog and the executor's Register report them.
	problems []string

	task   taskFunc
	batch  batchFunc
	conc   Concurrency
	stages []CompiledStage
	origin Origin
}

func newSpec(id string, k Kind, c Common) *baseSpec {
	s := &baseSpec{id: id, kind: k, common: c}
	if id == "" {
		s.problem("empty id")
	}
	if c.Title == "" {
		s.problem("empty Title")
	}
	if c.Schedule != nil && c.Schedule.err != nil {
		s.problem("invalid schedule: " + c.Schedule.err.Error())
	}
	return s
}

func (s *baseSpec) problem(msg string) { s.problems = append(s.problems, msg) }

// ID implements Definition.
func (s *baseSpec) ID() string { return s.id }

// Kind implements Definition.
func (s *baseSpec) Kind() Kind { return s.kind }

func (s *baseSpec) spec() *baseSpec { return s }

// DefaultTimeout is the kind's default Common.Timeout.
func DefaultTimeout(k Kind) time.Duration {
	switch k {
	case KindTask:
		return 30 * time.Minute
	case KindBatch, KindPipeline:
		return 24 * time.Hour
	case KindFixer:
		return 6 * time.Hour
	default:
		return 0
	}
}
