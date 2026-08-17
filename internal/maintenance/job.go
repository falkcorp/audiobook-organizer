// file: internal/maintenance/job.go
// version: 1.4.0
// guid: 11111111-1111-1111-1111-111111111111
// last-edited: 2026-08-17

package maintenance

import (
	"context"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

type contextKey string

const opIDKey contextKey = "maintenance_op_id"

// WithOperationID returns a context carrying the given operation ID.
func WithOperationID(ctx context.Context, opID string) context.Context {
	return context.WithValue(ctx, opIDKey, opID)
}

// OperationIDFromCtx returns the operation ID stored in the context, or "".
func OperationIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(opIDKey).(string)
	return v
}

// ProgressReporter is the minimal interface jobs use to report progress.
type ProgressReporter interface {
	SetTotal(n int)
	Increment()
	Log(level, message string, details *string)
}

// WriteBackEnqueuer is the narrow interface jobs use for iTunes write-back.
// Satisfied by *itunesservice.WriteBackBatcher.
type WriteBackEnqueuer interface {
	Enqueue(bookID string)
	EnqueueRemove(pid string)
}

// EnqueuerInjectable is implemented by jobs that need the write-back enqueuer.
type EnqueuerInjectable interface {
	InjectEnqueuer(e WriteBackEnqueuer)
}

// PermissionAware is optionally implemented by jobs that require a non-default
// permission. The dispatcher uses this to enforce per-job access control.
// Jobs that do not implement this interface default to the settings.manage permission.
type PermissionAware interface {
	Permission() string
}

// ExecutionPolicy declares how the operations registry should schedule, time out,
// and resume a job. It is the set of knobs an OperationDef needs that CanResume()
// cannot express.
//
// It exists because the maintenance.job bridge (internal/server/maintenance_job_op.go)
// registers ONE OperationDef for all 37 jobs and therefore hardcodes a single policy
// for every one of them — per-job variation is structurally impossible while the
// bridge stands. Declaring the policy on the job is the step that makes the 37
// separate OperationDefs of PR-2 a wiring change rather than a design change.
//
// Nothing reads this yet. The bridge still runs and still supplies its own hardcoded
// values, so adding these declarations is behaviour-preserving by construction.
//
// ⚠️ The zero value is INVALID and deliberately so: ResumeUnspecified and
// LivenessUnspecified are both `= iota` = 0, and RegisterOp rejects each
// (registry.go:433). A job returning ExecutionPolicy{} therefore compiles but would
// fail at server startup in PR-2. TestEveryJobDeclaresAUsablePolicy pulls that
// failure back to `go test`, where it belongs — do not delete it.
type ExecutionPolicy struct {
	// ResumePolicy says what happens to an in-flight run when the server restarts.
	//
	// Choosing between the two non-drop values is not a naming preference:
	//   - ResumeRestart means "reload last checkpoint, call Run again". The watchdog
	//     writes an `uncheckpointed` strike for EVERY ResumeRestart op that goes
	//     quiet (watchdog.go:156, which substitutes a default interval at :159-162 —
	//     the comment at :154 claiming it only applies to ops that set
	//     MinCheckpointInterval is stale against its own code). checkInfiniteRestart
	//     additionally force-drops one at ResumeCount>=3 with HighWaterProgress==0,
	//     the permanent state of a job that never checkpoints. So this value is only
	//     correct for a job that actually checkpoints.
	//   - ResumeRequeue means "re-run from zero", and its doc restricts it to
	//     idempotent ops.
	ResumePolicy opsregistry.ResumePolicy

	// Timeout caps a single run. Zero would mean "registry default" (120m
	// in-process); every job here states 4h explicitly because that is what the
	// bridge supplies today.
	Timeout time.Duration

	// ConcurrencyKey serializes ops sharing a non-empty key. Empty means this job
	// may run concurrently with itself — which is what the bridge allows today for
	// all 37, so empty is the behaviour-preserving value rather than an oversight.
	ConcurrencyKey string

	// Liveness declares how the job keeps the watchdog informed. LivenessManual
	// means the job calls UpdateProgress itself; note that Log does NOT stamp
	// liveness, so a job that only logs is struck as never_reported.
	Liveness opsregistry.LivenessMode

	// Capabilities are the system capabilities the job needs.
	Capabilities []opsregistry.Capability
}

// DefaultPolicy returns exactly what the maintenance.job bridge hardcodes for all
// 37 jobs today (internal/server/maintenance_job_op.go:38-42): ResumeDrop, a 4h
// timeout, no concurrency key, LivenessManual, and library read+write.
//
// A job whose Policy() is DefaultPolicy() is asserting "the bridge's behaviour is
// correct for me" — which is the behaviour-preserving default and true for 33 of
// the 37. Reviewing this PR therefore means checking DefaultPolicy() once against
// the bridge, then confirming the four exceptions below; it does not mean reading
// 37 struct literals for a transposed field.
//
// ConcurrencyKey is deliberately empty. That is what the bridge allows today, so
// it is the behaviour-preserving value — not an oversight. PR-2 is where per-job
// keys become expressible, and where a job that must not run twice concurrently
// should get one.
func DefaultPolicy() ExecutionPolicy {
	return ExecutionPolicy{
		ResumePolicy:   opsregistry.ResumeDrop,
		Timeout:        4 * time.Hour,
		ConcurrencyKey: "",
		Liveness:       opsregistry.LivenessManual,
		Capabilities: []opsregistry.Capability{
			opsregistry.CapLibraryRead,
			opsregistry.CapLibraryWrite,
		},
	}
}

// RestartPolicy is DefaultPolicy with ResumeRestart — "reload last checkpoint and
// call Run again".
//
// ⚠️ Correct ONLY for a job that actually checkpoints. The watchdog writes an
// `uncheckpointed` strike against every ResumeRestart op that goes quiet, and
// checkInfiniteRestart force-drops one whose HighWaterProgress never leaves 0.
// Use RequeuePolicy for a job that re-runs from scratch.
func RestartPolicy() ExecutionPolicy {
	p := DefaultPolicy()
	p.ResumePolicy = opsregistry.ResumeRestart
	return p
}

// RequeuePolicy is DefaultPolicy with ResumeRequeue — "re-run from zero".
//
// ⚠️ Restricted to jobs that are BOTH idempotent AND free of a dry_run parameter.
// The second condition is not theoretical: the two requeue implementations
// disagree about params. registry.resumeRequeue (resume.go) carries
// Params: row.Params forward, but server.resumeV2Op (server_lifecycle.go:122-127)
// re-enqueues with literal nil — under which DryRun unmarshals to Go's zero value,
// false, silently turning an interrupted PREVIEW into a real mutation. That is the
// exact bug maintenance_dispatcher.go:180 already exists to prevent.
//
// Until that divergence is resolved (todo.d/20260817-resumerequeue-two-divergent-implementations.md),
// a job advertising dry_run keeps DefaultPolicy's ResumeDrop.
func RequeuePolicy() ExecutionPolicy {
	p := DefaultPolicy()
	p.ResumePolicy = opsregistry.ResumeRequeue
	return p
}

// PolicyAware is satisfied by every MaintenanceJob. It is a separate interface
// only so that PR-2's registration code can accept the policy without depending
// on the rest of the v1 job surface, which PR-2 deletes.
type PolicyAware interface {
	Policy() ExecutionPolicy
}

// MaintenanceJob is the interface that every maintenance job must satisfy.
type MaintenanceJob interface {
	// ID returns the kebab-case identifier used in route paths and operation types.
	ID() string
	// Name returns the human-readable display name shown in the UI.
	Name() string
	// Description returns a one-sentence description of what the job does.
	Description() string
	// Category groups related jobs in the UI (e.g. "library", "files", "itunes", "dedup", "cleanup").
	Category() string
	// DefaultParams returns a struct with default parameter values (used by the frontend).
	DefaultParams() any
	// CanResume reports whether the job supports checkpoint-based resume after restart.
	//
	// Deprecated by PR-1 but still load-bearing: resumeLegacyOp
	// (internal/server/server_lifecycle.go) gates on this today. Policy().ResumePolicy
	// is the value PR-2 registers; CanResume goes away with the legacy sweep in PR-3.
	// Where the two disagree, Policy() is the intended one — see
	// TestPolicyAgreesWithCanResume for the exact permitted disagreements.
	CanResume() bool

	// Policy declares how the registry should schedule, time out, and resume this
	// job. See ExecutionPolicy — the zero value is invalid.
	Policy() ExecutionPolicy
	// Run executes the job. startFrom is the checkpoint index for resumable jobs (0 = fresh start).
	Run(ctx context.Context, store database.Store, reporter ProgressReporter, dryRun bool) error
}

var store database.Store

func InjectStore(s database.Store) { store = s }
func GetStore() database.Store     { return store }
