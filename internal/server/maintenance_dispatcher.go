// file: internal/server/maintenance_dispatcher.go
// version: 1.6.0
// guid: 55555555-5555-5555-5555-555555555555
// last-edited: 2026-08-11

package server

import (
	"errors"
	"io"
	"net/http"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/gin-gonic/gin"
	ulid "github.com/oklog/ulid/v2"
)

// listMaintenanceJobs returns the catalogue of all registered maintenance jobs.
func (s *Server) listMaintenanceJobs(c *gin.Context) {
	jobs := maintenance.All()
	type jobDef struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		Description   string `json:"description"`
		Category      string `json:"category"`
		DefaultParams any    `json:"default_params"`
		CanResume     bool   `json:"can_resume"`
		Permission    string `json:"permission,omitempty"`
	}
	out := make([]jobDef, len(jobs))
	for i, j := range jobs {
		perm := string(auth.PermSettingsManage)
		if pa, ok := j.(maintenance.PermissionAware); ok && pa.Permission() != "" {
			perm = pa.Permission()
		}
		out[i] = jobDef{
			ID:            j.ID(),
			Name:          j.Name(),
			Description:   j.Description(),
			Category:      j.Category(),
			DefaultParams: j.DefaultParams(),
			CanResume:     j.CanResume(),
			Permission:    perm,
		}
	}
	httputil.RespondWithOK(c, struct {
		Jobs []jobDef `json:"jobs"`
	}{Jobs: out})
}

// runMaintenanceJob enqueues the named maintenance job as an async operation.
func (s *Server) runMaintenanceJob(c *gin.Context) {
	jobID := c.Param("job_id")
	job, err := maintenance.Get(jobID)
	if err != nil {
		httputil.RespondWithNotFound(c, "maintenance job", jobID)
		return
	}

	// Enforce per-job access control. Jobs that implement PermissionAware use
	// their own permission; all others default to settings.manage.
	if config.AppConfig.EnableAuth {
		required := auth.Permission(auth.PermSettingsManage)
		if pa, ok := job.(maintenance.PermissionAware); ok && pa.Permission() != "" {
			required = auth.Permission(pa.Permission())
		}
		if !auth.Can(c.Request.Context(), required) {
			if _, hasUser := auth.UserFromContext(c.Request.Context()); !hasUser {
				httputil.RespondWithUnauthorized(c, "authentication required")
			} else {
				httputil.RespondWithForbidden(c, "permission denied: "+string(required))
			}
			return
		}
	}

	// dry_run is the only thing standing between a preview and a real mutation,
	// and its zero value is the DESTRUCTIVE one. A body that failed to parse —
	// a trailing comma, `"true"` instead of true, a truncated upload — left
	// DryRun false and the job below was enqueued for real. The 202 response is
	// byte-identical either way, so the operator who asked for a preview had no
	// signal at all that they got a mutation.
	//
	// An ABSENT body still means DryRun=false; that is this endpoint's existing
	// contract and is deliberately unchanged here. Only "you sent something and
	// we could not read it" becomes an error.
	var req struct {
		DryRun bool `json:"dry_run"`
	}
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		httputil.RespondWithBadRequest(c, "invalid request body: "+err.Error())
		return
	}

	opID := ulid.Make().String()
	opType := "maintenance:" + jobID
	store := s.Store()

	// Create the operation record first so it appears in active operations / activity bell.
	if _, err := store.CreateOperation(opID, opType, nil); err != nil {
		httputil.RespondWithInternalError(c, "failed to create operation record")
		return
	}

	if _, err := s.opRegistry.EnqueueOp(c.Request.Context(), "maintenance.job", maintenanceJobOpParams{
		LegacyOpID: opID,
		JobID:      jobID,
		DryRun:     req.DryRun,
	}); err != nil {
		httputil.RespondWithConflict(c, err.Error())
		return
	}
	httputil.RespondWithSuccess(c, http.StatusAccepted, struct {
		OperationID string `json:"operation_id"`
	}{OperationID: opID})
}
