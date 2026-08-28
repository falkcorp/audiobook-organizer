// file: internal/plugins/dedup/split_book_bulk_merge.go
// version: 1.0.0
// guid: 3f8eb2e1-b4b7-4d83-b176-fc427cc5d98c
// last-edited: 2026-08-28

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

func (p *Plugin) splitBookBulkMergeDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "dedup.split-book-bulk-merge",
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "dedup",
		DisplayName:     "Merge reviewed split-book candidates",
		Description:     "Merges a preflighted, disjoint set of reviewed split-book candidates.",
		ResumePolicy:    sdk.ResumeAsk,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "dedup.split-book-merge",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         60 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Writes:          []sdk.Resource{sdk.ResBooks, sdk.ResBookFiles},
		Run:             p.runSplitBookBulkMerge,
	}
}

func (p *Plugin) runSplitBookBulkMerge(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params dedupengine.BulkSplitBookMergeParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return fmt.Errorf("split-book bulk merge params: %w", err)
	}
	if len(params.Items) == 0 {
		return fmt.Errorf("split-book bulk merge: no candidate snapshots")
	}
	if p.store == nil {
		return fmt.Errorf("split-book bulk merge: store unavailable")
	}
	var candidateStore *dedupengine.SplitBookStore
	if !params.DryRun {
		if p.embeddingStore == nil || p.embeddingStore.PebbleDB() == nil {
			return fmt.Errorf("split-book bulk merge: candidate store unavailable")
		}
		candidateStore = dedupengine.NewSplitBookStore(p.embeddingStore.PebbleDB())
	}

	progress := sdk.NewProgress(reporter, len(params.Items))
	progress.Start("Processing reviewed split-book candidates...")
	for i, item := range params.Items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if params.DryRun {
			reporter.Logger().Info("split-book merge dry-run ready", "candidate_id", item.CandidateID, "book_count", len(item.BookIDs))
			progress.StepN(1, fmt.Sprintf("Dry-run ready: %d / %d", i+1, len(params.Items)))
			continue
		}
		srcIDs := make([]string, 0, len(item.BookIDs)-1)
		for _, bookID := range item.BookIDs {
			if bookID != item.KeepID {
				srcIDs = append(srcIDs, bookID)
			}
		}
		result, err := dedupengine.MergeSplitBookCluster(p.store, item.KeepID, srcIDs, item.SuggestedTitle)
		complete := err == nil && result != nil && len(result.Errors) == 0 && result.MergedSrcCount == len(srcIDs)
		if !complete {
			reporter.Logger().Error("split-book candidate merge incomplete", "candidate_id", item.CandidateID, "error", err, "result", result)
			progress.StepN(1, fmt.Sprintf("Retained incomplete candidate: %d / %d", i+1, len(params.Items)))
			continue
		}
		if err := candidateStore.Delete(item.CandidateID); err != nil {
			reporter.Logger().Error("split-book candidate delete failed after merge", "candidate_id", item.CandidateID, "error", err)
		}
		progress.StepN(1, fmt.Sprintf("Merged candidate: %d / %d", i+1, len(params.Items)))
	}
	progress.Done("Split-book bulk merge finished; incomplete candidates remain reviewable")
	return nil
}
