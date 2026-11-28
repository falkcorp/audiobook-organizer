// file: core/internal/executor/origin.go
// version: 1.0.0
// guid: b2e6c9f4-1d37-4a85-9c0e-7f3a5d8b6e12
// last-edited: 2026-11-28

package executor

import (
	"context"
	"fmt"
	"strings"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// A start's ORIGIN is what lets a kind's Perms.SystemStart apply: the system
// acting through a path the executor itself began, as opposed to a person, an
// API key or a module client acting. The origin is never read from a request.
// It lives in an unexported type, and the only values that carry one are the
// handles this file mints (ScheduleStager, OutboxRuns): the scheduler's, the
// relay's. The kernel hands each handle to its one consumer, so
//
//   - tasks.Executor.StartIn and tasks.Stager.StartIn as the kernel wires them
//     elsewhere start with NO origin and are judged on Perms.Start like any
//     other start (a StartRequest.Trigger, whatever it says, is only a label);
//   - a module holds a tasks.Client, whose Start has no origin either;
//   - an rc.Enqueue takes the origin of the RUN making it (attempt.Enqueue passes
//     the run's recorded origin), so a child of a person's run keeps the
//     person's authority and a child of a system-started run of the same module
//     is the system's.
//
// The minting functions are exported only because the kernel is another package
// of this module; core/internal/ keeps every other module out of reach,
// archtest.SystemStartMinterCallers pins which core files call them, and
// TestOriginIsUnreachableFromExportedSurface pins that no exported name of this
// package reaches an origin type.

// originKind says which executor path began a start.
type originKind uint8

const (
	originNone     originKind = iota // Client.Start, Executor.StartIn, Approve: no origin
	originSchedule                   // the scheduler's tick or window
	originOutbox                     // the outbox relay's dispatch of a ConsumeIntoTask consumer
)

// startOrigin is the unexported origin a minted handle stamps on its starts.
type startOrigin struct{ kind originKind }

// Recorded origins (tasks.RunRecord.Origin).
const (
	recordedSchedule = "schedule"
	recordedOutbox   = "outbox"
	recordedChild    = "child:" // + the parent run's ID
)

// recorded is the string stored in RunRecord.Origin for a start of this origin.
func (o startOrigin) recorded() string {
	switch o.kind {
	case originSchedule:
		return recordedSchedule
	case originOutbox:
		return recordedOutbox
	}
	return ""
}

// systemOrigin reports whether a stored origin is one of the system's.
func systemOrigin(rec string) bool {
	return rec == recordedSchedule || rec == recordedOutbox || strings.HasPrefix(rec, recordedChild)
}

// admitsSystemStart reports whether this start may take d's Perms.SystemStart
// exemption for actor: the kind carries the reason, the actor is the system
// actor of the kind's own module, and the start's origin is one of
//
//   - the scheduler's, for a kind that declares a Schedule;
//   - the relay's (the kernel refuses at boot a flagged kind that is no declared
//     consumer's target);
//   - an rc.Enqueue by a run of the SAME module whose recorded origin is a system
//     origin (a child of a run the system started).
//
// Anything else, a person's or an API key's start of a flagged kind included,
// is judged on Perms.Start alone.
func (s starter) admitsSystemStart(d *def, actor tasks.Actor) bool {
	if d.c.Perms.SystemStart == "" || actor.AuthMethod != systemMethod || actor.System == "" || d.module != actor.System {
		return false
	}
	switch s.origin.kind {
	case originSchedule:
		return d.c.Schedule != nil
	case originOutbox:
		return true
	}
	p := s.parent
	return p != nil && p.module == actor.System && systemOrigin(s.parentOrigin)
}

// recordOrigin is the RunRecord.Origin a start stages.
func (s starter) recordOrigin(actor tasks.Actor) string {
	if r := s.origin.recorded(); r != "" {
		return r
	}
	if s.parent != nil && actor.AuthMethod == systemMethod && systemOrigin(s.parentOrigin) {
		return recordedChild + string(s.parentRun)
	}
	return ""
}

// stagerHandle is the tasks.Stager a minted handle returns: StartIn stamps o on
// the start, WriteRuns is the executor's. It exposes nothing else, so a holder
// that asserts it to a wider interface reaches no more than these two.
type stagerHandle struct {
	e *executor
	o startOrigin
}

func (h stagerHandle) StartIn(ctx context.Context, b *storage.Batch, kind tasks.Kind, req tasks.StartRequest) (tasks.RunRef, error) {
	return h.e.startIn(ctx, b, kind, req, h.o)
}

func (h stagerHandle) WriteRuns(ctx context.Context, open func() *storage.Batch, stage func(ctx context.Context, b *storage.Batch) error) error {
	return h.e.WriteRuns(ctx, open, stage)
}

// runsHandle is stagerHandle plus Client.Start, the shape the outbox's Runs
// interface asks for. Its Start has no origin.
type runsHandle struct {
	stagerHandle
}

func (h runsHandle) Start(ctx context.Context, kind tasks.Kind, req tasks.StartRequest) (tasks.RunRef, error) {
	return h.e.Start(ctx, kind, req)
}

// Runs is what OutboxRuns returns: the relay's handle (outbox.Runs).
type Runs interface {
	StartIn(ctx context.Context, b *storage.Batch, kind tasks.Kind, req tasks.StartRequest) (tasks.RunRef, error)
	WriteRuns(ctx context.Context, open func() *storage.Batch, stage func(ctx context.Context, b *storage.Batch) error) error
	Start(ctx context.Context, kind tasks.Kind, req tasks.StartRequest) (tasks.RunRef, error)
}

func executorOf(x tasks.Executor) (*executor, error) {
	e, ok := x.(*executor)
	if !ok {
		return nil, fmt.Errorf("executor: %T is not this package's executor", x)
	}
	return e, nil
}

// ScheduleStager returns the handle the scheduler starts through: its starts
// carry the schedule origin. The kernel is its one caller.
func ScheduleStager(x tasks.Executor) (tasks.Stager, error) {
	e, err := executorOf(x)
	if err != nil {
		return nil, err
	}
	return stagerHandle{e: e, o: startOrigin{kind: originSchedule}}, nil
}

// OutboxRuns returns the handle the outbox relay binds (outbox.Outbox.BindRuns):
// its StartIn carries the outbox origin. The kernel is its one caller.
func OutboxRuns(x tasks.Executor) (Runs, error) {
	e, err := executorOf(x)
	if err != nil {
		return nil, err
	}
	return runsHandle{stagerHandle{e: e, o: startOrigin{kind: originOutbox}}}, nil
}

// CheckOrigin names an origin for CheckSystemStart. It only SELECTS which rule
// to evaluate; no value of it starts anything.
type CheckOrigin uint8

const (
	CheckSchedule CheckOrigin = iota + 1 // the scheduler's tick
	CheckOutbox                          // the relay's dispatch of a consumer
	CheckEnqueue                         // an rc.Enqueue by a system-started run of the same module
)

func (c CheckOrigin) String() string {
	switch c {
	case CheckSchedule:
		return "schedule"
	case CheckOutbox:
		return "outbox"
	case CheckEnqueue:
		return "enqueue"
	}
	return fmt.Sprintf("CheckOrigin(%d)", uint8(c))
}

// CheckSystemStart reports whether module's system actor may start kind from
// the origin, the question the kernel asks at P7 of every declared target: nil
// when the executor's real start rule (permitStart, the one StartIn and
// rc.Enqueue apply) would pass, else the refusal it would return (wrapping
// ErrUnknownKind for an unregistered kind, ErrNotPermitted for a refused start).
// x not built by this package is an error, never a pass.
func CheckSystemStart(x tasks.Executor, module string, kind tasks.Kind, from CheckOrigin) error {
	e, err := executorOf(x)
	if err != nil {
		return fmt.Errorf("cannot check the %s start of %s: %w", from, kind, err)
	}
	d, ok := e.defs[kind]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownKind, kind)
	}
	actor := tasks.Actor{AuthMethod: systemMethod, System: module}
	var s starter
	switch from {
	case CheckSchedule:
		s.origin = startOrigin{kind: originSchedule}
	case CheckOutbox:
		s.origin = startOrigin{kind: originOutbox}
	case CheckEnqueue:
		s.parent = &def{module: module}
		s.parentOrigin = recordedSchedule
	default:
		return fmt.Errorf("executor: unknown origin %s", from)
	}
	return e.permitStart(d, actor, s)
}
