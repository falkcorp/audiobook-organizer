// file: internal/plugins/maintenance/auto_match_transcribed.go
// version: 1.7.0
// guid: 7a3b5c1d-2e4f-6a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-09-28

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/applycap"
	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
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
		ID:           "maintenance.auto-match-transcribed",
		Liveness:     sdk.LivenessRunItems,
		Plugin:       "maintenance",
		DisplayName:  "Auto-match transcribed books",
		Description:  "Walks the library and auto-applies the best metadata candidate to unreviewed books whose audio-derived transcription exactly matches a search result above a configurable score threshold. Dry-run by default — pass dry_run=false to apply. Checkpointed and cancellable.",
		ResumePolicy: sdk.ResumeRestart,
		// This op APPLIES metadata candidates to books (ApplyTranscriptionCandidate),
		// so it reads and writes the library. It declared no Capabilities at all until
		// 2026-08-20 — permission enforcement had nothing to gate on. The gap survived
		// because TestMaintenancePlugin_AllOpsHaveCapabilities was t.Skip'd.
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.auto-match-transcribed",
		Cancellable:     true,
		Timeout:         12 * time.Hour,
		Run:             p.runAutoMatchTranscribed,
	}
}

func (p *Plugin) runAutoMatchTranscribed(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	if !p.deps.HasMetadataFetchService() {
		return fmt.Errorf("metadata fetch service not initialized; cannot run auto-match-transcribed")
	}

	var params autoMatchTranscribedParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("maintenance.auto-match-transcribed: decode params: %w", err)
		}
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

	var scanned, eligible, applied, manualOnly int

	// Fail-safe cap (internal/applycap). This op walks the WHOLE library and
	// decides per book, so its target set is unknowable up front — a running
	// counter is the only place the cap can live. The (cap+1)th apply aborts
	// the op with an error (RunItems' default ErrModeFail cancels the rest);
	// everything admitted before it stays applied, and the checkpoint lets a
	// deliberate re-dispatch continue from there under a fresh cap.
	capCounter := applycap.NewCounter("maintenance.auto-match-transcribed", config.AppConfig.BulkApplyMaxItems)

	// Series names for the owner-manual pre-check, read once per run: the
	// op's store has no per-id series read, and a book marked only by its
	// series row ("Big Finish Main Range") must be skipped in a dry run as
	// the apply's check refuses it. A failed read stops the op (fail closed):
	// without it the pre-check cannot see that marker.
	allSeries, err := store.GetAllSeries()
	if err != nil {
		return fmt.Errorf("list series for the owner-manual check: %w", err)
	}
	seriesNames := make(seriesNameIndex, len(allSeries))
	for _, s := range allSeries {
		seriesNames[s.ID] = s.Name
	}

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
		// Fill-only (owner ruling 2026-09-14): this op may write only an empty
		// title or author. With both filled there is nothing it may write, so
		// skip before searching; the dry run must not count as eligible a book
		// the real run would refuse. ApplyTranscriptionCandidate re-checks per
		// field on the book as it reads it at apply time. It also refuses a
		// match with no value for the empty field, or with that field locked;
		// this pre-check cannot see either, so a dry run still counts those.
		if strings.TrimSpace(b.Title) != "" &&
			(b.AuthorID != nil || (b.Author != nil && strings.TrimSpace(b.Author.Name) != "")) {
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

		top, found, searchErr := p.deps.SearchTranscriptionCandidate(
			ctx, id, transTitle, transAuthor,
		)
		candTitle, candAuthor, score := top.Title, top.Author, top.Score
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

		// Gate 4: owner-manual-only (owner rule, standing). Doctor Who / Big
		// Finish / Torchwood are applied by hand, one book at a time, and this
		// op is a bulk apply: such a book is never written, found by its path,
		// title, transcribed title, the matched candidate, or any book_file
		// path, series row or the candidate's series. A blank-titled Big
		// Finish book whose intro names it is exactly the book the
		// transcription match would otherwise fill. A failed book_file read
		// refuses too (fail closed). It is the same check the server's
		// ApplyTranscriptionCandidate re-runs before it writes, so a dry run
		// and a real run skip the same books; a refusal there (the cache
		// changed since this read) is still counted below.
		guard := applygate.BulkManualOnlyGuard(store, seriesNames, b, transTitle)
		if reason, detail := applygate.ManualOnlyDetail(b,
			&metafetch.MetadataCandidate{Title: candTitle, Author: candAuthor, Series: top.Series},
			applygate.TranscribedSearch{Query: transTitle}, guard); reason != "" {
			manualOnly++
			log.Info("auto-match-transcribed: skipped, owner applies this book by hand",
				"book_id", id, "reason", reason, "detail", logger.SanitizeLogValue(detail))
			return nil
		}

		// All gates passed — this book is eligible.
		eligible++

		if dryRun {
			log.Info("auto-match-transcribed: would-apply",
				"book_id", id, "db_title", b.Title,
				"candidate", candTitle, "score", score)
			return nil // no mutation in dry-run
		}

		if capErr := capCounter.Admit(); capErr != nil {
			log.Error("auto-match-transcribed: bulk apply cap reached, stopping",
				"applied", applied, "cap", capCounter.Cap(), "next_book_id", id)
			return capErr
		}
		applyErr := p.deps.ApplyTranscriptionCandidate(ctx, id, candTitle, candAuthor)
		if errors.Is(applyErr, ErrTranscriptionNothingToFill) {
			// Nothing left to write (filled since the pre-check, or the match
			// has no value for the empty field, or it is locked): a skip, not
			// a failure, so it is not counted as eligible. Its cap slot stays
			// consumed: applycap.Counter has no release, and the apply is the
			// only place this is known.
			eligible--
			log.Info("auto-match-transcribed: skipped, nothing left to fill",
				"book_id", id, "candidate", candTitle)
			return nil
		}
		if errors.Is(applyErr, ErrTranscriptionOwnerManualOnly) {
			// The server's guard refused what the pre-check passed (the cache
			// or a series changed in between): a skip, not a failure.
			eligible--
			manualOnly++
			log.Info("auto-match-transcribed: skipped, owner applies this book by hand",
				"book_id", id, "detail", logger.SanitizeLogValue(applyErr.Error()))
			return nil
		}
		if applyErr != nil {
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
			"auto-match-transcribed complete (dry-run): scanned %d, eligible %d, would-apply %d, owner-manual skipped %d",
			scanned, eligible, eligible, manualOnly,
		)
	} else {
		summary = fmt.Sprintf(
			"auto-match-transcribed complete: scanned %d, eligible %d, applied %d, owner-manual skipped %d",
			scanned, eligible, applied, manualOnly,
		)
	}
	log.Info(summary)
	_ = reporter.UpdateProgress(total, total, summary)
	return nil
}

// seriesNameIndex is the run's series id -> name map, read once
// (GetAllSeries), as the applygate.ManualOnlySeriesReader the owner-manual
// pre-check needs. An id it does not hold is a series created after the read:
// nil, as the store answers for a missing row.
type seriesNameIndex map[int]string

// GetSeriesByID implements applygate.ManualOnlySeriesReader.
func (idx seriesNameIndex) GetSeriesByID(id int) (*database.Series, error) {
	name, ok := idx[id]
	if !ok {
		return nil, nil
	}
	return &database.Series{ID: id, Name: name}, nil
}
