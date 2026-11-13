// file: internal/plugins/maintenance/recompact_activity_digests.go
// version: 1.1.0
// guid: 8b4f2e61-3d7a-4c95-a0e8-5f1c9d6b2a73
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// RecompactActivityDigestsDefID is the def id of the admin-triggered digest
// re-derivation. Start it with POST /api/v1/operations/v2 and
// {"def_id":"maintenance.recompact-activity-digests"}. (POST
// /api/v1/admin/recompact-digests, which used to enqueue it, answers 410 Gone
// since 01-P81g.)
const RecompactActivityDigestsDefID = "maintenance.recompact-activity-digests"

// RecompactActivityDigestsResult is the op's persisted result: how many
// digests were rewritten and how many already had nothing legacy in them.
type RecompactActivityDigestsResult struct {
	Touched int `json:"touched"`
	Skipped int `json:"skipped"`
}

// recompactActivityDigestsHelp is the def's Description, which the native kind
// carries as its Help.
const recompactActivityDigestsHelp = "Re-derives type, tier and tags on daily digests that were compacted before enrichment existed."

// recompactActivityDigestsDef is the admin "recompact digests" action.
//
// WHY AN OP AND NOT A HANDLER. Until 2026-09-10 POST /admin/recompact-digests
// (now retired) re-derived every digest inside the HTTP request — the same shape the
// Activity page's Compact button had until PR #3214, and the same failure:
// on a store with years of digests across two backends the request outlives
// any browser timeout and the caller cannot tell whether the work is still
// going. As an op the request answers in milliseconds with an op id and the
// work is visible in the operations list.
//
// LIVENESS. LivenessNone with an explicit budget, not LivenessManual. The
// store's RecompactDigests is one blocking call with no progress
// callback of its own (it checks ctx between digests but reports nothing), so
// there is nothing to forward to UpdateProgress mid-run. Declaring
// LivenessManual would invite the five-minute never_reported strike that
// killed the nightly cleanup until #3214; declaring the truth — coarse
// granularity, here is the budget — is the accurate choice. Digests are one row per day per source, so the work is
// bounded by days of history, not by row volume; 1h is a ceiling, not an
// estimate.
//
// CONCURRENCY. Shares cleanup-activity-log's key so it never rewrites a digest
// that compaction is building at the same moment.
func (p *Plugin) recompactActivityDigestsDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              RecompactActivityDigestsDefID,
		Liveness:        sdk.LivenessNone,
		ProgressTimeout: 60 * time.Minute,
		Plugin:          "maintenance",
		DisplayName:     "Recompact activity digests",
		Description:     recompactActivityDigestsHelp,
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.cleanup-activity-log",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         60 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runRecompactActivityDigests,
	}
}

func (p *Plugin) runRecompactActivityDigests(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	env := recompactActivityDigestsEnv{
		t6g6Env: t6g6HeadEnv(reporter, true),
		Writer:  t6g6HeadWriter{deps: p.deps},
	}
	_, err := recompactActivityDigestsCore(ctx, env)
	return err
}

// recompactActivityDigestsKind is rule R3's kind for the def above.
const recompactActivityDigestsKind = tasks.Kind("books." + RecompactActivityDigestsDefID)

// recompactActivityDigestsWriter is the one write the op makes: the digest
// re-derivation.
type recompactActivityDigestsWriter interface {
	RecompactActivityDigests(ctx context.Context) (database.RecompactResult, error)
}

// recompactActivityDigestsEnv is what the core takes from its shell.
type recompactActivityDigestsEnv struct {
	t6g6Env
	Writer recompactActivityDigestsWriter
}

// recompactActivityDigestsParallelism is the op's concurrency. HEAD declared none
// and the store's re-derivation is one blocking call.
func recompactActivityDigestsParallelism() tasks.Parallelism {
	return tasks.Sequential("HEAD declared no concurrency; the store's re-derivation is one blocking call")
}

// recompactActivityDigestsNative is the native kind. It rewrites stored digests,
// so it has no preview and refuses a Preview run.
func (p *Plugin) recompactActivityDigestsNative(deps t6g6Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := t6g6Common(recompactActivityDigestsKind, sched)
	c.Title = "Recompact activity digests"
	c.Help = recompactActivityDigestsHelp
	c.Effects = tasks.Writes(tasks.Res("books", "operations"))
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books.maintenance.cleanup-activity-log")
	c.Timeout = 60 * time.Minute
	c.ProgressTimeout = 60 * time.Minute
	return tasks.Task(recompactActivityDigestsKind, tasks.TaskSpec[struct{}, RecompactActivityDigestsResult]{
		Common: c,
		Run: func(rc *tasks.RC, _ struct{}) (RecompactActivityDigestsResult, error) {
			if err := t6g6LiveOnly(rc, recompactActivityDigestsKind); err != nil {
				return RecompactActivityDigestsResult{}, err
			}
			env := recompactActivityDigestsEnv{
				t6g6Env: t6g6NativeEnv(rc),
				Writer:  deps.NewWriter(rc.Writer()),
			}
			return recompactActivityDigestsCore(rc.Context(), env)
		},
	})
}

// recompactActivityDigestsCore is the body of runRecompactActivityDigests. The
// result comes back even when the re-derivation failed (the counters are what
// was committed before it), and env.SetResult, when set, persists it.
func recompactActivityDigestsCore(ctx context.Context, env recompactActivityDigestsEnv) (RecompactActivityDigestsResult, error) {
	startMsg := "Re-deriving legacy daily-digest items"
	logger.InfoCtx(ctx, startMsg)
	env.Log(slog.LevelInfo, startMsg)
	env.Progress(0, 0, startMsg)

	res, err := env.Writer.RecompactActivityDigests(ctx)
	result := RecompactActivityDigestsResult{Touched: res.Touched, Skipped: res.Skipped}

	// The counters are reported whatever happened: on a failure they are what
	// was committed before it, which is the number an operator needs to decide
	// whether a retry is worth it.
	doneMsg := fmt.Sprintf("Recompacted %d digest(s), %d already current", result.Touched, result.Skipped)
	if err != nil {
		doneMsg = fmt.Sprintf("FAILED after recompacting %d digest(s) (%d already current): %v", result.Touched, result.Skipped, err)
		env.Log(slog.LevelError, doneMsg)
	} else {
		env.Log(slog.LevelInfo, doneMsg)
	}
	logger.InfoCtx(ctx, doneMsg)
	env.Progress(result.Touched+result.Skipped, result.Touched+result.Skipped, doneMsg)

	if env.SetResult != nil {
		if setErr := env.SetResult(result); setErr != nil {
			if err != nil {
				return result, fmt.Errorf("recompact-activity-digests: %w (and persisting the result failed: %v)", err, setErr)
			}
			return result, fmt.Errorf("recompact-activity-digests: persist result: %w", setErr)
		}
	}
	if err != nil {
		return result, fmt.Errorf("recompact-activity-digests: %w", err)
	}
	return result, nil
}
