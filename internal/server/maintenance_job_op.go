// file: internal/server/maintenance_job_op.go
// version: 2.3.0
// guid: 7f3a9c21-4b8e-4d56-a123-0e5f6c7d8e9f
// last-edited: 2026-08-23

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

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

	// Every maintenance job serializes against itself. Two enqueues of the same
	// job queue and run one after the other instead of overlapping.
	//
	// Without a key none of the three serializing gates apply to this family:
	// EnqueueOp's dedupe block and dispatcher Gate 3 are both guarded on
	// `def.ConcurrencyKey != ""`, and Gate 3b needs a non-empty `Writes`, which no
	// maintenance def declares. All 37 use DefaultPolicy(), which hardcodes an
	// empty key, so a double-clicked "Run" has always started two CONCURRENT runs
	// of the same job over the same rows -- both read-modify-writing whole rows,
	// which is how the 2026-08-07 write-set incident lost fields.
	//
	// A job that declares its own key keeps it. That is the only way two DIFFERENT
	// jobs can share a serialization domain (two jobs that both rewrite file paths,
	// say); deriving unconditionally would make the field permanently dead. No job
	// declares one today, so this branch is currently behaviour-identical to always
	// deriving -- it exists so the declaration keeps meaning something.
	concurrencyKey := policy.ConcurrencyKey
	if concurrencyKey == "" {
		concurrencyKey = maintenanceOpID(jobID)
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
		ConcurrencyKey:  concurrencyKey,
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

			// The correlation id for everything this run records OUTSIDE its own
			// v2 row: the activity entries below, the per-item results the 8 jobs
			// that call maintenance.OperationIDFromCtx write via
			// CreateOperationResult, and the operation summary log.
			//
			// This is the run's OWN v2 id. It used to be p.LegacyOpID, the id of a
			// v1 operations row the dispatcher minted alongside the enqueue -- a row
			// that no longer exists. All three of those stores are keyed by an
			// operation id STRING with no foreign key to any operations row, so
			// re-keying them here IS the whole migration for them; nothing has to be
			// moved or backfilled. It also fixes what the legacy id never did: these
			// records now name an operation an operator can actually look up.
			//
			// Same shape as maintenance.window, which made this move first
			// (scheduler_maintenance_window_op.go). The fallback covers rows still
			// in flight across the deploy, whose params were written by the old
			// dispatcher and do carry a legacy id.
			opID := opsregistry.ReporterOpID(reporter)
			if opID == "" {
				opID = p.LegacyOpID
			}

			ctx = maintenance.WithOperationID(ctx, opID)
			progress := registryProgressAdapter{r: reporter}
			adapter := &maintenance.ProgressAdapter{Ops: progress}

			// Execute the job synchronously in this Run closure.
			runErr := job.Run(ctx, store, adapter, p.DryRun)

			// Emit an activity summary. Prefer any OperationSummaryLog the job
			// saved; fall back to the job name.
			//
			// The empty-id guard is kept rather than dropped. ReporterOpID returns
			// "" for a reporter that does not implement OpID(), and an entry written
			// with an empty operation id does not merely go uncorrelated -- the
			// activity feed groups by that id, so every such entry from every op
			// piles into one bucket. Skipping is the better failure, but it must not
			// be a SILENT one: an id this code could not obtain is exactly the
			// condition that would blind the feed without anyone noticing, so say so.
			if s.activityWriter != nil {
				if opID == "" {
					slog.Warn("maintenance job produced no operation id; skipping its activity summary",
						"jobID", jobID)
				} else {
					activity.FlushOperation(s.activityWriter, opID)
					if sum, serr := store.GetOperationSummaryLog(opID); serr == nil && sum != nil && sum.Result != nil {
						activity.EmitInfo(s.activityWriter, opID, jobID, jobID, *sum.Result, activity.AlwaysShow)
					} else {
						activity.EmitInfo(s.activityWriter, opID, jobID, jobID, job.Name(), activity.AlwaysShow)
					}
				}
			}

			return runErr
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterMaintenanceJobOps(reg) })
}
