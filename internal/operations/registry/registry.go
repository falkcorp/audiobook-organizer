// file: internal/operations/registry/registry.go
// version: 3.10.0
// guid: f6a7b8c9-d0e1-2f3a-4b5c-6d7e8f9a0b1c
// last-edited: 2026-08-07

package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
	"github.com/oklog/ulid/v2"
)

// Registry is the central in-memory and DB-backed object that owns every
// OperationDef, dispatches runs, enforces policies, and routes events.
type Registry struct {
	mu              sync.RWMutex
	defs            map[string]OperationDef
	running         map[string]*runHandle // opID → handle
	pluginRunning   map[string]int        // plugin → count of running ops
	pluginMax       map[string]int        // plugin → max_concurrent (0 = unlimited)
	concurrencyKeys map[string]string     // key → opID of holder
	// writeSetDeferred dedupes the Gate-3b deferral log line: queued opID →
	// running opID last logged as blocking it. Dispatcher-goroutine-private
	// (only dispatchCycle and its helpers touch it), so it is NOT guarded by
	// mu; pruned every cycle against the live queued set.
	writeSetDeferred map[string]string
	nextRun          chan *queuedRun
	dispatch         chan struct{}
	store            database.OpsV2Store
	bus              Bus // may be nil; wired in UOS-06
	activityRecorder ActivityRecorder
	logger           *slog.Logger
	workers          int
	abandoned        *abandonedTracker

	// shuttingDown is flipped at the top of Shutdown so the abandoned-run
	// watchdog in executeRun stops spawning replacement workers. Without
	// this flag the watchdog respawns a worker right as bgCtx is being
	// canceled — the new worker's runs then race against database.Close()
	// and panic with "pebble: closed".
	shuttingDown atomic.Bool

	// depsScheduler is the optional dependency-scheduling coordinator.
	// Set via SetDepsScheduler before Start(). Nil is safe: worker hooks
	// check for nil before notifying.
	depsScheduler *DepsScheduler

	// depBookStore provides GetBookByID and BookFiles for dep evaluation.
	// Set via SetDepBookStore before Start(). Nil is safe: combinedDepStore()
	// falls back to OpsV2DepAdapter (conservative: field_set always unmet).
	depBookStore DepBookStore

	// runContextDecorator optionally decorates each run's context before the
	// op's Run func is invoked (e.g. to stamp SLOG operation-id correlation).
	// Set via SetRunContextDecorator before Start(). Nil is safe: the worker
	// skips decoration and runs with the plain per-run context.
	//
	// This indirection exists so this package never imports the internal
	// logging package directly — that import would leak into
	// pkg/plugin/sdk's dependency tree via this package
	// (SDKGUARD-VIOLATION #1795). Production wiring happens
	// post-construction in internal/server/registry_wire.go.
	runContextDecorator func(ctx context.Context, opID string) context.Context

	// batch manages the M3 per-op-type debounce buckets for Batchable ops.
	batch *batchManager

	// cancelFn cancels the internal goroutine context created in Start().
	// Shutdown() calls this after draining running ops to stop the
	// dispatcher, watchdog, and idle workers before returning.
	cancelFn    context.CancelFunc
	goroutineWG sync.WaitGroup // tracks dispatcher + watchdog + workers + dep-notify goroutines

	// notifyStopped is set (under mu) in Shutdown just before goroutineWG.Wait()
	// so no new dep-notify goroutine can Add to the WaitGroup after the Wait has
	// begun. releaseRunHandle removes an op from r.running BEFORE the worker
	// calls notifyDepCompletion, so Shutdown's drain loop can reach Wait() in the
	// gap before a notify goroutine enrolls — gating the Add under mu against
	// this flag closes that "Add after Wait" window. Reset in Start() for restart.
	notifyStopped bool

	// Tunable intervals for testing. Zero means use defaults.
	watchdogInterval time.Duration
	// abandonGrace is how long a ctx-canceled op goroutine has to return before
	// it is classified as abandoned. Zero means use defaultAbandonGrace.
	abandonGrace time.Duration
	// sweepInterval controls how often DepsScheduler.SweepTick fires.
	// Zero means use the default (5m).
	sweepInterval time.Duration

	// sweepStopped is closed by the DepsScheduler sweep-ticker goroutine when
	// it exits. Shutdown joins on it UNCONDITIONALLY (after canceling the
	// internal context) so an in-flight SweepTick — which reads the store via
	// ListWaitingDepsOps — is guaranteed to finish before Shutdown returns and
	// the caller closes the store. Without this, the generic goroutineWG.Wait()
	// abandons the tick goroutine after its 2s escape, letting the caller close
	// the store while a sweep is mid-iteration (panic "pebble: closed",
	// PEBBLE-CLOSED-SWEEPTICK-RESIDUAL). nil when no scheduler was wired at
	// Start (no ticker goroutine spawned). Set fresh in each Start().
	sweepStopped chan struct{}
}

// Options contains optional tunable parameters for a Registry. Zero values
// use sensible defaults. Primarily used in tests to shorten intervals.
type Options struct {
	// WatchdogInterval overrides the 30-second watchdog ticker. Zero = default.
	WatchdogInterval time.Duration
	// AbandonedCap overrides the per-plugin abandoned goroutine cap (default 4).
	AbandonedCap int
	// AbandonGrace overrides how long a ctx-canceled op goroutine has to return
	// before it is classified as abandoned (default 5s). Zero = default.
	// Primarily used in tests to make shutdown-drain behavior fast.
	AbandonGrace time.Duration
	// Bus is the SSE event bus (UOS-06). Nil is safe.
	Bus Bus
	// SweepInterval overrides how often DepsScheduler.SweepTick is called.
	// Zero = default (5m). Only meaningful when a DepsScheduler is wired.
	SweepInterval time.Duration
}

// New creates a new Registry. workers controls the in-process worker pool size.
// store must implement database.OpsV2Store; the database.Store composite
// interface satisfies this automatically.
// bus may be nil; it will be wired to the real EventHub in UOS-06.
func New(store database.OpsV2Store, logger *slog.Logger, workers int, bus Bus) *Registry {
	return NewWithOptions(store, logger, workers, Options{Bus: bus})
}

// NewWithOptions is like New but accepts optional tunable parameters.
func NewWithOptions(store database.OpsV2Store, logger *slog.Logger, workers int, opts Options) *Registry {
	if workers <= 0 {
		workers = 8
	}
	return &Registry{
		defs:             make(map[string]OperationDef),
		running:          make(map[string]*runHandle),
		pluginRunning:    make(map[string]int),
		pluginMax:        make(map[string]int),
		concurrencyKeys:  make(map[string]string),
		writeSetDeferred: make(map[string]string),
		nextRun:          make(chan *queuedRun, workers*2),
		dispatch:         make(chan struct{}, 1),
		store:            store,
		bus:              opts.Bus,
		logger:           logger,
		workers:          workers,
		abandoned:        newAbandonedTracker(opts.AbandonedCap),
		watchdogInterval: opts.WatchdogInterval,
		abandonGrace:     opts.AbandonGrace,
		sweepInterval:    opts.SweepInterval,
		batch:            newBatchManager(),
	}
}

// SetDepsScheduler wires the dependency scheduler. Must be called BEFORE
// Start(). The scheduler's OnOpCompleted and OnOpFailed are notified
// asynchronously by the worker on status transitions.
func (r *Registry) SetDepsScheduler(s *DepsScheduler) {
	r.mu.Lock()
	r.depsScheduler = s
	r.mu.Unlock()
}

// DepBookStore is the narrow interface the dep evaluator needs to load a book
// and enumerate its files. It is satisfied by *database.PebbleStore in
// production (via a thin shim that maps GetBookFiles→BookFiles) and by any
// fake in tests.
//
// Separation from database.Store is intentional: keeping this interface small
// prevents the registry package from pulling in the full 50-method Store.
type DepBookStore interface {
	GetBookByID(id string) (*database.Book, error)
	// BookFiles returns the file IDs belonging to bookID.
	// May return nil, nil when file enumeration is not available;
	// the evaluator treats an empty list as "unmet" for AllFiles requirements.
	BookFiles(bookID string) ([]string, error)
}

// SetDepBookStore wires the book source used by the dep evaluator for
// ReqFieldSet and AllFiles checks. Must be called BEFORE Start().
// When nil (default), the evaluator falls back to the conservative
// OpsV2DepAdapter (GetBookByID always returns nil → field unmet).
func (r *Registry) SetDepBookStore(bs DepBookStore) {
	r.mu.Lock()
	r.depBookStore = bs
	r.mu.Unlock()
}

// SetRunContextDecorator wires a function that decorates each run's context
// immediately before Run is invoked (e.g. logger.WithOperation, which stamps
// a context-bound *slog.Logger tagged with the operation id for SLOG
// correlation). Must be called BEFORE Start(), mirroring SetDepBookStore:
// the worker reads r.runContextDecorator without locking (see
// combinedDepStore's identical precedent), so it is only safe to set once
// at startup. Nil is safe and simply disables decoration — runs then
// execute with the plain per-run context.
//
// This setter exists so the registry package never has to import the
// internal logging package (or any other decorator source) directly:
// importing it here would leak into pkg/plugin/sdk's allowed-dependency
// tree, since the SDK's public surface is built on type aliases into this
// package (SDKGUARD-VIOLATION #1795). Callers wire the concrete decorator
// post-construction — see internal/server/registry_wire.go.
func (r *Registry) SetRunContextDecorator(fn func(ctx context.Context, opID string) context.Context) {
	r.mu.Lock()
	r.runContextDecorator = fn
	r.mu.Unlock()
}

// combinedDepStore returns a DepStore that delegates OpsV2 methods to r.store
// and book-lookup methods to r.depBookStore. When depBookStore is nil it
// returns OpsV2DepAdapter{r.store}, preserving the original conservative
// behaviour (all field_set requirements unmet).
//
// Called from batchFire and EnqueueOp — both run under r.mu, but since we
// only need a snapshot of the pointer, we access it without locking here.
// Setting depBookStore is safe because it happens once at startup before
// Start() is called.
func (r *Registry) combinedDepStore() DepStore {
	if r.depBookStore == nil {
		return OpsV2DepAdapter{r.store}
	}
	return &combinedDepStoreImpl{ops: r.store, books: r.depBookStore}
}

// combinedDepStoreImpl implements DepStore by delegating OpsV2 calls to ops
// and book lookups to books.
type combinedDepStoreImpl struct {
	ops   database.OpsV2Store
	books DepBookStore
}

func (c *combinedDepStoreImpl) GetDepRev(sub database.OpSubject) (uint64, error) {
	return c.ops.GetDepRev(sub)
}
func (c *combinedDepStoreImpl) GetOpCompletion(sub database.OpSubject, opType string) (uint64, bool, error) {
	return c.ops.GetOpCompletion(sub, opType)
}
func (c *combinedDepStoreImpl) ListFileCompletions(sub database.OpSubject, opType string) (map[string]uint64, error) {
	return c.ops.ListFileCompletions(sub, opType)
}
func (c *combinedDepStoreImpl) BookFiles(bookID string) ([]string, error) {
	return c.books.BookFiles(bookID)
}
func (c *combinedDepStoreImpl) GetBookByID(id string) (*database.Book, error) {
	return c.books.GetBookByID(id)
}

// notifyDepCompletion notifies the scheduler (if wired) that opID completed for
// the given subject asynchronously so the worker is never blocked.
func (r *Registry) notifyDepCompletion(sub Subject, opType string) {
	// Enroll the goroutine in goroutineWG under mu so Shutdown drains it before
	// the caller closes the store (avoids a "pebble: closed" panic). The mu-gated
	// notifyStopped check prevents Add-after-Wait once Shutdown has begun waiting.
	r.mu.Lock()
	sched := r.depsScheduler
	if sched == nil || r.notifyStopped {
		r.mu.Unlock()
		return
	}
	r.goroutineWG.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.goroutineWG.Done()
		if err := sched.OnOpCompleted(context.Background(), sub, opType); err != nil {
			r.logger.Warn("deps_scheduler: OnOpCompleted error", "op_type", opType, "error", err)
		}
	}()
}

// notifyDepFailed notifies the scheduler (if wired) that opID failed for the
// given subject asynchronously so the worker is never blocked.
func (r *Registry) notifyDepFailed(sub Subject, opType string) {
	// See notifyDepCompletion for why enrollment is gated under mu.
	r.mu.Lock()
	sched := r.depsScheduler
	if sched == nil || r.notifyStopped {
		r.mu.Unlock()
		return
	}
	r.goroutineWG.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.goroutineWG.Done()
		if err := sched.OnOpFailed(context.Background(), sub, opType); err != nil {
			r.logger.Warn("deps_scheduler: OnOpFailed error", "op_type", opType, "error", err)
		}
	}()
}

// SetBus wires an EventHub to the registry so that operation lifecycle
// events (op.created, op.updated, op.log, op.terminal) are published
// as SSE events. Must be called BEFORE Start(). Safe to call with nil.
func (r *Registry) SetBus(bus Bus) {
	r.mu.Lock()
	r.bus = bus
	r.mu.Unlock()
}

// SetActivityRecorder mirrors operation log lines into the unified Activity
// Log. Safe to call with nil.
func (r *Registry) SetActivityRecorder(recorder ActivityRecorder) {
	r.mu.Lock()
	r.activityRecorder = recorder
	r.mu.Unlock()
}

// SetPluginMaxConcurrent configures the per-plugin concurrency cap.
// A value of 0 (the default) means unlimited.
func (r *Registry) SetPluginMaxConcurrent(plugin string, max int) {
	r.mu.Lock()
	r.pluginMax[plugin] = max
	r.mu.Unlock()
}

// Start launches the dispatcher and worker goroutines. Call once at startup.
// resumeAfterStartup is called first (synchronously in a goroutine context)
// to re-queue or drop ops that were in-flight at the last shutdown.
func (r *Registry) Start(ctx context.Context) {
	r.logger.Info("registry: starting", "workers", r.workers)
	trackLiveRegistry(r)
	// Clear the notify gate in case this Registry is being restarted after a
	// prior Shutdown (Shutdown sets notifyStopped to reject late enrollments).
	r.mu.Lock()
	r.notifyStopped = false
	r.mu.Unlock()
	// Resume must complete before the dispatcher starts accepting new work.
	r.resumeAfterStartup(ctx)
	// Reload any journaled batch buckets from the previous run and re-arm their
	// debounce timers. Must happen after resumeAfterStartup (so defs are registered)
	// and before goroutines start, so the context is already set.
	r.batchReloadOnStart(ctx)

	// Load the dependency scheduler's waiting_deps index now (kept out of
	// NewDepsScheduler so constructing the registry never touches the store).
	// Runs before any goroutine below, so no lock is needed on the index.
	r.mu.RLock()
	startSched := r.depsScheduler
	r.mu.RUnlock()
	if startSched != nil {
		startSched.rebuildIndex()
	}

	// Owned context: Shutdown() cancels this after draining running ops so
	// DB-touching goroutines stop before the caller closes the store.
	internalCtx, cancel := context.WithCancel(ctx)
	r.cancelFn = cancel

	r.goroutineWG.Add(1)
	go func() { defer r.goroutineWG.Done(); r.runDispatcher(internalCtx) }()
	r.goroutineWG.Add(1)
	go func() { defer r.goroutineWG.Done(); r.runWatchdog(internalCtx) }()
	for i := range r.workers {
		r.goroutineWG.Add(1)
		go func(slot int) { defer r.goroutineWG.Done(); r.startWorker(internalCtx, slot) }(i)
	}

	// Wire the dependency-scheduler sweep ticker if a scheduler has been set.
	// The goroutine is enrolled in goroutineWG so Shutdown() drains it cleanly.
	r.mu.RLock()
	sched := r.depsScheduler
	r.mu.RUnlock()
	r.sweepStopped = nil
	if sched != nil {
		sweepInterval := r.sweepInterval
		if sweepInterval <= 0 {
			sweepInterval = 5 * time.Minute
		}
		sweepStopped := make(chan struct{})
		r.sweepStopped = sweepStopped
		r.goroutineWG.Add(1)
		go func() {
			defer r.goroutineWG.Done()
			// Signal Shutdown that the ticker goroutine has fully exited so it
			// can safely let the caller close the store. See sweepStopped.
			defer close(sweepStopped)
			ticker := time.NewTicker(sweepInterval)
			defer ticker.Stop()
			for {
				select {
				case <-internalCtx.Done():
					return
				case <-ticker.C:
					// Skip (and exit) if shutdown has begun: a SweepTick started
					// after the internal context is canceled reads the store via
					// ListWaitingDepsOps and could touch it after the caller
					// closes it (PEBBLE-CLOSED-SWEEPTICK-RESIDUAL). This closes
					// the "next-tick" window; the explicit join in Shutdown
					// closes the "in-flight tick" window.
					if internalCtx.Err() != nil {
						return
					}
					r.mu.RLock()
					activeSched := r.depsScheduler
					r.mu.RUnlock()
					if activeSched != nil {
						activeSched.SweepTick(internalCtx)
					}
				}
			}
		}()
	}
}

// RegisterOp validates and registers an OperationDef.
// Returns an error if:
//   - def.ID is empty
//   - def.ID contains ':' (reserved by the completion keyspace)
//   - def.Run is nil
//   - def.ResumePolicy == ResumeUnspecified
//   - def.ID is already registered
func (r *Registry) RegisterOp(def OperationDef) error {
	if def.ID == "" {
		return errors.New("registry: OperationDef.ID must not be empty")
	}
	if strings.Contains(def.ID, ":") {
		return fmt.Errorf("registry: OperationDef.ID must not contain ':' (reserved by completion keyspace): %q", def.ID)
	}
	if def.Run == nil {
		return fmt.Errorf("registry: OperationDef.Run must not be nil (id=%s)", def.ID)
	}
	if def.ResumePolicy == ResumeUnspecified {
		return fmt.Errorf("registry: OperationDef.ResumePolicy must not be ResumeUnspecified (id=%s)", def.ID)
	}

	r.mu.Lock()
	if _, exists := r.defs[def.ID]; exists {
		r.mu.Unlock()
		return fmt.Errorf("registry: OperationDef already registered (id=%s)", def.ID)
	}

	// Cycle check: build the full requirement graph including the new def and
	// verify there are no cycles. Rejects the registration if a cycle is found.
	// This runs under the write lock so the graph view is consistent.
	if len(def.Requires) > 0 {
		graph := make(map[string][]Requirement, len(r.defs)+1)
		for id, d := range r.defs {
			graph[id] = d.Requires
		}
		graph[def.ID] = def.Requires
		if err := CheckRequirementCycle(graph); err != nil {
			r.mu.Unlock()
			return fmt.Errorf("registry: RegisterOp(%s): %w", def.ID, err)
		}
	}

	r.defs[def.ID] = def
	r.mu.Unlock()

	// Persist to op_definitions_v2. Best-effort; log on error.
	if err := r.upsertDefToDB(def); err != nil {
		r.logger.Warn("registry: failed to upsert op_definitions_v2", "id", def.ID, "error", err)
	}

	r.logger.Info("registry: registered op", "id", def.ID, "plugin", def.Plugin)
	return nil
}

// upsertDefToDB writes the def to op_definitions_v2.
func (r *Registry) upsertDefToDB(def OperationDef) error {
	capsJSON, _ := json.Marshal(def.Capabilities)
	permsJSON, _ := json.Marshal(def.Permissions)
	triggersJSON, _ := json.Marshal(triggersToNames(def.Triggers))
	dependsJSON, _ := json.Marshal(def.DependsOn)
	phasesJSON, _ := json.Marshal(phaseNames(def.Phases))

	var schedCron *string
	if def.Schedule != nil {
		schedCron = def.Schedule
	}

	timeoutSecs := int(def.Timeout.Seconds())

	return r.store.UpsertOpDefinitionV2(database.OpDefinitionV2Row{
		ID:             def.ID,
		Plugin:         def.Plugin,
		DisplayName:    def.DisplayName,
		Description:    def.Description,
		Capabilities:   string(capsJSON),
		Permissions:    string(permsJSON),
		Cancellable:    def.Cancellable,
		Isolate:        def.Isolate,
		ResumePolicy:   resumePolicyName(def.ResumePolicy),
		ScheduleCron:   schedCron,
		Triggers:       string(triggersJSON),
		DependsOn:      string(dependsJSON),
		Phases:         string(phasesJSON),
		TimeoutSeconds: timeoutSecs,
		RegisteredAt:   time.Now().UTC(),
	})
}

// EnqueueOp creates a new queued run for the given def. Returns the ULID of
// the new run.
//
// Batchable ops: when def.Batchable is true this call does NOT immediately
// create a row. The subject is added to the per-op-type bucket and ("", nil)
// is returned. The op ID is assigned at flush time (timer fire). Callers that
// need the resulting op ID may subscribe to the op.created SSE event. All
// existing callers ignore or log the returned ID, so this is safe.
func (r *Registry) EnqueueOp(ctx context.Context, defID string, params any, opts ...EnqueueOption) (string, error) {
	r.mu.RLock()
	def, ok := r.defs[defID]
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("registry: unknown defID %q", defID)
	}

	// --- M3 Batching: intercept before ConcurrencyKey dedupe ---
	// For Batchable ops we bucket the subject and return early.
	// Per-enqueue WithRequires options cannot be journaled and are therefore
	// ignored for batchable ops (requirements must be on OperationDef.Requires).
	if def.Batchable {
		// Marshal params to extract the subject (same logic as the non-batch path).
		var rawParamsForSubject json.RawMessage
		if params != nil {
			b, err := json.Marshal(params)
			if err != nil {
				return "", fmt.Errorf("registry: batchable marshal params: %w", err)
			}
			rawParamsForSubject = b
		} else {
			rawParamsForSubject = json.RawMessage("{}")
		}
		sub := subjectFromParams(rawParamsForSubject)
		if sub.ID == "" {
			// No subject derivable — fall through to normal non-batched path so
			// no-subject batchable ops are not silently dropped.
			r.logger.Warn("registry: batchable op has no subject in params; falling through to non-batch enqueue",
				"def_id", defID)
		} else {
			bw, bmw := effectiveBatchWindows(def)
			r.batchAdd(defID, database.OpSubject{Type: sub.Type, ID: sub.ID}, bw, bmw)
			r.logger.Debug("registry: batchable op bucketed",
				"def_id", defID, "subject_type", sub.Type, "subject_id", sub.ID)
			return "", nil // op ID assigned at flush time
		}
	}

	// Dedupe: if this defID has a non-empty ConcurrencyKey, and an op for
	// the same defID is already queued or running, return the existing op
	// id rather than enqueueing a duplicate. ConcurrencyKey serializes
	// RUNS but doesn't dedupe QUEUE entries — without this guard, every
	// cron tick piles up another row while the previous run is still in
	// flight (symptom: Active Operations panel shows "Purge Soft-Deleted"
	// twice from one cron schedule + one maintenance.window pass).
	if def.ConcurrencyKey != "" {
		if active, listErr := r.store.ListActiveOperationsV2(); listErr == nil {
			for _, op := range active {
				if op.DefID != defID {
					continue
				}
				// C-3: skip zombie rows — a row can be left "running" with no
				// live run handle (crash windows, historical abandonment bugs).
				// Deduping against such a row returns a dead op's ID for every
				// future enqueue of this def until restart, silently disabling
				// the op type. Only "running" rows are checked: "queued" rows
				// legitimately have no handle until dispatched.
				if op.Status == "running" && !r.hasLiveHandle(op.ID) {
					r.logger.Warn("registry: enqueue dedupe skipping zombie running row (no live handle)",
						"op_id", op.ID, "def_id", defID)
					continue
				}
				r.logger.Info("registry: enqueue deduped — active op exists",
					"op_id", op.ID, "def_id", defID, "status", op.Status)
				return op.ID, nil
			}
		}
	}

	// Marshal params.
	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return "", fmt.Errorf("registry: marshal params: %w", err)
		}
		rawParams = b
	} else {
		rawParams = json.RawMessage("{}")
	}

	// Apply options.
	eopts := &EnqueueOptions{}
	for _, opt := range opts {
		opt(eopts)
	}

	// Priority: option overrides def default.
	priority := def.DefaultPriority
	if eopts.Priority != nil {
		priority = *eopts.Priority
	}

	// Combine def-level and per-enqueue requirements.
	// Use a fresh backing array to avoid aliasing def.Requires under -race.
	allReqs := append(append([]Requirement(nil), def.Requires...), eopts.Requires...)

	// Dependency check: evaluate requirements; park if any unmet.
	status := "queued"
	var subjectType, subjectID string
	var reqSnapshotRev uint64
	var requirementsJSON string

	if len(allReqs) > 0 {
		// Resolve subject from params (v1: book_id key).
		sub := subjectFromParams(rawParams)
		subjectType = sub.Type
		subjectID = sub.ID

		// Marshal requirements for persistence.
		if b, err := json.Marshal(allReqs); err == nil {
			requirementsJSON = string(b)
		}

		// Evaluate: if subject is empty or requirements unmet, park.
		satisfied := false
		if sub.ID != "" {
			// Snapshot the current dep_rev so wakeup can compare.
			if rev, err := r.store.GetDepRev(database.OpSubject{Type: sub.Type, ID: sub.ID}); err == nil {
				reqSnapshotRev = rev
			}
			ok, _, err := AllSatisfied(r.combinedDepStore(), allReqs, sub)
			if err == nil {
				satisfied = ok
			}
		}
		if !satisfied {
			status = "waiting_deps"
		}
	}

	// Generate ULID.
	opID := ulid.Make().String()

	now := time.Now().UTC()

	var parentID *string
	if eopts.ParentID != "" {
		parentID = &eopts.ParentID
	}
	var actorUserID *string
	if eopts.ActorUserID != "" {
		actorUserID = &eopts.ActorUserID
	}
	var parentSpanID *string
	if eopts.ParentSpanID != "" {
		parentSpanID = &eopts.ParentSpanID
	}
	traceID := eopts.TraceID
	if traceID == "" {
		traceID = ulid.Make().String()
	}
	spanID := eopts.SpanID
	if spanID == "" {
		spanID = ulid.Make().String()
	}

	row := database.OperationV2Row{
		ID:             opID,
		DefID:          def.ID,
		Plugin:         def.Plugin,
		ParentID:       parentID,
		ActorUserID:    actorUserID,
		TraceID:        traceID,
		SpanID:         spanID,
		ParentSpanID:   parentSpanID,
		Status:         status,
		Priority:       int(priority),
		Params:         string(rawParams),
		QueuedAt:       now,
		SubjectType:    subjectType,
		SubjectID:      subjectID,
		Requirements:   requirementsJSON,
		ReqSnapshotRev: reqSnapshotRev,
	}

	if err := r.store.InsertOperationV2(row); err != nil {
		return "", fmt.Errorf("registry: insert operation_v2: %w", err)
	}

	if status == "waiting_deps" {
		r.logger.Info("registry: parked op (waiting_deps)", "op_id", opID, "def_id", defID,
			"subject_type", subjectType, "subject_id", subjectID)
	} else {
		r.logger.Info("registry: enqueued op", "op_id", opID, "def_id", defID, "priority", priority)
	}

	r.publishOpCreated(row, false)

	// Signal the dispatcher only for queued ops (waiting_deps are not dispatchable).
	if status == "queued" {
		r.pingDispatch()
	}

	return opID, nil
}

// subjectFromParams extracts a Subject from a serialized params JSON blob.
// v1 convention: params["book_id"] → Subject{Type:"book", ID:<value>}.
// Returns a zero Subject when params don't contain a recognized subject key.
func subjectFromParams(params json.RawMessage) Subject {
	if len(params) == 0 {
		return Subject{}
	}
	var p map[string]json.RawMessage
	if err := json.Unmarshal(params, &p); err != nil {
		return Subject{}
	}
	if raw, ok := p["book_id"]; ok {
		var id string
		if err := json.Unmarshal(raw, &id); err == nil && id != "" {
			return Subject{Type: "book", ID: id}
		}
	}
	return Subject{}
}

// subjectsFromParams extracts all subjects from a serialized params JSON blob,
// handling both the v1 single-subject shape and the batched-op shape.
//
//   - v1 (single): params["book_id"] → one Subject{Type:"book", ID:<value>}
//   - batched:     params["subjects"] → []Subject decoded from OpSubject json tags
//
// Returns nil (empty slice) when params contain no recognized subject keys.
func subjectsFromParams(params json.RawMessage) []Subject {
	if len(params) == 0 {
		return nil
	}
	var p map[string]json.RawMessage
	if err := json.Unmarshal(params, &p); err != nil {
		return nil
	}

	// Batched shape: {"subjects":[{"type":"book","id":"..."},...]}
	if raw, ok := p["subjects"]; ok {
		var dbSubs []database.OpSubject
		if err := json.Unmarshal(raw, &dbSubs); err == nil {
			out := make([]Subject, 0, len(dbSubs))
			for _, s := range dbSubs {
				if s.ID != "" {
					out = append(out, Subject{Type: s.Type, ID: s.ID})
				}
			}
			return out
		}
	}

	// v1 single-subject shape: {"book_id":"..."}
	if raw, ok := p["book_id"]; ok {
		var id string
		if err := json.Unmarshal(raw, &id); err == nil && id != "" {
			return []Subject{{Type: "book", ID: id}}
		}
	}
	return nil
}

// publishOpCreated fans out an op.created SSE event so the UI's operations
// bell can pick up newly enqueued OR server-resumed ops without waiting for
// the next op.updated event. The "resumed" flag distinguishes startup
// resume from a fresh enqueue so the client can render a "Resumed" badge
// if desired (currently it just triggers loadFromServer()).
func (r *Registry) publishOpCreated(row database.OperationV2Row, resumed bool) {
	if r.bus == nil {
		return
	}
	_ = r.bus.Publish(context.Background(), "op.created", map[string]any{
		"op_id":    row.ID,
		"def_id":   row.DefID,
		"plugin":   row.Plugin,
		"status":   row.Status,
		"priority": row.Priority,
		"resumed":  resumed,
	})
}

// publishOpTerminal fans out an op.terminal SSE event (R-1) whenever an
// operation reaches a terminal status (completed, failed, canceled, timeout,
// interrupted_*, or force-dropped). Without it, the UI's operations bell keeps
// rendering a finished op as "running" until the next manual refresh — the
// backend never told it the op ended. The frontend
// (web/src/stores/useOperationsStore.ts) responds to this event by reloading
// operation state from the server.
//
// context.Background() is used deliberately (not the run's context): every
// terminal call site reaches here AFTER the run context has been canceled
// (canceled/timeout/abandon paths) or is about to be, so passing runCtx would
// make the publish a no-op on exactly the paths that most need it. The bus is
// nil-safe; publishOpTerminal is a no-op when no bus is wired.
//
// This is also the single choke point every terminal-transition call site
// (worker.go's canceled/subprocess/abandoned/normal paths, deps_scheduler.go,
// and Cancel below) already routes through, so it doubles as the cleanup
// point for the OPS-5 per-op progress gauges (metrics.SetOpProgress): without
// deleting them here, audiobook_organizer_op_items_processed/_total would
// accumulate one label-series per historical op_id forever. This runs
// regardless of whether a bus is wired.
func (r *Registry) publishOpTerminal(opID, defID, status string) {
	metrics.ClearOpProgress(opID, defID)

	if r.bus == nil {
		return
	}
	_ = r.bus.Publish(context.Background(), "op.terminal", map[string]any{
		"op_id":  opID,
		"def_id": defID,
		"status": status,
	})
}

// Cancel cancels an operation by id.
// If the op is queued, it is marked canceled in the DB.
// If the op is running, its context is canceled.
// If the op has been claimed by the dispatcher but not yet picked up by a
// worker (a stub handle with nil cancel, the op sitting in the buffered
// nextRun channel), it is flagged so the worker drops it before Run and its
// DB row is marked canceled (C-1 — this case used to be a silent no-op and
// the op ran anyway).
func (r *Registry) Cancel(opID string) error {
	r.mu.Lock()
	h, running := r.running[opID]
	if running && h.cancel == nil {
		// Stub handle: no context to cancel yet. Flag it under the same lock
		// the worker uses when overwriting the stub (see executeRun), so the
		// worker is guaranteed to observe the flag and skip the run.
		h.queuedCancel = true
	}
	r.mu.Unlock()

	if running {
		if h.cancel != nil {
			r.logger.Info("registry: canceling running op", "op_id", opID)
			h.cancelIfActive()
			return nil
		}
		// Stub path: the DB row is still "queued" (the worker only marks it
		// running on pickup) — mark it canceled now so the UI reflects the
		// cancel immediately instead of after worker pickup.
		updated, err := r.store.SetOperationV2StatusIfQueued(opID, "canceled")
		if err != nil {
			return fmt.Errorf("registry: cancel op %s: %w", opID, err)
		}
		r.logger.Info("registry: canceled op awaiting worker pickup", "op_id", opID, "db_updated", updated)
		return nil
	}

	// Try to mark it canceled if it's still queued.
	updated, err := r.store.SetOperationV2StatusIfQueued(opID, "canceled")
	if err != nil {
		return fmt.Errorf("registry: cancel op %s: %w", opID, err)
	}
	if updated {
		r.logger.Info("registry: canceled queued op", "op_id", opID)
		// R-1: a purely-queued op is never picked up by a worker once canceled,
		// so no worker-path op.terminal fires — publish it here or the UI bell
		// leaves the op phantom-"running". def_id is best-effort (the FE only
		// needs op_id); an empty def_id on a lookup miss is harmless.
		defID := ""
		if row, gerr := r.store.GetOperationV2(opID); gerr == nil && row != nil {
			defID = row.DefID
		}
		r.publishOpTerminal(opID, defID, "canceled")
	}
	return nil
}

// hasLiveHandle reports whether opID currently has an in-memory run handle
// (stub or full). Used by EnqueueOp's ConcurrencyKey dedupe to distinguish a
// genuinely running op from a zombie "running" DB row.
func (r *Registry) hasLiveHandle(opID string) bool {
	r.mu.RLock()
	_, ok := r.running[opID]
	r.mu.RUnlock()
	return ok
}

// AbandonedCount returns the current number of abandoned goroutines for a
// plugin. Used by tests and metrics; the dispatcher uses isBlocked internally.
func (r *Registry) AbandonedCount(plugin string) int {
	return r.abandoned.countFor(plugin)
}

// GetCurrentItem returns the last SetCurrentItem label for a running operation.
// Returns empty string if the op is not running or no label has been set.
func (r *Registry) GetCurrentItem(opID string) string {
	r.mu.RLock()
	h, ok := r.running[opID]
	r.mu.RUnlock()
	if !ok {
		return ""
	}
	return h.getCurrentItem()
}

// ActiveDefs returns all registered OperationDefs.
func (r *Registry) ActiveDefs() []OperationDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]OperationDef, 0, len(r.defs))
	for _, d := range r.defs {
		out = append(out, d)
	}
	return out
}

// Def returns the registered OperationDef for the given ID, if any.
func (r *Registry) Def(id string) (OperationDef, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.defs[id]
	return def, ok
}

// Shutdown drains the worker pool. On timeout it marks remaining running ops
// as interrupted per their ResumePolicy and returns.
func (r *Registry) Shutdown(ctx context.Context) error {
	r.logger.Info("registry: shutting down")
	// Deregister from the live-registry tracker on return: once Shutdown has
	// run, ShutdownAllForStore must not re-drain this registry.
	defer untrackLiveRegistry(r)
	// Flip the shutdown flag before canceling handles so the abandoned-run
	// watchdog (in worker.go executeRun) refuses to spawn replacement
	// workers. Without this, a replacement worker is born just as the
	// embedded Pebble store is closing, and its next DB write panics
	// with "pebble: closed".
	r.shuttingDown.Store(true)

	// Stop all batch debounce timers. Subjects remain journaled; the next
	// Start() will reload them. We do NOT dispatch during shutdown because
	// InsertOperationV2 after db.Close() panics (the "pebble: closed" issue).
	r.batchStopAllTimers()

	// Gather running ops.
	r.mu.Lock()
	handles := make([]*runHandle, 0, len(r.running))
	for _, h := range r.running {
		handles = append(handles, h)
	}
	r.mu.Unlock()

	// Cancel all running ops.
	for _, h := range handles {
		h.cancelIfActive()
	}

	// Wait until context expires or all workers drain.
	done := make(chan struct{})
	go func() {
		// Poll until no running ops remain.
		for {
			r.mu.RLock()
			n := len(r.running)
			r.mu.RUnlock()
			if n == 0 {
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
		close(done)
	}()

	var shutdownErr error
	select {
	case <-done:
		r.logger.Info("registry: all workers drained")
	case <-ctx.Done():
		// Mark remaining as interrupted.
		r.mu.Lock()
		for opID, h := range r.running {
			h.abandoned = true
			status := interruptedStatus(h.resumePolicy)
			now := time.Now().UTC()
			_ = r.store.UpdateOperationV2Status(opID, status, nil, &now, nil)
		}
		r.mu.Unlock()
		r.logger.Warn("registry: shutdown timeout; marked remaining ops as interrupted")
		shutdownErr = ctx.Err()
	}

	// Cancel the internal context to stop the dispatcher, watchdog, and any
	// workers that are idle or finishing their current run. Then wait for all
	// goroutines to fully exit before returning — this guarantees callers can
	// safely close the underlying store immediately after Shutdown returns,
	// without racing against goroutines that are still making DB calls.
	if r.cancelFn != nil {
		r.cancelFn()
	}
	// Join the DepsScheduler sweep-ticker goroutine explicitly before returning.
	// The tick goroutine reads the store via SweepTick → ListWaitingDepsOps; the
	// generic goroutineWG.Wait() below abandons stragglers after its 2s escape,
	// which would let the caller close the store while a sweep is mid-iteration
	// (panic "pebble: closed", PEBBLE-CLOSED-SWEEPTICK-RESIDUAL). The gate in the
	// tick loop guarantees no NEW sweep starts once the context is canceled, so
	// this join blocks only on at most one in-flight sweep — a single, finite
	// ListWaitingDepsOps scan plus per-op promotions. The join is UNCONDITIONAL:
	// under heavy parallel-suite load a sweep can outlive both the caller ctx
	// and the 2s escape below, and returning early lets the caller close the
	// store under the still-running sweep. The 2s escape exists for stuck op
	// goroutines, not the sweeper. A warn is logged if the join is unexpectedly
	// slow so a genuinely wedged store read stays visible.
	if r.sweepStopped != nil {
		slowJoin := time.NewTimer(5 * time.Second)
		select {
		case <-r.sweepStopped:
		case <-slowJoin.C:
			r.logger.Warn("registry: still waiting on in-flight deps sweep before shutdown")
			<-r.sweepStopped
		}
		slowJoin.Stop()
	}
	// Reject any further dep-notify enrollment before we wait, so a worker
	// finishing its last op during teardown cannot Add to goroutineWG after the
	// Wait below has started. The op's terminal status is already persisted; the
	// next Start()'s SweepTick re-evaluates any waiting_deps ops for the subject.
	r.mu.Lock()
	r.notifyStopped = true
	r.mu.Unlock()
	goroutinesDone := make(chan struct{})
	go func() {
		r.goroutineWG.Wait()
		close(goroutinesDone)
	}()
	select {
	case <-goroutinesDone:
		r.logger.Info("registry: all goroutines exited")
	case <-time.After(2 * time.Second):
		r.logger.Warn("registry: goroutines did not exit within 2s; proceeding")
	}
	return shutdownErr
}

// writeStrike appends a strike record to op_strikes_v2 and logs it.
func (r *Registry) writeStrike(opID, defID, plugin, kind, message string) {
	details := fmt.Sprintf(`{"plugin":%q,"message":%q}`, plugin, message)
	row := database.OpStrikeV2Row{
		DefID:       defID,
		OperationID: opID,
		Kind:        kind,
		Details:     details,
		OccurredAt:  time.Now().UTC(),
	}
	if err := r.store.InsertOpStrikeV2(row); err != nil {
		r.logger.Warn("registry: failed to write strike", "op_id", opID, "kind", kind, "error", err)
	}
	r.logger.Warn("registry: strike recorded", "op_id", opID, "def_id", defID, "kind", kind, "message", message)
}

// pingDispatch sends a non-blocking signal to the dispatch channel.
func (r *Registry) pingDispatch() {
	select {
	case r.dispatch <- struct{}{}:
	default:
	}
}

// releaseRunHandle removes a handle from the running map and releases
// the concurrency key if held.
func (r *Registry) releaseRunHandle(opID string) {
	r.mu.Lock()
	h, ok := r.running[opID]
	if ok {
		delete(r.running, opID)
		if h.plugin != "" {
			r.pluginRunning[h.plugin]--
			if r.pluginRunning[h.plugin] < 0 {
				r.pluginRunning[h.plugin] = 0
			}
		}
		if h.concurrencyKey != "" {
			if holder, held := r.concurrencyKeys[h.concurrencyKey]; held && holder == opID {
				delete(r.concurrencyKeys, h.concurrencyKey)
			}
		}
	}
	r.mu.Unlock()
	r.pingDispatch()
}

// --- Helpers ---

func resumePolicyName(p ResumePolicy) string {
	switch p {
	case ResumeRestart:
		return "restart"
	case ResumeRequeue:
		return "requeue"
	case ResumeDrop:
		return "drop"
	case ResumeAsk:
		return "ask"
	default:
		return "unspecified"
	}
}

func interruptedStatus(p ResumePolicy) string {
	switch p {
	case ResumeDrop:
		return "interrupted_dropped"
	default:
		return "interrupted_quiesced"
	}
}

func triggersToNames(subs []EventSubscription) []string {
	names := make([]string, len(subs))
	for i, s := range subs {
		names[i] = s.EventName
	}
	return names
}

func phaseNames(phases []Phase) []string {
	names := make([]string, len(phases))
	for i, p := range phases {
		names[i] = p.Name
	}
	return names
}
