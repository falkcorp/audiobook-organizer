// file: internal/plugins/maintenance/optimize.go
// version: 1.10.6
// guid: d4e5f6a7-b8c9-0123-4567-890123456789
// last-edited: 2026-11-15

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations/childop"
	"github.com/falkcorp/audiobook-organizer/internal/serverdecode"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// optimizeHelp is the def's Description, which the native kind carries as its Help.
const optimizeHelp = "Chains cleanup-stale → fingerprint-rescan(missing) → dedup-acoustid-scan → backfill into one user-triggered maintenance pass."

// optimizeDef returns the OperationDef for maintenance.library-optimize
// (registered as library.optimize until 2026-09-25).
func (p *Plugin) optimizeDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID: "maintenance.library-optimize",
		// Renamed 2026-09-25 (naming audit class 8): a sweep of maintenance ops is a maintenance op.
		FormerIDs:       []string{"library.optimize"},
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Library optimize sweep",
		Description:     optimizeHelp,
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "maintenance.library-optimize",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         36 * time.Hour,
		// The sweep reports progress only by relaying what its running child
		// writes (see relayChildProgress), so its own watchdog budget must be at
		// least the longest silence a child is allowed. maintenance.temp-file-
		// cleanup is LivenessNone and reports nothing while it walks, so its
		// whole budget can be silence; optimizeProgressTimeout is derived from
		// it so the two cannot drift. The acoustid children report on the
		// 5-minute default. A hung child is still caught by its own watchdog
		// first.
		ProgressTimeout: optimizeProgressTimeout,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
			sdk.CapFilesRead,
			sdk.CapFilesWrite,
			sdk.CapFilesExecute,
			sdk.CapSubprocessSpawn,
		},
		Run: p.runOptimize,
	}
}

// optimizeProgressTimeout is maintenance.library-optimize's watchdog budget:
// the longest silent child budget plus a poll interval. It was a flat 25m
// while temp-file-cleanup's budget was 20m; that budget became 1h on
// 2026-10-10, when the scheduled scheduler.temp-file-cleanup (sized at 1h) was
// merged into it.
const optimizeProgressTimeout = tempFileCleanupProgressTimeout + 5*time.Minute

// relayChildProgress returns the WaitForOp callback that feeds this op's
// watchdog from what it observes of child i. Before 2026-09-28 the sweep
// reported nothing between enqueueing a child and the child finishing, so any
// child running longer than the 5-minute default ProgressTimeout (a fingerprint
// rescan or acoustid.scan routinely does) got the SWEEP reaped while the child
// was healthy -- the same bug class as dedup.drain-stale (#3600).
func relayChildProgress(prog *sdk.Progress, i, total int, name string) func(childop.Observation) {
	head := fmt.Sprintf("Running child op %d/%d: %s", i+1, total, name)
	return func(o childop.Observation) {
		switch o.Event {
		case childop.EventQueued:
			prog.StepN(i, head+" — waiting to start (queued behind other work)")
		case childop.EventPaused:
			prog.StepN(i, head+" — paused by an operator")
		default:
			msg := head
			if o.Row.ProgressMessage != "" {
				msg += " — " + o.Row.ProgressMessage
			}
			if o.Row.ProgressTotal > 0 {
				msg += fmt.Sprintf(" [child %d%%]", childop.Percent(o.Row))
			}
			prog.StepN(i, msg)
		}
	}
}

// childOp describes a single child operation in the optimize sweep.
type childOp struct {
	name   string
	defID  string
	params any
}

func (p *Plugin) runOptimize(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	env := optimizeEnv{
		t6g6Env: t6g6HeadEnv(reporter, true),
		Enqueue: p.deps.EnqueueOp,
		Wait:    p.deps.WaitForOp,
	}
	env.OpID = ctxOpID(ctx)
	return optimizeCore(ctx, env)
}

// optimizeKind is rule R3's kind for the def above.
const optimizeKind = tasks.Kind("books.maintenance.library-optimize")

// optimizeEnv is what the core takes from its shell. The sweep reads and writes
// no store itself: its children do, so it needs only a way to start one and wait
// for it.
type optimizeEnv struct {
	t6g6Env
	// Enqueue starts a child op by HEAD def ID and returns its run id.
	Enqueue func(ctx context.Context, defID string, params any) (string, error)
	// Deadline is this attempt's deadline (rc.Deadline); nil in a HEAD shell,
	// which never yields. Together with the env's Checkpoint and Restore it lets a
	// native sweep end the attempt between children and resume where it stopped.
	Deadline func() time.Time
	// Wait blocks until the child ends. relay is HEAD's progress relay, which
	// feeds this op's watchdog from the child's observations; the native wait
	// ignores it: the kernel's await keeps the waiting run alive while its child
	// is (core/internal/executor dispatch.go await).
	Wait func(ctx context.Context, childID string, relay func(childop.Observation)) error
	// Clock is the sweep's source of time for its 36h budget: the boundary check
	// at each child, and the end of a wait. The zero value is the wall clock; a
	// test swaps in a clock it advances by hand (optimizeSweepClock), so a budget
	// that ends "two seconds into a wait" is an event, not a race.
	Clock optimizeClock
}

// optimizeClock is the time the sweep's budget is measured on. Both members are
// optional; nil is the wall clock (time.Now, context.WithDeadline).
type optimizeClock struct {
	// Now is the current time.
	Now func() time.Time
	// WithDeadline is context.WithDeadline on this clock's time: the returned
	// context ends with context.DeadlineExceeded (its Err and its cause) when the
	// clock reaches t.
	WithDeadline func(ctx context.Context, t time.Time) (context.Context, context.CancelFunc)
}

func (c optimizeClock) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c optimizeClock) withDeadline(ctx context.Context, t time.Time) (context.Context, context.CancelFunc) {
	if c.WithDeadline != nil {
		return c.WithDeadline(ctx, t)
	}
	return context.WithDeadline(ctx, t)
}

// optimizeSweepClock is the clock the native shell hands the core. Nil members
// are the wall clock; tests swap it.
var optimizeSweepClock optimizeClock

// The HEAD def IDs of the four children the sweep starts. The native sweep starts
// each by t6g6KindFor(id) ("books." + id), so the kind a sibling row declares for
// its def must be exactly that. The IDs are the literals the owning defs carry
// (internal/plugins/maintenance cleanup.go and internal/plugins/acoustid
// fingerprint_rescan.go, scan.go, backfill.go).
const (
	optimizeChildTempFileCleanup   = "maintenance.temp-file-cleanup"
	optimizeChildFingerprintRescan = "acoustid.fingerprint-rescan"
	optimizeChildAcoustIDScan      = "acoustid.scan"
	optimizeChildAcoustIDBackfill  = "acoustid.backfill"
)

// optimizeTotalBudget is HEAD's Timeout for the whole sweep. The native kind's
// attempt is capped at tasks.MaxTimeout (24h), so a sweep that outlives one
// attempt yields between children and the resumed attempts share this budget.
const optimizeTotalBudget = 36 * time.Hour

// optimizeYieldMargin is how close to the attempt's deadline the sweep stops
// waiting on the running child (or starting a new one) and yields instead (the
// kernel's own YieldMargin).
const optimizeYieldMargin = 5 * time.Minute

// optimizeInFlight names the child a yielding (or crashing) attempt had started
// and not seen end: the resumed attempt re-attaches to this run instead of
// launching the child a second time.
type optimizeInFlight struct {
	Name  string `json:"name"`
	RunID string `json:"run_id"`
}

// optimizeCheckpoint is what the sweep saves after every child boundary and at a
// yield: when the sweep began (the 36h budget runs from it, across attempts),
// the children already finished, by name (the child list depends on config flags
// that can change between attempts, so an index would not be stable), and the
// child in flight, if any.
type optimizeCheckpoint struct {
	StartedAt time.Time         `json:"started_at"`
	Finished  []string          `json:"finished"`
	Completed int               `json:"completed"`
	Failed    int               `json:"failed"`
	InFlight  *optimizeInFlight `json:"in_flight,omitempty"`
	// Steps is the native progress counter (see optimizeCore): it only ever grows,
	// so an attempt that re-attaches to a child and yields again still reports
	// progress past where the one before stopped.
	Steps int `json:"steps,omitempty"`
}

// optimizeNative is the native kind. The sweep writes nothing itself (its
// children do), so it is declared ReadOnly. HEAD has no preview and a Preview
// run would start live children, so a Preview run is refused.
//
// Resume is ResumeContinue, a deviation from rule R5's mapping of HEAD's
// ResumeDrop, and unlike opchange the checkpoint IS read on restart (Restore, in
// optimizeCore). The executor only honours ErrContinueInPlace for a
// ResumeContinue kind, and the kind needs it to keep HEAD's 36h total under the
// kernel's 24h attempt cap. The sweep checkpoints after the child it enqueues
// and after every child that ends, so a crash resumes at the next child (or
// re-attaches to the child in flight) rather than re-running the sweep. HEAD
// dropped an interrupted sweep; a restart now resumes it, a behaviour change
// recorded in seams.d/F5-11.md.
func (p *Plugin) optimizeNative(_ t6g6Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := t6g6Common(optimizeKind, sched)
	c.Title = "Library optimize sweep"
	c.Help = optimizeHelp
	c.Effects = tasks.ReadOnly()
	c.Resume = tasks.ResumeContinue
	c.Priority = tasks.PriorityNormal
	c.ConcurrencyKey = tasks.Key("books.maintenance.library-optimize")
	// The kernel refuses a Timeout over tasks.MaxTimeout (24h). HEAD's 36h is kept
	// as optimizeTotalBudget across attempts: the sweep yields between children
	// before the attempt's deadline (optimizeYieldMargin), even while a child is
	// still running, and resumes.
	c.Timeout = tasks.MaxTimeout
	c.ProgressTimeout = optimizeProgressTimeout
	return tasks.Task(optimizeKind, tasks.TaskSpec[struct{}, struct{}]{
		Common: c,
		Run: func(rc *tasks.RC, _ struct{}) (struct{}, error) {
			if err := t6g6LiveOnly(rc, optimizeKind); err != nil {
				return struct{}{}, err
			}
			env := optimizeEnv{
				t6g6Env:  t6g6NativeEnv(rc),
				Deadline: rc.Deadline,
				Clock:    optimizeSweepClock,
				Enqueue: func(_ context.Context, defID string, params any) (string, error) {
					ref, err := rc.Enqueue(t6g6KindFor(defID), params)
					return string(ref.ID), err
				},
				// rc.Await takes no context, so the wait runs in its own goroutine
				// and ctx (the sweep's deadline minus the yield margin) ends the
				// wait without ending the child. The goroutine returns when the
				// attempt's own context ends, which a yield does. The wait stamps
				// no liveness itself: the kernel's await relays it to this run
				// while the child is alive (and, deliberately, not while it waits
				// for a restart or a person), core/internal/executor dispatch.go
				// await, pinned by core's
				// TestAwait_ParentSurvivesChildLongerThanItsWatchdogLimit and
				// TestAwait_ChildNeedingAPersonOrARestartDoesNotKeepParentAlive. A
				// tick of the row's own would keep the sweep alive while its child
				// waits for a person or a restart, overriding that rule.
				Wait: func(ctx context.Context, childID string, _ func(childop.Observation)) error {
					type awaited struct {
						out tasks.Outcome
						err error
					}
					done := make(chan awaited, 1)
					go func() {
						// A panic here is outside the run body's recover and
						// would end the process: report it as the wait's error.
						defer func() {
							if r := recover(); r != nil {
								done <- awaited{err: fmt.Errorf("await child run %s panicked: %v", childID, r)}
							}
						}()
						out, err := rc.Await(tasks.RunRef{ID: tasks.RunID(childID)})
						done <- awaited{out, err}
					}()
					select {
					case r := <-done:
						if r.err != nil {
							return r.err
						}
						if r.out.State != tasks.StateSucceeded {
							return fmt.Errorf("child run %s ended %s: %s", childID, r.out.State, r.out.Err)
						}
						return nil
					case <-ctx.Done():
						return context.Cause(ctx)
					}
				},
			}
			return struct{}{}, optimizeCore(rc.Context(), env)
		},
	})
}

// optimizeCore is the body of runOptimize. env.OpID names the run in the logs.
func optimizeCore(ctx context.Context, env optimizeEnv) error {
	opID := env.OpID
	start := env.Clock.now()

	logger.InfoCtx(ctx, "maintenance.library-optimize: sweep started",
		"operation_id", opID,
	)
	env.Log(slog.LevelInfo, "Library optimize sweep started")

	children := []childOp{
		{
			name:   "temp-file-cleanup",
			defID:  optimizeChildTempFileCleanup,
			params: nil,
		},
		{
			name:  "fingerprint-rescan-missing",
			defID: optimizeChildFingerprintRescan,
			params: map[string]any{
				"scope": "missing",
			},
		},
		{
			name:   "acoustid-scan",
			defID:  optimizeChildAcoustIDScan,
			params: nil,
		},
		{
			name:   "acoustid-backfill",
			defID:  optimizeChildAcoustIDBackfill,
			params: nil,
		},
	}

	// maintenance.acoustid_backfill (default OFF as of 2026-08-11): the backfill's load
	// phase pulls the whole book table into memory before it can start — ~862 MB of live
	// heap in production, implicated in three OOM kills in one night. optimize is a broad
	// "tidy everything" sweep rather than a request for this specific op, so it is an
	// automatic trigger for the purposes of the flag and respects it. A direct
	// EnqueueOp("acoustid.backfill") via the ops API stays ungated — that is the
	// deliberate opt-in path. Filtering here rather than skipping mid-loop keeps `total`
	// accurate, so progress does not report a child it never intended to run.
	if !config.Snapshot().Maintenance.AcoustIDBackfill {
		kept := make([]childOp, 0, len(children))
		for _, ch := range children {
			if ch.defID == optimizeChildAcoustIDBackfill {
				logger.InfoCtx(ctx, "maintenance.library-optimize: acoustid-backfill excluded",
					"operation_id", opID,
					"reason", "maintenance.acoustid_backfill=false",
				)
				env.Log(slog.LevelInfo,
					"Skipping acoustid-backfill: disabled by maintenance.acoustid_backfill")
				continue
			}
			kept = append(kept, ch)
		}
		children = kept
	}

	// fingerprint-rescan decodes audio in-process, which is refused unless the host
	// opted in (ALLOW_SERVER_DECODE). Drop the child up front so the sweep does not
	// report one failed child on every run.
	if !serverdecode.Allowed() {
		kept := make([]childOp, 0, len(children))
		for _, ch := range children {
			if ch.defID == optimizeChildFingerprintRescan {
				logger.InfoCtx(ctx, "maintenance.library-optimize: fingerprint-rescan-missing excluded",
					"operation_id", opID,
					"reason", serverdecode.EnvVar+" is not true",
				)
				env.Log(slog.LevelInfo,
					"Skipping fingerprint-rescan-missing: in-process audio decoding is not allowed on this server")
				continue
			}
			kept = append(kept, ch)
		}
		children = kept
	}

	total := len(children)
	completed := 0
	failed := 0
	finished := map[string]bool{}
	var finishedOrder []string

	// A resumed native attempt carries on from the children the earlier attempt
	// finished, under the one 36h budget measured from the sweep's first start,
	// and re-attaches to the child it left in flight.
	var inFlight *optimizeInFlight
	steps := 0
	if env.Restore != nil {
		var cp optimizeCheckpoint
		ok, err := env.Restore(&cp)
		if err != nil {
			return fmt.Errorf("restore optimize checkpoint: %w", err)
		}
		if ok {
			if cp.StartedAt.IsZero() {
				// A zero start would silently restart the 36h budget.
				return fmt.Errorf("restore optimize checkpoint: no start time recorded")
			}
			start = cp.StartedAt
			completed, failed = cp.Completed, cp.Failed
			for _, name := range cp.Finished {
				finished[name] = true
			}
			finishedOrder = append(finishedOrder, cp.Finished...)
			inFlight = cp.InFlight
			steps = cp.Steps
			// The child that was in flight is still running (or re-queued by the
			// restart), so it is always waited on first, whether or not today's
			// list still holds it: a flag change between attempts can drop it
			// (ALLOW_SERVER_DECODE turned off) or put another child ahead of it
			// (turned on), and either way no later child may start beside it.
			// Re-attaching never enqueues, so it needs no def.
			if inFlight != nil && !finished[inFlight.Name] {
				kept := []childOp{}
				head := childOp{name: inFlight.Name}
				listed := false
				for _, ch := range children {
					if ch.name == inFlight.Name {
						head, listed = ch, true
						continue
					}
					kept = append(kept, ch)
				}
				children = append([]childOp{head}, kept...)
				total = len(children)
				if !listed {
					env.Log(slog.LevelInfo, fmt.Sprintf("Optimize sweep: child %s is no longer in the list; waiting on its run %s first", inFlight.Name, inFlight.RunID))
				}
			}
			env.Log(slog.LevelInfo, fmt.Sprintf("Resuming optimize sweep: %d/%d children already finished", len(cp.Finished), max(total, len(cp.Finished))))
		}
	}
	native := env.Deadline != nil
	if steps < 2*len(finishedOrder) {
		steps = 2 * len(finishedOrder) // a checkpoint from before Steps existed
	}
	finishedThisAttempt := 0
	budgetErr := func() error {
		return fmt.Errorf("optimize sweep exceeded its %s budget across attempts", optimizeTotalBudget)
	}
	// save records the sweep's position (native only: HEAD has no Checkpoint).
	save := func(fl *optimizeInFlight) error {
		if env.Checkpoint == nil {
			return nil
		}
		if err := env.Checkpoint(optimizeCheckpoint{StartedAt: start, Finished: finishedOrder, Completed: completed, Failed: failed, InFlight: fl, Steps: steps}); err != nil {
			return fmt.Errorf("checkpoint optimize sweep: %w", err)
		}
		return nil
	}

	prog := t6g6NewProgress(env.t6g6Env, total)
	// Native progress is a count of sweep steps: two per child (it started, it
	// ended), plus one for every attempt that re-attaches to a child still in
	// flight, and it is persisted in the checkpoint. The kernel fails a yield
	// whose attempt reported no progress past its starting point, and a
	// re-attached child that is still running at the next deadline has finished
	// nothing, so the re-attach itself must count. HEAD keeps its per-child
	// progress.
	report := func(msg string, advance int) {
		steps += advance
		t := 2 * total
		if steps > t {
			t = steps
		}
		env.Progress(steps, t, msg)
	}
	if native {
		report("Starting optimize sweep...", 0)
	} else {
		prog.Start("Starting optimize sweep...")
	}

	for i, ch := range children {
		if finished[ch.name] {
			msg := fmt.Sprintf("Child %d/%d: %s already finished", i+1, total, ch.name)
			if native {
				report(msg, 0)
			} else {
				prog.StepN(i+1, msg)
			}
			continue
		}
		// HEAD's 36h total is checked at every child boundary: no child starts
		// past it.
		if env.Clock.now().Sub(start) >= optimizeTotalBudget {
			return budgetErr()
		}
		reattach := inFlight != nil && inFlight.Name == ch.name
		// Yield before starting a child when the attempt is about to run out. At
		// least one child must have finished in this attempt, or the resumed
		// attempt would make no progress and yield again at once. A child that
		// is already running is handled at its wait, below.
		if !reattach && env.Deadline != nil && env.Checkpoint != nil && finishedThisAttempt > 0 {
			if dl := env.Deadline(); !dl.IsZero() && dl.Sub(env.Clock.now()) < optimizeYieldMargin {
				if err := save(nil); err != nil {
					return err
				}
				env.Log(slog.LevelInfo, fmt.Sprintf("Optimize sweep yielding before child %d/%d: attempt deadline near", i+1, total))
				return tasks.ErrContinueInPlace
			}
		}
		if env.Canceled() {
			logger.InfoCtx(ctx, "maintenance.library-optimize: sweep canceled",
				"operation_id", opID,
				"completed", completed,
				"failed", failed,
			)
			env.Log(slog.LevelInfo,
				fmt.Sprintf("Optimize sweep canceled after %d/%d children", i, total))
			return fmt.Errorf("canceled")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if msg := fmt.Sprintf("Running child op %d/%d: %s", i+1, total, ch.name); native {
			report(msg, 1)
		} else {
			prog.StepN(i, msg)
		}

		childStart := time.Now()
		logger.InfoCtx(ctx, "maintenance.library-optimize: child started",
			"operation_id", opID,
			"child", ch.name,
			"def_id", ch.defID,
			"child_index", i+1,
			"child_total", total,
		)
		env.Log(slog.LevelInfo, fmt.Sprintf("Starting child %d/%d: %s", i+1, total, ch.name))

		var childID string
		if reattach {
			// The earlier attempt started this child and did not see it end:
			// wait on that run (a child that finished in between answers at
			// once) and never launch it twice.
			childID = inFlight.RunID
			inFlight = nil
			env.Log(slog.LevelInfo, fmt.Sprintf("Re-attaching to child %d/%d: %s (run %s)", i+1, total, ch.name, childID))
			// Checkpoint at once: a re-attach attempt that waits out its whole
			// span would otherwise be struck as uncheckpointed.
			if err := save(&optimizeInFlight{Name: ch.name, RunID: childID}); err != nil {
				return err
			}
		} else {
			var err error
			childID, err = env.Enqueue(ctx, ch.defID, ch.params)
			if err != nil {
				elapsed := time.Since(childStart)
				logger.WarnCtx(ctx, "maintenance.library-optimize: child enqueue failed",
					"operation_id", opID,
					"child", ch.name,
					"def_id", ch.defID,
					"elapsed_ms", elapsed.Milliseconds(),
					"error", err,
				)
				env.Log(slog.LevelWarn,
					fmt.Sprintf("Child %s enqueue failed (skipping): %v", ch.name, err))
				failed++
				finished[ch.name] = true
				finishedOrder = append(finishedOrder, ch.name)
				finishedThisAttempt++
				if native {
					report(fmt.Sprintf("Child %d/%d: %s could not be started", i+1, total, ch.name), 1)
				}
				if err := save(nil); err != nil {
					return err
				}
				continue
			}
			// Record the run id before waiting: a crash from here re-attaches.
			if err := save(&optimizeInFlight{Name: ch.name, RunID: childID}); err != nil {
				return err
			}
		}

		logger.InfoCtx(ctx, "maintenance.library-optimize: child enqueued",
			"operation_id", opID,
			"child", ch.name,
			"child_id", childID,
		)

		// Wait for the child to reach a terminal state, relaying its progress so
		// this op's watchdog sees the child working.
		// A native wait ends at the earlier of the attempt's deadline minus the
		// yield margin and the end of the 36h budget, so a child still running
		// then is yielded on instead of timing the whole attempt out.
		waitCtx, cancelWait := ctx, context.CancelFunc(func() {})
		var budgetAt time.Time
		if env.Deadline != nil && env.Checkpoint != nil {
			budgetAt = start.Add(optimizeTotalBudget)
			until := budgetAt
			if dl := env.Deadline(); !dl.IsZero() && dl.Add(-optimizeYieldMargin).Before(until) {
				until = dl.Add(-optimizeYieldMargin)
			}
			waitCtx, cancelWait = env.Clock.withDeadline(ctx, until)
		}
		waitErr := env.Wait(waitCtx, childID, relayChildProgress(prog, i, total, ch.name))
		waitExpired := waitErr != nil && ctx.Err() == nil &&
			errors.Is(waitCtx.Err(), context.DeadlineExceeded) && errors.Is(waitErr, context.DeadlineExceeded)
		cancelWait()
		if waitErr != nil && env.Deadline != nil && ctx.Err() != nil {
			// The attempt itself is ending (cancel or timeout), not the child
			// failing: the child stays in flight in the last checkpoint.
			return context.Cause(ctx)
		}
		if waitExpired {
			if !env.Clock.now().Before(budgetAt) {
				return budgetErr()
			}
			if err := save(&optimizeInFlight{Name: ch.name, RunID: childID}); err != nil {
				return err
			}
			env.Log(slog.LevelInfo, fmt.Sprintf("Optimize sweep yielding with child %d/%d (%s) still running: attempt deadline near", i+1, total, ch.name))
			return tasks.ErrContinueInPlace
		}
		if waitErr != nil {
			elapsed := time.Since(childStart)
			logger.WarnCtx(ctx, "maintenance.library-optimize: child failed or timed out",
				"operation_id", opID,
				"child", ch.name,
				"child_id", childID,
				"elapsed_ms", elapsed.Milliseconds(),
				"error", waitErr,
			)
			env.Log(slog.LevelWarn,
				fmt.Sprintf("Child %s failed (continuing): %v", ch.name, waitErr))
			failed++
			finished[ch.name] = true
			finishedOrder = append(finishedOrder, ch.name)
			finishedThisAttempt++
			if native { // native only: HEAD reports nothing after a child ends
				report(fmt.Sprintf("Child %d/%d: %s failed", i+1, total, ch.name), 1)
			}
			if err := save(nil); err != nil {
				return err
			}
			continue
		}

		elapsed := time.Since(childStart)
		logger.InfoCtx(ctx, "maintenance.library-optimize: child completed",
			"operation_id", opID,
			"child", ch.name,
			"child_id", childID,
			"elapsed_ms", elapsed.Milliseconds(),
		)
		env.Log(slog.LevelInfo,
			fmt.Sprintf("Child %s completed in %s", ch.name, elapsed.Round(time.Second)))
		completed++
		finished[ch.name] = true
		finishedOrder = append(finishedOrder, ch.name)
		finishedThisAttempt++
		if native { // native only: HEAD reports nothing after a child ends
			report(fmt.Sprintf("Child %d/%d: %s done", i+1, total, ch.name), 1)
		}
		if err := save(nil); err != nil {
			return err
		}
	}

	totalElapsed := time.Since(start)
	logger.InfoCtx(ctx, "maintenance.library-optimize: sweep complete",
		"operation_id", opID,
		"children_completed", completed,
		"children_failed", failed,
		"children_total", max(total, completed+failed),
		"elapsed_ms", totalElapsed.Milliseconds(),
	)
	doneMsg := fmt.Sprintf("Optimize sweep complete in %s — %d/%d children succeeded",
		totalElapsed.Round(time.Second), completed, max(total, completed+failed))
	if native {
		report(doneMsg, max(0, 2*total-steps))
	} else {
		prog.Done(doneMsg)
	}
	env.Log(slog.LevelInfo,
		fmt.Sprintf("Library optimize sweep complete: %d/%d children succeeded, %d failed (elapsed: %s)",
			completed, max(total, completed+failed), failed, totalElapsed.Round(time.Second)))

	return nil
}
