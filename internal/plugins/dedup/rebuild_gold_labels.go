// file: internal/plugins/dedup/rebuild_gold_labels.go
// version: 1.0.0
// guid: 74b6de83-de1b-4231-8edc-6920a2f7b91c
// last-edited: 2026-07-04

// Package dedup — op dedup.rebuild-gold-labels.
//
// The dedup gold-label store (dedup:label:*) predates the bge-m3 re-embed
// cutover and the CONS-16/17/FRAG candidate-quality fixes. This op wipes and
// regenerates the mechanically-derived portion of the label set — the
// label_source="rule" rows (dataset.Classify) and label_source="auto_high_conf"
// rows (dataset.MineHighConfidenceDup) — against *current* candidate/book/
// embedding state, while never touching label_source="human" rows (real
// merge/dismiss decisions) or unlabeled rows (LabelSource=="", Label=="",
// written by dedup.dataset-backfill for pairs no catcher fired on).
//
// Dry-run by default: for every existing rule/auto_high_conf example, re-derives
// what label it would get today and reports a diff (changed / unchanged /
// newly-unlabelable — candidate gone or the catcher no longer fires) plus a
// pass-through count of human-labeled examples. Pass {"apply":true} to delete
// all rule/auto_high_conf rows and reinsert the freshly computed set. Human
// rows are never deleted or modified by either mode.
//
// Idempotent: apply computes the same fresh set from current state each run,
// so a second apply run is a stable no-op (same counts, same rows).
package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/dataset"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

type rebuildGoldLabelsParams struct {
	// Apply, if true, deletes all rule/auto_high_conf sourced examples and
	// reinserts the freshly computed set. Default false (dry-run/report only).
	Apply bool `json:"apply"`
}

// rebuildBucketStats tallies the diff for one label_source bucket
// (rule or auto_high_conf) across the rebuild pass.
type rebuildBucketStats struct {
	Examined    int
	Unchanged   int
	Changed     int
	Unlabelable int // candidate gone, or the catcher no longer fires
	NewTrueDup  int
	NewNotDup   int
}

func (s rebuildBucketStats) String() string {
	return fmt.Sprintf("examined=%d changed=%d unchanged=%d unlabelable=%d new_true_dup=%d new_not_dup=%d",
		s.Examined, s.Changed, s.Unchanged, s.Unlabelable, s.NewTrueDup, s.NewNotDup)
}

func (p *Plugin) rebuildGoldLabelsDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "dedup.rebuild-gold-labels",
		Plugin:      "dedup",
		DisplayName: "Rebuild mechanically-derived gold labels",
		Description: "Re-derives label_source=rule and label_source=auto_high_conf gold labels against " +
			"current candidate/book/embedding state and reports a changed/unchanged/unlabelable diff. " +
			"Dry-run by default; pass apply=true to delete and reinsert the freshly computed rule/" +
			"auto_high_conf rows. label_source=human rows are always passed through, never deleted or " +
			"modified. Idempotent: re-running apply against unchanged state yields the same counts.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "dedup.rebuild-gold-labels",
		Cancellable:     true,
		Timeout:         60 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runRebuildGoldLabels,
	}
}

func (p *Plugin) runRebuildGoldLabels(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	if p.embeddingStore == nil {
		return fmt.Errorf("embedding store not available")
	}
	if p.store == nil {
		return fmt.Errorf("main store not available")
	}

	var params rebuildGoldLabelsParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	reporter.Logger().Info("rebuild-gold-labels start", "apply", params.Apply)

	_ = reporter.UpdateProgress(0, 3, "Loading existing labeled examples…")
	existing, err := p.embeddingStore.ListLabeledExamples(database.LabeledExampleFilter{Limit: 0})
	if err != nil {
		return fmt.Errorf("list labeled examples: %w", err)
	}
	reporter.Logger().Info("rebuild-gold-labels: existing examples loaded", "count", len(existing))

	adapter := builderAdapter{store: p.store}

	var (
		ruleStats  rebuildBucketStats
		autoStats  rebuildBucketStats
		humanCount int
		otherCount int // LabelSource=="" (unlabeled backfill rows) — untouched, counted for visibility
		fresh      []database.LabeledExample
	)

	_ = reporter.UpdateProgress(1, 3, fmt.Sprintf("Re-deriving %d existing examples…", len(existing)))
	for i := range existing {
		if reporter.IsCanceled() {
			return context.Canceled
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		old := existing[i]
		if (i+1)%1000 == 0 {
			_ = reporter.UpdateProgress(1, 3, fmt.Sprintf("Re-derived %d/%d…", i+1, len(existing)))
		}

		switch old.LabelSource {
		case "human":
			humanCount++
			continue
		case "rule":
			ruleStats.Examined++
			ex, unlabelable, changed := p.rebuildRuleExample(adapter, old)
			if unlabelable {
				ruleStats.Unlabelable++
				continue
			}
			if changed {
				ruleStats.Changed++
			} else {
				ruleStats.Unchanged++
			}
			switch ex.Label {
			case "true_dup":
				ruleStats.NewTrueDup++
			case "not_dup":
				ruleStats.NewNotDup++
			}
			fresh = append(fresh, ex)
		case "auto_high_conf":
			autoStats.Examined++
			ex, unlabelable, changed := p.rebuildAutoHighConfExample(adapter, old)
			if unlabelable {
				autoStats.Unlabelable++
				continue
			}
			if changed {
				autoStats.Changed++
			} else {
				autoStats.Unchanged++
			}
			switch ex.Label {
			case "true_dup":
				autoStats.NewTrueDup++
			case "not_dup":
				autoStats.NewNotDup++
			}
			fresh = append(fresh, ex)
		default:
			// Unlabeled backfill rows (LabelSource=="") and any unrecognized
			// source are left entirely alone — out of scope for this op.
			otherCount++
		}
	}

	reporter.Logger().Info("rebuild-gold-labels: rule bucket", "stats", ruleStats.String())
	reporter.Logger().Info("rebuild-gold-labels: auto_high_conf bucket", "stats", autoStats.String())
	reporter.Logger().Info("rebuild-gold-labels: pass-through", "human", humanCount, "other_untouched", otherCount)

	summary := fmt.Sprintf(
		"rule[%s] auto_high_conf[%s] human=%d(passthrough) other_untouched=%d",
		ruleStats.String(), autoStats.String(), humanCount, otherCount,
	)

	if !params.Apply {
		_ = reporter.UpdateProgress(3, 3, fmt.Sprintf(
			"Dry-run — %d rule + %d auto_high_conf would change, %d would become unlabelable. %s. Pass apply=true to write.",
			ruleStats.Changed, autoStats.Changed, ruleStats.Unlabelable+autoStats.Unlabelable, summary))
		reporter.Logger().Info("rebuild-gold-labels: dry-run only; nothing written")
		return nil
	}

	_ = reporter.UpdateProgress(2, 3, fmt.Sprintf("Applying — deleting %d rule/auto_high_conf rows…", ruleStats.Examined+autoStats.Examined))
	deleted, err := p.embeddingStore.DeleteLabeledExamplesBySource("rule", "auto_high_conf")
	if err != nil {
		return fmt.Errorf("delete rule/auto_high_conf examples: %w", err)
	}
	reporter.Logger().Info("rebuild-gold-labels: deleted stale mechanical labels", "deleted", deleted)

	var written, writeErrs int
	for _, ex := range fresh {
		if err := p.embeddingStore.UpsertLabeledExample(ex); err != nil {
			writeErrs++
			reporter.Logger().Error("rebuild-gold-labels: upsert error", "candidate_id", ex.CandidateID, "error", err)
			continue
		}
		written++
	}

	_ = reporter.UpdateProgress(3, 3, fmt.Sprintf(
		"Complete — deleted %d, wrote %d fresh (%d errors). %s", deleted, written, writeErrs, summary))
	reporter.Logger().Info("rebuild-gold-labels complete", "deleted", deleted, "written", written, "write_errs", writeErrs, "summary", summary)
	return nil
}

// rebuildRuleExample re-derives a label_source="rule" example's label against
// current state by re-running dataset.Classify() on a freshly built
// LabeledExample for the same candidate. Returns the fresh example, whether it
// is now unlabelable (candidate gone or Classify no longer fires), and whether
// the label changed relative to old.
func (p *Plugin) rebuildRuleExample(adapter builderAdapter, old database.LabeledExample) (ex database.LabeledExample, unlabelable, changed bool) {
	cand, err := p.embeddingStore.GetCandidateByID(old.CandidateID)
	if err != nil || cand == nil {
		return database.LabeledExample{}, true, false
	}

	fresh, err := dataset.BuildExample(adapter, *cand)
	if err != nil {
		return database.LabeledExample{}, true, false
	}

	label, reason, fires := dataset.Classify(fresh)
	if !fires {
		return database.LabeledExample{}, true, false
	}

	fresh.Label = label
	fresh.LabelSource = "rule"
	fresh.LabelReason = reason
	fresh.DecidedAt = time.Now().UTC().Format(time.RFC3339)
	return fresh, false, label != old.Label
}

// rebuildAutoHighConfExample re-derives a label_source="auto_high_conf"
// example's label against current state by re-running
// dataset.MineHighConfidenceDup() on the candidate's current books/files.
// Returns the fresh example, whether it is now unlabelable (candidate gone or
// the heuristic no longer fires), and whether the label changed.
func (p *Plugin) rebuildAutoHighConfExample(adapter builderAdapter, old database.LabeledExample) (ex database.LabeledExample, unlabelable, changed bool) {
	cand, err := p.embeddingStore.GetCandidateByID(old.CandidateID)
	if err != nil || cand == nil {
		return database.LabeledExample{}, true, false
	}

	a, aErr := adapter.GetBook(cand.EntityAID)
	b, bErr := adapter.GetBook(cand.EntityBID)
	if aErr != nil || bErr != nil || a == nil || b == nil {
		return database.LabeledExample{}, true, false
	}
	aFiles, afErr := adapter.GetBookFiles(cand.EntityAID)
	bFiles, bfErr := adapter.GetBookFiles(cand.EntityBID)
	if afErr != nil || bfErr != nil {
		return database.LabeledExample{}, true, false
	}

	label, reason, fires := dataset.MineHighConfidenceDup(a, b, aFiles, bFiles)
	if !fires {
		return database.LabeledExample{}, true, false
	}

	fresh, err := dataset.BuildExample(adapter, *cand)
	if err != nil {
		return database.LabeledExample{}, true, false
	}
	fresh.Label = label
	fresh.LabelSource = "auto_high_conf"
	fresh.LabelReason = reason
	fresh.DecidedAt = time.Now().UTC().Format(time.RFC3339)
	return fresh, false, label != old.Label
}
