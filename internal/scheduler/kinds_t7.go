// file: internal/scheduler/kinds_t7.go
// version: 1.0.0
// guid: 96300c47-5b07-450e-8e25-f8482768cb5d
// last-edited: 2026-11-18

package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/books/taskmanage"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// Native books Task kinds for conversion group T7, the scheduler's op
// definition (F5-13, 05-tasks 5T.8.2): scheduler.metadata-upgrade becomes
// books.scheduler.metadata-upgrade. It is DARK: nothing registers it before the
// tasks flip (F5-18), which also removes HEAD's v2 shell.
//
// This is the scheduler's own copy of the small foundation internal/server's
// kinds_t7.go holds (this package cannot import internal/server, which imports
// it). The derivation of a kind's Common block from HEAD's definition is the same
// rule (R5, R6) in both; each package's TestT7ServerKinds_MatchHEADDefs and TestT7SchedulerKinds_MatchHEADDefs state their rows
// by hand, so a drift in either copy fails there.

const (
	// t7KindPrefix is rule R3: tasks.Kind("books." + <HEAD def ID>).
	t7KindPrefix = "books."

	// t7SignalMemDB is the readiness component every kind with HEAD's
	// NoWarmupWait false waits for.
	t7SignalMemDB tasks.ReadySignal = "books.memdb"
)

// t7KindFor is the kind name rule R3 gives a HEAD def ID.
func t7KindFor(headID string) tasks.Kind { return tasks.Kind(t7KindPrefix + headID) }

// t7Env is what the core takes from its shell besides its writer: the run's
// logging, progress and cancellation (HEAD's Reporter, or the native run's RC).
type t7Env struct {
	// Log writes a run-log line at level.
	Log func(level slog.Level, msg string, attrs ...slog.Attr)
	// Progress reports current out of total (total <= 0 is unknown) with a note.
	Progress func(current, total int, msg string)
	// Canceled reports whether the run was canceled.
	Canceled func() bool
}

// progress is Progress behind a nil check.
func (e t7Env) progress(current, total int, msg string) {
	if e.Progress != nil {
		e.Progress(current, total, msg)
	}
}

// reporter is the env's operations.ProgressReporter.
func (e t7Env) reporter() t7ProgressReporter { return t7ProgressReporter{env: e} }

// t7HeadEnv fills the env from HEAD's Reporter.
func t7HeadEnv(rep opsregistry.Reporter) t7Env {
	return t7Env{
		Log: func(level slog.Level, msg string, attrs ...slog.Attr) {
			_ = rep.Log(level, msg, attrs...)
		},
		Progress: func(current, total int, msg string) {
			_ = rep.UpdateProgress(current, total, msg)
		},
		// A closure, not a method value: taking one from a nil Reporter panics, and
		// HEAD's body only touches the reporter when it first needs it.
		Canceled: func() bool { return rep.IsCanceled() },
	}
}

// t7NativeEnv fills the env from the native run.
func t7NativeEnv(rc *tasks.RC) t7Env {
	return t7Env{
		Log: func(level slog.Level, msg string, attrs ...slog.Attr) {
			rc.Log().LogAttrs(rc.Context(), level, msg, attrs...)
		},
		Progress: func(current, total int, msg string) {
			t := tasks.Unknown()
			if total > 0 {
				t = tasks.Known(int64(total))
			}
			rc.Progress().Set(int64(current), t)
			if msg != "" {
				rc.Progress().Note(msg)
			}
		},
		Canceled: rc.Canceled,
	}
}

// t7ProgressReporter is the operations.ProgressReporter the shared helpers that
// take one are handed: the env's hooks behind the interface.
type t7ProgressReporter struct{ env t7Env }

func (r t7ProgressReporter) UpdateProgress(current, total int, message string) error {
	r.env.progress(current, total, message)
	return nil
}

func (r t7ProgressReporter) Log(level, message string, details *string) error {
	l := slog.LevelInfo
	switch level {
	case "warn", "warning":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	case "debug":
		l = slog.LevelDebug
	}
	var attrs []slog.Attr
	if details != nil {
		attrs = append(attrs, slog.String("details", *details))
	}
	if r.env.Log != nil {
		r.env.Log(l, message, attrs...)
	}
	return nil
}

func (r t7ProgressReporter) IsCanceled() bool { return r.env.Canceled != nil && r.env.Canceled() }

// t7Hold is a scan stand-down held for the rest of a run. HEAD's shell fills it
// from opsregistry.HoldScanStandDown, and the native shell from rc.StandDown.
type t7Hold struct {
	// Ctx is the run context rebound to the hold, canceled when the lease is lost
	// (HEAD's registry hold). Nil leaves the caller's ctx as it is.
	Ctx context.Context
	// Progress reports progress and renews the lease (HEAD's hold reporter). Nil
	// leaves the env's Progress as it is.
	Progress func(current, total int, msg string)
	// Finish releases the hold and returns runErr, joined with the lease-lost
	// cause when the lease lapsed.
	Finish func(runErr error) error
}

// t7StandDown is the acquire a body asks of its shell: hold the library scan's
// stand-down for reason before the first write.
type t7StandDown func(ctx context.Context, reason string) (t7Hold, error)

// t7Rebind applies a hold to the run: the ctx the hold rebinds, and the env with
// the hold's lease-renewing progress.
func t7Rebind(ctx context.Context, env t7Env, h t7Hold) (context.Context, t7Env) {
	if h.Ctx != nil {
		ctx = h.Ctx
	}
	if h.Progress != nil {
		env.Progress = h.Progress
	}
	return ctx, env
}

// t7HeadStandDown is HEAD's acquire for the metadata-applying ops:
// opsregistry.HoldScanStandDown over the scan gate, keyed by the run's own id
// and renewed on every progress stamp. A nil gate holds nothing (tests).
func t7HeadStandDown(gate opsregistry.ScanStandDownGate, rep opsregistry.Reporter) t7StandDown {
	return func(ctx context.Context, reason string) (t7Hold, error) {
		hold, err := opsregistry.HoldScanStandDown(ctx, gate, rep, reason)
		if err != nil {
			return t7Hold{}, err
		}
		return t7Hold{
			Ctx: hold.Context(),
			Progress: func(current, total int, msg string) {
				_ = hold.Reporter().UpdateProgress(current, total, msg)
			},
			Finish: hold.Finish,
		}, nil
	}
}

// t7NativeStandDown is the native acquire: rc.StandDown over the scan's key. An
// empty key refuses, so a live write never runs without the interlock. The
// kernel fences the run's writer when the lease is lost and cancels its context.
func t7NativeStandDown(rc *tasks.RC, key tasks.Key) t7StandDown {
	return func(_ context.Context, reason string) (t7Hold, error) {
		if key == "" {
			return t7Hold{}, fmt.Errorf("no scan stand-down key configured, refusing to %s without it", reason)
		}
		h, err := rc.StandDown(key)
		if err != nil {
			return t7Hold{}, fmt.Errorf("%s: the library scan did not stand down: %w", reason, err)
		}
		return t7Hold{Finish: func(runErr error) error { h.Release(); return runErr }}, nil
	}
}

// t7Store is every read the converted cores make, declared beside each core. A
// method that writes is never on it.
type t7Store interface {
	metadataUpgradeStore
}

// t7Writer is every write the converted cores make, declared beside each core.
// The native shell builds it with t7Deps.NewWriter over the run's kernel-gated
// tasks.Writer, so every write goes through the gate (and is recorded, not
// executed, in a Preview run).
type t7Writer interface {
	metadataUpgradeWriter
}

// t7Deps is what the native kinds are built from (rule R10 item 4). Every field
// is set once, by the F5-18 registration; tests pass a recording fake.
type t7Deps struct {
	// Store is the read side the cores run over.
	Store t7Store
	// StandDownKey is the concurrency key the library scan kinds run under, which
	// a native live write holds with rc.StandDown. Empty refuses a live write.
	StandDownKey tasks.Key
	// NewWriter builds the write side over the run's gated writer. Per run: the
	// writer it wraps belongs to that run's executor.
	NewWriter func(tasks.Writer) t7Writer
}

// t7NeedStore refuses a native run whose owner left the store unset, before the
// core dereferences it.
func t7NeedStore(kind tasks.Kind, deps t7Deps) error {
	if deps.Store == nil {
		return fmt.Errorf("%s: t7Deps.Store is not set", kind)
	}
	return nil
}

// t7NewWriter builds the run's writer over its kernel-gated tasks.Writer,
// refusing a run whose owner declared no factory (or one that returned nothing).
func t7NewWriter(kind tasks.Kind, deps t7Deps, rc *tasks.RC) (t7Writer, error) {
	if deps.NewWriter == nil {
		return nil, fmt.Errorf("%s: t7Deps.NewWriter is not set", kind)
	}
	w := deps.NewWriter(rc.Writer())
	if w == nil {
		return nil, fmt.Errorf("%s: t7Deps.NewWriter returned no writer", kind)
	}
	return w, nil
}

// t7LiveOnly is the refusal a live-only writer gives a Preview run: HEAD has no
// preview for it, and a native Preview must not write.
func t7LiveOnly(rc *tasks.RC, kind tasks.Kind) error {
	if rc.Mode() != tasks.ModeLive {
		return fmt.Errorf("%s: preview not supported; start with mode live", kind)
	}
	return nil
}

// t7ModeKeys are the param keys that would carry the run's mode. The mode comes
// from the start request (05-tasks 5T.3.7), never from params, so a native shell
// refuses them.
var t7ModeKeys = []string{"dry_run", "dryRun"}

// t7DecodeNative is the native shell's params step: a mode key is refused
// (case-insensitively, as encoding/json matches field names), then the rest is
// decoded into a fresh P. Empty and null params decode to the zero value;
// params that are not a JSON object are refused.
func t7DecodeNative[P any](kind tasks.Kind, raw json.RawMessage) (P, error) {
	var v P
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return v, nil
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(t, &keys); err != nil {
		return v, fmt.Errorf("%s: decode params: %w", kind, err)
	}
	for key := range keys {
		for _, banned := range t7ModeKeys {
			if strings.EqualFold(key, banned) {
				return v, fmt.Errorf("%s: params carry %q; the run's mode comes from the start request, not params", kind, key)
			}
		}
	}
	if err := json.Unmarshal(t, &v); err != nil {
		return v, fmt.Errorf("%s: decode params: %w", kind, err)
	}
	return v, nil
}

// t7AtomScopes is the route requirement of each HEAD permission atom
// (05-rbac 6.1, internal/books.AtomTable), as in internal/server's kinds_t7.go.
// internal/books imports this package through its import graph, so the table
// cannot be read from here; the external test TestKindAtomScopes_MatchBooksAtomTable
// fails when the two drift.
var t7AtomScopes = map[auth.Permission][]rbac.Permission{
	auth.PermLibraryView:         {"books:view", "core:tasks.view"},
	auth.PermLibraryEditMetadata: {"books:edit"},
	auth.PermLibraryDelete:       {"books:delete"},
	auth.PermLibraryOrganize:     {"books:organize"},
	auth.PermScanTrigger:         {"books:scan"},
	auth.PermIntegrationsManage:  {"core:integrations.manage", "books:integrations.manage"},
	auth.PermSettingsManage: {"core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
		"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage"},
	auth.PermCollectionsManage: {"books:collections.manage"},
}

// t7StartRequirement is rule R6: every one of HEAD's atoms is required (AllOf the
// union of their scopes); an empty list is HEAD's default settings.manage; a list
// of library.view alone keeps the scan floor, AllOf(books:scan, books:view). A
// permission with no row yields the zero requirement, so the kind is refused at
// registration (fail closed) rather than starting on a guessed scope.
func t7StartRequirement(perms []auth.Permission) rbac.Requirement {
	if len(perms) == 0 {
		perms = []auth.Permission{auth.PermSettingsManage}
	}
	if !slices.ContainsFunc(perms, func(p auth.Permission) bool { return p != auth.PermLibraryView }) {
		return rbac.AllOf("books:scan", "books:view")
	}
	var scopes []rbac.Permission
	for _, p := range perms {
		row, ok := t7AtomScopes[p]
		if !ok {
			return rbac.Requirement{}
		}
		for _, s := range row {
			if !slices.Contains(scopes, s) {
				scopes = append(scopes, s)
			}
		}
	}
	return rbac.AllOf(scopes...)
}

// t7Resume is rule R5's resume mapping for a body that reads no checkpoint:
// ResumeRestart and ResumeRequeue are ResumeFromZero.
func t7Resume(p opsregistry.ResumePolicy) tasks.ResumePolicy {
	switch p {
	case opsregistry.ResumeRestart, opsregistry.ResumeRequeue:
		return tasks.ResumeFromZero
	case opsregistry.ResumeDrop:
		return tasks.ResumeDrop
	case opsregistry.ResumeAsk:
		return tasks.ResumeAsk
	}
	return 0
}

// t7Priority is rule R5's priority mapping (0/1/2 to Low/Normal/High).
func t7Priority(p opsregistry.Priority) tasks.Priority {
	switch p {
	case opsregistry.PriorityLow:
		return tasks.PriorityLow
	case opsregistry.PriorityHigh:
		return tasks.PriorityHigh
	}
	return tasks.PriorityNormal
}

// t7Common is the Common block rule R5 derives from a HEAD definition: the
// fields the table maps one to one. The caller sets Effects (rule R7). Schedule
// comes from the owner's hook (rule R9).
func t7Common(head opsregistry.OperationDef, sched func(tasks.Kind) *tasks.Schedule) tasks.Common {
	kind := t7KindFor(head.ID)
	c := tasks.Common{
		Title:    head.DisplayName,
		Help:     head.Description,
		Perms:    tasks.Permissions{Start: t7StartRequirement(head.Permissions), Manage: taskmanage.Requirement()},
		Resume:   t7Resume(head.ResumePolicy),
		Priority: t7Priority(head.DefaultPriority),
		// Clamped: the kernel refuses a Timeout over tasks.MaxTimeout (24h).
		Timeout:         min(head.Timeout, tasks.MaxTimeout),
		ProgressTimeout: head.ProgressTimeout,
		Dedupe: tasks.DedupePolicy{
			AnyParams:     head.DedupeQueuedRuns,
			SameActorOnly: head.DedupeSameActorOnly,
			Merge:         head.MergeQueuedParams,
		},
	}
	if head.ConcurrencyKey != "" {
		c.ConcurrencyKey = tasks.Key(t7KindPrefix + head.ConcurrencyKey)
	}
	for _, id := range head.DependsOn {
		c.NotWhile = append(c.NotWhile, t7KindFor(id))
	}
	if !head.NoWarmupWait {
		c.Ready = []tasks.ReadySignal{t7SignalMemDB}
	}
	if sched != nil {
		c.Schedule = sched(kind)
	}
	return c
}

// t7Res maps HEAD resource names to refs (rule R7: res(s) = tasks.Res("books", s)).
func t7Res(rs ...ops.Resource) []entity.Ref {
	out := make([]entity.Ref, 0, len(rs))
	for _, r := range rs {
		out = append(out, tasks.Res("books", string(r)))
	}
	return out
}

// t7Writes is the Effects of a kind that writes rs (rule R7).
func t7Writes(rs ...ops.Resource) tasks.Effects { return tasks.Writes(t7Res(rs...)...) }

// t7KindList is every native kind this file group builds, as a package-level
// function: tasklint's unregistered-kind rule counts package-level list
// functions only, and not a method. Rule R9: sched is the owner's schedule
// hook, nil for no schedule.
func t7KindList(r *ExtraOpsRegistrar, deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return []tasks.Definition{
		r.metadataUpgradeNative(deps, sched),
	}
}

// t7Kinds is the list function of rule R9: every native kind of the scheduler's
// op definitions.
func (r *ExtraOpsRegistrar) t7Kinds(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return t7KindList(r, deps, sched)
}

// t7KindsRoot names t7KindList from non-test code so tasklint's
// unregistered-kind rule counts it a registration root (a list function nothing
// references registers nothing, and the analyzer does not load test files).
// F5-18 deletes it with the HEAD shell.
var _ = t7KindList

// The brief's R9 line, kept so the method stays referenced.
var _ = (*ExtraOpsRegistrar).t7Kinds
