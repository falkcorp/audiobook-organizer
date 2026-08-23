// file: internal/maintenance/job.go
// version: 1.7.0
// guid: 11111111-1111-1111-1111-111111111111
// last-edited: 2026-08-23

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
// As of PR-2 the bridge is gone and RegisterMaintenanceJobOps reads this per job,
// so a value here is now load-bearing rather than declarative. Four jobs
// (backfill-file-hashes, bulk-fetch-metadata, recompute-book-aggregates,
// retention-and-hygiene) declare something other than DefaultPolicy(); the other
// 33 assert the bridge's old behaviour was correct for them.
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

// RestartPolicy is DefaultPolicy with ResumeRestart — "resume this run in place":
// resumeRestart increments resume_count and re-dispatches the SAME row, so the
// operation id, its params and its per-item results all survive.
//
// Choose this when the OPERATION ID is the anchor. For a maintenance job it always
// is: maintenance_job_op.go keys the activity summary and GetOperationSummaryLog
// off the run's own v2 id, and the 202 response hands that id to the operator.
// Three jobs additionally rebuild their skip-set from GetOperationResults(id) —
// scan-composer-tags, repair-missing-files and bulk-fetch-metadata — which is the
// same shape metadata.candidate-fetch uses, and whose comment there calls the
// filter "load-bearing for ResumePolicy=ResumeRestart". ResumeRequeue mints a
// fresh ULID and moves all of that.
//
// This does NOT require the job to checkpoint. It said so until 2026-08-23, on two
// consequences that have both since been fixed at their source:
//
//   - the watchdog's `uncheckpointed` strike now gates on the def's declared
//     MinCheckpointInterval rather than on ResumePolicy, so it fires only against a
//     def that promised a cadence and missed it; and
//   - high_water_progress now advances on every progress report rather than only at
//     a Checkpoint call, so checkInfiniteRestart no longer force-drops a job that
//     reported real progress.
//
// A maintenance job could never have complied with the old rule anyway:
// ProgressReporter above declares only SetTotal/Increment/Log and has no Checkpoint
// method to call.
func RestartPolicy() ExecutionPolicy {
	p := DefaultPolicy()
	p.ResumePolicy = opsregistry.ResumeRestart
	return p
}

// RequeuePolicy is DefaultPolicy with ResumeRequeue — "re-run from zero".
//
// ⚠️ Restricted to jobs that are idempotent AND whose operation id is not an
// anchor — requeue mints a fresh ULID, so any skip-set keyed on
// GetOperationResults(id), and the id handed back to the operator, are both lost.
//
// HISTORY, because the reason recorded here was overtaken. This used to add "and
// free of a dry_run parameter", because server.resumeV2Op re-enqueues with literal
// nil params, under which DryRun unmarshals to false and an interrupted PREVIEW
// resumes as a real mutation. Two things retired that argument: resumeV2Op is
// unreachable for maintenance (it dispatches only when opRegistry.Def(op.Type)
// resolves, and v1 maintenance rows are typed "maintenance:<job>" while v2 defs are
// "maintenance.<job>" — RegisterOp rejects ids containing ':'), and retiring the v1
// op minter means no v1 row is created to reach it with. registry.resumeRequeue
// itself carries Params forward, which TestResume_PreservesParamsAcrossRestartAndRequeue
// pins for both arms including dry_run:true. The underlying divergence between the
// two requeue implementations is still recorded in TODO.md, but it no longer gates
// this choice.
func RequeuePolicy() ExecutionPolicy {
	p := DefaultPolicy()
	p.ResumePolicy = opsregistry.ResumeRequeue
	return p
}

// The maintenance jobs' database surface, grouped by what each part is for.
//
// JobStore was twelve database.* embeds — 187 methods — after #2534 narrowed
// Run's parameter down from database.Store (398). That was the right move at the
// time and the arbitration deliberately chose a shared store over per-job
// interfaces. What it could not know is how little of the 187 the jobs touch.
//
// Measured 2026-08-18 by emptying JobStore and reading the compiler's
// enumeration across all 37 jobs: 37 methods called directly, plus 15 more
// reached only through the narrow slices in jobs/store_slices.go. 52 of 187.
//
// Kept as a composition rather than a flat list because interfacebloat counts
// declared entries: the flat form would trade a smaller method set for a wider
// declaration. Seven entries leaves one slot of headroom under the limit of
// eight, so the next job needing a new capability adds a method to a group
// rather than restructuring this type.
//
// What this does NOT do: it does not delete database.MockStore, which satisfies
// the narrower JobStore just as it satisfied the wider one, so the job tests
// that build one still compile unchanged.
type jobBookReader interface {
	GetBookByID(id string) (*database.Book, error)
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetAllBooksFullFrom(afterID string, limit int) ([]database.Book, error)
	ListBookIDs() ([]string, error)
	GetBooksBySeriesIDCore(seriesID int) ([]database.BookCore, error)
	GetBookChangeHistory(bookID string, limit int) ([]database.MetadataChangeRecord, error)
}

type jobBookWriter interface {
	UpdateBook(id string, book *database.Book) (*database.Book, error)
	DeleteBook(id string) error
	RecomputeBookAggregates(bookID string) error
	MergeChapterBooks(primaryID string, srcIDs []string, commonTitle string, totalDuration float64) error
}

type jobBookStore interface {
	jobBookReader
	jobBookWriter
}

type jobBookFileReader interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	GetBookFilesNeedingDelugeImportCore() ([]database.BookFileCore, error)
}

type jobBookFileWriter interface {
	CreateBookFile(file *database.BookFile) error
	UpdateBookFile(id string, file *database.BookFile) error
	UpsertBookFile(file *database.BookFile) error
	SetBookFileHash(id, hash string) error
	DeleteBookFilesForBook(bookID string) error
}

type jobBookFileStore interface {
	jobBookFileReader
	jobBookFileWriter
}

type jobContributorStore interface {
	CreateAuthor(name string) (*database.Author, error)
	GetAllAuthors() ([]database.Author, error)
	GetAuthorByID(id int) (*database.Author, error)
	GetAuthorByName(name string) (*database.Author, error)
	GetAllSeries() ([]database.Series, error)
	GetAllSeriesBookCounts() (map[int]int, error)
	DeleteSeries(id int) error
}

type jobUserStateStore interface {
	ListUsers() ([]database.User, error)
	GetUserBookState(userID, bookID string) (*database.UserBookState, error)
	SetUserBookState(state *database.UserBookState) error
	SetUserPosition(userID, bookID, segmentID string, positionSeconds float64) error
	ListUserPositionsForBook(userID, bookID string) ([]database.UserPosition, error)
	AddBookUserTag(bookID string, tag string) error
	GetBookUserTags(bookID string) ([]string, error)
}

type jobOperationRecordStore interface {
	GetOperationByID(id string) (*database.Operation, error)
	GetOperationParams(opID string) ([]byte, error)
	GetOperationResults(operationID string) ([]database.OperationResult, error)
	CreateOperationResult(result *database.OperationResult) error
	SaveOperationSummaryLog(op *database.OperationSummaryLog) error
	ListOperations(limit, offset int) ([]database.Operation, int, error)
	DeleteOperationWithLogs(id string) error
}

type jobOperationStateStore interface {
	GetOperationState(opID string) ([]byte, error)
	SaveOperationState(opID string, state []byte) error
	DeleteOperationState(opID string) error
}

type jobOperationStore interface {
	jobOperationRecordStore
	jobOperationStateStore
}

// jobKVStore is settings plus the raw key/value space the retention and sweep
// jobs use for their own bookkeeping rows.
type jobKVStore interface {
	GetSetting(key string) (*database.Setting, error)
	SetSetting(key, value, typ string, isSecret bool) error
	GetRaw(key string) ([]byte, error)
	SetRaw(key string, value []byte) error
	DeleteRaw(key string) error
	ScanPrefix(prefix string) ([]database.KVPair, error)
	CountPrefix(prefix string) (int64, error)
}

type jobExternalIDStore interface {
	GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error)
	ReassignExternalIDs(oldBookID, newBookID string) error
}

// JobStore is the database contract every maintenance job runs against.
// Widening it should still feel like a decision: a job needing a genuinely new
// capability adds one method to one group, and that line is the review surface.
type JobStore interface {
	jobBookStore
	jobBookFileStore
	jobContributorStore
	jobUserStateStore
	jobOperationStore
	jobKVStore
	jobExternalIDStore
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
	Run(ctx context.Context, store JobStore, reporter ProgressReporter, dryRun bool) error
}
