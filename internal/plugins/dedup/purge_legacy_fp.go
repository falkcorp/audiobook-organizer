// file: internal/plugins/dedup/purge_legacy_fp.go
// version: 1.1.0
// guid: 3b7a2e9f-c1d4-4f8b-a5e0-6d3c8b2f1e47
// last-edited: 2026-07-06

// Package dedup — op dedup.purge-legacy-fp-candidates (T015, SPEC 1 §8 step 2).
//
// Why this op exists: before whole-file fingerprinting (cutover ~2026-05-12) the
// dedup engine produced ~12,320 exact-layer sim=1.0 candidates by comparing per-
// segment acoustid hashes rather than whole-file hashes. Those candidates are
// false positives — any two files that shared even one fingerprint segment were
// flagged as 100% identical. The whole-file migration made these unreliable, but
// they remain in the candidate store as "pending" and pollute review queues.
//
// This op marks them "stale-fp" (never deletes) after a per-candidate re-check:
// if a genuine whole-file hash equality can be established from current BookFiles,
// the candidate is preserved unchanged. Only candidates with NO recomputable file-
// hash match on either side are marked stale.
//
// Exclusion: the `acoustid` layer (2,591 pending rows from post-cutover May 31)
// represents CURRENT data and must never be touched by this op.
//
// The op defaults to dry-run; pass `{"apply":true}` in the params JSON to mark rows.
// A versioned flag `dedup_fp_purge_v1_done` prevents double-runs after completion.

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// purgeLegacyFPDoneFlag is the versioned key stored in Settings to prevent
// re-running the purge after it has completed with apply=true. Bump to v2 if
// purge criteria ever change and a re-run is required.
const purgeLegacyFPDoneFlag = "dedup_fp_purge_v1_done"

// purgeLegacyFPDefaultCutover is the default cutover date — the day after
// prod was observed to contain segment-era exact/embedding sim=1.0 candidates
// (created 2026-05-11). Candidates created on or after this date are skipped.
var purgeLegacyFPDefaultCutover = time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)

// purgeLegacyFPParams are the JSON parameters accepted by the op.
type purgeLegacyFPParams struct {
	// Apply, if true, performs the actual stale-fp marking. Default false
	// (dry-run) — the op only reports counts and logs what it would do.
	Apply bool `json:"apply"`
	// CutoverDate is the exclusive upper bound for CreatedAt. Candidates
	// with CreatedAt >= CutoverDate are considered current data and are not
	// touched. Defaults to purgeLegacyFPDefaultCutover.
	// Format: RFC3339, e.g. "2026-05-12T00:00:00Z".
	CutoverDate string `json:"cutover_date"`
}

// purgeLegacyFPDef returns the OperationDef for dedup.purge-legacy-fp-candidates.
func (p *Plugin) purgeLegacyFPDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "dedup.purge-legacy-fp-candidates",
		Plugin:      "dedup",
		DisplayName: "Purge legacy fingerprint candidates",
		Description: "Marks pre-whole-file-fingerprint exact/embedding sim=1.0 candidates as stale-fp. " +
			"Dry-run by default (pass apply=true to execute). " +
			"Skips acoustid-layer rows and any candidate whose files still share a whole-file hash. " +
			"Idempotent: a versioned flag prevents re-running after completion.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityHigh, // user-triggered, runs fast
		ConcurrencyKey:  "dedup.purge-legacy-fp-candidates",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
		},
		Run: p.runPurgeLegacyFP,
	}
}

// runPurgeLegacyFP implements the purge-legacy-fp-candidates op.
func (p *Plugin) runPurgeLegacyFP(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	if p.embeddingStore == nil {
		return fmt.Errorf("embedding store not available")
	}
	if p.store == nil {
		return fmt.Errorf("main store not available")
	}

	// --- Parse params ---
	var params purgeLegacyFPParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}

	cutover := purgeLegacyFPDefaultCutover
	if params.CutoverDate != "" {
		var err error
		cutover, err = time.Parse(time.RFC3339, params.CutoverDate)
		if err != nil {
			return fmt.Errorf("invalid cutover_date %q: must be RFC3339 (e.g. 2026-05-12T00:00:00Z): %w", params.CutoverDate, err)
		}
	}

	reporter.Logger().Info("purge-legacy-fp-candidates start",
		"apply", params.Apply,
		"cutover", cutover.Format(time.RFC3339))

	// --- Guard: skip if already completed with apply=true ---
	if params.Apply {
		if done, err := p.isFlagSet(purgeLegacyFPDoneFlag); err != nil {
			reporter.Logger().Warn("purge-legacy-fp: flag check error (proceeding)", "error", err)
		} else if done {
			reporter.Logger().Info("purge-legacy-fp: already completed; skipping (flag set)",
				"flag", purgeLegacyFPDoneFlag)
			_ = reporter.UpdateProgress(1, 1, "Already completed (flag set); nothing to do.")
			return nil
		}
	}

	// --- Build file-hash lookup from current BookFiles ---
	// We index by book ID → set of non-empty hashes so we can re-check whether
	// either side of a candidate still has an alive file with a matching hash.
	_ = reporter.UpdateProgress(0, 3, "Loading current book files for hash re-check…")
	filesByBookID, err := p.buildFileHashIndex(ctx)
	if err != nil {
		return fmt.Errorf("load book files: %w", err)
	}
	reporter.Logger().Info("purge-legacy-fp: loaded book file hash index", "books", len(filesByBookID))

	// --- Walk all candidates ---
	_ = reporter.UpdateProgress(1, 3, "Scanning candidates…")
	filter := database.CandidateFilter{
		Status: "pending",
		Limit:  1000000, // large upper bound — we filter below
	}
	candidates, _, err := p.embeddingStore.ListCandidates(filter)
	if err != nil {
		return fmt.Errorf("list candidates: %w", err)
	}

	var (
		examined    int
		staleCount  int
		keptHash    int
		keptLayer   int
		keptCutover int
	)

	// Collect IDs to mark in one pass, so we don't interleave reads and writes.
	var toMark []int64

	for _, cand := range candidates {
		if reporter.IsCanceled() {
			return context.Canceled
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		examined++

		// --- Criteria gate ---

		// 1. Only exact and embedding layers are legacy segment-era candidates.
		//    The acoustid layer is CURRENT data (post-cutover) and must not be touched.
		if cand.Layer != "exact" && cand.Layer != "embedding" {
			keptLayer++
			continue
		}

		// 2. Must have sim == 1.0 — these are the segment-era perfect-match false positives.
		if cand.Similarity == nil || *cand.Similarity != 1.0 {
			continue
		}

		// 3. Must have been created before the whole-file cutover date.
		if !cand.CreatedAt.Before(cutover) {
			keptCutover++
			continue
		}

		// 4. Re-check: does either side still have a recomputable whole-file hash match?
		//    If yes, this is a genuine exact duplicate — preserve it.
		if p.hasFileHashMatch(cand.EntityAID, cand.EntityBID, filesByBookID) {
			keptHash++
			reporter.Logger().Debug("purge-legacy-fp: keeping genuine hash-dupe",
				"candidate_id", cand.ID,
				"entity_a", cand.EntityAID,
				"entity_b", cand.EntityBID)
			continue
		}

		// Candidate is stale.
		staleCount++
		toMark = append(toMark, cand.ID)
	}

	reporter.Logger().Info("purge-legacy-fp: scan complete",
		"examined", examined,
		"stale", staleCount,
		"kept_genuine_hash", keptHash,
		"kept_wrong_layer", keptLayer,
		"kept_post_cutover", keptCutover,
		"apply", params.Apply)

	summary := fmt.Sprintf(
		"Scan: %d examined, %d stale-fp, %d kept (genuine hash), %d kept (other layer), %d kept (post-cutover)",
		examined, staleCount, keptHash, keptLayer, keptCutover,
	)

	if !params.Apply {
		_ = reporter.UpdateProgress(3, 3,
			fmt.Sprintf("Dry-run complete — %d candidates would be marked stale-fp. Pass apply=true to execute.", staleCount))
		reporter.Logger().Info("purge-legacy-fp: dry-run only; no changes written", "would_mark", staleCount)
		return nil
	}

	// --- Apply: mark stale rows ---
	_ = reporter.UpdateProgress(2, 3, fmt.Sprintf("Marking %d candidates stale-fp…", len(toMark)))

	var marked int
	for _, id := range toMark {
		if reporter.IsCanceled() {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := p.embeddingStore.UpdateCandidateStatus(id, "stale-fp"); err != nil {
			reporter.Logger().Error("purge-legacy-fp: mark stale-fp error", "candidate_id", id, "error", err)
			// Continue — partial progress is better than aborting; the op is idempotent.
		} else {
			marked++
		}
	}

	// --- Set versioned completion flag ---
	if err := p.store.SetSetting(purgeLegacyFPDoneFlag, "true", "bool", false); err != nil {
		reporter.Logger().Warn("purge-legacy-fp: could not set done flag", "flag", purgeLegacyFPDoneFlag, "error", err)
	} else {
		reporter.Logger().Info("purge-legacy-fp: set done flag", "flag", purgeLegacyFPDoneFlag)
	}

	_ = reporter.UpdateProgress(3, 3,
		fmt.Sprintf("Complete — %d/%d marked stale-fp. %s", marked, staleCount, summary))
	reporter.Logger().Info("purge-legacy-fp: complete", "marked", marked, "intended", staleCount)
	return nil
}

// buildFileHashIndex returns a map from bookID → set of non-empty file hashes
// for all current BookFiles. This is used to re-verify whether a candidate pair
// still has a genuine whole-file hash match before marking it stale.
func (p *Plugin) buildFileHashIndex(_ context.Context) (map[string]map[string]struct{}, error) {
	files, err := p.store.GetAllBookFilesCore()
	if err != nil {
		return nil, err
	}
	// Index: bookID → set of whole-file hashes (FileHash and OriginalFileHash).
	idx := make(map[string]map[string]struct{}, len(files)/4)
	for _, f := range files {
		if f.BookID == "" {
			continue
		}
		if idx[f.BookID] == nil {
			idx[f.BookID] = make(map[string]struct{})
		}
		if f.FileHash != "" {
			idx[f.BookID][f.FileHash] = struct{}{}
		}
		if f.OriginalFileHash != "" {
			idx[f.BookID][f.OriginalFileHash] = struct{}{}
		}
	}
	return idx, nil
}

// hasFileHashMatch returns true when at least one file hash is shared between
// any file belonging to bookAID and any file belonging to bookBID. A true
// result means the candidate is a genuine hash-based exact duplicate and should
// NOT be marked stale.
func (p *Plugin) hasFileHashMatch(bookAID, bookBID string, idx map[string]map[string]struct{}) bool {
	hashesA := idx[bookAID]
	hashesB := idx[bookBID]
	if len(hashesA) == 0 || len(hashesB) == 0 {
		// If either book has no indexed files we cannot confirm the match, so
		// treat conservatively: do NOT keep (the candidate is stale by absence).
		return false
	}
	for h := range hashesA {
		if _, ok := hashesB[h]; ok {
			return true
		}
	}
	return false
}

// isFlagSet checks whether the named Settings key holds "true".
func (p *Plugin) isFlagSet(key string) (bool, error) {
	setting, err := p.store.GetSetting(key)
	if err != nil {
		return false, err
	}
	if setting == nil {
		return false, nil
	}
	return setting.Value == "true", nil
}
