// file: internal/dedup/auto_resolve.go
// version: 1.1.0
// guid: 6d1e9b52-4f70-4c83-a2b9-1e5c8d0f7a34
// last-edited: 2026-07-13

package dedup

import (
	"context"
	"fmt"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"log/slog"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// autoResolveSampleCapDefault bounds the per-run sample list in the dry-run
// (and apply) report so the payload stays small regardless of corpus size.
const autoResolveSampleCapDefault = 50

// autoResolveMaxMergesDefault is the default hard cap on merges performed in a
// single apply=true call.
const autoResolveMaxMergesDefault = 200

// autoResolvePrimaryKinds is the allow-list of PRIMARY signal kinds that count
// toward the "≥2 distinct corroborating signal kinds" Tier-1 eligibility rule.
// Supporting kinds (duration, folder) and the weaker embedding/fuzzy kinds are
// intentionally excluded — a CERTAIN band alone is not sufficient; the design
// (02-dedup, CONS-10 lesson) requires two independent strong signals or a
// whole-book-signature true_dup label.
var autoResolvePrimaryKinds = map[unified.SignalKind]bool{
	unified.SigExactFile:     true,
	unified.SigExactAcoustID: true,
	unified.SigISBNASIN:      true,
	unified.SigMetaSrcHash:   true,
}

// AutoResolveResult is the report returned by AutoResolveCertain. In dry-run
// mode Merged is 0 and Samples enumerates the eligible pairs that WOULD merge.
type AutoResolveResult struct {
	Checked    int                 `json:"checked"`     // CERTAIN pending candidates examined
	Eligible   int                 `json:"eligible"`    // passed every Tier-1 guard
	Merged     int                 `json:"merged"`      // merges performed (0 when dry-run)
	SkippedCap int                 `json:"skipped_cap"` // eligible pairs left unmerged because max_merges was hit
	DryRun     bool                `json:"dry_run"`
	Samples    []AutoResolveSample `json:"samples"`
}

// AutoResolveSample is one eligible candidate, for the dry-run/apply report.
type AutoResolveSample struct {
	CandidateID int64  `json:"candidate_id"`
	BookAID     string `json:"book_a_id"`
	BookBID     string `json:"book_b_id"`
	TitleA      string `json:"title_a"`
	TitleB      string `json:"title_b"`
	Band        string `json:"band"`
	Reason      string `json:"reason"` // why this pair is Tier-1 eligible
	Merged      bool   `json:"merged"` // true only on the apply path once the merge succeeded
	WinnerID    string `json:"winner_id,omitempty"`
}

// AutoResolveCertain runs the Tier-1 (Band CERTAIN) auto-resolution pass.
//
// Dry-run (apply=false, the default caller contract) never calls MergeBooks: it
// counts eligible pairs and returns a capped sample with per-pair reasons. This
// always works regardless of the AutoResolveEnabled kill switch so an operator
// can produce the audit report before anything is greenlit.
//
// Apply (apply=true) requires config.AppConfig.Dedup.AutoResolveEnabled to be
// true — otherwise it returns an error with zero merges. Eligible pairs are
// merged via the existing mergeService.MergeBooks + CleanupCandidatesAfterMerge
// path, the survivor is tagged dedup:merge-survivor:auto-certain, and a reversal
// journal entry is written per merge. The run stops after maxMerges merges and
// reports how many eligible pairs were skipped due to the cap.
func (de *Engine) AutoResolveCertain(ctx context.Context, apply bool, maxMerges, sampleCap int) (AutoResolveResult, error) {
	if sampleCap <= 0 {
		sampleCap = autoResolveSampleCapDefault
	}
	if maxMerges <= 0 {
		maxMerges = autoResolveMaxMergesDefault
	}

	res := AutoResolveResult{DryRun: !apply}

	if de == nil || de.embedStore == nil || de.bookStore == nil {
		return res, fmt.Errorf("auto-resolve: dedup engine not fully initialised (embed/book store nil)")
	}

	// Apply path is gated behind the global kill switch. This check lives here
	// in the engine (not only the op wrapper) so calling the method directly
	// cannot bypass the greenlight gate. Dry-run is exempt.
	if apply && !config.AppConfig.Dedup.AutoResolveEnabled {
		return res, fmt.Errorf("auto-resolve: apply=true requested but dedup.auto_resolve_enabled is false — owner greenlight required")
	}
	if apply && de.mergeService == nil {
		return res, fmt.Errorf("auto-resolve: apply=true requested but merge service is unavailable (embeddings/merge disabled in this build)")
	}

	candidates, _, err := de.embedStore.ListCandidates(database.CandidateFilter{
		EntityType: "book",
		Status:     "pending",
		Band:       unified.BandCertain,
		Limit:      1_000_000,
	})
	if err != nil {
		return res, fmt.Errorf("auto-resolve: list candidates: %w", err)
	}

	for _, c := range candidates {
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		default:
		}
		res.Checked++

		bookA, errA := de.bookStore.GetBookByID(c.EntityAID)
		bookB, errB := de.bookStore.GetBookByID(c.EntityBID)
		if errA != nil || errB != nil || bookA == nil || bookB == nil {
			// Conservative: cannot judge a pair whose books we can't load.
			continue
		}
		// Skip a pair whose book was already merged away earlier in this same
		// run (candidates are snapshotted once up front; a shared book across
		// two CERTAIN pairs would otherwise be double-merged). GetBookByID does
		// not filter soft-deleted rows, so check the flag explicitly.
		if bookSoftDeleted(bookA) || bookSoftDeleted(bookB) {
			continue
		}

		eligible, reason := de.autoResolveEligible(c, bookA, bookB)
		if !eligible {
			continue
		}
		res.Eligible++

		sample := AutoResolveSample{
			CandidateID: c.ID,
			BookAID:     c.EntityAID,
			BookBID:     c.EntityBID,
			TitleA:      bookA.Title,
			TitleB:      bookB.Title,
			Band:        c.Band,
			Reason:      reason,
		}

		if !apply {
			if len(res.Samples) < sampleCap {
				res.Samples = append(res.Samples, sample)
			}
			continue
		}

		// Apply path.
		if res.Merged >= maxMerges {
			res.SkippedCap++
			continue
		}

		winnerID, mergeErr := de.autoMergeCertain(c)
		if mergeErr != nil {
			logging.Error(ctx, "dedup auto-resolve merge failed",
				"candidate", c.ID, "a", c.EntityAID, "b", c.EntityBID, "err", mergeErr)
			continue
		}
		res.Merged++
		sample.Merged = true
		sample.WinnerID = winnerID
		if len(res.Samples) < sampleCap {
			res.Samples = append(res.Samples, sample)
		}
	}

	logging.Info(ctx, "dedup auto-resolve pass complete",
		"dry_run", res.DryRun, "checked", res.Checked, "eligible", res.Eligible,
		"merged", res.Merged, "skipped_cap", res.SkippedCap)
	return res, nil
}

// autoResolveEligible implements the Tier-1 eligibility rules (02-dedup design).
// Returns (true, humanReason) only when EVERY guard holds. The reason string is
// surfaced in the audit sample so an operator can see why each pair qualified.
func (de *Engine) autoResolveEligible(c database.DedupCandidate, bookA, bookB *database.Book) (bool, string) {
	if c.Band != unified.BandCertain {
		return false, "band is not CERTAIN"
	}
	// A nil breakdown is a pre-T015 legacy row with nothing to audit — never
	// auto-merge it.
	if c.ScoreBreakdown == nil {
		return false, "no score breakdown (legacy row)"
	}
	if len(c.ScoreBreakdown.Suppressors) > 0 {
		return false, "score has active suppressors: " + strings.Join(c.ScoreBreakdown.Suppressors, ",")
	}

	// Both sides must look like real audio, and their identifiers must not
	// actively disagree. These are independent of the signal-corroboration
	// check and are kept as separate guards.
	if !hasPlausibleAudio(bookA) || !hasPlausibleAudio(bookB) {
		return false, "at least one side lacks plausible audio"
	}
	if identifiersConflict(bookA, bookB) {
		return false, "identifiers conflict between the two books"
	}

	// Corroboration: ≥2 distinct primary signal kinds with Confidence>0, OR a
	// whole-book-signature true_dup labeled example.
	distinct := map[unified.SignalKind]bool{}
	for _, sig := range c.ScoreBreakdown.Signals {
		if sig.Confidence > 0 && autoResolvePrimaryKinds[sig.Kind] {
			distinct[sig.Kind] = true
		}
	}
	if len(distinct) >= 2 {
		kinds := make([]string, 0, len(distinct))
		for k := range distinct {
			kinds = append(kinds, string(k))
		}
		return true, fmt.Sprintf("%d distinct primary signals: %s", len(distinct), strings.Join(kinds, "+"))
	}

	// Fallback: a whole-book-signature oracle label. Reuse the already-labeled
	// example — do not recompute. GetLabeledExample errors are non-fatal: log
	// and fall through to the (failed) count-based decision.
	if ex, err := de.embedStore.GetLabeledExample(c.ID); err != nil {
		slog.Warn("dedup auto-resolve: GetLabeledExample failed", "candidate", c.ID, "err", err)
	} else if ex != nil && ex.Label == "true_dup" && strings.Contains(ex.LabelReason, "whole-book signatures match") {
		return true, "whole-book-signature true_dup label"
	}

	return false, fmt.Sprintf("insufficient corroboration (%d primary signal kind(s), no whole-book-signature label)", len(distinct))
}

// autoMergeCertain performs a single Tier-1 merge for an eligible candidate,
// mirroring Engine.ApplyVerdicts' auto-merge shape and additionally: cleaning up
// residual candidates, and writing a reversal journal entry. Returns the
// surviving primary book ID.
//
// Reversibility rail (must exist BEFORE the destructive act): a PROVISIONAL
// journal entry — candidate id + the predicted winner/loser — is written before
// MergeBooks. A provisional-write failure is a HARD error for this pair: the
// merge is skipped so we never perform an irreversible merge with no undo key. A
// crash between the two writes leaves the provisional breadcrumb (candidate +
// both book IDs) pointing at the pair; MergeBooks' own book_ver snapshots are on
// disk, so an operator can still recover. After the merge succeeds we PATCH the
// SAME journal key with the authoritative winner/loser + snapshot timestamps; a
// patch failure is logged (the provisional entry already stands) rather than
// unwinding a completed merge.
func (de *Engine) autoMergeCertain(c database.DedupCandidate) (string, error) {
	// Capture the newest pre-existing book_ver snapshot timestamp for each side
	// BEFORE the merge. MergeBooks calls UpdateBook for every book (which writes
	// the pre-update state to book_ver), so the earliest snapshot NEWER than
	// this baseline is the genuine pre-merge record — the loser gets a second
	// (soft-delete) snapshot on top, which is why "newest after merge" is wrong.
	baseA := de.newestSnapshotNanos(c.EntityAID)
	baseB := de.newestSnapshotNanos(c.EntityBID)

	// Predict the winner the SAME way MergeBooks will (auto-pick via
	// merge.BookIsBetter over [EntityAID, EntityBID] — books[0] == EntityAID,
	// so A wins unless B is strictly better). This is only for the provisional
	// journal record; MergeBooks re-derives the authoritative winner and we
	// overwrite the entry with it afterward, so there is no semantic drift.
	bookA, errA := de.bookStore.GetBookByID(c.EntityAID)
	bookB, errB := de.bookStore.GetBookByID(c.EntityBID)
	if errA != nil || errB != nil || bookA == nil || bookB == nil {
		return "", fmt.Errorf("load books before merge (a=%v b=%v): %v/%v", c.EntityAID, c.EntityBID, errA, errB)
	}
	predWinner, predLoser := c.EntityAID, c.EntityBID
	if merge.BookIsBetter(bookB, bookA) {
		predWinner, predLoser = c.EntityBID, c.EntityAID
	}

	tag := "dedup:merge-survivor:auto-certain"
	// mergedAt fixes the journal key so the post-merge patch overwrites the same
	// entry instead of creating a second one.
	mergedAt := time.Now().UnixNano()
	provisional := database.AutoMergeJournalEntry{
		CandidateID: c.ID,
		WinnerID:    predWinner,
		LoserID:     predLoser,
		Tag:         tag,
		MergedAt:    mergedAt,
		// Pre-merge snapshot timestamps are unknown until MergeBooks writes
		// them; patched in after the merge.
	}
	if _, err := de.embedStore.PutAutoMergeJournalEntry(provisional); err != nil {
		// HARD error: do not perform an irreversible merge with no journal.
		return "", fmt.Errorf("write provisional journal entry (merge skipped): %w", err)
	}

	result, mergeErr := de.mergeService.MergeBooks(
		[]string{c.EntityAID, c.EntityBID},
		"", // auto-pick primary via bookIsBetter
	)
	if mergeErr != nil {
		return "", fmt.Errorf("merge books: %w", mergeErr)
	}
	if result == nil || result.PrimaryID == "" {
		return "", fmt.Errorf("merge returned no primary id")
	}

	winnerID := result.PrimaryID
	loserID := c.EntityAID
	if loserID == winnerID {
		loserID = c.EntityBID
	}

	// Mark the candidate merged so it drops off the pending/review tab.
	if err := de.embedStore.UpdateCandidateStatus(c.ID, "merged"); err != nil {
		slog.Error("dedup auto-resolve: mark candidate merged failed", "candidate", c.ID, "err", err)
	}

	// Tag the survivor with the auto-certain provenance suffix (distinct from
	// the LLM-verdict :llm-auto suffix so the two tiers are filterable apart).
	if err := database.EnsureSingletonBookTag(
		de.bookStore, winnerID, "dedup:merge-survivor", tag, "system",
	); err != nil {
		slog.Error("dedup auto-resolve: tag survivor failed", "winner", winnerID, "err", err)
	}

	// Clean up residual pending candidates referencing either merged-away book
	// so they don't drift into the stale-candidate backlog.
	de.CleanupCandidatesAfterMerge([]string{loserID})

	// Locate the pre-merge snapshots and PATCH the provisional journal entry
	// (same MergedAt key) with the authoritative winner/loser + timestamps.
	winnerTS := de.preMergeSnapshotNanos(winnerID, baselineFor(winnerID, c.EntityAID, baseA, baseB))
	loserTS := de.preMergeSnapshotNanos(loserID, baselineFor(loserID, c.EntityAID, baseA, baseB))

	entry := database.AutoMergeJournalEntry{
		CandidateID:      c.ID,
		WinnerID:         winnerID,
		LoserID:          loserID,
		WinnerPreMergeTS: winnerTS,
		LoserPreMergeTS:  loserTS,
		Tag:              tag,
		MergedAt:         mergedAt,
	}
	if _, err := de.embedStore.PutAutoMergeJournalEntry(entry); err != nil {
		// The provisional entry already provides a reversibility breadcrumb;
		// the merge is complete, so log rather than fail the (done) merge.
		slog.Error("dedup auto-resolve: patch journal entry failed (provisional entry stands)", "candidate", c.ID, "err", err)
	}

	slog.Info("dedup auto-resolve merged CERTAIN pair",
		"candidate", c.ID, "winner", winnerID, "loser", loserID)
	return winnerID, nil
}

// bookSoftDeleted reports whether a book has been soft-deleted (marked for
// deletion) — e.g. as a merge loser. GetBookByID returns such rows unfiltered.
func bookSoftDeleted(b *database.Book) bool {
	return b.IsSoftDeleted()
}

// baselineFor picks the correct pre-merge baseline nanos for a book ID given the
// two baselines captured for EntityAID / EntityBID.
func baselineFor(bookID, entityAID string, baseA, baseB int64) int64 {
	if bookID == entityAID {
		return baseA
	}
	return baseB
}

// newestSnapshotNanos returns the UnixNano of the newest existing book_ver
// snapshot for a book, or 0 if none exists.
func (de *Engine) newestSnapshotNanos(bookID string) int64 {
	snaps, err := de.bookStore.GetBookSnapshots(bookID, 1)
	if err != nil || len(snaps) == 0 {
		return 0
	}
	return snaps[0].Timestamp.UnixNano()
}

// preMergeSnapshotNanos returns the UnixNano of the earliest book_ver snapshot
// strictly newer than baselineNanos — i.e. the pre-merge book record written by
// MergeBooks' first UpdateBook call. Returns 0 if none is found.
func (de *Engine) preMergeSnapshotNanos(bookID string, baselineNanos int64) int64 {
	snaps, err := de.bookStore.GetBookSnapshots(bookID, 0)
	if err != nil {
		slog.Warn("dedup auto-resolve: GetBookSnapshots failed", "book", bookID, "err", err)
		return 0
	}
	// GetBookSnapshots returns newest-first; walk oldest-first to find the
	// earliest snapshot newer than the baseline.
	var best int64
	for i := len(snaps) - 1; i >= 0; i-- {
		ns := snaps[i].Timestamp.UnixNano()
		if ns > baselineNanos {
			best = ns
			break
		}
	}
	return best
}

// UnmergeAuto reverses a Tier-1 auto-merge recorded in the journal at journalKey
// by reverting both the winner and loser books to their pre-merge book_ver
// snapshots (restoring IsPrimaryVersion / VersionGroupID / MarkedForDeletion).
//
// SCOPE LIMIT: this restores the BOOK RECORD state only. It does NOT reverse the
// external-ID reassignment (loser→winner) that MergeBooks performed, nor any
// enqueued iTunes write-back removals. It exists so the "reversibility" safety
// rail is buildable ahead of any human sign-off; a follow-on task wires it into
// an admin "undo merge" endpoint that can also handle external-ID restoration.
// It is deliberately NOT called by AutoResolveCertain / the op path.
func (de *Engine) UnmergeAuto(journalKey string) error {
	if de == nil || de.embedStore == nil || de.bookStore == nil {
		return fmt.Errorf("unmerge-auto: engine not fully initialised")
	}
	entry, err := de.embedStore.GetAutoMergeJournalEntry(journalKey)
	if err != nil {
		return fmt.Errorf("unmerge-auto: read journal: %w", err)
	}
	if entry == nil {
		return fmt.Errorf("unmerge-auto: no journal entry at %s", journalKey)
	}

	var errs []string
	if entry.LoserPreMergeTS != 0 {
		if _, err := de.bookStore.RevertBookToVersion(entry.LoserID, time.Unix(0, entry.LoserPreMergeTS)); err != nil {
			errs = append(errs, fmt.Sprintf("loser %s: %v", entry.LoserID, err))
		}
	} else {
		errs = append(errs, fmt.Sprintf("loser %s: no pre-merge snapshot recorded", entry.LoserID))
	}
	if entry.WinnerPreMergeTS != 0 {
		if _, err := de.bookStore.RevertBookToVersion(entry.WinnerID, time.Unix(0, entry.WinnerPreMergeTS)); err != nil {
			errs = append(errs, fmt.Sprintf("winner %s: %v", entry.WinnerID, err))
		}
	} else {
		errs = append(errs, fmt.Sprintf("winner %s: no pre-merge snapshot recorded", entry.WinnerID))
	}

	if len(errs) > 0 {
		return fmt.Errorf("unmerge-auto: %s", strings.Join(errs, "; "))
	}
	slog.Info("dedup auto-resolve unmerged", "journal", journalKey,
		"winner", entry.WinnerID, "loser", entry.LoserID)
	return nil
}
