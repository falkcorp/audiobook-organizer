// file: internal/plugins/maintenance/regroup_shattered_ai.go
// version: 1.0.0
// guid: 8b3e6d21-4f97-4c05-a1d8-2e7b9c0f5a63
// last-edited: 2026-07-13

// Package maintenance — op maintenance.regroup-shattered-ai (PR-B1).
//
// The FIRST producer into the universal review queue (PR-A1). It enumerates the
// whole library, groups the thousands of single-file "books" left by the broken
// iTunes import (one track = one book) by their book folder, classifies each
// folder's shape with a deterministic regex tier (NO AI — that's a deferred
// fast-follow), and writes ONE review-queue HOLD per candidate folder.
//
// This is DRY-RUN ONLY (locked decision #5): run #1 writes ZERO book/file changes.
// Even confident multi-disc collapses are written as `pending` holds, never
// auto-applied — the apply path (CombineBooks/version-group writes) is PR-B2. The
// only writes this op makes are review-queue rows via UpsertReviewItem, which is
// idempotent on its stable DedupKey so re-running never duplicates a hold or
// resurfaces a human-decided one.
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
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// regroupParams configures the dry-run regroup scan. There is no apply flag in B1 —
// this op is always a dry-run review-queue producer (decision #5).
type regroupParams struct {
	// Limit caps how many detected groups get a review hold this run (0 = no cap).
	// Useful for a first canary run before writing the full queue.
	Limit int `json:"limit"`
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
}

func (p *Plugin) regroupShatteredAIDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.regroup-shattered-ai",
		Plugin:      "maintenance",
		DisplayName: "Regroup shattered books (dry-run · review-queue producer)",
		Description: "Scans the whole library, groups the single-file books left by the broken iTunes import " +
			"(one track = one book) by their book folder, classifies each folder's shape with a deterministic " +
			"regex tier (multi-disc, version group, anthology, or ambiguous — NO AI), and writes one review-queue " +
			"HOLD per candidate folder. DRY-RUN ONLY: writes ZERO book/file changes; the only writes are review " +
			"rows (idempotent). The apply path is a later PR.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.regroup-shattered-ai",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         120 * time.Minute,
		// Read-only w.r.t. books/files in B1 (writes ONLY review-queue rows). No
		// CapLibraryWrite until the apply path lands (PR-B2).
		Capabilities: []sdk.Capability{sdk.CapLibraryRead},
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
	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
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
		v := itunesservice.ShatterBook{
			BookID:    b.ID,
			Title:     b.Title,
			FileCount: len(files),
			IsPrimary: b.IsPrimaryVersion == nil || *b.IsPrimaryVersion,
		}
		// Prefer the real BookFile path (more reliable than the virtual Book.FilePath);
		// fall back to Book.FilePath for zero-file virtual shells.
		if len(files) >= 1 && files[0].FilePath != "" {
			v.FilePath = files[0].FilePath
		} else {
			v.FilePath = b.FilePath
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
	written, alreadyDecided, writeErrs := 0, 0, 0
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

	result := fmt.Sprintf(
		"DRY RUN — review holds written=%d (already-decided=%d, write-errors=%d) from %d detected groups; library untouched. bykind=%v",
		written, alreadyDecided, writeErrs, len(groups), st.ByKind)
	_ = reporter.Log(slog.LevelInfo, result)
	_ = reporter.UpdateProgress(len(ids)+1, len(ids)+1, result)
	if writeErrs > 0 {
		return fmt.Errorf("%d review-hold write errors during regroup dry-run (see op log)", writeErrs)
	}
	return nil
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
		return fmt.Sprintf("Multi-disc: %d tracks → 1 book — %s", n, g.FolderRef)
	case itunesservice.KindVersionGroup:
		return fmt.Sprintf("Version group (Abridged + Unabridged): %d files — %s", n, g.FolderRef)
	case itunesservice.KindAnthology:
		return fmt.Sprintf("Anthology/collection: %d works — %s", n, g.FolderRef)
	default:
		return fmt.Sprintf("Ambiguous folder (%d files) — needs review — %s", n, g.FolderRef)
	}
}

// buildRegroupPayload serializes a group into the review-hold JSON payload.
func buildRegroupPayload(g itunesservice.RegroupGroup) (string, error) {
	files := make([]string, 0, len(g.Members))
	ids := make([]string, 0, len(g.Members))
	for _, m := range g.Members {
		files = append(files, m.FilePath)
		ids = append(ids, m.BookID)
	}
	confidence := "review"
	if g.Confident {
		confidence = "high"
	}
	data, err := json.Marshal(regroupPayload{
		Folder:         g.FolderRef,
		Files:          files,
		ProposedAction: g.ProposedAction,
		MemberBookIDs:  ids,
		SurvivorTitle:  g.SurvivorTitle,
		Confidence:     confidence,
	})
	if err != nil {
		return "", err
	}
	return string(data), nil
}
