// file: internal/plugins/dedup/split_book_bulk_merge.go
// version: 1.5.0
// guid: 3f8eb2e1-b4b7-4d83-b176-fc427cc5d98c
// last-edited: 2026-11-18

package dedup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// splitBookBulkMergeKind is rule R3's native kind for dedup.split-book-bulk-merge.
const splitBookBulkMergeKind = tasks.Kind("books.dedup.split-book-bulk-merge")

// splitBookBulkMergeHelp is HEAD's Description, shared by both definitions (rule R5).
const splitBookBulkMergeHelp = "Merges a preflighted, disjoint set of reviewed split-book candidates."

func (p *Plugin) splitBookBulkMergeDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "dedup.split-book-bulk-merge",
		Preview:         sdk.PreviewSpec{Params: dedupengine.BulkSplitBookMergeParams{}},
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "dedup",
		DisplayName:     "Merge reviewed split-book candidates",
		Description:     splitBookBulkMergeHelp,
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

// splitBookBulkMergeReads is the one read the merge core makes: the merge-time
// recheck a dry run answers with.
type splitBookBulkMergeReads interface {
	RecheckSplitBookMembers(bookIDs []string, chapterMaxSec int) ([]string, error)
}

// splitBookBulkMergeWriter is what a live merge writes: the rechecked cluster
// merge (books, files and the undo journal, under the run's id) and the merged
// candidate's row.
type splitBookBulkMergeWriter interface {
	MergeSplitBookClusterRechecked(bookIDs []string, keepID, suggestedTitle, opID string) (*dedupengine.SplitBookMergeResult, error)
	Delete(candidateID string) error
}

// splitBookBulkMergeParams is the native run's params. The mode is the start
// request's, so (unlike BulkSplitBookMergeParams) there is no dry-run key.
type splitBookBulkMergeParams struct {
	Items []dedupengine.BulkSplitBookMergeItem `json:"items"`
}

// splitBookBulkMergeEnv is what the core takes from its shell. Reads serves a dry
// run and Writer a live run; the other may be nil.
type splitBookBulkMergeEnv struct {
	kindEnv
	Reads  splitBookBulkMergeReads
	Writer splitBookBulkMergeWriter
	Items  []dedupengine.BulkSplitBookMergeItem
}

// splitBookBulkMergeResult is the native run's result. In a dry run Merged is 0
// and Ready counts the candidates the real run would attempt.
type splitBookBulkMergeResult struct {
	Merged         int `json:"merged"`
	Ready          int `json:"ready,omitempty"`
	RefusedITunes  int `json:"refused_itunes"`
	RefusedRecheck int `json:"refused_recheck"`
	Retained       int `json:"retained"`
}

// errSplitBookNoItems is the refusal of an empty batch.
var errSplitBookNoItems = errors.New("split-book bulk merge: no candidate snapshots")

// splitBookBulkMergeCore is the body of runSplitBookBulkMerge, shared by both
// shells. The loop is sequential on purpose: every merge takes the process-wide
// merge lock (merge.LockMergeRMW), so a pool would only queue on it, and the
// batch is a disjoint preflighted set a single run owns.
func splitBookBulkMergeCore(ctx context.Context, env splitBookBulkMergeEnv) (splitBookBulkMergeResult, error) {
	var out splitBookBulkMergeResult
	if len(env.Items) == 0 {
		return out, errSplitBookNoItems
	}
	opID := env.OpID
	progress := newProgress(env.kindEnv, len(env.Items))
	progress.Start("Processing reviewed split-book candidates...")
	for i, item := range env.Items {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if !env.Live {
			// A dry run answers the question the real run will: would the
			// merge-time recheck refuse this candidate now?
			reasons, rerr := env.Reads.RecheckSplitBookMembers(item.BookIDs, dedupengine.RepairChapterMaxSec())
			switch {
			case rerr != nil:
				env.Logger.Warn("split-book merge dry-run: recheck could not read a member",
					"candidate_id", item.CandidateID, "error", rerr)
				progress.StepN(1, fmt.Sprintf("Dry-run would retain (unreadable): %d / %d", i+1, len(env.Items)))
			case len(reasons) > 0:
				out.RefusedRecheck++
				env.Logger.Warn("split-book merge dry-run: the real run would refuse this candidate",
					"candidate_id", item.CandidateID, "reasons", reasons)
				progress.StepN(1, fmt.Sprintf("Dry-run would refuse (not chapters): %d / %d", i+1, len(env.Items)))
			default:
				out.Ready++
				env.Logger.Info("split-book merge dry-run ready", "candidate_id", item.CandidateID, "book_count", len(item.BookIDs))
				progress.StepN(1, fmt.Sprintf("Dry-run ready: %d / %d", i+1, len(env.Items)))
			}
			continue
		}
		// Each merge journals its undo pointer under this run, so reverting
		// the op undoes every merge of the batch. The candidate is a snapshot
		// stored at scan time (or queued before a rescan): the merge re-reads
		// every member and refuses a cluster that is no longer shown to be
		// chapters of one work, so a stale row can never merge distinct books.
		srcIDs := make([]string, 0, len(item.BookIDs)-1)
		for _, bookID := range item.BookIDs {
			if bookID != item.KeepID {
				srcIDs = append(srcIDs, bookID)
			}
		}
		result, err := env.Writer.MergeSplitBookClusterRechecked(item.BookIDs, item.KeepID, item.SuggestedTitle, opID)
		if errors.Is(err, dedupengine.ErrSplitBookRefused) {
			out.RefusedRecheck++
			env.Logger.Warn("split-book candidate merge refused: its books are no longer shown to be chapters of one work; the candidate stays reviewable",
				"candidate_id", item.CandidateID, "error", err)
			progress.StepN(1, fmt.Sprintf("Refused (not chapters): %d / %d", i+1, len(env.Items)))
			continue
		}
		if errors.Is(err, merge.ErrITunesProtected) {
			out.RefusedITunes++
			env.Logger.Warn("split-book candidate merge refused: a book has a file under the active iTunes library",
				"candidate_id", item.CandidateID, "error", err)
			progress.StepN(1, fmt.Sprintf("Refused (iTunes library): %d / %d", i+1, len(env.Items)))
			continue
		}
		complete := err == nil && result != nil && len(result.Errors) == 0 && result.MergedSrcCount == len(srcIDs)
		if !complete {
			out.Retained++
			env.Logger.Error("split-book candidate merge incomplete", "candidate_id", item.CandidateID, "error", err, "result", result)
			progress.StepN(1, fmt.Sprintf("Retained incomplete candidate: %d / %d", i+1, len(env.Items)))
			continue
		}
		out.Merged++
		if err := env.Writer.Delete(item.CandidateID); err != nil {
			env.Logger.Error("split-book candidate delete failed after merge", "candidate_id", item.CandidateID, "error", err)
		}
		progress.StepN(1, fmt.Sprintf("Merged candidate: %d / %d", i+1, len(env.Items)))
	}
	progress.Done(fmt.Sprintf("Split-book bulk merge finished; incomplete candidates remain reviewable (%d refused: iTunes library, %d refused: not shown to be chapters)", out.RefusedITunes, out.RefusedRecheck))
	return out, nil
}

func (p *Plugin) runSplitBookBulkMerge(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params dedupengine.BulkSplitBookMergeParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return fmt.Errorf("split-book bulk merge params: %w", err)
	}
	dryRun, err := opmode.ResolveDryRun("dedup.split-book-bulk-merge", params.DryRun, params.DryRunCamel)
	if err != nil {
		return err
	}
	if len(params.Items) == 0 {
		return errSplitBookNoItems
	}
	if p.store == nil {
		return fmt.Errorf("split-book bulk merge: store unavailable")
	}
	env := splitBookBulkMergeEnv{
		kindEnv: headEnv(reporter, !dryRun),
		Reads:   headSplitReads{store: p.store},
		Items:   params.Items,
	}
	if !dryRun {
		if p.embeddingStore == nil || p.embeddingStore.PebbleDB() == nil {
			return fmt.Errorf("split-book bulk merge: candidate store unavailable")
		}
		env.Writer = headSplitWriter{store: p.store, candidates: dedupengine.NewSplitBookStore(p.embeddingStore.PebbleDB())}
	}
	_, err = splitBookBulkMergeCore(ctx, env)
	return err
}
