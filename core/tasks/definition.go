// file: core/tasks/definition.go
// version: 1.0.1
// guid: 91c4b2c9-bbf5-484e-b81c-2622f4951d42
// last-edited: 2026-11-10

package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/health"
)

// Definition is opaque; build it with Task, Batch or Fixer.
// Constructors never panic: problems are collected and reported by
// module.Registrar at registration, which fails app.New (fail-closed).
type Definition interface {
	Kind() Kind
	Shape() Shape
	spec() *spec // unexported: only package tasks implements Definition
}

// Common is the field set every shape shares.
type Common struct {
	Title string // required
	Help  string // 1-3 sentences; archtest warns when empty

	Effects Effects      // required: ReadOnly(...) or Writes(...)
	Perms   Permissions  // required: Perms.Start at least
	Resume  ResumePolicy // required (A2): zero value refused

	Cancel          CancelContract // zero = shape default (5T.3.6)
	ConcurrencyKey  Key            // zero = Key(Kind)
	NotWhile        []Kind         // v2 DependsOn: must not be running
	Priority        Priority       // zero = PriorityNormal
	Timeout         time.Duration  // per attempt; 0 = shape default; > 24h refused
	ProgressTimeout time.Duration  // watchdog gap; 0 = derived (5T.3.6)
	Schedule        *Schedule      // the ONE timer declaration (5T.3.8); modules use ScheduleFromConfig
	Approval        *Approval      // required for ShapeFixer
	Ready           []ReadySignal  // readiness to wait for before dispatch; nil = {SignalStorage}; ReadySignal = health.ComponentID (5T.4)
	Lanes           []LaneID       // lanes items may be handed to (5T.3.9)
	Subjects        SubjectsFunc   // optional per-run write set (5T.3.4)
	Dedupe          DedupePolicy   // queued-run dedupe/merge (v2 DedupeQueuedRuns/MergeQueuedParams)
	Debounce        *Debounce      // v2 Batchable/BatchWindow/BatchMaxWait
	Yields          bool           // may be quiesced by a stand-down (requires ResumeContinue), 5T.3.7
	Notify          Notify
	// There is no alias field (D10): a Kind is the one name its runs, clocks
	// and Requires stamps are stored and labelled under. A rename is a data
	// migration of the owner's that rewrites the stored kind (5T.8.3).
}

// MaxTimeout caps Common.Timeout (5T.3.6). Name coined by F2-01.
const MaxTimeout = 24 * time.Hour

// DedupePolicy decides whether a start folds into an existing run of the same
// kind instead of inserting a new one: HEAD's DedupeQueuedRuns,
// DedupeSameActorOnly and MergeQueuedParams (the v2 registry's
// types.go:99-125). The zero value is HEAD's default: a start folds into a
// queued or running run of the kind whose params are byte-identical. A start
// that folds inserts nothing and returns the existing run's RunRef.
type DedupePolicy struct {
	AnyParams     bool // fold regardless of params (v2 DedupeQueuedRuns)
	SameActorOnly bool // fold only into a run of the same actor and permission set (v2 DedupeSameActorOnly)
	// Key, when set, replaces the params rule: a start folds only into a QUEUED
	// run whose Key(params) is equal (for example a job ID, 05-ai's batch
	// apply); a running run is never folded into, so a start made while it runs
	// queues behind it under the dispatch conflict rule. "" from Key = never fold.
	Key func(params json.RawMessage) (string, error)
	// Merge may union an incoming start into an existing QUEUED run (v2
	// MergeQueuedParams); never called for a running or claimed run.
	Merge func(existing, incoming json.RawMessage) (json.RawMessage, bool, error)
}

// Priority orders dispatch.
type Priority uint8

const (
	PriorityLow Priority = iota + 1
	PriorityNormal
	PriorityHigh
	// PrioritySystem is core-only (kernel refuses it on module kinds). Such
	// runs dispatch on core.tasks.system_workers reserved workers, so core
	// plumbing (outbox relay, scheduler window runs, updater) is never starved
	// by a saturated executor.
	PrioritySystem
)

// Debounce collects starts of a kind for a window and enqueues one run for
// them (v2 Batchable, BatchWindow, BatchMaxWait: the window restarts on each
// arrival but the first flush never waits past MaxWait). Field names coined
// by F2-01.
type Debounce struct {
	Window  time.Duration // 0 = DefaultDebounceWindow
	MaxWait time.Duration // 0 = DefaultDebounceMaxWait; never below Window
}

// The v2 defaults (the v2 registry's types.go:234,239). Names coined by F2-01.
const (
	DefaultDebounceWindow  = 5 * time.Second
	DefaultDebounceMaxWait = 60 * time.Second
)

// Notify says where a run appears. The zero value means "by trigger":
// NotifyAlert for a manual run, NotifyActivity for a scheduled one.
type Notify uint8

const (
	NotifyAlert    Notify = iota + 1 // the bell badge and the activity timeline
	NotifyActivity                   // the activity timeline only
)

// ReadySignal is a readiness component a definition waits for before
// dispatch (05-health §5H.3.1).
type ReadySignal = health.ComponentID

// SignalStorage is core storage's readiness; the default Ready set.
const SignalStorage ReadySignal = "core.storage"

// LaneID is "<module>.<name>", e.g. "books.audio-decode".
type LaneID string

// Lane is a module-declared capacity class items may be handed to (A5).
type Lane struct {
	ID              LaneID
	Title           string
	Capacity        func() int // current capacity (e.g. connected remote workers); re-read on lease
	NoLocalFallback bool       // work declared for this lane must not run in-process if no capacity
}

// LaneLease is one leased unit of a lane's capacity.
type LaneLease interface {
	Release()
	Context() context.Context // canceled if the lane revokes the lease
}

// KindDial is one entry of the operator dial core.tasks.kinds (5T.10):
// ParallelismCap lowers any declared worker count of the kind at run time.
type KindDial struct {
	ParallelismCap int `json:"parallelism_cap"`
}

// TaskSpec is one function that runs once.
type TaskSpec[P, R any] struct {
	Common
	Run func(rc *RC, p P) (R, error)
}

// BatchSpec is a source plus a per-item function; the runner owns the loop.
type BatchSpec[P, T, R any] struct {
	Common
	Source        func(rc *RC, p P) (Source[T], error)
	Item          func(ic *ItemRC, p P, item T) error // MUST be idempotent
	Finish        func(rc *RC, p P, s Summary) (R, error)
	Parallelism   Parallelism          // required (N8); FromParam allowed here
	PartitionBy   func(T) string       // one key per item: same key -> same worker, in order
	ExclusiveRefs func(T) []entity.Ref // many refs per item: overlapping items never run at once (5T.3.4)
	ChunkSize     int                  // 0 = 256
	ItemTimeout   time.Duration        // clock starts after the pause gate and ref locks
	OnItemError   ErrorMode            // zero = Collect (count failed, continue)
	Label         func(T) string       // pure function of the item; runs in workers
}

// DefaultChunkSize is BatchSpec.ChunkSize when it is zero. Name coined by F2-01.
const DefaultChunkSize = 256

// FixerSpec plans rows, lets a person approve some, then applies exactly
// those, re-evaluating each against the plan.
type FixerSpec[P, C any] struct {
	Common      // Effects must be Writes(...); Approval required
	Candidates  func(rc *RC, p P) (Source[C], error)
	Load        func(rc *RC, p P, rowID string) (C, bool, error)
	Evaluate    func(ic *ItemRC, p P, c C) (Row, error) // read-only; runs at plan AND apply
	Apply       func(ic *ItemRC, w Writer, p P, row Row) error
	Parallelism Parallelism
	PartitionBy func(Row) string // optional; Row.Subjects always act as ExclusiveRefs at apply
	AfterApply  func(rc *RC, applied []string) error
	Guards      []Guard
}

// spec is the one Definition implementation.
type spec struct {
	kind     Kind
	shape    Shape
	common   Common
	par      Parallelism
	problems []error
}

func (s *spec) Kind() Kind   { return s.kind }
func (s *spec) Shape() Shape { return s.shape }
func (s *spec) spec() *spec  { return s }

func (s *spec) problem(format string, args ...any) {
	s.problems = append(s.problems, fmt.Errorf("tasks: %s: %s", s.kind, fmt.Sprintf(format, args...)))
}

// Task builds a Task definition. It never panics; see Problems.
func Task[P, R any](kind Kind, s TaskSpec[P, R]) Definition {
	d := newSpec(kind, ShapeTask, s.Common)
	if s.Run == nil {
		d.problem("Task has no Run")
	}
	return d
}

// Batch builds a Batch definition. It never panics; see Problems.
func Batch[P, T, R any](kind Kind, s BatchSpec[P, T, R]) Definition {
	d := newSpec(kind, ShapeBatch, s.Common)
	d.par = s.Parallelism
	d.parallelismProblems()
	if s.Source == nil {
		d.problem("Batch has no Source")
	}
	if s.Item == nil {
		d.problem("Batch has no Item")
	}
	if s.PartitionBy != nil && s.ExclusiveRefs != nil {
		d.problem("Batch sets both PartitionBy and ExclusiveRefs")
	}
	if s.ChunkSize < 0 {
		d.problem("ChunkSize %d is negative", s.ChunkSize)
	}
	if s.ItemTimeout < 0 {
		d.problem("ItemTimeout %s is negative", s.ItemTimeout)
	}
	if s.OnItemError > Collect {
		d.problem("unknown OnItemError %d", s.OnItemError)
	}
	return d
}

// Fixer builds a Fixer definition. It never panics; see Problems.
func Fixer[P, C any](kind Kind, s FixerSpec[P, C]) Definition {
	d := newSpec(kind, ShapeFixer, s.Common)
	d.par = s.Parallelism
	d.parallelismProblems()
	if s.Effects.Declared() && s.Effects.IsReadOnly() {
		d.problem("a Fixer's Effects must be Writes(...)")
	}
	if s.Approval == nil {
		d.problem("a Fixer needs Approval")
	}
	for _, f := range []struct {
		name    string
		missing bool
	}{
		{"Candidates", s.Candidates == nil}, {"Load", s.Load == nil},
		{"Evaluate", s.Evaluate == nil}, {"Apply", s.Apply == nil},
	} {
		if f.missing {
			d.problem("Fixer has no %s", f.name)
		}
	}
	return d
}

func (s *spec) parallelismProblems() {
	if err := s.par.Validate(); err != nil {
		s.problems = append(s.problems, fmt.Errorf("tasks: %s: Parallelism %s: %w", s.kind, s.par, err))
	}
}

// newSpec records every Common problem a constructor can see. The checks
// that need the registering module or the config schema (kind prefix, scope
// namespaces, declared resources, schedule key ownership, Every outside the
// relay, PrioritySystem on a module kind) are the kernel's at registration.
func newSpec(kind Kind, shape Shape, c Common) *spec {
	d := &spec{kind: kind, shape: shape, common: c}
	if _, err := ParseKind(string(kind)); err != nil {
		d.problems = append(d.problems, err)
	}
	if c.Title == "" {
		d.problem("Title is required")
	}
	for _, p := range c.Effects.problems() {
		d.problem("%s", p)
	}
	if c.Perms.Start.IsZero() {
		d.problem("Perms.Start is required")
	}
	if !c.Resume.valid() {
		d.problem("Resume is required (ResumeContinue, ResumeFromZero, ResumeDrop or ResumeAsk), got %d", c.Resume)
	}
	if c.Yields && c.Resume != ResumeContinue {
		d.problem("Yields requires ResumeContinue")
	}
	for _, p := range c.Cancel.problems() {
		d.problem("%s", p)
	}
	if c.ConcurrencyKey != "" && !c.ConcurrencyKey.valid() {
		d.problem("ConcurrencyKey %q is not <module>.<name>", c.ConcurrencyKey)
	}
	for _, k := range c.NotWhile {
		if _, err := ParseKind(string(k)); err != nil {
			d.problem("NotWhile: %v", err)
		}
	}
	if c.Priority > PrioritySystem {
		d.problem("unknown Priority %d", c.Priority)
	}
	if c.Timeout < 0 || c.Timeout > MaxTimeout {
		d.problem("Timeout %s is outside [0, %s]", c.Timeout, MaxTimeout)
	}
	if c.ProgressTimeout < 0 {
		d.problem("ProgressTimeout %s is negative", c.ProgressTimeout)
	}
	if c.Schedule != nil {
		for _, p := range c.Schedule.problemsFor(c.Effects.Declared() && !c.Effects.IsReadOnly()) {
			d.problem("%s", p)
		}
	}
	if c.Approval != nil {
		if c.Perms.Approve.IsZero() {
			d.problem("Approval needs Perms.Approve")
		}
		if c.Approval.MaxPlanAge < 0 {
			d.problem("Approval.MaxPlanAge %s is negative", c.Approval.MaxPlanAge)
		}
	}
	for _, r := range c.Ready {
		if r == "" {
			d.problem("Ready names an empty component")
		}
	}
	for _, l := range c.Lanes {
		if _, err := ParseKind(string(l)); err != nil {
			d.problem("Lanes: %q is not <module>.<name>", l)
		}
	}
	if db := c.Debounce; db != nil {
		if db.Window < 0 || db.MaxWait < 0 {
			d.problem("Debounce durations must not be negative")
		}
		if db.MaxWait > 0 && db.MaxWait < max(db.Window, 0) {
			d.problem("Debounce.MaxWait %s is below Window %s", db.MaxWait, db.Window)
		}
	}
	if c.Notify > NotifyActivity {
		d.problem("unknown Notify %d", c.Notify)
	}
	return d
}

// Problems returns every problem the definition's constructor recorded, in a
// stable order; nil when it has none. Registration fails on any (5T.3.1). A
// Parallelism problem wraps ErrNoParallelism. Name coined by F2-01.
func Problems(d Definition) []error {
	if d == nil {
		return []error{errors.New("tasks: nil Definition")}
	}
	return slices.Clone(d.spec().problems)
}

// CommonOf returns the Common a definition was built with, as given (no
// defaults applied). For the kernel and the executor. Name coined by F2-01.
func CommonOf(d Definition) Common {
	if d == nil {
		return Common{}
	}
	return d.spec().common
}

// ParallelismOf returns a Batch's or Fixer's declared Parallelism; a Task has
// none (the zero value). Name coined by F2-01.
func ParallelismOf(d Definition) Parallelism {
	if d == nil {
		return Parallelism{}
	}
	return d.spec().par
}
