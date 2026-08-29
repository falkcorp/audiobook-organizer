// file: internal/server/maintenance_dispatcher.go
// version: 2.2.0
// guid: 55555555-5555-5555-5555-555555555555
// last-edited: 2026-08-29

package server

import (
	"bytes"
	"encoding/json"
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
	// force no longer needs a binding of its own. #2965 added one because this
	// route dropped the key before EnqueueOp ever saw it -- which is how
	// recompute-book-aggregates ended up printing "Use Force=true to override"
	// over a flag no caller could set. Keeping every key makes that binding
	// redundant: force now flows through as an ordinary operator key, and so does
	// the NEXT custom parameter somebody adds, without a third edit here. The
	// per-key binding was a fix for one key; this is a fix for the shape.
	//
	// Read the body ONCE as raw bytes and keep EVERY key the operator sent.
	//
	// Binding to a fixed struct is exactly what lost the custom parameters. This
	// handler bound dry_run (and, since #2965, force) and then enqueued
	// maintenanceJobOpParams — a three-field struct — so every other key was
	// dropped right here, at the dispatcher, before it could reach the params
	// blob. Five jobs take a custom parameter and all five silently received
	// nothing on this route: revert-metadata-fetch (fetch_op_ids),
	// bulk-fetch-metadata (prefer_audible, skip_cached), bulk-deluge-import
	// (max_books), scan-composer-tags (fix_mode), prune-book-snapshots
	// (keep_count).
	//
	// It is the same defect #2965 fixed one layer down for force, with the same
	// tell: listMaintenanceJobs (above) publishes each job's DefaultParams() to
	// clients as `default_params`, so the catalogue route ADVERTISES fetch_op_ids
	// and fix_mode while the run route threw them away. fetch_op_ids is REQUIRED,
	// so revert-metadata-fetch could only ever return "fetch_op_ids required" —
	// it was 100% non-functional from the entry point the UI uses.
	//
	// A map[string]json.RawMessage is what preserves unknown keys. Values stay
	// raw and are never re-interpreted, so a job decodes the operator's own bytes
	// into the shape DefaultParams() advertises.
	body, readErr := io.ReadAll(c.Request.Body)
	if readErr != nil {
		httputil.RespondWithBadRequest(c, "invalid request body: "+readErr.Error())
		return
	}

	// An absent or whitespace-only body is legal and means "no parameters" — the
	// io.EOF tolerance the previous ShouldBindJSON call spelled out. Anything
	// else must parse as a JSON object; a trailing comma or a truncated upload
	// is a 400, never a silently-defaulted run.
	params := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &params); err != nil {
			httputil.RespondWithBadRequest(c, "invalid request body: "+err.Error())
			return
		}
	}

	// dry_run is decoded from the map rather than bound separately so there is
	// one parse of one body. A wrong TYPE ("true" instead of true) is still a
	// 400 — the whole point of the *bool is that a malformed dry_run must not
	// collapse to false and silently mutate. An explicit null is treated as
	// omitted, matching what ShouldBindJSON did with it.
	var reqDryRun *bool
	if raw, ok := params["dry_run"]; ok {
		if err := json.Unmarshal(raw, &reqDryRun); err != nil {
			httputil.RespondWithBadRequest(c, "invalid request body: dry_run must be a boolean")
			return
		}
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
	if reqDryRun != nil {
		dryRun = *reqDryRun
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
	// Overlay the two keys the dispatcher OWNS onto the operator's object and
	// enqueue that, rather than marshalling a fixed struct.
	//
	// job_id is the dispatcher's to set; a client cannot rename the run it asked
	// for. dry_run is written back RESOLVED, because advertisedDryRunDefault may
	// have supplied it — a body with no dry_run must persist the advertised
	// default, not an absent key, so that both resume paths (which copy
	// row.Params verbatim) restore the operator's effective choice rather than
	// falling to Go's zero value, which is the DESTRUCTIVE one. That is the
	// property #2419 worked around and the v2 params field now provides directly.
	//
	// force is deliberately NOT overlaid: it has no dispatcher-side default, so
	// it flows through as an ordinary operator key like every other custom
	// parameter. maintenanceJobOpParams is still the shape the Run closure
	// DECODES (job_id, dry_run, force); it is simply no longer the shape this
	// route ENCODES, which is what made it a filter.
	params["job_id"], _ = json.Marshal(jobID)
	params["dry_run"], _ = json.Marshal(dryRun)

	// encoding/json marshals map keys in sorted order, so this byte shape is
	// deterministic run to run — which EnqueueOp's dedupe requires, since
	// legacy_op_id is gone and that comparison is now exact bytes.
	//
	// Two bounded effects, both in the safe direction. Across the deploy, a
	// request arriving while a PRE-deploy run is still in flight compares
	// sorted-key bytes against that run's struct-order bytes, does not match, and
	// queues a second run instead of merging — the same window #2965 accepted for
	// dropping legacy_op_id, and it closes when the last pre-deploy run finishes.
	// Going forward, two requests for the same job with DIFFERENT custom params
	// now differ and queue instead of merging, which is required: a run asking to
	// revert operations A and B must not be silently swallowed by an in-flight run
	// reverting C. The per-job ConcurrencyKey still serializes them either way.
	enqParams, marshalErr := json.Marshal(params)
	if marshalErr != nil {
		httputil.RespondWithBadRequest(c, "invalid request body: "+marshalErr.Error())
		return
	}

	v2OpID, err := s.opRegistry.EnqueueOp(c.Request.Context(), maintenanceOpID(jobID), json.RawMessage(enqParams))
	if err != nil {
		httputil.RespondWithConflict(c, err.Error())
		return
	}

	httputil.RespondWithSuccess(c, http.StatusAccepted, struct {
		OperationID string `json:"operation_id"`
	}{OperationID: v2OpID})
}
