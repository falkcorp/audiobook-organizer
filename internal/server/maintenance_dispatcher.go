// file: internal/server/maintenance_dispatcher.go
// version: 1.7.0
// guid: 55555555-5555-5555-5555-555555555555
// last-edited: 2026-08-14

package server

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
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

// advertisedDryRunDefault reports the dry_run value this job publishes in its
// DefaultParams(), which is what listMaintenanceJobs serves to clients as
// `default_params`. Jobs that expose no dry_run key at all report false.
//
// It goes through JSON deliberately rather than reflecting over the struct.
// DefaultParams() returns `any` — in practice an anonymous struct declared
// inline in each job file — and JSON is the representation the client actually
// received. Reading it the same way the client did is what keeps the advertised
// default and the applied default from drifting apart again; a reflection-based
// reader could disagree with the wire format over tags, embedding or casing,
// which is the exact class of divergence this function exists to close.
func advertisedDryRunDefault(job maintenance.MaintenanceJob) bool {
	raw, err := json.Marshal(job.DefaultParams())
	if err != nil {
		return false
	}
	var dp struct {
		DryRun *bool `json:"dry_run"`
	}
	if err := json.Unmarshal(raw, &dp); err != nil || dp.DryRun == nil {
		return false
	}
	return *dp.DryRun
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
	// DryRun is a *bool so that "omitted" is representable at all. With a plain
	// bool, omitted and false are the same value and the handler cannot tell an
	// operator who asked to apply from one who said nothing.
	var req struct {
		DryRun *bool `json:"dry_run"`
	}
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		httputil.RespondWithBadRequest(c, "invalid request body: "+err.Error())
		return
	}

	// When dry_run is omitted, honor the default this job ADVERTISES rather than
	// Go's zero value.
	//
	// listMaintenanceJobs (above) publishes each job's DefaultParams() to clients
	// as `default_params`, and 18 of the 34 registered jobs advertise
	// {"dry_run": true} — including cleanup-series, cleanup-organize-mess,
	// dedup-books, repair-missing-files and fix-library-states. This handler
	// previously ignored that entirely and fell through to false, so a client
	// that read the catalogue, saw dry_run:true, and POSTed without a body got
	// the exact opposite of what the API had just told it — a real mutation,
	// behind an identical 202.
	//
	// cleanup-series is the sharp case: its first phase deletes every series
	// holding exactly one book, and a census on 2026-08-14 found 2,322 of 6,245
	// single-book series are genuinely distinct real series. Series names are
	// not recoverable once deleted — they are not on the book row, not in
	// operation_changes, and not in file paths.
	//
	// This changes behavior only for jobs advertising dry_run:true, and only in
	// the fail-safe direction (omission now previews instead of applies). The 16
	// jobs advertising false are unaffected. An explicit "dry_run": false still
	// applies — callers that mean it say so.
	dryRun := advertisedDryRunDefault(job)
	if req.DryRun != nil {
		dryRun = *req.DryRun
	}

	opID := ulid.Make().String()
	opType := "maintenance:" + jobID
	store := s.Store()

	// Create the operation record first so it appears in active operations / activity bell.
	if _, err := store.CreateOperation(opID, opType, nil); err != nil {
		httputil.RespondWithInternalError(c, "failed to create operation record")
		return
	}

	// Persist the resolved dry_run so a restart mid-run can resume FAITHFULLY
	// rather than guess.
	//
	// database.Operation carries no params field, so before this the resume path
	// (resumeLegacyOp in server_lifecycle.go) had no record of the operator's
	// choice at all and fell through to Go's zero value — turning an interrupted
	// PREVIEW into a real mutation, under the original operation's own ID. Seven
	// jobs are both CanResume() and advertise dry_run:true, one of which
	// (cleanup-empty-folders) removes directories from disk.
	//
	// This mirrors the bulk_write_back case in the same switch, which already
	// saves its params and reloads them on resume.
	//
	// A save failure does not fail the request: the operator asked for this job
	// and it is about to run correctly. Only a subsequent restart is affected,
	// and resumeLegacyOp falls back to the advertised default there. Log it so
	// the degraded resume is not silent.
	if err := operations.SaveParams(store, opID, maintenanceJobOpParams{
		LegacyOpID: opID,
		JobID:      jobID,
		DryRun:     dryRun,
	}); err != nil {
		slog.Warn("maintenance job params not saved; a resume would fall back to the advertised dry_run default",
			"opID", opID, "jobID", jobID, "dryRun", dryRun, "err", err)
	}

	if _, err := s.opRegistry.EnqueueOp(c.Request.Context(), "maintenance.job", maintenanceJobOpParams{
		LegacyOpID: opID,
		JobID:      jobID,
		DryRun:     dryRun,
	}); err != nil {
		httputil.RespondWithConflict(c, err.Error())
		return
	}
	httputil.RespondWithSuccess(c, http.StatusAccepted, struct {
		OperationID string `json:"operation_id"`
	}{OperationID: opID})
}
