// file: internal/plugins/maintenance/repair_merged_user_state.go
// version: 1.1.0
// guid: 8d4b2f67-1a9e-4c35-b7d0-9e6f3a2c1b58
// last-edited: 2026-11-13

// Moves user state stranded under merged-away books onto their survivors.
//
// WHY THIS EXISTS. When books are merged, every piece of a listener's state --
// position, finished flag, progress %, last played, the hide-from-continue-
// listening flag, bookmarks -- must end up on the surviving book (owner
// requirement 2026-09-26). Until then the merge follow was best-effort: a
// failure left the rows under the merged-away id, GET progress on the
// survivor answered 404, and nothing brought them back. The follow now leaves
// a durable pending-repair record on failure (internal/merge/pending_repair.go);
// this op completes those records and also finds rows stranded before the fix.
//
// maintenance.repair-merged-user-state is PREVIEW BY DEFAULT ({} writes
// nothing). It completes the pending records, scans every user's ubs/upos
// rows and bookmarks, resolves each dead book to its live survivor (sync
// redirect chain, then merged_into_book_id) and, with apply=true, moves the
// state under the merge conflict rule. There is deliberately no scheduled
// variant: a scheduled op always runs with {}, which must be a preview (owner
// rule 2026-09-25, testdata/write_op_modes.golden), so pending records are
// completed automatically by a server ticker (merge.PendingRepairLoop, started
// in server_lifecycle.go) and at the start of every merge of the same books.
//
// Trigger: POST /api/v1/operations/v2 with
//
//	{"def_id":"maintenance.repair-merged-user-state","params":{}}              preview
//	{"def_id":"maintenance.repair-merged-user-state","params":{"apply":true}}  move
package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

type repairMergedUserStateParams struct {
	// Apply must be explicitly true to move anything. Default false = preview.
	Apply bool `json:"apply"`
	// PendingOnly limits the run to the pending-repair records.
	PendingOnly bool `json:"pending_only,omitempty"`
}

// repairMergedUserStateHelp is the def's Description, which the native kind
// carries as its Help.
const repairMergedUserStateHelp = "Finds listening state (position, finished, progress, last played, hide flag, bookmarks) still stored under merged-away or deleted books whose surviving book is known, and moves it onto the survivor with the merge conflict rule (finished sticky, newest position wins, last played = max, hide kept). Also completes the pending-repair records a failed merge left. Preview by default; apply=true moves. pending_only=true limits it to those records."

func (p *Plugin) repairMergedUserStateDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              merge.UserStateRepairOpID,
		Preview:         sdk.PreviewSpec{Params: repairMergedUserStateParams{}},
		Liveness:        sdk.LivenessNone,
		ProgressTimeout: 30 * time.Minute,
		Plugin:          "maintenance",
		DisplayName:     "Repair user state stranded by merges",
		Description:     repairMergedUserStateHelp,
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  merge.UserStateRepairOpID,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runRepairMergedUserState,
	}
}

func (p *Plugin) runRepairMergedUserState(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	var params repairMergedUserStateParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	return p.repairMergedUserState(ctx, params, reporter)
}

func (p *Plugin) repairMergedUserState(ctx context.Context, params repairMergedUserStateParams, reporter sdk.Reporter) error {
	env := repairMergedUserStateEnv{t6g6Env: t6g6HeadEnv(reporter, params.Apply)}
	// A nil store leaves the env's writer nil (a typed-nil adapter would hide it),
	// so the core's "no store" check still fires.
	if store := p.deps.MergeUserStateStore(); store != nil {
		env.Writer = headRepairMergedUserStateWriter{store: store}
	}
	_, err := repairMergedUserStateCore(ctx, params, env)
	return err
}

// repairMergedUserStateKind is rule R3's kind for the def above.
const repairMergedUserStateKind = tasks.Kind("books." + merge.UserStateRepairOpID)

// repairMergedUserStateWriter is the repair itself. The merge package finds the
// stranded rows and moves them in one pass through the same store, so the whole
// pass is one writer call: a native run's writer is the kernel-gated one, and a
// report-only (Preview) pass makes no write through it.
type repairMergedUserStateWriter interface {
	RepairMergedUserState(ctx context.Context, opts merge.UserStateRepairOptions) (*merge.UserStateRepairReport, error)
}

// headRepairMergedUserStateWriter is HEAD's writer: merge.RepairMergedUserState
// over the plugin's user-state store.
type headRepairMergedUserStateWriter struct{ store merge.UserStateRepairStore }

func (w headRepairMergedUserStateWriter) RepairMergedUserState(ctx context.Context, opts merge.UserStateRepairOptions) (*merge.UserStateRepairReport, error) {
	return merge.RepairMergedUserState(ctx, w.store, opts)
}

// repairMergedUserStateEnv is what the core takes from its shell. The op reads
// only through the writer's pass, so it has no separate store.
type repairMergedUserStateEnv struct {
	t6g6Env
	Writer repairMergedUserStateWriter
}

// repairMergedUserStateParallelism is the op's concurrency: HEAD declared none
// and the merge package's pass is sequential.
func repairMergedUserStateParallelism() tasks.Parallelism {
	return tasks.Sequential("HEAD declared no concurrency; merge.RepairMergedUserState is one sequential pass over the pending records and every user's rows")
}

// repairMergedUserStateNative is the native kind. Preview is the report-only pass
// HEAD's apply=false was; Live moves the state.
func (p *Plugin) repairMergedUserStateNative(deps t6g6Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := t6g6Common(repairMergedUserStateKind, sched)
	c.Title = "Repair user state stranded by merges"
	c.Help = repairMergedUserStateHelp
	// HEAD declared no Writes. The pass moves listening state (position rows,
	// progress rows, bookmarks) and clears the pending-repair records; those
	// are user-state rows of the books domain.
	c.Effects = tasks.Writes(tasks.Res("books", "user_state"))
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books." + merge.UserStateRepairOpID)
	c.Timeout = 30 * time.Minute
	c.ProgressTimeout = 30 * time.Minute
	return tasks.Task(repairMergedUserStateKind, tasks.TaskSpec[repairMergedUserStateNativeParams, merge.UserStateRepairReport]{
		Common: c,
		Run: func(rc *tasks.RC, in repairMergedUserStateNativeParams) (merge.UserStateRepairReport, error) {
			params := repairMergedUserStateParams{Apply: rc.Mode() == tasks.ModeLive, PendingOnly: in.PendingOnly}
			env := repairMergedUserStateEnv{t6g6Env: t6g6NativeEnv(rc)}
			// Built in Preview too: a report-only pass writes nothing through it.
			if deps.NewWriter != nil {
				env.Writer = deps.NewWriter(rc.Writer())
			}
			rep, err := repairMergedUserStateCore(rc.Context(), params, env)
			if rep == nil {
				return merge.UserStateRepairReport{}, err
			}
			return *rep, err
		},
	})
}

// repairMergedUserStateNativeParams are the native run's params: HEAD's without
// Apply, which the run's mode decides.
type repairMergedUserStateNativeParams struct {
	PendingOnly bool `json:"pending_only,omitempty"`
}

// repairMergedUserStateCore is the body of repairMergedUserState. It returns the
// report so a native shell can hand it back as the run's result.
func repairMergedUserStateCore(ctx context.Context, params repairMergedUserStateParams, env repairMergedUserStateEnv) (*merge.UserStateRepairReport, error) {
	if env.Writer == nil {
		return nil, fmt.Errorf("repair-merged-user-state: no store")
	}
	rep, err := env.Writer.RepairMergedUserState(ctx, merge.UserStateRepairOptions{Apply: params.Apply, PendingOnly: params.PendingOnly})
	if err != nil {
		return nil, fmt.Errorf("repair-merged-user-state: %w", err)
	}
	summary := fmt.Sprintf("Merged user state (apply=%t pending_only=%t): pending %d (completed %d), stranded books %d, state rows %d, position rows %d, moved %d, bookmarks owed %d (copied %d), unresolved books %d, errors %d",
		rep.Apply, rep.PendingOnly, rep.PendingRecords, rep.PendingCompleted, rep.StrandedBooks, rep.StateRows,
		rep.PositionRows, rep.StateMoved, rep.BookmarksOwed, rep.BookmarksCopied, rep.UnresolvedBooks, rep.Errors)
	env.Log(slog.LevelInfo, summary)
	env.Progress(1, 1, summary)
	if env.SetResult != nil {
		if err := env.SetResult(rep); err != nil {
			env.Log(slog.LevelWarn, fmt.Sprintf("could not persist result data: %v", err))
		}
	}
	if rep.Errors > 0 {
		return rep, fmt.Errorf("repair-merged-user-state: %d item(s) failed; see result items", rep.Errors)
	}
	return rep, nil
}
