// file: internal/server/maintenance_dispatcher.go
// version: 2.1.0
// guid: 55555555-5555-5555-5555-555555555555
// last-edited: 2026-08-29

package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/gin-gonic/gin"
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
	//
	// Force is bound here too. Without it this route drops the key before
	// EnqueueOp ever sees it, so a job's Force parameter is unreachable from the
	// entry point the UI uses. POST /operations/v2 forwards params verbatim and
	// would have carried it, but nothing in the web client uses that route for
	// maintenance -- which is how recompute-book-aggregates ended up printing
	// "Use Force=true to override" over a flag no caller could set.
	//
	// A plain bool is right for Force where DryRun needs a pointer: DryRun's zero
	// value is the DESTRUCTIVE one, so "omitted" must stay distinguishable from
	// "false". Force's zero value is the safe one, so the two may collapse.
	var req struct {
		DryRun *bool `json:"dry_run"`
		Force  bool  `json:"force"`
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

	// Enqueue the run. This mints a v2 operations row and nothing else.
	//
	// It used to mint a v1 row FIRST -- store.CreateOperation, plus
	// operations.SaveParams to persist the resolved dry_run, plus a
	// DeleteOperationWithLogs to clean the row up again when the enqueue merged
	// into an already-active run and left it twinned to nothing. All of that is
	// gone with the row.
	//
	// The comment justifying that row said it existed "so it appears in active
	// operations / activity bell". Neither half held any longer: /operations/active
	// and /operations/recent answer 410 Gone and the UI reads the v2-only
	// /operations/timeline, while the activity bell writes ActivityEntry rows keyed
	// by an operation id STRING with no foreign key to any operations row -- so the
	// bell follows the id this run reports, which is now its own v2 id.
	//
	// Persisting dry_run separately is likewise unnecessary: the v2 row HAS a params
	// field, and both registry resume paths preserve it verbatim (resumeRestart
	// updates the row in place, resumeRequeue copies row.Params onto the new row).
	// A restart mid-run therefore resumes with the operator's actual choice instead
	// of reconstructing it, which is what the v1 row could never do -- database.Operation
	// has no params field, and that gap is what turned an interrupted PREVIEW into a
	// real mutation before #2419 worked around it.
	v2OpID, err := s.opRegistry.EnqueueOp(c.Request.Context(), maintenanceOpID(jobID), maintenanceJobOpParams{
		JobID:  jobID,
		DryRun: dryRun,
		Force:  req.Force,
	})
	if err != nil {
		httputil.RespondWithConflict(c, err.Error())
		return
	}

	httputil.RespondWithSuccess(c, http.StatusAccepted, struct {
		OperationID string `json:"operation_id"`
	}{OperationID: v2OpID})
}
