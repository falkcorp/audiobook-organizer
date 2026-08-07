// file: internal/plugins/maintenance/auto_match_transcribed.go
// version: 1.0.0
// guid: 7a3b5c1d-2e4f-6a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-06-29

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// autoMatchTranscribedParams is the checkpoint/input state for a
// maintenance.auto-match-transcribed run.
type autoMatchTranscribedParams struct {
	// LastBookID is the resume checkpoint. On restart, processing skips every
	// book whose ID comes before (and including) this value in ListBookIDs order.
	LastBookID string `json:"last_book_id,omitempty"`
	// DryRun defaults to true when nil. The op MUST NOT mutate any book unless
	// this is explicitly set to false.
	DryRun *bool `json:"dry_run,omitempty"`
	// MinScore is the minimum candidate score required to qualify for apply.
	// Defaults to 0.75 when ≤0. Scores are uncapped (transcription boosts can
	// push them above 1.0), so 0.75 sits well above a random token-overlap hit.
	MinScore float64 `json:"min_score,omitempty"`
}

func (p *Plugin) autoMatchTranscribedDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.auto-match-transcribed",
		Plugin:          "maintenance",
		DisplayName:     "Auto-match transcribed books",
		Description:     "Walks the library and auto-applies the best metadata candidate to unreviewed books whose audio-derived transcription exactly matches a search result above a configurable score threshold. Dry-run by default — pass dry_run=false to apply. Checkpointed and cancellable.",
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.auto-match-transcribed",
		Cancellable:     true,
		Timeout:         12 * time.Hour,
		Run:             p.runAutoMatchTranscribed,
	}
}

func (p *Plugin) runAutoMatchTranscribed(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	if !p.deps.HasMetadataFetchService() {
		return fmt.Errorf("metadata fetch service not initialized; cannot run auto-match-transcribed")
	}

	var params autoMatchTranscribedParams
	if len(rawParams) > 0 {
		_ = json.Unmarshal(rawParams, &params)
	}

	// Default dry_run to true — safety first.
	dryRun := params.DryRun == nil || *params.DryRun

	// Default min_score to 0.75.
	minScore := params.MinScore
	if minScore <= 0 {
		minScore = 0.75
	}

	log := reporter.Logger()

	allIDs, err := store.ListBookIDs()
	if err != nil {
		return fmt.Errorf("list book ids: %w", err)
	}
	total := len(allIDs)

	// Resume: find where to restart from the checkpoint.
	startIdx := 0
	if params.LastBookID != "" {
		for i, id := range allIDs {
			if id == params.LastBookID {
				startIdx = i + 1
				break
			}
		}
	}

	log.Info("auto-match-transcribed: starting",
		"dry_run", dryRun, "min_score", minScore,
		"total_books", total, "start_index", startIdx)

	if startIdx >= total {
		_ = reporter.UpdateProgress(total, total, "nothing to process — already at end")
		return nil
	}

	var scanned, eligible, applied int

	// lastID tracks the most-recently visited book for checkpoint writes.
	lastID := params.LastBookID

	err = registry.RunItems(ctx, reporter, allIDs[startIdx:], func(ctx context.Context, id string) error {
		scanned++
		b, getErr := store.GetBookByID(id)
		if getErr != nil || b == nil {
			return nil // non-fatal: skip missing / deleted books
		}
		lastID = id

		// Only touch books that have never been reviewed.
		if b.MetadataReviewStatus != nil {
			return nil
		}
		// Must have an audio-derived title to search with.
		if b.TranscribedTitle == nil || *b.TranscribedTitle == "" {
			return nil
		}

		transTitle := *b.TranscribedTitle
		transAuthor := ""
		if b.TranscribedAuthor != nil {
			transAuthor = *b.TranscribedAuthor
		}

		candTitle, candAuthor, score, found, searchErr := p.deps.SearchTranscriptionCandidate(
			ctx, id, transTitle, transAuthor,
		)
		if searchErr != nil {
			log.Warn("auto-match-transcribed: search failed",
				"book_id", id, "err", searchErr)
			return nil // non-fatal: move on to next book
		}
		if !found {
			return nil
		}

		// Gate 1: candidate score must meet the threshold.
		if score < minScore {
			return nil
		}

		// Gate 2: normalized title must exactly match the transcribed title.
		// This is the same normalization used by ApplyMetadataCandidate's
		// audio-confirm path (TASK-02), ensuring the two checks agree.
		if util.NormalizeTitle(candTitle) != util.NormalizeTitle(transTitle) {
			return nil
		}

		// Gate 3: if the transcribed author is substantial (>3 chars), require
		// that it appears inside the candidate author or vice versa. Mirrors the
		// containsCI guard in service_scoring.go so the gate never fires on a
		// short/empty token.
		if len(transAuthor) > 3 {
			al := strings.ToLower(candAuthor)
			tl := strings.ToLower(transAuthor)
			if !strings.Contains(al, tl) && !strings.Contains(tl, al) {
				return nil
			}
		}

		// All gates passed — this book is eligible.
		eligible++

		if dryRun {
			log.Info("auto-match-transcribed: would-apply",
				"book_id", id, "db_title", b.Title,
				"candidate", candTitle, "score", score)
			return nil // no mutation in dry-run
		}

		if applyErr := p.deps.ApplyTranscriptionCandidate(ctx, id, candTitle, candAuthor); applyErr != nil {
			log.Warn("auto-match-transcribed: apply failed",
				"book_id", id, "candidate", candTitle, "err", applyErr)
			return nil // non-fatal: continue with remaining books
		}
		log.Info("auto-match-transcribed: applied",
			"book_id", id, "db_title", b.Title, "candidate", candTitle, "score", score)
		applied++
		return nil
	}, registry.RunItemsOptions{
		Concurrency:    1,
		ProgressTotal:  total,
		ProgressOffset: startIdx,
		Label: func(i, t int) string {
			return fmt.Sprintf("auto-match %d/%d — eligible: %d, applied: %d",
				startIdx+i+1, t, eligible, applied)
		},
		CheckpointFn: func(_ context.Context) error {
			dr := dryRun
			return reporter.Checkpoint(autoMatchTranscribedParams{
				LastBookID: lastID,
				DryRun:     &dr,
				MinScore:   minScore,
			})
		},
	})
	if err != nil {
		return err
	}

	var summary string
	if dryRun {
		summary = fmt.Sprintf(
			"auto-match-transcribed complete (dry-run): scanned %d, eligible %d, would-apply %d",
			scanned, eligible, eligible,
		)
	} else {
		summary = fmt.Sprintf(
			"auto-match-transcribed complete: scanned %d, eligible %d, applied %d",
			scanned, eligible, applied,
		)
	}
	log.Info(summary)
	_ = reporter.UpdateProgress(total, total, summary)
	return nil
}
