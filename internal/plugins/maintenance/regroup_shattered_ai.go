// file: internal/plugins/maintenance/regroup_shattered_ai.go
// version: 1.12.0
// guid: 8b3e6d21-4f97-4c05-a1d8-2e7b9c0f5a63
// last-edited: 2026-10-18

// Package maintenance — op maintenance.regroup-shattered-ai (PR-B1).
//
// The FIRST producer into the universal review queue (PR-A1). It enumerates the
// whole library, groups the thousands of single-file "books" left by the broken
// iTunes import (one track = one book) by their book folder, classifies each
// folder's shape with a deterministic regex tier (NO AI — that's a deferred
// fast-follow), and writes ONE review-queue HOLD per candidate folder.
//
// It NEVER changes a book or a book file (locked decision #5). Even confident
// multi-disc collapses are written as `pending` holds, never auto-applied — the
// apply path (CombineBooks/version-group writes) is PR-B2. The only writes this op
// makes are review-queue rows: UpsertReviewItem, which is idempotent on its stable
// DedupKey so re-running never duplicates a hold or resurfaces a human-decided one,
// and DeleteReviewItem for superseded PENDING holds. Both happen only when
// dry_run=false: a run that states no mode (the default) is a PREVIEW that computes
// the same candidates and reports what it would upsert and delete, calling neither.
//
// REAL-WORLD YIELD IS UNVERIFIED. The prior tag-anchored heal (fs_regroup_xml.go)
// healed 0 of the ~44,327 shattered records that remain, which means those records
// likely do NOT have the classic `<prefix> - N` chapter shape this classifier's
// chapter branch (and the old regex) recognizes. The broadened branches (flat
// multi-track, disc sets, edition folders) target the other shapes and group on the
// REAL BookFile path rather than the virtual Book.FilePath, but they have only been
// validated against synthetic fixtures. The op is itself the intended acceptance
// gate: a read-only whole-library dry-run on prod is required to confirm the queue
// fills with real holds and the classification kinds are sane before any apply
// toggle is enabled (see docs/plans/2026-07-13-review-queue-and-regroup.md).

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// regroupParams configures the regroup scan. The op never changes books or files
// (decision #5); what dry_run gates is the review-queue rows it writes and the
// stale pending holds it deletes.
type regroupParams struct {
	// Limit caps how many detected groups get a review hold this run (0 = no cap).
	// Useful for a first canary run before writing the full queue.
	Limit int `json:"limit"`
	// DryRun (dry_run) and DryRunCamel (dryRun) are one switch, resolved by
	// opmode.ResolveDryRun: omitted means PREVIEW (no review row is written or
	// deleted), false writes the holds, and the two spellings disagreeing is an
	// error.
	DryRun      *bool `json:"dry_run,omitempty"`
	DryRunCamel *bool `json:"dryRun,omitempty"`
}

// regroupPayload is the JSON stored on each review-queue hold. The A1 frontend
// renders it; keep the field names stable.
type regroupPayload struct {
	Folder         string   `json:"folder"`
	Files          []string `json:"files"`
	ProposedAction string   `json:"proposedAction"`
	MemberBookIDs  []string `json:"memberBookIDs"`
	SurvivorTitle  string   `json:"survivorTitle"`
	Confidence     string   `json:"confidence"` // "high" (confident) | "review"

	// DiscNumbers / TrackNumbers are PARALLEL to Files (same index = same member) and
	// carry the play-order the apply path (ApplyMultidisc) writes onto the merged
	// book's BookFile rows. They are set only for confident multidisc collapses; other
	// kinds leave them nil. omitempty + the apply path's length guard keep holds
	// written before this field existed working unchanged (they just skip the
	// disc/track write). A DiscNumber of 0 means "sequential chapters on one disc — no
	// disc concept", distinct from a real "Disc N" set (see assignDiscTrack).
	DiscNumbers  []int `json:"discNumbers,omitempty"`
	TrackNumbers []int `json:"trackNumbers,omitempty"`

	// RecommendedAction is what the classifier thinks a human should DO about this
	// hold — one of the itunesservice.Action* strings. It is what the approve handler
	// DISPATCHES on (owner item 2), so it is the most load-bearing field here.
	//
	// 🔴 ADDITIVE, AND DELIBERATELY NOT BACKFILLED. Every hold already sitting in
	// prod's queue was written before this field existed and decodes with
	// RecommendedAction == "". That empty string is the intended "old hold" tell:
	// the approve handler treats it as ActionInsufficientEvidence, which has no apply
	// handler, so an old hold can never dispatch to a merge on the strength of a
	// field it does not carry. Refreshing them is a re-scan (UpsertReviewItem rewrites
	// the payload of a still-pending hold), never a migration.
	RecommendedAction string `json:"recommendedAction"`
	// RecommendationReason is the one-sentence human-readable case for the action,
	// quoting its own numbers. It replaces ProposedAction as the sentence a reviewer
	// actually reads — 762 of 777 holds carried the same generic ProposedAction.
	RecommendationReason string `json:"recommendationReason"`
	// RecommendationEvidence is the arithmetic behind the reason so a reviewer can
	// check it instead of trusting it. A value struct, not a pointer: an old hold
	// decodes it as the zero value, which reads honestly as "no evidence recorded".
	RecommendationEvidence itunesservice.RecommendationEvidence `json:"recommendationEvidence"`
}

func (p *Plugin) regroupShatteredAIDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.regroup-shattered-ai",
		Preview:     sdk.PreviewSpec{Params: regroupParams{}},
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "maintenance",
		DisplayName: "Regroup shattered books (review-queue producer)",
		Description: "Scans the whole library, groups the single-file books left by the broken iTunes import " +
			"(one track = one book) by their book folder, classifies each folder's shape with a deterministic " +
			"regex tier (multi-disc, version group, anthology, or ambiguous — NO AI), and writes one review-queue " +
			"HOLD per candidate folder. Never changes a book or file; the only writes are review-queue rows (upserts " +
			"are idempotent) and deletes of superseded pending holds. dry_run defaults to true: a run that states " +
			"no mode computes the candidates and reports what it would upsert and delete, writing nothing. Pass " +
			"dry_run=false to write the holds. The apply path is a later PR.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.regroup-shattered-ai",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         120 * time.Minute,
		// Never writes books or files; with dry_run=false it writes review-queue
		// rows (upsert and delete), which are library data, hence CapLibraryWrite.
		// The book/file apply path is PR-B2.
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:          p.runRegroupShatteredAI,
	}
}

func (p *Plugin) runRegroupShatteredAI(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params := regroupParams{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	dryRun, err := opmode.ResolveDryRun("maintenance.regroup-shattered-ai", params.DryRun, params.DryRunCamel)
	if err != nil {
		return err
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	if dryRun {
		_ = reporter.Log(slog.LevelInfo, "PREVIEW (dry_run) — no review hold is written or deleted; pass dry_run=false to write them")
	}

	_ = reporter.UpdateProgress(0, 0, "Phase 1/2: enumerating library…")
	ids, err := store.ListBookIDs()
	if err != nil {
		return fmt.Errorf("ListBookIDs: %w", err)
	}

	// CONCURRENCY (CLAUDE.md mandate): a plain `for range books` full-library scan is
	// forbidden — a serial dedup.full-scan went silent for 3+ hours at 100% CPU on a
	// single core on 2026-07-05 (docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md).
	// Fan the read-only per-book scan out across NumCPU workers via registry.RunItems
	// (the memdb-cap-safe ListBookIDs + RunItems pattern). Grouping, classification, and
	// the review-row writes run single-threaded AFTER the scan, so there are no write
	// races — the only shared state here is the slim-view slice, guarded by mu.
	var mu sync.Mutex
	// skippedFrozen counts books excluded because they live in the hands-off
	// books/itunes/** tree; reported so an operator can see the queue shrank by
	// policy rather than by a silent bug.
	var skippedFrozen atomic.Int64
	views := make([]itunesservice.ShatterBook, 0, len(ids))
	scanErr := registry.RunItems(ctx, reporter, ids, func(ctx context.Context, id string) error {
		b, err := store.GetBookByID(id)
		if err != nil || b == nil {
			return nil // non-fatal: skip a book that vanished mid-scan
		}
		files, ferr := store.GetBookFiles(id)
		if ferr != nil {
			return nil // non-fatal: skip on read error
		}
		// Runtime is what tells a SERIES apart from a CHAPTER SET — six two-hour
		// files are six books, six three-minute files are six chapters. Summed from
		// the BookFile rows and normalised per row, because ~1.9% of historical rows
		// stored milliseconds and a 1000x-inflated member would look book-length to
		// the guard that depends on this.
		durationSec := 0
		for i := range files {
			durationSec += database.NormalizeDurationSec(files[i].FileSize, files[i].Duration)
		}

		v := itunesservice.ShatterBook{
			BookID:      b.ID,
			Title:       b.Title,
			FileCount:   len(files),
			IsPrimary:   b.IsPrimaryVersion == nil || *b.IsPrimaryVersion,
			DurationSec: durationSec,
		}
		// Prefer the real BookFile path (more reliable than the virtual Book.FilePath);
		// fall back to Book.FilePath for zero-file virtual shells.
		if len(files) >= 1 && files[0].FilePath != "" {
			v.FilePath = files[0].FilePath
		} else {
			v.FilePath = b.FilePath
		}
		// Fold in the ORIGINAL iTunes album-folder path as an ADDITIONAL grouping
		// signal when present (Bug 2). These shattered books are largely an
		// iTunes-import artifact, so the original album folder is often a stronger
		// identity signal than the reorganized FilePath. Empty for non-iTunes books.
		if len(files) >= 1 {
			v.ITunesPath = files[0].ITunesPath
		}
		// 🔴 SKIP THE FROZEN iTunes TREE. books/itunes/** is the externally-managed
		// Original library, marked Frozen and read-only — we are never permitted to
		// reorganise it. Proposing regroups there is worse than useless:
		//
		//   - 561 of 777 ambiguous holds (72%) were iTunes AUTHOR folders
		//     (`iTunes Media/Audiobooks/<Author>/`), because that layout puts an
		//     author's whole catalogue in one directory and the classifier reads a
		//     shared folder as a shared book; and
		//   - approving one would propose merging distinct novels into a single book
		//     inside a tree we must not touch.
		//
		// A proposal we cannot carry out is noise in a human's queue at best, and an
		// invitation to destroy a series at worst. Excluded at the SOURCE so no
		// downstream classifier heuristic has to re-derive the policy.
		if pathutil.UnderFrozenITunesTree(v.FilePath) || pathutil.UnderFrozenITunesTree(v.ITunesPath) {
			skippedFrozen.Add(1)
			return nil
		}

		mu.Lock()
		views = append(views, v)
		mu.Unlock()
		return nil
	}, registry.RunItemsOptions{
		Concurrency:   runtime.NumCPU(),
		ProgressTotal: len(ids),
		ErrMode:       registry.ErrModeCollect,
		Label: func(i, total int) string {
			return fmt.Sprintf("Phase 1/2: scanning book %d/%d…", i+1, total)
		},
	})
	if scanErr != nil {
		return fmt.Errorf("library scan: %w", scanErr)
	}

	_ = reporter.UpdateProgress(len(ids), len(ids)+1, "Phase 2/2: classifying folders + writing review holds…")
	groups, st := itunesservice.ClassifyShatteredFolders(views)

	// RECONCILE: account for EVERY book so the record count ties out vs the library
	// (mirror fs_regroup_xml.go — no silent filtering). scan-skipped = books whose
	// GetBookByID/GetBookFiles read failed or that vanished mid-scan; surfacing it
	// makes st.TotalBooks + scan-skipped tie back to len(ids).
	scanSkipped := len(ids) - len(views)
	recon := fmt.Sprintf(
		"RECONCILE: library=%d scan-skipped=%d total=%d non-primary=%d multi-file=%d no-path=%d singletons=%d distinct-skipped=%d groups=%d bykind=%v",
		len(ids), scanSkipped, st.TotalBooks, st.NonPrimary, st.MultiFile, st.NoPath, st.Singletons, st.DistinctSkip, st.Groups, st.ByKind)
	_ = reporter.Log(slog.LevelInfo, recon)

	// Write one idempotent review hold per detected group. ZERO book/file writes.
	// A preview builds every payload and item exactly as a live run does (so a
	// marshal failure shows up in a preview too) but never calls UpsertReviewItem.
	written, alreadyDecided, writeErrs := 0, 0, 0
	wouldUpsert := 0
	var sampleUpserts []string
	// A preview counts only the holds a live run would change. UpsertReviewItem
	// is a full no-op on a hold a human already decided (approved, rejected,
	// applied), so those are reported as already-decided, not as would-upsert.
	var decided map[string]struct{}
	if dryRun {
		decided = decidedRegroupHolds(store, reporter)
	}
	for i := range groups {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if params.Limit > 0 && i >= params.Limit {
			break
		}
		g := groups[i]
		payload, perr := buildRegroupPayload(g)
		if perr != nil {
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("marshal payload for %q: %v", g.FolderRef, perr))
			writeErrs++
			continue
		}
		item := database.ReviewItem{
			Kind:      g.Kind,
			DedupKey:  regroupDedupKey(g.Kind, g.FolderRef),
			FolderRef: g.FolderRef,
			Summary:   regroupSummary(g),
			Payload:   payload,
		}
		if dryRun {
			if _, ok := decided[item.DedupKey]; ok {
				alreadyDecided++
				continue
			}
			wouldUpsert++
			if len(sampleUpserts) < regroupPreviewSamples {
				sampleUpserts = append(sampleUpserts, fmt.Sprintf("%s %q", item.Kind, item.FolderRef))
			}
			continue
		}
		res, uerr := store.UpsertReviewItem(item)
		if uerr != nil {
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("UpsertReviewItem %q: %v", g.FolderRef, uerr))
			writeErrs++
			continue
		}
		written++
		if res.Status != database.ReviewStatusPending {
			alreadyDecided++
		}
		if written%200 == 0 {
			_ = reporter.UpdateProgress(len(ids), len(ids)+1,
				fmt.Sprintf("Phase 2/2: wrote %d/%d review holds…", written, len(groups)))
		}
	}

	// RECONCILE-PURGE: delete PENDING regroup holds whose folder is no longer a
	// candidate this run — a folder that flipped Kind (leaving an orphan under the old
	// Kind's DedupKey) or one the tuned classifier stopped emitting entirely (e.g. an
	// author/collection folder that used to over-merge). UpsertReviewItem only ever
	// UPSERTs folders it emits, so without this a superseded hold lingers in the queue
	// forever. Only PENDING holds are purged — a human decision (approved/rejected/
	// applied) is always preserved. Skipped on a capped/canary run (Limit > 0), where
	// the emitted set is intentionally partial and would wrongly purge the remainder.
	var result string
	if dryRun {
		stale := staleRegroupHolds(store, reporter, groups, params.Limit)
		samples := make([]string, 0, regroupPreviewSamples)
		for i := range stale {
			if i >= regroupPreviewSamples {
				break
			}
			samples = append(samples, fmt.Sprintf("%s %q", stale[i].Kind, stale[i].FolderRef))
		}
		_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
			"PREVIEW: would upsert %d review holds (e.g. %s) and delete %d stale pending holds (e.g. %s); %d already decided, left as they are",
			wouldUpsert, strings.Join(sampleUpserts, "; "), len(stale), strings.Join(samples, "; "), alreadyDecided))
		result = fmt.Sprintf(
			"PREVIEW (dry_run, nothing written) — would upsert %d review holds and delete %d stale pending holds "+
				"(already-decided=%d left as they are, marshal-errors=%d, skipped-frozen-itunes=%d) from %d detected groups; library untouched. bykind=%v",
			wouldUpsert, len(stale), alreadyDecided, writeErrs, skippedFrozen.Load(), len(groups), st.ByKind)
	} else {
		purged := 0
		if holds, ok := database.AsCapability[reviewHoldStore](store); ok {
			purged = reconcileStaleHolds(ctx, holds, reporter, groups, params.Limit)
		} else {
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf(
				"reconcile: store %T cannot delete review holds conditionally; skipping the stale-hold purge", store))
		}
		result = fmt.Sprintf(
			"APPLY — review holds written=%d (already-decided=%d, write-errors=%d, stale-purged=%d, "+
				"skipped-frozen-itunes=%d) from %d detected groups; books and files untouched. bykind=%v",
			written, alreadyDecided, writeErrs, purged, skippedFrozen.Load(), len(groups), st.ByKind)
	}
	_ = reporter.Log(slog.LevelInfo, result)
	_ = reporter.UpdateProgress(len(ids)+1, len(ids)+1, result)
	if writeErrs > 0 {
		return fmt.Errorf("%d review-hold write errors during regroup (see op log)", writeErrs)
	}
	return nil
}

// reconcileScanLimit bounds the pending-holds fetch during reconcile. Comfortably
// exceeds any realistic regroup hold population (intentional holds only).
const reconcileScanLimit = 100_000

// reconcileStaleHolds deletes PENDING regroup.* holds whose DedupKey is not in the set
// this run emitted, returning the number purged. Returns 0 (a no-op) when limit > 0 (a
// canary run emits only a subset, so purging the rest would be wrong). It never touches
// non-regroup holds (another producer's rows) or human-decided holds: the list is a
// snapshot, so each delete re-checks the status under the store's review lock
// (DeleteReviewItemIfPending) and a hold decided or claimed for apply since the list
// is left alone and not counted. Sequential by design: the pending-hold count is
// bounded (intentional holds only), each delete is a cheap local Pebble write, and the
// delete and UpsertReviewItem share one mutex, so a worker pool would only add
// contention.
func reconcileStaleHolds(ctx context.Context, store reviewHoldStore, reporter sdk.Reporter, groups []itunesservice.RegroupGroup, limit int) int {
	stale := staleRegroupHolds(store, reporter, groups, limit)
	purged := 0
	for _, it := range stale {
		if ctx.Err() != nil {
			break
		}
		deleted, derr := store.DeleteReviewItemIfPending(it.ID)
		if derr != nil {
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("reconcile: delete stale hold %s (%s): %v", it.ID, it.FolderRef, derr))
			continue
		}
		if deleted {
			purged++
		}
	}
	_ = reporter.Log(slog.LevelInfo,
		fmt.Sprintf("RECONCILE-PURGE: deleted %d superseded pending regroup holds (folder no longer emitted)", purged))
	return purged
}

// regroupPreviewSamples bounds the example holds a preview names in its log.
const regroupPreviewSamples = 5

// staleRegroupHolds is the READ half of the reconcile: the PENDING regroup.* holds whose
// DedupKey is not in the set this run emitted. It deletes nothing, so a preview calls it
// to report what a live run would purge and reconcileStaleHolds deletes exactly this
// list. Nil when limit > 0 (a canary run emits a subset) or when the pending list cannot
// be read (logged).
func staleRegroupHolds(store reviewHoldLister, reporter sdk.Reporter, groups []itunesservice.RegroupGroup, limit int) []database.ReviewItem {
	if limit > 0 {
		return nil
	}
	emitted := make(map[string]struct{}, len(groups))
	for i := range groups {
		emitted[regroupDedupKey(groups[i].Kind, groups[i].FolderRef)] = struct{}{}
	}
	pending, _, err := store.ListReviewItems(database.ReviewFilter{
		Status: database.ReviewStatusPending,
		Limit:  reconcileScanLimit,
	})
	if err != nil {
		_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("reconcile: list pending review items: %v", err))
		return nil
	}
	var stale []database.ReviewItem
	for _, it := range pending {
		if !strings.HasPrefix(it.Kind, "regroup.") {
			continue // never purge another producer's holds
		}
		if _, ok := emitted[it.DedupKey]; ok {
			continue // still a candidate this run
		}
		stale = append(stale, it)
	}
	return stale
}

// decidedRegroupHolds returns the DedupKeys of regroup.* holds whose status is
// not pending: the ones UpsertReviewItem leaves untouched. One full scan of the
// review queue (the status index has no "not pending" scan, and a new decided
// status must not be missed). On a read error it logs and returns nil, so the
// preview counts every group as would-upsert, as it did before this check.
func decidedRegroupHolds(store reviewHoldLister, reporter sdk.Reporter) map[string]struct{} {
	items, total, err := store.ListReviewItems(database.ReviewFilter{Limit: reconcileScanLimit})
	if err != nil {
		_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("preview: list review items: %v; already-decided holds are counted as would-upsert", err))
		return nil
	}
	if total > len(items) {
		_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("preview: review queue has %d items, read %d; decided holds past the cap are counted as would-upsert", total, len(items)))
	}
	decided := map[string]struct{}{}
	for _, it := range items {
		if strings.HasPrefix(it.Kind, "regroup.") && it.Status != database.ReviewStatusPending {
			decided[it.DedupKey] = struct{}{}
		}
	}
	return decided
}

// regroupDedupKey is the STABLE upsert target: a hash of (Kind, FolderRef). Re-running
// the scan on unchanged data produces the same key, so UpsertReviewItem updates in
// place instead of duplicating. Known limitation: if a folder's Kind flips between
// runs (as data changes), the key changes and the old row is orphaned — acceptable for
// a dry-run producer; the frontend surfaces the current classification.
func regroupDedupKey(kind, folderRef string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + folderRef))
	return "regroup:" + hex.EncodeToString(sum[:16])
}

// regroupSummary renders the one-line human label for a hold.
func regroupSummary(g itunesservice.RegroupGroup) string {
	n := len(g.Members)
	switch g.Kind {
	case itunesservice.KindMultidisc:
		// Distinguish a genuine multi-DISC source from same-disc CHAPTERS — the owner's
		// original confusion. Both flatten to one continuous track list (discs don't
		// exist), so the count is always tracks (= files), never discs; the Structure
		// just tells the human where the files came from.
		if g.Structure == "disc" {
			return fmt.Sprintf("Multi-disc source: %d tracks → 1 book (flattened) — %s", n, g.FolderRef)
		}
		return fmt.Sprintf("Chapters: %d tracks → 1 book — %s", n, g.FolderRef)
	case itunesservice.KindVersionGroup:
		return fmt.Sprintf("Version group (Abridged + Unabridged): %d files — %s", n, g.FolderRef)
	case itunesservice.KindAnthology:
		// An anthology is ONE book (owner decision 2026-07-26) — approving combines its
		// files into a single audiobook. DistinctWorks is the story/chapter count within
		// that one book, shown for context.
		works := g.DistinctWorks
		if works <= 0 {
			works = n
		}
		return fmt.Sprintf("Anthology/collection: %d files (%d stories) → 1 book — %s", n, works, g.FolderRef)
	default:
		return fmt.Sprintf("Ambiguous folder (%d files) — needs review — %s", n, g.FolderRef)
	}
}

// buildRegroupPayload serializes a group into the review-hold JSON payload.
func buildRegroupPayload(g itunesservice.RegroupGroup) (string, error) {
	files := make([]string, 0, len(g.Members))
	ids := make([]string, 0, len(g.Members))
	discs := make([]int, 0, len(g.Members))
	tracks := make([]int, 0, len(g.Members))
	for _, m := range g.Members {
		files = append(files, m.FilePath)
		ids = append(ids, m.BookID)
		discs = append(discs, m.DiscNumber)
		tracks = append(tracks, m.TrackNumber)
	}
	confidence := "review"
	if g.Confident {
		confidence = "high"
	}
	// Disc/track are meaningful only for the COMBINE kinds — multidisc and anthology
	// both collapse into one ordered multi-file book (an anthology is a single book,
	// owner decision 2026-07-26), so both carry the classifier's chapter order.
	// Version-group and ambiguous leave them nil (all-zero noise the apply path skips).
	if g.Kind != itunesservice.KindMultidisc && g.Kind != itunesservice.KindAnthology {
		discs, tracks = nil, nil
	}
	data, err := json.Marshal(regroupPayload{
		Folder:         g.FolderRef,
		Files:          files,
		ProposedAction: g.ProposedAction,
		MemberBookIDs:  ids,
		SurvivorTitle:  g.SurvivorTitle,
		Confidence:     confidence,
		DiscNumbers:    discs,
		TrackNumbers:   tracks,
		// Carried verbatim from the classifier — this producer never re-derives or
		// second-guesses the recommendation. classifyGroup owns the rule (including
		// the KindVersionGroup override); duplicating any of it here would be a second
		// place for the vocabulary to drift.
		RecommendedAction:      g.RecommendedAction,
		RecommendationReason:   g.RecommendationReason,
		RecommendationEvidence: g.RecommendationEvidence,
	})
	if err != nil {
		return "", err
	}
	return string(data), nil
}
