// file: internal/server/itunes_path_ops.go
// version: 1.3.0
// guid: 7c4e9b2a-1f3d-4e5a-8b6c-0d2e4f6a8c0e
// last-edited: 2026-08-22
//
// itunes_path_ops registers the v2 OperationDefs for iTunes path-reconcile
// and path-repair operations, and provides the HTTP handlers that replace
// the legacy PathReconciler.Start / PathRepairer.Start methods.
//
// Both ops are v2-native: no v1 operations row is created, and restart
// behaviour is declared by ResumePolicy rather than by the resumeLegacyOp
// shim in server_lifecycle.go. See RegisterITunesPathRepairOp for why that
// distinction is a data-safety matter here and not just bookkeeping.

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
)

type itunesPathReconcileOpParams struct{}

type itunesPathRepairOpParams struct {
	DryRun bool `json:"dry_run"`
}

// itunesPathOpResponse is what both path handlers return. They used to respond
// with the whole v1 database.Operation row; there is no such row any more, and
// the id is the only field a caller ever used.
type itunesPathOpResponse struct {
	OperationID string `json:"operation_id"`
	Status      string `json:"status"`
	DryRun      *bool  `json:"dry_run,omitempty"`
}

// handleITunesPathReconcile is the HTTP handler for
// POST /api/v1/operations/itunes-path-reconcile. It enqueues a v2 op and
// returns its id; no v1 op record is created.
func (s *Server) handleITunesPathReconcile(c *gin.Context) {
	// Readiness guard only — the handler no longer writes through this store,
	// but a nil one means the DB is not up and the enqueue below would fail
	// with a less legible error.
	if store := s.Ops(); store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	opID, enqErr := s.opRegistry.EnqueueOp(c.Request.Context(), "itunes.path-reconcile", itunesPathReconcileOpParams{})
	if enqErr != nil {
		slog.Error("handleITunesPathReconcile enqueue", "enqErr", enqErr)
		httputil.InternalError(c, "failed to enqueue operation", enqErr)
		return
	}
	httputil.RespondWithSuccess(c, 202, itunesPathOpResponse{OperationID: opID, Status: "queued"})
}

// handleITunesPathRepair is the HTTP handler for
// POST /api/v1/operations/itunes-path-repair.
// Reads ?apply=true|1 to switch from dry-run (default) to apply mode.
func (s *Server) handleITunesPathRepair(c *gin.Context) {
	// Readiness guard only; see handleITunesPathReconcile.
	if store := s.Ops(); store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	apply := strings.ToLower(c.Query("apply"))
	dryRun := apply != "true" && apply != "1"

	opID, enqErr := s.opRegistry.EnqueueOp(c.Request.Context(), "itunes.path-repair", itunesPathRepairOpParams{DryRun: dryRun})
	if enqErr != nil {
		slog.Error("handleITunesPathRepair enqueue", "enqErr", enqErr)
		httputil.InternalError(c, "failed to enqueue operation", enqErr)
		return
	}
	httputil.RespondWithSuccess(c, 202, itunesPathOpResponse{OperationID: opID, Status: "queued", DryRun: &dryRun})
}

// RegisterITunesPathReconcileOp registers the "itunes.path-reconcile" v2 OperationDef.
func (s *Server) RegisterITunesPathReconcileOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "itunes.path-reconcile",
		Liveness:        opsregistry.LivenessManual,
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
			if s.itunesSvc == nil || s.itunesSvc.Paths == nil {
				return fmt.Errorf("iTunes service not initialized")
			}
			progress := registryProgressAdapter{r: reporter}
			return s.itunesSvc.Paths.Reconcile(ctx, opsregistry.ReporterOpID(reporter), progress)
		},
	})
}

// RegisterITunesPathRepairOp registers the "itunes.path-repair" v2 OperationDef.
//
// ResumePolicy is ResumeDrop, and that is now the ONLY thing governing what
// happens to an interrupted repair. Previously two policies contradicted each
// other: this def said drop, while resumeLegacyOp in server_lifecycle.go saw
// the op's v1 row and re-enqueued a fresh run. The shim won, and it re-enqueued
// with NIL PARAMS — which EnqueueOp normalizes to "{}", decoding to the zero
// value, where DryRun is FALSE.
//
// So an interrupted DRY RUN came back as a real APPLY that rewrites locations
// in the user's live iTunes library, with nothing in the request that asked for
// it. Not creating the v1 row removes that path entirely.
//
// Dropping (rather than switching to ResumeRestart, which would resume with the
// saved params and thus preserve DryRun correctly) is the deliberate choice: a
// six-hour library-writing op that auto-restarts on every deploy is the exact
// failure the reconcile_scan comment in server_lifecycle.go documents — two
// stuck runs pinning both queue workers. An operator re-triggers this by hand.
func (s *Server) RegisterITunesPathRepairOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "itunes.path-repair",
		Liveness:        opsregistry.LivenessManual,
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
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("itunes.path-repair: decode params: %w", err)
				}
			}
			if s.itunesSvc == nil || s.itunesSvc.Repair == nil {
				return fmt.Errorf("iTunes service not initialized")
			}
			progress := registryProgressAdapter{r: reporter}
			return s.itunesSvc.Repair.Repair(ctx, opsregistry.ReporterOpID(reporter), p.DryRun, progress)
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
