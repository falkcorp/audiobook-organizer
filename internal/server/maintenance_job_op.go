// file: internal/server/maintenance_job_op.go
// version: 2.1.0
// guid: 7f3a9c21-4b8e-4d56-a123-0e5f6c7d8e9f
// last-edited: 2026-08-18

package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

type maintenanceJobOpParams struct {
	LegacyOpID string `json:"legacy_op_id"`
	// JobID is redundant now that each job has its own OperationDef — the job is
	// captured in the closure, not looked up from params. It is retained because
	// resume reads params written by an older build (operations.SaveParams /
	// LoadParams in maintenance_dispatcher.go and server_lifecycle.go), and
	// dropping the field would silently discard it from those rows.
	JobID  string `json:"job_id"`
	DryRun bool   `json:"dry_run"`
}

// maintenanceOpID returns the v2 operation ID for a maintenance job.
//
// Job IDs are kebab-case and contain no ':', the only character RegisterOp
// rejects, so the prefixed form is always a legal op ID. Verified against all 37:
// zero contain ':' and zero collide with the 144 op IDs already registered.
func maintenanceOpID(jobID string) string { return "maintenance." + jobID }

// RegisterMaintenanceJobOps registers one OperationDef per maintenance job.
//
// This replaces the single "maintenance.job" bridge, which registered ONE
// OperationDef for all 37 jobs and therefore had to hardcode one policy for every
// one of them — per-job variation was structurally impossible. Each def now reads
// its job's Policy() (declared in PR-1, #2531) for resume, timeout, concurrency
// key, liveness, and capabilities.
//
// The "maintenance.job" ID is retired. That strands no data: an in-flight v2 row
// whose def_id no longer resolves is dropped by resumeAfterStartup
// (registry/resume.go:58, "unknown def at startup"), and the bridge's policy was
// ResumeDrop, so such a row took the drop path before this change too. The
// observable difference across the deploy is one extra warning line.
//
// Permissions come from the job's own PermissionAware implementation where it has
// one, defaulting to settings.manage otherwise -- the same rule the v1 dispatcher
// applies at maintenance_dispatcher.go:91-96.
//
// This used to be hardcoded to settings.manage, which was correct only for as
// long as the dispatcher was the enforcing path. #2536 made the v2 trigger route
// enforce OperationDef.Permissions, so the hardcoded value became the operative
// one, and bulkFetchMetadataJob -- the single job that implements
// PermissionAware -- was reachable only by settings.manage despite asking for
// library.edit_metadata. That is stricter than intended, not laxer, but it is
// still the wrong permission, and it diverges from the v1 route that phase 1
// deletes.
func (s *Server) RegisterMaintenanceJobOps(reg *opsregistry.Registry) error {
	for _, job := range maintenance.All() {
		if err := s.registerMaintenanceJobOp(reg, job); err != nil {
			return err
		}
	}
	return nil
}

// registerMaintenanceJobOp registers the OperationDef for a single job.
func (s *Server) registerMaintenanceJobOp(reg *opsregistry.Registry, job maintenance.MaintenanceJob) error {
	policy := job.Policy()
	jobID := job.ID()

	// A job that implements PermissionAware and returns a non-empty permission
	// requires that one; everything else defaults to settings.manage. Kept
	// deliberately identical to the v1 dispatcher's rule so retiring the v1 route
	// (phase 1, step 2) cannot change who can run what.
	required := auth.PermSettingsManage
	if pa, ok := job.(maintenance.PermissionAware); ok && pa.Permission() != "" {
		required = auth.Permission(pa.Permission())
	}

	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              maintenanceOpID(jobID),
		Liveness:        policy.Liveness,
		Plugin:          "maintenance",
		DisplayName:     job.Name(),
		Description:     job.Description(),
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         policy.Timeout,
		ResumePolicy:    policy.ResumePolicy,
		ConcurrencyKey:  policy.ConcurrencyKey,
		Permissions:     []auth.Permission{required},
		Capabilities:    policy.Capabilities,
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p maintenanceJobOpParams
			// Params may legitimately be empty (a requeue re-enqueues with nil),
			// in which case the zero value is correct: DryRun false, no legacy ID.
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("%s: decode params: %w", maintenanceOpID(jobID), err)
				}
			}

			store := s.storeForWiring()
			ctx = maintenance.WithOperationID(ctx, p.LegacyOpID)
			progress := registryProgressAdapter{r: reporter}
			adapter := &maintenance.ProgressAdapter{Ops: progress}

			// Execute the job synchronously in this Run closure.
			runErr := job.Run(ctx, store, adapter, p.DryRun)

			// Emit an activity summary if an activity writer is available and a legacy
			// operation ID was provided. Prefer any saved OperationSummaryLog created
			// by the job; fall back to the job name.
			if s.activityWriter != nil && p.LegacyOpID != "" {
				activity.FlushOperation(s.activityWriter, p.LegacyOpID)
				if sum, serr := store.GetOperationSummaryLog(p.LegacyOpID); serr == nil && sum != nil && sum.Result != nil {
					activity.EmitInfo(s.activityWriter, p.LegacyOpID, jobID, jobID, *sum.Result, activity.AlwaysShow)
				} else {
					activity.EmitInfo(s.activityWriter, p.LegacyOpID, jobID, jobID, job.Name(), activity.AlwaysShow)
				}
			}

			return runErr
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterMaintenanceJobOps(reg) })
}
