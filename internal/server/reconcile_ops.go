// file: internal/server/reconcile_ops.go
// version: 1.7.0
// guid: 5c2d8f41-a3e7-4b19-8d60-9f1e2c3a4b5d
// last-edited: 2026-10-14

// reconcile_ops registers the v2 OperationDefs for the reconcile scan and
// reconcile apply operations. The HTTP handlers in reconcile.go enqueue these
// defs directly and read the run back with reconcileOperationView; they no
// longer mint a v1 op record alongside.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/applycap"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/reconcile"
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

// RegisterReconcileScanOpV2 registers the "reconcile.scan" v2 OperationDef.
func (s *Server) RegisterReconcileScanOpV2(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID: "reconcile.scan",
		// Merged 2026-10-14 (04-P14c, D27): the nightly maintenance op of the other
		// name did the same scan and is gone; its id resolves here. Rows recorded
		// under it are read back through the registry's canonical id (see
		// recentReconcileScans).
		FormerIDs:       []string{"maintenance.reconcile-scan"},
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
			store := s.storeForWiring()
			if store == nil {
				return fmt.Errorf("reconcile.scan: database not initialized")
			}
			progress := registryProgressAdapter{r: reporter}
			opID := opsregistry.ReporterOpID(reporter)
			saveResult := func(payload any) error {
				return opsregistry.ReporterSetResult(reporter, payload)
			}
			// Heartbeat covers the stretches with no progress report of their own
			// (the directory walk, the title match) on the 5-minute watchdog, and
			// ctx makes Cancel real: the retired nightly op had the same
			// Cancellable flag, but this body used to ignore ctx entirely.
			hooks := reconcile.ScanHooks{Heartbeat: func() { opsregistry.TouchLiveness(reporter) }}
			runErr := reconcile.RunReconcileScan(ctx, store, saveResult, progress, hooks)
			// Activity tags on this run's own id. It used to tag on the legacy id,
			// so dropping the stamp without repointing would leave the entries
			// written but orphaned from the operation that produced them.
			if s.activityWriter != nil && opID != "" {
				activity.FlushOperation(s.activityWriter, opID)
				summary := "Reconcile scan completed"
				if runErr != nil {
					summary = fmt.Sprintf("Reconcile scan failed: %v", runErr)
				}
				activity.EmitInfo(s.activityWriter, opID, "reconcile.scan", "reconcile", summary, activity.AlwaysShow)
			}
			return runErr
		},
	})
}

// RegisterReconcileApplyOp registers the "reconcile.apply" v2 OperationDef.
func (s *Server) RegisterReconcileApplyOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "reconcile.apply",
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
			// Fail-safe cap (internal/applycap): each match moves a file and
			// rewrites a book row. Refuse the whole set before the first move.
			if err := applycap.Check("reconcile.apply", len(p.Matches), config.AppConfig.BulkApplyMaxItems); err != nil {
				return err
			}
			store := s.storeForWiring()
			if store == nil {
				return fmt.Errorf("reconcile.apply: database not initialized")
			}
			progress := registryProgressAdapter{r: reporter}
			log := operations.LoggerFromReporter(progress)
			opID := opsregistry.ReporterOpID(reporter)
			// The undo log keys OperationChange rows on this id. That keyspace takes
			// an arbitrary string, so it does not care which era the id came from —
			// but an EMPTY id would file every change row under one blank key, so it
			// is checked rather than assumed.
			if opID == "" {
				return fmt.Errorf("reconcile.apply: reporter returned no operation id")
			}
			saveResult := func(payload any) error {
				return opsregistry.ReporterSetResult(reporter, payload)
			}
			runErr := reconcile.ExecuteReconcile(ctx, store, opID, saveResult, p.Matches, log)
			if s.activityWriter != nil {
				activity.FlushOperation(s.activityWriter, opID)
				summary := fmt.Sprintf("Reconcile apply completed: %d matches applied", len(p.Matches))
				if runErr != nil {
					summary = fmt.Sprintf("Reconcile apply failed: %v", runErr)
				}
				activity.EmitInfo(s.activityWriter, opID, "reconcile.apply", "reconcile", summary, activity.AlwaysShow)
			}
			return runErr
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterReconcileScanOpV2(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterReconcileApplyOp(reg) })
}
