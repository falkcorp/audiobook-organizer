// file: internal/server/reconcile.go
// version: 3.5.0
// guid: e7f8a9b0-c1d2-3e4f-5a6b-7c8d9e0f1a2b
// HTTP adapters — all logic in internal/reconcile

package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/reconcile"
	"github.com/gin-gonic/gin"
)

func (s *Server) reconcilePreview(c *gin.Context) {
	store := s.storeForWiring()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	result, err := reconcile.BuildReconcilePreview(store)
	if err != nil {
		httputil.InternalError(c, "failed to build reconcile preview", err)
		return
	}
	httputil.RespondWithOK(c, result)
}

func (s *Server) startReconcileScan(c *gin.Context) {
	store := s.Ops()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	if s.opRegistry == nil {
		httputil.RespondWithInternalError(c, "operation registry not initialized")
		return
	}

	// Return the run EnqueueOp actually created. This used to mint a v1 row,
	// hand the caller that row, and discard the v2 id — so the id the client
	// held (api.ts reads raw.id off this body) belonged to a row the operations
	// endpoints no longer serve.
	opID, err := s.opRegistry.EnqueueOp(c.Request.Context(), "reconcile.scan", reconcileScanOpParams{})
	if err != nil {
		httputil.InternalError(c, "failed to enqueue operation", err)
		return
	}
	op := reconcileOperationView(store, opID, "reconcile_scan")
	if op == nil {
		httputil.InternalError(c, "failed to load enqueued operation",
			fmt.Errorf("reconcile.scan %s not readable after enqueue", opID))
		return
	}

	httputil.RespondWithSuccess(c, http.StatusAccepted, op)
}

func (s *Server) latestReconcileScan(c *gin.Context) {
	store := s.Ops()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	// Find the most recent reconcile scan, newest first, across BOTH keyspaces.
	// A v2-only scan would hide every reconcile run from before the v1 row was
	// retired; a v1-only scan (what this did) now finds nothing at all, because
	// new runs no longer write one.
	ops := recentReconcileScans(store, 200)

	// Only the newest op is ever considered. This was written as a loop over all
	// 200, but every path through the body returned on the first iteration, so
	// the rest were unreachable -- staticcheck SA4004, which made `make ci` red.
	// Written as an index so the behaviour is stated rather than implied.
	//
	// NOTE: this preserves the existing behaviour exactly, including a latent
	// flaw it carries -- a completed scan whose ResultData fails to unmarshal
	// answers with preview:nil instead of falling back to an older scan that
	// would parse. Fixing that changes what this endpoint returns, which is an
	// API decision for this lane's owner; filed as
	// todo.d/20260825-latest-reconcile-scan-hides-older-usable-previews.md.
	if len(ops) == 0 {
		httputil.RespondWithOK(c, gin.H{"operation": nil, "preview": nil})
		return
	}

	op := ops[0]
	if op.Status == "completed" && op.ResultData != nil {
		var preview reconcile.ReconcilePreviewResult
		if err := json.Unmarshal([]byte(*op.ResultData), &preview); err == nil {
			httputil.RespondWithOK(c, gin.H{
				"operation": op,
				"preview":   preview,
			})
			return
		}
	}
	httputil.RespondWithOK(c, gin.H{
		"operation": op,
		"preview":   nil,
	})
}

func (s *Server) startReconcile(c *gin.Context) {
	store := s.Ops()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	if s.opRegistry == nil {
		httputil.RespondWithInternalError(c, "operation registry not initialized")
		return
	}

	var req struct {
		Matches []reconcile.ReconcileApplyItem `json:"matches"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	if len(req.Matches) == 0 {
		httputil.RespondWithBadRequest(c, "no matches provided")
		return
	}

	opID, err := s.opRegistry.EnqueueOp(c.Request.Context(), "reconcile.apply",
		reconcileApplyOpParams{Matches: req.Matches})
	if err != nil {
		httputil.InternalError(c, "failed to enqueue operation", err)
		return
	}
	op := reconcileOperationView(store, opID, "reconcile")
	if op == nil {
		httputil.InternalError(c, "failed to load enqueued operation",
			fmt.Errorf("reconcile.apply %s not readable after enqueue", opID))
		return
	}

	httputil.RespondWithSuccess(c, http.StatusAccepted, op)
}

func (s *Server) cleanupDuplicateVersionGroupsHandler(c *gin.Context) {
	dryRun := c.Query("dry_run") == "true"
	result, err := reconcile.CleanupDuplicateVersionGroups(s.storeForWiring(), config.AppConfig.RootDir, dryRun)
	if err != nil {
		httputil.InternalError(c, "failed to cleanup version groups", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{
		"dry_run": dryRun,
		"result":  result,
	})
}

func (s *Server) markBrokenSegmentBooksHandler(c *gin.Context) {
	dryRun := c.Query("dry_run") == "true"
	result, err := reconcile.FindBrokenSegmentBooks(s.storeForWiring(), dryRun)
	if err != nil {
		httputil.InternalError(c, "failed to find broken segments", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{
		"dry_run": dryRun,
		"result":  result,
	})
}

func (s *Server) mergeNoVGDuplicatesHandler(c *gin.Context) {
	dryRun := c.Query("dry_run") == "true"
	result, err := reconcile.MergeNoVGDuplicates(s.storeForWiring(), config.AppConfig.RootDir, dryRun)
	if err != nil {
		httputil.InternalError(c, "failed to merge duplicates", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{
		"dry_run": dryRun,
		"result":  result,
	})
}

func (s *Server) assignOrphanVGsHandler(c *gin.Context) {
	result, err := reconcile.AssignOrphanVGs(s.storeForWiring(), config.AppConfig.RootDir)
	if err != nil {
		httputil.InternalError(c, "failed to assign version groups", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"result": result})
}

// electMissingPrimariesHandler repairs version groups that elect no primary at
// all, which makes every member invisible to any client applying the default
// is_primary_version=true filter (the web Library page does).
//
// Defaults to a dry run: a mutating apply must be opted into explicitly with
// ?dry_run=false, so an accidental POST previews rather than rewrites rows.
func (s *Server) electMissingPrimariesHandler(c *gin.Context) {
	dryRun := c.DefaultQuery("dry_run", "true") != "false"
	result, err := reconcile.ElectMissingPrimaries(s.storeForWiring(), dryRun)
	if err != nil {
		httputil.InternalError(c, "failed to elect missing primary versions", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"dry_run": dryRun, "result": result})
}
