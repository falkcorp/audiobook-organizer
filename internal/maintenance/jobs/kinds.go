// file: internal/maintenance/jobs/kinds.go
// version: 1.0.1
// guid: a3972df8-a6a8-49ee-b157-c35c462e3c8d
// last-edited: 2026-11-12

package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// Native books Task kinds for the maintenance jobs (F5-01, 05-tasks 5T.8.2 group
// T2). One shell serves every job: each job's existing Run(ctx, store,
// reporter, dryRun) is the shared body, HEAD's wrapper
// (internal/server/maintenance_job_op.go) is its v2 shell and Kinds below is
// its native one. The kinds are DARK: nothing registers them before the tasks
// flip (F5-18), which also deletes internal/maintenance.
//
// Every job body reaches its stores only through the Store its shell hands it
// (05-tasks conversion rule 3): the native shell hands deps.NewStore(rc.Writer()).

// The groups below are maintenance.JobStore's, copied one for one: Store is
// built from them, one embedded interface per group, because interfacebloat
// counts declared entries and a flat list would be one 100-entry finding.
// The two compile-time assertions after Store keep the method sets equal.

type storeBookReader interface {
	storeBookLookup
	storeBookRelatedReader
}

type storeBookLookup interface {
	GetBookByID(id string) (*database.Book, error)
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetAllBooksFullFrom(afterID string, limit int) ([]database.Book, error)
	ListBookIDs() ([]string, error)
}

type storeBookRelatedReader interface {
	GetBooksBySeriesIDCore(seriesID int) ([]database.BookCore, error)
	GetBooksBySeriesIDAllVersions(seriesID int) ([]database.BookCore, error)
	GetBookChangeHistory(bookID string, limit int) ([]database.MetadataChangeRecord, error)
	CountBookSnapshots(id string) (int, error)
	GetBooksByVersionGroup(groupID string) ([]database.Book, error)
}

type storeBookWriter interface {
	UpdateBook(id string, book *database.Book) (*database.Book, error)
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	DeleteBook(id string) error
	RecomputeBookAggregates(bookID string) error
	PruneBookSnapshots(id string, keepCount int) (int, error)
}

type storeManualOnlyReader interface {
	GetSeriesByID(id int) (*database.Series, error)
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	GetBookTagsDetailed(bookID string) ([]database.BookTag, error)
}

type storeBookStore interface {
	storeBookReader
	storeBookWriter
	storeManualOnlyReader
	database.MetadataFieldStateReader
}

type storeBookFileReader interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
	GetBookFileByPath(filePath string) (*database.BookFile, error)
	BookFilesAtPath(path string) ([]database.BookFile, error)
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	GetBookFilesNeedingDelugeImportCore() ([]database.BookFileCore, error)
}

type storeBookFileWriter interface {
	CreateBookFile(file *database.BookFile) error
	BatchCreateBookFiles(files []*database.BookFile) error
	UpdateBookFile(id string, file *database.BookFile) error
	MoveBookFilesToBook(fileIDs []string, sourceBookID, targetBookID string) error
	SetBookFileHash(id, hash string) error
	ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
}

type storeBookFileStore interface {
	storeBookFileReader
	storeBookFileWriter
}

type storeContributorStore interface {
	CreateAuthor(name string) (*database.Author, error)
	GetAllAuthors() ([]database.Author, error)
	GetAuthorByID(id int) (*database.Author, error)
	GetAuthorByName(name string) (*database.Author, error)
	ModifyBookAuthors(bookID string, fn func([]database.BookAuthor) ([]database.BookAuthor, error)) ([]database.BookAuthor, error)
	GetAllSeries() ([]database.Series, error)
	GetAllSeriesBookCounts() (map[int]int, error)
	DeleteSeries(id int) error
}

type storeUserStateStore interface {
	ListUsers() ([]database.User, error)
	GetUserBookState(userID, bookID string) (*database.UserBookState, error)
	SetUserBookState(state *database.UserBookState) error
	SetUserPosition(userID, bookID, segmentID string, positionSeconds float64) error
	ListUserPositionsForBook(userID, bookID string) ([]database.UserPosition, error)
	AddBookUserTag(bookID string, tag string) error
	GetBookUserTags(bookID string) ([]string, error)
}

type storeOperationRecordStore interface {
	GetOperationByID(id string) (*database.Operation, error)
	GetOperationV2(id string) (*database.OperationV2Row, error)
	GetOperationResults(operationID string) ([]database.OperationResult, error)
	CreateOperationResult(result *database.OperationResult) error
	SaveOperationSummaryLog(op *database.OperationSummaryLog) error
	ListOperations(limit, offset int) ([]database.Operation, int, error)
	DeleteOperationWithLogs(id string) error
}

type storeOperationStateStore interface {
	GetOperationState(opID string) ([]byte, error)
	SaveOperationState(opID string, state []byte) error
	DeleteOperationState(opID string) error
}

type storeUndoLedgerStore interface {
	GetBookChanges(bookID string) ([]*database.OperationChange, error)
	CreateOperationChange(change *database.OperationChange) error
}

type storeOperationStore interface {
	storeOperationRecordStore
	storeOperationStateStore
	storeUndoLedgerStore
}

type storeKVStore interface {
	GetSetting(key string) (*database.Setting, error)
	SetSetting(key, value, typ string, isSecret bool) error
	database.RawKVStore
}

type storeExternalIDStore interface {
	GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error)
	ReassignExternalIDs(oldBookID, newBookID string) error
}

// Store is the narrow interface the converted jobs call (plan section (l) item
// 4): the method set of maintenance.JobStore, declared here so F5-18 can delete
// internal/maintenance.
//
// Several jobs also reach for a wider capability by a runtime assertion on this
// value (database.AsSyncIdentityStore, database.AsBookmarkStore, dedup.Store,
// fileops.BookFileHashRecorder and the others named in
// docs/agent-tasks/platform-core/seams.d/F5-01.md). What deps.NewStore returns
// must satisfy each of them, or that job refuses or silently skips.
type Store interface {
	storeBookStore
	storeBookFileStore
	storeContributorStore
	storeUserStateStore
	storeOperationStore
	storeKVStore
	storeExternalIDStore
	database.ChapterReader
}

var (
	_ Store                = maintenance.JobStore(nil)
	_ maintenance.JobStore = Store(nil)
)

// KindDeps is what the owner (books, from F5-18) declares for the kinds (rule
// R10; 05-tasks 5T.8.4 store-capture).
type KindDeps struct {
	// NewStore returns the job's store view for one run: its write methods go
	// through w (F5-18: internal/books/writer over rc.Writer()), its read
	// methods to the books store. w is nil for a ReadOnly kind (core hands a
	// ReadOnly run no writer), and such a job writes nothing. A nil NewStore, or
	// a nil Store from it, fails the run before the job starts.
	NewStore func(w tasks.Writer) Store
}

const (
	// kindNamePrefix is rule R3: tasks.Kind("books." + <HEAD def ID>), and the
	// HEAD def ID is "maintenance." + the job ID.
	kindNamePrefix = "books.maintenance."

	// signalMemDB is the readiness component the kinds wait for (rule R5,
	// NoWarmupWait false, which every maintenance def is). Books registers the
	// component with the health registry at F5-18.
	signalMemDB tasks.ReadySignal = "books.memdb"
)

// Kinds returns one native kind per registered maintenance job, in
// maintenance.All() order. Nothing registers them in production: F5-18 passes
// the list to Registrar.Tasks. sched is the schedule lookup of rule R9; nil
// means no kind is scheduled.
func Kinds(deps KindDeps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	jobs := maintenance.All()
	out := make([]tasks.Definition, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, kindFor(job, deps, sched))
	}
	return out
}

// kindsRoot names Kinds from non-test code so tasklint's unregistered-kind rule
// counts it a registration root until F5-18's Registrar.Tasks call replaces
// this line (a list function nothing references registers nothing, and the
// analyzer does not load test files). F5-18 deletes it.
var _ = Kinds

// kindFor builds one job's native kind.
func kindFor(job maintenance.MaintenanceJob, deps KindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	id := job.ID()
	kind := tasks.Kind(kindNamePrefix + id)
	pol := job.Policy()

	c := tasks.Common{
		Title:    job.Name(),
		Help:     job.Description(),
		Effects:  effectsFor(id),
		Perms:    tasks.Permissions{Start: startRequirement(job)},
		Resume:   resumeFor(pol.ResumePolicy),
		Priority: tasks.PriorityNormal,
		Timeout:  pol.Timeout,
		Ready:    []tasks.ReadySignal{signalMemDB},
	}
	if pol.ConcurrencyKey != "" {
		c.ConcurrencyKey = tasks.Key("books." + pol.ConcurrencyKey)
	}
	if sched != nil {
		c.Schedule = sched(kind)
	}
	shell := kindShell{kind: kind, job: job, deps: deps, liveOnly: !AdvertisedDryRunDefault(job)}
	return tasks.Task(kind, tasks.TaskSpec[json.RawMessage, json.RawMessage]{Common: c, Run: shell.run})
}

// resumeFor is rule R5's resume column. An unspecified policy stays zero, which
// registration refuses.
func resumeFor(p opsregistry.ResumePolicy) tasks.ResumePolicy {
	switch p {
	case opsregistry.ResumeRestart:
		return tasks.ResumeContinue
	case opsregistry.ResumeRequeue:
		return tasks.ResumeFromZero
	case opsregistry.ResumeDrop:
		return tasks.ResumeDrop
	case opsregistry.ResumeAsk:
		return tasks.ResumeAsk
	}
	return 0
}

// settingsManageScopes is the route requirement of HEAD's settings.manage atom
// (05-rbac 6.1, internal/books.AtomTable). internal/books imports this package
// through its import graph, so the table cannot be read from here; the
// external test TestJobKinds_StartRequirementMatchesAtomTable fails when the two
// drift.
var settingsManageScopes = []rbac.Permission{
	"core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
	"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage",
}

// startRequirement is rule R6: the job's PermissionAware atom, else
// settings.manage (the HEAD wrapper's rule). A permission other than
// settings.manage has no row here, so it yields the zero requirement and the
// kind is refused at registration (fail closed) rather than starting on a
// guessed scope.
func startRequirement(job maintenance.MaintenanceJob) rbac.Requirement {
	atom := "settings.manage"
	if pa, ok := job.(maintenance.PermissionAware); ok && pa.Permission() != "" {
		atom = pa.Permission()
	}
	if atom == "settings.manage" {
		return rbac.AllOf(settingsManageScopes...)
	}
	return rbac.Requirement{}
}

// jobWrites is rule R7's "Writes found": per job, the books resource kinds its
// body and every function it calls write, read off the write methods it calls
// on its store argument and the files it writes (reachability over the job's
// Run, listed per job in the PR description). An empty set is a job that
// writes nothing (ReadOnly). A job missing from the table gets no declared
// Effects, which registration refuses.
//
// Resource kinds (tasks.Res("books", kind)), named as pkg/ops names them:
// books, book_files, authors, series, external_ids, user_state, operations,
// settings, files (bytes on disk); sync_identity and metrics are coined here.
var jobWrites = map[string][]string{
	"backfill-book-files":             {"book_files", "operations"},
	"backfill-file-hashes":            {"books", "book_files", "operations", "files"},
	"backfill-itunes-positions":       {"user_state"},
	"backfill-merge-undo-pointers":    {"operations"},
	"backfill-metadata-source-hash":   {"books"},
	"backfill-sync-ids":               {"sync_identity"},
	"bulk-deluge-import":              {"book_files", "operations"},
	"cleanup-empty-folders":           {"files"},
	"cleanup-organize-mess":           {"files"},
	"cleanup-series":                  {"books", "series"},
	"dedup-books":                     {"books", "book_files", "external_ids", "user_state"},
	"enrich-book-files":               {"book_files"},
	"fix-author-narrator-swap":        {"books"},
	"fix-file-modes":                  {"files"},
	"fix-read-by-narrator":            {"books"},
	"fix-version-groups":              {"books", "book_files"},
	"merge-chapter-groups":            {"books", "book_files", "authors", "external_ids", "user_state", "operations", "sync_identity"},
	"normalize-primary-flags":         {"books"},
	"prune-book-snapshots":            {"books"},
	"purge-unknown-author-duplicates": {"books"},
	"recompute-book-aggregates":       {"books", "operations"},
	"recompute-itunes-paths":          {"book_files"},
	"refetch-missing-authors":         {"books", "authors"},
	"relink-missing-to-itunes":        {"books", "book_files"},
	"relink-report":                   {},
	"repoint-version-primary":         {"books"},
	"retention-and-hygiene":           {"operations", "settings"},
	"revert-metadata-fetch":           {"books"},
	"scan-chapter-groups":             {},
	"scan-composer-tags":              {"book_files", "operations", "files"},
	"scan-duplicate-files":            {},
	"scan-duration-mismatch":          {},
	"scan-metadata-hash-dups":         {},
	"sweep-pebble-metrics-ttl":        {"metrics"},
}

func effectsFor(jobID string) tasks.Effects {
	ws, ok := jobWrites[jobID]
	if !ok {
		return tasks.Effects{}
	}
	if len(ws) == 0 {
		return tasks.ReadOnly()
	}
	refs := make([]tasks.Resource, len(ws))
	for i, w := range ws {
		refs[i] = tasks.Res("books", w)
	}
	return tasks.Writes(refs...)
}

// kindShell is the native shell around one job's Run.
type kindShell struct {
	kind tasks.Kind
	job  maintenance.MaintenanceJob
	deps KindDeps
	// liveOnly is HEAD's "no preview": a job that advertises no dry-run default
	// declares no Preview at HEAD, and keeps refusing a preview here (rule R7,
	// "Live-only writer").
	liveOnly bool
}

// run is the native TaskSpec.Run of every maintenance kind (05-tasks 5T.8.2).
func (k kindShell) run(rc *tasks.RC, raw json.RawMessage) (json.RawMessage, error) {
	if k.liveOnly && rc.Mode() != tasks.ModeLive {
		return nil, fmt.Errorf("%s: preview not supported; start with mode live", k.kind)
	}
	if err := refuseModeKeys(k.kind, raw); err != nil {
		return nil, err
	}
	if k.deps.NewStore == nil {
		return nil, fmt.Errorf("%s: KindDeps.NewStore is not set", k.kind)
	}
	store := k.deps.NewStore(rc.Writer())
	if store == nil {
		return nil, fmt.Errorf("%s: KindDeps.NewStore returned no store", k.kind)
	}

	var res resultBox
	ctx := rc.Context()
	ctx = maintenance.WithOperationID(ctx, string(rc.RunID()))
	ctx = maintenance.WithRawParams(ctx, raw)
	ctx = maintenance.WithResultSetter(ctx, res.set)
	ctx = withCheckpointer(ctx, rcCheckpointer{rc: rc})

	// The write direction is never the default: only an explicit live mode
	// writes (a requeue or resume reaches here with the mode the run was
	// started with, never Go's zero value).
	dryRun := rc.Mode() != tasks.ModeLive
	runErr := k.job.Run(ctx, store, newRCProgress(rc), dryRun)

	out, merr := res.marshal()
	if merr != nil {
		return nil, errors.Join(fmt.Errorf("%s: encode result: %w", k.kind, merr), runErr)
	}
	if runErr != nil && out != nil {
		// HEAD stores a failed run's result on its row (merge-chapter-groups
		// persists a cancelled merge's outcomes and journal IDs on purpose).
		// core/tasks discards the result of a Run that returns an error, so until
		// it keeps one the payload is written to the run's log rather than lost.
		rc.Log().Warn("run failed; its partial result follows", "kind", string(k.kind), "result", string(out))
	}
	if errors.Is(runErr, maintenance.ErrContinueInPlace) {
		// The job's "restart this same run" is core's ErrContinueInPlace.
		runErr = fmt.Errorf("%w: %w", tasks.ErrContinueInPlace, runErr)
	}
	return out, runErr
}

// refuseModeKeys is 05-tasks 5T.3.7 mode contract item 1: a dry_run or dryRun
// key in params is refused (the start route answers 400 first; this is the
// backstop). Keys are compared case-insensitively because the jobs decode their
// params with encoding/json, which matches field names that way. Empty and
// null params are fine: a requeue starts with none.
func refuseModeKeys(kind tasks.Kind, raw json.RawMessage) error {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(t, &m); err != nil {
		return fmt.Errorf("%s: decode params: %w", kind, err)
	}
	for key := range m {
		if strings.EqualFold(key, "dry_run") || strings.EqualFold(key, "dryRun") {
			return fmt.Errorf("%s: params carry %q; the run's mode comes from the start request, not params", kind, key)
		}
	}
	return nil
}

// resultBox captures the value a job passes to maintenance.SetResult.
type resultBox struct {
	mu  sync.Mutex
	has bool
	v   any
}

func (b *resultBox) set(v any) error {
	b.mu.Lock()
	b.has, b.v = true, v
	b.mu.Unlock()
	return nil
}

// marshal returns the captured result, or nil (JSON null) when the job set none.
func (b *resultBox) marshal() (json.RawMessage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.has {
		return nil, nil
	}
	return json.Marshal(b.v)
}

// checkpointer is what the checkpointing jobs call instead of the v1 helpers
// when their ctx carries one. HEAD's wrapper carries none, so the jobs keep
// calling operations.SaveCheckpoint/LoadCheckpoint exactly as before.
type checkpointer interface {
	Load() (*operations.OperationState, error)
	Save(opType, phase string, index, total int) error
}

type checkpointerKey struct{}

func withCheckpointer(ctx context.Context, c checkpointer) context.Context {
	return context.WithValue(ctx, checkpointerKey{}, c)
}

// checkpointerFrom returns the native shell's checkpointer, or nil under HEAD's
// wrapper.
func checkpointerFrom(ctx context.Context) checkpointer {
	c, _ := ctx.Value(checkpointerKey{}).(checkpointer)
	return c
}

// rcCheckpointer persists HEAD's own checkpoint struct (operations.OperationState)
// through rc.Checkpoint/rc.Restore, so Restore decodes the bytes HEAD's
// SaveCheckpoint wrote (05-tasks 5T.3.6, an imported run carries HEAD's bytes).
type rcCheckpointer struct{ rc *tasks.RC }

func (c rcCheckpointer) Load() (*operations.OperationState, error) {
	var st operations.OperationState
	ok, err := c.rc.Restore(&st)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return &st, nil
}

func (c rcCheckpointer) Save(opType, phase string, index, total int) error {
	return c.rc.Checkpoint(operations.OperationState{
		OperationID: string(c.rc.RunID()),
		Type:        opType,
		Phase:       phase,
		PhaseIndex:  index,
		PhaseTotal:  total,
		Status:      "running",
		UpdatedAt:   time.Now(),
	})
}

// rcProgress is the native maintenance.ProgressReporter (and ProgressSeeder)
// over rc.Progress()/rc.Log(). Jobs report from their own worker pools, so the
// counters and the report that carries them sit under one mutex: a report never
// carries a count older than one already sent.
type rcProgress struct {
	rc    *tasks.RC
	mu    sync.Mutex
	done  int64
	total int64
}

var (
	_ maintenance.ProgressReporter = (*rcProgress)(nil)
	_ maintenance.ProgressSeeder   = (*rcProgress)(nil)
)

func newRCProgress(rc *tasks.RC) *rcProgress { return &rcProgress{rc: rc} }

func (p *rcProgress) SetTotal(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.total = int64(n)
	p.reportLocked()
}

func (p *rcProgress) Increment() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done++
	p.reportLocked()
}

// SeedProgress moves the count to done for a job that resumes by skipping rows
// the earlier attempt finished.
func (p *rcProgress) SeedProgress(done int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done = int64(done)
	p.reportLocked()
}

func (p *rcProgress) reportLocked() {
	total := tasks.Unknown()
	if p.total > 0 {
		total = tasks.Known(p.total)
	}
	p.rc.Progress().Set(p.done, total)
}

// Log writes the job's line to the run's logger at the level it names (an
// unknown level logs at info) and does not stamp liveness, as at HEAD.
func (p *rcProgress) Log(level, message string, details *string) {
	lvl := slog.LevelInfo
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	if details != nil {
		p.rc.Log().Log(p.rc.Context(), lvl, message, "details", *details)
		return
	}
	p.rc.Log().Log(p.rc.Context(), lvl, message)
}
