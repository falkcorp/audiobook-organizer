// file: internal/plugins/dedup/mine_gold_labels.go
// version: 1.1.0
// guid: 8f4c2a16-5d70-4e93-bc28-1a6e9d3b7c52
// last-edited: 2026-07-05

// Package dedup — op dedup.mine-gold-labels (dedup tuning dataset, positive miner).
//
// Iterates pending candidates and labels the high-confidence DUPLICATES that the
// rule Classify() does not catch as positives — pairs that share a file hash, an
// AcoustID recording id, or an ASIN/ISBN (with audio on both sides). Each firing
// candidate gets a LabeledExample written with label_source="auto_high_conf" and
// label="true_dup", reusing the candidate's own id (no synthetic rows).
//
// This complements dedup.dataset-backfill (which mines rule-based NEGATIVES) and
// the live human-capture path (merge/dismiss → label_source="human"): together they
// seed the tuning dataset. These auto-mined labels are high precision but NOT human
// gold — the classifier should treat them as weak/strong supervision and validate
// only on human labels.
//
// Dry-run by default: reports counts, writes nothing. apply=true is idempotent
// (UpsertLabeledExample overwrites), so re-running is safe.
package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/dataset"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

type mineGoldLabelsParams struct {
	// Apply, if true, writes the mined true_dup labels. Default false (dry-run).
	Apply bool `json:"apply"`
}

func (p *Plugin) mineGoldLabelsDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "dedup.mine-gold-labels",
		Plugin:      "dedup",
		DisplayName: "Mine high-confidence dedup labels",
		Description: "Labels pending candidates that share a file hash, AcoustID recording id, or " +
			"ASIN/ISBN as true_dup (label_source=auto_high_conf), seeding the tuning dataset with " +
			"in-house positive ground truth. Dry-run by default.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "dedup.mine-gold-labels",
		Cancellable:     true,
		Timeout:         60 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runMineGoldLabels,
	}
}

func (p *Plugin) runMineGoldLabels(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	if p.embeddingStore == nil {
		return fmt.Errorf("embedding store not available")
	}
	if p.store == nil {
		return fmt.Errorf("main store not available")
	}

	var params mineGoldLabelsParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	reporter.Logger().Info("mine-gold-labels start", "apply", params.Apply)

	_ = reporter.UpdateProgress(0, 2, "Loading pending candidates…")
	cands, _, err := p.embeddingStore.ListCandidates(database.CandidateFilter{Status: "pending", Limit: 1_000_000})
	if err != nil {
		return fmt.Errorf("list candidates: %w", err)
	}
	reporter.Logger().Info("mine-gold-labels: candidates loaded", "count", len(cands))

	// Book-lookup memoization: a book referenced by many candidate rows was
	// re-read from the store on every occurrence (up to 4 reads/candidate with
	// no reuse). Mirrors the bookCache pattern in drain_stale.go /
	// server_maintenance_deps.go, wrapped in a mutex since the cache is now
	// shared across the RunItems worker pool below (CONC-8).
	adapter := newMemoizedBuilderAdapter(builderAdapter{store: p.store})

	// examined/mined/written/errs are incremented from every worker goroutine
	// below, so they're guarded by statsMu (the candidate processing itself —
	// the store reads/writes — is independent per candidate and needs no lock).
	var (
		statsMu                        sync.Mutex
		examined, mined, written, errs int
	)

	_ = reporter.UpdateProgress(1, 2, fmt.Sprintf("Mining %d candidates…", len(cands)))
	err = registry.RunItems(ctx, reporter, cands, func(ctx context.Context, c database.DedupCandidate) error {
		if reporter.IsCanceled() {
			return context.Canceled
		}

		statsMu.Lock()
		examined++
		statsMu.Unlock()

		a, aErr := adapter.GetBook(c.EntityAID)
		b, bErr := adapter.GetBook(c.EntityBID)
		if aErr != nil || bErr != nil || a == nil || b == nil {
			statsMu.Lock()
			errs++
			statsMu.Unlock()
			return nil
		}
		aFiles, afErr := adapter.GetBookFiles(c.EntityAID)
		bFiles, bfErr := adapter.GetBookFiles(c.EntityBID)
		if afErr != nil || bfErr != nil {
			statsMu.Lock()
			errs++
			statsMu.Unlock()
			return nil
		}

		label, reason, fires := dataset.MineHighConfidenceDup(a, b, aFiles, bFiles)
		if !fires {
			return nil
		}
		statsMu.Lock()
		mined++
		statsMu.Unlock()

		if params.Apply {
			ex, buildErr := dataset.BuildExample(adapter, c)
			if buildErr != nil {
				statsMu.Lock()
				errs++
				statsMu.Unlock()
				reporter.Logger().Warn("mine-gold-labels: build example failed", "candidate_id", c.ID, "error", buildErr)
				return nil
			}
			ex.Label = label
			ex.LabelSource = "auto_high_conf"
			ex.LabelReason = reason
			ex.DecidedAt = time.Now().UTC().Format(time.RFC3339)
			if err := p.embeddingStore.UpsertLabeledExample(ex); err != nil {
				statsMu.Lock()
				errs++
				statsMu.Unlock()
				reporter.Logger().Error("mine-gold-labels: upsert error", "candidate_id", c.ID, "error", err)
				return nil
			}
			statsMu.Lock()
			written++
			statsMu.Unlock()
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: runtime.NumCPU(),
		Label: func(i, total int) string {
			return fmt.Sprintf("Mined %d/%d…", i+1, total)
		},
	})
	if err != nil {
		return err
	}

	summary := fmt.Sprintf("examined=%d mined=%d written=%d errs=%d (apply=%v)", examined, mined, written, errs, params.Apply)
	reporter.Logger().Info("mine-gold-labels complete", "summary", summary)
	if !params.Apply {
		_ = reporter.UpdateProgress(2, 2, fmt.Sprintf("Dry-run — %d of %d candidates are high-confidence true_dup. Pass apply=true to write.", mined, examined))
	} else {
		_ = reporter.UpdateProgress(2, 2, fmt.Sprintf("Complete — wrote %d true_dup labels. %s", written, summary))
	}
	return nil
}
