// file: internal/server/itunes_path_ops.go
// version: 1.0.0
// guid: 7c4e9b2a-1f3d-4e5a-8b6c-0d2e4f6a8c0e
// last-edited: 2026-05-11
//
// itunes_path_ops registers the v2 OperationDefs for iTunes path-reconcile
// and path-repair operations, and provides the HTTP handlers that replace
// the legacy PathReconciler.Start / PathRepairer.Start methods.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/gin-gonic/gin"
	ulid "github.com/oklog/ulid/v2"
)

type itunesPathReconcileOpParams struct {
	LegacyOpID string `json:"legacy_op_id,omitempty"`
}

type itunesPathRepairOpParams struct {
	LegacyOpID string `json:"legacy_op_id,omitempty"`
	DryRun     bool   `json:"dry_run"`
}

// handleITunesPathReconcile is the HTTP handler for
// POST /api/v1/operations/itunes-path-reconcile.
// It creates a v1 op record for polling compatibility, then enqueues via v2.
func (s *Server) handleITunesPathReconcile(c *gin.Context) {
	store := s.Store()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	id := ulid.Make().String()
	op, err := store.CreateOperation(id, "itunes_path_reconcile", nil)
	if err != nil {
		slog.Error("handleITunesPathReconcile create operation", "err", err)
		httputil.InternalError(c, "failed to create operation", err)
		return
	}
	params := itunesPathReconcileOpParams{LegacyOpID: op.ID}
	if _, enqErr := s.opRegistry.EnqueueOp(c.Request.Context(), "itunes.path-reconcile", params); enqErr != nil {
		slog.Error("handleITunesPathReconcile enqueue", "enqErr", enqErr)
		httputil.InternalError(c, "failed to enqueue operation", enqErr)
		return
	}
	httputil.RespondWithSuccess(c, 202, op)
}

// handleITunesPathRepair is the HTTP handler for
// POST /api/v1/operations/itunes-path-repair.
// Reads ?apply=true|1 to switch from dry-run (default) to apply mode.
func (s *Server) handleITunesPathRepair(c *gin.Context) {
	store := s.Store()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	apply := strings.ToLower(c.Query("apply"))
	dryRun := apply != "true" && apply != "1"

	id := ulid.Make().String()
	op, err := store.CreateOperation(id, "itunes_path_repair", nil)
	if err != nil {
		slog.Error("handleITunesPathRepair create operation", "err", err)
		httputil.InternalError(c, "failed to create operation", err)
		return
	}
	params := itunesPathRepairOpParams{LegacyOpID: op.ID, DryRun: dryRun}
	if _, enqErr := s.opRegistry.EnqueueOp(c.Request.Context(), "itunes.path-repair", params); enqErr != nil {
		slog.Error("handleITunesPathRepair enqueue", "enqErr", enqErr)
		httputil.InternalError(c, "failed to enqueue operation", enqErr)
		return
	}
	httputil.RespondWithSuccess(c, 202, op)
}

// RegisterITunesPathReconcileOp registers the "itunes.path-reconcile" v2 OperationDef.
func (s *Server) RegisterITunesPathReconcileOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "itunes.path-reconcile",
		Plugin:          "itunes",
		DisplayName:     "iTunes Path Reconcile",
		Description:     "Recompute ITunesPath fields for all iTunes-tracked books and enqueue write-back.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "itunes.path-reconcile",
		Permissions:     []auth.Permission{auth.PermScanTrigger},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p itunesPathReconcileOpParams
			if len(rawParams) > 0 {
				_ = json.Unmarshal(rawParams, &p)
			}
			if s.itunesSvc == nil || s.itunesSvc.Paths == nil {
				return fmt.Errorf("iTunes service not initialized")
			}
			progress := registryProgressAdapter{r: reporter}
			return s.itunesSvc.Paths.Reconcile(ctx, p.LegacyOpID, progress)
		},
	})
}

// RegisterITunesPathRepairOp registers the "itunes.path-repair" v2 OperationDef.
func (s *Server) RegisterITunesPathRepairOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "itunes.path-repair",
		Plugin:          "itunes",
		DisplayName:     "iTunes Path Repair",
		Description:     "Find stale iTunes locations and re-discover correct paths via PID, tag scan, or fuzzy match.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         6 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "itunes.path-repair",
		Permissions:     []auth.Permission{auth.PermScanTrigger},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p itunesPathRepairOpParams
			if len(rawParams) > 0 {
				_ = json.Unmarshal(rawParams, &p)
			}
			if s.itunesSvc == nil || s.itunesSvc.Repair == nil {
				return fmt.Errorf("iTunes service not initialized")
			}
			progress := registryProgressAdapter{r: reporter}
			return s.itunesSvc.Repair.Repair(ctx, p.LegacyOpID, p.DryRun, progress)
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error {
		return s.RegisterITunesPathReconcileOp(reg)
	})
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error {
		return s.RegisterITunesPathRepairOp(reg)
	})
}
