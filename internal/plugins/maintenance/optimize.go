// file: internal/plugins/maintenance/optimize.go
// version: 1.7.1
// guid: d4e5f6a7-b8c9-0123-4567-890123456789
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
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
	// Wait blocks until the child ends. relay is HEAD's progress relay, which
	// feeds this op's watchdog from the child's observations; the native wait
	// ignores it (rc.Await relays the child's liveness itself).
	Wait func(ctx context.Context, childID string, relay func(childop.Observation)) error
}

// optimizeParallelism is the op's concurrency: the children run one after the
// other.
func optimizeParallelism() tasks.Parallelism {
	return tasks.Sequential("HEAD declared no concurrency; the sweep runs its child ops one after the other")
}

// optimizeNative is the native kind. The sweep writes nothing itself (its
// children do), so it is declared ReadOnly. HEAD has no preview and a Preview
// run would start live children, so a Preview run is refused.
func (p *Plugin) optimizeNative(_ t6g6Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := t6g6Common(optimizeKind, sched)
	c.Title = "Library optimize sweep"
	c.Help = optimizeHelp
	c.Effects = tasks.ReadOnly()
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityNormal
	c.ConcurrencyKey = tasks.Key("books.maintenance.library-optimize")
	// The kernel refuses a Timeout over tasks.MaxTimeout (24h); HEAD's def keeps
	// its 36h, so the native kind runs under the cap until F5-18 retires the def.
	c.Timeout = tasks.MaxTimeout
	c.ProgressTimeout = optimizeProgressTimeout
	return tasks.Task(optimizeKind, tasks.TaskSpec[struct{}, struct{}]{
		Common: c,
		Run: func(rc *tasks.RC, _ struct{}) (struct{}, error) {
			if err := t6g6LiveOnly(rc, optimizeKind); err != nil {
				return struct{}{}, err
			}
			env := optimizeEnv{
				t6g6Env: t6g6NativeEnv(rc),
				Enqueue: func(_ context.Context, defID string, params any) (string, error) {
					ref, err := rc.Enqueue(t6g6KindFor(defID), params)
					return string(ref.ID), err
				},
				Wait: func(_ context.Context, childID string, _ func(childop.Observation)) error {
					out, err := rc.Await(tasks.RunRef{ID: tasks.RunID(childID)})
					if err != nil {
						return err
					}
					if out.State != tasks.StateSucceeded {
						return fmt.Errorf("child run %s ended %s: %s", childID, out.State, out.Err)
					}
					return nil
				},
			}
			return struct{}{}, optimizeCore(rc.Context(), env)
		},
	})
}

// optimizeCore is the body of runOptimize. env.OpID names the run in the logs.
func optimizeCore(ctx context.Context, env optimizeEnv) error {
	opID := env.OpID
	start := time.Now()

	logger.InfoCtx(ctx, "maintenance.library-optimize: sweep started",
		"operation_id", opID,
	)
	env.Log(slog.LevelInfo, "Library optimize sweep started")

	children := []childOp{
		{
			name:   "temp-file-cleanup",
			defID:  "maintenance.temp-file-cleanup",
			params: nil,
		},
		{
			name:  "fingerprint-rescan-missing",
			defID: "acoustid.fingerprint-rescan",
			params: map[string]any{
				"scope": "missing",
			},
		},
		{
			name:   "acoustid-scan",
			defID:  "acoustid.scan",
			params: nil,
		},
		{
			name:   "acoustid-backfill",
			defID:  "acoustid.backfill",
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
	if !config.AppConfig.Maintenance.AcoustIDBackfill {
		kept := make([]childOp, 0, len(children))
		for _, ch := range children {
			if ch.defID == "acoustid.backfill" {
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
			if ch.defID == "acoustid.fingerprint-rescan" {
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

	prog := t6g6NewProgress(env.t6g6Env, total)
	prog.Start("Starting optimize sweep...")

	for i, ch := range children {
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

		prog.StepN(i, fmt.Sprintf("Running child op %d/%d: %s", i+1, total, ch.name))

		childStart := time.Now()
		logger.InfoCtx(ctx, "maintenance.library-optimize: child started",
			"operation_id", opID,
			"child", ch.name,
			"def_id", ch.defID,
			"child_index", i+1,
			"child_total", total,
		)
		env.Log(slog.LevelInfo, fmt.Sprintf("Starting child %d/%d: %s", i+1, total, ch.name))

		childID, err := env.Enqueue(ctx, ch.defID, ch.params)
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
			continue
		}

		logger.InfoCtx(ctx, "maintenance.library-optimize: child enqueued",
			"operation_id", opID,
			"child", ch.name,
			"child_id", childID,
		)

		// Wait for the child to reach a terminal state, relaying its progress so
		// this op's watchdog sees the child working.
		if waitErr := env.Wait(ctx, childID, relayChildProgress(prog, i, total, ch.name)); waitErr != nil {
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
	}

	totalElapsed := time.Since(start)
	logger.InfoCtx(ctx, "maintenance.library-optimize: sweep complete",
		"operation_id", opID,
		"children_completed", completed,
		"children_failed", failed,
		"children_total", total,
		"elapsed_ms", totalElapsed.Milliseconds(),
	)
	prog.Done(fmt.Sprintf("Optimize sweep complete in %s — %d/%d children succeeded",
		totalElapsed.Round(time.Second), completed, total))
	env.Log(slog.LevelInfo,
		fmt.Sprintf("Library optimize sweep complete: %d/%d children succeeded, %d failed (elapsed: %s)",
			completed, total, failed, totalElapsed.Round(time.Second)))

	return nil
}
