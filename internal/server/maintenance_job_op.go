// file: internal/server/maintenance_job_op.go
// version: 3.1.0
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

// maintenanceJobOpParams is what a maintenance run carries in its v2 row.
//
// legacy_op_id is GONE. It held the id of a v1 operations row the dispatcher
// minted alongside the enqueue, and that row is no longer created. Dropping the
// field changes the params byte-shape, which has one bounded effect across the
// deploy: EnqueueOp's dedupe compares a fresh request against the params of any
// ACTIVE op, and sameParamsIgnoringLegacyID takes its key-wise path only when
// BOTH sides carry the key. A request arriving while a pre-deploy run is still
// in flight therefore falls to the exact comparison, does not match, and queues a
// second run instead of merging. The def's ConcurrencyKey still serializes the
// two, so this costs a redundant run in a window that closes when the last
// pre-deploy run finishes — the same safe direction the byte-equality rule
// already chose.
//
// JobID is still written and is currently read by nothing: the job is captured in
// the Run closure, and EnqueueOp's dedupe is scoped to a single def
// (registry.go, `if op.DefID != defID { continue }`) so it cannot conflate two
// jobs whatever the params say. It is kept as the human-readable record in a
// params blob an operator reads while debugging, NOT for the reason previously
// stated here — that comment justified it by resume reading params written by an
// older build through operations.SaveParams / LoadParams, and both of those call
// sites were deleted with the v1 minter.
type maintenanceJobOpParams struct {
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
			// (scheduler_maintenance_window_op.go).
			opID := opsregistry.ReporterOpID(reporter)

			// Warn BEFORE the id is threaded into ctx, not merely before the
			// activity summary. An empty id disables far more than the summary:
			// the eight jobs that read maintenance.OperationIDFromCtx all guard
			// their result writes with `if opID != ""`, so an empty id makes
			// CreateOperationResult/GetOperationResults no-op, the operator-facing
			// result routes return an empty set, and any job keying a resume
			// skip-set off those results silently redoes its whole input. None of
			// that surfaces as an error anywhere, which is precisely why it has to
			// be said out loud here.
			if opID == "" {
				slog.Warn("maintenance job has no operation id; per-item results, "+
					"the activity summary, and resume skip-sets are all disabled for this run",
					"jobID", jobID)
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
			// piles into one bucket. Skipping is the better failure; the WARN that
			// keeps it from being a silent one is raised above, at the point the id
			// is obtained, because an empty id disables more than this summary.
			if s.activityWriter != nil && opID != "" {
				activity.FlushOperation(s.activityWriter, opID)
				if sum, serr := store.GetOperationSummaryLog(opID); serr == nil && sum != nil && sum.Result != nil {
					activity.EmitInfo(s.activityWriter, opID, jobID, jobID, *sum.Result, activity.AlwaysShow)
				} else {
					activity.EmitInfo(s.activityWriter, opID, jobID, jobID, job.Name(), activity.AlwaysShow)
				}
			}

			return runErr
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterMaintenanceJobOps(reg) })
}
