// file: internal/server/handlers/dedup/label_capture.go
// version: 1.2.0
// guid: 7c1d9e42-3a8b-4f60-9c21-5e0a7b2d6f48
// last-edited: 2026-07-13

package deduphandler

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/dataset"
)

// Human-decision dedup labels. When a user merges or dismisses a candidate we
// capture the decision as a gold ground-truth LabeledExample (label_source =
// labelSourceHuman). These are the labels the dedup tuning classifier trains and
// validates on — see [[project-dedup-tuning]]. Capture is always best-effort: a
// failure is logged and swallowed so it can never block the user's action.
const (
	labelTrueDup = "true_dup"
	labelNotDup  = "not_dup"

	labelSourceHuman = "human"

	labelReasonUserMerge   = "user_merge"
	labelReasonUserDismiss = "user_dismiss"
)

// builderStoreAdapter bridges the handler's DedupStore (GetBookByID) onto
// dataset.BuilderStore (GetBook), which the feature builder expects. Mirrors the
// builderAdapter in the dedup.dataset-backfill op so the feature snapshot is
// computed identically here and in the backfill.
type builderStoreAdapter struct{ s DedupStore }

func (b builderStoreAdapter) GetBook(id string) (*database.Book, error) { return b.s.GetBookByID(id) }
func (b builderStoreAdapter) GetBookFiles(id string) ([]database.BookFile, error) {
	return b.s.GetBookFiles(id)
}

// snapshotCandidateExample builds the feature snapshot for a candidate pair.
//
// For a MERGE it MUST be called BEFORE the merge executes: a merge absorbs and
// deletes one side, after which BuildExample can no longer load that book and the
// snapshot would fail. For a dismiss both books still exist, so timing is free.
//
// Best-effort: returns nil (after logging) on any failure so the caller's
// merge/dismiss flow is never blocked.
func (h *Handler) snapshotCandidateExample(cand *database.DedupCandidate) *database.LabeledExample {
	if cand == nil {
		return nil
	}
	store := h.store
	if store == nil {
		slog.Warn("dedup label capture: no store; skipping snapshot", "candidate_id", cand.ID)
		return nil
	}
	ex, err := dataset.BuildExample(builderStoreAdapter{store}, *cand)
	if err != nil {
		slog.Warn("dedup label capture: build example failed",
			"candidate_id", cand.ID, "entity_a", cand.EntityAID, "entity_b", cand.EntityBID, "error", err)
		return nil
	}
	return &ex
}

// recordHumanLabel finalizes a pre-built example with a human ground-truth label
// and persists it to the dedup:label keyspace. Best-effort: a nil example (the
// snapshot failed upstream) or a write error is logged and swallowed.
func (h *Handler) recordHumanLabel(ex *database.LabeledExample, label, reason string) {
	if ex == nil {
		return
	}
	es := h.embeddingStore
	if es == nil {
		slog.Warn("dedup label capture: no embedding store; dropping label", "candidate_id", ex.CandidateID)
		return
	}
	ex.Label = label
	ex.LabelSource = labelSourceHuman
	ex.LabelReason = reason
	ex.DecidedAt = time.Now().UTC().Format(time.RFC3339)
	// (Re)snapshot the pair's current ScoreBreakdown onto this example BEFORE the
	// single upsert, so a dismissed/below-band pair still carries a persisted
	// breakdown for calibration coverage. Best-effort: touches only the score
	// fields and never blocks the label write. Background context — this gold
	// capture should complete regardless of the request's own cancellation.
	h.refreshExampleBreakdown(context.Background(), ex)
	if err := es.UpsertLabeledExample(*ex); err != nil {
		slog.Warn("dedup label capture: upsert failed", "candidate_id", ex.CandidateID, "error", err)
		return
	}
	slog.Info("dedup label capture: recorded human label",
		"candidate_id", ex.CandidateID, "label", label, "reason", reason,
		"entity_a", ex.EntityAID, "entity_b", ex.EntityBID)
}

// captureHumanLabelByID fetches the candidate, snapshots it, and records the label
// in one best-effort call. Use this ONLY for dismiss-style actions where both books
// still exist; for merges, snapshot before the merge and recordHumanLabel after.
func (h *Handler) captureHumanLabelByID(candidateID int64, label, reason string) {
	es := h.embeddingStore
	if es == nil {
		return
	}
	cand, err := es.GetCandidateByID(candidateID)
	if err != nil || cand == nil {
		slog.Warn("dedup label capture: candidate lookup failed", "candidate_id", candidateID, "error", err)
		return
	}
	h.recordHumanLabel(h.snapshotCandidateExample(cand), label, reason)
}

// refreshExampleBreakdown recomputes the pair's current ScoreBreakdown with the
// engine's shared scorer (Engine.ScorePairsForBook — the same collectors +
// unified.ComposeScore the operational scan uses, no fork) and narrow-writes ONLY
// ex.Score / ex.ScoreBreakdown / ex.Band in place. It is the ongoing counterpart
// to the one-shot dedup.rescore-labeled-examples backfill: without it, dismissing
// or relabeling a pair rewrites the label but never re-snapshots its breakdown
// (engine.upsertCandidateWithLiveLabel captures only brand-new pairs), so the
// calibration gold set slowly rots back to no-coverage as new labels accrue.
//
// Contract:
//   - Best-effort: any failure (nil engine, nil book, score error, marshal error)
//     is logged at debug and swallowed — the caller's label write MUST still
//     proceed. A user dismissing a pair always succeeds even if rescoring hiccups.
//   - Below-band pairs are persisted: ScorePairsForBook does NOT drop below-band
//     scores, and this method persists whenever a score with >=1 signal is
//     produced — those low-scoring negatives ARE the calibration signal.
//   - Zero-signal / merge-deleted-book pairs no-op cleanly (nil result or
//     NumSignals==0), so no bogus breakdown is ever written.
//   - Data safety: it touches ONLY the three score fields. Label, LabelSource
//     (esp. "human"), LabelReason, DecidedAt and every other field the caller
//     just set are left exactly as they are.
//
// EmbeddingCos is sourced from the example's stored Similarity ONLY when its Layer
// is "embedding" — identical to dedup.rescore-labeled-examples, so the two paths
// reconstruct the embedding signal the same way and stay bit-consistent.
func (h *Handler) refreshExampleBreakdown(ctx context.Context, ex *database.LabeledExample) {
	if ex == nil || h.dedupEngine == nil {
		return
	}
	if ex.EntityAID == "" || ex.EntityBID == "" {
		return
	}

	in := dedup.RescorePairInput{OtherID: ex.EntityBID}
	if ex.Layer == "embedding" && ex.Similarity != nil {
		cos := *ex.Similarity
		in.EmbeddingCos = &cos
	}

	results, err := h.dedupEngine.ScorePairsForBook(ctx, ex.EntityAID, []dedup.RescorePairInput{in})
	if err != nil {
		slog.Debug("dedup label capture: rescore failed",
			"candidate_id", ex.CandidateID, "entity_a", ex.EntityAID, "entity_b", ex.EntityBID, "error", err)
		return
	}
	if len(results) == 0 || results[0].Score == nil || results[0].NumSignals == 0 {
		// Zero-signal / unscorable / merge-deleted-book pair — never persist a
		// bogus (empty) breakdown; leave whatever the example already carried.
		return
	}

	sc := results[0].Score
	raw, mErr := json.Marshal(sc)
	if mErr != nil {
		slog.Debug("dedup label capture: marshal score failed", "candidate_id", ex.CandidateID, "error", mErr)
		return
	}
	// Narrow write: ONLY the score fields.
	ex.Score = sc.Score
	ex.ScoreBreakdown = raw
	ex.Band = sc.Band
}
