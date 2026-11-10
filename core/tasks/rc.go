// file: core/tasks/rc.go
// version: 1.1.1
// guid: 485cb90e-f19f-43ac-911a-0ba9d24400dc
// last-edited: 2026-11-10

package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/lifecycle"
)

// RunID is opaque; assigned by RunStore.NewID (5T.3.13); an imported run keeps
// HEAD's op ID (5T.8.3), so app rows that name it still resolve.
type RunID string

// Mode is a run's mode.
type Mode uint8

const (
	ModePreview Mode = iota + 1
	ModeLive
)

// String names the mode ("preview", "live"), or "unset".
func (m Mode) String() string {
	switch m {
	case ModePreview:
		return "preview"
	case ModeLive:
		return "live"
	default:
		return "unset"
	}
}

// ModeRequest is the mode a start asks for: unset, preview or live. Only the
// HTTP route and Client.Start resolve ModeRequestUnset (Preview for writers, Live for
// ReadOnly defs, 5T.3.7); every other starter must state it (ErrModeUnset).
// The constant names are coined by F2-01.
type ModeRequest uint8

const (
	ModeRequestUnset ModeRequest = iota
	ModeRequestPreview
	ModeRequestLive
)

// Mode is the requested mode, false for ModeRequestUnset or an unknown value.
// Accessor coined by F2-01.
func (r ModeRequest) Mode() (Mode, bool) {
	switch r {
	case ModeRequestPreview:
		return ModePreview, true
	case ModeRequestLive:
		return ModeLive, true
	default:
		return 0, false
	}
}

// Trigger is what started a run.
type Trigger string

// The triggers (5T.3.3). Constant names coined by F2-01.
const (
	TriggerManual   Trigger = "manual"
	TriggerSchedule Trigger = "schedule"
	TriggerWindow   Trigger = "window"
	TriggerOnStart  Trigger = "on_start"
	TriggerOutbox   Trigger = "outbox"
	TriggerParent   Trigger = "parent"
	TriggerResume   Trigger = "resume"
)

// Actor is who started or acted on a run. A person has UserID and the
// AuthMethod it signed in with; a system actor (schedules, the relay, a
// module's own starts) has System set to the owning module's ID, or "core".
// The System field is coined by F2-01.
type Actor struct {
	UserID     string
	AuthMethod string // session | token | system; never derived from an unsigned header
	System     string
}

// RunRef names a run a start created or folded into.
type RunRef struct {
	ID       RunID
	Existing bool // the start folded into (dedupe) or replayed (idempotency) an existing run; coined by F2-01
}

// Outcome is how an awaited child run ended (rc.Await).
type Outcome struct {
	State  RunState
	Result json.RawMessage
	Err    string
}

// StartOption adjusts a child start made with rc.Enqueue. Coined by F2-01.
type StartOption func(*StartRequest)

// Hold is a held stand-down (rc.StandDown). Release it when the write phase
// ends; the executor also releases it when the run ends.
type Hold interface{ Release() }

// The causes an RC context is canceled with (context.Cause). All wrap
// context.Canceled or context.DeadlineExceeded so existing errors.Is checks hold.
var (
	ErrCanceled = fmt.Errorf("tasks: run canceled: %w", context.Canceled) // operator cancel
	//archtest:allow-var spec-mandated re-export of lifecycle.ErrShutdown (05-tasks 5T.3.3, 03 3.3 L0): an error value, not a map, slice, func, pointer or chan
	ErrShutdown     = lifecycle.ErrShutdown                                                              // process shutdown; re-exported from core/lifecycle
	ErrQuiesced     = fmt.Errorf("tasks: run quiesced: %w", context.Canceled)                            // stand-down or pause-for-requeue
	ErrTimedOut     = fmt.Errorf("tasks: run timed out: %w", context.DeadlineExceeded)                   // attempt Timeout
	ErrAbandoned    = fmt.Errorf("tasks: run abandoned after its cancel grace: %w", context.Canceled)    // grace expired after a cancel; fence revoked
	ErrItemsStopped = fmt.Errorf("tasks: a sibling item failed; dispatch stopped: %w", context.Canceled) // FailFast sibling stop (item ctx only)
)

// ErrContinueInPlace: return it (after Checkpoint) to be re-queued under the
// SAME RunID with a fresh Timeout. Honoured only for Resume == ResumeContinue,
// only if this attempt reported progress past its starting point AND
// checkpointed since it started; otherwise the run fails with a message saying
// which proof was missing (current rule, continue_in_place.go:31-41).
var ErrContinueInPlace = errors.New("tasks: yielded before deadline; continue in place")

// RunHost is the executor's handle behind one RC (F2-03 implements it in
// core/internal/executor; taskstest.FakeRun implements it for tests). It is
// not an app-facing interface: an app never implements it. A test double
// implements it by embedding taskstest.FakeRun and overriding the methods it
// needs, because RunHost may grow methods in any release. Every RC method
// delegates to it, and RunItems/RunSeq/ForEach reach the run's CPU budget,
// pause gate, ref-lock table, liveness and stand-down hold only through it.
// The executor's run behaviour lives behind it; F2-03 still adds, in this
// package, the unexported runner closures the Task, Batch and Fixer
// constructors store (seams.d/F2-01.md). Name coined by F2-01.
type RunHost interface { //nolint:interfacebloat // one run handle: every RC method delegates to it (05-tasks 5T.3.3), plus the loop hooks of 5T.3.2's table
	Context() context.Context
	RunID() RunID
	Kind() Kind
	Mode() Mode
	Trigger() Trigger
	Actor() Actor
	Log() *slog.Logger
	Writer() Writer
	Resumed() bool
	Deadline() time.Time
	ShouldYield() bool
	Phase(name string) (end func())
	// Checkpoint and Restore persist a named JSON value of the run
	// (RunStore.SaveCheckpoint); RC.Checkpoint uses the name "".
	Checkpoint(name string, v any) error
	Restore(name string, v any) (bool, error)
	// ReportProgress is a Task's Progress.Set; Note its status line; Touch
	// stamps liveness without a number.
	ReportProgress(done int64, total Total)
	Note(msg string)
	Touch()
	Enqueue(kind Kind, params any, opts ...StartOption) (RunRef, error)
	Await(ref RunRef) (Outcome, error)
	StandDown(key Key) (Hold, error)
	LockRefs(ctx context.Context, refs ...entity.Ref) (release func(), err error)
	Lane(ctx context.Context, id LaneID) (LaneLease, error)

	// CapWorkers lowers a declared worker count by the operator dial
	// core.tasks.kinds[<kind>].parallelism_cap; it never raises it.
	CapWorkers(declared int) int
	// AwaitUnpaused parks a worker on the executor's pause gate.
	AwaitUnpaused(ctx context.Context) error
	// AcquireCPU takes one core.tasks.cpu_slots slot (CPU() workers only).
	AcquireCPU(ctx context.Context) (release func(), err error)
	// RenewHold renews the run's stand-down hold, if it holds one; an error
	// means the hold is lost and fails the item before its write.
	RenewHold(ctx context.Context) error
	// ItemsProgress delivers a loop's absolute completion count (in
	// completion order, under one lock) and the current item's label; each
	// call also touches liveness.
	ItemsProgress(done, total int64, current string)
}

// hostKey is the context key under which an RC context carries its host, so
// ForEach and LockRefs can tell a ctx that descends from a run.
type hostKey struct{}

func hostFrom(ctx context.Context) RunHost {
	if ctx == nil {
		return nil
	}
	h, _ := ctx.Value(hostKey{}).(RunHost)
	return h
}

// RC is one run's context: what a definition's body receives.
type RC struct {
	h   RunHost
	ctx context.Context
}

// NewRC wraps the executor's run handle h. It is for core/internal/executor
// and taskstest; an app never builds an RC. A nil h returns nil, which the
// loops refuse with ErrNoRunContext. Name coined by F2-01.
func NewRC(h RunHost) *RC {
	if h == nil {
		return nil
	}
	return &RC{h: h, ctx: context.WithValue(h.Context(), hostKey{}, h)}
}

// Context is canceled on cancel, timeout, quiesce, shutdown (cause tells which).
func (rc *RC) Context() context.Context { return rc.ctx }

// RunID is the run's ID.
func (rc *RC) RunID() RunID { return rc.h.RunID() }

// Kind is the run's kind.
func (rc *RC) Kind() Kind { return rc.h.Kind() }

// Mode is the run's mode.
func (rc *RC) Mode() Mode { return rc.h.Mode() }

// Trigger is Manual, Schedule, Window, OnStart, Outbox, Parent or Resume.
func (rc *RC) Trigger() Trigger { return rc.h.Trigger() }

// Actor is who started the run; the system actor for schedules.
func (rc *RC) Actor() Actor { return rc.h.Actor() }

// Log has run_id and kind attached (05-logging child logger).
func (rc *RC) Log() *slog.Logger { return rc.h.Log() }

// Progress is the run's progress handle.
func (rc *RC) Progress() *Progress { return &Progress{h: rc.h} }

// Phase names the current phase until end is called.
func (rc *RC) Phase(name string) (end func()) { return rc.h.Phase(name) }

// Writer is nil for ReadOnly defs; intent-recording in Preview.
func (rc *RC) Writer() Writer { return rc.h.Writer() }

// Checkpoint JSON-encodes v into the run's checkpoint record.
func (rc *RC) Checkpoint(v any) error { return rc.h.Checkpoint("", v) }

// Restore decodes the run's checkpoint into v; false when there is none.
func (rc *RC) Restore(v any) (bool, error) { return rc.h.Restore("", v) }

// Resumed reports an attempt that resumes an interrupted run.
func (rc *RC) Resumed() bool { return rc.h.Resumed() }

// Deadline is this attempt's Timeout deadline.
func (rc *RC) Deadline() time.Time { return rc.h.Deadline() }

// ShouldYield is true when Deadline - now < YieldMargin (5T.3.6).
func (rc *RC) ShouldYield() bool { return rc.h.ShouldYield() }

// Enqueue starts a child run, parent-linked.
func (rc *RC) Enqueue(kind Kind, params any, opts ...StartOption) (RunRef, error) {
	return rc.h.Enqueue(kind, params, opts...)
}

// Await waits for a child run and relays its liveness to this run.
func (rc *RC) Await(ref RunRef) (Outcome, error) { return rc.h.Await(ref) }

// StandDown quiesces running holders of key and keeps new ones queued while
// the hold lives (5T.3.7).
func (rc *RC) StandDown(key Key) (Hold, error) { return rc.h.StandDown(key) }

// LockRefs takes the executor's keyed ref locks, sorted, all-or-wait (5T.3.4).
func (rc *RC) LockRefs(refs ...entity.Ref) (release func(), err error) {
	return rc.h.LockRefs(rc.ctx, refs...)
}

// Canceled reports a run context that is done.
func (rc *RC) Canceled() bool { return rc.ctx.Err() != nil }

// Progress is a run's progress handle (rc.Progress()). A Batch or Fixer gets
// its numbers from the runner and RunItems from its loop; a Task sets them.
type Progress struct{ h RunHost }

// Set reports a Task's position.
func (p *Progress) Set(done int64, total Total) { p.h.ReportProgress(done, total) }

// Tick stamps liveness without a number.
func (p *Progress) Tick() { p.h.Touch() }

// Note sets the free-text status line.
func (p *Progress) Note(msg string) { p.h.Note(msg) }
