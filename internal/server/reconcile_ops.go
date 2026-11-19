// file: internal/server/reconcile_ops.go
// version: 1.9.0
// guid: 5c2d8f41-a3e7-4b19-8d60-9f1e2c3a4b5d
// last-edited: 2026-11-18

// reconcile_ops registers the v2 OperationDefs for the reconcile scan and
// reconcile apply operations. The HTTP handlers in reconcile.go enqueue these
// defs directly and read the run back with reconcileOperationView; they no
// longer mint a v1 op record alongside.
//
// Each def has two shells over one core (F5-13): this file's v2 shell and the
// native books.<id> kind. The cores reach the reconcile engine only through
// their env's store (scan) and writer (apply), which HEAD's shells fill from
// the server.

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/applycap"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/reconcile"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// Op ids of the reconcile definitions.
const (
	reconcileScanOpID  = "reconcile.scan"
	reconcileApplyOpID = "reconcile.apply"
)

// reconcileScanOpParams is empty: a reconcile scan takes no inputs. It carried a
// LegacyOpID until 2026-08-22 so Run could write results onto a v1 row the
// handler minted; results now go to this run's own v2 row.
type reconcileScanOpParams struct{}

// reconcileApplyOpParams carries the set of matches to apply from the HTTP
// request body.
type reconcileApplyOpParams struct {
	Matches []reconcile.ReconcileApplyItem `json:"matches"`
}

// reconcileScanStore is the reconcile scan engine: it reads the library and the
// untracked trees and hands the result to the saver it is given. It writes
// nothing itself, so the scan is ReadOnly.
type reconcileScanStore interface {
	// RunReconcileScan builds the scan result and passes it to saveResult once.
	RunReconcileScan(ctx context.Context, saveResult reconcile.SaveResultFunc, progress operations.ProgressReporter, hooks reconcile.ScanHooks) error
}

// reconcileApplyWriter is the writes of reconcile.apply: the whole apply (move
// each matched file, repoint its book and book_file rows, journal the undo
// rows; reconcile.ExecuteReconcile) and the activity row the run closes with.
type reconcileApplyWriter interface {
	// ExecuteReconcile applies matches, journaling under opID and handing the
	// result to saveResult as it goes.
	ExecuteReconcile(ctx context.Context, opID string, saveResult reconcile.SaveResultFunc, matches []reconcile.ReconcileApplyItem, log logger.Logger) error
	t7ActivityWriter
}

// reconcileScanEnv is what the scan core takes from its shell (rules R2, R10).
// Activity is HEAD's activity-log row for the run; the native env leaves it nil.
type reconcileScanEnv struct {
	t7Env
	Store    reconcileScanStore
	Activity t7ActivityWriter
}

// reconcileApplyEnv is what the apply core takes from its shell (rules R2, R10).
type reconcileApplyEnv struct {
	t7Env
	Writer reconcileApplyWriter
}

// reconcileSaver stores a result the engine hands over: HEAD's shell persists it
// through the reporter (env.SetResult), and every shell keeps the last one so
// the native Task can return it. The engine always passes json.RawMessage.
func reconcileSaver(env t7Env, saved *json.RawMessage) reconcile.SaveResultFunc {
	return func(payload any) error {
		if raw, ok := payload.(json.RawMessage); ok {
			*saved = raw
		}
		if env.SetResult != nil {
			return env.SetResult(payload)
		}
		return nil
	}
}

// RegisterReconcileScanOpV2 registers the "reconcile.scan" v2 OperationDef.
func (s *Server) RegisterReconcileScanOpV2(reg *opsregistry.Registry) error {
	return reg.RegisterOp(s.reconcileScanDef())
}

// reconcileScanDef is HEAD's definition of reconcile.scan. Its Run is the v2 shell.
func (s *Server) reconcileScanDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
		ID: reconcileScanOpID,
		// Merged 2026-10-14 (04-P14c, D27): the nightly maintenance op of the other
		// name did the same scan and is gone. Its alias was dropped 2026-10-31
		// after a soak in which nothing used it and prod stored no run under it
		// (P14C-SOAK).
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "reconcile",
		DisplayName:     "Reconcile Scan",
		Description:     "Scan for books with missing files and match them to untracked files on disk.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		// 3h is the retired nightly op's budget, the larger of the two: the scan
		// stats every book path and hashes every untracked file, which on a large
		// library over network storage outran the survivor's old 2h.
		Timeout:        3 * time.Hour,
		ResumePolicy:   opsregistry.ResumeDrop,
		ConcurrencyKey: "reconcile.scan",
		Permissions:    []auth.Permission{auth.PermSettingsManage},
		// CapFilesRead: the scan stats book paths, walks the library, import and
		// iTunes trees, and reads files to hash them. The retired op declared it;
		// this def did not.
		Capabilities: []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapFilesRead},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			// The retired nightly op was enqueued with no params at all, and its id
			// still resolves here, so an empty payload is a valid call.
			var p reconcileScanOpParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("reconcile.scan: decode params: %w", err)
				}
			}
			if s.storeForWiring() == nil {
				return fmt.Errorf("reconcile.scan: database not initialized")
			}
			h := s.headReconcile()
			_, err := reconcileScanCore(ctx, reconcileScanEnv{
				t7Env:    t7HeadEnv(reporter, true),
				Store:    h,
				Activity: h.headT7Activity,
			})
			return err
		},
	}
}

// reconcileScanCore is the body of reconcile.scan, shared by HEAD's shell and the
// native kind. It returns the result the engine saved (nil when it failed before
// saving one).
func reconcileScanCore(ctx context.Context, env reconcileScanEnv) (json.RawMessage, error) {
	opID := env.OpID
	var saved json.RawMessage
	// Heartbeat covers the stretches with no progress report of their own
	// (the directory walk, the title match) on the 5-minute watchdog, and
	// ctx makes Cancel real: the retired nightly op had the same
	// Cancellable flag, but this body used to ignore ctx entirely.
	hooks := reconcile.ScanHooks{Heartbeat: func() {
		if env.Touch != nil {
			env.Touch()
		}
	}}
	runErr := env.Store.RunReconcileScan(ctx, reconcileSaver(env.t7Env, &saved), env.reporter(), hooks)
	// Activity tags on this run's own id. It used to tag on the legacy id,
	// so dropping the stamp without repointing would leave the entries
	// written but orphaned from the operation that produced them.
	t7Announce(env.Activity, opID, reconcileScanOpID, "reconcile", reconcileScanSummary(runErr))
	if runErr != nil {
		return saved, fmt.Errorf("reconcile.scan: %w", runErr)
	}
	return saved, nil
}

// reconcileScanNative is the native books.reconcile.scan kind. HEAD declares no
// Writes and the body writes nothing (it stats, walks, hashes and matches), so it
// is ReadOnly. The result the engine saves is returned by the Task.
func (s *Server) reconcileScanNative(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	kind := t7KindFor(reconcileScanOpID)
	c := t7Common(s.reconcileScanDef(), sched)
	c.Effects = tasks.ReadOnly()
	return tasks.Task(kind, tasks.TaskSpec[json.RawMessage, json.RawMessage]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (json.RawMessage, error) {
			if _, err := t7DecodeNative[reconcileScanOpParams](kind, raw); err != nil {
				return nil, err
			}
			if err := t7NeedStore(kind, deps); err != nil {
				return nil, err
			}
			return reconcileScanCore(rc.Context(), reconcileScanEnv{t7Env: t7NativeEnv(rc), Store: deps.Store})
		},
	})
}

// reconcileScanSummary is the activity-log line for a finished reconcile scan.
// A canceled scan is not a failure: the operator asked for it (or the server is
// shutting down), and a "failed" entry sends them looking for a fault.
func reconcileScanSummary(runErr error) string {
	switch {
	case runErr == nil:
		return "Reconcile scan completed"
	case errors.Is(runErr, context.Canceled):
		return "Reconcile scan canceled"
	default:
		return fmt.Sprintf("Reconcile scan failed: %v", runErr)
	}
}

// RegisterReconcileApplyOp registers the "reconcile.apply" v2 OperationDef.
func (s *Server) RegisterReconcileApplyOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(s.reconcileApplyDef())
}

// reconcileApplyDef is HEAD's definition of reconcile.apply. Its Run is the v2 shell.
func (s *Server) reconcileApplyDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
		ID:              reconcileApplyOpID,
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "reconcile",
		DisplayName:     "Reconcile Apply",
		Description:     "Apply a set of file-to-book reconcile matches, moving files and updating the database.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         1 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "reconcile.apply",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapFilesWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p reconcileApplyOpParams
			if err := json.Unmarshal(rawParams, &p); err != nil {
				return fmt.Errorf("reconcile.apply: decode params: %w", err)
			}
			if err := reconcileApplyPrecheck(p); err != nil {
				return err
			}
			if s.storeForWiring() == nil {
				return fmt.Errorf("reconcile.apply: database not initialized")
			}
			_, err := reconcileApplyCore(ctx, p, reconcileApplyEnv{
				t7Env:  t7HeadEnv(reporter, true),
				Writer: s.headReconcile(),
			})
			return err
		},
	}
}

// reconcileApplyPrecheck is the fail-safe cap (internal/applycap): each match
// moves a file and rewrites a book row, so the whole set is refused before the
// first move. Both shells call it before the core.
func reconcileApplyPrecheck(p reconcileApplyOpParams) error {
	return applycap.Check(reconcileApplyOpID, len(p.Matches), config.AppConfig.BulkApplyMaxItems)
}

// reconcileApplyCore is the body of reconcile.apply, shared by HEAD's shell and
// the native kind. p has passed reconcileApplyPrecheck. It returns the result the
// engine saved last (nil when it never saved one).
func reconcileApplyCore(ctx context.Context, p reconcileApplyOpParams, env reconcileApplyEnv) (json.RawMessage, error) {
	opID := env.OpID
	// The undo log keys OperationChange rows on this id. That keyspace takes
	// an arbitrary string, so it does not care which era the id came from —
	// but an EMPTY id would file every change row under one blank key, so it
	// is checked rather than assumed.
	if opID == "" {
		return nil, fmt.Errorf("reconcile.apply: reporter returned no operation id")
	}
	log := operations.LoggerFromReporter(env.reporter())
	var saved json.RawMessage
	runErr := env.Writer.ExecuteReconcile(ctx, opID, reconcileSaver(env.t7Env, &saved), p.Matches, log)
	summary := fmt.Sprintf("Reconcile apply completed: %d matches applied", len(p.Matches))
	if runErr != nil {
		summary = fmt.Sprintf("Reconcile apply failed: %v", runErr)
	}
	t7Announce(env.Writer, opID, reconcileApplyOpID, "reconcile", summary)
	return saved, runErr
}

// reconcileApplyNative is the native books.reconcile.apply kind. HEAD declares no
// Writes, no Preview and a library write capability, so a Preview run is refused.
// Its write set is inferred from what the apply writes: the book rows it
// repoints, their book_file rows, the files it moves and the undo journal.
func (s *Server) reconcileApplyNative(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	kind := t7KindFor(reconcileApplyOpID)
	c := t7Common(s.reconcileApplyDef(), sched)
	c.Effects = t7Writes(ops.ResBooks, ops.ResBookFiles, ops.ResFiles, ops.ResOperations)
	return tasks.Task(kind, tasks.TaskSpec[json.RawMessage, json.RawMessage]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (json.RawMessage, error) {
			if err := t7LiveOnly(rc, kind); err != nil {
				return nil, err
			}
			p, err := t7DecodeNative[reconcileApplyOpParams](kind, raw)
			if err != nil {
				return nil, err
			}
			if err := reconcileApplyPrecheck(p); err != nil {
				return nil, err
			}
			w, err := t7NewWriter(kind, deps, rc)
			if err != nil {
				return nil, err
			}
			return reconcileApplyCore(rc.Context(), p, reconcileApplyEnv{t7Env: t7NativeEnv(rc), Writer: w})
		},
	})
}

// headReconcile is HEAD's side of the reconcile store and writer: the server's
// store, passed to the engine as HEAD's bodies passed it.
type headReconcile struct {
	headT7Activity
	s *Server
}

func (s *Server) headReconcile() headReconcile {
	return headReconcile{headT7Activity: headT7Activity{w: s.activityWriter}, s: s}
}

func (h headReconcile) RunReconcileScan(ctx context.Context, saveResult reconcile.SaveResultFunc, progress operations.ProgressReporter, hooks reconcile.ScanHooks) error {
	return reconcile.RunReconcileScan(ctx, h.s.storeForWiring(), saveResult, progress, hooks)
}

func (h headReconcile) ExecuteReconcile(ctx context.Context, opID string, saveResult reconcile.SaveResultFunc, matches []reconcile.ReconcileApplyItem, log logger.Logger) error {
	return reconcile.ExecuteReconcile(ctx, h.s.storeForWiring(), opID, saveResult, matches, log)
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterReconcileScanOpV2(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterReconcileApplyOp(reg) })
}
