// file: internal/plugins/dedup/rebuild_gold_labels.go
// version: 1.1.0
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

// rebuildDiffSample is one changed-or-unlabelable row surfaced in the report
// sample, so a reviewer can spot-check specific candidates before applying.
type rebuildDiffSample struct {
	CandidateID int64
	Source      string // "rule" | "auto_high_conf"
	OldLabel    string
	NewLabel    string // empty when Unlabelable
	Unlabelable bool
}

// rebuildSampleLimit bounds how many changed/unlabelable rows are captured
// per bucket for the report sample, mirroring drain-stale's reason-breakdown
// logging without holding onto an unbounded slice for huge label stores.
const rebuildSampleLimit = 5

// rebuildReport is the full result of computeRebuildDiff: per-bucket stats,
// pass-through counts, the freshly computed rows ready to write on apply, and
// a bounded sample of changed/unlabelable candidates for human review.
type rebuildReport struct {
	Rule       rebuildBucketStats
	Auto       rebuildBucketStats
	HumanCount int
	OtherCount int // LabelSource=="" (unlabeled backfill rows) — untouched, counted for visibility
	Fresh      []database.LabeledExample
	Sample     []rebuildDiffSample
}

func (r rebuildReport) summary() string {
	return fmt.Sprintf(
		"rule[%s] auto_high_conf[%s] human=%d(passthrough) other_untouched=%d",
		r.Rule.String(), r.Auto.String(), r.HumanCount, r.OtherCount,
	)
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

	_ = reporter.UpdateProgress(1, 3, fmt.Sprintf("Re-deriving %d existing examples…", len(existing)))
	report, err := p.computeRebuildDiff(ctx, reporter, existing)
	if err != nil {
		return err
	}

	reporter.Logger().Info("rebuild-gold-labels: rule bucket", "stats", report.Rule.String())
	reporter.Logger().Info("rebuild-gold-labels: auto_high_conf bucket", "stats", report.Auto.String())
	reporter.Logger().Info("rebuild-gold-labels: pass-through", "human", report.HumanCount, "other_untouched", report.OtherCount)
	for _, s := range report.Sample {
		reporter.Logger().Info("rebuild-gold-labels: sample",
			"candidate_id", s.CandidateID, "source", s.Source,
			"old_label", s.OldLabel, "new_label", s.NewLabel, "unlabelable", s.Unlabelable)
	}

	summary := report.summary()

	if !params.Apply {
		_ = reporter.UpdateProgress(3, 3, fmt.Sprintf(
			"Dry-run — %d rule + %d auto_high_conf would change, %d would become unlabelable. %s. Pass apply=true to write.",
			report.Rule.Changed, report.Auto.Changed, report.Rule.Unlabelable+report.Auto.Unlabelable, summary))
		reporter.Logger().Info("rebuild-gold-labels: dry-run only; nothing written")
		return nil
	}

	_ = reporter.UpdateProgress(2, 3, fmt.Sprintf("Applying — deleting %d rule/auto_high_conf rows…", report.Rule.Examined+report.Auto.Examined))
	deleted, err := p.embeddingStore.DeleteLabeledExamplesBySource("rule", "auto_high_conf")
	if err != nil {
		return fmt.Errorf("delete rule/auto_high_conf examples: %w", err)
	}
	reporter.Logger().Info("rebuild-gold-labels: deleted stale mechanical labels", "deleted", deleted)

	var written, writeErrs int
	for _, ex := range report.Fresh {
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

// computeRebuildDiff is the pure(ish) core of the op: it re-derives every
// rule/auto_high_conf example against current state and returns the full
// diff — per-bucket stats, pass-through counts, the freshly computed rows
// ready to write on apply, and a bounded sample of changed/unlabelable
// candidates. Split out from runRebuildGoldLabels so the diff itself (the
// deliverable of dry-run) is directly unit-testable, not just observable via
// log lines. The only side effects are reads (GetCandidateByID, GetBook,
// GetBookFiles via adapter) — no writes happen here.
func (p *Plugin) computeRebuildDiff(ctx context.Context, reporter sdk.Reporter, existing []database.LabeledExample) (rebuildReport, error) {
	adapter := builderAdapter{store: p.store}

	var report rebuildReport

	for i := range existing {
		if reporter.IsCanceled() {
			return rebuildReport{}, context.Canceled
		}
		select {
		case <-ctx.Done():
			return rebuildReport{}, ctx.Err()
		default:
		}

		old := existing[i]
		if (i+1)%1000 == 0 {
			_ = reporter.UpdateProgress(1, 3, fmt.Sprintf("Re-derived %d/%d…", i+1, len(existing)))
		}

		switch old.LabelSource {
		case "human":
			report.HumanCount++
		case "rule":
			ex, unlabelable, changed := p.rebuildRuleExample(adapter, old)
			report.applyBucketResult(&report.Rule, "rule", old, ex, unlabelable, changed)
		case "auto_high_conf":
			ex, unlabelable, changed := p.rebuildAutoHighConfExample(adapter, old)
			report.applyBucketResult(&report.Auto, "auto_high_conf", old, ex, unlabelable, changed)
		default:
			// Unlabeled backfill rows (LabelSource=="") and any unrecognized
			// source are left entirely alone — out of scope for this op.
			report.OtherCount++
		}
	}

	return report, nil
}

// applyBucketResult records one rebuilt example's outcome into the given
// bucket's stats, appends it to the fresh set (unless unlabelable), and
// grows the bounded diff sample for changed/unlabelable rows.
func (r *rebuildReport) applyBucketResult(bucket *rebuildBucketStats, source string, old, fresh database.LabeledExample, unlabelable, changed bool) {
	bucket.Examined++
	if unlabelable {
		bucket.Unlabelable++
		if len(r.Sample) < rebuildSampleLimit {
			r.Sample = append(r.Sample, rebuildDiffSample{
				CandidateID: old.CandidateID, Source: source, OldLabel: old.Label, Unlabelable: true,
			})
		}
		return
	}
	if changed {
		bucket.Changed++
		if len(r.Sample) < rebuildSampleLimit {
			r.Sample = append(r.Sample, rebuildDiffSample{
				CandidateID: old.CandidateID, Source: source, OldLabel: old.Label, NewLabel: fresh.Label,
			})
		}
	} else {
		bucket.Unchanged++
	}
	switch fresh.Label {
	case "true_dup":
		bucket.NewTrueDup++
	case "not_dup":
		bucket.NewNotDup++
	}
	r.Fresh = append(r.Fresh, fresh)
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
