// file: internal/server/reconcile_ops.go
// version: 1.2.0
// guid: 5c2d8f41-a3e7-4b19-8d60-9f1e2c3a4b5d
// last-edited: 2026-08-19

// reconcile_ops registers the v2 OperationDefs for the reconcile scan and
// reconcile apply operations. The HTTP handlers in reconcile.go create v1 op
// records for backward compatibility and then enqueue these defs.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/reconcile"
)

// reconcileScanOpParams carries the v1 operation ID written by the HTTP
// handler so the Run func can write results back under the same ID.
type reconcileScanOpParams struct {
	LegacyOpID string `json:"legacy_op_id"`
}

// reconcileApplyOpParams carries the v1 operation ID and the set of matches
// to apply from the HTTP request body.
type reconcileApplyOpParams struct {
	LegacyOpID string                         `json:"legacy_op_id"`
	Matches    []reconcile.ReconcileApplyItem `json:"matches"`
}

// RegisterReconcileScanOpV2 registers the "reconcile.scan" v2 OperationDef.
func (s *Server) RegisterReconcileScanOpV2(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "reconcile.scan",
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "reconcile",
		DisplayName:     "Reconcile Scan",
		Description:     "Scan for books with missing files and match them to untracked files on disk.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "reconcile.scan",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p reconcileScanOpParams
			if err := json.Unmarshal(rawParams, &p); err != nil {
				return fmt.Errorf("reconcile.scan: decode params: %w", err)
			}
			store := s.storeForWiring()
			if store == nil {
				return fmt.Errorf("reconcile.scan: database not initialized")
			}
			progress := registryProgressAdapter{r: reporter}
			runErr := reconcile.RunReconcileScan(store, ctx, p.LegacyOpID, progress)
			if s.activityWriter != nil && p.LegacyOpID != "" {
				activity.FlushOperation(s.activityWriter, p.LegacyOpID)
				summary := "Reconcile scan completed"
				if runErr != nil {
					summary = fmt.Sprintf("Reconcile scan failed: %v", runErr)
				}
				activity.EmitInfo(s.activityWriter, p.LegacyOpID, "reconcile.scan", "reconcile", summary, activity.AlwaysShow)
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
			store := s.storeForWiring()
			if store == nil {
				return fmt.Errorf("reconcile.apply: database not initialized")
			}
			progress := registryProgressAdapter{r: reporter}
			log := operations.LoggerFromReporter(progress)
			runErr := reconcile.ExecuteReconcile(ctx, store, p.LegacyOpID, p.Matches, log)
			if s.activityWriter != nil && p.LegacyOpID != "" {
				activity.FlushOperation(s.activityWriter, p.LegacyOpID)
				summary := fmt.Sprintf("Reconcile apply completed: %d matches applied", len(p.Matches))
				if runErr != nil {
					summary = fmt.Sprintf("Reconcile apply failed: %v", runErr)
				}
				activity.EmitInfo(s.activityWriter, p.LegacyOpID, "reconcile.apply", "reconcile", summary, activity.AlwaysShow)
			}
			return runErr
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterReconcileScanOpV2(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterReconcileApplyOp(reg) })
}
