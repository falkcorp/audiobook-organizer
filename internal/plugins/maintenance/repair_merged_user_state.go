// file: internal/plugins/maintenance/repair_merged_user_state.go
// version: 1.0.2
// guid: 8d4b2f67-1a9e-4c35-b7d0-9e6f3a2c1b58
// last-edited: 2026-09-26

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

	"github.com/falkcorp/audiobook-organizer/internal/merge"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

type repairMergedUserStateParams struct {
	// Apply must be explicitly true to move anything. Default false = preview.
	Apply bool `json:"apply"`
	// PendingOnly limits the run to the pending-repair records.
	PendingOnly bool `json:"pending_only,omitempty"`
}

func (p *Plugin) repairMergedUserStateDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.repair-merged-user-state",
		Liveness:        sdk.LivenessNone,
		ProgressTimeout: 30 * time.Minute,
		Plugin:          "maintenance",
		DisplayName:     "Repair user state stranded by merges",
		Description:     "Finds listening state (position, finished, progress, last played, hide flag, bookmarks) still stored under merged-away or deleted books whose surviving book is known, and moves it onto the survivor with the merge conflict rule (finished sticky, newest position wins, last played = max, hide kept). Also completes the pending-repair records a failed merge left. Preview by default; apply=true moves. pending_only=true limits it to those records.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.repair-merged-user-state",
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
	store := p.deps.MergeUserStateStore()
	if store == nil {
		return fmt.Errorf("repair-merged-user-state: no store")
	}
	rep, err := merge.RepairMergedUserState(ctx, store, merge.UserStateRepairOptions{Apply: params.Apply, PendingOnly: params.PendingOnly})
	if err != nil {
		return fmt.Errorf("repair-merged-user-state: %w", err)
	}
	summary := fmt.Sprintf("Merged user state (apply=%t pending_only=%t): pending %d (completed %d), stranded books %d, state rows %d, position rows %d, moved %d, bookmarks owed %d (copied %d), unresolved books %d, errors %d",
		rep.Apply, rep.PendingOnly, rep.PendingRecords, rep.PendingCompleted, rep.StrandedBooks, rep.StateRows,
		rep.PositionRows, rep.StateMoved, rep.BookmarksOwed, rep.BookmarksCopied, rep.UnresolvedBooks, rep.Errors)
	_ = reporter.Log(slog.LevelInfo, summary)
	_ = reporter.UpdateProgress(1, 1, summary)
	if err := opsregistry.ReporterSetResult(reporter, rep); err != nil {
		_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("could not persist result data: %v", err))
	}
	if rep.Errors > 0 {
		return fmt.Errorf("repair-merged-user-state: %d item(s) failed; see result items", rep.Errors)
	}
	return nil
}
