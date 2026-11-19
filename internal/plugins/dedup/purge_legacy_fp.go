// file: internal/plugins/dedup/purge_legacy_fp.go
// version: 1.5.0
// guid: 3b7a2e9f-c1d4-4f8b-a5e0-6d3c8b2f1e47
// last-edited: 2026-11-18

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
// Review-queue-only pairs (dedup/automated_guard.go) are never marked either:
// a manual (human-pinned) candidate, and a same-path pair (two book rows at one
// cleaned file path, dedup.SamePathPair). The same-path check needs the books'
// paths, which the op loads once, in bulk, for the candidates that pass every
// other gate.
//
// The op defaults to dry-run; pass `{"apply":true}` in the params JSON to mark rows.
// A versioned flag `dedup_fp_purge_v1_done` prevents double-runs after completion.

package dedup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
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
		ID:              "dedup.purge-legacy-fp-candidates",
		Preview:         sdk.PreviewSpec{Params: purgeLegacyFPParams{}},
		Liveness:        sdk.LivenessManual,
		Plugin:          "dedup",
		DisplayName:     "Purge legacy fingerprint candidates",
		Description:     purgeLegacyFPHelp,
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityHigh, // user-triggered, runs fast
		NoWarmupWait:    true,             // so it does not wait out the post-restart warmup
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

// purgeLegacyFPKind is rule R3's native kind for dedup.purge-legacy-fp-candidates.
const purgeLegacyFPKind = tasks.Kind("books.dedup.purge-legacy-fp-candidates")

// purgeLegacyFPHelp is HEAD's Description and the native Help (rule R5).
const purgeLegacyFPHelp = "Marks pre-whole-file-fingerprint exact/embedding sim=1.0 candidates as stale-fp. " +
	"Dry-run by default (pass apply=true to execute). " +
	"Skips acoustid-layer rows and any candidate whose files still share a whole-file hash. " +
	"Idempotent: a versioned flag prevents re-running after completion."

// purgeLegacyFPStore is the reads the core takes.
type purgeLegacyFPStore interface {
	ListCandidates(f database.CandidateFilter) ([]database.DedupCandidate, int, error)
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetBookByID(id string) (*database.Book, error)
	GetSetting(key string) (*database.Setting, error)
}

// purgeLegacyFPWriter is the writes the core makes: the guarded status flip and
// the completion flag.
type purgeLegacyFPWriter interface {
	ReclassifyCandidate(id int64, fromStatus, toStatus string) error
	SetSetting(key, value, dataType string, internal bool) error
}

// purgeLegacyFPEnv is what the core takes from its shell. Live is HEAD's
// apply=true; a dry run never calls Writer, which may then be nil.
type purgeLegacyFPEnv struct {
	kindEnv
	Store   purgeLegacyFPStore
	Writer  purgeLegacyFPWriter
	Cutover time.Time
}

// purgeLegacyFPResult is the native run's result. HEAD stored none.
type purgeLegacyFPResult struct {
	Examined       int  `json:"examined"`
	Stale          int  `json:"stale"`
	KeptHash       int  `json:"kept_genuine_hash"`
	KeptLayer      int  `json:"kept_wrong_layer"`
	KeptCutover    int  `json:"kept_post_cutover"`
	KeptManual     int  `json:"kept_manual"`
	KeptSamePath   int  `json:"kept_same_path"`
	Marked         int  `json:"marked"`
	SkippedAtWrite int  `json:"skipped_at_write"`
	AlreadyDone    bool `json:"already_done,omitempty"`
}

// headPurgeLegacyFP is HEAD's reads and writes for the core: the embedding store
// for the candidates and the main store for books, files and the flag.
type headPurgeLegacyFP struct {
	emb   *database.EmbeddingStore
	store pluginStore
}

func (h headPurgeLegacyFP) ListCandidates(f database.CandidateFilter) ([]database.DedupCandidate, int, error) {
	return h.emb.ListCandidates(f)
}
func (h headPurgeLegacyFP) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	return h.store.GetAllBookFilesCore()
}
func (h headPurgeLegacyFP) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	return h.store.GetAllBooksCore(limit, offset)
}
func (h headPurgeLegacyFP) GetBookByID(id string) (*database.Book, error) {
	return h.store.GetBookByID(id)
}
func (h headPurgeLegacyFP) GetSetting(key string) (*database.Setting, error) {
	return h.store.GetSetting(key)
}
func (h headPurgeLegacyFP) ReclassifyCandidate(id int64, fromStatus, toStatus string) error {
	return h.emb.ReclassifyCandidate(id, fromStatus, toStatus)
}
func (h headPurgeLegacyFP) SetSetting(key, value, dataType string, internal bool) error {
	return h.store.SetSetting(key, value, dataType, internal)
}

// purgeLegacyFPCutover resolves the params' cutover date: the default when
// empty, else RFC3339 or the HEAD error.
func purgeLegacyFPCutover(s string) (time.Time, error) {
	if s == "" {
		return purgeLegacyFPDefaultCutover, nil
	}
	cutover, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cutover_date %q: must be RFC3339 (e.g. 2026-05-12T00:00:00Z): %w", s, err)
	}
	return cutover, nil
}

// purgeLegacyFPCore is the body of runPurgeLegacyFP, shared by both shells.
func purgeLegacyFPCore(ctx context.Context, env purgeLegacyFPEnv) (purgeLegacyFPResult, error) {
	cutover := env.Cutover
	env.Logger.Info("purge-legacy-fp-candidates start",
		"apply", env.Live,
		"cutover", cutover.Format(time.RFC3339))

	// --- Guard: skip if already completed with apply=true ---
	if env.Live {
		if done, err := isFlagSetStore(env.Store, purgeLegacyFPDoneFlag); err != nil {
			env.Logger.Warn("purge-legacy-fp: flag check error (proceeding)", "error", err)
		} else if done {
			env.Logger.Info("purge-legacy-fp: already completed; skipping (flag set)",
				"flag", purgeLegacyFPDoneFlag)
			env.Progress(1, 1, "Already completed (flag set); nothing to do.")
			return purgeLegacyFPResult{AlreadyDone: true}, nil
		}
	}

	// --- Build file-hash lookup from current BookFiles ---
	// We index by book ID → set of non-empty hashes so we can re-check whether
	// either side of a candidate still has an alive file with a matching hash.
	env.Progress(0, 3, "Loading current book files for hash re-check…")
	filesByBookID, err := buildFileHashIndexVia(env.Store)
	if err != nil {
		return purgeLegacyFPResult{}, fmt.Errorf("load book files: %w", err)
	}
	env.Logger.Info("purge-legacy-fp: loaded book file hash index", "books", len(filesByBookID))

	// --- Walk all candidates ---
	env.Progress(1, 3, "Scanning candidates…")
	filter := database.CandidateFilter{
		Status: "pending",
		Limit:  1000000, // large upper bound — we filter below
	}
	candidates, _, err := env.Store.ListCandidates(filter)
	if err != nil {
		return purgeLegacyFPResult{}, fmt.Errorf("list candidates: %w", err)
	}

	var (
		examined     int
		staleCount   int
		keptHash     int
		keptLayer    int
		keptCutover  int
		keptManual   int
		keptSamePath int
	)

	// Collect the rows that pass every per-row gate in one pass, so we don't
	// interleave reads and writes. The same-path gate runs after, on this
	// set only, because it needs the books' paths.
	//
	// This scan stays a plain loop, as HEAD ran it: every gate is an in-memory
	// comparison or a map lookup against the indexes built above (the only store
	// reads are the two bulk loads), so there is no per-item DB, network or
	// hashing work for a pool to overlap.
	var stale []database.DedupCandidate

	for _, cand := range candidates {
		if env.Canceled() {
			return purgeLegacyFPResult{}, context.Canceled
		}
		select {
		case <-ctx.Done():
			return purgeLegacyFPResult{}, ctx.Err()
		default:
		}

		examined++

		// --- Criteria gate ---

		// 0. A manual (human-pinned) candidate keeps its scanner layer and
		//    similarity, so a pinned legacy row passes every gate below. It
		//    is review-queue-only and stays pending. The apply loop re-checks
		//    at write time too (ReclassifyCandidate), for a pin that lands
		//    between this scan and the write.
		if database.IsManualCandidate(cand) {
			keptManual++
			continue
		}

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
		if hasFileHashMatchIn(cand.EntityAID, cand.EntityBID, filesByBookID) {
			keptHash++
			env.Logger.Debug("purge-legacy-fp: keeping genuine hash-dupe",
				"candidate_id", cand.ID,
				"entity_a", cand.EntityAID,
				"entity_b", cand.EntityBID)
			continue
		}

		stale = append(stale, cand)
	}

	// 5. A same-path pair (two book rows at one cleaned file path) is
	//    review-queue-only by owner decision (dedup.AutomatedResolutionRefusal),
	//    whatever its layer or similarity.
	var toMark []int64
	if len(stale) > 0 {
		need := make(map[string]struct{}, len(stale)*2)
		for _, c := range stale {
			need[c.EntityAID] = struct{}{}
			need[c.EntityBID] = struct{}{}
		}
		books, err := loadBooksForPathCheckVia(ctx, env.kindEnv, env.Store, need)
		if err != nil {
			return purgeLegacyFPResult{}, fmt.Errorf("load books for the same-path check: %w", err)
		}
		for _, c := range stale {
			if _, refused := dedupengine.AutomatedResolutionRefusal(c, books[c.EntityAID], books[c.EntityBID]); refused {
				keptSamePath++
				continue
			}
			toMark = append(toMark, c.ID)
		}
	}
	staleCount = len(toMark)

	env.Logger.Info("purge-legacy-fp: scan complete",
		"examined", examined,
		"stale", staleCount,
		"kept_genuine_hash", keptHash,
		"kept_wrong_layer", keptLayer,
		"kept_post_cutover", keptCutover,
		"kept_manual", keptManual,
		"kept_same_path", keptSamePath,
		"apply", env.Live)

	res := purgeLegacyFPResult{Examined: examined, Stale: staleCount, KeptHash: keptHash, KeptLayer: keptLayer,
		KeptCutover: keptCutover, KeptManual: keptManual, KeptSamePath: keptSamePath}

	summary := fmt.Sprintf(
		"Scan: %d examined, %d stale-fp, %d kept (genuine hash), %d kept (other layer), %d kept (post-cutover), %d kept (manual), %d kept (same path)",
		examined, staleCount, keptHash, keptLayer, keptCutover, keptManual, keptSamePath,
	)

	if !env.Live {
		env.Progress(3, 3,
			fmt.Sprintf("Dry-run complete — %d candidates would be marked stale-fp. Pass apply=true to execute.", staleCount))
		env.Logger.Info("purge-legacy-fp: dry-run only; no changes written", "would_mark", staleCount)
		return res, nil
	}

	// --- Apply: mark stale rows ---
	// The mark loop stays sequential, as HEAD ran it: each flip is a guarded
	// point write (ReclassifyCandidate takes the manual-pin lock and re-checks
	// the row), the loop is ordered with the completion flag below, and a
	// cancel must leave a prefix marked. (A pool is a candidate follow-up; the
	// rows are disjoint by candidate id. See the F5-04 report.)
	env.Progress(2, 3, fmt.Sprintf("Marking %d candidates stale-fp…", len(toMark)))

	var marked, skippedAtWrite int
	for _, id := range toMark {
		if env.Canceled() {
			break
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		default:
		}
		// ReclassifyCandidate refuses a row a human pinned or decided since
		// the scan read it (see its doc); those are counted, not errors.
		err := env.Writer.ReclassifyCandidate(id, "pending", "stale-fp")
		switch {
		case err == nil:
			marked++
		case errors.Is(err, database.ErrManualCandidateProtected), errors.Is(err, database.ErrCandidateStatusChanged):
			skippedAtWrite++
			env.Logger.Info("purge-legacy-fp: left a row changed since the scan", "candidate_id", id, "reason", err)
		default:
			env.Logger.Error("purge-legacy-fp: mark stale-fp error", "candidate_id", id, "error", err)
			// Continue — partial progress is better than aborting; the op is idempotent.
		}
	}
	res.Marked, res.SkippedAtWrite = marked, skippedAtWrite

	// --- Set versioned completion flag ---
	if err := env.Writer.SetSetting(purgeLegacyFPDoneFlag, "true", "bool", false); err != nil {
		env.Logger.Warn("purge-legacy-fp: could not set done flag", "flag", purgeLegacyFPDoneFlag, "error", err)
	} else {
		env.Logger.Info("purge-legacy-fp: set done flag", "flag", purgeLegacyFPDoneFlag)
	}

	env.Progress(3, 3,
		fmt.Sprintf("Complete — %d/%d marked stale-fp (%d changed since the scan, left alone). %s", marked, staleCount, skippedAtWrite, summary))
	env.Logger.Info("purge-legacy-fp: complete", "marked", marked, "intended", staleCount, "skipped_at_write", skippedAtWrite)
	return res, nil
}

// runPurgeLegacyFP is the v2 shell: HEAD's Run (nil checks, params) over
// purgeLegacyFPCore.
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
	cutover, err := purgeLegacyFPCutover(params.CutoverDate)
	if err != nil {
		return err
	}
	h := headPurgeLegacyFP{emb: p.embeddingStore, store: p.store}
	_, err = purgeLegacyFPCore(ctx, purgeLegacyFPEnv{kindEnv: headEnv(reporter, params.Apply), Store: h, Writer: h, Cutover: cutover})
	return err
}

// purgeLegacyFPBookPage is the GetAllBooksCore page size for the same-path
// check's bulk path load.
const purgeLegacyFPBookPage = 5000

// loadBooksForPathCheckVia returns the books in need, keyed by ID, carrying only
// what dedup.SamePathPair reads (ID and FilePath). It pages GetAllBooksCore
// once rather than issuing one GetBookByID per candidate; an ID the bulk read
// did not return (a memdb gap, or a book deleted since) falls back to one
// GetBookByID, and a book that cannot be loaded at all stays absent, which
// SamePathPair treats as "not same-path", as the other guards do.
func loadBooksForPathCheckVia(ctx context.Context, env kindEnv, store purgeLegacyFPStore, need map[string]struct{}) (map[string]*database.Book, error) {
	out := make(map[string]*database.Book, len(need))
	for offset := 0; ; offset += purgeLegacyFPBookPage {
		if env.Canceled() {
			return nil, context.Canceled
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := store.GetAllBooksCore(purgeLegacyFPBookPage, offset)
		if err != nil {
			return nil, fmt.Errorf("get all books at offset %d: %w", offset, err)
		}
		for i := range page {
			if _, ok := need[page[i].ID]; ok {
				out[page[i].ID] = &database.Book{ID: page[i].ID, FilePath: page[i].FilePath}
			}
		}
		if len(page) < purgeLegacyFPBookPage || len(out) == len(need) {
			break
		}
	}
	for id := range need {
		if _, ok := out[id]; ok {
			continue
		}
		b, err := store.GetBookByID(id)
		if err == nil && b != nil {
			out[id] = b
		}
	}
	return out, nil
}

// buildFileHashIndexVia returns a map from bookID → set of non-empty file hashes
// for all current BookFiles. This is used to re-verify whether a candidate pair
// still has a genuine whole-file hash match before marking it stale.
func buildFileHashIndexVia(store purgeLegacyFPStore) (map[string]map[string]struct{}, error) {
	files, err := store.GetAllBookFilesCore()
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

// hasFileHashMatchIn returns true when at least one file hash is shared between
// any file belonging to bookAID and any file belonging to bookBID. A true
// result means the candidate is a genuine hash-based exact duplicate and should
// NOT be marked stale.
func hasFileHashMatchIn(bookAID, bookBID string, idx map[string]map[string]struct{}) bool {
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

// isFlagSet checks whether the named Settings key holds "true". Other defs of
// this package (drain-stale, emb-reencode) call it.
func (p *Plugin) isFlagSet(key string) (bool, error) {
	return isFlagSetStore(p.store, key)
}
