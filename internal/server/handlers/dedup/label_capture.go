// file: internal/server/handlers/dedup/label_capture.go
// version: 1.1.0
// guid: 7c1d9e42-3a8b-4f60-9c21-5e0a7b2d6f48
// last-edited: 2026-06-23

package deduphandler

import (
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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
